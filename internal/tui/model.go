package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	Lanes    int           // N, for the board's fixed rows
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

// view is which of SCOPE's three views fills the body.
type view int

const (
	viewAttention view = iota
	viewBoard
	viewQueue
)

func (v view) String() string {
	switch v {
	case viewAttention:
		return "attention"
	case viewBoard:
		return "board"
	default:
		return "queue"
	}
}

// laneState is the runner state a board row shows.
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

// pollEvery is the redraw-and-tail cadence, independent of the loop's ticks.
const pollEvery = 250 * time.Millisecond

// Messages. The tick chain is strictly sequential — tickMsg starts a pass,
// tickedMsg schedules the next timer — so passes never overlap no matter how
// long one blocks on Linear.
type (
	tickMsg   struct{}
	tickedMsg struct{}
	eventMsg  struct{ ev loop.Event }
	pollMsg   struct{}
)

type model struct {
	o   Options
	ctx context.Context

	view          view
	width, height int
	ready         bool

	lanes    map[int]*lane
	order    []int // lane numbers, sorted; adopted runs may sit above N
	selected int   // the selected lane's NUMBER, not its position (see reorder)

	// queues is the loop's latest queue snapshot, replaced wholesale on every
	// pass; nil until the first pass reports. It is display state only — the
	// queue view edits nothing (SCOPE: not a Linear client).
	queues []loop.QueueSnapshot

	// attention is the loop's latest full list of what waits on the operator;
	// attentionSeen separates "no pass has reported yet" from the goal state,
	// so an empty screen never claims "nothing needs you" before it is known.
	attention     []loop.AttentionItem
	attentionSeen bool

	vp     viewport.Model
	tail   tail
	follow bool

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
	m := model{o: o, ctx: ctx, view: viewBoard, lanes: make(map[int]*lane),
		vp: viewport.New(0, 0), follow: true, passes: &sync.WaitGroup{}}
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
		m.refreshLog()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tickMsg:
		m.passHadErr = false
		return m, m.runTick()
	case tickedMsg:
		// A pass that produced no error supersedes whatever error line an
		// earlier one left; lane-level failures stay visible as lane notes.
		if !m.passHadErr {
			m.lastErr = ""
		}
		return m, tea.Tick(m.o.Interval, func(time.Time) tea.Msg { return tickMsg{} })
	case eventMsg:
		m.apply(msg.ev)
		return m, m.waitEvent()
	case pollMsg:
		// The poll is also the clock: elapsed times re-render even when the
		// log is quiet.
		if m.tail.read() {
			m.refreshLog()
		}
		return m, poll()
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "1":
		m.view = viewAttention
	case "2":
		m.view = viewBoard
	case "3":
		m.view = viewQueue
	case "tab":
		m.view = (m.view + 1) % 3
	case "up", "k":
		if m.view == viewBoard {
			if i := slices.Index(m.order, m.selected); i > 0 {
				m.selected = m.order[i-1]
				m.retarget()
			}
		}
	case "down", "j":
		if m.view == viewBoard {
			if i := slices.Index(m.order, m.selected); i >= 0 && i < len(m.order)-1 {
				m.selected = m.order[i+1]
				m.retarget()
			}
		}
	case "pgup", "b":
		m.vp.ViewUp()
		m.follow = m.vp.AtBottom()
	case "pgdown", "f":
		m.vp.ViewDown()
		m.follow = m.vp.AtBottom()
	case "home", "g":
		m.vp.GotoTop()
		m.follow = false
	case "end", "G":
		m.vp.GotoBottom()
		m.follow = true
	}
	m.layout()
	return m, nil
}

// apply folds one loop event into the board.
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
			m.settle(ev, "failed; see below")
		}
	case loop.EventQueues:
		m.queues = ev.Queues
	case loop.EventAttention:
		m.attention = ev.Attention
		m.attentionSeen = true
	}
	m.reorder()
	m.layout()
	m.retarget()
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

func (m *model) refreshLog() {
	m.vp.SetContent(m.tail.content())
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) selectedLane() *lane {
	return m.lanes[m.selected]
}

// layout gives the log pane whatever height the fixed chrome leaves over.
func (m *model) layout() {
	if !m.ready {
		return
	}
	m.vp.Width = m.width
	// header + lane rows + log title + error line + help line
	chrome := 1 + len(m.order) + 1 + 1 + 1
	m.vp.Height = max(3, m.height-chrome)
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	activeStyle   = lipgloss.NewStyle().Bold(true).Underline(true)
	inactiveStyle = lipgloss.NewStyle().Faint(true)
	selectedStyle = lipgloss.NewStyle().Bold(true)
	idleStyle     = lipgloss.NewStyle().Faint(true)
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	helpStyle     = lipgloss.NewStyle().Faint(true)
)

func (m model) View() string {
	if !m.ready {
		return "starting lerp…\n"
	}
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n")
	switch m.view {
	case viewAttention:
		b.WriteString(m.attentionList())
	case viewBoard:
		b.WriteString(m.board())
	case viewQueue:
		b.WriteString(m.queueView())
	}
	b.WriteString(m.footer())
	return b.String()
}

func (m model) header() string {
	tabs := make([]string, 0, 3)
	for v := viewAttention; v <= viewQueue; v++ {
		label := fmt.Sprintf("%d %s", int(v)+1, v)
		if v == m.view {
			tabs = append(tabs, activeStyle.Render(label))
		} else {
			tabs = append(tabs, inactiveStyle.Render(label))
		}
	}
	return titleStyle.Render("lerp") + "  " + strings.Join(tabs, "  ")
}

// attentionList renders the Attention view: every ticket the loop reported
// as blocked on the operator — the definition lives on the loop's attention
// pass — with the reason and Linear's URL for it, which most terminals make
// clickable. The view is deliberately dumb: it folds attention events and
// renders them; acting on an item happens in Linear, because lerp is not a
// Linear client. The empty state is the goal state.
func (m model) attentionList() string {
	if !m.attentionSeen {
		return inactiveStyle.Render("reading the board…") + "\n"
	}
	if len(m.attention) == 0 {
		return inactiveStyle.Render("nothing needs you") + "\n" +
			idleStyle.Render("(shows your claimed tickets sitting in statuses no queue serves)") + "\n"
	}
	var b strings.Builder
	for _, it := range m.attention {
		b.WriteString(fmt.Sprintf("%-9s %s\n", it.Ticket, it.Title))
		detail := it.Reason
		if it.URL != "" {
			detail += "  " + it.URL
		}
		b.WriteString(idleStyle.Render("          " + detail))
		b.WriteString("\n")
	}
	return b.String()
}

func (m model) board() string {
	var b strings.Builder
	for _, n := range m.order {
		b.WriteString(m.laneRow(n))
		b.WriteString("\n")
	}
	b.WriteString(m.logTitle())
	b.WriteString("\n")
	b.WriteString(m.vp.View())
	b.WriteString("\n")
	return b.String()
}

func (m model) laneRow(number int) string {
	ln := m.lanes[number]
	marker := "  "
	if number == m.selected {
		marker = "▸ "
	}
	row := fmt.Sprintf("%s%2d  %-12s %-12s %s", marker, number, ln.name(), ln.queueName(), ln.status())
	if number == m.selected {
		return selectedStyle.Render(row)
	}
	if ln.state == laneIdle {
		return idleStyle.Render(row)
	}
	return row
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

func (ln *lane) queueName() string {
	if ln.queue == "" {
		return "—"
	}
	return ln.queue
}

func (ln *lane) status() string {
	switch ln.state {
	case laneProvisioning:
		return "provisioning " + elapsed(ln.since)
	case laneRunning:
		return "running " + elapsed(ln.since)
	case laneAdopted:
		return "adopted " + elapsed(ln.since)
	default:
		if ln.note != "" {
			return "idle — " + ln.note
		}
		return "idle"
	}
}

func elapsed(since time.Time) string {
	return time.Since(since).Truncate(time.Second).String()
}

// queueView renders what runs next: each configured queue with every ticket
// sitting in its status, in the loop's own pickup order — the body is the
// loop's per-pass snapshot, verbatim. Eligible tickets run in listed order as
// lanes free up. Blocked and claimed tickets are shown faint rather than
// omitted: a ticket that silently vanished from its queue would look lost,
// when it is really just gated on a blocker or already being worked —
// possibly by a colleague's lerp. The view is read-only; to change what runs
// next, move tickets in Linear.
func (m model) queueView() string {
	if m.queues == nil {
		return inactiveStyle.Render("waiting for the first pass…") + "\n"
	}
	var lines []string
	for _, q := range m.queues {
		lines = append(lines, titleStyle.Render(q.Name)+
			inactiveStyle.Render(fmt.Sprintf("  %s · team %s", q.Status, q.Team)))
		if len(q.Tickets) == 0 {
			lines = append(lines, idleStyle.Render(fmt.Sprintf("    empty — tickets enter when moved to %q", q.Status)))
			continue
		}
		for _, tk := range q.Tickets {
			lines = append(lines, m.queueTicketRow(tk))
		}
	}
	// Cap to the body height the chrome leaves over (header above, error and
	// help lines below), so a deep backlog cannot push the footer off screen.
	if body := m.height - 3; len(lines) > body && body > 1 {
		over := len(lines) - (body - 1)
		lines = append(lines[:body-1], idleStyle.Render(fmt.Sprintf("    … %d more", over)))
	}
	return strings.Join(lines, "\n") + "\n"
}

// queueTicketRow renders one ticket: normal when the loop would pick it up,
// faint with the reason when it would not.
func (m model) queueTicketRow(tk loop.QueueTicket) string {
	row := fmt.Sprintf("    %-12s %s", tk.Identifier, tk.Title)
	if tk.Eligible {
		return truncate(row, m.width)
	}
	note := "claimed"
	if len(tk.BlockedBy) > 0 {
		note = "blocked by " + strings.Join(tk.BlockedBy, ", ")
	}
	return idleStyle.Render(truncate(row+"  — "+note, m.width))
}

func (m model) logTitle() string {
	label := "no lane selected"
	if ln := m.selectedLane(); ln != nil {
		switch {
		case ln.logPath == "":
			label = "no log yet"
		case ln.state == laneIdle:
			label = "last log of lane " + fmt.Sprint(m.selected)
		default:
			label = fmt.Sprintf("log: %s", ln.name())
		}
	}
	rule := "── " + label + " "
	if pad := m.width - lipgloss.Width(rule); pad > 0 {
		rule += strings.Repeat("─", pad)
	}
	return inactiveStyle.Render(rule)
}

func (m model) footer() string {
	errLine := ""
	if m.lastErr != "" {
		errLine = errStyle.Render(truncate(m.lastErr, max(0, m.width)))
	}
	help := helpStyle.Render("1/2/3 views · tab next · ↑/↓ lane · pgup/pgdn scroll · end follow · q quit")
	return errLine + "\n" + help
}

func truncate(s string, width int) string {
	if width == 0 || len(s) <= width {
		return s
	}
	if width <= 1 {
		return s[:width]
	}
	return s[:width-1] + "…"
}
