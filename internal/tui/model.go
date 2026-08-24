package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mattwalters/lerp/internal/loop"
)

// Ticker is the loop as the TUI drives it. The TUI is the engine — it owns
// the cadence and calls Tick itself; there is no daemon behind it.
type Ticker interface {
	Tick(ctx context.Context)
}

// Options wires the shell to the loop.
type Options struct {
	Ticker   Ticker
	Interval time.Duration // tick cadence; loop.DefaultInterval when zero
	Lanes    int           // N, for the fixed lane rows
	Events   <-chan loop.Event
}

func (o Options) validate() error {
	switch {
	case o.Ticker == nil:
		return fmt.Errorf("tui: ticker is required")
	case o.Lanes < 1:
		return fmt.Errorf("tui: lanes must be at least 1")
	case o.Events == nil:
		return fmt.Errorf("tui: events channel is required")
	}
	return nil
}

// panel is one of the three side panels — SCOPE's three questions, all on
// screen at once. Focus decides where selection keys go and what lens the
// main pane shows.
type panel int

const (
	panelAttention panel = iota
	panelLanes
	panelNext
)

func (p panel) String() string {
	switch p {
	case panelAttention:
		return "attention"
	case panelLanes:
		return "lanes"
	default:
		return "up next"
	}
}

// laneState is the runner state a lane row shows.
type laneState int

const (
	laneIdle         laneState = iota // no agent; note says how the last run ended
	laneProvisioning                  // claimed; the workspace is being prepared
	laneRunning                       // an agent this process started
	laneAdopted                       // a live agent inherited from a previous process
)

// lane is one board row, maintained purely from loop events.
type lane struct {
	state    laneState
	runID    string
	ticketID string
	ticket   string // human identifier; empty for adopted runs
	queue    string
	logPath  string // survives the run, so the tail outlives the agent
	since    time.Time
	note     string // idle lanes: how the last occupant ended
}

const (
	// pollEvery is the redraw-and-tail cadence, independent of the loop's
	// ticks; it is also the animation clock for the heartbeat frames.
	pollEvery = 250 * time.Millisecond
	// narrowWidth is where the side-by-side layout gives up and the panels
	// stack above the main pane instead. One threshold, no second layout.
	narrowWidth = 100
)

// Messages. The tick chain is strictly sequential — tickMsg starts a pass,
// tickedMsg schedules the next timer — so passes never overlap no matter how
// long one blocks on Linear.
type (
	tickMsg    struct{}
	tickedMsg  struct{}
	eventMsg   struct{ ev loop.Event }
	pollMsg    struct{}
	openErrMsg struct{ err error }
)

// nextRef addresses one ticket in the queue snapshot: queue index, ticket
// index. The up-next selection walks these, skipping header rows.
type nextRef struct{ qi, ti int }

type model struct {
	o   Options
	ctx context.Context

	focus         panel
	width, height int
	ready         bool
	helpOn        bool

	lanes    map[int]*lane
	order    []int // lane numbers, sorted; adopted runs may sit above N
	selected int   // the selected lane's NUMBER, not its position (see reorder)

	// queues is the loop's latest queue snapshot, replaced wholesale on every
	// pass; nil until the first pass reports. It is display state only — the
	// up-next panel edits nothing (SCOPE: not a Linear client).
	queues  []loop.QueueSnapshot
	nextSel int // index into nextRefs()

	// attention is the loop's latest full list of what waits on the operator;
	// attentionSeen separates "no pass has reported yet" from the goal state,
	// so an empty panel never claims "nothing needs you" before it is known.
	attention     []loop.AttentionItem
	attentionSeen bool
	attnSel       int

	vp     viewport.Model
	tail   tail
	follow bool

	keys keymap
	help help.Model

	// The status bar's heartbeat: frame advances on every poll, inFlight
	// spans a pass from tickMsg to tickedMsg, lastPass is when the previous
	// pass finished (zero until one has).
	frame    int
	inFlight bool
	lastPass time.Time

	lastErr    string
	passHadErr bool // an error event arrived during the pass now in flight

	// passes counts in-flight reconciliation passes; Run waits on it after
	// the program exits, so quitting never severs a pass mid-mutation.
	passes *sync.WaitGroup
}

func newModel(ctx context.Context, o Options) model {
	if o.Interval <= 0 {
		o.Interval = loop.DefaultInterval
	}
	h := help.New()
	h.ShowAll = true
	m := model{o: o, ctx: ctx, focus: panelLanes, lanes: make(map[int]*lane),
		vp: viewport.New(0, 0), follow: true, keys: newKeymap(), help: h,
		inFlight: true, // Init starts the first pass immediately
		passes:   &sync.WaitGroup{}}
	for n := 1; n <= o.Lanes; n++ {
		m.lanes[n] = &lane{}
	}
	m.reorder()
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.runTick(), m.waitEvent(), poll())
}

// runTick runs one reconciliation pass off the render loop. The context is
// the loop's, not the program's: quitting the TUI never cancels a pass or
// kills an agent — Run waits (bounded) for the pass to finish instead. The
// WaitGroup is incremented here, on the render loop, so a quit racing the
// command's goroutine still sees the pass as in flight.
func (m model) runTick() tea.Cmd {
	m.passes.Add(1)
	return func() tea.Msg {
		defer m.passes.Done()
		m.o.Ticker.Tick(m.ctx)
		return tickedMsg{}
	}
}

func (m model) waitEvent() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.o.Events
		if !ok {
			return nil
		}
		return eventMsg{ev: ev}
	}
}

func poll() tea.Cmd {
	return tea.Tick(pollEvery, func(time.Time) tea.Msg { return pollMsg{} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		m.refreshMain()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tickMsg:
		m.passHadErr = false
		m.inFlight = true
		return m, m.runTick()
	case tickedMsg:
		// A pass that produced no error supersedes whatever error line an
		// earlier one left; lane-level failures stay visible as lane notes.
		if !m.passHadErr {
			m.lastErr = ""
		}
		m.inFlight = false
		m.lastPass = time.Now()
		return m, tea.Tick(m.o.Interval, func(time.Time) tea.Msg { return tickMsg{} })
	case eventMsg:
		m.apply(msg.ev)
		return m, m.waitEvent()
	case pollMsg:
		// The poll is also the clock: elapsed times and the heartbeat
		// re-render even when the log is quiet.
		m.frame++
		if m.tail.read() && m.focus == panelLanes {
			m.refreshLog()
		}
		return m, poll()
	case openErrMsg:
		m.lastErr = msg.err.Error()
		return m, nil
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.helpOn = !m.helpOn
	case key.Matches(msg, m.keys.Attention):
		m.setFocus(panelAttention)
	case key.Matches(msg, m.keys.Lanes):
		m.setFocus(panelLanes)
	case key.Matches(msg, m.keys.UpNext):
		m.setFocus(panelNext)
	case key.Matches(msg, m.keys.NextPanel):
		m.setFocus((m.focus + 1) % 3)
	case key.Matches(msg, m.keys.PrevPanel):
		m.setFocus((m.focus + 2) % 3)
	case key.Matches(msg, m.keys.Up):
		m.moveSelection(-1)
	case key.Matches(msg, m.keys.Down):
		m.moveSelection(1)
	case key.Matches(msg, m.keys.PageUp):
		m.vp.ViewUp()
		m.follow = m.vp.AtBottom()
	case key.Matches(msg, m.keys.PageDown):
		m.vp.ViewDown()
		m.follow = m.vp.AtBottom()
	case key.Matches(msg, m.keys.Top):
		m.vp.GotoTop()
		m.follow = false
	case key.Matches(msg, m.keys.Bottom):
		m.vp.GotoBottom()
		m.follow = true
	case key.Matches(msg, m.keys.Open):
		return m, openURL(m.selectedURL())
	}
	m.layout()
	return m, nil
}

func (m *model) setFocus(p panel) {
	m.focus = p
	m.refreshMain()
}

// moveSelection moves within the focused panel. The lane selection is by
// lane number (see reorder); the other two are plain indexes into lists the
// loop replaces wholesale.
func (m *model) moveSelection(delta int) {
	switch m.focus {
	case panelLanes:
		if i := slices.Index(m.order, m.selected); i >= 0 {
			if j := i + delta; j >= 0 && j < len(m.order) {
				m.selected = m.order[j]
				m.retarget()
			}
		}
	case panelAttention:
		m.attnSel = clampIndex(m.attnSel+delta, len(m.attention))
		m.refreshMain()
	case panelNext:
		m.nextSel = clampIndex(m.nextSel+delta, len(m.nextRefs()))
		m.refreshMain()
	}
}

func clampIndex(i, n int) int {
	return max(0, min(i, n-1))
}

// selectedURL is what `o` opens: Linear's own URL for the selected attention
// item or up-next ticket. Lanes have no URL — a running lane's door is its
// log, already on screen.
func (m *model) selectedURL() string {
	switch m.focus {
	case panelAttention:
		if i := clampIndex(m.attnSel, len(m.attention)); i < len(m.attention) {
			return m.attention[i].URL
		}
	case panelNext:
		if t := m.nextTicket(); t != nil {
			return t.URL
		}
	}
	return ""
}

// openURL hands the URL to the OS opener. This is the TUI opening the
// operator's browser, not lerp speaking to an API; acting on the ticket
// still happens in Linear.
func openURL(url string) tea.Cmd {
	if url == "" {
		return nil
	}
	return func() tea.Msg {
		var c *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			c = exec.Command("open", url)
		default:
			c = exec.Command("xdg-open", url)
		}
		if err := c.Start(); err != nil {
			return openErrMsg{err: fmt.Errorf("open %s: %w", url, err)}
		}
		go c.Wait()
		return nil
	}
}

// apply folds one loop event into the model.
func (m *model) apply(ev loop.Event) {
	if ev.Err != nil {
		m.lastErr = ev.Err.Error()
		m.passHadErr = true
	}
	switch ev.Type {
	case loop.EventProvisioning:
		m.lanes[ev.Lane] = &lane{state: laneProvisioning, runID: ev.RunID, ticketID: ev.TicketID,
			ticket: ev.Ticket, queue: ev.Queue, logPath: ev.LogPath, since: eventSince(ev)}
	case loop.EventStarted:
		m.lanes[ev.Lane] = &lane{state: laneRunning, runID: ev.RunID, ticketID: ev.TicketID,
			ticket: ev.Ticket, queue: ev.Queue, logPath: ev.LogPath, since: eventSince(ev)}
	case loop.EventAdopted:
		m.lanes[ev.Lane] = &lane{state: laneAdopted, runID: ev.RunID, ticketID: ev.TicketID,
			queue: ev.Queue, logPath: ev.LogPath, since: eventSince(ev)}
	case loop.EventExited:
		note := fmt.Sprintf("%s exited %d", ev.Ticket, ev.ExitCode)
		if ev.Err != nil {
			note += " (move failed)"
		}
		m.settle(ev, note)
	case loop.EventReaped:
		m.settle(ev, "reaped a dead run")
	case loop.EventError:
		if ev.Lane > 0 {
			m.settle(ev, "failed; see the log")
		}
	case loop.EventQueues:
		m.queues = ev.Queues
		m.nextSel = clampIndex(m.nextSel, len(m.nextRefs()))
	case loop.EventAttention:
		m.attention = ev.Attention
		m.attentionSeen = true
		m.attnSel = clampIndex(m.attnSel, len(m.attention))
	}
	m.reorder()
	m.layout()
	m.retarget()
	m.refreshMain()
}

// settle frees the lane a run just left. An in-range lane goes idle with a
// note; an out-of-range lane existed only for an adopted run, so its row
// disappears with it.
func (m *model) settle(ev loop.Event, note string) {
	ln := m.lanes[ev.Lane]
	if ln == nil {
		return
	}
	if ev.RunID != "" && ln.runID != "" && ln.runID != ev.RunID {
		return // a newer occupant already owns the lane
	}
	if ev.Lane > m.o.Lanes {
		delete(m.lanes, ev.Lane)
		return
	}
	logPath := ev.LogPath
	if logPath == "" {
		logPath = ln.logPath
	}
	m.lanes[ev.Lane] = &lane{state: laneIdle, note: note, logPath: logPath}
}

// eventSince prefers the loop's own start time, so an adopted run's elapsed
// clock shows the run's true age, not the moment this process learned of it.
func eventSince(ev loop.Event) time.Time {
	if !ev.StartedAt.IsZero() {
		return ev.StartedAt
	}
	return time.Now()
}

// reorder rebuilds the row order. The selection follows the lane's number,
// not its position: adopted rows appearing or vanishing above the selected
// lane must not silently move the selection — and with it the tail — to a
// different lane. Only when the selected row itself is gone does the
// selection fall back to the nearest remaining row.
func (m *model) reorder() {
	m.order = m.order[:0]
	for n := range m.lanes {
		m.order = append(m.order, n)
	}
	slices.Sort(m.order)
	if len(m.order) == 0 {
		return
	}
	if i, ok := slices.BinarySearch(m.order, m.selected); !ok {
		m.selected = m.order[min(i, len(m.order)-1)]
	}
}

// retarget points the tail at the selected lane's log. Reattaching only on a
// path change keeps the scrollback across renders and across the run's exit —
// the buffer is the operator's copy of a log whose file may already be gone.
func (m *model) retarget() {
	path := ""
	if ln := m.selectedLane(); ln != nil {
		path = ln.logPath
	}
	if path == m.tail.path {
		return
	}
	m.tail = newTail(path)
	m.follow = true
	m.tail.read()
	m.refreshLog()
}

// refreshMain points the main pane's viewport at whatever the focused panel
// selects: the log tail for lanes, a detail lens for the other two.
func (m *model) refreshMain() {
	switch m.focus {
	case panelLanes:
		m.refreshLog()
	case panelAttention:
		m.vp.SetContent(m.attentionDetail())
		m.vp.GotoTop()
	case panelNext:
		m.vp.SetContent(m.nextDetail())
		m.vp.GotoTop()
	}
}

func (m *model) refreshLog() {
	if m.focus != panelLanes {
		return
	}
	m.vp.SetContent(m.tail.content())
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) selectedLane() *lane {
	return m.lanes[m.selected]
}

// nextRefs flattens the queue snapshot into selectable tickets, in the
// loop's own pickup order.
func (m *model) nextRefs() []nextRef {
	var refs []nextRef
	for qi, q := range m.queues {
		for ti := range q.Tickets {
			refs = append(refs, nextRef{qi: qi, ti: ti})
		}
	}
	return refs
}

func (m *model) nextTicket() *loop.QueueTicket {
	refs := m.nextRefs()
	if len(refs) == 0 {
		return nil
	}
	r := refs[clampIndex(m.nextSel, len(refs))]
	return &m.queues[r.qi].Tickets[r.ti]
}

// geometry is the cockpit's arithmetic: side panels sized to their content,
// the main pane taking whatever is left, one narrow fallback that stacks
// everything. Heights include borders; panelBox truncates overflow itself.
type geometry struct {
	wide                 bool
	sideW, mainW         int
	bodyH                int
	attnH, lanesH, nextH int
	mainH                int
}

func (m *model) geometry() geometry {
	g := geometry{bodyH: max(4, m.height-1)}
	g.wide = m.width >= narrowWidth

	// The attention panel sizes to its list, one line minimum for its state
	// text, six maximum before it truncates — attention must never crowd out
	// the lanes below it.
	g.attnH = min(max(len(m.attention), 1), 6) + 2
	g.lanesH = len(m.order) + 2

	if g.wide {
		g.sideW = max(28, m.width/3)
		g.mainW = m.width - g.sideW
		g.mainH = g.bodyH
		g.nextH = max(4, g.bodyH-g.attnH-g.lanesH)
		return g
	}
	g.sideW = m.width
	g.mainW = m.width
	nextRows := 0
	for _, q := range m.queues {
		nextRows += 1 + len(q.Tickets)
	}
	g.nextH = min(max(nextRows, 1), 6) + 2
	g.mainH = max(5, g.bodyH-g.attnH-g.lanesH-g.nextH)
	return g
}

// layout sizes the main pane's viewport from the geometry.
func (m *model) layout() {
	if !m.ready {
		return
	}
	g := m.geometry()
	m.vp.Width = max(0, g.mainW-2)
	m.vp.Height = max(1, g.mainH-2)
	m.help.Width = m.vp.Width
}

func (m model) View() string {
	if !m.ready {
		return "starting lerp…\n"
	}
	if m.width < 24 || m.height < 8 {
		return "lerp — window too small\n"
	}
	g := m.geometry()
	side := lipgloss.JoinVertical(lipgloss.Left,
		m.attentionPanel(g.sideW, g.attnH),
		m.lanesPanel(g.sideW, g.lanesH),
		m.nextPanel(g.sideW, g.nextH))
	main := m.mainPanel(g.mainW, g.mainH)
	var body string
	if g.wide {
		body = lipgloss.JoinHorizontal(lipgloss.Top, side, main)
	} else {
		body = lipgloss.JoinVertical(lipgloss.Left, side, main)
	}
	return body + "\n" + m.statusBar()
}

// panelTitle renders "[n] name" with the focus accent when the panel has
// focus; extra is already-styled trailing decoration (a count, a fraction).
func panelTitle(n int, name string, focused bool, extra string) string {
	label := fmt.Sprintf("[%d] %s", n, name)
	if focused {
		label = styleTitleFocus.Render(label)
	} else {
		label = styleFaint.Render(label)
	}
	return label + extra
}

// marker renders the selection arrow for one row of a focused panel.
func marker(on bool) string {
	if on {
		return styleFocus.Render("▸ ")
	}
	return "  "
}

func (m model) attentionPanel(w, h int) string {
	focused := m.focus == panelAttention
	extra := ""
	if len(m.attention) > 0 {
		extra = styleAttention.Render(fmt.Sprintf(" ● %d", len(m.attention)))
	}
	var rows []string
	switch {
	case !m.attentionSeen:
		rows = []string{styleFaint.Render("reading the board…")}
	case len(m.attention) == 0:
		rows = []string{styleFaint.Render("nothing needs you")}
	default:
		for i, it := range m.attention {
			rows = append(rows, marker(focused && i == m.attnSel)+
				styleAttention.Render("● ")+styleTicket.Render(it.Ticket)+" "+it.Title)
		}
	}
	return panelBox(panelTitle(1, "attention", focused, extra), focused, w, h, rows)
}

func (m model) lanesPanel(w, h int) string {
	focused := m.focus == panelLanes
	busy := 0
	for _, ln := range m.lanes {
		if ln.state != laneIdle {
			busy++
		}
	}
	extra := styleFaint.Render(fmt.Sprintf(" · %d/%d busy", busy, m.o.Lanes))
	rows := make([]string, 0, len(m.order))
	for _, n := range m.order {
		rows = append(rows, m.laneRow(n, w-2))
	}
	return panelBox(panelTitle(2, "lanes", focused, extra), focused, w, h, rows)
}

// laneRow is one lane, elapsed clock right-aligned so it survives narrow
// panels; the state is a colored dot plus a label where color alone would
// be ambiguous (adopted, provisioning, idle).
func (m model) laneRow(number, width int) string {
	ln := m.lanes[number]
	var dot, desc, right string
	switch ln.state {
	case laneProvisioning:
		dot = styleProvisioning.Render(heartbeatFrames[m.frame%len(heartbeatFrames)])
		desc = styleProvisioning.Render("provisioning")
		right = styleFaint.Render(elapsed(ln.since))
	case laneRunning:
		dot = styleRunning.Render("●")
		desc = ln.queue
		right = styleFaint.Render(elapsed(ln.since))
	case laneAdopted:
		dot = styleAdopted.Render("●")
		desc = styleAdopted.Render("adopted") + styleFaint.Render(" · "+ln.queue)
		right = styleFaint.Render(elapsed(ln.since))
	default:
		state := "idle"
		if ln.note != "" {
			state += " — " + ln.note
		}
		dot = styleFaint.Render("○")
		desc = styleFaint.Render(state)
	}
	// Idle lanes carry no ticket worth a name column; the note says how the
	// last occupant ended.
	name := ""
	if ln.state != laneIdle {
		name = styleTicket.Render(ln.name()) + " "
	}
	left := fmt.Sprintf("%s%s %s %s%s",
		marker(number == m.selected), styleFaint.Render(fmt.Sprintf("%d", number)), dot, name, desc)
	leftMax := width - lipgloss.Width(right)
	if right != "" {
		leftMax--
	}
	left = ansi.Truncate(left, max(0, leftMax), "…")
	pad := strings.Repeat(" ", max(0, leftMax-lipgloss.Width(left)))
	if right == "" {
		return left
	}
	return left + pad + " " + right
}

func (m model) nextPanel(w, h int) string {
	focused := m.focus == panelNext
	if m.queues == nil {
		return panelBox(panelTitle(3, "up next", focused, ""), focused, w, h,
			[]string{styleFaint.Render("waiting for the first pass…")})
	}
	var rows []string
	sel := -1
	if refs := m.nextRefs(); len(refs) > 0 {
		sel = clampIndex(m.nextSel, len(refs))
	}
	idx := 0
	for _, q := range m.queues {
		meta := fmt.Sprintf(" %s · %s · %d", q.Status, q.Team, len(q.Tickets))
		if len(q.Tickets) == 0 {
			meta = fmt.Sprintf(" %s · %s · empty", q.Status, q.Team)
		}
		rows = append(rows, styleTicket.Render(q.Name)+styleFaint.Render(meta))
		for _, tk := range q.Tickets {
			row := marker(focused && idx == sel)
			if tk.Eligible {
				row += styleTicket.Render(tk.Identifier) + " " + tk.Title
			} else {
				row += styleFaint.Render(tk.Identifier + " " + tk.Title)
			}
			rows = append(rows, row)
			idx++
		}
	}
	return panelBox(panelTitle(3, "up next", focused, ""), focused, w, h, rows)
}

// mainPanel is the lens: the ? overlay when open, otherwise the log for
// lanes and a read-only detail for the other panels.
func (m model) mainPanel(w, h int) string {
	if m.helpOn {
		return panelBox(styleTitleFocus.Render("help"), true, w, h,
			strings.Split(m.help.View(m.keys), "\n"))
	}
	title := m.mainTitle()
	return panelBox(styleFaint.Render(title), false, w, h,
		strings.Split(m.vp.View(), "\n"))
}

func (m model) mainTitle() string {
	switch m.focus {
	case panelAttention:
		if i := clampIndex(m.attnSel, len(m.attention)); i < len(m.attention) {
			return m.attention[i].Ticket
		}
		return "attention"
	case panelNext:
		if t := m.nextTicket(); t != nil {
			return t.Identifier
		}
		return "up next"
	}
	ln := m.selectedLane()
	switch {
	case ln == nil:
		return "no lane selected"
	case ln.logPath == "":
		return "no log yet"
	case ln.state == laneIdle:
		return fmt.Sprintf("last log · lane %d", m.selected)
	default:
		return fmt.Sprintf("log · %s · %s · lane %d", ln.name(), ln.queue, m.selected)
	}
}

// attentionDetail is the main pane's lens on the selected attention item —
// everything the loop knows, plus Linear's URL, which is where acting on
// the item happens (SCOPE: lerp is not a Linear client).
func (m model) attentionDetail() string {
	if !m.attentionSeen {
		return styleFaint.Render("reading the board…")
	}
	if len(m.attention) == 0 {
		return styleFaint.Render("nothing needs you — the empty list is the goal state")
	}
	it := m.attention[clampIndex(m.attnSel, len(m.attention))]
	return strings.Join([]string{
		styleTicket.Render(it.Ticket) + " " + it.Title,
		"",
		styleFaint.Render("status  ") + it.Status,
		styleFaint.Render("why     ") + it.Reason,
		styleFaint.Render("linear  ") + it.URL,
		"",
		styleFaint.Render("o opens it in Linear; acting on it happens there"),
	}, "\n")
}

// nextDetail is the lens on the selected up-next ticket: where it sits in
// pickup order and what, if anything, gates it.
func (m model) nextDetail() string {
	if m.queues == nil {
		return styleFaint.Render("waiting for the first pass…")
	}
	refs := m.nextRefs()
	if len(refs) == 0 {
		return styleFaint.Render("every queue is empty")
	}
	r := refs[clampIndex(m.nextSel, len(refs))]
	q := m.queues[r.qi]
	tk := q.Tickets[r.ti]
	gate := styleRunning.Render(fmt.Sprintf("runs when a lane frees — position %d of %d", r.ti+1, len(q.Tickets)))
	switch {
	case len(tk.BlockedBy) > 0:
		gate = styleAttention.Render("blocked by " + strings.Join(tk.BlockedBy, ", "))
	case tk.Assigned:
		gate = styleFaint.Render("claimed — an assigned ticket is never picked up")
	}
	return strings.Join([]string{
		styleTicket.Render(tk.Identifier) + " " + tk.Title,
		"",
		styleFaint.Render("queue   ") + q.Name + styleFaint.Render(" · "+q.Status+" · team "+q.Team),
		styleFaint.Render("pickup  ") + gate,
		styleFaint.Render("linear  ") + tk.URL,
		"",
		styleFaint.Render("o opens it in Linear; to change what runs next, move it there"),
	}, "\n")
}

// statusBar is the heartbeat line: focused panel, pass clock, capacity,
// attention count, keys. A pass error takes over the middle — there is no
// permanently reserved error line.
func (m model) statusBar() string {
	badgeColor := colorFocus
	switch m.focus {
	case panelAttention:
		badgeColor = colorAttention
	case panelNext:
		badgeColor = colorAdopted
	}
	badge := lipgloss.NewStyle().Bold(true).Foreground(colorBadgeText).
		Background(badgeColor).Render(" " + strings.ToUpper(m.focus.String()) + " ")

	var heart string
	switch {
	case m.inFlight:
		heart = styleRunning.Render(heartbeatFrames[m.frame%len(heartbeatFrames)]) + " pass running…"
	case m.lastPass.IsZero():
		heart = styleFaint.Render("starting…")
	default:
		ago := time.Since(m.lastPass).Truncate(time.Second)
		next := max(0, m.o.Interval-time.Since(m.lastPass)).Truncate(time.Second)
		heart = styleFaint.Render(fmt.Sprintf("pass %s ago · next in %s", ago, next))
	}

	left := badge + " " + heart
	if m.lastErr != "" {
		left += "  " + styleErr.Render("✗ "+m.lastErr)
	} else {
		busy := 0
		for _, ln := range m.lanes {
			if ln.state != laneIdle {
				busy++
			}
		}
		left += "  " + styleFaint.Render(fmt.Sprintf("lanes %d/%d", busy, m.o.Lanes))
		if len(m.attention) > 0 {
			left += "  " + styleAttention.Render(fmt.Sprintf("● %d need you", len(m.attention)))
		}
	}
	right := styleFaint.Render("? help · q quit")

	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		left = ansi.Truncate(left, max(0, m.width-lipgloss.Width(right)-1), "…")
		pad = max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right))
	}
	return left + strings.Repeat(" ", pad) + right
}

func elapsed(since time.Time) string {
	return time.Since(since).Truncate(time.Second).String()
}

// name is the ticket column: the human identifier when the loop knows it, a
// shortened ticket ID for adopted runs, whose local records carry only the ID.
func (ln *lane) name() string {
	if ln.ticket != "" {
		return ln.ticket
	}
	if len(ln.ticketID) > 8 {
		return ln.ticketID[:8] + "…"
	}
	if ln.ticketID != "" {
		return ln.ticketID
	}
	return "—"
}
