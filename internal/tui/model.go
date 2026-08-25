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

// Ejector is the escape hatch (SCOPE, "The interface"): stop a lane's agent
// and hand back the runner's own resume command, so the headless run becomes
// the operator's interactive session. It is Reconciler.Eject in production.
// CanEject answers for a queue rather than a run, because the panel has to
// know before the key is pressed whether to offer it, and with what reason
// when it cannot.
type Ejector interface {
	Eject(ctx context.Context, ticketID string) (loop.Ejection, error)
	CanEject(queue string) (bool, string)
}

// Starter is the TUI's other write action: running one selected queued
// ticket now, past the lane limit. It is Reconciler.ForceStart in
// production, and it overrides exactly one thing — the lane count. Every
// refusal lives behind it, decided against the board rather than against a
// snapshot up to an interval old.
type Starter interface {
	ForceStart(ctx context.Context, ticketID string) error
}

// Reader is the inbox pane's one read beyond the pass: the body and
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
	Ejector  Ejector
	Starter  Starter
	Reader   Reader
	Statuses []string      // promote targets: configured queue statuses, plus the pipeline's exits
	Interval time.Duration // tick cadence; loop.DefaultInterval when zero
	Lanes    int           // N: at most this many agents at once
	Events   <-chan loop.Event
}

func (o Options) validate() error {
	switch {
	case o.Ticker == nil:
		return fmt.Errorf("tui: ticker is required")
	case o.Promoter == nil:
		return fmt.Errorf("tui: promoter is required")
	case o.Ejector == nil:
		return fmt.Errorf("tui: ejector is required")
	case o.Starter == nil:
		return fmt.Errorf("tui: starter is required")
	case o.Reader == nil:
		return fmt.Errorf("tui: reader is required")
	case o.Lanes < 1:
		return fmt.Errorf("tui: lanes must be at least 1")
	case o.Events == nil:
		return fmt.Errorf("tui: events channel is required")
	}
	return nil
}

// panel is one of the two side panels — SCOPE's two questions, both on
// screen at once. Focus decides where the selection keys go; the lens the
// main pane shows is the selected row's, not the panel's.
type panel int

const (
	panelAttention panel = iota
	panelWork
)

func (p panel) String() string {
	if p == panelAttention {
		return "inbox"
	}
	return "work"
}

// sortMode is the inbox table's one control. Sorting is grouping: the
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

// defaultSort is the mode the inbox opens on. Grouping by status puts the
// two things most worth acting on — where runs fail, then where they
// finish — at the top under headers that say so, where a flat list buries
// a nearly-done ticket among the backlog rows. Leverage keeps its
// throughput job as the order within each group, one `s` away as a mode.
const defaultSort = sortStatus

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

// laneState is the runner state a lane is in.
type laneState int

const (
	laneIdle         laneState = iota // no agent
	laneProvisioning                  // claimed; the workspace is being prepared
	laneRunning                       // an agent this process started
	laneAdopted                       // a live agent inherited from a previous process
)

// lane is one concurrency slot, maintained purely from loop events. It no
// longer owns a row of its own: the work panel draws tickets, and a lane is
// how a ticket's row knows it is running (see workRow).
type lane struct {
	state    laneState
	runID    string
	ticketID string
	ticket   string // human identifier; empty for adopted runs
	queue    string
	logPath  string // survives the run, so the tail outlives the agent
	since    time.Time
	// pulse reads that log for what the run's row shows beyond its age: when
	// it last said anything, and the activity behind it. Nil until the first
	// poll finds a log path; a new run gets a new lane, so it never inherits
	// the last occupant's counts.
	pulse *pulse
}

const (
	// pollEvery is the redraw-and-tail cadence, independent of the loop's
	// ticks; it is also the animation clock for the heartbeat frames.
	pollEvery = 250 * time.Millisecond
	// detailDebounce is how long an inbox selection must hold still
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
	// ejectedMsg reports the outcome of an eject: the agent is dead and the
	// lane free, or it is untouched and err says why.
	ejectedMsg struct {
		ticket   string
		ejection loop.Ejection
		err      error
	}
	// forcedMsg reports the outcome of a force-start: the claim and the
	// provision it kicks off run off the render loop, like every other
	// write, so a slow Linear call never blocks a frame.
	forcedMsg struct {
		ticket string
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

// workRow is one selectable line of the work panel: a ticket, and the lane
// running it when one is. Rows are rebuilt from the queue snapshot and the
// lanes on every pass, so a row is a picture of one moment — it carries what
// drawing it needs rather than an index into state the next pass replaces.
type workRow struct {
	ticketID string
	ticket   string // human identifier, e.g. LERP-42
	title    string
	url      string
	queue    string // the queue whose group the row sits in
	status   string // that queue's Linear status; empty off the board
	team     string
	// lane is the lane running this ticket, 0 when nothing is; state and
	// since describe that run.
	lane  int
	state laneState
	since time.Time
	// heard is when that run's log last grew, zero while it has none; rate
	// is its recent activity per bucket, oldest first, for the sparkline.
	heard time.Time
	rate  []int
	// The pickup gate, for a ticket that is not running: where it sits in
	// its queue's order, and what holds it there.
	pos, of   int
	assigned  bool
	blockedBy []string
	eligible  bool
}

// workGroup is one queue as the panel draws it: a header, then its rows —
// what is running in it first, then what runs next.
type workGroup struct {
	name, status, team string
	// offBoard marks a group that is not a configured queue: a live run
	// whose ticket the pass no longer lists anywhere. The work is real, so
	// it keeps a group instead of vanishing.
	offBoard bool
	rows     []workRow
}

type model struct {
	o   Options
	ctx context.Context

	focus         panel
	width, height int
	ready         bool
	helpOn        bool

	lanes map[int]*lane
	order []int // lane numbers, sorted; adopted runs may sit above N
	// lastLog is where a ticket's last finished run wrote its log, kept so
	// the work panel can still show it once the lane is gone — the run's
	// row was the only door to it, and that row disappears with the run.
	lastLog map[string]string

	// queues is the loop's latest queue snapshot, replaced wholesale on every
	// pass; nil until the first pass reports. It is display state only — the
	// work panel edits nothing (SCOPE: not a Linear client).
	queues []loop.QueueSnapshot
	// The work panel selects a ticket, not a row: workSel is the selected
	// ticket's ID, and it survives the row moving — to another queue, into a
	// lane, up or down its group. workPos is where that row last sat, the
	// fallback for a ticket that has left the panel entirely.
	workSel string
	workPos int

	// attention is the loop's latest full list of what waits on the operator;
	// attentionSeen separates "no pass has reported yet" from the goal state,
	// so an empty panel never claims the inbox is empty before it is known.
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
	// sortMode starts at defaultSort, so it is set in newModel rather than
	// left to the zero value.
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

	// detailOpen is whether the main pane is open, per panel — a panel
	// doubles as its index, the way geometry's wants and floors are indexed.
	// The list owns the screen until the operator asks for the detail, and
	// each panel remembers the answer: the inbox's detail is something you
	// open once you have decided to read a ticket, a running ticket's live
	// log is the point of watching it. A display default, not a rule about
	// process, and session-only like sort and the project filter.
	detailOpen [2]bool

	// promoting is the promote picker's open/closed state; promoteSel is its
	// selected index into o.Statuses. Opened by "p" on a selected inbox
	// item, closed by confirming, cancelling, or the list going empty.
	promoting  bool
	promoteSel int

	// ejecting is the eject confirm overlay's open/closed state, and ejectRow
	// the row it is about — captured when the key was pressed, not re-read
	// when enter lands. A pass between the two moves the cursor (the row's
	// own ticket may leave the panel entirely), and killing whatever agent
	// the cursor ended up on is not what the operator confirmed.
	//
	// ejection is the result panel that replaces the confirm, nil when none
	// is up. That panel is sticky — dismissed by esc, never by the next
	// pass — because the resume command it holds is the one string the
	// operator has to copy, and a status-bar note would be cleared out from
	// under them.
	ejecting bool
	ejectRow workRow
	ejection *loop.Ejection

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

	lastErr string
	// notes are this interval's transient reports — run outcomes, a
	// promote's result — in arrival order, cleared at the next pass. A
	// single slot could not hold them: with N lanes, two runs settling
	// inside one interval is routine, and the second silently overwrote the
	// first. The status bar renders them all, and renders them alongside
	// lastErr rather than behind it, so a broken queue listing cannot hide
	// the fact that a run failed.
	notes      []note
	passHadErr bool // an error event arrived during the pass now in flight
	// logOffset parks the log pane's scroll position while the selection is
	// on a row that has no log, so walking past a pending ticket and back
	// returns to where the operator was rather than to the top.
	logOffset int

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
	// bubbles renders the panels' key line, so it takes the theme's faint
	// and the "·" the rest of the TUI separates facts with rather than the
	// component's own grey and bullet. The ? overlay keeps bubbles' own two
	// greys: its key and description columns are what tell it apart from a
	// wall of text.
	h.ShortSeparator = " · "
	h.Styles.ShortKey = styleFaint
	h.Styles.ShortDesc = styleFaint
	h.Styles.ShortSeparator = styleFaint
	h.Styles.Ellipsis = styleFaint
	m := model{o: o, ctx: ctx, focus: panelWork, lanes: make(map[int]*lane),
		details: make(map[string]*ticketDetail), lastLog: make(map[string]string),
		vp: viewport.New(0, 0), follow: true, keys: newKeymap(), help: h,
		sortMode:   defaultSort,
		detailOpen: [2]bool{panelAttention: false, panelWork: true},
		inFlight:   true, // Init starts the first pass immediately
		passes:     &sync.WaitGroup{}}
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
		// A window that shrank below the pane's floor closes the two things
		// that took it modally. detailOpen is the operator's own preference
		// and survives — esc is on the too-small screen — but a picker left
		// live behind that screen would still take the enter that writes to
		// Linear, and the overlay would still eat the keyboard.
		if !m.roomForMain() {
			m.promoting, m.helpOn = false, false
		}
		m.layout()
		m.refreshMain()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tickMsg:
		m.passHadErr = false
		m.notes = nil // a new pass starting is the "transient" in transient note
		m.inFlight = true
		return m, m.runTick()
	case tickedMsg:
		// A pass that produced no error supersedes whatever error line an
		// earlier one left. Run outcomes are notes, not errors, and are
		// cleared by the next pass starting rather than by this one ending.
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
		m.readPulses()
		if m.tail.read() && m.showingLog() {
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
	case ejectedMsg:
		if msg.err != nil {
			m.lastErr = clean(fmt.Sprintf("eject %s: %v", msg.ticket, msg.err))
		} else {
			// No note here: the loop's own EventEjected leaves one as the
			// lane frees, and the panel below says far more than a note can.
			ej := msg.ejection
			m.ejection = &ej
			m.layout()
		}
		return m, nil
	case promotedMsg:
		if msg.err != nil {
			m.lastErr = clean(fmt.Sprintf("promote %s to %s: %v", msg.ticket, msg.status, msg.err))
		} else {
			m.note(fmt.Sprintf("promoted %s to %s", msg.ticket, msg.status), false)
		}
		return m, nil
	case forcedMsg:
		if msg.err != nil {
			m.lastErr = clean(msg.err.Error())
		} else {
			m.note("force-started "+msg.ticket, false)
		}
		return m, nil
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.promoting {
		return m.handlePromoteKey(msg)
	}
	if m.ejecting || m.ejection != nil {
		return m.handleEjectKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		// Closing it never needs the room opening it did.
		if m.helpOn || m.roomForMain() {
			m.helpOn = !m.helpOn
		}
	case key.Matches(msg, m.keys.Attention):
		m.setFocus(panelAttention)
	case key.Matches(msg, m.keys.Work):
		m.setFocus(panelWork)
	case key.Matches(msg, m.keys.NextPanel):
		m.setFocus((m.focus + 1) % 2)
	case key.Matches(msg, m.keys.PrevPanel):
		m.setFocus((m.focus + 1) % 2)
	case key.Matches(msg, m.keys.Up):
		m.moveSelection(-1)
	case key.Matches(msg, m.keys.Down):
		m.moveSelection(1)
	// enter opens the focused panel's detail and esc closes it — neither
	// flips. esc inside the promote picker still cancels the picker, because
	// handlePromoteKey ran before this switch and returned.
	case key.Matches(msg, m.keys.Detail):
		// Not under the overlay: it is what is on screen, and enter behind it
		// would open a pane nobody asked for and read a ticket nobody sees.
		if m.roomForMain() && !m.helpOn {
			m.detailOpen[m.focus] = true
			// Size before filling: the viewport was zero-width while the
			// pane was closed, and refreshMain wraps to that width.
			m.layout()
			m.refreshMain()
		}
	case key.Matches(msg, m.keys.Close):
		// esc closes the overlay first, the way it cancels the picker: a key
		// acts on what is on screen.
		if m.helpOn {
			m.helpOn = false
			break
		}
		m.detailOpen[m.focus] = false
	case key.Matches(msg, m.keys.Promote):
		if m.focus == panelAttention && len(m.shown) > 0 && len(m.o.Statuses) > 0 && m.roomForMain() {
			m.promoting = true
			m.promoteSel = 0
		}
	case key.Matches(msg, m.keys.Eject):
		m.startEject()
	case key.Matches(msg, m.keys.ForceStart):
		// No gate here beyond having a row: every refusal is the
		// reconciler's, decided against the board rather than against a
		// snapshot up to an interval old. Pressing S on a running row gets
		// its refusal back like any other — "already claimed", since a run
		// this lerp started holds the ticket.
		if m.focus == panelWork {
			if r := m.selectedWork(); r != nil && r.ticketID != "" {
				return m, m.doForceStart(r.ticketID, r.ticket)
			}
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
	// A closed pane shows nothing to scroll, so these keys are inert rather
	// than silently unfollowing a log the operator cannot see and parking it
	// at the top for when it reopens.
	case !m.mainOpen() && (key.Matches(msg, m.keys.PageUp) || key.Matches(msg, m.keys.PageDown) ||
		key.Matches(msg, m.keys.Top) || key.Matches(msg, m.keys.Bottom)):
	// The scroll keys move whatever the main pane shows; follow is the log's
	// state alone, so a detour through a detail lens can never freeze the tail.
	case key.Matches(msg, m.keys.PageUp):
		m.vp.ViewUp()
		if m.showingLog() {
			m.follow = m.vp.AtBottom()
		}
	case key.Matches(msg, m.keys.PageDown):
		m.vp.ViewDown()
		if m.showingLog() {
			m.follow = m.vp.AtBottom()
		}
	case key.Matches(msg, m.keys.Top):
		m.vp.GotoTop()
		if m.showingLog() {
			m.follow = false
		}
	case key.Matches(msg, m.keys.Bottom):
		m.vp.GotoBottom()
		if m.showingLog() {
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

// wantDetail points the pane at the current inbox selection. It only
// schedules: the read waits for the selection to settle, so holding j down
// a fifteen-row list schedules fifteen ticks and fires one fetch.
func (m *model) wantDetail() tea.Cmd {
	// A shut pane is nobody reading: walking the inbox with it closed costs
	// no Linear calls at all, and enter is what asks for one. The panel's own
	// flag, not mainOpen: the promote picker and the ? overlay take the pane
	// for themselves and never render the detail, so neither should fetch it.
	if m.focus != panelAttention || !m.detailOpen[m.focus] {
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
	// The pane may have closed inside the debounce, which is a quarter of a
	// second the operator can shut it in. Forget what it wanted as well as
	// dropping the read: wantDetail refuses to schedule the ticket it is
	// already pointed at, so a detailWant left standing for a read that
	// never happened is a row that stays blank however often it is reopened.
	if !m.detailOpen[panelAttention] {
		m.detailWant = ""
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
// inbox panel's selected ticket, or back out without touching Linear.
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
	// The picker and the lens under it are the same box now, so this is not
	// resizing anything — it is the clamp. A pass landing while the picker
	// was open can swap a long detail for a short one under a scrolled
	// viewport, and layout re-pins the offset inside the content before the
	// pane is handed back.
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

// handleEjectKey drives both halves of eject: the confirm overlay, where
// enter stops the agent and esc backs out having touched nothing, and the
// result panel that follows, which esc dismisses. Both are modal, like the
// promote picker: the second one holds the resume command, and a keystroke
// meant for the board must not throw it away.
func (m model) handleEjectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.ejecting = false
		m.ejection = nil
	case "enter":
		if m.ejecting {
			m.ejecting = false
			cmd = m.doEject(m.ejectRow.ticketID, m.ejectRow.ticket)
		}
	}
	// Leaving either overlay hands the main pane back to a lens of a
	// different height; re-fit before the next frame draws into the old one.
	m.layout()
	return m, cmd
}

// doEject stops the agent off the render loop, like every other call into the
// loop. It writes nothing to Linear: the ticket keeps its claim and its
// status, because ejecting is taking the work over rather than dropping it.
func (m model) doEject(ticketID, ticket string) tea.Cmd {
	return func() tea.Msg {
		ejection, err := m.o.Ejector.Eject(m.ctx, ticketID)
		return ejectedMsg{ticket: ticket, ejection: ejection, err: err}
	}
}

// startEject opens the confirm for the selected row, capturing it, or says
// why it cannot. A queue whose runner has no resume command is the one
// refusal worth a word: the row looks exactly like an ejectable one, and the
// key line's silence about "e" is easy to miss. The others — a ticket that
// is not running, a lane still provisioning — are plain on the row itself.
func (m *model) startEject() {
	if m.focus != panelWork {
		return
	}
	r := m.selectedWork()
	if r == nil || r.lane == 0 || r.state == laneProvisioning {
		return
	}
	if can, why := m.o.Ejector.CanEject(r.queue); !can {
		m.note("cannot eject "+r.ticket+": "+clean(why), true)
		return
	}
	m.ejectRow, m.ejecting = *r, true
}

// canEjectSelected reports whether the work panel's selected row is a run
// eject could take over: a live agent, in a queue whose runner has a resume
// command. A provisioning lane has no agent yet and a waiting ticket has no
// run at all, so neither offers the key.
func (m *model) canEjectSelected() bool {
	if m.focus != panelWork {
		return false
	}
	r := m.selectedWork()
	if r == nil || r.lane == 0 || r.state == laneProvisioning {
		return false
	}
	can, _ := m.o.Ejector.CanEject(r.queue)
	return can
}

// ejectRowIsRunning reports whether the row the confirm captured is still a
// running row. A run that ended while the overlay was open has nothing left
// to eject.
func (m *model) ejectRowIsRunning() bool {
	for _, r := range m.workRows() {
		if r.ticketID == m.ejectRow.ticketID {
			return r.lane > 0
		}
	}
	return false
}

// doForceStart runs the second of the TUI's two writes off the render loop,
// for the same reason doPromote does: a claim plus a provision must never
// block a frame.
func (m model) doForceStart(ticketID, ticket string) tea.Cmd {
	return func() tea.Msg {
		err := m.o.Starter.ForceStart(m.ctx, ticketID)
		return forcedMsg{ticket: ticket, err: err}
	}
}

func (m *model) setFocus(p panel) {
	m.focus = p
	m.retarget()
	// Size before filling. The two panels remember the pane separately, so
	// focus moves the width the viewport wraps to — and content wrapped to
	// the outgoing panel's width would stay wrong until the next byte of log
	// or the next keystroke.
	m.layout()
	m.refreshMain()
	if !m.showingLog() {
		m.vp.GotoTop()
	}
}

// moveSelection moves within the focused panel. Neither selection is a
// position: the work panel's follows its ticket (see retargetWork), the
// inbox table's follows its own (see resort).
func (m *model) moveSelection(delta int) {
	switch m.focus {
	case panelWork:
		rows := m.workRows()
		if len(rows) == 0 {
			return
		}
		// A detour through a row with no log must not cost the operator the
		// place they had scrolled back to: one viewport serves both lenses,
		// so leaving parks the offset and arriving puts it back. Following
		// needs nothing parked — refreshLog pins it to the bottom.
		if m.showingLog() && !m.follow {
			m.logOffset = m.vp.YOffset
		}
		m.workPos = clampIndex(m.workPos+delta, len(rows))
		m.workSel = rows[m.workPos].ticketID
		m.retarget()
		m.refreshMain()
		switch {
		case !m.showingLog():
			m.vp.GotoTop()
		case !m.follow:
			// retarget leaves follow on for a log it just switched to, so
			// this only restores a log the operator was already reading.
			m.vp.SetYOffset(m.logOffset)
		}
	case panelAttention:
		m.attnSel = clampIndex(m.attnSel+delta, len(m.shown))
		m.refreshMain()
		m.vp.GotoTop()
	}
}

func clampIndex(i, n int) int {
	return max(0, min(i, n-1))
}

// selectedURL is what `o` opens: Linear's own URL for the selected item.
// Every row in either panel is a ticket now, so every row has a door —
// except a run whose ticket the pass no longer lists.
func (m *model) selectedURL() string {
	switch m.focus {
	case panelAttention:
		if it := m.selectedAttention(); it != nil {
			return it.URL
		}
	case panelWork:
		if r := m.selectedWork(); r != nil {
			return r.url
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
		changed = panelWork
	case loop.EventStarted:
		m.lanes[ev.Lane] = &lane{state: laneRunning, runID: ev.RunID, ticketID: ev.TicketID,
			ticket: ev.Ticket, queue: ev.Queue, logPath: ev.LogPath, since: eventSince(ev)}
		changed = panelWork
	case loop.EventAdopted:
		m.lanes[ev.Lane] = &lane{state: laneAdopted, runID: ev.RunID, ticketID: ev.TicketID,
			queue: ev.Queue, logPath: ev.LogPath, since: eventSince(ev)}
		changed = panelWork
	case loop.EventExited:
		// How the run ended goes on the status bar, and only there: see
		// settle for why the ticket's own row cannot hold it.
		note := fmt.Sprintf("%s exited %d", ev.Ticket, ev.ExitCode)
		if ev.Err != nil {
			note += " (move failed)"
		}
		warn := ev.ExitCode != 0 || ev.Err != nil
		if ev.Note != "" {
			// A hop the loop skipped is the larger story — a stage of the
			// operator's pipeline did not run — so it replaces the plain
			// outcome rather than crowding the line beside it.
			note, warn = ev.Note, true
		}
		m.note(note, warn)
		m.settle(ev)
		changed = panelWork
	case loop.EventEjected:
		// Not a warning: the operator asked for this, and the run ending is
		// the point rather than a surprise. The resume command is on the
		// result panel, which is where they are looking.
		m.note(ejectedNote(ev), false)
		m.settle(ev)
		changed = panelWork
	case loop.EventReaped:
		m.note(reapedNote(ev), true)
		m.settle(ev)
		changed = panelWork
	case loop.EventError:
		if ev.Lane > 0 {
			// A lane's failure used to sit on its row as a note until the
			// lane was reused. The rows are gone, and lastErr alone names the
			// ticket by its Linear id rather than its identifier — so record
			// the identifier too, or the operator cannot tell whose run died.
			if ev.Ticket != "" {
				m.note(ev.Ticket+": run failed, see its log", true)
			}
			m.settle(ev)
			changed = panelWork
		}
	case loop.EventQueues:
		m.queues = ev.Queues
		changed = panelWork
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
	m.retargetWork()
	// A run that ended while the confirm was open leaves nothing to kill, so
	// the overlay closes rather than sending enter after a dead agent — the
	// same care the promote picker takes when its list empties.
	if m.ejecting && !m.ejectRowIsRunning() {
		m.ejecting = false
	}
	m.layout()
	m.retarget()
	// Only the lens this event feeds is re-rendered; a live log also
	// refreshes through retarget and the poll.
	if changed == m.focus {
		m.refreshMain()
	}
}

// settle frees the lane a run just left. The log outlives the lane: the
// ticket keeps a pointer to it, so selecting the ticket still shows what the
// agent did once the run is over and the file may already be gone.
//
// How the run *ended* is deliberately not kept here. A finished run has
// already moved its ticket — to on_success, on_failure, or wherever the
// agent left it — so a note pinned to the ticket's row would sit in a group
// the ticket has just left, or on no row at all. The status bar is the one
// surface that outlives the row, so apply puts the outcome there.
func (m *model) settle(ev loop.Event) {
	ln := m.lanes[ev.Lane]
	if ln == nil {
		return
	}
	if ev.RunID != "" && ln.runID != "" && ln.runID != ev.RunID {
		return // a newer occupant already owns the lane
	}
	logPath := ev.LogPath
	if logPath == "" {
		logPath = ln.logPath
	}
	if ln.ticketID != "" && logPath != "" {
		m.lastLog[ln.ticketID] = logPath
	}
	if ev.Lane > m.o.Lanes {
		delete(m.lanes, ev.Lane) // the lane existed only for an adopted run
		return
	}
	// The idle lane keeps no log path: workGroups skips idle lanes, so no row
	// can point at one. lastLog above is where a finished run's log lives now.
	m.lanes[ev.Lane] = &lane{state: laneIdle}
}

// eventSince prefers the loop's own start time, so an adopted run's elapsed
// clock shows the run's true age, not the moment this process learned of it.
func eventSince(ev loop.Event) time.Time {
	if !ev.StartedAt.IsZero() {
		return ev.StartedAt
	}
	return time.Now()
}

// reorder rebuilds the lane order — the numbers, sorted — so running rows
// keep a stable order within their group as adopted lanes appear and vanish
// above them. What the operator selected is a ticket, not a lane number;
// retargetWork is what keeps that.
func (m *model) reorder() {
	m.order = m.order[:0]
	for n := range m.lanes {
		m.order = append(m.order, n)
	}
	slices.Sort(m.order)
}

// retargetWork re-finds the selected ticket after a rebuild. The merged list
// reorders under the cursor far more than the lane rows ever did — a ticket
// starts running, finishes, changes queue — so the selection keys on the
// ticket ID and follows it wherever the row went. Only when the ticket has
// left the panel does the cursor fall back to the nearest remaining row,
// which is the rule the lane rows used.
func (m *model) retargetWork() {
	rows := m.workRows()
	if len(rows) == 0 {
		m.workSel, m.workPos = "", 0
		return
	}
	if m.workSel != "" {
		if i := slices.IndexFunc(rows, func(r workRow) bool { return r.ticketID == m.workSel }); i >= 0 {
			m.workPos = i
			return
		}
	}
	m.workPos = clampIndex(m.workPos, len(rows))
	m.workSel = rows[m.workPos].ticketID
}

// retarget points the tail at the selected row's log. Reattaching only on a
// path change keeps the scrollback across renders and across the run's exit —
// the buffer is the operator's copy of a log whose file may already be gone.
// A row with no log detaches nothing: walking through what runs next and back
// must not cost the operator the tail they were reading.
func (m *model) retarget() {
	path := m.selectedLogPath()
	if path == "" || path == m.tail.path {
		return
	}
	m.tail = newTail(path)
	m.follow = true
	m.tail.read()
	m.refreshLog()
}

// readPulses reads every live lane's log for what its row shows about the
// run: when the log last grew, and the activity behind it. It rides the same
// 250ms poll as the tail and the heartbeat — one clock for everything.
//
// The selected lane's log is read twice, once here and once by the tail. The
// two want different positions in the same file — the tail holds a
// scrollback, the pulse only counts what arrives — and a poll that finds
// nothing new costs a stat, so the duplication buys each of them its own
// place for a price the poll was already paying.
//
// This does decode every live lane's log, where before only the selected
// one was parsed at all. That is the count being of events rather than of
// bytes, which is what makes it degrade with logfmt; an agent writes a few
// kilobytes a second and a whole tool result is one line, so the poll's
// share of a 250ms budget stays small.
func (m *model) readPulses() {
	now := time.Now()
	for _, ln := range m.lanes {
		if ln.state == laneIdle || ln.logPath == "" {
			continue
		}
		if ln.pulse == nil {
			ln.pulse = newPulse(ln.logPath)
		}
		ln.pulse.read(now)
	}
}

// selectedLogPath is the log behind the selected row: the live lane's while a
// run holds it, then the one that run left behind.
func (m *model) selectedLogPath() string {
	r := m.selectedWork()
	if r == nil {
		return ""
	}
	if r.lane > 0 {
		if ln := m.lanes[r.lane]; ln != nil && ln.logPath != "" {
			return ln.logPath
		}
	}
	return m.lastLog[r.ticketID]
}

// showingLog reports whether the main pane is the log rather than a detail.
// The lens is the selected row's, not the panel's: a ticket with a log shows
// it, a ticket without shows what the pass knows about it.
func (m *model) showingLog() bool {
	return m.focus == panelWork && m.selectedLogPath() != ""
}

// mainOpen is the single question everything else asks: is the main pane on
// screen? The focused panel's own state, forced open by the promote picker
// and the ? overlay — both live in that pane, so they take the width while
// they are up and hand it back when they close. That keeps p working from a
// closed inbox, the default, without a second rendering path.
func (m *model) mainOpen() bool {
	return m.promoting || m.helpOn || m.detailOpen[m.focus]
}

// roomForMain reports whether this window can hold the main pane at all —
// View's own guard, asked of the pane rather than of the frame in hand.
//
// The keys that open the pane ask first. With the pane shut a short terminal
// is a usable screen, which is the point — but a key that turned that screen
// into "window too small" would take the panels away, and with the promote
// picker it would take them away while the picker was still live and still
// taking the enter that writes to Linear. A key that does nothing, and that
// the panel line and the status bar both stop offering, is the better half
// of that trade.
func (m *model) roomForMain() bool {
	return m.width >= minWidth && m.height >= m.minHeight(true)
}

// refreshMain points the main pane's viewport at whatever the selection asks
// for. Scroll position is the caller's concern — focus and selection changes
// jump to the top; a data refresh keeps the operator's place.
func (m *model) refreshMain() {
	if !m.mainOpen() {
		return
	}
	if m.showingLog() {
		m.refreshLog()
		return
	}
	// The viewport's width is the pane's inner width, so prose wrapped here
	// is wrapped to the columns it will be drawn in — panelBox truncates its
	// rows rather than wrapping them, and a line too long is a line cut.
	m.vp.SetContent(m.detail(m.vp.Width))
}

// detail is the read-only lens the main pane shows for a selection with no
// log. width is the pane's inner width, which the inbox lens wraps prose to.
func (m *model) detail(width int) string {
	if m.focus == panelWork {
		return m.workDetail()
	}
	return m.attentionDetail(width)
}

// refreshLog points the pane at the selected row's log: the decoded view of
// what the agent is doing, or — with the raw toggle on — the bytes the runner
// wrote. Nothing but the rendering differs between the two; the file on disk
// and the scrollback are the same either way.
func (m *model) refreshLog() {
	if !m.mainOpen() || !m.showingLog() {
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

// workGroups is the merged list: one group per configured queue, in the
// loop's own order, holding the tickets running in it first — by lane
// number, which is stable — then what runs next in pickup order. Running and
// pending are one question about the same tickets, which is why they are one
// list (SCOPE, the interface).
func (m *model) workGroups() []workGroup {
	groups := make([]workGroup, 0, len(m.queues)+1)
	for _, q := range m.queues {
		groups = append(groups, workGroup{name: q.Name, status: q.Status, team: q.Team})
	}
	running := make(map[string]bool, len(m.order))
	for _, n := range m.order {
		ln := m.lanes[n]
		if ln.state == laneIdle {
			continue
		}
		row := workRow{ticketID: ln.ticketID, ticket: ln.name(), queue: ln.queue,
			lane: n, state: ln.state, since: ln.since}
		if ln.pulse != nil {
			row.heard, row.rate = ln.pulse.heard, ln.pulse.window()
		}
		// A running ticket normally still sits in its queue's listing,
		// claimed and ineligible: that listing is the group, and it carries
		// the ticket's title and URL. Failing that, the queue the run started
		// from; failing that, a group of its own — an adopted run, or one
		// whose agent moved its own ticket mid-run, must not vanish.
		gi := -1
		if qi, ti := m.findQueueTicket(ln.ticketID); qi >= 0 {
			tk := m.queues[qi].Tickets[ti]
			row.ticket, row.title, row.url = tk.Identifier, tk.Title, tk.URL
			row.assigned, row.blockedBy = tk.Assigned, tk.BlockedBy
			gi = qi
		} else {
			// An off-board group already opened for this queue counts: the
			// second inherited run from one queue belongs under the first
			// one's header, not under a duplicate of it.
			gi = slices.IndexFunc(groups, func(g workGroup) bool { return g.name == ln.queue })
		}
		if gi < 0 {
			groups = append(groups, workGroup{name: ln.queue, offBoard: true})
			gi = len(groups) - 1
		}
		row.queue, row.status, row.team = groups[gi].name, groups[gi].status, groups[gi].team
		groups[gi].rows = append(groups[gi].rows, row)
		if ln.ticketID != "" {
			running[ln.ticketID] = true
		}
	}
	for qi, q := range m.queues {
		var pending []workRow
		for _, tk := range q.Tickets {
			if running[tk.ID] {
				continue // already on screen as a running row
			}
			pending = append(pending, workRow{ticketID: tk.ID, ticket: tk.Identifier,
				title: tk.Title, url: tk.URL, queue: q.Name, status: q.Status, team: q.Team,
				assigned: tk.Assigned, blockedBy: tk.BlockedBy, eligible: tk.Eligible})
		}
		for i := range pending {
			pending[i].pos, pending[i].of = i+1, len(pending)
		}
		groups[qi].rows = append(groups[qi].rows, pending...)
	}
	return groups
}

// findQueueTicket locates a ticket in the pass's snapshot: queue index and
// ticket index, or -1, -1.
func (m *model) findQueueTicket(ticketID string) (int, int) {
	if ticketID == "" {
		return -1, -1
	}
	for qi, q := range m.queues {
		for ti := range q.Tickets {
			if q.Tickets[ti].ID == ticketID {
				return qi, ti
			}
		}
	}
	return -1, -1
}

// workRows flattens the groups into the selectable rows, in screen order.
func (m *model) workRows() []workRow {
	var rows []workRow
	for _, g := range m.workGroups() {
		rows = append(rows, g.rows...)
	}
	return rows
}

// selectedWork is the row under the cursor, nil when the panel has no rows —
// the one place that owns the empty case.
func (m *model) selectedWork() *workRow {
	rows := m.workRows()
	if len(rows) == 0 {
		return nil
	}
	r := rows[clampIndex(m.workPos, len(rows))]
	return &r
}

// selectedAttention is the inbox selection, nil when nothing is shown —
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

// geometry is the screen's arithmetic. One rule: needs-you gets the room.
// Work is the smaller panel because it is the smaller question — it asks
// for the rows it renders, held to about a third of the panel stack, and
// needs-you takes everything left over. Focus is not in the arithmetic at
// all: moving between panels never moves the geometry, and a panel that is
// quiet keeps its border rather than collapsing to a line. The main pane is
// not in that split: wide, it is a column of its own and fills the body;
// stacked, it takes half the body and the panels share the rest. Heights
// include borders, and the stack always fits bodyH so the status bar stays
// on screen.
type geometry struct {
	wide         bool
	sideW, mainW int
	bodyH        int
	attnH, workH int
	mainH        int
}

const (
	// panelFloor is the smallest a panel is drawn at: a border, two rows, a
	// border. Two, not one: with a single row windowRows has nothing to
	// window (its own guard gives up under two lines) and panelBox spends
	// that row on the "⋯ n more" marker, so a panel holding a list would
	// render none of it — including the row the selection is on.
	// mainFloor is the same for the main pane in the stacked layout, where
	// it shares the body with the panels.
	panelFloor = 4
	mainFloor  = 5
	// minWidth is where four columns of table and a border stop being a
	// screen at all, whatever the height.
	minWidth = 24
)

// minHeight is the shortest window View will draw: both panels' floors and
// the status bar, plus the main pane's floor when the layout stacks and the
// pane is on screen — stacked, the pane comes out of the same body. withMain
// is the caller's question: View asks about the pane it is about to draw,
// roomForMain about the pane a key would open.
func (m *model) minHeight(withMain bool) int {
	h := 2*panelFloor + 1
	if withMain && m.width < narrowWidth {
		h += mainFloor
	}
	return h
}

func (m *model) geometry() geometry {
	g := geometry{bodyH: max(4, m.height-1)}
	g.wide = m.width >= narrowWidth
	// Widths first: the row builders lay their rows out to the panel width,
	// and work's want is counted from those very rows.
	// A closed pane is not a claimant: the panels take the whole width, which
	// is the point of the key — a 45% column spent on something nobody is
	// reading is why titles truncate.
	open := m.mainOpen()
	g.sideW, g.mainW = m.width, 0
	if open {
		g.mainW = m.width
		if g.wide {
			// Four columns need the room: a third of the terminal truncated
			// the status column out of a real backlog. No resize key — the
			// split is a proportion of the window, and the window is the knob.
			g.sideW = max(28, m.width*45/100)
			g.mainW = m.width - g.sideW
		}
	}
	// Wants come from the same row builders the panels draw with, so the
	// counts can never drift from what lands on screen.
	workRows, _ := m.workListRows(padList.inner(g.sideW))
	attnRows, _ := m.attentionRows(padList.inner(g.sideW))

	stackH := g.bodyH
	switch {
	case !open:
		// mainH stays 0 and the whole body is the stack's: fitPanels hands
		// it to the two panels exactly as it does the rest of the time.
	case g.wide:
		// The main pane has the other column to itself, so it takes the whole
		// of it. Fitting it to its content ended the box a third of the way
		// down the screen on a short ticket and at the bottom on a long one,
		// with dead space under it and no visible reason why — a pane that
		// changes size with its contents reads as a glitch rather than as a
		// rule. It competes with nothing here, so there was nothing to win by
		// fitting. What it holds scrolls.
		g.mainH = g.bodyH
	default:
		// Stacked, the body is split rather than fitted: half the screen is
		// the board, half is whatever the selected row opens. Fitting the
		// main pane to its content here would put focus straight back into
		// the arithmetic — the log lens asks for the whole body and opens on
		// focusing work — and both panels would jump on the keystroke. What
		// the lens holds scrolls; a panel that shrank under the operator
		// does not.
		g.mainH = fitH(g.bodyH/2, mainFloor, g.bodyH-2*panelFloor)
		stackH = g.bodyH - g.mainH
	}
	g.workH = workHeight(stackH, m.panelWant(panelWork, len(workRows)), m.panelWant(panelAttention, len(attnRows)))
	g.attnH = stackH - g.workH
	return g
}

// panelWant is one panel's height: the rows it renders plus its borders, and
// one line more when it is the focused panel carrying key hints. Both the
// height bought here and the line panelBody draws ask keyHints, so the two
// can never disagree about whether the line is there.
func (m *model) panelWant(p panel, rows int) int {
	if m.focus == p && m.keyHints(p) {
		rows++
	}
	return rows + 2
}

// workHeight is work's share of the stack: the rows it renders plus its
// borders, held to about a third of the stack however much it holds and
// never under a panel's floor however little. A quiet work panel is still a
// panel, and a busy one still leaves needs-you the room.
//
// The third is a ceiling, not a reservation: work may take what needs-you
// has no rows to put in, so neither panel truncates its list while the
// other holds blank lines — and needs-you takes its two thirds back the
// moment it has the rows to fill them. Content decides that; focus never
// does. In a window too short for both floors needs-you keeps its own
// first, being the panel the backlog is in.
func workHeight(stackH, want, attnWant int) int {
	share := max(stackH/3, stackH-attnWant)
	return fitH(want, panelFloor, min(max(panelFloor, share), stackH-panelFloor))
}

// fitH holds a height between lo and hi, and hi wins a conflict: the body
// is a hard limit where the floors are a preference, so a window under both
// shrinks its panels to nothing rather than push the status bar off screen.
func fitH(h, lo, hi int) int {
	return max(0, min(max(h, lo), hi))
}

// layout sizes the main pane's viewport from the geometry.
func (m *model) layout() {
	if !m.ready {
		return
	}
	g := m.geometry()
	m.vp.Width = max(0, padMain.inner(g.mainW))
	m.vp.Height = max(1, g.mainH-2)
	m.help.Width = m.vp.Width
	// A pane that just changed height holds a scroll position measured
	// against the old one: re-pin a followed log to the bottom, and clamp
	// anything else back inside the new box.
	if m.showingLog() && m.follow {
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
	// floor when the layout stacks and the pane is on screen — geometry can
	// only produce a screen taller than the terminal. Say so instead of
	// rendering one.
	if m.width < minWidth || m.height < m.minHeight(m.mainOpen()) {
		// When the pane is the whole of what does not fit, name the key that
		// gives the window back: this frame has no status bar to carry the
		// hint, and a terminal that starts this short starts with work's
		// pane open.
		if m.width >= minWidth && m.height >= m.minHeight(false) {
			return "lerp — window too small\nesc closes the pane\n"
		}
		return "lerp — window too small\n"
	}
	g := m.geometry()
	side := lipgloss.JoinVertical(lipgloss.Left,
		m.attentionPanel(g.sideW, g.attnH),
		m.workPanel(g.sideW, g.workH))
	// With the pane closed, side is the whole body — joined with an empty
	// string it would still cost a column or a row.
	body := side
	if m.mainOpen() {
		main := m.mainPanel(g.mainW, g.mainH)
		if g.wide {
			body = lipgloss.JoinHorizontal(lipgloss.Top, side, main)
		} else {
			body = lipgloss.JoinVertical(lipgloss.Left, side, main)
		}
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

// panelBody is what a panel draws inside its border: its list rows, windowed
// so the focused panel's selection stays visible, and — on the focused panel
// — the key hints on the last line. A panel squeezed down to a single row
// keeps the row and drops the hints: a key line over an empty body says what
// the keys do to nothing.
func (m *model) panelBody(p panel, rows []string, cur cursor, width, ih int) []string {
	if m.focus != p {
		return rows
	}
	// The hint costs a line, and it is only affordable when the rows can
	// still be shown in what is left: either they fit outright, or two lines
	// remain, which is the least windowRows needs to keep the selection
	// visible. Below that the rows win — a panel showing only "⋯ n more" has
	// lost the cursor the keys move.
	hint := ""
	if ih >= 3 || (ih == 2 && len(rows) <= 1) {
		hint = m.keyHint(p, width)
	}
	if hint != "" {
		ih--
	}
	if cur.at >= 0 {
		rows = windowRows(rows, cur, ih)
	}
	if hint == "" {
		return rows
	}
	// Pinned to the last line rather than left floating under the rows: a
	// focused panel absorbs the layout's slack, and a key line adrift in
	// that space reads as one more row.
	rows = fitRows(rows, ih)
	for len(rows) < ih {
		rows = append(rows, "")
	}
	return append(rows, hint)
}

// keyHint is the focused panel's key line, rendered by bubbles/help so it
// truncates to the panel rather than overflowing it. The model's own help
// component draws it, on a copy: the ? overlay owns m.help.Width.
func (m model) keyHint(p panel, width int) string {
	if width < 1 || !m.keyHints(p) {
		return ""
	}
	h := m.help
	h.Width = width
	return h.ShortHelpView(m.panelKeys(p))
}

// panelKeys is the focused panel's key line as bindings: what the row under
// its cursor answers to. A panel with no row under the cursor has none —
// the first frame, before any pass has reported, and the settled empty
// states, where every one of these keys is dead.
func (m *model) panelKeys(p panel) []key.Binding {
	switch p {
	case panelAttention:
		if m.selectedAttention() == nil {
			return nil
		}
	case panelWork:
		if m.selectedWork() == nil {
			return nil
		}
	}
	return m.keys.panelHelp(p, m.selectedLogPath() != "", m.selectedURL() != "",
		len(m.o.Statuses) > 0 && m.roomForMain(), m.canEjectSelected())
}

// keyHints reports whether the focused panel is carrying a key line at all:
// no overlay may have taken the keyboard — handleKey routes everything to the
// promote picker and to eject's two panels, so those keys would be dead — and
// the row under the cursor has to answer to something. Both the height
// panelWant buys and the line panelBody draws ask this, so the two can never
// disagree about whether the line is there.
func (m *model) keyHints(p panel) bool {
	return !m.modal() && len(m.panelKeys(p)) > 0
}

// modal reports whether an overlay owns the keyboard and the main pane.
func (m *model) modal() bool {
	return m.promoting || m.ejecting || m.ejection != nil
}

// marker renders the selection arrow for one row of a focused panel.
func marker(on bool) string {
	if on {
		return styleFocus.Render("▸ ")
	}
	return "  "
}

// attentionRows builds the inbox table's rows — under a grouping mode,
// a header above each run of them — and where the cursor sits among them,
// for the focus window. Every inbox row is one line.
func (m *model) attentionRows(width int) ([]string, cursor) {
	none := cursor{at: -1}
	switch {
	case !m.attentionSeen:
		return []string{styleFaint.Render("reading the board…")}, none
	case len(m.attention) == 0:
		return []string{styleFaint.Render("the inbox is empty")}, none
	case len(m.shown) == 0:
		return []string{styleFaint.Render("nothing in " + m.project)}, none
	}
	focused := m.focus == panelAttention
	// Every column is padded to the widest cell on the list, so the four of
	// them line up as columns worth scanning rather than as ragged text.
	idW, levW, statusW, projW := 0, 0, 0, 0
	for _, it := range m.shown {
		idW = max(idW, lipgloss.Width(it.Ticket))
		levW = max(levW, lipgloss.Width(leverageCell(it)))
		statusW = max(statusW, lipgloss.Width(statusText(it)))
		projW = max(projW, lipgloss.Width(projectName(it.Project)))
	}
	var rows []string
	sel := -1
	header := ""
	// A header separates one group from the next, so a list with a single
	// group draws none: there is no boundary left for it to mark, and on a
	// squeezed panel it costs the row or the key hint that line was worth
	// more as. It costs the group's derived note, which no row column
	// carries — but that note explains why a group ranks where it does, and
	// with one group there is no ranking to explain.
	grouped := m.sortMode.grouped() && !m.oneGroup()
	for i, it := range m.shown {
		if grouped {
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
		rows = append(rows, attentionRow(it, focused && i == m.attnSel, idW, levW, statusW, projW, width))
	}
	return rows, cursor{at: sel, span: 1}
}

// oneGroup reports whether every shown row falls under the same header.
// The rows are already in group order, so a single header over all of them
// means there is exactly one group. Only called with a non-empty list.
func (m *model) oneGroup() bool {
	first, _ := m.sortMode.header(m.shown[0])
	for _, it := range m.shown[1:] {
		if h, _ := m.sortMode.header(it); h != first {
			return false
		}
	}
	return true
}

// A column earns its width only while the title still reads as one. Below
// titleFloor the project drops out of the row entirely and the title takes
// the space back — a title cut shorter than this has stopped being a title,
// and the project is the one column a routing decision can most often do
// without. The priority is held to a lower bar, titleStub — what the column
// itself costs — because it is the fact a routing decision least often does
// without: it goes on paying for itself down to a title as narrow as the
// column is.
const (
	titleFloor = 20
	titleStub  = len("Urgent") + 2
)

// attentionRow is one waiting ticket as a table row: the fixed-width columns
// first, in a stable order — identifier, leverage, status, project, priority
// — and the title last, elastic, taking whatever the panel has left. Every
// fact a routing decision needs is on the line, so the choice can be made
// without selecting the row — which is the whole point of the panel.
//
// The cut lands at the right edge, on the title, where the part lost costs
// the least: the fixed columns are packed instead. Below titleFloor the
// project drops out and below titleStub the priority follows, each giving
// its width back to the title rather than holding it while the title reads
// as an ellipsis. Below that the fixed columns are all that is left and the
// title is the ellipsis; narrower still and the cut reaches the columns
// themselves, taking the status — the last of them, and the only one whose
// width the operator's own status vocabulary sets — before the identifier
// and the leverage, the two facts that make a row addressable at all, which
// survive any width.
func attentionRow(it loop.AttentionItem, selected bool, idW, levW, statusW, projW, width int) string {
	id := styleTicket.Render(it.Ticket) + strings.Repeat(" ", max(0, idW-lipgloss.Width(it.Ticket)))
	lev := leverageCell(it)
	lev += strings.Repeat(" ", max(0, levW-lipgloss.Width(lev)))
	// statusCell pads to the column and no further, so head carries the
	// gutter itself: every branch below ends in one, and a status wide
	// enough to leave no pad of its own still cannot touch the title.
	head := marker(selected) + id + " " + lev + " " + statusCell(it, statusW) + "  "
	full := head + projectCell(it.Project, projW) + "  " + priorityCell(it.Priority) + "  "
	noProject := head + priorityCell(it.Priority) + "  "
	// Both columns priced out and the identifier, the leverage and the status
	// are the last three things standing. Every row measures the same
	// columns, so the whole panel elides together and the titles stay in one
	// column.
	cols := head
	switch {
	case width-lipgloss.Width(full) >= titleFloor:
		cols = full
	case width-lipgloss.Width(noProject) >= titleStub:
		cols = noProject
	}
	return ansi.Truncate(cols+it.Title, max(0, width), "…")
}

// statusText is the row's status as it reads: the real Linear status name —
// the vocabulary the operator already chose, never a synonym invented here —
// and a mark for a status the configured pipeline never names. That mark is
// the fingerprint of a ticket that left the pipeline, worth seeing without
// selecting the row. The column is measured through this, so the mark is
// paid for in the column's own width rather than out of the gutter beside
// it — which the title would otherwise be buying for it, on every row.
func statusText(it loop.AttentionItem) string {
	if it.Relevance == loop.StatusUnnamed {
		return it.Status + " " + styleAttention.Render("⚠")
	}
	return it.Status
}

// statusCell is statusText padded out to the status column.
func statusCell(it loop.AttentionItem, w int) string {
	cell := statusText(it)
	return cell + strings.Repeat(" ", max(0, w-lipgloss.Width(cell)))
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

// leverageCell says what routing this ticket would free: ⊘ for a ticket
// something still blocks, ↓n for the count it transitively unblocks. Bold
// marks the ones with downstream — shape and weight, not color alone. The
// pad here is the cell's own floor, not the column: a count in the hundreds
// outgrows it, so the column is measured across the list like the others and
// the row pads out to that.
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
	rows, cur := m.attentionRows(padList.inner(w))
	rows = m.panelBody(panelAttention, rows, cur, padList.inner(w), h-2)
	return panelBox(panelTitle(1, "inbox", focused, extra), focused, w, h, rows, padList)
}

// liveLanes counts every lane hosting a live run, including the ones above
// N: a forced run, or one adopted from a previous process with a bigger lane
// count. The loop charges those against the budget too — freeLanes computes
// capacity as Lanes minus every active run, wherever it sits — so counting
// them is what makes the fraction below agree with what can actually start.
func (m model) liveLanes() int {
	live := 0
	for _, ln := range m.lanes {
		if ln.state != laneIdle {
			live++
		}
	}
	return live
}

// capacityLabel is the one number the status bar and the work panel title
// both render. The fraction says whether anything can start, so it is the
// loop's own arithmetic: full at N live runs, whatever lane numbers they
// hold. The suffix says the board is over capacity, which is a state the
// operator asked for and should be able to see.
//
// Counting only lanes 1..N here would read "1/2 running" while two runs are
// live and freeLanes returns nothing — a free lane the operator would wait
// on and never get.
func (m model) capacityLabel() string {
	live := m.liveLanes()
	label := fmt.Sprintf("%d/%d running", min(live, m.o.Lanes), m.o.Lanes)
	if over := live - m.o.Lanes; over > 0 {
		label += fmt.Sprintf(" · +%d over", over)
	}
	return label
}

func (m model) workPanel(w, h int) string {
	focused := m.focus == panelWork
	// Capacity has two homes now that the lane rows are gone: this title and
	// the status bar. It is the number that says whether anything can start.
	extra := styleFaint.Render(" · " + m.capacityLabel())
	rows, sel := m.workListRows(padList.inner(w))
	rows = m.panelBody(panelWork, rows, sel, padList.inner(w), h-2)
	return panelBox(panelTitle(2, "work", focused, extra), focused, w, h, rows, padList)
}

// workListRows renders the merged list: each queue's header, then its
// tickets — running first, then what runs next — and where the cursor sits
// among the rendered lines, for the focus window. A ticket a lane holds
// draws two lines, so the cursor carries that span rather than a bare index.
func (m *model) workListRows(width int) ([]string, cursor) {
	none := cursor{at: -1}
	groups := m.workGroups()
	if len(groups) == 0 {
		if m.queues == nil {
			return []string{styleFaint.Render("waiting for the first pass…")}, none
		}
		return []string{styleFaint.Render("no queues configured")}, none
	}
	n := 0
	for _, g := range groups {
		n += len(g.rows)
	}
	selRow := -1
	if n > 0 {
		selRow = clampIndex(m.workPos, n)
	}
	focused := m.focus == panelWork
	var rows []string
	var cont []bool
	at, span, idx := -1, 1, 0
	for _, g := range groups {
		rows, cont = append(rows, groupHeader(g)), append(cont, false)
		for _, r := range g.rows {
			lines := m.workRowLines(r, focused && idx == selRow, width)
			if idx == selRow {
				at, span = len(rows), len(lines)
			}
			for i := range lines {
				cont = append(cont, i > 0)
			}
			rows = append(rows, lines...)
			idx++
		}
	}
	return rows, cursor{at: at, span: span, cont: cont}
}

// groupHeader is one queue's line: its name, the Linear status a ticket
// enters it by, its team, and how many rows sit under it.
func groupHeader(g workGroup) string {
	if g.offBoard {
		if g.name == "" {
			return styleFaint.Render("off the board")
		}
		return styleTicket.Render(g.name) + styleFaint.Render(" · off the board")
	}
	count := fmt.Sprintf("%d", len(g.rows))
	if len(g.rows) == 0 {
		count = "empty"
	}
	return styleTicket.Render(g.name) +
		styleFaint.Render(fmt.Sprintf(" · %s · %s · %s", g.status, g.team, count))
}

// workRowLines is one ticket as the panel draws it: a line naming it and
// what is running it or what it waits on, and — for a ticket a lane holds —
// a second line of how that run is going. The right-hand column is
// right-aligned so the fact that is changing is never the one truncated
// away; the state is a colored dot plus a word, since color alone would not
// carry it.
func (m model) workRowLines(r workRow, selected bool, width int) []string {
	name := styleTicket.Render(r.ticket) + " " + r.title
	if r.lane == 0 {
		if !r.eligible {
			name = styleFaint.Render(r.ticket + " " + r.title)
		}
		right := ""
		switch {
		case len(r.blockedBy) > 0:
			right = styleAttention.Render("⊘ blocked by " + strings.Join(r.blockedBy, ", "))
		case r.assigned:
			right = styleFaint.Render("claimed")
		}
		// Two spaces where a running row draws its dot, so identifiers line
		// up down the group whether or not a lane holds them.
		return []string{splitRow(marker(selected)+"  "+name, right, width)}
	}
	var dot, state string
	switch r.state {
	case laneProvisioning:
		dot = styleProvisioning.Render(heartbeatFrames[m.frame%len(heartbeatFrames)])
		state = styleProvisioning.Render("provisioning")
	default:
		// laneAdopted falls here on purpose: an adopted run draws as
		// `running` because that is what the operator is looking at. The
		// badge it used to carry was earned when adoption meant remembering
		// rather than resuming — an adopted run reached the end of its work
		// and then did not take its queue's hop, so the badge warned of an
		// ambush. Since LERP-74 an adopted run records its own exit status
		// and reap applies the same move rule, so all the badge reported was
		// which process spawned the agent: bookkeeping, in jargon, that the
		// operator cannot act on. It is not quite a nonexistent difference —
		// an adopted run concludes from its exit file, and a missing or torn
		// one still falls back to releasing the claim without hopping — but
		// that is rare, invisible while the run is live, and plain on the
		// board when it happens. The distinction survives where it earns its
		// keep: laneAdopted is still its own lane state, and EventAdopted
		// still lands in .lerp/loop.log.
		dot = styleRunning.Render("●")
		state = styleFaint.Render("running")
	}
	// The elapsed clock stays on this line, where it already was: a squeezed
	// panel keeps the first line of a row and cuts the second, and the row
	// that survives that cut must not say less than it did before the
	// second line existed.
	right := state + " " + styleFaint.Render(elapsed(r.since))
	lines := []string{splitRow(marker(selected)+dot+" "+name, right, width)}
	if reading := runLine(r, width); reading != "" {
		lines = append(lines, reading)
	}
	return lines
}

// runLine is the second line of a row a lane holds: how long since its log
// last said anything, and a sparkline of the activity behind that. Beside the
// elapsed clock on the line above, they answer what elapsed alone cannot —
// whether a run that started four minutes ago is still doing something.
//
// It is empty for a run with no log to read, which is a lane still
// provisioning: a blank line under the row would claim a reading that does
// not exist, and cost the panel a row to say nothing.
//
// This is a reading, not a verdict. Nothing here compares the number to a
// threshold or calls a run stuck; SCOPE defers hang detection, and this shows
// the operator what the log already knows and leaves the decision to eject
// theirs.
func runLine(r workRow, width int) string {
	if r.heard.IsZero() {
		return ""
	}
	// Four spaces: the cursor column and the state dot, so the line starts
	// under the ticket identifier rather than under the cursor.
	left := "    heard " + elapsed(r.heard) + " ago"
	// The number is the reading; the sparkline is the shape behind it.
	// splitRow protects its right column against a narrow panel, and here
	// the right column is the one that can be spared — a panel too narrow
	// for both drops the line rather than truncate the digits.
	right := ""
	spark := sparkline(r.rate)
	if spark != "" && lipgloss.Width(left)+1+lipgloss.Width(spark) <= width {
		right = styleFaint.Render(spark)
	}
	return splitRow(styleFaint.Render(left), right, width)
}

// mainPanel is the lens: the promote picker while it is open, the ? overlay,
// otherwise the selected row's log, or a read-only detail when it has none.
func (m model) mainPanel(w, h int) string {
	if m.promoting {
		if it := m.selectedAttention(); it != nil {
			return m.promotePicker(*it, w, h)
		}
	}
	if m.ejection != nil {
		return m.ejectResult(*m.ejection, w, h)
	}
	if m.ejecting {
		return m.ejectConfirm(m.ejectRow, w, h)
	}
	if m.helpOn {
		return panelBox(styleTitleFocus.Render("help"), true, w, h,
			strings.Split(m.help.View(m.keys), "\n"), padMain)
	}
	title := m.mainTitle()
	return panelBox(styleFaint.Render(title), false, w, h,
		strings.Split(m.vp.View(), "\n"), padMain)
}

// promotePicker renders the target-status list for the selected inbox
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
	rows = windowRows(rows, cursor{at: 2 + m.promoteSel, span: 1}, h-2)
	return panelBox(styleTitleFocus.Render("promote "+it.Ticket), true, w, h, rows, padMain)
}

// ejectConfirm is the overlay eject opens: what pressing enter kills, and
// what it deliberately leaves standing. The workspace and the ticket are
// spelled out because they are the two things an operator expects a "stop"
// to take away, and eject takes neither.
func (m model) ejectConfirm(r workRow, w, h int) string {
	rows := []string{
		styleTicket.Render(r.ticket) + " " + r.title,
		"",
		styleAttention.Render("stops") + "  the agent in lane " + fmt.Sprintf("%d", r.lane) +
			", running " + elapsed(r.since),
		styleFaint.Render("keeps") + "  the workspace, the claim and the status — the ticket does not move",
		"",
		styleFaint.Render("lerp hands back the runner's resume command; the workspace, worktree"),
		styleFaint.Render("and all, is yours to finish in and yours to remove."),
	}
	rows = fitRows(rows, h-2)
	return panelBox(styleTitleFocus.Render("eject "+r.ticket), true, w, h, rows, padMain)
}

// ejectResult is the panel the eject leaves behind: the workspace it did not
// dispose, and the command that reopens the run as the operator's own
// session. It stays up until esc, because nothing else on screen keeps it.
func (m model) ejectResult(ej loop.Ejection, w, h int) string {
	// Wrapped, not truncated, and the command first: panelBox cuts a row to
	// the pane's width and fitRows drops the tail on a short pane, either of
	// which would hand back half a command. A resume command and a workspace
	// path are both routinely wider than a 45%-of-the-terminal pane.
	width := padMain.inner(w)
	rows := []string{styleFaint.Render("resume")}
	rows = append(rows, styleTicket.Render(ansi.Wrap(ej.Resume, max(8, width), " ")))
	rows = append(rows, "", styleFaint.Render("workspace"))
	rows = append(rows, wrapText(ej.Workspace, width)...)
	// The log line is not an aside: a command wrapped across rows pastes as
	// several commands, so the operator who wants to copy rather than read
	// needs somewhere it exists on one line.
	rows = append(rows, "")
	for _, line := range wrapText("the agent is stopped; the ticket is untouched in Linear. "+
		"esc dismisses this panel; the whole command is on one line in .lerp/loop.log", width) {
		rows = append(rows, styleFaint.Render(line))
	}
	// Split back into single lines: a styled block is one string, and
	// panelBox draws — and elides — by row.
	rows = strings.Split(strings.Join(rows, "\n"), "\n")
	title := "ejected"
	if ej.Ticket != "" {
		title += " " + ej.Ticket
	}
	return panelBox(styleTitleFocus.Render(title), true, w, h, rows, padMain)
}

func (m model) mainTitle() string {
	if m.focus == panelAttention {
		if it := m.selectedAttention(); it != nil {
			return it.Ticket
		}
		return "inbox"
	}
	r := m.selectedWork()
	switch {
	case r == nil:
		return "work"
	case m.selectedLogPath() == "":
		if r.lane > 0 {
			return "no log yet"
		}
		return r.ticket
	case r.lane > 0:
		return fmt.Sprintf("log%s · %s · %s", m.rawSuffix(), r.ticket, r.queue)
	default:
		// The run is over and its ticket has moved on, but its log is still
		// the freshest thing anyone knows about the ticket.
		return fmt.Sprintf("last log%s · %s", m.rawSuffix(), r.ticket)
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

// labelGutter is the width of the detail lenses' label column, and the
// hanging indent a wrapped value lines up under.
const labelGutter = "        "

// attentionDetail is the main pane's lens on the selected inbox item:
// everything the loop knows, Linear's URL, and then the ticket itself (see
// ticketLines). Promote is the one action here; everything else about the
// item happens in Linear.
func (m model) attentionDetail(width int) string {
	if !m.attentionSeen {
		return styleFaint.Render("reading the board…")
	}
	if len(m.attention) == 0 {
		return styleFaint.Render("the inbox is empty — that is the goal state") + "\n" +
			styleFaint.Render("(shows "+loop.AttentionDefinition+")")
	}
	if len(m.shown) == 0 {
		return styleFaint.Render("nothing in "+m.project) + "\n" +
			styleFaint.Render("(P cycles the project filter back to all)")
	}
	it := m.selectedAttention()
	status := statusText(*it)
	// These lines come from the pass and always render first, whatever the
	// read of the ticket itself is doing: a failed fetch must never cost the
	// operator the pane that works today.
	lines := []string{
		styleTicket.Render(it.Ticket) + " " + it.Title,
		"",
		styleFaint.Render("status  ") + status,
		styleFaint.Render("project ") + projectName(it.Project),
	}
	// The reason is a sentence, not a cell. panelBox truncates its rows, and
	// the pane's padding costs two columns, so a long "why" would lose the
	// tail — the part that says what is actually holding the ticket up.
	// Wrapped under the gutter, the way the ticket body already is.
	for i, l := range wrapText(it.Reason, width-len(labelGutter)) {
		if i == 0 {
			lines = append(lines, styleFaint.Render("why     ")+l)
			continue
		}
		lines = append(lines, labelGutter+l)
	}
	lines = append(lines, styleFaint.Render("linear  ")+it.URL)
	return strings.Join(append(lines, m.ticketLines(it.TicketID, width)...), "\n")
}

// ticketLines is the ticket itself, below the pass's own lines: the body,
// then the comments oldest first — so lerp's last stage-boundary artifact,
// the verdict that parked the ticket, is where the eye lands. Read-only and
// flat: nothing here is selectable, no thread is followed, no other ticket
// is reachable from it. Markdown is rendered (see markdown.go); `o` is the
// answer to anything that wants more.
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
		lines = append(lines, renderMarkdown(body, width)...)
	} else {
		lines = append(lines, styleFaint.Render("(no description)"))
	}
	if len(d.comments) == 0 {
		return append(lines, "", styleFaint.Render("(no comments)"))
	}
	for _, c := range d.comments {
		lines = append(lines, "", styleFaint.Render(commentHead(c)))
		lines = append(lines, renderMarkdown(strings.TrimSpace(c.Body), width)...)
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
// sentence, where it would throw away everything past the first line. The
// ticket body goes through renderMarkdown instead; this is for lerp's own
// prose, which is not markdown and must not be read as any.
func wrapText(s string, width int) []string {
	return strings.Split(ansi.Wrap(s, max(8, width), "-"), "\n")
}

// workDetail is the lens on a selected ticket no run has a log for: where
// it sits in pickup order and what, if anything, gates it. With nothing
// queued it says how tickets enter each queue instead.
func (m model) workDetail() string {
	r := m.selectedWork()
	if r == nil {
		if m.queues == nil {
			return styleFaint.Render("waiting for the first pass…")
		}
		lines := []string{styleFaint.Render("nothing is running and every queue is empty"), ""}
		for _, q := range m.queues {
			lines = append(lines, styleTicket.Render(q.Name)+
				styleFaint.Render(fmt.Sprintf(` — tickets enter when moved to "%s"`, q.Status)))
		}
		return strings.Join(lines, "\n")
	}
	queue := r.queue + styleFaint.Render(" · off the board")
	if r.status != "" {
		queue = r.queue + styleFaint.Render(" · "+r.status+" · team "+r.team)
	}
	gate := styleRunning.Render(fmt.Sprintf("runs when capacity frees — position %d of %d", r.pos, r.of))
	switch {
	case r.lane > 0:
		gate = styleRunning.Render("running — " + elapsed(r.since))
	case len(r.blockedBy) > 0:
		gate = styleAttention.Render("blocked by " + strings.Join(r.blockedBy, ", "))
	case r.assigned:
		gate = styleFaint.Render("claimed — an assigned ticket is never picked up")
	}
	return strings.Join([]string{
		styleTicket.Render(r.ticket) + " " + r.title,
		"",
		styleFaint.Render("queue   ") + queue,
		styleFaint.Render("pickup  ") + gate,
		styleFaint.Render("linear  ") + r.url,
		"",
		styleFaint.Render("to change what runs next, move the ticket in Linear"),
	}, "\n")
}

// statusBar is the heartbeat line: focused panel, pass clock, capacity,
// inbox count, keys. A pass error — or a transient note like a
// promote's outcome — takes over the whole line; a truncated error is not
// actionable, so nothing else competes with it for the width.
// note is one transient report on the status bar. warn marks something that
// went unhandled rather than something that worked, so a promote's success
// and a run's non-zero exit never read the same.
type note struct {
	text string
	warn bool
}

// note records one, in arrival order.
func (m *model) note(text string, warn bool) {
	m.notes = append(m.notes, note{text: text, warn: warn})
}

// ejectedNote names the run the operator took over. An adopted run whose
// record never carried the identifier still names its lane, which is the row
// the operator was looking at.
func ejectedNote(ev loop.Event) string {
	if ev.Ticket != "" {
		return ev.Ticket + ": ejected, the agent is stopped"
	}
	return fmt.Sprintf("lane %d ejected, the agent is stopped", ev.Lane)
}

// reapedNote names the ticket when the record knew it, because "reaped a
// dead run" told the operator nothing about which run.
func reapedNote(ev loop.Event) string {
	if ev.Ticket != "" {
		return ev.Ticket + ": reaped a dead run"
	}
	return "reaped a dead run"
}

// noteLine is the status bar while anything transient is pending: the pass
// error first when there is one, then every note this interval collected.
// A truncated line is not actionable, so nothing else competes for the
// width — but the notes compete with each other rather than overwriting,
// since losing that a run failed is worse than a crowded line.
func (m model) noteLine() string {
	var segs []string
	if m.lastErr != "" {
		segs = append(segs, styleErr.Render("✗ "+m.lastErr))
	}
	for _, n := range m.notes {
		mark, style := "✓ ", styleRunning
		if n.warn {
			mark, style = "! ", styleAttention
		}
		segs = append(segs, style.Render(mark+n.text))
	}
	return strings.Join(segs, "  ")
}

func (m model) statusBar() string {
	if line := m.noteLine(); line != "" {
		return ansi.Truncate(line, m.width, "…")
	}
	badgeColor := colorFocus
	if m.focus == panelAttention {
		badgeColor = colorAttention
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
	left += "  " + styleFaint.Render(m.capacityLabel())
	if len(m.attention) > 0 {
		left += "  " + styleAttention.Render(fmt.Sprintf("● %d in the inbox", len(m.attention)))
	}
	// The bar offers what this window and this frame actually answer to. ?
	// draws its overlay in the main pane, so a window with no room for the
	// pane has none for the overlay either — though closing one never needs
	// the room opening it did.
	globals := "? help · q quit"
	if !m.helpOn && !m.roomForMain() {
		globals = "q quit"
	}
	hint := globals
	switch {
	// Behind the overlay, esc and ? are the overlay's and enter is inert, so
	// the pane has no key here to offer.
	case m.helpOn:
	case m.detailOpen[m.focus]:
		hint = "esc close · " + hint
	case m.roomForMain():
		hint = "enter detail · " + hint
	}
	// A modal has the keyboard, so its own instructions replace the line.
	switch {
	case m.promoting:
		hint = "↑/↓ choose · enter promote · esc cancel"
	case m.ejecting:
		hint = "enter eject · esc cancel"
	case m.ejection != nil:
		hint = "esc dismiss"
	}
	right := styleFaint.Render(hint)

	// The pane's segment is the first thing the bar gives up. Below it the
	// truncation takes the left side instead, and what it would take is
	// "● n in the inbox" — the one number the needs-you panel exists for,
	// spent on advertising a key. A modal's line is not a hint but its
	// instructions, so it is not up for this.
	if !m.modal() && m.width-lipgloss.Width(left)-lipgloss.Width(right) < 1 {
		right = styleFaint.Render(globals)
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
	// A clock that disagrees with the filesystem's can put a log's mtime a
	// moment in the future; "-1s ago" would read as a bug in the board.
	return max(time.Since(since), 0).Truncate(time.Second).String()
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
