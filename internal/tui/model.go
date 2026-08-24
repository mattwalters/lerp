package tui

import (
	"cmp"
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

	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/loop"
)

// Ticker is the loop as the TUI drives it. The TUI is the engine — it owns
// the cadence and calls Tick itself; there is no daemon behind it.
type Ticker interface {
	Tick(ctx context.Context)
}

// Promoter is the TUI's one write action (SCOPE's promote amendment): moving
// a selected ticket into a queue. It is Reconciler.Promote in production; a
// plain MoveIssue through the same client the loop reads with.
type Promoter interface {
	Promote(ctx context.Context, ticketID, status string) error
}

// Reader is the needs-you pane's one read beyond the pass: the body and
// comments of the ticket the operator selected. It is Reconciler.IssueDetail
// in production. Read-only, one ticket at a time — SCOPE's "not a Linear
// client" bullet fences the rest, and `o` is the answer to everything it
// leaves out.
type Reader interface {
	IssueDetail(ctx context.Context, ticketID string) (linear.IssueDetail, error)
}

// Options wires the shell to the loop.
type Options struct {
	Ticker   Ticker
	Promoter Promoter
	Reader   Reader
	Statuses []string      // promote targets: configured queue statuses, plus the pipeline's exits
	Interval time.Duration // tick cadence; loop.DefaultInterval when zero
	Lanes    int           // N, for the fixed lane rows
	Events   <-chan loop.Event
}

func (o Options) validate() error {
	switch {
	case o.Ticker == nil:
		return fmt.Errorf("tui: ticker is required")
	case o.Promoter == nil:
		return fmt.Errorf("tui: promoter is required")
	case o.Reader == nil:
		return fmt.Errorf("tui: reader is required")
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
		return "needs you"
	case panelLanes:
		return "running"
	default:
		return "up next"
	}
}

// sortMode is the needs-you table's one control. Sorting is grouping: the
// mode picks the row order and, with it, whether the table draws headers —
// two flat modes for working a list top-down, two grouped ones for reading
// the board by the column they sort on. Deliberately not behind it: a sort
// builder, secondary sort keys, per-column toggles.
type sortMode int

const (
	sortLeverage sortMode = iota
	sortPriority
	sortStatus
	sortProject
	sortModes // the count, so one key can cycle them
)

func (s sortMode) String() string {
	switch s {
	case sortPriority:
		return "priority"
	case sortStatus:
		return "status"
	case sortProject:
		return "project"
	}
	return "leverage"
}

// grouped reports whether the mode draws a header above each run of rows.
func (s sortMode) grouped() bool { return s == sortStatus || s == sortProject }

// header is the group an item belongs to under this mode, and the short
// derived note that says why that group sits where it does. The flat modes
// return nothing and draw no headers.
func (s sortMode) header(it loop.AttentionItem) (string, string) {
	switch s {
	case sortStatus:
		return it.Status, it.Relevance.Note()
	case sortProject:
		if it.Project == "" {
			return "no project", ""
		}
		return it.Project, ""
	}
	return "", ""
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
	// detailDebounce is how long a needs-you selection must hold still
	// before its ticket is read. Trailing, so walking the list fires one
	// fetch — for the row the operator stopped on — instead of one per row.
	detailDebounce = 250 * time.Millisecond
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
	// promotedMsg reports the outcome of a promote action: MoveIssue on a
	// selected ticket, run off the render loop like every other write.
	promotedMsg struct {
		ticket string
		status string
		err    error
	}
	// detailDueMsg is the debounce firing for a ticket; detailMsg is the
	// read coming back.
	detailDueMsg struct{ ticketID string }
	detailMsg    struct {
		ticketID string
		detail   linear.IssueDetail
		err      error
	}
)

// detailState is how far the pane has got with one ticket's body and
// comments.
type detailState int

const (
	detailLoading detailState = iota
	detailReady
	detailFailed
)

// ticketDetail is one ticket as the pane holds it: already cleaned, because
// the fetch sanitizes on the way in.
type ticketDetail struct {
	state    detailState
	body     string
	comments []linear.Comment
	err      string
}

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
	// shown is that list under the current sort and filter — what the panel
	// actually renders, and what attnSel indexes.
	attention     []loop.AttentionItem
	attentionSeen bool
	shown         []loop.AttentionItem
	attnSel       int // index into shown; the promote target

	// sortMode and project are the table's two session-only controls: one
	// key cycles the order, another scopes the rows to a single Linear
	// project ("" is every project). Neither is saved anywhere — they are a
	// way to read one list the pass already fetched, not a view to keep.
	sortMode sortMode
	project  string

	// details is what the Reader has returned, keyed by ticket ID and kept
	// for the process's lifetime: a stale body is a view of Linear, not
	// state (invariant 1), so there is no eviction and no refresh key —
	// moving off the ticket and back is the refresh. detailWant is the
	// ticket the pane currently wants: the debounce's target, and what a
	// late reply is checked against.
	details    map[string]*ticketDetail
	detailWant string

	// promoting is the promote picker's open/closed state; promoteSel is its
	// selected index into o.Statuses. Opened by "p" on a selected needs-you
	// item, closed by confirming, cancelling, or the list going empty.
	promoting  bool
	promoteSel int

	vp     viewport.Model
	tail   tail
	follow bool
	// rawLog flips the lane pane between the decoded view and the runner's
	// own bytes. It is the same escape hatch as o and eject: when the
	// formatter is wrong, the operator needs to see what it was wrong about.
	rawLog bool

	keys keymap
	help help.Model

	// The status bar's heartbeat: frame advances on every poll, inFlight
	// spans a pass from tickMsg to tickedMsg, lastPass is when the previous
	// pass finished (zero until one has).
	frame    int
	inFlight bool
	lastPass time.Time

	lastErr      string
	lastInfo     string // transient note, e.g. a promote's outcome; cleared at the next pass
	lastInfoWarn bool   // the note reports something that went unhandled, not something that worked
	passHadErr   bool   // an error event arrived during the pass now in flight

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
		details: make(map[string]*ticketDetail),
		vp:      viewport.New(0, 0), follow: true, keys: newKeymap(), help: h,
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
		m.lastInfo, m.lastInfoWarn = "", false // a new pass starting is the "transient" in transient note
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
		// A pass may reorder the list under the cursor, so the pane re-targets
		// on data as well as on keys.
		return m, tea.Batch(m.waitEvent(), m.wantDetail())
	case pollMsg:
		// The poll is also the clock: elapsed times and the heartbeat
		// re-render even when the log is quiet.
		m.frame++
		if m.tail.read() && m.focus == panelLanes {
			m.refreshLog()
		}
		return m, poll()
	case openErrMsg:
		m.lastErr = clean(msg.err.Error())
		return m, nil
	case detailDueMsg:
		return m, m.fetchDetail(msg.ticketID)
	case detailMsg:
		m.applyDetail(msg)
		return m, nil
	case promotedMsg:
		if msg.err != nil {
			m.lastErr = clean(fmt.Sprintf("promote %s to %s: %v", msg.ticket, msg.status, msg.err))
		} else {
			m.lastInfo, m.lastInfoWarn = fmt.Sprintf("promoted %s to %s", msg.ticket, msg.status), false
		}
		return m, nil
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.promoting {
		return m.handlePromoteKey(msg)
	}
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
	case key.Matches(msg, m.keys.Promote):
		if m.focus == panelAttention && len(m.shown) > 0 && len(m.o.Statuses) > 0 {
			m.promoting = true
			m.promoteSel = 0
		}
	case key.Matches(msg, m.keys.Sort):
		if m.focus == panelAttention {
			m.sortMode = (m.sortMode + 1) % sortModes
			m.resort()
			m.refreshMain()
		}
	case key.Matches(msg, m.keys.Project):
		if m.focus == panelAttention {
			m.cycleProject()
			m.refreshMain()
		}
	// The scroll keys move whatever the main pane shows; follow is the log's
	// state alone, so a detour through a detail lens can never freeze the tail.
	case key.Matches(msg, m.keys.PageUp):
		m.vp.ViewUp()
		if m.focus == panelLanes {
			m.follow = m.vp.AtBottom()
		}
	case key.Matches(msg, m.keys.PageDown):
		m.vp.ViewDown()
		if m.focus == panelLanes {
			m.follow = m.vp.AtBottom()
		}
	case key.Matches(msg, m.keys.Top):
		m.vp.GotoTop()
		if m.focus == panelLanes {
			m.follow = false
		}
	case key.Matches(msg, m.keys.Bottom):
		m.vp.GotoBottom()
		if m.focus == panelLanes {
			m.follow = true
		}
	case key.Matches(msg, m.keys.Raw):
		m.rawLog = !m.rawLog
		m.refreshLog()
	case key.Matches(msg, m.keys.Open):
		return m, openURL(m.selectedURL())
	}
	cmd := m.wantDetail()
	m.layout()
	return m, cmd
}

// wantDetail points the pane at the current needs-you selection. It only
// schedules: the read waits for the selection to settle, so holding j down
// a fifteen-row list schedules fifteen ticks and fires one fetch.
func (m *model) wantDetail() tea.Cmd {
	if m.focus != panelAttention {
		return nil
	}
	it := m.selectedAttention()
	if it == nil || it.TicketID == "" || it.TicketID == m.detailWant {
		return nil
	}
	m.detailWant = it.TicketID
	id := it.TicketID
	return tea.Tick(detailDebounce, func(time.Time) tea.Msg { return detailDueMsg{ticketID: id} })
}

// fetchDetail issues the read, unless the selection moved on while the
// debounce ran or the pane already has the ticket. Like doPromote it runs
// off the render loop, so a slow Linear call never blocks a frame. A failed
// entry is retried when the pane comes back to it; a loaded one never is.
func (m *model) fetchDetail(ticketID string) tea.Cmd {
	if ticketID != m.detailWant {
		return nil
	}
	if d := m.details[ticketID]; d != nil && d.state != detailFailed {
		return nil
	}
	m.details[ticketID] = &ticketDetail{state: detailLoading}
	m.refreshMain()
	m.layout()
	return func() tea.Msg {
		detail, err := m.o.Reader.IssueDetail(m.ctx, ticketID)
		return detailMsg{ticketID: ticketID, detail: detail, err: err}
	}
}

// applyDetail folds one read into the cache. It is where this text stops
// being untrusted, exactly as apply is for events: cleaned once, on the way
// in.
func (m *model) applyDetail(msg detailMsg) {
	d := &ticketDetail{state: detailReady}
	if msg.err != nil {
		d.state = detailFailed
		d.err = clean(msg.err.Error())
	} else {
		detail := cleanDetail(msg.detail)
		d.body, d.comments = detail.Body, detail.Comments
	}
	m.details[msg.ticketID] = d
	if m.focus == panelAttention {
		m.refreshMain()
	}
	m.layout()
}

// handlePromoteKey drives the promote picker: choose a target status for the
// needs-you panel's selected ticket, or back out without touching Linear.
func (m model) handlePromoteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.promoting = false
	case "up", "k":
		if m.promoteSel > 0 {
			m.promoteSel--
		}
	case "down", "j":
		if m.promoteSel < len(m.o.Statuses)-1 {
			m.promoteSel++
		}
	case "enter":
		it := m.selectedAttention()
		m.promoting = false
		if it != nil {
			cmd = m.doPromote(it.TicketID, it.Ticket, m.o.Statuses[m.promoteSel])
		}
	}
	// Closing the picker hands the main pane back to a lens of a different
	// height; re-fit before the next frame draws into the old one.
	m.layout()
	return m, cmd
}

// doPromote calls the one write the TUI is allowed (SCOPE's promote
// amendment) off the render loop, so a slow Linear call never blocks a
// frame.
func (m model) doPromote(ticketID, ticket, status string) tea.Cmd {
	return func() tea.Msg {
		err := m.o.Promoter.Promote(m.ctx, ticketID, status)
		return promotedMsg{ticket: ticket, status: status, err: err}
	}
}

func (m *model) setFocus(p panel) {
	m.focus = p
	m.refreshMain()
	if p != panelLanes {
		m.vp.GotoTop()
	}
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
		m.attnSel = clampIndex(m.attnSel+delta, len(m.shown))
		m.refreshMain()
		m.vp.GotoTop()
	case panelNext:
		m.nextSel = clampIndex(m.nextSel+delta, len(m.nextRefs()))
		m.refreshMain()
		m.vp.GotoTop()
	}
}

func clampIndex(i, n int) int {
	return max(0, min(i, n-1))
}

// selectedURL is what `o` opens: Linear's own URL for the selected needs-you
// item or up-next ticket. Lanes have no URL — a running lane's door is its
// log, already on screen.
func (m *model) selectedURL() string {
	switch m.focus {
	case panelAttention:
		if it := m.selectedAttention(); it != nil {
			return it.URL
		}
	case panelNext:
		if t := m.nextTicket(); t != nil {
			return t.URL
		}
	}
	return ""
}

// openURL hands the URL to the OS opener. This is the TUI opening the
// operator's browser, not lerp speaking to an API; everything beyond
// promote still happens in Linear.
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

// apply folds one loop event into the model. It is also where untrusted
// text stops being untrusted: every Linear-sourced string on the event is
// cleaned here, once, so the views below can stay plain string building.
func (m *model) apply(ev loop.Event) {
	ev = cleanEvent(ev)
	if ev.Err != nil {
		// Pass errors interpolate Linear's own status and team names.
		m.lastErr = clean(ev.Err.Error())
		m.passHadErr = true
	}
	changed := panel(-1) // which panel's lens data this event feeds
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
		if ev.Note != "" {
			// A hop the loop skipped is the operator's business, not the log
			// file's alone: it means a stage of their pipeline did not run.
			m.lastInfo, m.lastInfoWarn = ev.Note, true
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
		changed = panelNext
	case loop.EventAttention:
		m.attention = ev.Attention
		m.attentionSeen = true
		// The filter is a choice about the list that was on screen. When the
		// pass no longer has that project, the choice would hide the whole
		// panel behind a name nothing waits in, so it resets to all.
		if m.project != "" && !slices.Contains(m.projects(), m.project) {
			m.project = ""
		}
		// A pass mid-picker may shrink or empty the list out from under it;
		// resort clamps the selection, and the picker closes rather than
		// promote a ticket that is no longer there.
		m.resort()
		if len(m.shown) == 0 {
			m.promoting = false
		}
		changed = panelAttention
	}
	m.reorder()
	m.layout()
	m.retarget()
	// Only the lens this event feeds is re-rendered; the log pane refreshes
	// through retarget and the poll, never from here.
	if changed == m.focus {
		m.refreshMain()
	}
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
// selects: the log tail for running lanes, a detail lens for the other two.
// Scroll position is the caller's concern — focus and selection changes jump
// to the top; a data refresh keeps the operator's place.
func (m *model) refreshMain() {
	if m.focus == panelLanes {
		m.refreshLog()
		return
	}
	// The viewport's width is the pane's inner width, and it follows the
	// terminal's alone — so wrapping against it here can never disagree with
	// the width geometry measured the same content at.
	m.vp.SetContent(m.detail(m.vp.Width))
}

// detail is the read-only lens the main pane shows for the two panels that
// are not the log — and the measure geometry fits the pane's box to. width
// is the pane's inner width, which the needs-you lens wraps prose to.
func (m *model) detail(width int) string {
	if m.focus == panelNext {
		return m.nextDetail()
	}
	return m.attentionDetail(width)
}

// refreshLog points the pane at the selected lane's log: the decoded view of
// what the agent is doing, or — with the raw toggle on — the bytes the runner
// wrote. Nothing but the rendering differs between the two; the file on disk
// and the scrollback are the same either way.
func (m *model) refreshLog() {
	if m.focus != panelLanes {
		return
	}
	if m.rawLog {
		m.vp.SetContent(cleanLog(m.tail.content()))
	} else {
		m.vp.SetContent(m.tail.rendered(m.vp.Width))
	}
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

// selectedAttention is the needs-you selection, nil when nothing is shown —
// the one place that owns the empty case, like nextTicket for the other
// panel. It points into the sorted-and-filtered list, so the selection is
// always the row under the cursor rather than a position in the pass's own
// order.
func (m *model) selectedAttention() *loop.AttentionItem {
	if len(m.shown) == 0 {
		return nil
	}
	return &m.shown[clampIndex(m.attnSel, len(m.shown))]
}

// resort rebuilds the shown list under the current mode and filter. The
// selection follows its ticket across the rebuild: pressing the sort key
// moves the rows, never the cursor.
func (m *model) resort() {
	selected := ""
	if it := m.selectedAttention(); it != nil {
		selected = it.Ticket
	}
	m.shown = sortAttention(filterAttention(m.attention, m.project), m.sortMode)
	i := slices.IndexFunc(m.shown, func(it loop.AttentionItem) bool { return it.Ticket == selected })
	if selected == "" || i < 0 {
		m.attnSel = clampIndex(m.attnSel, len(m.shown))
		return
	}
	m.attnSel = i
}

// filterAttention scopes the list to one Linear project. There is no filter
// syntax and no second query behind this: it is the project column, matched
// whole, or every project.
func filterAttention(items []loop.AttentionItem, project string) []loop.AttentionItem {
	if project == "" {
		return items
	}
	out := make([]loop.AttentionItem, 0, len(items))
	for _, it := range items {
		if it.Project == project {
			out = append(out, it)
		}
	}
	return out
}

// sortAttention orders a copy of items for the mode. Every mode falls
// through to leverage and then to the identifier, so no two rows are ever
// in an arbitrary order and no mode needs a second sort key configured.
func sortAttention(items []loop.AttentionItem, mode sortMode) []loop.AttentionItem {
	out := slices.Clone(items)
	slices.SortFunc(out, func(a, b loop.AttentionItem) int {
		switch mode {
		case sortPriority:
			if c := cmp.Compare(priorityRank(a.Priority), priorityRank(b.Priority)); c != 0 {
				return c
			}
		case sortStatus:
			// Pipeline-relevance first, so the statuses a run left a ticket
			// in sort above the ones the pipeline never named.
			if c := cmp.Compare(a.Relevance, b.Relevance); c != 0 {
				return c
			}
			if c := strings.Compare(a.Status, b.Status); c != 0 {
				return c
			}
		case sortProject:
			if c := compareProject(a.Project, b.Project); c != 0 {
				return c
			}
		}
		return compareLeverage(a, b)
	})
	return out
}

// compareProject orders two project names, with a ticket in no project
// last — "none" is not a name to file under, the same reason an unset
// priority ranks below Low rather than above Urgent.
func compareProject(a, b string) int {
	if (a == "") != (b == "") {
		if a == "" {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

// compareLeverage is the default order and every other mode's tiebreak:
// what routing a ticket would free, then priority, then the identifier.
func compareLeverage(a, b loop.AttentionItem) int {
	// A blocked ticket cannot usefully be routed anywhere yet, so it sorts
	// below every ticket that can be — however much it would unblock once
	// its own blocker clears.
	if ablocked, bblocked := len(a.BlockedBy) > 0, len(b.BlockedBy) > 0; ablocked != bblocked {
		if ablocked {
			return 1
		}
		return -1
	}
	if c := cmp.Compare(b.Unblocks, a.Unblocks); c != 0 {
		return c
	}
	if c := cmp.Compare(priorityRank(a.Priority), priorityRank(b.Priority)); c != 0 {
		return c
	}
	return strings.Compare(a.Ticket, b.Ticket)
}

// priorityRank turns Linear's priority into a sort key. The scale runs
// urgent (1) to low (4) but puts "no priority" at 0, which would otherwise
// sort ahead of urgent; unset means unranked, so it goes last.
func priorityRank(p int) int {
	if p == 0 {
		return 5
	}
	return p
}

// projects lists the Linear projects present in the pass's list, in name
// order — the filter's cycle. A ticket filed under none is not a project
// and is not a stop on it.
func (m *model) projects() []string {
	var names []string
	for _, it := range m.attention {
		if it.Project != "" && !slices.Contains(names, it.Project) {
			names = append(names, it.Project)
		}
	}
	slices.Sort(names)
	return names
}

// cycleProject advances the filter one stop: every project, then each
// project present, then back to every project.
func (m *model) cycleProject() {
	names := m.projects()
	switch i := slices.Index(names, m.project); {
	case i+1 >= len(names):
		m.project = ""
	default:
		m.project = names[i+1]
	}
	m.resort()
}

// geometry is the screen's arithmetic. One rule: every panel asks for the
// rows it will render, and the panel with focus absorbs whatever is left
// over — so the panel being worked in is the one that grows, and moving
// focus moves the space without an expand key to press. A panel with
// nothing to show asks for a single line, its title row, and takes its body
// back the moment it has something to say or the operator focuses it. When
// the wants exceed the body, the unfocused panels are squeezed to a floor
// before the focused one gives anything up. Heights include borders, and
// the stack always fits bodyH so the status bar stays on screen.
type geometry struct {
	wide                 bool
	sideW, mainW         int
	bodyH                int
	attnH, lanesH, nextH int
	mainH                int
}

const (
	// panelFloor is the smallest a panel is squeezed to: a border, one row,
	// a border. mainFloor is the same for the main pane in the stacked
	// layout, where it shares the body with the three panels. collapsedH is
	// what a panel with nothing to show costs.
	panelFloor = 3
	mainFloor  = 5
	collapsedH = 1
)

func (m *model) geometry() geometry {
	g := geometry{bodyH: max(4, m.height-1)}
	g.wide = m.width >= narrowWidth
	// Widths first: the row builders lay their rows out to the panel width,
	// and the wants are counted from those very rows.
	g.sideW, g.mainW = m.width, m.width
	if g.wide {
		// Four columns need the room: a third of the terminal truncated the
		// status column out of a real backlog. No resize key — the split is
		// a proportion of the window, and the window is the knob.
		g.sideW = max(28, m.width*45/100)
		g.mainW = m.width - g.sideW
	}

	// Wants come from the same row builders the panels draw with, so the
	// counts can never drift from what lands on screen. The panel constants
	// are the stack's order, so a panel doubles as its index.
	attnRows, _ := m.attentionRows(g.sideW - 2)
	nextRows, _ := m.nextListRows()
	want := []int{
		m.panelWant(panelAttention, len(attnRows)),
		m.panelWant(panelLanes, len(m.order)),
		m.panelWant(panelNext, len(nextRows)),
	}
	floor := []int{panelFloor, panelFloor, panelFloor}

	if g.wide {
		// The main pane has the other column to itself, so it fits its own
		// content and never competes with the stack.
		g.mainH = min(g.bodyH, m.mainWant(g.bodyH, g.mainW-2))
		h := fitPanels(want, floor, g.bodyH, int(m.focus))
		g.attnH, g.lanesH, g.nextH = h[0], h[1], h[2]
		return g
	}
	// Stacked, the main pane is one more claimant on the same body.
	h := fitPanels(append(want, m.mainWant(g.bodyH, g.mainW-2)), append(floor, mainFloor), g.bodyH, int(m.focus))
	g.attnH, g.lanesH, g.nextH, g.mainH = h[0], h[1], h[2], h[3]
	return g
}

// panelWant is one panel's height in the stack: the rows it will render
// plus its borders, or a single title row when it has nothing to show. The
// focused panel is never collapsed — focus is how the operator opens an
// empty panel back up to select in it.
func (m *model) panelWant(p panel, rows int) int {
	if m.focus != p && m.panelEmpty(p) {
		return collapsedH
	}
	return rows + 2
}

// panelEmpty is the content test behind the collapse: nothing waits on the
// operator, no lane is busy, no queue holds a ticket. Not knowing yet is not
// empty — a panel no pass has reported on keeps its body and says so.
func (m *model) panelEmpty(p panel) bool {
	switch p {
	case panelAttention:
		return m.attentionSeen && len(m.attention) == 0
	case panelLanes:
		for _, ln := range m.lanes {
			if ln.state != laneIdle {
				return false
			}
		}
		return true
	case panelNext:
		return m.queues != nil && len(m.nextRefs()) == 0
	}
	return false
}

// mainWant is the main pane's height by the same rule. The detail lenses ask
// for the lines they draw; the log tail, the promote picker and the help
// overlay ask for the whole body, because what they hold scrolls.
func (m *model) mainWant(bodyH, width int) int {
	if m.promoting || m.helpOn || m.focus == panelLanes {
		return bodyH
	}
	return strings.Count(m.detail(width), "\n") + 3
}

// fitPanels turns wants into heights summing to avail: grant every want when
// they fit and hand the slack to focused, otherwise take rows from the
// tallest panel still above its floor, sparing focused until the others have
// nothing left to give. A panel asking for less than its floor keeps its
// want as the floor, so a collapsed panel is never padded back out.
func fitPanels(want, floors []int, avail, focused int) []int {
	h := slices.Clone(want)
	floor := make([]int, len(h))
	total := 0
	for i, v := range h {
		floor[i] = min(v, floors[i])
		total += v
	}
	if total <= avail {
		h[focused] += avail - total
		return h
	}
	over := squeeze(h, floor, total-avail, focused)
	over = squeeze(h, floor, over, -1)
	// Under every floor at once means a window smaller than View's guard
	// allows. Keep the arithmetic honest anyway: panels shrink to nothing
	// rather than push the status bar off screen.
	squeeze(h, make([]int, len(h)), over, -1)
	return h
}

// squeeze takes rows one at a time from the tallest panel above its floor,
// skipping the panel at keep, and reports what it could not take.
func squeeze(h, floor []int, need, keep int) int {
	for need > 0 {
		tallest := -1
		for i := range h {
			if i == keep || h[i] <= floor[i] {
				continue
			}
			if tallest < 0 || h[i] > h[tallest] {
				tallest = i
			}
		}
		if tallest < 0 {
			return need
		}
		h[tallest]--
		need--
	}
	return 0
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
	// A pane that just changed height holds a scroll position measured
	// against the old one: re-pin a followed log to the bottom, and clamp
	// anything else back inside the new box.
	if m.focus == panelLanes && m.follow {
		m.vp.GotoBottom()
		return
	}
	m.vp.SetYOffset(m.vp.YOffset)
}

func (m model) View() string {
	if !m.ready {
		return "starting lerp…\n"
	}
	// Below every panel's floor plus the status bar — plus the main pane's
	// floor when the layout stacks — geometry can only produce a screen
	// taller than the terminal. Say so instead of rendering one.
	minH := 3*panelFloor + 1
	if m.width < narrowWidth {
		minH += mainFloor
	}
	if m.width < 24 || m.height < minH {
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

// attentionRows builds the needs-you table's rows — under a grouping mode,
// a header above each run of them; sel is the selected row's index (-1 with
// nothing to select), for the focus window.
func (m *model) attentionRows(width int) ([]string, int) {
	switch {
	case !m.attentionSeen:
		return []string{styleFaint.Render("reading the board…")}, -1
	case len(m.attention) == 0:
		return []string{styleFaint.Render("nothing needs you")}, -1
	case len(m.shown) == 0:
		return []string{styleFaint.Render("nothing needs you in " + m.project)}, -1
	}
	focused := m.focus == panelAttention
	// Every column is padded to the widest cell on the list, so the four of
	// them line up as columns worth scanning rather than as ragged text.
	idW, statusW, projW := 0, 0, 0
	for _, it := range m.shown {
		idW = max(idW, lipgloss.Width(it.Ticket))
		statusW = max(statusW, lipgloss.Width(it.Status))
		projW = max(projW, lipgloss.Width(projectName(it.Project)))
	}
	var rows []string
	sel := -1
	header := ""
	for i, it := range m.shown {
		if m.sortMode.grouped() {
			if h, note := m.sortMode.header(it); h != header {
				header = h
				row := styleTicket.Render(h)
				if note != "" {
					row += styleFaint.Render(" — " + note)
				}
				rows = append(rows, row)
			}
		}
		if i == m.attnSel {
			sel = len(rows)
		}
		rows = append(rows, attentionRow(it, focused && i == m.attnSel, idW, statusW, projW, width))
	}
	return rows, sel
}

// titleFloor is how much of a title has to survive for the project column
// to earn its width. Below it the project drops out of the row entirely and
// the title takes the space back — a title cut shorter than this has stopped
// being a title, and the project is the one column a routing decision can
// most often do without.
const titleFloor = 20

// attentionRow is one waiting ticket as a table row: identifier, leverage
// and title, then status, project and priority as right-hand columns. Every
// fact a routing decision needs is on the line, so the choice can be made
// without selecting the row — which is the whole point of the panel.
//
// Columns elide from the right: the title truncates first, and the project
// drops out before the status column would ever be squeezed. The identifier,
// the leverage and the real Linear status survive any width.
func attentionRow(it loop.AttentionItem, selected bool, idW, statusW, projW, width int) string {
	id := styleTicket.Render(it.Ticket) + strings.Repeat(" ", max(0, idW-lipgloss.Width(it.Ticket)))
	head := marker(selected) + id + " " + leverageCell(it) + " "
	status := statusCell(it, statusW)
	right := status + "  " + priorityCell(it.Priority)
	full := status + "  " + projectCell(it.Project, projW) + "  " + priorityCell(it.Priority)
	switch {
	case width-lipgloss.Width(full) >= lipgloss.Width(head)+titleFloor:
		right = full
	case width-lipgloss.Width(right) < lipgloss.Width(head):
		// Narrower than even the title-less row: the priority goes too, so
		// the identifier, the leverage and the status are the last three
		// things standing. Every row measures the same head and the same
		// columns, so the whole panel elides together.
		right = status
	}
	return splitRow(head+it.Title, right, width)
}

// statusCell is the row's status column: the real Linear status name — the
// vocabulary the operator already chose, never a synonym invented here —
// and a mark for a status the configured pipeline never names. That mark is
// the fingerprint of a ticket that left the pipeline, worth seeing without
// selecting the row.
func statusCell(it loop.AttentionItem, w int) string {
	cell := it.Status
	if it.Relevance == loop.StatusUnnamed {
		cell += " " + styleAttention.Render("⚠")
	}
	return cell + strings.Repeat(" ", max(0, w+2-lipgloss.Width(cell)))
}

// projectCell is the row's project column, a dash for a ticket filed under
// no project.
func projectCell(project string, w int) string {
	name := projectName(project)
	cell := name
	if project == "" {
		cell = styleFaint.Render(name)
	}
	return cell + strings.Repeat(" ", max(0, w-lipgloss.Width(name)))
}

// projectName is how a project reads in a row: its name, or a dash. Saying
// "none" would read as a project of its own, the same reason priorityCell
// draws an unset priority as a dash.
func projectName(project string) string {
	if project == "" {
		return "—"
	}
	return project
}

// leverageCell says what routing this ticket would free, in a fixed-width
// cell so the titles line up: ⊘ for a ticket something still blocks, ↓n for
// the count it transitively unblocks. Bold marks the ones with downstream —
// shape and weight, not color alone.
func leverageCell(it loop.AttentionItem) string {
	if len(it.BlockedBy) > 0 {
		return styleAttention.Render("⊘") + "  "
	}
	cell := fmt.Sprintf("↓%d", it.Unblocks)
	pad := strings.Repeat(" ", max(0, 3-lipgloss.Width(cell)))
	if it.Unblocks > 0 {
		return styleTicket.Render(cell) + pad
	}
	return styleFaint.Render(cell) + pad
}

// priorityCell renders Linear's priority scale as its own words. An unset
// priority is a dash: saying "none" would read as a rank of its own. The
// cell is padded to the widest label so the columns to its left stay put
// from row to row.
func priorityCell(p int) string {
	label, style := "—", styleFaint
	switch p {
	case 1:
		label, style = "Urgent", styleAttention
	case 2:
		label = "High"
	case 3:
		label = "Medium"
	case 4:
		label = "Low"
	}
	return style.Render(label) + strings.Repeat(" ", max(0, len("Urgent")-lipgloss.Width(label)))
}

func (m model) attentionPanel(w, h int) string {
	focused := m.focus == panelAttention
	extra := ""
	if len(m.attention) > 0 {
		// The sort mode and the project filter live in the title because
		// they are the only two things about this panel a key changed, and
		// a table sorted differently than the operator remembers is worse
		// than one that says how it is sorted.
		count := fmt.Sprintf(" ● %d", len(m.attention))
		if m.project != "" {
			count = fmt.Sprintf(" ● %d/%d", len(m.shown), len(m.attention))
		}
		extra = styleAttention.Render(count) + styleFaint.Render(" · by "+m.sortMode.String())
		if m.project != "" {
			extra += styleFaint.Render(" · " + m.project)
		}
	}
	if h <= collapsedH {
		if m.panelEmpty(panelAttention) {
			extra += styleFaint.Render(" — nothing needs you")
		}
		return panelLine(panelTitle(1, "needs you", focused, extra), w)
	}
	rows, sel := m.attentionRows(w - 2)
	if focused && sel >= 0 {
		rows = windowRows(rows, sel, h-2)
	}
	return panelBox(panelTitle(1, "needs you", focused, extra), focused, w, h, rows)
}

// busyLanes counts the configured lanes hosting live runs. Adopted runs
// above N are visible as extra rows but sit outside the configured
// capacity, so they stay out of the fraction.
func (m model) busyLanes() int {
	busy := 0
	for n, ln := range m.lanes {
		if n <= m.o.Lanes && ln.state != laneIdle {
			busy++
		}
	}
	return busy
}

func (m model) lanesPanel(w, h int) string {
	focused := m.focus == panelLanes
	extra := styleFaint.Render(fmt.Sprintf(" · %d/%d busy", m.busyLanes(), m.o.Lanes))
	if h <= collapsedH {
		if m.panelEmpty(panelLanes) {
			extra += styleFaint.Render(" — all lanes idle")
		}
		return panelLine(panelTitle(2, "running", focused, extra), w)
	}
	rows := make([]string, 0, len(m.order))
	for _, n := range m.order {
		rows = append(rows, m.laneRow(n, w-2))
	}
	if focused {
		if i := slices.Index(m.order, m.selected); i >= 0 {
			rows = windowRows(rows, i, h-2)
		}
	}
	return panelBox(panelTitle(2, "running", focused, extra), focused, w, h, rows)
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
	return splitRow(left, right, width)
}

// nextListRows builds the up-next panel's rows — each queue header, then its
// tickets in pickup order; sel is the selected row's index (-1 with nothing
// to select), for the focus window.
func (m *model) nextListRows() ([]string, int) {
	if m.queues == nil {
		return []string{styleFaint.Render("waiting for the first pass…")}, -1
	}
	focused := m.focus == panelNext
	selIdx := -1
	if refs := m.nextRefs(); len(refs) > 0 {
		selIdx = clampIndex(m.nextSel, len(refs))
	}
	var rows []string
	sel := -1
	idx := 0
	for _, q := range m.queues {
		meta := fmt.Sprintf(" %s · %s · %d", q.Status, q.Team, len(q.Tickets))
		if len(q.Tickets) == 0 {
			meta = fmt.Sprintf(" %s · %s · empty", q.Status, q.Team)
		}
		rows = append(rows, styleTicket.Render(q.Name)+styleFaint.Render(meta))
		for _, tk := range q.Tickets {
			row := marker(focused && idx == selIdx)
			if tk.Eligible {
				row += styleTicket.Render(tk.Identifier) + " " + tk.Title
			} else {
				row += styleFaint.Render(tk.Identifier + " " + tk.Title)
			}
			if idx == selIdx {
				sel = len(rows)
			}
			rows = append(rows, row)
			idx++
		}
	}
	return rows, sel
}

func (m model) nextPanel(w, h int) string {
	focused := m.focus == panelNext
	if h <= collapsedH {
		extra := ""
		if m.panelEmpty(panelNext) {
			extra = styleFaint.Render(" — all queues empty")
		}
		return panelLine(panelTitle(3, "up next", focused, extra), w)
	}
	rows, sel := m.nextListRows()
	if focused && sel >= 0 {
		rows = windowRows(rows, sel, h-2)
	}
	return panelBox(panelTitle(3, "up next", focused, ""), focused, w, h, rows)
}

// mainPanel is the lens: the promote picker while it is open, the ? overlay,
// otherwise the log for running lanes and a read-only detail for the other
// panels.
func (m model) mainPanel(w, h int) string {
	if m.promoting {
		if it := m.selectedAttention(); it != nil {
			return m.promotePicker(*it, w, h)
		}
	}
	if m.helpOn {
		return panelBox(styleTitleFocus.Render("help"), true, w, h,
			strings.Split(m.help.View(m.keys), "\n"))
	}
	title := m.mainTitle()
	return panelBox(styleFaint.Render(title), false, w, h,
		strings.Split(m.vp.View(), "\n"))
}

// promotePicker renders the target-status list for the selected needs-you
// item: every configured queue status plus the pipeline's exits — exactly
// what Promote (a plain MoveIssue) is allowed to move a ticket into.
func (m model) promotePicker(it loop.AttentionItem, w, h int) string {
	rows := []string{it.Title, ""}
	for i, status := range m.o.Statuses {
		if i == m.promoteSel {
			rows = append(rows, styleFocus.Render("▸ "+status))
		} else {
			rows = append(rows, "  "+status)
		}
	}
	// The highlighted status must be on screen before enter can confirm it.
	rows = windowRows(rows, 2+m.promoteSel, h-2)
	return panelBox(styleTitleFocus.Render("promote "+it.Ticket), true, w, h, rows)
}

func (m model) mainTitle() string {
	switch m.focus {
	case panelAttention:
		if it := m.selectedAttention(); it != nil {
			return it.Ticket
		}
		return "needs you"
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
		return fmt.Sprintf("last log%s · lane %d", m.rawSuffix(), m.selected)
	default:
		return fmt.Sprintf("log%s · %s · %s · lane %d", m.rawSuffix(), ln.name(), ln.queue, m.selected)
	}
}

// rawSuffix marks the pane's title while the raw toggle is on, so a pane full
// of stream JSON is never a mystery.
func (m model) rawSuffix() string {
	if m.rawLog {
		return " (raw)"
	}
	return ""
}

// attentionDetail is the main pane's lens on the selected needs-you item:
// everything the loop knows, Linear's URL, and then the ticket itself (see
// ticketLines). Promote is the one action here; everything else about the
// item happens in Linear.
func (m model) attentionDetail(width int) string {
	if !m.attentionSeen {
		return styleFaint.Render("reading the board…")
	}
	if len(m.attention) == 0 {
		return styleFaint.Render("nothing needs you — the empty list is the goal state") + "\n" +
			styleFaint.Render("(shows "+loop.AttentionDefinition+")")
	}
	if len(m.shown) == 0 {
		return styleFaint.Render("nothing needs you in "+m.project) + "\n" +
			styleFaint.Render("(P cycles the project filter back to all)")
	}
	it := m.selectedAttention()
	status := it.Status
	if it.Relevance == loop.StatusUnnamed {
		status += " " + styleAttention.Render("⚠")
	}
	// These lines come from the pass and always render first, whatever the
	// read of the ticket itself is doing: a failed fetch must never cost the
	// operator the pane that works today.
	lines := []string{
		styleTicket.Render(it.Ticket) + " " + it.Title,
		"",
		styleFaint.Render("status  ") + status,
		styleFaint.Render("project ") + projectName(it.Project),
		styleFaint.Render("why     ") + it.Reason,
		styleFaint.Render("linear  ") + it.URL,
		"",
		// Short enough to survive the pane: the hint that gets truncated is
		// the hint that was not there.
		styleFaint.Render("p promote · s sort · P project · o open in Linear"),
	}
	return strings.Join(append(lines, m.ticketLines(it.TicketID, width)...), "\n")
}

// ticketLines is the ticket itself, below the pass's own lines: the body,
// then the comments oldest first — so lerp's last stage-boundary artifact,
// the verdict that parked the ticket, is where the eye lands. Read-only and
// flat: nothing here is selectable, no thread is followed, no other ticket
// is reachable from it. Markdown is rendered as the plain text it is; `o` is
// the answer to anything that wants more.
func (m model) ticketLines(ticketID string, width int) []string {
	d := m.details[ticketID]
	switch {
	case d == nil:
		return nil
	case d.state == detailLoading:
		return []string{"", styleFaint.Render("reading the ticket…")}
	case d.state == detailFailed:
		return []string{"", styleFaint.Render("couldn't read the ticket: " + d.err), styleFaint.Render("o opens it in Linear")}
	}
	lines := []string{""}
	if body := strings.TrimSpace(d.body); body != "" {
		lines = append(lines, wrapText(body, width)...)
	} else {
		lines = append(lines, styleFaint.Render("(no description)"))
	}
	if len(d.comments) == 0 {
		return append(lines, "", styleFaint.Render("(no comments)"))
	}
	for _, c := range d.comments {
		lines = append(lines, "", styleFaint.Render(commentHead(c)))
		lines = append(lines, wrapText(strings.TrimSpace(c.Body), width)...)
	}
	return lines
}

// commentHead is one comment's byline: who wrote it and how long ago.
func commentHead(c linear.Comment) string {
	if c.CreatedAt.IsZero() {
		return c.Author
	}
	return c.Author + " · " + age(c.CreatedAt)
}

// age renders a comment's age in one unit. Reading a thread, the scale that
// matters is "minutes or days", never seconds.
func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// wrapText word-wraps prose to the pane's inner width. panelBox truncates
// its rows instead of wrapping — right for a one-line list row, wrong for a
// ticket body, where it would throw away everything past the first line.
func wrapText(s string, width int) []string {
	return strings.Split(ansi.Wrap(s, max(8, width), "-"), "\n")
}

// nextDetail is the lens on the selected up-next ticket: where it sits in
// pickup order and what, if anything, gates it. With nothing queued it says
// how tickets enter each queue instead.
func (m model) nextDetail() string {
	if m.queues == nil {
		return styleFaint.Render("waiting for the first pass…")
	}
	refs := m.nextRefs()
	if len(refs) == 0 {
		lines := []string{styleFaint.Render("every queue is empty"), ""}
		for _, q := range m.queues {
			lines = append(lines, styleTicket.Render(q.Name)+
				styleFaint.Render(fmt.Sprintf(` — tickets enter when moved to "%s"`, q.Status)))
		}
		return strings.Join(lines, "\n")
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
// needs-you count, keys. A pass error — or a transient note like a
// promote's outcome — takes over the whole line; a truncated error is not
// actionable, so nothing else competes with it for the width.
func (m model) statusBar() string {
	switch {
	case m.lastErr != "":
		return ansi.Truncate(styleErr.Render("✗ "+m.lastErr), m.width, "…")
	case m.lastInfo != "":
		// A promote worked; a skipped hop did not. Reporting both with the
		// same green tick would read as "all is well" either way.
		mark, style := "✓ ", styleRunning
		if m.lastInfoWarn {
			mark, style = "! ", styleAttention
		}
		return ansi.Truncate(style.Render(mark+m.lastInfo), m.width, "…")
	}
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
	left += "  " + styleFaint.Render(fmt.Sprintf("lanes %d/%d", m.busyLanes(), m.o.Lanes))
	if len(m.attention) > 0 {
		left += "  " + styleAttention.Render(fmt.Sprintf("● %d need you", len(m.attention)))
	}
	right := styleFaint.Render("? help · q quit")
	if m.promoting {
		right = styleFaint.Render("↑/↓ choose · enter promote · esc cancel")
	}

	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		right = ansi.Truncate(right, max(0, m.width-1), "…")
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
