package tui

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mattwalters/lerp/internal/browser"
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

// Engine is the whole of what the shell asks of the loop: the roles above,
// in one interface. loop.Reconciler satisfies it, and both callers hand it
// over whole rather than assigning a field per role.
//
// One interface rather than five fields because a struct field left unset is
// a runtime refusal — Validate catches it when Run opens the screen, which is
// too late for the demo harness, whose refusals vhs records as a bash error
// and exits 0 on. That is how a missing Starter shipped a blank cast. A sixth
// role added here instead breaks `go build ./...` at every call site, and
// `make check` runs that.
//
// What the bundle gives up: Validate can only ask whether an engine is here,
// not whether it is whole. A composite whose parts are nil is still a non-nil
// Engine, and the panic lands on the first tick. Nothing in production can
// build one — the reconciler satisfies this or the call site does not compile
// — so the check that is gone was only ever protecting assembled-by-hand
// engines, which is to say test code.
type Engine interface {
	Ticker
	Promoter
	Ejector
	Starter
	Reader
}

// Options wires the shell to the loop.
type Options struct {
	Engine   Engine
	Statuses []string      // promote targets: configured queue statuses, plus the pipeline's exits
	Interval time.Duration // tick cadence; loop.DefaultInterval when zero
	Lanes    int           // N: at most this many agents at once
	Events   <-chan loop.Event
}

// Validate returns an error naming the first option Run needs and does not
// have. Run calls it before opening the screen; it is exported because the
// demo harness assembles its Options in a package of its own, and a test
// there can only hold them to this contract if it can call it.
func (o Options) Validate() error {
	switch {
	case o.Engine == nil:
		return fmt.Errorf("tui: engine is required")
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
	// is its recent activity per bucket, oldest first, for the sparkline —
	// history the log dates included (see pulse.window).
	heard time.Time
	rate  []int
	// tool and target are the last tool call the log carried, empty until it
	// carries one; tokens is what the run has spent, summed over the whole
	// log — history included, so an adopted run reports the run's total.
	tool, target string
	tokens       int
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

	focus panel
	// keysInMain is the operator's answer to which surface the keys are
	// talking to: the focused panel's list, or the pane open beside it.
	// focus stays the panel either way — the pane is a lens on that
	// panel's selected row, not a third panel with a cursor of its own —
	// so everything about what the pane shows is unchanged while it holds
	// the keys. Ask mainFocused, never this: the pane can only hold them
	// while it is the operator's own detail on screen.
	keysInMain    bool
	width, height int
	ready         bool
	helpOn        bool
	// helpOffset is where the main pane was scrolled to when the ? overlay
	// took it over, and helpLens is what it was showing, so closing the
	// overlay gives the operator back the place they were reading — and
	// knows not to, when they re-aimed the pane behind it.
	helpOffset int
	helpLens   mainLens

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

	// sortMode and project are two of the table's four session-only
	// controls: one key cycles the order, another scopes the rows to a
	// single Linear project ("" is every project). None of the four is
	// saved anywhere — they are a way to read one list the pass already
	// fetched, not a view to keep. sortMode starts at defaultSort, so it is
	// set in newModel rather than left to the zero value.
	sortMode sortMode
	project  string

	// The third is search (see search.go): searching is the prompt's
	// open/closed state, search the query the rows are filtered by ("" is
	// every row), searchWas and searchSelWas the query and the selected row
	// esc puts back. The filter outlives the prompt — enter closes the box
	// and keeps the rows narrowed, so the keys can promote what the search
	// found.
	searching    bool
	search       string
	searchWas    string
	searchSelWas string
	searchInput  textinput.Model

	// The fourth is the fold. backlogOpen is whether the panel is also
	// showing the tickets that have not entered the pipeline yet; it opens
	// closed, so what the panel says on sight is what is blocked on a
	// human. Pulling from the backlog is a deliberate sit-down motion and
	// can live behind a key; being blocked-on is an interrupt and owns the
	// default view. Model state like the other three, so it survives a pass
	// and is saved nowhere.
	backlogOpen bool

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
	// The list owns the screen until the operator asks for the detail, in
	// both panels alike: a log is something you open to read a particular
	// run, the way a ticket's body is something you open once you have
	// decided to read that ticket. Work kept the pane open while the row
	// said nothing about its run; the row answers that itself now — how
	// long since the log grew, and the shape of the activity behind it —
	// so the pane is no longer the only way to see a run is alive.
	//
	// Each panel still remembers its own answer, session-only like sort and
	// the project filter. A display default, not a rule about process.
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
	// heard is whether anything has come back from the loop yet — any event
	// at all, which is the moment the board stops having nothing to draw. It
	// is what the opening splash gives way to; see splashing.
	heard bool

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
	// Focus opens on the inbox: the loop runs the board on its own, so what
	// the operator is at the terminal for the moment they open it is what
	// needs them, and it agrees with the panel numbering. The pane defaults
	// below are unchanged, so the first screen is two lists and no main
	// pane — the inbox table gets the full width, where it keeps the project
	// column an open pane squeezes out of it, and the log is one `2` away
	// with no `enter` behind it, because work's pane stays open.
	m := model{o: o, ctx: ctx, focus: panelAttention, lanes: make(map[int]*lane),
		details: make(map[string]*ticketDetail), lastLog: make(map[string]string),
		vp: viewport.New(0, 0), follow: true, keys: newKeymap(), help: h,
		sortMode:    defaultSort,
		searchInput: newSearchInput(),
		detailOpen:  [2]bool{panelAttention: false, panelWork: false},
		inFlight:    true, // Init starts the first pass immediately
		passes:      &sync.WaitGroup{}}
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
		m.o.Engine.Tick(m.ctx)
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
		// A window that shrank below the pane's floor closes what took it
		// modally. detailOpen is the operator's own preference and survives
		// — esc is on the too-small screen — but a picker left live behind
		// that screen would still take the enter that writes to Linear, an
		// eject confirm the enter that kills an agent, and the overlay would
		// still eat the keyboard. An eject that already happened is not
		// cancellable and its panel is the only copy of the resume command,
		// so it waits for the window to come back rather than being thrown
		// away here.
		if !m.roomForMain() {
			m.promoting, m.helpOn, m.ejecting = false, false, false
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
	default:
		// The search prompt's own messages land here: the cases above are
		// this model's, and a clipboard read on ctrl+v is the widget's.
		// Without this the paste it asked for is dropped on the floor.
		if m.searching {
			return m.updateSearch(msg)
		}
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
	if m.searching {
		return m.handleSearchKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		// Closing it never needs the room opening it did.
		if m.helpOn || m.roomForMain() {
			m.setHelp(!m.helpOn)
		}
	case key.Matches(msg, m.keys.Attention):
		m.setFocus(panelAttention)
	case key.Matches(msg, m.keys.Work):
		m.setFocus(panelWork)
	case key.Matches(msg, m.keys.NextPanel):
		m.cycleSurface(1)
	case key.Matches(msg, m.keys.PrevPanel):
		m.cycleSurface(-1)
	// The selection keys move the selection, everywhere the selection is
	// what the keys are pointed at. In the pane they move the pane, a line
	// at a time, which is the movement the scroll keys never had.
	//
	// And where the keys are in the pane but the pane is not the operator's
	// to move — a window too short to hold it, which is a screen with no
	// panels drawn on it either — they are inert, the rule the scroll keys
	// follow on a closed pane one case below. Falling through to the
	// selection would walk a list nobody can see, and the pane would come
	// back re-aimed at a row the operator never chose, at the top of it.
	case m.keysInMain && !m.mainFocused() &&
		(key.Matches(msg, m.keys.Up) || key.Matches(msg, m.keys.Down)):
	case key.Matches(msg, m.keys.Up):
		if m.mainFocused() {
			m.scrollMain(-1)
		} else {
			m.moveSelection(-1)
		}
	case key.Matches(msg, m.keys.Down):
		if m.mainFocused() {
			m.scrollMain(1)
		} else {
			m.moveSelection(1)
		}
	// enter opens the focused panel's detail and esc closes it — neither
	// flips. esc inside the promote picker still cancels the picker, because
	// handlePromoteKey ran before this switch and returned.
	case key.Matches(msg, m.keys.Detail):
		// Not under the overlay: it is what is on screen, and enter behind it
		// would open a pane nobody asked for and read a ticket nobody sees.
		// Nor under the splash, which is the same rule about a different
		// screen: there is no row to detail before the first pass reports,
		// and the pane would be open in the geometry without being on it —
		// enough, on a short window, to turn the next resize into "window
		// too small · esc closes the pane" about a pane never drawn.
		if m.roomForMain() && !m.helpOn && !m.splashing() {
			m.detailOpen[m.focus] = true
			// Size before filling: the viewport was zero-width while the
			// pane was closed, and refreshMain wraps to that width.
			m.layout()
			m.refreshMain()
		}
	case key.Matches(msg, m.keys.Close):
		// esc acts on what is on screen, nearest first: the overlay, the way
		// it cancels the picker; then a filter narrowing the list, which the
		// panel line offers as "clear" while one is on; and only then the
		// detail pane. handleSearchKey has esc while the prompt itself is
		// open, and the filter is not the inbox panel's alone — it is on the
		// list wherever the operator is standing.
		switch {
		case m.helpOn:
			m.setHelp(false)
		case m.search != "":
			m.setSearch("")
			m.refreshMain()
		default:
			// esc means the same thing it always meant — close the pane —
			// and the keys it was holding go back to the list that opened
			// it. Left set, they would be waiting inside the next pane the
			// operator opened, which is not what enter asked for.
			m.detailOpen[m.focus] = false
			m.keysInMain = false
		}
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
	case key.Matches(msg, m.keys.Backlog):
		if m.focus == panelAttention {
			m.backlogOpen = !m.backlogOpen
			m.resort()
			m.refreshMain()
		}
	case key.Matches(msg, m.keys.Search):
		// Nothing to narrow is nothing to search: the question is whether
		// this panel has a row on it, not whether the pass found one. An
		// empty inbox, a fold hiding every row, and a project scope left
		// over one all end at the same one-line panel, and a prompt opened
		// over that line takes the keyboard for a filter with nothing to
		// match. The way back out of each is the key that empty line names.
		if m.focus == panelAttention && len(m.shown) > 0 {
			m.openSearch()
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
	// The raw toggle acts on the log in the pane, so it is inert wherever
	// that log is not on screen: the pane shut, the ? overlay covering it,
	// or the inbox's own detail in it. Anywhere else the flip would change
	// the decoding of a log nobody is reading, and the operator would meet
	// it as a surprise the next time they opened the pane.
	case !m.logOnScreen() && key.Matches(msg, m.keys.Raw):
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
		detail, err := m.o.Engine.IssueDetail(m.ctx, ticketID)
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
	switch {
	// Quit backs the picker out rather than leaving the board, which is what
	// the raw switch did with the same two keys: the modal has the keyboard,
	// and q here means "not this status" and not "not this session". (The
	// eject overlay answers ctrl+c the other way, with tea.Quit. That
	// disagreement is older than this switch and is not ours to settle.)
	case key.Matches(msg, m.keys.Close), key.Matches(msg, m.keys.Quit):
		m.promoting = false
	case key.Matches(msg, m.keys.Up):
		if m.promoteSel > 0 {
			m.promoteSel--
		}
	case key.Matches(msg, m.keys.Down):
		if m.promoteSel < len(m.o.Statuses)-1 {
			m.promoteSel++
		}
	case key.Matches(msg, m.keys.Detail):
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
		err := m.o.Engine.Promote(m.ctx, ticketID, status)
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
		ejection, err := m.o.Engine.Eject(m.ctx, ticketID)
		return ejectedMsg{ticket: ticket, ejection: ejection, err: err}
	}
}

// startEject opens the confirm for the selected row, capturing it, or says
// why it cannot. A queue whose runner has no resume command is the one
// refusal worth a word: the row looks exactly like an ejectable one, and the
// key line's silence about "e" is easy to miss. The others — a ticket that
// is not running, a lane still provisioning — are plain on the row itself.
func (m *model) startEject() {
	if m.focus != panelWork || !m.roomForMain() {
		return
	}
	r := m.selectedWork()
	if r == nil || r.lane == 0 || r.state == laneProvisioning {
		return
	}
	if can, why := m.o.Engine.CanEject(r.queue); !can {
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
	// The confirm is drawn in the main pane, so a window with no room for
	// the pane has none for it: the same trade p makes, and the same reason
	// — a key that replaced the panels with "window too small" would leave
	// a live confirm behind it, still holding the enter that kills an agent.
	if m.focus != panelWork || !m.roomForMain() {
		return false
	}
	r := m.selectedWork()
	if r == nil || r.lane == 0 || r.state == laneProvisioning {
		return false
	}
	can, _ := m.o.Engine.CanEject(r.queue)
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
		err := m.o.Engine.ForceStart(m.ctx, ticketID)
		return forcedMsg{ticket: ticket, err: err}
	}
}

// setHelp opens or closes the ? overlay. The overlay is one more thing the
// pane holds, so it goes through the viewport the log and the detail already
// scroll in: a legend under the key table is only a legend if a short
// terminal can reach it. Which means it is drawn over the place the operator
// was reading — a plan halfway down a ticket as much as a tail scrolled back
// through — so that place is parked on the way in and put back on the way
// out. A followed tail needs nothing put back: refreshLog has already pinned
// it to the bottom.
//
// Both keys that leave the overlay come through here, so ? and esc cannot
// put the pane back differently.
func (m *model) setHelp(on bool) {
	if m.helpOn == on {
		return
	}
	m.helpOn = on
	if on {
		m.helpOffset, m.helpLens = m.vp.YOffset, m.lens()
	}
	// Size before filling, as enter does: the overlay changes whether the
	// pane is open at all, and refreshMain fills the viewport that height
	// and width leave it.
	m.layout()
	m.refreshMain()
	switch {
	case on:
		m.vp.GotoTop()
	case m.showingLog() && m.follow:
		// refreshLog has pinned the tail to the bottom.
	case m.lens() != m.helpLens:
		// The operator re-aimed the pane behind the overlay, so what comes
		// back is not what was parked, and comes back at its top.
		m.vp.GotoTop()
	default:
		m.vp.SetYOffset(m.helpOffset)
	}
}

// mainFocused reports whether the keys are in the main pane rather than in
// the focused panel's list. It is derived and never stale: the pane holds
// them only while the operator's own detail is what is on screen in it, so
// a window that shrinks under the pane, the ? overlay, or a modal drawn in
// that same pane each hand the keys straight back to the list — and give
// them back when they are done, the way detailOpen survives a resize.
func (m *model) mainFocused() bool {
	return m.keysInMain && m.paneTakesKeys(m.focus)
}

// paneTakesKeys reports whether p's pane can hold the keys at all: open,
// with the room to draw, and not covered by a modal that has the keyboard
// already. It is the rule for keeping them, which is why it is this short —
// focus moves when the operator moves it, and a pass landing is not the
// operator.
//
// A modal is in that list and the ? overlay is not. A modal has the
// keyboard outright and draws its own instructions, and the search prompt
// lives in the panel's own footer, which has to stay lit while it is being
// typed into. The overlay only borrows the pane: the keys stay where the
// operator put them, scrolling whatever the pane is holding — which is the
// help while it is up. That is also what keeps the screen honest, since a
// panel still lit behind the overlay is what says the keys are on the list.
func (m *model) paneTakesKeys(p panel) bool {
	return m.detailOpen[p] && m.roomForMain() && !m.modal()
}

// paneJoinsCycle is the stricter rule for tab arriving: on top of holding
// the keys, the pane has to be visible and a lens on a row.
//
// Not visible is the overlay drawn over it — tab behind the overlay means
// what it always meant, the next panel, rather than moving the keys into a
// surface the operator cannot see them arrive in. Not a row is a pane
// holding a state sentence — "waiting for the first pass…", "the inbox is
// empty" — where the keys would arrive with nothing to scroll and no
// selection left to move.
//
// Only arriving asks: a panel that empties under a pane the operator is
// already reading keeps its keys, because taking them back would be a
// board's read deciding where the keyboard points. A pass whose Linear
// calls all failed reports an empty queue exactly like an empty one, and
// once a pass can move focus, an outage moves it every interval.
func (m *model) paneJoinsCycle(p panel) bool {
	return m.paneTakesKeys(p) && !m.helpOn && m.hasRow(p)
}

// hasRow reports whether p's cursor is standing on anything.
func (m *model) hasRow(p panel) bool {
	if p == panelWork {
		return m.selectedWork() != nil
	}
	return m.selectedAttention() != nil
}

// cycleSurface is tab: the two panels, and after each one the pane it has
// open, which is a surface exactly when it is open. Nothing here changes
// what a key means — the pane joins a cycle that already meant "move
// between surfaces", and enter and esc still only open and close.
//
// shift+tab is the exact inverse, which is what makes tab safe to lean on:
// stepping back off a panel lands in that panel's pane when it has one, and
// stepping back off a pane lands on the panel whose row it is showing.
func (m *model) cycleSurface(delta int) {
	switch {
	case delta > 0 && !m.mainFocused() && m.paneJoinsCycle(m.focus):
		// Into the pane the focused panel already has open. Deliberately
		// not setFocus: the panel is not changing, and re-aiming the lens
		// would scroll the pane the operator is moving into back to its top.
		m.keysInMain = true
	case delta < 0 && m.mainFocused():
		m.keysInMain = false
	default:
		next := (m.focus + 1) % 2
		m.setFocus(next)
		m.keysInMain = delta < 0 && m.paneJoinsCycle(next)
	}
}

// scrollMain moves the pane a line at a time while it holds the keys: the
// movement the page keys never had, under the page keys' own rule about the
// tail. follow is the log's state, so a line off the bottom stops it and a
// line back onto the bottom picks it up again.
func (m *model) scrollMain(delta int) {
	if delta < 0 {
		m.vp.LineUp(1)
	} else {
		m.vp.LineDown(1)
	}
	if m.showingLog() {
		m.follow = m.vp.AtBottom()
	}
}

func (m *model) setFocus(p panel) {
	m.focus = p
	// A panel key is a key for a list: 1, 2 and tab's move between panels
	// all put the keys on the row, and cycleSurface sets this again when it
	// is the pane it is stepping into.
	m.keysInMain = false
	// Deliberately no roomForMain check here, unlike the keys that open the
	// pane. detailOpen is the operator's answer to whether they want a
	// panel's detail, not a fact about the window: enter and esc are the
	// keys that mean open and close, and a resize leaves it alone on
	// purpose. A key that only moves between panels may not edit it either.
	//
	// The cost is that moving to a panel whose pane is open can land on the
	// too-small screen in a short window. That screen is the existing answer
	// to a pane that does not fit, it names its key, and a shrink under an
	// open pane lands on the same one — one rule for both routes beats two.
	// Closing the pane here instead would be a change the operator did not
	// ask for at the moment they asked for something else, with no line on
	// screen to say it happened; enter would bring it back, but only once
	// they noticed the log was gone. Worst case, a panel key typed ahead of
	// the first WindowSizeMsg — width and height still zero, so nothing has
	// room — would close work's pane before any window had been measured.
	m.retarget()
	// Size before filling. The two panels remember the pane separately, so
	// focus moves the width the viewport wraps to — and content wrapped to
	// the outgoing panel's width would stay wrong until the next byte of log
	// or the next keystroke.
	m.layout()
	if m.helpOn {
		// The overlay is nobody's row. Moving between panels behind it
		// re-aims the lens under it — which refreshMain draws on the way
		// out — but must not scroll the help the operator is reading. The
		return
	}
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
		if m.helpOn {
			return // see setFocus
		}
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
		if m.helpOn {
			return // see setFocus
		}
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

// openURL hands the URL to the OS opener, through internal/browser. This is
// the TUI opening the operator's browser, not lerp speaking to an API;
// everything beyond promote still happens in Linear.
func openURL(rawURL string) tea.Cmd {
	if rawURL == "" {
		return nil
	}
	return func() tea.Msg {
		// A refusal is louder than a no-op on purpose. The key line drops `o`
		// from a row whose URL browser.Openable refuses, so pressing it
		// anyway is the operator asking a question — and the status bar is
		// where the answer goes.
		if err := browser.Open(rawURL); err != nil {
			return openErrMsg{err: err}
		}
		return nil
	}
}

// apply folds one loop event into the model. It is also where untrusted
// text stops being untrusted: every Linear-sourced string on the event is
// cleaned here, once, so the views below can stay plain string building.
func (m *model) apply(ev loop.Event) {
	ev = cleanEvent(ev)
	m.heard = true
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
		// panel behind a name nothing waits in, so it resets to all. A
		// search is not reset the same way: a project is a category that
		// stopped existing, where a query is text the operator typed and can
		// see in the title — clearing it under them would be the surprise.
		//
		// Asked of the whole pass rather than of projects(), which follows
		// the fold: a project the operator scoped to while browsing the
		// backlog has not stopped existing when the fold closes over it, and
		// a pass is not the place to take that choice away.
		if m.project != "" && !slices.ContainsFunc(m.attention, func(it loop.AttentionItem) bool {
			return it.Project == m.project
		}) {
			m.project = ""
		}
		// An inbox with nothing in it has nothing to narrow, and the title
		// stops carrying the query along with the count — so a filter left
		// behind by a closed prompt goes with the rows, rather than
		// narrowing the pass that repopulates the board out of sight.
		//
		// An open prompt keeps its query, which is on screen in the box
		// whatever the title says. Taking the keyboard back here would be
		// taking it back mid-word, from a passing event rather than from a
		// key the operator pressed, and their next letter would land on the
		// list as a command — a `q` in the middle of "queue" would quit.
		if len(m.attention) == 0 && !m.searching && m.search != "" {
			m.search = ""
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
			// The pulse reads the log from its start, so a run inherited
			// from a previous process draws the history its log dates and
			// reports the run's own totals — a fresh run's log is a line or
			// two, and costs nothing to read the same way.
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

// mainLens identifies what the main pane is showing: the panel whose cursor
// it follows, and the row under that cursor. It is an identity and not a
// position — the inbox's selection follows its ticket through a re-sort —
// so it answers one question: is the pane still showing what it was?
type mainLens struct {
	panel  panel
	ticket string
}

func (m *model) lens() mainLens {
	if m.focus == panelWork {
		return mainLens{panelWork, m.workSel}
	}
	if it := m.selectedAttention(); it != nil {
		return mainLens{panelAttention, it.Ticket}
	}
	return mainLens{panel: panelAttention}
}

// showingLog reports whether the main pane is the log rather than a detail.
// The lens is the selected row's, not the panel's: a ticket with a log shows
// it, a ticket without shows what the pass knows about it. The ? overlay is
// that same viewport holding neither, so while it is up the answer is no:
// every caller is asking what the pane is showing, and what the pane is
// showing is the help. It is the detail lens's own rule — a detour through
// something that is not the log neither freezes the tail nor gets written
// over by it — and the overlay is one more detour.
func (m *model) showingLog() bool {
	return !m.helpOn && m.focus == panelWork && m.selectedLogPath() != ""
}

// logOnScreen is showingLog with the pane's own state folded in: is a log
// actually in front of the operator? showingLog answers what the selected
// row would show, which is a different question — the pane may be shut, or
// the overlay may have taken it — and everything that acts on the log the
// operator can see asks this instead.
func (m *model) logOnScreen() bool {
	return m.mainOpen() && m.showingLog()
}

// mainOpen is the single question everything else asks: is the main pane on
// screen? The focused panel's own state, forced open by the promote picker,
// the ? overlay and eject's two panels — every one of them is drawn in that
// pane, so they take the width while they are up and hand it back when they
// close. That keeps p and e working from a closed panel — which both panels
// now are until the operator opens one — without a second rendering path.
// Miss one here and the key still takes the keyboard while nothing it draws
// reaches the screen.
func (m *model) mainOpen() bool {
	return m.promoting || m.helpOn || m.ejecting || m.ejection != nil ||
		m.detailOpen[m.focus]
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
	if m.helpOn {
		m.vp.SetContent(m.helpText())
		return
	}
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
	if !m.logOnScreen() {
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
			row.tool, row.target = ln.pulse.tool, ln.pulse.target
			row.tokens = ln.pulse.tokens
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
	m.shown = sortAttention(filterAttention(m.attention, m.project, m.search, m.backlogOpen), m.sortMode)
	i := slices.IndexFunc(m.shown, func(it loop.AttentionItem) bool { return it.Ticket == selected })
	if selected == "" || i < 0 {
		m.attnSel = clampIndex(m.attnSel, len(m.shown))
		return
	}
	m.attnSel = i
}

// filterAttention narrows the list to one Linear project, to the rows the
// search matches, and — while the backlog is folded — to the tickets that
// are actually blocked on a human. There is no filter syntax and no second
// query behind either of the two the operator types: the project column
// matched whole, and a plain substring over the facts a row already shows
// (see matchesSearch).
//
// What the fold hides is folds(); everything else stays on screen.
func filterAttention(items []loop.AttentionItem, project, query string, backlogOpen bool) []loop.AttentionItem {
	if project == "" && query == "" && backlogOpen {
		return items
	}
	out := make([]loop.AttentionItem, 0, len(items))
	for _, it := range items {
		if !backlogOpen && folds(it) {
			continue
		}
		if matchesFilters(it, project, query) {
			out = append(out, it)
		}
	}
	return out
}

// folds reports whether the fold hides this row: a ticket resting where
// Linear files work that has not started, and that nobody has claimed.
//
// Two readings, not one, because the tier alone cannot answer it. The
// backlog tier is derived from Linear's status category and knows nothing
// about who holds the ticket, and a ticket the operator has claimed resting
// in an intake status has not failed to enter the pipeline — it fell back
// out of one, and no pass can pick it up again while the claim stands
// (invariant 4: an assigned ticket is never eligible). That is as blocked on
// a human as a row gets, so it is never folded and never uncounted.
//
// A negative test rather than an allow-list of the tiers to keep:
// StatusUnknown is the reconciler's bug marker, sorts first, and an
// allow-list would silently fold it away.
func folds(it loop.AttentionItem) bool {
	return it.Relevance == loop.StatusBacklog && !it.Claimed
}

// matchesFilters is everything the two typed controls say about one row —
// the project scope and the search — with no reading of the fold. The fold
// and the summary line behind it ask the same question of the same rows, so
// they ask it through one predicate.
func matchesFilters(it loop.AttentionItem, project, query string) bool {
	return (project == "" || it.Project == project) && matchesSearch(it, query)
}

// unfolded is the pass's list under the fold alone: every row the panel
// could show before the project scope and the search narrow it. It is what
// P cycles over, what / opens on, and the base the panel title counts
// against — so the title's fraction is always over the rows this panel can
// reach, and P can never stop on a project whose every row is folded away.
func (m *model) unfolded() []loop.AttentionItem {
	return filterAttention(m.attention, "", "", m.backlogOpen)
}

// foldedRows is what the fold is holding back: the backlog rows that pass
// the project scope and the search already in force — exactly what pressing
// B would put on screen, so the summary line's number can never disagree
// with what it reveals. Nothing is folded while the backlog is open.
func (m *model) foldedRows() []loop.AttentionItem {
	if m.backlogOpen {
		return nil
	}
	var out []loop.AttentionItem
	for _, it := range m.attention {
		if folds(it) && matchesFilters(it, m.project, m.search) {
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

// projects lists the Linear projects present in the rows the fold lets
// through, in name order — the filter's cycle. A ticket filed under none is
// not a project and is not a stop on it, and neither is a project whose
// every row is folded away: cycling to one would scope the panel to
// "nothing in X".
func (m *model) projects() []string {
	var names []string
	for _, it := range m.unfolded() {
		if it.Project != "" && !slices.Contains(names, it.Project) {
			names = append(names, it.Project)
		}
	}
	slices.Sort(names)
	return names
}

// hasProjects reports whether the rows the fold lets through have any
// project to scope to.
// P cycles between every project and each one present, so with none present
// it is a key that does nothing — projects() answers the same question but
// builds and sorts the cycle to do it.
func (m *model) hasProjects() bool {
	return slices.ContainsFunc(m.unfolded(), func(it loop.AttentionItem) bool {
		return it.Project != ""
	})
}

// cycleProject advances the filter one stop: every project, then each
// project present, then back to every project.
func (m *model) cycleProject() {
	names := m.projects()
	switch i := slices.Index(names, m.project); {
	// A scope the fold has taken off the cycle — every row of that project
	// is backlog, and the backlog just closed — has one stop from here, and
	// it is the one the empty panel's hint promises. Not folded in with the
	// case below, whose i is -1 for the empty scope that starts the cycle.
	case m.project != "" && i < 0:
		m.project = ""
	case i+1 >= len(names):
		m.project = ""
	default:
		m.project = names[i+1]
	}
	m.resort()
}

// blockedOnYou counts the tickets in the pass's list that are waiting on a
// human: everything the fold does not hide. One predicate for both, so the
// number on the status bar and the rows the panel opens on can never
// disagree about what "blocked on you" means. Read off m.attention rather
// than the shown rows, so neither the fold's own state nor a filter can
// change a number that is about the whole board.
func (m *model) blockedOnYou() int {
	n := 0
	for _, it := range m.attention {
		if !folds(it) {
			n++
		}
	}
	return n
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
	// The pinned header is a line the panel draws and so a line it asks
	// for, the same as any row.
	attnLines := len(attnRows)
	if m.attentionHeader(padList.inner(g.sideW)) != "" {
		attnLines++
	}

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
		// the arithmetic — the log lens asks for the whole body — and both
		// panels would jump on the keystroke. What
		// the lens holds scrolls; a panel that shrank under the operator
		// does not.
		g.mainH = fitH(g.bodyH/2, mainFloor, g.bodyH-2*panelFloor)
		stackH = g.bodyH - g.mainH
	}
	g.workH = workHeight(stackH, m.panelWant(panelWork, len(workRows)), m.panelWant(panelAttention, attnLines))
	g.attnH = stackH - g.workH
	return g
}

// panelWant is one panel's height: the rows it renders plus its borders, and
// one line more when it is the focused panel carrying a footer. Both the
// height bought here and the line panelBody draws ask hasFooter, so the two
// can never disagree about whether the line is there.
func (m *model) panelWant(p panel, rows int) int {
	if m.focus == p && m.hasFooter(p) {
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
	// The prompt is a row of the inbox panel, so a query longer than the
	// panel scrolls within it rather than overflowing it. Two columns for
	// the "/" and the cursor.
	m.searchInput.Width = max(1, padList.inner(g.sideW)-2)
	// A pane that just changed height holds a scroll position measured
	// against the old one: re-pin a followed log to the bottom, and clamp
	// anything else back inside the new box.
	if m.showingLog() && m.follow {
		m.vp.GotoBottom()
		return
	}
	m.vp.SetYOffset(m.vp.YOffset)
}

// splashing reports the state the splash stands in for: lerp is up, the
// first pass is out, and nothing has come back from it — no event, and no
// pass finished. Either one ends it, and neither un-happens, which is what
// keeps the splash the first screen and never a later one.
//
// An event is the board having something to draw, whatever it was about: a
// lane, a queue, the inbox. A failed pass is an event too — its error goes
// to the status bar, where it can be read, rather than under a spinner that
// would go on saying "working" about a pass that is over. And a pass that
// finished having said nothing at all has nothing left to wait for.
func (m model) splashing() bool {
	return !m.heard && m.lastPass.IsZero()
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
		// hint, and the pane is the operator's own state. Neither a panel
		// key nor a resize edits detailOpen, so both routes here — moving
		// focus to a panel whose pane is open, and shrinking a window under
		// one — leave it open, and esc is the way out.
		if m.width >= minWidth && m.height >= m.minHeight(false) {
			// esc resolves nearest-first, so a live filter is what the first
			// one takes. Say so rather than promise a pane it will not
			// close: with no status bar here, an esc that looks like it did
			// nothing is the worse half of the trade.
			if m.search != "" {
				return "lerp — window too small\nesc clears the filter, then closes the pane\n"
			}
			return "lerp — window too small\nesc closes the pane\n"
		}
		return "lerp — window too small\n"
	}
	// Below the too-small screen, which is the actionable one: a splash on a
	// window that cannot draw a board would hide the one thing the operator
	// can do something about behind a spinner. And above everything else
	// except what has taken the keyboard — the ? overlay, a picker, an
	// eject — for mainOpen's reason rather than its exact list: something
	// that answers to keys while nothing it draws reaches the screen is a ?
	// that does nothing, or an enter that writes to Linear from under a
	// spinner. Only ? can be open here today, because every other one of
	// them is gated on a row, and a row means the pass has reported.
	//
	// A detail pane cannot be in that list at all: enter does not open one
	// while the splash is up, precisely so that this guard and the
	// too-small guard above it cannot disagree about a pane the operator
	// never saw.
	if m.splashing() && !m.modal() && !m.helpOn {
		return m.splash()
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
	// The footer costs a line, and it is only affordable when the rows can
	// still be shown in what is left: either they fit outright, or two lines
	// remain, which is the least windowRows needs to keep the selection
	// visible. Below that the rows win — a panel showing only "⋯ n more" has
	// lost the cursor the keys move. That holds for the search prompt too:
	// the title carries the query either way, so dropping the line costs the
	// cursor rather than the operator's place in what they typed.
	foot := ""
	if ih >= 3 || (ih == 2 && len(rows) <= 1) {
		foot = m.panelFooter(p, width)
	}
	if foot != "" {
		ih--
	}
	if cur.at >= 0 {
		rows = windowRows(rows, cur, ih)
	}
	if foot == "" {
		return rows
	}
	// Pinned to the last line rather than left floating under the rows: a
	// focused panel absorbs the layout's slack, and a key line adrift in
	// that space reads as one more row.
	rows = fitRows(rows, ih)
	for len(rows) < ih {
		rows = append(rows, "")
	}
	return append(rows, foot)
}

// panelFooter is the line the focused panel carries under its rows: the
// search prompt while it is open, otherwise the keys the row under the
// cursor answers to.
func (m *model) panelFooter(p panel, width int) string {
	if width < 1 {
		return ""
	}
	if m.searchOpen(p) {
		return m.searchInput.View()
	}
	return m.keyHint(p, width)
}

// searchOpen reports whether p is the panel the prompt is on. Inbox only,
// for now: it is the panel with rows enough to lose track of.
func (m *model) searchOpen(p panel) bool {
	return m.searching && p == panelAttention
}

// hasFooter is panelFooter's question without the rendering, for the
// geometry that buys the line.
func (m *model) hasFooter(p panel) bool {
	return m.searchOpen(p) || m.keyHints(p)
}

// keyHint is the focused panel's key line, rendered by bubbles/help so it
// fits the panel rather than overflowing it. The model's own help component
// draws it, on a copy: the ? overlay owns m.help.Width.
//
// bubbles drops whole hints off the end and marks what is left out with an
// ellipsis, so no hint is ever half-shown — which makes panelHelp's order
// the thing that decides what an inbox row's five keys lose on a narrow
// panel, and the ? overlay the place that still has all of them.
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
			// A search that matched nothing has no row to act on, but esc is
			// the way back and this line is where the operator looks for it.
			if m.search != "" {
				return []key.Binding{short(m.keys.ClearSearch, "clear search")}
			}
			return nil
		}
	case panelWork:
		if m.selectedWork() == nil {
			return nil
		}
	}
	return m.keys.panelHelp(p, rowKeys{
		// The raw toggle acts on the log in the pane, so the line offers it
		// where that log is on screen — the panel line advertises what this
		// frame answers to, the way it drops p where the picker has no room
		// to open. Under the ? overlay it is the one moment the pane is
		// certainly not showing a log, and the key is inert to match.
		hasLog:     m.logOnScreen(),
		hasURL:     browser.Openable(m.selectedURL()),
		filtered:   m.search != "",
		projects:   m.hasProjects(),
		canPromote: len(m.o.Statuses) > 0 && m.roomForMain(),
		canEject:   m.canEjectSelected(),
	})
}

// keyHints reports whether the focused panel is carrying a key line at all:
// nothing modal may have taken the keyboard — handleKey routes everything to
// the promote picker, to the search prompt and to eject's two panels, so
// those keys would be dead — and the row under the cursor has to answer to
// something. Both the height panelWant buys and the line panelBody draws ask
// this, so the two can never disagree about whether the line is there.
func (m *model) keyHints(p panel) bool {
	return !m.modal() && len(m.panelKeys(p)) > 0
}

// modal reports whether something has taken the keyboard off the panels. The
// picker and eject's two panels own the main pane with it; the search prompt
// is a row of the inbox panel instead, but it takes every keystroke the same
// way, which is what the key line is answering.
func (m *model) modal() bool {
	return m.promoting || m.searching || m.ejecting || m.ejection != nil
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
	if line := m.attentionEmptyLine(); line != "" {
		// The summary draws here too: an inbox with nothing on the operator
		// is precisely when the key that reveals the rest has to be visible.
		return append([]string{line}, m.backlogSummary()...), cursor{at: -1}
	}
	focused := m.focus == panelAttention
	cols := m.attentionColumns()
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
		rows = append(rows, attentionRow(it, focused && i == m.attnSel, cols, width, m.search))
	}
	return append(rows, m.backlogSummary()...), cursor{at: sel, span: 1}
}

// backlogSummary is the one line the fold leaves standing for the rows it
// holds back: how many, why they are not on the list, and the key that puts
// them there. The phrase is StatusBacklog's own note, so the fold and the
// status group header the rows arrive under say the same thing, and the key
// comes from the binding, so the line and the ? overlay can never disagree
// about what to press.
//
// A row rather than a line the panel pins: geometry counts what
// attentionRows returns, so the panel asks for it like any other row and
// the footer keeps the last line for the key hints. The cost is that a long
// blocked-on-you list can window it away behind "⋯ n more" — the overlay
// carries the key in that case.
//
// Nothing folded is no line: a key that would reveal nothing is not worth a
// row on a panel this tight.
//
// The key drops off the line while something modal has the keyboard, the
// same rule the panel's key hints answer to (see keyHints). The prompt
// swallows every keystroke, so a `B` pressed on this line's advice would
// land in the search box as a letter — and an advertised key that does
// nothing is worse than one left out. The count stays: it is the fact that
// explains why a search found nothing here, which is exactly when the
// prompt is open over it.
func (m *model) backlogSummary() []string {
	n := len(m.foldedRows())
	if n == 0 {
		return nil
	}
	line := fmt.Sprintf("%d %s", n, loop.StatusBacklog.Note())
	if !m.modal() {
		line += " — " + m.keys.Backlog.Help().Key + " to browse"
	}
	return []string{styleFaint.Render(line)}
}

// emptyNote says why an inbox that has rows is showing none of them: the
// search, the project scope, or — with neither of them narrowing it — the
// fold, which means everything the pass found is waiting to enter the
// pipeline and none of it is on a human. The panel never draws an empty box
// here: a list narrowed down to nothing and a board with nothing on it are
// the same picture, and only one of them is the goal state — which is why
// the fold does not get to claim "the inbox is empty".
func (m *model) emptyNote() string {
	switch {
	case m.search != "" && m.project != "":
		return fmt.Sprintf("no match for /%s in %s", m.search, m.project)
	case m.search != "":
		return "no match for /" + m.search
	case m.project != "":
		return "nothing in " + m.project
	}
	return "nothing is waiting on you"
}

// emptyHint is the keys that put those rows back — the one that clears
// whatever narrowed the list, and B where the fold is holding rows behind
// it. Both, because either can be the reason the panel is empty and the
// operator cannot tell which from the note alone.
func (m *model) emptyHint() string {
	var hints []string
	switch {
	case m.search != "":
		hints = append(hints, "(esc clears the search)")
	case m.project != "":
		hints = append(hints, "(P cycles the project filter back to all)")
	}
	if len(m.foldedRows()) > 0 {
		hints = append(hints, "(B browses the backlog)")
	}
	return strings.Join(hints, " ")
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

// attentionEmptyLine is the one line the inbox panel draws instead of a
// table, empty when there is a table to draw. It is the single reading of
// that question: the header sits above rows only when there are rows, and
// the two can never disagree about which the panel is showing.
func (m *model) attentionEmptyLine() string {
	switch {
	case !m.attentionSeen:
		return styleFaint.Render("reading the board…")
	case len(m.attention) == 0:
		return styleFaint.Render("the inbox is empty")
	case len(m.shown) == 0:
		return styleFaint.Render(m.emptyNote())
	}
	return ""
}

// The inbox table's column names. Six columns and no names is a table the
// reader is expected to decode — the leverage cell and the mark beside a
// status especially, which say nothing at all on their own. The header
// costs a line of a panel that has few to spare, so these are the shortest
// words that are still words; the marks inside the columns are spelled out
// in the ? overlay, which is the one place with room (see inboxLegend).
const (
	hdrTicket   = "ticket"
	hdrLeverage = "frees"
	hdrStatus   = "status"
	hdrProject  = "project"
	hdrPriority = "priority"
	hdrTitle    = "title"
)

// attentionColumns is the width of each padded column: the widest cell on
// the list, but never narrower than the header naming it — a header is a
// column of the table, measured with the rest, not a label squeezed on top
// of one.
type attentionColumns struct{ id, status, project int }

func (m *model) attentionColumns() attentionColumns {
	c := attentionColumns{id: len(hdrTicket), status: len(hdrStatus), project: len(hdrProject)}
	for _, it := range m.shown {
		c.id = max(c.id, lipgloss.Width(it.Ticket))
		c.status = max(c.status, lipgloss.Width(statusText(it)))
		c.project = max(c.project, lipgloss.Width(projectName(it.Project)))
	}
	return c
}

// attentionHeader is the inbox table's header row, faint, laid out through
// the same assembler as the rows so it elides with them and its labels
// stand over the columns they name. Empty when the panel is drawing an
// empty state rather than a table.
//
// It is drawn every time and not conditionally on how full the table is: a
// header that comes and goes is worse than none, because then its absence
// has to be read too. The panel pins it above the rows (see
// attentionPanel), so scrolling the list never scrolls it away.
func (m *model) attentionHeader(width int) string {
	if m.attentionEmptyLine() != "" {
		return ""
	}
	c := m.attentionColumns()
	return inboxLine(marker(false), headerCell(hdrTicket, c.id), headerCell(hdrLeverage, leverageW),
		headerCell(hdrStatus, c.status), headerCell(hdrProject, c.project),
		headerCell(hdrPriority, priorityW), styleFaint.Render(hdrTitle), width)
}

// headerCell is one column name padded out to its column.
func headerCell(label string, w int) string {
	return padTo(styleFaint.Render(label), w)
}

// The two columns that are the same width on every row: wide enough for the
// widest value each can hold, and for the header that names it.
const (
	leverageW = len(hdrLeverage) // wider than ⊘ or ↓n
	priorityW = len(hdrPriority) // wider than "Urgent"
)

// A column earns its width only while the title still reads as one. Below
// titleFloor the project drops out of the row entirely and the title takes
// the space back — a title cut shorter than this has stopped being a title,
// and the project is the one column a routing decision can most often do
// without. The priority is held to a cheaper bar, titleStub: it costs a
// fraction of the project's width, so it goes on paying for itself down to a
// title as narrow as the priority column is itself.
const (
	titleFloor = 20
	titleStub  = priorityW + 2
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
// themselves, taking the status — the last of them, and the only one a
// selected row repeats — before the identifier and the leverage, which
// survive any width.
//
// query is the search the row highlights its matches from, "" for no search.
//
// The selected row takes the band across the panel's whole inner width —
// past the title's own cut, which is why the band is laid on the assembled
// line rather than built into any one cell.
func attentionRow(it loop.AttentionItem, selected bool, c attentionColumns, width int, query string) string {
	id := padTo(highlight(it.Ticket, query, styleTicket), c.id)
	row := inboxLine(marker(selected), id, leverageCell(it), statusCell(it, c.status, query),
		projectCell(it.Project, c.project, query), priorityCell(it.Priority),
		highlight(it.Title, query, stylePlain), width)
	if selected {
		row = selectRow(row, width)
	}
	return row
}

// inboxLine assembles one line of the inbox table from cells already padded
// to their columns and already carrying their highlights — a ticket's row,
// or the header naming them — so the two are laid out by one piece of code
// and cannot drift apart.
func inboxLine(mark, id, leverage, status, project, priority, title string, width int) string {
	// statusCell pads to the column and no further, so head carries the
	// gutter itself: every branch below ends in one, and a status wide
	// enough to leave no pad of its own still cannot touch the title.
	head := mark + id + " " + leverage + " " + status + "  "
	full := head + project + "  " + priority + "  "
	noProject := head + priority + "  "
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
	return ansi.Truncate(cols+title, max(0, width), "…")
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

// statusCell is statusText padded out to the status column, with the
// search's matches marked inside it. The mark is not part of the status
// name, so it is appended after the highlight rather than searched through.
func statusCell(it loop.AttentionItem, w int, query string) string {
	cell := highlight(it.Status, query, stylePlain)
	if it.Relevance == loop.StatusUnnamed {
		cell += " " + styleAttention.Render("⚠")
	}
	return padTo(cell, w)
}

// projectCell is the row's project column, a dash for a ticket filed under
// no project. The dash is not a project name and never highlights: a row
// with no project is on screen because something else about it matched.
func projectCell(project string, w int, query string) string {
	name := projectName(project)
	cell := highlight(name, query, stylePlain)
	if project == "" {
		cell = styleFaint.Render(name)
	}
	return padTo(cell, w)
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
		return padTo(styleAttention.Render("⊘"), leverageW)
	}
	cell := fmt.Sprintf("↓%d", it.Unblocks)
	if it.Unblocks > 0 {
		return padTo(styleTicket.Render(cell), leverageW)
	}
	return padTo(styleFaint.Render(cell), leverageW)
}

// priorityCell renders Linear's priority scale as its own words. An unset
// priority is a dash: saying "none" would read as a rank of its own. The
// cell is padded to priorityW so the columns to its left stay put from row
// to row.
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
	return padTo(style.Render(label), priorityW)
}

func (m model) attentionPanel(w, h int) string {
	focused := m.focus == panelAttention
	// Two different questions: focused is whose selection this is, and it
	// is why the row stays marked while the pane reads it; keys is which
	// surface the keys are talking to, which is what the border says.
	keys := focused && !m.mainFocused()
	extra := ""
	// The title counts what this panel can show under the current fold, not
	// what the pass found: the fraction under a filter is over that same
	// base, so the title and the rows beneath it always agree. It differs
	// from the status bar's number on purpose while the backlog is open —
	// the bar answers "should I look up", the title "what is in this panel".
	base := m.unfolded()
	if len(m.attention) > 0 {
		// The sort mode, the project filter and the fold live in the title
		// because they are the only things about this panel a key changed,
		// and a table sorted or folded differently than the operator
		// remembers is worse than one that says how it is. They are not
		// gated on the count below: a board worked down to nothing but
		// backlog has no count to show and still answers to `s`, and a sort
		// that changed without the title moving is a silent one.
		if len(base) > 0 {
			count := fmt.Sprintf(" ● %d", len(base))
			if m.project != "" || m.search != "" {
				count = fmt.Sprintf(" ● %d/%d", len(m.shown), len(base))
			}
			extra = styleAttention.Render(count)
		}
		// The query sits next to the fraction, ahead of the two controls
		// that were already here: they are the one fact that explains the
		// other, and a title truncated by a narrow panel loses them last.
		if m.search != "" {
			extra += styleFocus.Render(" · /" + m.search)
		}
		extra += styleFaint.Render(" · by " + m.sortMode.String())
		if m.project != "" {
			extra += styleFaint.Render(" · " + m.project)
		}
		// The way back from an expanded backlog: the rows below are not the
		// panel's default, and the title is where this panel says so.
		if m.backlogOpen {
			extra += styleFaint.Render(" · backlog")
		}
	}
	inner := padList.inner(w)
	rows, cur := m.attentionRows(inner)
	// The header is pinned rather than listed: windowing a header is how a
	// header scrolls away. It costs the rows a line, and — by the same rule
	// panelBody holds the key hint to — only when what is left can still
	// show the rows: either two lines remain, the least windowRows needs to
	// keep the selection visible, or the rows all fit in one line anyway.
	// The rule reads the height and not the focus, so tabbing between
	// panels can never be what makes a column header appear.
	ih := h - 2
	header := ""
	if ih >= 3 || len(rows) <= ih-1 {
		if header = m.attentionHeader(inner); header != "" {
			ih--
		}
	}
	rows = m.panelBody(panelAttention, rows, cur, inner, ih)
	if header != "" {
		rows = append([]string{header}, rows...)
	}
	return panelBox(panelTitle(1, "inbox", keys, extra), keys, w, h, rows, padList, nil)
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
	keys := focused && !m.mainFocused() // see attentionPanel
	// Capacity has two homes now that the lane rows are gone: this title and
	// the status bar. It is the number that says whether anything can start.
	extra := styleFaint.Render(" · " + m.capacityLabel())
	rows, sel := m.workListRows(padList.inner(w))
	rows = m.panelBody(panelWork, rows, sel, padList.inner(w), h-2)
	return panelBox(panelTitle(2, "work", keys, extra), keys, w, h, rows, padList, nil)
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
//
// The selection band covers whatever the row actually drew — the one line
// of a waiting ticket, both lines of a running one — so it never marks a
// span the row does not own. A squeezed panel cuts the second line after
// this, and the first keeps the band it was given.
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
		return selectLines([]string{splitRow(marker(selected)+"  "+name, right, width)}, selected, width)
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
	// second line existed. What the run has spent joins it there for the
	// same reason — it is a fact about the whole run, like its age, and not
	// a reading of the moment.
	totals := elapsed(r.since)
	if r.tokens > 0 {
		totals += " · " + tokenCount(r.tokens)
	}
	right := state + " " + styleFaint.Render(totals)
	lines := []string{splitRow(marker(selected)+dot+" "+name, right, width)}
	if reading := runLine(r, width); reading != "" {
		lines = append(lines, reading)
	}
	return selectLines(lines, selected, width)
}

// selectLines bands every line of a row the cursor is on, and hands back an
// unselected row untouched.
func selectLines(lines []string, selected bool, width int) []string {
	if !selected {
		return lines
	}
	for i, l := range lines {
		lines[i] = selectRow(l, width)
	}
	return lines
}

// runLine is the second line of a row a lane holds: the last thing its log
// says the agent did, and a sparkline of the activity behind it. Beside the
// elapsed clock on the line above, they answer what elapsed alone cannot —
// whether a run that started four minutes ago is still doing something, and
// what it is doing.
//
// The line used to say how long since the log last grew. The sparkline was
// already the better answer to that — a number falling behind says the same
// thing as a line falling flat, and only the line says how long it had been
// busy first — so the columns go to the one reading nothing else on the
// board carries: what the agent just ran. Opening the log pane is still the
// way to read more than one line of it; this is the line the operator would
// have opened it for.
//
// It is empty for a run with no log to read, which is a lane still
// provisioning: a blank line under the row would claim a reading that does
// not exist, and cost the panel a row to say nothing. A log that exists but
// has not reached a tool call yet keeps the line and leaves the left of it
// empty — the sparkline is still a reading, and an agent that has only been
// thinking has done nothing to name.
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
	left := "    " + lastCall(r.tool, r.target)
	// The call is what the agent did; the sparkline is the shape around it.
	// splitRow protects its right column against a narrow panel, and here
	// the right column is the one that can be spared — a panel too narrow
	// for both keeps the call and drops the line.
	//
	// The line takes the width it is given: one cell per free column, up to
	// the whole history the ring holds. On a wide terminal's full-width
	// list a row draws back a quarter of an hour; the same row beside an
	// open detail pane draws the recent end of that history. What a cell
	// covers is the same either way — fifteen seconds, wherever the line is
	// drawn — so a narrow row is a shorter reach, never a coarser one. The
	// bar heights are the drawn window's own scale, as they always were:
	// narrowing the row rescales them to what is left in view, which is the
	// same trade the row already makes against the run beside it.
	right := ""
	if room := width - lipgloss.Width(left) - 1; room >= min(sparkMinCells, len(r.rate)) {
		if cells := min(room, len(r.rate)); cells > 0 {
			right = styleFaint.Render(sparkline(r.rate[len(r.rate)-cells:]))
		}
	}
	return splitRow(left, right, width)
}

// lastCall renders one tool call for a work row: the tool, then what it acted
// on. Most calls are shell commands, and spelling the tool out for those
// would spend the row's columns saying "Bash" over and over — a $ says it in
// one, the way a prompt does, and leaves the command itself the readable part
// of the line. Every other tool keeps its name, because "model.go" alone does
// not say whether it was read or written.
//
// The target is the agent's own text and untrusted like everything else the
// log carries (logfmt bounds its length; clean makes it inert). The line is
// faint but for that text: the row above it is the ticket, and this is the
// one thing on this line the eye is looking for.
func lastCall(tool, target string) string {
	if tool == "" {
		return ""
	}
	prefix := clean(tool)
	if strings.EqualFold(tool, "bash") || strings.EqualFold(tool, "shell") {
		prefix = "$"
	}
	if target == "" {
		return styleFaint.Render(prefix)
	}
	return styleFaint.Render(prefix+" ") + clean(target)
}

// tokenCount renders what a run has spent in the columns a work row can
// spare: 1,400 is 1.4k, 847,000 is 847k, 5,200,000 is 5.2M. The decimal goes
// where it changes the reading and not where it is noise, and the cutover to
// M is a hair under the million so that 999,900 does not draw as 1000k.
//
// The figure is the run's own even for a run adopted mid-way: the pulse reads
// the log from its start, and the log records every call the run was billed
// for.
func tokenCount(n int) string {
	var s string
	switch {
	case n >= 999_500:
		s = fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		s = fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1_000:
		s = fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		s = fmt.Sprintf("%d", n)
	}
	// The unit stays on: a bare number beside a clock reads as another
	// duration.
	return s + " tok"
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
			strings.Split(m.vp.View(), "\n"), padMain, m.mainScrollbar(h))
	}
	title := m.mainTitle()
	// The pane lights up the same way a panel does — heavy box, title in
	// the focus accent — so "where are my keys pointed" is answered by the
	// chrome the operator already reads for it. Between the two panels and
	// this pane it is one surface at a time: paneTakesKeys is what the
	// panels ask too, so the box can only move, never split. The overlay
	// and the modals are the exception, and were before this: each draws
	// itself focused in this pane while the panel behind it stays focused
	// too — they have the keyboard outright, so neither box is a claim on
	// keys the other one has.
	if m.mainFocused() {
		return panelBox(styleTitleFocus.Render(title), true, w, h,
			strings.Split(m.vp.View(), "\n"), padMain, m.mainScrollbar(h))
	}
	return panelBox(styleFaint.Render(title), false, w, h,
		strings.Split(m.vp.View(), "\n"), padMain, m.mainScrollbar(h))
}

// mainScrollbar is the pane's position indicator over whatever the viewport
// currently holds — reused across the ticket lens, the log tail, its raw
// toggle and the help overlay alike, since all four are this same vp drawn
// through this same box. h is the panel's outer height, matching panelBox's
// own ih = h-2, so the two can never disagree about how many rows the thumb
// has to cover.
func (m model) mainScrollbar(h int) *scrollbar {
	sb, ok := scrollThumb(m.vp.TotalLineCount(), h-2, m.vp.YOffset)
	if !ok {
		return nil
	}
	return &sb
}

// helpText is the ? overlay: every binding the keymap declares, and then
// the legend for the marks the inbox draws inside its columns.
func (m model) helpText() string {
	return strings.Join(append(strings.Split(m.help.View(m.keys), "\n"), inboxLegend()...), "\n")
}

// inboxLegend spells out the three marks the inbox table draws inside its
// columns. The header names the columns; a glyph standing in one still says
// nothing on its own, and the ? overlay is the one place with the room to
// say it in a sentence. Each mark is rendered exactly as the row renders
// it, so the legend is read by matching shapes and not by trusting a
// description of one.
func inboxLegend() []string {
	rows := []string{"", styleFaint.Render("inbox marks")}
	for _, l := range []struct{ glyph, says string }{
		{styleTicket.Render("↓n"), "routing this frees n other tickets"},
		{styleAttention.Render("⊘"), "something unfinished still blocks it"},
		{styleAttention.Render("⚠"), "the pipeline never named this status"},
	} {
		rows = append(rows, "  "+padTo(l.glyph, 2)+"  "+styleFaint.Render(l.says))
	}
	return rows
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
	return panelBox(styleTitleFocus.Render("promote "+it.Ticket), true, w, h, rows, padMain, nil)
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
	return panelBox(styleTitleFocus.Render("eject "+r.ticket), true, w, h, rows, padMain, nil)
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
	return panelBox(styleTitleFocus.Render(title), true, w, h, rows, padMain, nil)
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
		return styleFaint.Render(m.emptyNote()) + "\n" + styleFaint.Render(m.emptyHint())
	}
	it := m.selectedAttention()
	// These lines come from the pass and always render first, whatever the
	// read of the ticket itself is doing: a failed fetch must never cost the
	// operator the pane that works today.
	lines := []string{
		styleTicket.Render(it.Ticket) + " " + it.Title,
		"",
		styleFaint.Render("status  ") + statusText(*it),
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
	return strings.Join(append(lines, m.ticketLines(it.TicketID, width, browser.Openable(it.URL))...), "\n")
}

// ticketLines is the ticket itself, below the pass's own lines: the body,
// then the comments oldest first — so lerp's last stage-boundary artifact,
// the verdict that parked the ticket, is where the eye lands. Read-only and
// flat: nothing here is selectable, no thread is followed, no other ticket
// is reachable from it. Markdown is rendered (see markdown.go); `o` is the
// answer to anything that wants more — but only where `o` has a door to
// open, so hasDoor is the same question the key line asks. A read that
// failed on a row whose URL the opener refuses has nowhere to send the
// operator, and saying so twice is worse than saying nothing.
func (m model) ticketLines(ticketID string, width int, hasDoor bool) []string {
	d := m.details[ticketID]
	switch {
	case d == nil:
		return nil
	case d.state == detailLoading:
		return []string{"", styleFaint.Render("reading the ticket…")}
	case d.state == detailFailed:
		failed := []string{"", styleFaint.Render("couldn't read the ticket: " + d.err)}
		if hasDoor {
			failed = append(failed, styleFaint.Render("o opens it in Linear"))
		}
		return failed
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
	// A claimed row with no lane is the one gate a keystroke can lift: the
	// claim may be this operator's own, left on the ticket by a run that died
	// where nothing reaps it. Naming S here is what makes that recoverable
	// rather than merely visible — every other row keeps the ordering hint,
	// since ordering is deliberately not a keystroke.
	hint := "to change what runs next, move the ticket in Linear"
	if r.lane == 0 && r.assigned && len(r.blockedBy) == 0 {
		hint = "S takes over your own claim and runs it here"
	}
	return strings.Join([]string{
		styleTicket.Render(r.ticket) + " " + r.title,
		"",
		styleFaint.Render("queue   ") + queue,
		styleFaint.Render("pickup  ") + gate,
		styleFaint.Render("linear  ") + r.url,
		"",
		styleFaint.Render(hint),
	}, "\n")
}

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

// overdueAfter is how long the bar sits on a finished pass before calling
// the board stale. Deliberately far longer than the poll: a pass is due one
// interval after the last one landed and the tick re-arms the moment it
// does, so a minute of nothing is a wedged loop or a slept machine, not
// ordinary scheduling slack — and a board a few seconds behind is not news
// to anyone reading it. A pass that is merely running long is the spinner's
// to report, however late it is.
const overdueAfter = time.Minute

// overdue reports that nothing has run for long enough to say so. Wall clock
// on purpose: a machine that slept stops the monotonic reading time.Since
// would otherwise use — and stops the pending tick with it — so the one case
// that leaves a board hours out of date is the one case a monotonic
// comparison cannot see. Round(0) drops the monotonic reading from the copy
// this compares.
func (m model) overdue() bool {
	return !m.inFlight && !m.lastPass.IsZero() && time.Since(m.lastPass.Round(0)) > overdueAfter
}

// What the heartbeat can say, and the room the bar keeps for it. The slot is
// held open whether or not there is a heartbeat in it, so it is the widest
// of them and not whichever one is showing — a slot sized to the line of the
// moment would move everything beside it as the lines took turns.
const (
	heartRunning  = "pass running…"
	heartOverdue  = "pass overdue"
	heartStarting = "starting…"
)

var heartbeatSlot = func() int {
	// Every frame, not the first one: the frames are all one column wide
	// today, and a spinner whose were not would have the padding below going
	// negative on the frames the slot was not measured against.
	w := max(lipgloss.Width(heartOverdue), lipgloss.Width(heartStarting))
	for _, frame := range heartbeatFrames {
		w = max(w, lipgloss.Width(frame+" "+heartRunning))
	}
	return w
}()

// pickerLine is the promote picker's instructions, in the room the bar has
// for them. They are bindings rather than a string, drawn by the same
// component that draws the panels' key lines — one renderer, so the
// separator and the faint are declared once and a rebind moves the line
// with the keys.
//
// Reading the keys off the bindings costs columns the hardcoded line never
// paid, and this is where they come from. The bar gives up a key line
// before the heartbeat and the heartbeat before "● n in the inbox", so the
// room offered here is what is left after both: what a narrow window spends
// is the pair that only moves inside the picker, not somebody else's
// segment. Both lines are rendered whole and measured — bubbles' own fitting
// keeps an over-wide hint when the ellipsis marking the cut would not fit
// either, which is a way to come back wider than the room.
//
// Below the exits there is nothing left to drop, and they are handed back
// anyway: a line that cannot say how to leave the picker is worse than a
// clipped count, which the panel underneath is showing in full regardless.
// statusBar's truncation then takes what it takes, exactly as it did while
// this was a string.
func (m model) pickerLine(room int) string {
	h := m.help
	h.Width = 0
	if full := h.ShortHelpView(m.keys.promoteHelp()); lipgloss.Width(full) <= room {
		return full
	}
	return h.ShortHelpView(m.keys.promoteExits())
}

// statusBar is the heartbeat line: the lerp mark, what the pass is doing
// when that is worth saying, capacity, inbox count, keys. A pass error — or
// a transient note like a promote's outcome — takes over the whole line; a
// truncated error is not actionable, so nothing else competes with it for
// the width.
func (m model) statusBar() string {
	// A note reports on a pass and holds the line until the next pass starts.
	// Where no next pass is coming — a wedged loop, a machine that slept —
	// it has stopped being transient, and a two-hour-old ✓ sitting there
	// would hide the one thing worth saying about a board in that state.
	// Nothing competes with the note line; the note that outlived its pass
	// just stops being one.
	if line := m.noteLine(); line != "" && !m.overdue() {
		return ansi.Truncate(line, m.width, "…")
	}
	// The corner is the bar's one fixed point: same text, same weight, same
	// width, every frame. Which panel holds the keys is already drawn by the
	// panel borders, so the focus badge that used to sit here said it twice.
	brand := styleMark.Render(markWord)

	// The heartbeat speaks only when there is something to say. "Is the
	// board fresh?" is the bar's real question, and yes is silence: a pass
	// landing on schedule needs no words, where the countdown to the next
	// one re-rendered at second precision and changed width as it went,
	// shoving everything right of it sideways once a second.
	var heart string
	switch {
	case m.inFlight:
		heart = styleRunning.Render(heartbeatFrames[m.frame%len(heartbeatFrames)]) + " " + heartRunning
	case m.lastPass.IsZero():
		heart = styleFaint.Render(heartStarting)
	case m.overdue():
		// A state, not a clock: that the board is stale is the whole fact,
		// and how stale changes nothing the operator would do about it.
		heart = styleAttention.Render(heartOverdue)
	}

	// The heartbeat is the only segment here that comes and goes, so the bar
	// is laid out around the room it keeps for it rather than around the
	// heartbeat itself — see below. Placed in front of the capacity and the
	// inbox count it shoved both of them a spinner's width sideways every
	// time a pass started; sized into the hints below, it moved those.
	left := brand + "  " + styleFaint.Render(m.capacityLabel())
	// Only the tickets blocked on a human are counted, so the number means
	// "things that should make you look up" — and it does not move when B
	// expands the backlog into the panel.
	if n := m.blockedOnYou(); n > 0 {
		left += "  " + styleAttention.Render(fmt.Sprintf("● %d in the inbox", n))
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
	case m.mainFocused():
		// The keys are somewhere they have never been before, so the bar
		// says how to leave as well as how to close: tab carries on round
		// the cycle it came in on. esc is the same esc it was — including
		// that a live filter is what it takes first, which the panel's own
		// line says with "esc clear" while one is on.
		hint = "tab next · esc close · " + hint
	case m.detailOpen[m.focus]:
		hint = "esc close · " + hint
	case m.roomForMain():
		hint = "enter detail · " + hint
	}
	// A modal has the keyboard, so its own instructions replace the line.
	switch {
	case m.ejecting:
		hint = "enter eject · esc cancel"
	case m.ejection != nil:
		hint = "esc dismiss"
	case m.searching:
		hint = "type to filter · enter accept · esc cancel"
	}
	right := styleFaint.Render(hint)

	// The heartbeat's room is held open whether or not there is a heartbeat
	// in it. That is what lets it come and go without moving anything: every
	// other segment is placed against a width that does not depend on it,
	// where sizing the bar around the heartbeat itself would have let a pass
	// starting decide, once an interval, whether the hints could still
	// afford the pane's key.
	slot := heartbeatSlot + 2
	// The picker's line is fitted against that held-open room rather than
	// into it: a key line is the segment the bar gives up before the
	// heartbeat, so the picker's hints go before the heartbeat does.
	if m.promoting {
		right = m.pickerLine(m.width - lipgloss.Width(left) - slot - 1)
	}
	fits := func(slot int) bool {
		return m.width-lipgloss.Width(left)-slot-lipgloss.Width(right) >= 1
	}

	// The pane's segment is the first thing the bar gives up — before the
	// heartbeat, which is the one segment here that reports something is
	// happening rather than which key does what. Below it the truncation
	// takes the left side instead, and what it would take is "● n in the
	// inbox" — the one number the needs-you panel exists for, spent on
	// advertising a key. A modal's line is not a hint but its instructions,
	// so it is not up for this.
	if !m.modal() && !fits(slot) {
		right = styleFaint.Render(globals)
	}
	// Narrower than that and the bar carries no heartbeat at all — at every
	// frame, so the window that cannot hold one is silent about passes the
	// whole time rather than flickering one in and out. What decides it is
	// the room, never which line the heartbeat would be showing, so it does
	// not come and go under a board doing the same thing throughout; and
	// widening a window never takes the heartbeat away.
	if !fits(slot) {
		slot = 0
	}

	pad := m.width - lipgloss.Width(left) - slot - lipgloss.Width(right)
	if pad < 1 {
		right = ansi.Truncate(right, max(0, m.width-1), "…")
		left = ansi.Truncate(left, max(0, m.width-lipgloss.Width(right)-1), "…")
		pad = max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right))
	}
	if slot > 0 {
		left += "  " + heart + strings.Repeat(" ", slot-2-lipgloss.Width(heart))
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
