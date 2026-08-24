package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
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

// view is which of SCOPE's three views fills the body. Only Board is built;
// Attention and Queue are placeholders until their own tickets land.
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
	laneIdle    laneState = iota // no agent; note says how the last run ended
	laneRunning                  // an agent this process started
	laneAdopted                  // a live agent inherited from a previous process
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
	selected int   // index into order

	vp     viewport.Model
	tail   tail
	follow bool

	lastErr string
}

func newModel(ctx context.Context, o Options) model {
	if o.Interval <= 0 {
		o.Interval = loop.DefaultInterval
	}
	m := model{o: o, ctx: ctx, view: viewBoard, lanes: make(map[int]*lane),
		vp: viewport.New(0, 0), follow: true}
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
// kills an agent.
func (m model) runTick() tea.Cmd {
	return func() tea.Msg {
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
		return m, m.runTick()
	case tickedMsg:
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
		if m.view == viewBoard && m.selected > 0 {
			m.selected--
			m.retarget()
		}
	case "down", "j":
		if m.view == viewBoard && m.selected < len(m.order)-1 {
			m.selected++
			m.retarget()
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
	}
	switch ev.Type {
	case loop.EventStarted:
		m.lanes[ev.Lane] = &lane{state: laneRunning, runID: ev.RunID, ticketID: ev.TicketID,
			ticket: ev.Ticket, queue: ev.Queue, logPath: ev.LogPath, since: time.Now()}
	case loop.EventAdopted:
		m.lanes[ev.Lane] = &lane{state: laneAdopted, runID: ev.RunID, ticketID: ev.TicketID,
			queue: ev.Queue, logPath: ev.LogPath, since: time.Now()}
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

func (m *model) reorder() {
	m.order = m.order[:0]
	for n := range m.lanes {
		m.order = append(m.order, n)
	}
	slices.Sort(m.order)
	if m.selected >= len(m.order) {
		m.selected = len(m.order) - 1
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
	if m.selected < 0 || m.selected >= len(m.order) {
		return nil
	}
	return m.lanes[m.order[m.selected]]
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
	case viewBoard:
		b.WriteString(m.board())
	default:
		// Deliberately empty shells: the Attention and Queue views are their
		// own tickets. The switching seam is what this shell provides.
		b.WriteString(inactiveStyle.Render(fmt.Sprintf("the %s view is not built yet", m.view)))
		b.WriteString("\n")
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

func (m model) board() string {
	var b strings.Builder
	for i, n := range m.order {
		b.WriteString(m.laneRow(i, n))
		b.WriteString("\n")
	}
	b.WriteString(m.logTitle())
	b.WriteString("\n")
	b.WriteString(m.vp.View())
	b.WriteString("\n")
	return b.String()
}

func (m model) laneRow(index, number int) string {
	ln := m.lanes[number]
	marker := "  "
	if index == m.selected {
		marker = "▸ "
	}
	row := fmt.Sprintf("%s%2d  %-12s %-12s %s", marker, number, ln.name(), ln.queueName(), ln.status())
	if index == m.selected {
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

func (m model) logTitle() string {
	label := "no lane selected"
	if ln := m.selectedLane(); ln != nil {
		switch {
		case ln.logPath == "":
			label = "no log yet"
		case ln.state == laneIdle:
			label = "last log of lane " + fmt.Sprint(m.order[m.selected])
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
