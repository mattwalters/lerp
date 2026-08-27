package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/loop"
)

// countingTicker stands in for the reconciler: the TUI only needs something
// to tick.
type countingTicker struct {
	ticks atomic.Int64
}

func (c *countingTicker) Tick(context.Context) { c.ticks.Add(1) }

// recordingPromoter stands in for the reconciler's one write action: it
// records every Promote call, so a test can assert what the picker sent,
// and returns whatever err is set to — or, for a ticket named in errs, that
// ticket's own error, so a batch test can fail one of several calls.
type recordingPromoter struct {
	mu    sync.Mutex
	calls []promoteCall
	err   error
	errs  map[string]error
}

type promoteCall struct {
	ticketID string
	status   string
}

func (p *recordingPromoter) Promote(_ context.Context, ticketID, status string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, promoteCall{ticketID, status})
	if err, ok := p.errs[ticketID]; ok {
		return err
	}
	return p.err
}

func (p *recordingPromoter) last() promoteCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		return promoteCall{}
	}
	return p.calls[len(p.calls)-1]
}

// recordingEjector stands in for the reconciler's escape hatch: it records
// every Eject call and hands back whatever ejection or error is set on it.
// resumable is which queues CanEject says yes to — empty means every queue,
// so the tests that do not care about the greyed-out case need say nothing.
type recordingEjector struct {
	mu        sync.Mutex
	calls     []string
	resumable []string
	ejection  loop.Ejection
	err       error
}

func (e *recordingEjector) Eject(_ context.Context, ticketID string) (loop.Ejection, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, ticketID)
	return e.ejection, e.err
}

func (e *recordingEjector) CanEject(queue string) (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.resumable) == 0 || slices.Contains(e.resumable, queue) {
		return true, ""
	}
	return false, "runner has no resume command"
}

func (e *recordingEjector) ejected() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

// recordingStarter stands in for the reconciler's force-start: it records
// every ticket it was asked to run past the limit, and returns whatever err
// is set to — the refusals themselves are the reconciler's, not the TUI's.
type recordingStarter struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (s *recordingStarter) ForceStart(_ context.Context, ticketID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, ticketID)
	return s.err
}

func (s *recordingStarter) started() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// recordingReader stands in for the reconciler's one read beyond the pass.
// It records every ticket it was asked for — so a test can prove that
// walking the list does not fire a fetch per row — and hands back whatever
// detail or error is set on it.
type recordingReader struct {
	mu     sync.Mutex
	calls  []string
	detail linear.IssueDetail
	err    error
}

func (r *recordingReader) IssueDetail(_ context.Context, ticketID string) (linear.IssueDetail, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ticketID)
	return r.detail, r.err
}

func (r *recordingReader) fetched() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *recordingReader) returns(detail linear.IssueDetail, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detail, r.err = detail, err
}

// defaultTestStatuses are the promote picker's options in tests that do not
// care which ones — two, so up/down has somewhere to go.
var defaultTestStatuses = []string{"Planning", "Implementing"}

// fakeEngine is the recording roles as the one interface the shell takes.
// The embedded pointers are what satisfy Engine, so a role added to it fails
// here at compile time exactly as it does at the two production call sites —
// which is the whole point of the bundle.
type fakeEngine struct {
	*countingTicker
	*recordingPromoter
	*recordingEjector
	*recordingStarter
	*recordingReader
}

// newFakeEngine is a whole engine nobody is going to look at, for the tests
// that need the shell wired but assert on none of it.
func newFakeEngine() fakeEngine {
	return fakeEngine{&countingTicker{}, &recordingPromoter{}, &recordingEjector{}, &recordingStarter{}, &recordingReader{}}
}

// newTestModel is the stock model at a stock window size. Lerp opens focused
// on the inbox with the inbox's pane closed, so a test about the work panel,
// its main pane or the geometry that pane drives presses "2" first — without
// it, it is asserting against the wrong panel.
func newTestModel(t *testing.T, lanes int) (model, *countingTicker, chan loop.Event) {
	t.Helper()
	m, ticker, events := newTestModelWith(t, lanes, defaultTestStatuses, &recordingPromoter{}, &recordingEjector{}, &recordingStarter{}, &recordingReader{})
	return m, ticker, events
}

// newPromoteTestModel is newTestModel plus the recording promoter, for tests
// that drive the promote picker and need to see what it sent.
func newPromoteTestModel(t *testing.T, lanes int, statuses []string) (model, *countingTicker, chan loop.Event, *recordingPromoter) {
	t.Helper()
	promoter := &recordingPromoter{}
	m, ticker, events := newTestModelWith(t, lanes, statuses, promoter, &recordingEjector{}, &recordingStarter{}, &recordingReader{})
	return m, ticker, events, promoter
}

// newEjectTestModel is newTestModel plus the recording ejector, for tests
// that drive eject and need to see what it sent.
func newEjectTestModel(t *testing.T, lanes int, ejector *recordingEjector) (model, chan loop.Event) {
	t.Helper()
	m, _, events := newTestModelWith(t, lanes, defaultTestStatuses, &recordingPromoter{}, ejector, &recordingStarter{}, &recordingReader{})
	return m, events
}

// newReadingTestModel is newTestModel plus the recording reader, for tests
// that drive the inbox pane's read of the selected ticket.
func newReadingTestModel(t *testing.T) (model, chan loop.Event, *recordingReader) {
	t.Helper()
	reader := &recordingReader{}
	m, _, events := newTestModelWith(t, 1, defaultTestStatuses, &recordingPromoter{}, &recordingEjector{}, &recordingStarter{}, reader)
	return m, events, reader
}

// newStartingTestModel is newTestModel plus the recording starter, for tests
// that press the force-start key and need to see what it sent.
func newStartingTestModel(t *testing.T, lanes int) (model, chan loop.Event, *recordingStarter) {
	t.Helper()
	starter := &recordingStarter{}
	m, _, events := newTestModelWith(t, lanes, defaultTestStatuses, &recordingPromoter{}, &recordingEjector{}, starter, &recordingReader{})
	return m, events, starter
}

func newTestModelWith(t *testing.T, lanes int, statuses []string, promoter *recordingPromoter, ejector *recordingEjector, starter *recordingStarter, reader *recordingReader) (model, *countingTicker, chan loop.Event) {
	t.Helper()
	ticker := &countingTicker{}
	events := make(chan loop.Event, 8)
	m := newModel(context.Background(), Options{
		Engine:   fakeEngine{ticker, promoter, ejector, starter, reader},
		Statuses: statuses,
		Interval: time.Millisecond,
		Lanes:    lanes,
		Events:   events,
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return resized.(model), ticker, events
}

func update(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(model)
}

// pastTheSplash lands the first pass on a fresh model. The opening splash
// owns the whole screen until one reports (see splashing), so a test about
// what the board draws starts by getting past it — with the pass reporting
// nothing, which is the weaker of the two ways out and leaves the panels on
// their own empty states.
func pastTheSplash(t *testing.T, m model) model {
	t.Helper()
	m = update(t, m, tickedMsg{})
	if m.splashing() {
		t.Fatal("the splash still owns the screen after the first pass landed")
	}
	return m
}

// openMain opens the focused panel's main pane. Both panels start with the
// list owning the screen, so a test about what the pane holds asks for it
// with the key an operator would press rather than reaching into the model.
func openMain(t *testing.T, m model) model {
	t.Helper()
	m = update(t, m, keyMsg("enter"))
	if !m.mainOpen() {
		t.Fatal("enter did not open the main pane")
	}
	return m
}

// updateCmd is update for the messages whose command is the assertion — the
// detail fetch, which either fires or does not.
func updateCmd(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestWorkPanelShowsTheRunLifecycle(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = pastTheSplash(t, m)
	view := m.View()
	for _, want := range []string{"inbox", "work", "q quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("initial view is missing %q:\n%s", want, view)
		}
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})
	view = m.View()
	for _, want := range []string{"LERP-42", "implement", "1/2 running"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view after start is missing %q:\n%s", want, view)
		}
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", ExitCode: 0}})
	view = m.View()
	if !strings.Contains(view, "LERP-42 exited 0") {
		t.Fatalf("view after exit does not note the outcome:\n%s", view)
	}
	if !strings.Contains(view, "0/2 running") {
		t.Fatalf("view after exit still counts a running ticket:\n%s", view)
	}
	if rows := m.workRows(); len(rows) != 0 {
		t.Fatalf("finished run left %d rows behind: %+v", len(rows), rows)
	}
}

// A skipped hop is the operator's business — a stage of their pipeline did
// not run — so it rides the exit event onto the status bar, not into the log
// file alone.
func TestExitedEventReportsASkippedHop(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	note := `LERP-42 left "Implementing" for "In Progress" during its run — ` +
		`the on_success hop to "Agent Review" was skipped.`
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", ExitCode: 0, Note: note}})
	if !strings.Contains(m.View(), "the on_success hop") {
		t.Fatalf("view does not report the skipped hop:\n%s", m.View())
	}
	if len(m.notes) != 1 || m.notes[0].text != note || !m.notes[0].warn {
		t.Errorf("status notes = %+v, want one warning note %q", m.notes, note)
	}
	if strings.Contains(m.View(), "✓ LERP-42 left") {
		t.Errorf("a skipped hop is reported as a success:\n%s", m.View())
	}
}

// Claude states a run's cost on the same result line that ends its log, at
// the exact moment its row is about to be torn down — and the loop's own
// record of that run, log included, may already be gone by the time a
// subscriber reacts to EventExited (see runCost in internal/loop). So the
// event itself carries the final figure, computed by the loop before either
// of those disappear, and the TUI's only job is to put it on the exit note
// where it survives the row.
func TestExitedEventCarriesTheRunsFinalCost(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", ExitCode: 0, Cost: 0.42}})

	if view := m.View(); !strings.Contains(view, "LERP-42 exited 0 · $0.42") {
		t.Fatalf("the exit note does not carry the run's cost:\n%s", view)
	}
}

// A skipped hop's note replaces the plain "exited" outcome with the larger
// story, but the run still cost money either way: the figure must not be
// lost along with the outcome it replaced. A short note here, deliberately —
// the real one is long enough to be truncated on its own at this width, which
// would make the assertion about truncation rather than about the cost.
func TestSkippedHopNoteStillCarriesCost(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", ExitCode: 0, Note: "hop skipped", Cost: 0.42}})
	if view := m.View(); !strings.Contains(view, "hop skipped · $0.42") {
		t.Fatalf("the skipped-hop note does not carry the run's cost:\n%s", view)
	}
}

func TestProvisioningTicketIsOccupied(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventProvisioning, RunID: "r1", Lane: 1,
		TicketID: "id-9", Ticket: "LERP-9", Queue: "plan", StartedAt: time.Now()}})
	view := m.View()
	for _, want := range []string{"provisioning", "LERP-9", "1/1 running"} {
		if !strings.Contains(view, want) {
			t.Fatalf("provisioning ticket is missing %q from the panel:\n%s", want, view)
		}
	}
}

func TestAdoptedRowShowsTrueRunAge(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Queue: "plan", StartedAt: time.Now().Add(-2 * time.Hour)}})
	view := m.View()
	if !strings.Contains(view, "2h0m") {
		t.Fatalf("adopted row does not show the run's true age:\n%s", view)
	}
}

// An adopted run is a live agent on a ticket, which is all the operator acts
// on, so the work panel draws it exactly as it draws a run this process
// started. That a successor took the run over stays a diagnostic fact, in
// .lerp/loop.log; it is not a badge on the screen.
func TestAdoptedRunReadsAsRunning(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Queue: "plan", LogPath: "/dev/null"}})
	rows := m.workRows()
	if len(rows) != 1 {
		t.Fatalf("panel has %d rows, want the one adopted run", len(rows))
	}
	// Rendered against the same row in the state a run this process started
	// would be in: identical output is the whole claim, and it holds the dot
	// as well as the word — under the Ascii profile a test sees the shape but
	// not the colour, so an assertion on either alone would miss the other.
	// Without the trailing clock: elapsed is recomputed per render, so a
	// second falling between the two calls would fail this for no reason.
	line := func(r workRow) string {
		s := m.workRowLines(r, false, 80)[0]
		return s[:strings.LastIndex(s, " ")]
	}
	row := rows[0]
	adopted := line(row)
	row.state = laneRunning
	started := line(row)
	if adopted != started {
		t.Errorf("adopted row is drawn differently from a started one:\n%q\n%q", adopted, started)
	}
	if !strings.Contains(started, styleFaint.Render("running")) {
		t.Errorf("neither row reads as running: %q", started)
	}
	if view := m.View(); strings.Contains(view, "adopted") {
		t.Errorf("the word adopted is on the operator's screen:\n%s", view)
	}
}

func TestAdoptedRunOccupiesAndFreesItsRow(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r9", Lane: 5,
		TicketID: "abcdef1234567890", Queue: "review", LogPath: "/dev/null"}})
	rows := m.workRows()
	if len(rows) != 1 {
		t.Fatalf("panel has %d rows, want the one adopted run", len(rows))
	}
	// The row itself, not the view: the main pane titles the selected row's
	// log with the same shortened ID, so a view check would pass even with
	// the ticket column gone.
	if line := m.workRowLines(rows[0], false, 80)[0]; !strings.Contains(line, "abcdef12…") {
		t.Fatalf("adopted run not on the board: %q", line)
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventReaped, RunID: "r9", Lane: 5,
		TicketID: "abcdef1234567890", Queue: "review"}})
	if len(m.workRows()) != 0 {
		t.Fatalf("reaped adopted run still on the board: %d rows", len(m.workRows()))
	}
}

// Focus moves between the two panels — both stay on screen; the main pane
// is a lens on what the focused one selects. There is no third panel, and
// the key that used to open one is bound to nothing.
func TestFocusSwitching(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	if m.focus != panelAttention {
		t.Fatalf("lerp opens focused on %v, want inbox", m.focus)
	}
	m = update(t, m, keyMsg("2"))
	if m.focus != panelWork {
		t.Fatalf("key 2 focused %v, want work", m.focus)
	}
	if !strings.Contains(m.View(), "waiting for the first pass") {
		t.Fatalf("empty work lens missing its state text:\n%s", m.View())
	}
	m = update(t, m, keyMsg("1"))
	if m.focus != panelAttention {
		t.Fatalf("key 1 focused %v, want inbox", m.focus)
	}
	if !strings.Contains(m.View(), "reading the board") {
		t.Fatalf("inbox lens before the first pass missing its state text:\n%s", m.View())
	}
	m = update(t, m, keyMsg("3"))
	if m.focus != panelAttention {
		t.Fatalf("key 3 moved focus to %v; it is bound to nothing", m.focus)
	}
	m = update(t, m, keyMsg("2"))
	if m.focus != panelWork {
		t.Fatalf("key 2 focused %v, want work", m.focus)
	}
	m = update(t, m, keyMsg("tab"))
	if m.focus != panelAttention {
		t.Fatalf("tab from work landed on %v, want inbox", m.focus)
	}
}

// The work panel is the loop's own snapshot, verbatim: eligible tickets in
// pickup order, blocked and claimed ones visibly gated, empty queues named,
// the whole body replaced on every pass. The main pane details the selected
// ticket, including why it will not run.
func TestWorkPanelShowsWhatRunsNext(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("2"))
	m = openMain(t, m)

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "t1", Identifier: "LERP-1", Title: "ship the thing", Eligible: true,
				URL: "https://linear.app/acme/issue/LERP-1/ship"},
			{ID: "t2", Identifier: "LERP-2", Title: "gated work", BlockedBy: []string{"LERP-1", "LERP-9"}},
			{ID: "t3", Identifier: "LERP-3", Title: "already picked up", Assigned: true},
		}},
		{Team: "LERP", Name: "review", Status: "In Review"},
	}}})
	view := m.View()
	for _, want := range []string{
		"implement · Todo · LERP · 3",
		"LERP-1", "ship the thing",
		"review · In Review · LERP · empty",
		"team LERP", "position 1 of 3", // the selected ticket's detail lens
		"https://linear.app/acme/issue/LERP-1/ship",
		"to change what runs next", // ordering is not a keystroke

	} {
		if !strings.Contains(view, want) {
			t.Fatalf("work view is missing %q:\n%s", want, view)
		}
	}

	// Selection walks the pickup order; the lens explains each gate.
	m = update(t, m, keyMsg("down"))
	if view := m.View(); !strings.Contains(view, "blocked by LERP-1, LERP-9") {
		t.Fatalf("blocked ticket's lens does not name its blockers:\n%s", view)
	}
	m = update(t, m, keyMsg("down"))
	view = m.View()
	if !strings.Contains(view, "claimed") {
		t.Fatalf("claimed ticket's lens does not say so:\n%s", view)
	}
	// A claim is the one gate a keystroke lifts, so the claimed row is the one
	// row whose hint names S instead of the ordering rule (LERP-113).
	if !strings.Contains(view, "S takes over your own claim") {
		t.Fatalf("claimed ticket's lens does not offer the takeover:\n%s", view)
	}
	if strings.Contains(view, "to change what runs next") {
		t.Fatalf("claimed ticket's lens still shows the ordering hint:\n%s", view)
	}

	// The next pass's snapshot replaces the whole view: a ticket that left
	// the queue leaves the screen, and the emptied board explains how
	// tickets enter.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo"},
	}}})
	view = m.View()
	if strings.Contains(view, "LERP-1") {
		t.Fatalf("stale ticket survived a refresh:\n%s", view)
	}
	if !strings.Contains(view, `tickets enter when moved to "Todo"`) {
		t.Fatalf("emptied queue does not explain how tickets enter:\n%s", view)
	}
}

// One panel, grouped by queue, with what is running at the top of its own
// group in lane order — the same tickets the queue snapshot lists, not a
// second picture of the machine's slots.
func TestWorkPanelPutsRunningAtTheTopOfItsQueue(t *testing.T) {
	m, _, _ := newTestModel(t, 3)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = resized.(model)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Implementing", Tickets: []loop.QueueTicket{
			{ID: "id-49", Identifier: "LERP-49", Title: "the log tail", Assigned: true},
			{ID: "id-37", Identifier: "LERP-37", Title: "waits its turn", Eligible: true},
			{ID: "id-51", Identifier: "LERP-51", Title: "also running", Assigned: true},
		}},
		{Team: "LERP", Name: "review", Status: "Agent Review"},
	}}})
	// Started out of order: the rows sort by lane, which is stable, not by
	// the order the events arrived in.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r2", Lane: 2,
		TicketID: "id-51", Ticket: "LERP-51", Queue: "implement", LogPath: "/dev/null"}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-49", Ticket: "LERP-49", Queue: "implement", LogPath: "/dev/null"}})

	var got []string
	for _, r := range m.workRows() {
		got = append(got, r.ticket)
	}
	if want := []string{"LERP-49", "LERP-51", "LERP-37"}; !slices.Equal(got, want) {
		t.Fatalf("row order = %v, want %v", got, want)
	}
	if rows := m.workRows(); rows[0].lane != 1 || rows[1].lane != 2 || rows[2].lane != 0 {
		t.Fatalf("rows do not carry their lanes: %d, %d, %d", rows[0].lane, rows[1].lane, rows[2].lane)
	}
	view := m.View()
	for _, want := range []string{
		"implement · Implementing · LERP · 3",
		"review · Agent Review · LERP · empty",
		"2/3 running",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("work panel is missing %q:\n%s", want, view)
		}
	}
}

// The lens is the selected row's, not the panel's: a running ticket shows
// its live log, the ticket below it shows what gates it, and the tail keeps
// running underneath the detour.
func TestTheLensFollowsTheRowNotThePanel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	writeLog(t, path, []byte("agent at work\n"))

	m, _, _ := newTestModel(t, 2)
	m = update(t, m, keyMsg("2"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "id-1", Identifier: "LERP-1", Title: "running one", Assigned: true},
			{ID: "id-2", Identifier: "LERP-2", Title: "waiting two", Eligible: true,
				URL: "https://linear.app/acme/issue/LERP-2/two"},
		}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "implement", LogPath: path}})
	m = openMain(t, m)
	if !strings.Contains(m.View(), "agent at work") {
		t.Fatalf("the running row does not show its log:\n%s", m.View())
	}

	m = update(t, m, keyMsg("down"))
	view := m.View()
	if strings.Contains(view, "agent at work") {
		t.Fatalf("a pending row still shows the running one's log:\n%s", view)
	}
	if !strings.Contains(view, "https://linear.app/acme/issue/LERP-2/two") {
		t.Fatalf("the pending row's detail is missing:\n%s", view)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("more from the agent\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m = update(t, m, pollMsg{})

	m = update(t, m, keyMsg("up"))
	if !strings.Contains(m.View(), "more from the agent") {
		t.Fatalf("the tail did not survive the detour through a pending row:\n%s", m.View())
	}
	if !m.follow {
		t.Error("a detour through a pending row broke log follow")
	}
}

// An adopted run may sit on a lane above N. It still costs a lane of the
// budget — nothing downstream can tell it from a forced run — and it is
// never off the panel. Its queue is not on the board, so it keeps a group
// of its own, and it selects like any other row.
func TestAdoptedRunAboveCapacityIsOnThePanel(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("2"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "plan", Status: "Planning", Tickets: []loop.QueueTicket{
			{ID: "id-1", Identifier: "LERP-1", Title: "one", Eligible: true}}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r9", Lane: 4,
		TicketID: "abcdef1234567890", Queue: "review", LogPath: "/dev/null"}})
	view := m.View()
	// One live run against a budget of one is full, whatever lane number it
	// landed on: freeLanes charges every active run against N, so "0/1"
	// here would advertise a lane no pass can fill.
	for _, want := range []string{"abcdef12…", "review · off the board", "1/1 running"} {
		if !strings.Contains(view, want) {
			t.Fatalf("adopted run above capacity is missing %q:\n%s", want, view)
		}
	}
	m = update(t, m, keyMsg("down"))
	if r := m.selectedWork(); r == nil || r.ticketID != "abcdef1234567890" {
		t.Fatalf("the adopted run is not selectable: %+v", r)
	}
}

// How the last run ended has one home: the status bar. The ticket's own row
// has moved on by then — or gone — so the note goes where it outlives the
// row, and it clears at the next pass like every other transient note.
func TestExitOutcomeLandsOnTheStatusBar(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", ExitCode: 1}})
	if !strings.Contains(m.View(), "! LERP-42 exited 1") {
		t.Fatalf("a failed run's exit is not reported as one:\n%s", m.View())
	}
	m = update(t, m, tickMsg{})
	if strings.Contains(m.View(), "exited 1") {
		t.Fatalf("the outcome outlived the pass that superseded it:\n%s", m.View())
	}
}

// A backlog deeper than the terminal is capped inside its panel, not allowed
// to push the status bar off screen.
func TestWorkPanelCapsToItsPanel(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	tickets := make([]loop.QueueTicket, 60)
	for i := range tickets {
		tickets[i] = loop.QueueTicket{ID: fmt.Sprintf("t%d", i),
			Identifier: fmt.Sprintf("LERP-%d", i), Title: "work", Eligible: true}
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: tickets},
	}}})
	view := m.View()
	if !strings.Contains(view, "more") {
		t.Fatalf("overflowing work panel shows no cap line:\n%s", view)
	}
	if got := strings.Count(view, "\n"); got > 30 {
		t.Fatalf("view is %d lines tall in a 30-line window", got)
	}
	if !strings.Contains(view, "q quit") {
		t.Fatalf("cap pushed the status bar off screen:\n%s", view)
	}
}

// The inbox panel folds attention events; its lens shows the loop's
// reason and Linear's URL for the selected item. The empty state is the goal
// state — but never claimed before the first pass has reported, and it says
// what would make items appear.
func TestInboxListsWhatWaits(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))

	if view := m.View(); strings.Contains(view, "the inbox is empty") {
		t.Fatalf("view claims the goal state before any pass reported:\n%s", view)
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-42", Title: "Fix the flaky test", Status: "Needs Help",
			Reason: `claimed in "Needs Help" — no queue serves it`,
			URL:    "https://linear.app/acme/issue/LERP-42/fix"},
	}}})
	view := m.View()
	for _, want := range []string{
		"LERP-42",
		"Fix the flaky test",
		"https://linear.app/acme/issue/LERP-42/fix",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("inbox view is missing %q:\n%s", want, view)
		}
	}
	// The reason is one column too long for this pane, so it wraps under its
	// label rather than being truncated: both halves have to be on screen,
	// and the tail — the part that says what is holding the ticket up — has
	// to line up under the gutter rather than at the margin.
	if !strings.Contains(view, `why     claimed in "Needs Help" — no queue serves`) {
		t.Fatalf("the reason does not start under its label:\n%s", view)
	}
	if !strings.Contains(view, labelGutter+"it") {
		t.Fatalf("the reason's tail was cut, or is not under the gutter:\n%s", view)
	}

	// A later pass with nothing waiting clears the list and says so.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	view = m.View()
	if !strings.Contains(view, "the inbox is empty") {
		t.Fatalf("empty inbox list does not read as the goal state:\n%s", view)
	}
	if !strings.Contains(view, "shows unclaimed tickets") {
		t.Fatalf("empty inbox lens does not explain what would make items appear:\n%s", view)
	}
	if strings.Contains(view, "LERP-42") {
		t.Fatalf("cleared item still rendered:\n%s", view)
	}
}

// board is an inbox list with everything the table sorts, groups, marks
// and filters by: two projects and one ticket in none, three statuses, and
// a chain that gives the top ticket its leverage.
func board() loop.Event {
	return loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", TicketID: "id-1", Title: "Fix the build", Status: "Needs Attention",
			Project: "Open-source readiness", Relevance: loop.StatusFailed, Priority: 3,
			Claimed: true, Reason: `claimed in "Needs Attention" — a run failed here`},
		{Ticket: "LERP-22", TicketID: "id-22", Title: "GoReleaser: tagged releases", Status: "Backlog",
			Project: "Open-source readiness", Relevance: loop.StatusBacklog, Priority: 2,
			Unblocks: 2, Blocks: []string{"LERP-23"}},
		{Ticket: "LERP-23", TicketID: "id-23", Title: "curl install", Status: "Backlog",
			Project: "Open-source readiness", Relevance: loop.StatusBacklog, Priority: 3,
			Unblocks: 1, BlockedBy: []string{"LERP-22"}},
		{Ticket: "LERP-48", TicketID: "id-48", Title: "Read the ticket in the TUI", Status: "In Review",
			Project: "TUI redesign", Relevance: loop.StatusFinished, Priority: 1},
		// The one ticket the pipeline lost: a status no queue serves, no
		// on_success or on_failure points at, and Linear does not file as
		// waiting either — so something moved it out from under lerp. Most
		// of a board looks like the Backlog rows above instead.
		{Ticket: "LERP-60", TicketID: "id-60", Title: "Unfiled work", Status: "In Progress",
			Relevance: loop.StatusUnnamed, Priority: 4},
		// Priority 0 is Linear's "No priority", not its highest: it must sort
		// below Low, never above Urgent. This is the only fixture carrying it,
		// and the order assertions below are what guard priorityRank.
		{Ticket: "LERP-70", TicketID: "id-70", Title: "Unprioritized work", Status: "Backlog",
			Relevance: loop.StatusBacklog, Priority: 0},
	}}
}

// browseBacklog presses B on the inbox panel. The fixture is half backlog
// rows and the panel opens folded, so every test below that is about the
// whole of it — the sort order, the grouping, the columns, the filters —
// asks for the rest of the list the way an operator does. The tests about
// what the panel says on sight do not call it.
func browseBacklog(t *testing.T, m model) model {
	t.Helper()
	m = update(t, m, keyMsg("B"))
	if !m.backlogOpen {
		t.Fatal("B did not expand the backlog")
	}
	return m
}

// rowOf returns the panel line carrying the ticket, for the assertions that
// are about one row rather than about the order of several.
func rowOf(t *testing.T, panel, ticket string) string {
	t.Helper()
	for _, line := range strings.Split(panel, "\n") {
		if strings.Contains(line, ticket+" ") {
			return line
		}
	}
	t.Fatalf("no row for %s:\n%s", ticket, panel)
	return ""
}

// order returns the tickets in the order the panel renders them.
func order(panel string, tickets ...string) []string {
	var got []string
	for _, line := range strings.Split(panel, "\n") {
		for _, ticket := range tickets {
			if strings.Contains(line, ticket+" ") {
				got = append(got, ticket)
			}
		}
	}
	return got
}

// Done-when: no row and no header anywhere says "to route" or "parked on
// you". The status column carries Linear's own name for where the ticket
// rests, and a status the configured pipeline never names is marked as one
// — the fingerprint of a ticket that left the pipeline, readable without
// selecting the row.
func TestInboxRowsCarryTheRealStatus(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})
	m = browseBacklog(t, m)

	panel := m.attentionPanel(96, 14)
	view := m.View()
	for _, gone := range []string{"to route", "parked on you"} {
		if strings.Contains(view, gone) {
			t.Fatalf("the view still says %q:\n%s", gone, view)
		}
	}
	for _, want := range []string{"Needs Attention", "Backlog", "In Review"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("inbox panel is missing the status %q:\n%s", want, panel)
		}
	}
	// The mark is for a ticket the pipeline lost, and only that: LERP-60
	// rests in a status no queue serves, that nothing points at, and that
	// Linear does not file as waiting either. The Backlog rows are the
	// ordinary state of most of a board — a mark on those would be texture,
	// not a warning — and the statuses the pipeline does name are not
	// marked at all.
	if !strings.Contains(rowOf(t, panel, "LERP-60"), "⚠") {
		t.Fatalf("the one ticket that left the pipeline is unmarked:\n%s", panel)
	}
	for _, unmarked := range []string{"LERP-1", "LERP-48", "LERP-22", "LERP-23", "LERP-70"} {
		if strings.Contains(rowOf(t, panel, unmarked), "⚠") {
			t.Fatalf("%s did not leave the pipeline, but the row marks it:\n%s", unmarked, panel)
		}
	}
	// Every column on one line, no selection required.
	row := rowOf(t, panel, "LERP-1")
	for _, want := range []string{"LERP-1", "↓0", "Fix the build", "Needs Attention", "Open-source readiness", "Medium"} {
		if !strings.Contains(row, want) {
			t.Fatalf("the LERP-1 row is missing %q:\n%s", want, row)
		}
	}
	if got := rowOf(t, panel, "LERP-60"); !strings.Contains(got, "—") {
		t.Fatalf("a ticket in no project does not read as one:\n%s", got)
	}
}

// Done-when: the inbox table says what its columns are. Six columns and no
// header row is a table the reader has to decode from the values in it —
// and two of the six carry marks that decode to nothing at all. One faint
// line names every column, stands over the column it names, and stays put
// when the list under it scrolls.
func TestInboxTableNamesItsColumns(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})

	// Display columns, not byte offsets: the cells carry ↓ and — , which
	// are wider in bytes than on screen.
	col := func(line, s string) int {
		t.Helper()
		i := strings.Index(line, s)
		if i < 0 {
			t.Fatalf("%q is not on the line:\n%s", s, line)
		}
		return lipgloss.Width(line[:i])
	}

	panel := m.attentionPanel(120, 14)
	// The header is the panel's first body line, above every row.
	header := ansi.Strip(strings.Split(panel, "\n")[1])
	row := ansi.Strip(rowOf(t, panel, "LERP-1"))
	for _, c := range []struct{ name, cell string }{
		{hdrTicket, "LERP-1"}, {hdrLeverage, "↓0"}, {hdrStatus, "Needs Attention"},
		{hdrProject, "Open-source readiness"}, {hdrPriority, "Medium"}, {hdrTitle, "Fix the build"},
	} {
		if h, r := col(header, c.name), col(row, c.cell); h != r {
			t.Fatalf("%q is at column %d and the %q it names at %d:\n%s\n%s",
				c.name, h, c.cell, r, header, row)
		}
	}

	// Pinned, not listed: a panel too short for its rows windows them, and
	// a header windowed with them would come and go — worse than none,
	// because then its absence has to be read too.
	for i := 0; i < len(m.shown)-1; i++ {
		m = update(t, m, keyMsg("j"))
	}
	scrolled := m.attentionPanel(120, 6)
	if !strings.Contains(scrolled, "⋯") {
		t.Fatalf("the short panel did not window its rows, so nothing scrolled:\n%s", scrolled)
	}
	if got := ansi.Strip(strings.Split(scrolled, "\n")[1]); !strings.Contains(got, hdrTicket) {
		t.Fatalf("the header scrolled away with the rows:\n%s", scrolled)
	}
	// The header belongs to the table and not to the cursor: with one row
	// in the inbox and a full work panel, the line the panel spends on it
	// must not depend on which panel has focus. A header that appeared on
	// tab would be exactly the header that comes and goes.
	one, _, _ := newTestModel(t, 1)
	resized, _ := one.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	one = update(t, resized.(model), keyMsg("1"))
	one = update(t, one, eventMsg{ev: loop.Event{Type: loop.EventAttention,
		Attention: []loop.AttentionItem{{Ticket: "LERP-1", Title: "waiting", Reason: "unclaimed"}}}})
	tickets := make([]loop.QueueTicket, 40)
	for i := range tickets {
		tickets[i] = loop.QueueTicket{ID: fmt.Sprintf("t%d", i),
			Identifier: fmt.Sprintf("QUEUED-%d", i+1), Title: "work", Eligible: true}
	}
	one = update(t, one, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: tickets},
	}}})
	for _, focus := range []string{"2", "1"} {
		one = update(t, one, keyMsg(focus))
		g := one.geometry()
		panel := one.attentionPanel(g.sideW, g.attnH)
		if !strings.Contains(strings.Split(ansi.Strip(panel), "\n")[1], hdrTicket) {
			t.Fatalf("with focus on panel %s the inbox lost its column header:\n%s", focus, panel)
		}
	}

	// And no header over an empty state, which is a sentence and not a table.
	empty, _, _ := newTestModel(t, 1)
	empty = update(t, empty, keyMsg("1"))
	empty = update(t, empty, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	if got := empty.attentionPanel(120, 8); strings.Contains(got, hdrPriority) {
		t.Fatalf("an empty inbox drew column headers over no columns:\n%s", got)
	}
}

// Done-when: the ? overlay decodes every mark the inbox draws inside a
// column. A header can name a column; only a sentence can say what a glyph
// standing in one means, and the overlay is the one place with the room.
func TestHelpOverlayDecodesTheInboxMarks(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = update(t, resized.(model), keyMsg("?"))

	view := ansi.Strip(m.View())
	for _, want := range []struct{ glyph, says string }{
		{"↓n", "frees"}, {"⊘", "blocks"}, {"⚠", "never named"},
	} {
		// A row of the table can carry the same glyph, so it is the line
		// that also carries the sentence that has to exist.
		shown, decoded := false, false
		for _, l := range strings.Split(view, "\n") {
			if !strings.Contains(l, want.glyph) {
				continue
			}
			shown = true
			decoded = decoded || strings.Contains(l, want.says)
		}
		if !shown {
			t.Fatalf("the help overlay does not show %q at all:\n%s", want.glyph, view)
		}
		if !decoded {
			t.Fatalf("no line says %q beside %s, so the mark is still undecoded:\n%s",
				want.says, want.glyph, view)
		}
	}

	// A terminal too short to hold the overlay whole has to be able to
	// scroll to the rest of it: the legend sits under the key table, so a
	// pane that only ever showed its first screen would be a pane the
	// legend is not in.
	short, _, _ := newTestModel(t, 1)
	small, _ := short.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	short = update(t, small.(model), keyMsg("?"))
	reached := false
	for i := 0; i < 8 && !reached; i++ {
		reached = strings.Contains(ansi.Strip(short.View()), "never named")
		short = update(t, short, keyMsg("f"))
	}
	if !reached {
		t.Fatalf("the legend is out of reach on an 80x24 terminal:\n%s", short.View())
	}
}

// Done-when: the ? overlay is a lens like the others, so a live log behind
// it neither writes over it nor is disturbed by it. One viewport serves the
// log, the detail and the overlay; the overlay is the one the operator
// reads while an agent is running, which is exactly when the tail is busy.
func TestTheHelpOverlayIsNotWrittenOverByALiveLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "one.log")
	var body []byte
	for i := 0; i < 200; i++ {
		body = append(body, []byte(fmt.Sprintf("line %d\n", i))...)
	}
	writeLog(t, logPath, body)

	m, _, _ := newTestModel(t, 1)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = resized.(model)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "t1", Identifier: "LERP-1", Title: "running", Assigned: true},
			{ID: "t2", Identifier: "LERP-2", Title: "waiting", Eligible: true},
		}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "t1", Ticket: "LERP-1", Queue: "implement", LogPath: logPath}})
	m = openMain(t, update(t, m, keyMsg("2")))
	if !strings.Contains(m.View(), "line 199") {
		t.Fatalf("the log lens is not up, so this test is not exercising the case:\n%s", m.View())
	}

	// The agent writes while the overlay is up: the poll reads the tail, and
	// what it reads must not land in the pane the operator is reading.
	m = update(t, m, keyMsg("?"))
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("brand new agent line\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m = update(t, m, pollMsg{})
	if view := m.View(); strings.Contains(view, "brand new agent line") {
		t.Fatalf("a live log wrote over the help overlay:\n%s", view)
	}
	// So must an event that re-aims the tail, and the raw toggle.
	m = update(t, m, keyMsg("r"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "t1", Ticket: "LERP-1", Queue: "implement"}})
	if view := m.View(); !strings.Contains(view, "inbox marks") {
		t.Fatalf("the overlay lost the pane to the log behind it:\n%s", view)
	}

	// Closing it puts the operator back on a following tail, with what the
	// agent wrote while they were reading the help.
	m = update(t, m, keyMsg("?"))
	if view := m.View(); !strings.Contains(view, "brand new agent line") {
		t.Fatalf("the log did not come back caught up:\n%s", view)
	}
	if !m.follow {
		t.Error("reading the help froze the tail")
	}
}

// Done-when: reading the help costs the operator nothing in the pane it is
// drawn over — the ticket lens as much as the log. A parked ticket's plan is
// the one thing that reliably overflows the pane, and it is decided from
// that one screen, so being dropped back at the top of it is a real loss.
func TestTheHelpOverlayGivesTheTicketPaneBackWhereItWas(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = update(t, resized.(model), keyMsg("1"))
	m = update(t, m, keyMsg("enter")) // the inbox reads a ticket once its pane is open
	m = update(t, m, eventMsg{ev: threeWaiting()})
	body := strings.Repeat("a line of the plan\n", 80)
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: body}, nil, reader)

	m = update(t, m, keyMsg("f"))
	m = update(t, m, keyMsg("f"))
	want := m.vp.YOffset
	if want == 0 {
		t.Fatal("paging into the ticket did not move the viewport")
	}

	m = update(t, m, keyMsg("?"))
	m = update(t, m, keyMsg("?"))
	if got := m.vp.YOffset; got != want {
		t.Errorf("offset after reading the help = %d, want %d — the plan came back at the top", got, want)
	}

	// Unless the operator re-aimed the pane while the overlay was up: what
	// comes back is then a different ticket, and a scroll position measured
	// against the last one would be meaningless.
	m = update(t, m, keyMsg("f"))
	m = update(t, m, keyMsg("?"))
	m = update(t, m, keyMsg("j"))
	m = update(t, m, keyMsg("?"))
	if got := m.vp.YOffset; got != 0 {
		t.Errorf("a different ticket came back scrolled to %d, want the top", got)
	}
}

// Done-when: opening a long ticket shows at a glance how long it is and
// where the operator is in it, a document shorter than the pane shows no
// lying thumb, and scrolling moves the indicator rather than leaving it
// standing still. TestScrollThumb* in theme_test.go pins the math this test
// exercises through the model — the two are complementary, not redundant:
// this is what catches the pane wiring the wrong height or offset into it.
func TestMainPaneScrollbarTracksThePosition(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = update(t, resized.(model), keyMsg("1"))
	m = update(t, m, keyMsg("enter")) // the inbox reads a ticket once its pane is open
	m = update(t, m, eventMsg{ev: threeWaiting()})

	// mainView isolates the pane's own rendering — scrollThumbGlyph is also
	// the sparkline's busiest bar (pulse.go), drawn into the work panel's
	// rows, so a check against the whole screen would pass today only
	// because no run happens to be active and could start failing for a
	// reason that has nothing to do with this pane.
	mainView := func(m model) string {
		g := m.geometry()
		return m.mainPanel(g.mainW, g.mainH)
	}

	// A ticket short enough to fit the pane outright draws no thumb: a bar
	// that always reads full would say "there is more" about a document that
	// has none.
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "a short ticket"}, nil, reader)
	g := m.geometry()
	if sb := m.mainScrollbar(g.mainH); sb != nil {
		t.Fatalf("a ticket shorter than the pane drew a thumb: %+v", *sb)
	}
	if strings.Contains(mainView(m), scrollThumbGlyph) {
		t.Fatalf("a ticket shorter than the pane drew a thumb:\n%s", mainView(m))
	}

	// A ticket far longer than the pane draws one, at the top to start. A
	// different row, since the selection has already settled on the first —
	// settling again on the same one schedules no second fetch.
	body := strings.Repeat("a line of the plan\n", 80)
	m = selectAndRead(t, m, 1, linear.IssueDetail{Body: body}, nil, reader)
	g = m.geometry()
	top := m.mainScrollbar(g.mainH)
	if top == nil {
		t.Fatalf("a ticket far longer than the pane drew no thumb:\n%s", mainView(m))
	}
	if top.top != 0 {
		t.Errorf("a freshly opened ticket's thumb starts at row %d, want 0", top.top)
	}
	if !strings.Contains(mainView(m), scrollThumbGlyph) {
		t.Fatalf("the rendered pane carries no thumb glyph despite one at %+v:\n%s", *top, mainView(m))
	}

	// tab moves the keys into the pane, so j moves it a line at a time
	// (scrollMain) instead of the list's selection — a page-at-a-time key
	// here would jump straight to the bottom in one press on a pane this
	// size and never show an intermediate position at all.
	m = update(t, m, keyMsg("tab"))
	if !m.mainFocused() {
		t.Fatalf("tab did not move the keys into the pane")
	}
	for i := 0; i < 5; i++ {
		m = update(t, m, keyMsg("j"))
	}
	g = m.geometry()
	moved := m.mainScrollbar(g.mainH)
	if moved == nil {
		t.Fatalf("the thumb disappeared after scrolling down:\n%s", mainView(m))
	}
	if moved.top <= top.top {
		t.Errorf("after scrolling down, thumb top = %d, want it past %d", moved.top, top.top)
	}
	if last := moved.top + moved.len; last >= g.mainH-2 {
		t.Errorf("five lines down already reached the bottom (rows [%d,%d) of %d) — want an intermediate position",
			moved.top, last, g.mainH-2)
	}

	// And it reaches the very last row once the pane is following the tail
	// end of the ticket, the same edge scrollThumb's own tests pin.
	m = update(t, m, keyMsg("G"))
	g = m.geometry()
	bottom := m.mainScrollbar(g.mainH)
	if bottom == nil {
		t.Fatalf("the thumb disappeared at the bottom:\n%s", mainView(m))
	}
	if last := bottom.top + bottom.len; last != g.mainH-2 {
		t.Errorf("at the bottom, thumb covers rows [%d,%d), want it flush with the last row %d",
			bottom.top, last, g.mainH-2)
	}
}

// Done-when: scrolling the overlay, and moving the cursor behind it, are
// not the log's business. Follow is the log's state alone — the rule the
// scroll keys already hold to for the detail lens — and the place the
// operator had scrolled a log back to survives a detour through the help.
func TestReadingTheHelpDoesNotDisturbTheLogBehindIt(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "one.log")
	var body []byte
	for i := 0; i < 200; i++ {
		body = append(body, []byte(fmt.Sprintf("line %d\n", i))...)
	}
	writeLog(t, logPath, body)

	m, _, _ := newTestModel(t, 1)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = resized.(model)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "t1", Identifier: "LERP-1", Title: "running", Assigned: true},
			{ID: "t2", Identifier: "LERP-2", Title: "waiting", Eligible: true},
		}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "t1", Ticket: "LERP-1", Queue: "implement", LogPath: logPath}})
	m = openMain(t, update(t, m, keyMsg("2")))
	m = update(t, m, keyMsg("pgup"))
	m = update(t, m, keyMsg("pgup"))
	if m.follow {
		t.Fatal("scrolling up left follow on, so this test is not exercising the case")
	}
	want := m.vp.YOffset
	if want == 0 {
		t.Fatal("scrolling up did not move the viewport")
	}

	// Read the help: page down to the legend, step the cursor about behind
	// it, and page back. None of it is the log talking.
	m = update(t, m, keyMsg("?"))
	m = update(t, m, keyMsg("f"))
	scrolled := m.vp.YOffset
	m = update(t, m, keyMsg("down"))
	m = update(t, m, keyMsg("up"))
	if got := m.vp.YOffset; got != scrolled {
		t.Errorf("moving the cursor behind the overlay scrolled it to %d, want %d", got, scrolled)
	}
	m = update(t, m, keyMsg("b"))
	m = update(t, m, keyMsg("G"))

	m = update(t, m, keyMsg("?"))
	if m.follow {
		t.Error("scrolling the help overlay turned log follow on")
	}
	if got := m.vp.YOffset; got != want {
		t.Errorf("offset after reading the help = %d, want %d — the operator lost their place", got, want)
	}
}

// Done-when: four sort modes cycle on one key, the two grouped ones draw
// headers and the two flat ones do not, and the panel title says which is
// in force. Sorting is the only grouping control there is.
func TestInboxSortModesCycle(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})
	m = browseBacklog(t, m)

	// Status is the default: pipeline-relevance first — a failure route,
	// then where a clean run comes to rest, then the statuses the pipeline
	// never named the ticket into, and last the intake it never left — with
	// a header per status carrying the note that explains the rank, and
	// leverage ordering the rows inside a group.
	panel := m.attentionPanel(96, 18)
	want := []string{"LERP-1", "LERP-48", "LERP-60", "LERP-22", "LERP-70", "LERP-23"}
	if got := order(panel, want...); !slices.Equal(got, want) {
		t.Fatalf("status order = %v, want %v:\n%s", got, want, panel)
	}
	if !strings.Contains(panel, "by status") {
		t.Fatalf("the panel title does not name the sort mode:\n%s", panel)
	}
	for _, note := range []string{"a run failed here", "a run finished here",
		"the pipeline never names it", "waiting to enter the pipeline"} {
		if !strings.Contains(panel, note) {
			t.Fatalf("status headers do not carry %q:\n%s", note, panel)
		}
	}

	// Project, alphabetically, with the unfiled ticket last.
	m = update(t, m, keyMsg("s"))
	panel = m.attentionPanel(96, 16)
	want = []string{"LERP-22", "LERP-1", "LERP-23", "LERP-48", "LERP-60", "LERP-70"}
	if got := order(panel, want...); !slices.Equal(got, want) {
		t.Fatalf("project order = %v, want %v:\n%s", got, want, panel)
	}
	if !strings.Contains(panel, "TUI redesign") || !strings.Contains(panel, "no project") {
		t.Fatalf("project mode draws no project headers:\n%s", panel)
	}

	// Leverage: what promoting frees first, then priority, then the
	// identifier — and a blocked ticket below every routable one. Flat, so
	// no headers.
	m = update(t, m, keyMsg("s"))
	panel = m.attentionPanel(96, 14)
	want = []string{"LERP-22", "LERP-48", "LERP-1", "LERP-60", "LERP-70", "LERP-23"}
	if got := order(panel, want...); !slices.Equal(got, want) {
		t.Fatalf("leverage order = %v, want %v:\n%s", got, want, panel)
	}
	if !strings.Contains(panel, "by leverage") {
		t.Fatalf("the panel title does not name the sort mode:\n%s", panel)
	}
	if strings.Contains(panel, "a run failed here") || strings.Contains(panel, "no project") {
		t.Fatalf("a flat mode still draws headers:\n%s", panel)
	}

	// Priority, then leverage.
	m = update(t, m, keyMsg("s"))
	panel = m.attentionPanel(96, 14)
	// Priority is the primary key here, so the blocked LERP-23 outranks the
	// routable LERP-60 that leverage put above it.
	want = []string{"LERP-48", "LERP-22", "LERP-1", "LERP-23", "LERP-60", "LERP-70"}
	if got := order(panel, want...); !slices.Equal(got, want) {
		t.Fatalf("priority order = %v, want %v:\n%s", got, want, panel)
	}
	if !strings.Contains(panel, "by priority") {
		t.Fatalf("the panel title does not name the sort mode:\n%s", panel)
	}
	if strings.Contains(panel, "a run failed here") || strings.Contains(panel, "no project") {
		t.Fatalf("a flat mode still draws headers:\n%s", panel)
	}

	// One more press is back to the grouped default, headers and all.
	m = update(t, m, keyMsg("s"))
	panel = m.attentionPanel(96, 16)
	if !strings.Contains(panel, "by status") {
		t.Fatalf("the sort key does not cycle back to the default:\n%s", panel)
	}
	if !strings.Contains(panel, "a run failed here") {
		t.Fatalf("the grouped default draws no headers:\n%s", panel)
	}
}

// Done-when: a grouped mode with a single group draws no group header — the
// line says nothing the rows do not, and a squeezed panel spends it on the
// key hint instead of on a header over one row. The column header is pinned
// and stays: it names what the row carries, which one row does not.
func TestSingleGroupDrawsNoHeader(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", TicketID: "id-1", Title: "Fix the build", Status: "Needs Attention",
			Relevance: loop.StatusFailed, Priority: 3,
			Reason: `claimed in "Needs Attention" — a run failed here`},
	}}})

	panel := m.attentionPanel(60, 5) // the column header, one row, the hint
	if strings.Contains(panel, "a run failed here") {
		t.Fatalf("one group still drew a header:\n%s", panel)
	}
	if !strings.Contains(panel, "s sort") {
		t.Fatalf("a header displaced the key hint on a squeezed panel:\n%s", panel)
	}
	if !strings.Contains(rowOf(t, panel, "LERP-1"), "Needs Attention") {
		t.Fatalf("the row lost the status the header would have carried:\n%s", panel)
	}

	// A second status is a second group, so the headers come back.
	m = update(t, m, eventMsg{ev: board()})
	if !strings.Contains(m.attentionPanel(96, 16), "a run failed here") {
		t.Fatalf("more than one group draws no headers:\n%s", m.attentionPanel(96, 16))
	}
}

// Done-when: the sort key moves the rows, not the cursor. The selection is
// a ticket, so re-sorting keeps the operator on the one they were reading.
func TestSortKeepsTheSelectedTicket(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})
	m = update(t, m, keyMsg("j")) // LERP-48, second under the status default

	if got := m.selectedAttention().Ticket; got != "LERP-48" {
		t.Fatalf("selection = %s, want LERP-48", got)
	}
	m = update(t, m, keyMsg("s"))
	if got := m.selectedAttention().Ticket; got != "LERP-48" {
		t.Fatalf("selection after sorting = %s, want the same LERP-48", got)
	}
}

// Done-when: one key scopes the panel to a single project and cycles back
// to all, and a pass that no longer has the scoped project resets the
// filter rather than leaving the panel hidden behind a stale choice.
func TestInboxProjectFilter(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})
	m = browseBacklog(t, m)

	// Projects cycle in name order: OSS readiness, then TUI redesign, then
	// back to every project. A ticket in no project is not a stop.
	m = update(t, m, keyMsg("P"))
	panel := m.attentionPanel(96, 14)
	if m.project != "Open-source readiness" {
		t.Fatalf("filter = %q, want the first project by name", m.project)
	}
	if strings.Contains(panel, "LERP-48") || strings.Contains(panel, "LERP-60") {
		t.Fatalf("the filter kept tickets from other projects:\n%s", panel)
	}
	if !strings.Contains(panel, "LERP-22") {
		t.Fatalf("the filter dropped a ticket in the scoped project:\n%s", panel)
	}
	if !strings.Contains(panel, "3/6") || !strings.Contains(panel, "Open-source readiness") {
		t.Fatalf("the panel title does not say what it is scoped to:\n%s", panel)
	}

	m = update(t, m, keyMsg("P"))
	if m.project != "TUI redesign" {
		t.Fatalf("filter = %q, want the next project by name", m.project)
	}
	if got := len(m.shown); got != 1 {
		t.Fatalf("the TUI redesign filter shows %d rows, want 1", got)
	}

	m = update(t, m, keyMsg("P"))
	if m.project != "" || len(m.shown) != 6 {
		t.Fatalf("the filter did not cycle back to every project: %q, %d rows", m.project, len(m.shown))
	}

	// A pass without the scoped project resets the filter; the panel is
	// never hidden behind a name nothing waits in.
	m = update(t, m, keyMsg("P"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-9", TicketID: "id-9", Title: "Something else", Status: "Backlog",
			Project: "Another project", Relevance: loop.StatusUnnamed},
	}}})
	if m.project != "" {
		t.Fatalf("filter = %q after its project left the list, want every project", m.project)
	}
	if !strings.Contains(m.attentionPanel(96, 14), "LERP-9") {
		t.Fatalf("a stale filter hid the whole panel:\n%s", m.attentionPanel(96, 14))
	}
}

// Done-when: the inbox opens on the tickets a human is blocked on — where a
// run failed, where one finished, and the status the pipeline never named —
// and the backlog is one line saying how many are behind it and which key
// opens them.
func TestInboxOpensOnWhatIsBlockedOnYou(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})

	panel := m.attentionPanel(96, 18)
	want := []string{"LERP-1", "LERP-48", "LERP-60"}
	if got := shownTickets(m); !slices.Equal(got, want) {
		t.Fatalf("the inbox opens on %v, want the three blocked on a human", got)
	}
	for _, folded := range []string{"LERP-22", "LERP-23", "LERP-70"} {
		if strings.Contains(panel, folded+" ") {
			t.Fatalf("%s has not entered the pipeline, but the panel opens on it:\n%s", folded, panel)
		}
	}
	// The count, the reason and the key, in the backlog tier's own words.
	if !strings.Contains(panel, "3 waiting to enter the pipeline — B to browse") {
		t.Fatalf("the fold does not say what it is holding back:\n%s", panel)
	}
	// A long enough blocked-on-you list windows the summary away behind
	// "⋯ n more", so the key has to be somewhere that never scrolls.
	if got := ansi.Strip(update(t, m, keyMsg("?")).View()); !strings.Contains(got, "browse the backlog") {
		t.Fatalf("the ? overlay does not carry the fold's key:\n%s", got)
	}
}

// Done-when: B expands the fold in place — the same panel, the same table,
// the same pinned header, the backlog rows under their own status group
// header — and B again puts it back. Not a tab and not a second view.
func TestBacklogExpandsInPlace(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})

	m = browseBacklog(t, m)
	panel := m.attentionPanel(96, 18)
	want := []string{"LERP-1", "LERP-48", "LERP-60", "LERP-22", "LERP-70", "LERP-23"}
	if got := order(panel, want...); !slices.Equal(got, want) {
		t.Fatalf("expanded order = %v, want the whole list in the sort's own order:\n%s", got, panel)
	}
	// Same table: the column header is still pinned above it, and the rows
	// arrive under the status group header the sort would give them anyway.
	for _, keep := range []string{hdrTicket, hdrStatus, "Backlog — waiting to enter the pipeline"} {
		if !strings.Contains(panel, keep) {
			t.Fatalf("the expanded panel is missing %q:\n%s", keep, panel)
		}
	}
	// Nothing left folded is nothing left to say.
	if strings.Contains(panel, "to browse") {
		t.Fatalf("the summary line survived the expansion:\n%s", panel)
	}
	// The way back is in the title, beside the other controls a key changed.
	if !strings.Contains(panel, "· backlog") {
		t.Fatalf("the title does not say the backlog is expanded:\n%s", panel)
	}

	m = update(t, m, keyMsg("B"))
	if m.backlogOpen {
		t.Fatal("B a second time did not fold the backlog back")
	}
	panel = m.attentionPanel(96, 18)
	if got := shownTickets(m); len(got) != 3 {
		t.Fatalf("folding back left %v, want the three blocked on a human", got)
	}
	if strings.Contains(panel, "· backlog") {
		t.Fatalf("the folded title still says the backlog is expanded:\n%s", panel)
	}
}

// Done-when: "● n in the inbox" counts only what is blocked on a human, so
// it is the same number folded or not — that is what makes it mean
// "something to look up at". The panel's own count is the other question,
// what is in this panel, and follows the fold.
func TestTheInboxCountIsWhatIsBlockedOnYou(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})

	if got := m.View(); !strings.Contains(got, "● 3 in the inbox") {
		t.Fatalf("the status bar counts the backlog into the inbox:\n%s", got)
	}
	if panel := m.attentionPanel(96, 18); !strings.Contains(panel, "● 3") {
		t.Fatalf("the folded panel title does not count its rows:\n%s", panel)
	}

	m = browseBacklog(t, m)
	if got := m.View(); !strings.Contains(got, "● 3 in the inbox") {
		t.Fatalf("expanding the backlog moved the status bar's count:\n%s", got)
	}
	if panel := m.attentionPanel(96, 18); !strings.Contains(panel, "● 6") {
		t.Fatalf("the expanded panel title does not count what it shows:\n%s", panel)
	}
}

// Done-when: an expanded backlog row is a row like any other — enter reads
// it and p promotes it. The fold is display over the list the pass already
// fetched, not a second class of row.
func TestExpandedBacklogRowsTakeTheKeys(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, []string{"Planning", "Implementing"})
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})
	m = browseBacklog(t, m)

	// Down to LERP-22, the first backlog row under the status default.
	for range 3 {
		m = update(t, m, keyMsg("j"))
	}
	if got := m.selectedAttention().Ticket; got != "LERP-22" {
		t.Fatalf("the cursor reached %s, want the first expanded backlog row", got)
	}
	m = openMain(t, m)
	if got := m.View(); !strings.Contains(got, "GoReleaser: tagged releases") {
		t.Fatalf("enter on an expanded backlog row opened nothing:\n%s", got)
	}

	m = update(t, m, keyMsg("p"))
	if !m.promoting {
		t.Fatal("p did not open the promote picker on an expanded backlog row")
	}
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(model)
	if cmd == nil {
		t.Fatal("enter produced no promote command")
	}
	cmd()
	if got := promoter.last(); got.ticketID != "id-22" || got.status != "Planning" {
		t.Fatalf("Promote call = %+v, want {id-22 Planning}", got)
	}
}

// Done-when: P stops only at projects with a row the fold lets through.
// Cycling to one whose every ticket is folded away would scope the panel to
// "nothing in X" — a filter that hides the whole panel is the thing the
// project cycle already refuses to do.
func TestProjectFilterSkipsAProjectThatIsAllBacklog(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: allBacklogProject()})

	if got := m.projects(); !slices.Equal(got, []string{"Shipping"}) {
		t.Fatalf("the project cycle offers %v, want only the project with a visible row", got)
	}
	m = update(t, m, keyMsg("P"))
	if m.project != "Shipping" {
		t.Fatalf("P scoped to %q, want the one project on screen", m.project)
	}
	m = update(t, m, keyMsg("P"))
	if m.project != "" {
		t.Fatalf("P scoped to %q, want back to every project", m.project)
	}

	// Expanding puts the project back on the cycle: the rows are on screen,
	// so scoping to them shows something.
	m = browseBacklog(t, m)
	if got := m.projects(); !slices.Equal(got, []string{"Later", "Shipping"}) {
		t.Fatalf("the expanded project cycle offers %v, want both projects", got)
	}

	// Scope to the backlog-only project, then fold it away underneath: the
	// scope is a choice the operator made and the fold is not a reason to
	// take it back, but P has to leave it in one press. The panel is showing
	// nothing and the only text on it is the hint that says so.
	m = update(t, m, keyMsg("P"))
	if m.project != "Later" {
		t.Fatalf("P scoped to %q, want the backlog-only project", m.project)
	}
	m = update(t, m, keyMsg("B"))
	if m.project != "Later" || len(m.shown) != 0 {
		t.Fatalf("folding changed the scope: %q, %d rows", m.project, len(m.shown))
	}
	// Both keys: either one can be why the panel is empty, and the note
	// above the hint does not say which.
	for _, want := range []string{"P cycles the project filter back to all", "B browses the backlog"} {
		if got := m.emptyHint(); !strings.Contains(got, want) {
			t.Fatalf("the empty panel's hint = %q, want it to name %q", got, want)
		}
	}
	m = update(t, m, keyMsg("P"))
	if m.project != "" {
		t.Fatalf("P from a folded-away project went to %q, want every project", m.project)
	}

	// And a pass does not take the scope away on its own: the project did
	// not stop existing, it went behind the fold.
	m = update(t, m, keyMsg("B")) // expanded, so Later is on the cycle again
	m = update(t, m, keyMsg("P"))
	if m.project != "Later" {
		t.Fatalf("P scoped to %q, want the backlog-only project", m.project)
	}
	m = update(t, m, keyMsg("B")) // and now folded away under the scope
	m = update(t, m, eventMsg{ev: allBacklogProject()})
	if m.project != "Later" {
		t.Fatalf("a pass cleared the scope to %q, though the board did not change", m.project)
	}
}

// allBacklogProject is a board with one ticket blocked on a human and a
// whole project behind the fold: the shape that separates "the fold is
// hiding this project" from "the pass no longer has it".
func allBacklogProject() loop.Event {
	return loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", TicketID: "id-1", Title: "Fix the build", Status: "Needs Attention",
			Project: "Shipping", Relevance: loop.StatusFailed},
		{Ticket: "LERP-2", TicketID: "id-2", Title: "Someday", Status: "Backlog",
			Project: "Later", Relevance: loop.StatusBacklog},
	}}
}

// Done-when: an inbox holding nothing but backlog says nothing is waiting on
// a human, and still offers the key to the rows behind it. It must not claim
// "the inbox is empty" — a board with nothing waiting at all is the goal
// state, and a fold does not get to wear it.
func TestAFoldedBacklogDoesNotClaimTheGoalState(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-2", TicketID: "id-2", Title: "Someday", Status: "Backlog",
			Relevance: loop.StatusBacklog},
	}}})

	panel := m.attentionPanel(96, 14)
	if !strings.Contains(panel, "nothing is waiting on you") {
		t.Fatalf("an inbox of nothing but backlog does not say so:\n%s", panel)
	}
	if strings.Contains(panel, "the inbox is empty") {
		t.Fatalf("the fold claimed the goal state:\n%s", panel)
	}
	if !strings.Contains(panel, "1 waiting to enter the pipeline — B to browse") {
		t.Fatalf("the one line that is on you is precisely when the key must show:\n%s", panel)
	}
	if got := m.View(); strings.Contains(got, "in the inbox") {
		t.Fatalf("the status bar counts a backlog nobody is blocked on:\n%s", got)
	}
	// No count to show is not no title: the sort is still in force, and a
	// panel that says nothing about it makes `s` a silent state change.
	if !strings.Contains(panel, "by status") {
		t.Fatalf("the title dropped the sort mode along with the count:\n%s", panel)
	}
	m = update(t, m, keyMsg("s"))
	if got := m.attentionPanel(96, 14); !strings.Contains(got, "by project") {
		t.Fatalf("s cycled the sort without the title moving:\n%s", got)
	}
	m = update(t, m, keyMsg("s"))

	// A board with nothing waiting at all still reads as the goal state, and
	// has no fold to advertise.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	panel = m.attentionPanel(96, 14)
	if !strings.Contains(panel, "the inbox is empty") {
		t.Fatalf("an empty board no longer reads as the goal state:\n%s", panel)
	}
	if strings.Contains(panel, "to browse") {
		t.Fatalf("an empty board advertises a fold with nothing behind it:\n%s", panel)
	}
}

// Done-when: a pass does not reset the fold. It is model state like the sort
// and the project scope, and a list that re-folded itself every few seconds
// would take the rows back out from under the operator's hands.
func TestAPassDoesNotResetTheFold(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})
	m = browseBacklog(t, m)

	m = update(t, m, eventMsg{ev: board()})
	if !m.backlogOpen {
		t.Fatal("a pass folded the backlog back")
	}
	if got := len(m.shown); got != 6 {
		t.Fatalf("the list after a pass has %d rows, want the whole expanded list", got)
	}
}

// Done-when: a ticket the operator has claimed, resting in a status Linear
// files as intake, is never folded away and is counted on the status bar.
// The backlog tier is derived from Linear's category alone and says nothing
// about who holds the ticket; a claimed one there did not fail to enter the
// pipeline, it fell back out of one, and no pass can pick it up again while
// the claim stands. Folding it would hide the one row only a human can
// unstick behind a key nothing tells them to press.
func TestAClaimedTicketInIntakeIsNeverFolded(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-5", TicketID: "id-5", Title: "Dragged back to Todo", Status: "Todo",
			Relevance: loop.StatusBacklog, Claimed: true,
			Reason: `claimed in "Todo" — waiting to enter the pipeline`},
		{Ticket: "LERP-6", TicketID: "id-6", Title: "Nobody has started this", Status: "Todo",
			Relevance: loop.StatusBacklog,
			Reason:    `unassigned in "Todo" — waiting to enter the pipeline`},
	}}})

	if got := shownTickets(m); !slices.Equal(got, []string{"LERP-5"}) {
		t.Fatalf("the folded inbox shows %v, want the stranded claimed ticket", got)
	}
	if got := m.View(); !strings.Contains(got, "● 1 in the inbox") {
		t.Fatalf("the status bar does not count the stranded claimed ticket:\n%s", got)
	}
	// And the one beside it, which nobody has claimed, is behind the fold.
	panel := m.attentionPanel(96, 14)
	if !strings.Contains(panel, "1 waiting to enter the pipeline — B to browse") {
		t.Fatalf("the unclaimed intake row is not folded:\n%s", panel)
	}
	if strings.Contains(panel, "nothing is waiting on you") {
		t.Fatalf("a panel with a stranded claimed ticket on it says nothing is:\n%s", panel)
	}
}

// Done-when: an item carrying StatusUnknown is never folded away. It is the
// reconciler's bug marker — nothing set a relevance on this row — which is
// why the fold hides the backlog tier rather than keeping an allow-list of
// the tiers it shows.
func TestTheBugMarkerIsNeverFolded(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-3", TicketID: "id-3", Title: "Nothing ranked this", Status: "Somewhere"},
	}}})

	if got := shownTickets(m); !slices.Equal(got, []string{"LERP-3"}) {
		t.Fatalf("the folded inbox shows %v, want the unranked row it cannot classify", got)
	}
	if panel := m.attentionPanel(96, 14); strings.Contains(panel, "to browse") {
		t.Fatalf("an unranked row was counted as folded backlog:\n%s", panel)
	}
}

// Done-when: leverage, priority and blocked-ness are readable on the row
// itself, without selecting it — and the columns elide from the right, so a
// narrow panel truncates the title first, then drops the project, and never
// costs the identifier or the leverage.
func TestInboxRowsCarryLeverageAndPriority(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-22", Title: "GoReleaser: tagged releases", Priority: 2, Status: "Backlog",
			Project: "Open-source readiness", Unblocks: 3, Blocks: []string{"LERP-23", "LERP-38"}},
		{Ticket: "LERP-36", Title: "Sanitize control characters", Priority: 1, Status: "Backlog"},
		{Ticket: "LERP-23", Title: "curl install", Priority: 3, Status: "Backlog",
			Unblocks: 1, BlockedBy: []string{"LERP-22"}},
	}}})

	panel := m.attentionPanel(70, 8)
	for _, want := range []string{"↓3", "↓0", "⊘", "High", "Urgent", "Medium"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("inbox row is missing %q:\n%s", want, panel)
		}
	}
	// The selection sits on the first row, so the second row's Urgent and
	// the third row's ⊘ are both facts no selection revealed.
	for _, want := range []struct{ ticket, mark string }{
		{"LERP-22", "↓3"}, {"LERP-36", "Urgent"}, {"LERP-23", "⊘"},
	} {
		if got := rowOf(t, panel, want.ticket); !strings.Contains(got, want.mark) {
			t.Fatalf("the %s row does not carry %s:\n%s", want.ticket, want.mark, panel)
		}
	}

	// Narrow enough that the project no longer fits: it goes, the title is
	// cut, and the three columns a routing decision starts from survive.
	narrow := m.attentionPanel(50, 8)
	if strings.Contains(narrow, "Open-source readiness") {
		t.Fatalf("a narrow panel kept the project column:\n%s", narrow)
	}
	// Scoped to the row, not to the panel: a grouped mode draws the status
	// in a header too, and an assertion the header can satisfy would not
	// notice the status column going missing from the row itself.
	for _, want := range []string{"↓3", "Backlog"} {
		if got := rowOf(t, narrow, "LERP-22"); !strings.Contains(got, want) {
			t.Fatalf("a narrow panel dropped %q from the row:\n%s", want, narrow)
		}
	}
	if !strings.Contains(narrow, "Urgent") {
		t.Fatalf("a narrow panel dropped the priority column:\n%s", narrow)
	}
	if strings.Contains(narrow, "GoReleaser: tagged releases") {
		t.Fatalf("a narrow panel did not truncate the title:\n%s", narrow)
	}

	// Narrower than a title-less row: the priority goes too, and the three
	// columns a routing decision cannot start without are the last things
	// standing.
	tiny := m.attentionPanel(34, 8)
	for _, want := range []string{"LERP-22", "↓3", "Backlog"} {
		if !strings.Contains(tiny, want) {
			t.Fatalf("the narrowest panel dropped %q:\n%s", want, tiny)
		}
	}
	for _, gone := range []string{"Urgent", "High"} {
		if strings.Contains(tiny, gone) {
			t.Fatalf("the narrowest panel kept the priority column at the cost of %q:\n%s", gone, tiny)
		}
	}
}

// Done-when: every fixed-width column sits on the left in a stable order and
// the title is the last column, elastic, taking whatever the panel has left.
// The cut then lands at the right edge, on the title, instead of on the one
// column whose whole value is the part being cut off.
func TestInboxTitleIsTheLastColumn(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})
	m = browseBacklog(t, m)

	panel := m.attentionPanel(120, 14)
	row := ansi.Strip(rowOf(t, panel, "LERP-1"))
	cols := []struct{ name, cell string }{
		{"identifier", "LERP-1"}, {"leverage", "\u2193" + "0"}, {"status", "Needs Attention"},
		{"project", "Open-source readiness"}, {"priority", "Medium"}, {"title", "Fix the build"},
	}
	for i := 1; i < len(cols); i++ {
		if strings.Index(row, cols[i].cell) <= strings.Index(row, cols[i-1].cell) {
			t.Fatalf("the %s column is not left of the %s column:\n%s", cols[i-1].name, cols[i].name, row)
		}
	}
	if got := strings.Trim(row, " \u2502\u2503"); !strings.HasSuffix(got, "Fix the build") {
		t.Fatalf("something follows the title, so it is not the last column:\n%s", row)
	}

	// Elastic: the same row in a narrower panel spends the width on the fixed
	// columns and cuts the title at the right edge.
	narrow := ansi.Strip(rowOf(t, m.attentionPanel(90, 14), "LERP-22"))
	if !strings.HasSuffix(strings.Trim(narrow, " \u2502\u2503"), "\u2026") {
		t.Fatalf("the narrower panel did not cut the title at the right edge:\n%s", narrow)
	}

	// A column holds its width only while the title still reads as one: at
	// no width does a row spend columns on the project or the priority while
	// the title itself has been cut to less than they cost. Sweeping every
	// width crosses both boundaries of the elision ladder, where the row
	// prefix actually changes.
	const full22, status22 = "GoReleaser: tagged releases", "Backlog \u26a0"
	for w := 24; w <= 120; w++ {
		row := ansi.Strip(rowOf(t, m.attentionPanel(w, 14), "LERP-22"))
		body := strings.Trim(row, " \u2502\u2503")
		// A cut title is exactly as wide as the space the row left it, so it
		// measures the ladder's decision; a whole one only measures the
		// fixture. Below the ladder the cut reaches the fixed columns
		// themselves — the status and its gutter stop being whole — and
		// there is no title left to measure; the narrowest row is asserted
		// on its own below. The title is the tail past the last gutter, and
		// has to still read as the head of the real one to be measured at
		// all — a fixture that grew a double space would otherwise be
		// silently mismeasured here.
		gutter := strings.LastIndex(body, "  ")
		if gutter < 0 || !strings.HasSuffix(body, "\u2026") || !strings.Contains(body, status22+"  ") {
			continue
		}
		tail := body[gutter+2:]
		if !strings.HasPrefix(full22, strings.TrimSuffix(tail, "\u2026")) {
			t.Fatalf("a %d-column panel did not end in a cut of %q:\n%s", w, full22, row)
		}
		switch title := lipgloss.Width(tail); {
		case strings.Contains(body, "Open-source readiness") && title < titleFloor:
			t.Fatalf("a %d-column panel kept the project for a %d-column title:\n%s", w, title, row)
		case strings.Contains(body, "High") && title < titleStub:
			t.Fatalf("a %d-column panel kept the priority for a %d-column title:\n%s", w, title, row)
		}
	}

	// Narrower than the fixed columns themselves and the cut reaches them:
	// it takes the status, and the identifier and the leverage — the two
	// facts a row is useless without — still read.
	tight := ansi.Strip(rowOf(t, m.attentionPanel(22, 14), "LERP-22"))
	if !strings.Contains(tight, "LERP-22") || !strings.Contains(tight, "\u21932") {
		t.Fatalf("the identifier and the leverage did not survive the narrowest row:\n%s", tight)
	}

	// The status column carries a gutter of its own, so the mark on a status
	// the pipeline never names cannot run into the title on the narrowest
	// row, where the status is the last column before it.
	marked, _, _ := newTestModel(t, 1)
	marked = update(t, marked, keyMsg("1"))
	marked = update(t, marked, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-22", Title: "curl", Status: "Waiting for review", Relevance: loop.StatusUnnamed},
		{Ticket: "LERP-23", Title: "tags", Status: "In Review"},
	}}})
	if got := ansi.Strip(rowOf(t, marked.attentionPanel(46, 12), "LERP-22")); !strings.Contains(got, "\u26a0  curl") {
		t.Fatalf("the unnamed-status mark runs into the title:\n%s", got)
	}

	// The column is measured with the mark, so a marked status wide enough to
	// set the column does not push its own row's columns two past every other
	// row's. LERP-22 carries the widest status on this list and the mark:
	// the only arrangement where a column measured without it comes up short.
	wide := marked.attentionPanel(70, 12)
	at22, at23 := ansi.Strip(rowOf(t, wide, "LERP-22")), ansi.Strip(rowOf(t, wide, "LERP-23"))
	i22, i23 := strings.Index(at22, "curl"), strings.Index(at23, "tags")
	if i22 < 0 || i23 < 0 {
		t.Fatalf("a title is not on its row whole:\n%s", wide)
	}
	if lipgloss.Width(at22[:i22]) != lipgloss.Width(at23[:i23]) {
		t.Fatalf("the marked status pushed its row's title out of the column:\n%s", wide)
	}

	// The priority column carries a gutter of its own for the same reason
	// the status column does: the pad priorityCell leaves is the column's,
	// not the gutter's, so the widest label a row can carry still cannot
	// touch the title.
	urgent, _, _ := newTestModel(t, 1)
	urgent = update(t, urgent, keyMsg("1"))
	urgent = update(t, urgent, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-36", Title: "Sanitize config", Status: "Backlog", Priority: 1},
	}}})
	if got := ansi.Strip(rowOf(t, urgent.attentionPanel(64, 8), "LERP-36")); !strings.Contains(got, "Urgent"+strings.Repeat(" ", priorityW-len("Urgent")+2)+"Sanitize") {
		t.Fatalf("the priority runs into the title:\n%s", got)
	}

	// A leverage count wider than leverageCell's own pad widens the column
	// rather than its own row: every column hangs off the head now, so a row
	// measured short would take its own rung of the ladder and carry every
	// column after it one place right.
	big, _, _ := newTestModel(t, 1)
	big = update(t, big, keyMsg("1"))
	big = update(t, big, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "hundred blocker", Status: "Backlog", Unblocks: 100},
		{Ticket: "LERP-2", Title: "ordinary row", Status: "Backlog", Unblocks: 2},
	}}})
	hundreds := big.attentionPanel(76, 10)
	many, few := ansi.Strip(rowOf(t, hundreds, "LERP-1")), ansi.Strip(rowOf(t, hundreds, "LERP-2"))
	iMany, iFew := strings.Index(many, "hundred blocker"), strings.Index(few, "ordinary row")
	if iMany < 0 || iFew < 0 {
		t.Fatalf("a title is not on its row whole:\n%s", hundreds)
	}
	if lipgloss.Width(many[:iMany]) != lipgloss.Width(few[:iFew]) {
		t.Fatalf("a three-digit leverage count moved its own row's columns:\n%s", hundreds)
	}

	// Fixed columns to the left means every title starts in the same place,
	// whatever the rows around it carry — LERP-23, the blocked row, is the
	// one whose leverage cell is the ⊘ rather than a count.
	at := -1
	for _, tc := range []struct{ ticket, title string }{
		{"LERP-1", "Fix the build"}, {"LERP-48", "Read the ticket in the TUI"},
		{"LERP-60", "Unfiled work"}, {"LERP-23", "curl install"},
	} {
		row := ansi.Strip(rowOf(t, panel, tc.ticket))
		start := strings.Index(row, tc.title)
		if start < 0 {
			t.Fatalf("the %s title is not on its row whole, so the rows cannot be lined up:\n%s", tc.ticket, row)
		}
		i := lipgloss.Width(row[:start])
		if at >= 0 && i != at {
			t.Fatalf("the %s title starts at column %d, the rows above it at %d:\n%s",
				tc.ticket, i, at, panel)
		}
		at = i
	}
}

// Selecting an inbox item and pressing "p" opens the promote picker in
// the main pane; choosing a status and confirming calls Promote with the
// ticket's Linear id and the chosen status, and settles into a transient
// note on the status bar. Cancelling touches nothing.
func TestPromotePicker(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, []string{"Planning", "Implementing"})
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this", Status: "Backlog"},
	}}})

	// esc backs out without promoting.
	m = update(t, m, keyMsg("p"))
	if !m.promoting {
		t.Fatal("p did not open the promote picker")
	}
	m = update(t, m, keyMsg("esc"))
	if m.promoting {
		t.Fatal("esc did not close the promote picker")
	}
	if len(promoter.calls) != 0 {
		t.Fatalf("esc called Promote: %+v", promoter.calls)
	}

	// p, choose the second status, enter: confirms with the right ticket id
	// and status.
	m = update(t, m, keyMsg("p"))
	view := m.View()
	for _, want := range []string{"promote LERP-4", "Planning", "Implementing", "enter promote"} {
		if !strings.Contains(view, want) {
			t.Fatalf("promote picker is missing %q:\n%s", want, view)
		}
	}
	m = update(t, m, keyMsg("down"))
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(model)
	if m.promoting {
		t.Fatal("enter did not close the promote picker")
	}
	if cmd == nil {
		t.Fatal("enter produced no promote command")
	}
	// Promote runs off the render loop, exactly like a tick; running the
	// command here is what actually calls it.
	msg := cmd()
	promoted, ok := msg.(promotedMsg)
	if !ok {
		t.Fatalf("promote command yielded %T, want promotedMsg", msg)
	}
	if got := promoter.last(); got.ticketID != "loose" || got.status != "Implementing" {
		t.Fatalf("Promote call = %+v, want {loose Implementing}", got)
	}

	m = update(t, m, promoted)
	if !strings.Contains(m.View(), "promoted LERP-4 to Implementing") {
		t.Fatalf("view does not note the promotion:\n%s", m.View())
	}
}

// The picker reads its keys from the keymap, so a rebound Up/Down moves the
// selection there like it does everywhere else, and the picker's line on the
// status bar names the new keys. Matching raw strings passed this test's
// stock case and failed both of these.
func TestPromotePickerFollowsTheKeymap(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, []string{"Planning", "Implementing"})
	m.keys.Up = key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "select up"))
	m.keys.Down = key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("ctrl+n", "select down"))
	// Wide enough to be about the keys and not about the room: these labels
	// are six columns each where the arrows were three, and on the stock
	// hundred-column window the bar would rightly spend that on the count
	// instead — which TestThePickersLineGivesWayBeforeTheInboxCount is for.
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = resized.(model)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this", Status: "Backlog"},
	}}})

	m = update(t, m, keyMsg("p"))
	if !m.promoting {
		t.Fatal("p did not open the promote picker")
	}
	view := m.View()
	for _, want := range []string{"ctrl+p ctrl+n choose", "enter promote", "esc cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker hint is missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "↑/↓") || strings.Contains(view, "↑/k") {
		t.Fatalf("picker hint still names the stock arrows after a rebind:\n%s", view)
	}

	m = update(t, m, tea.KeyMsg{Type: tea.KeyCtrlN})
	if m.promoteSel != 1 {
		t.Fatalf("rebound Down left promoteSel = %d, want 1", m.promoteSel)
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.promoteSel != 0 {
		t.Fatalf("rebound Up left promoteSel = %d, want 0", m.promoteSel)
	}

	// The keys Up and Down used to hold are the rebind's to spend: j and k
	// do nothing in the picker now, rather than moving a selection the
	// operator rebound away from them.
	m = update(t, m, keyMsg("j"))
	if m.promoteSel != 0 {
		t.Fatalf("j moved the picker after Down was rebound: promoteSel = %d", m.promoteSel)
	}
	if len(promoter.calls) != 0 {
		t.Fatalf("the picker wrote to Linear while only moving: %+v", promoter.calls)
	}
}

// The picker's line is built from bindings now, so its width is whatever
// their labels add up to rather than a string somebody counted — and
// reading them off the bindings costs columns the hardcoded line never
// paid. Those columns come out of the line, which drops the pair that only
// moves inside the picker, and never out of "● n in the inbox": the number
// the truncation at the bottom of statusBar is written to protect.
//
// Swept rather than pinned, because the widths where this goes wrong are
// the ones nobody thought to pin. Every width the picker can be open at, on
// the stock two-lane board and on ten lanes with a three-digit count, has
// to hold: the bar fits its window, the line still says how to leave the
// modal, the count never comes back and go away again as the window widens,
// and the line never spends columns on the nav pair while the count is
// clipped.
func TestThePickersLineGivesWayBeforeTheInboxCount(t *testing.T) {
	for _, cfg := range []struct{ lanes, items int }{{lanes: 2, items: 12}, {lanes: 10, items: 120}} {
		count := fmt.Sprintf("● %d in the inbox", cfg.items)
		var whole []int
		for w := 30; w <= 120; w++ {
			m, _, _, _ := newPromoteTestModel(t, cfg.lanes, defaultTestStatuses)
			resized, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
			m = resized.(model)
			m = update(t, m, keyMsg("1"))
			var items []loop.AttentionItem
			for i := range cfg.items {
				items = append(items, loop.AttentionItem{
					Ticket: fmt.Sprintf("LERP-%d", i), TicketID: fmt.Sprintf("id-%d", i),
					Title: "Nobody's routed this", Status: "Backlog"})
			}
			m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: items}})
			m = update(t, m, keyMsg("p"))

			bar := m.statusBar()
			where := fmt.Sprintf("%d columns, %d lanes, %d in the inbox", w, cfg.lanes, cfg.items)
			if got := lipgloss.Width(bar); got > w {
				t.Fatalf("%s: the bar came out %d wide:\n%s", where, got, bar)
			}
			// promoteExits, at every width this loop covers.
			for _, want := range []string{"enter promote", "esc cancel"} {
				if !strings.Contains(bar, want) {
					t.Fatalf("%s: the picker's line lost %q:\n%s", where, want, bar)
				}
			}
			hasCount := strings.Contains(bar, count)
			if hasCount {
				whole = append(whole, w)
			} else if len(whole) > 0 {
				t.Fatalf("%s: the count was whole at %d and is clipped again here:\n%s",
					where, whole[0], bar)
			}
			// The nav pair is what the line gives up to leave the count
			// alone, so it can never be on screen while the count is not.
			if strings.Contains(bar, "↑/k ↓/j choose") && !hasCount {
				t.Fatalf("%s: the line kept the nav hint and clipped the count:\n%s", where, bar)
			}
		}
		if len(whole) == 0 {
			t.Fatalf("%d lanes, %d in the inbox: the count was clipped at every width", cfg.lanes, cfg.items)
		}
	}
}

// A pass that reports the promoted ticket gone (it moved out of inbox)
// while the picker is still open must not leave a dangling selection.
func TestPromotePickerClosesWhenTheListEmpties(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this"},
	}}})
	m = update(t, m, keyMsg("p"))
	if !m.promoting {
		t.Fatal("p did not open the promote picker")
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	if m.promoting {
		t.Fatal("promote picker stayed open after its item vanished")
	}
}

// Done-when: a pass that reorders the list while the picker is still open
// must not change what gets promoted — the picker committed to its target
// the moment it opened, and a background pass has no door to that decision.
func TestPromoteCommitsToTheTargetCapturedAtOpen(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, defaultTestStatuses)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()}) // cursor starts on id-1

	m = update(t, m, keyMsg("p"))
	if !m.promoting {
		t.Fatal("p did not open the promote picker")
	}

	// A pass lands while the picker is still open: id-1 (the captured
	// target) has left the board, so the cursor's own row moves on.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-2", TicketID: "id-2", Title: "Second", Status: "Backlog"},
		{Ticket: "LERP-3", TicketID: "id-3", Title: "Third", Status: "Backlog"},
	}}})
	if !m.promoting {
		t.Fatal("the picker closed even though the list is not empty")
	}
	if got := m.selectedAttention(); got == nil || got.TicketID == "id-1" {
		t.Fatalf("test setup: the cursor should have moved off id-1, got %+v", got)
	}

	next, cmd := updateCmd(t, m, keyMsg("enter"))
	m = next
	if cmd == nil {
		t.Fatal("enter produced no promote command")
	}
	cmd()
	if len(promoter.calls) != 1 || promoter.calls[0].ticketID != "id-1" {
		t.Fatalf("Promote calls = %+v, want exactly one call for id-1 (the target captured at open)",
			promoter.calls)
	}
}

// Done-when: esc on another panel closes what is in front of the operator
// rather than spending itself on the inbox's own selection — invisible
// there, since a range draws nothing while that panel is unfocused.
func TestEscOnAnotherPanelDoesNotSwallowTheVisualSelection(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("v"))
	m = update(t, m, keyMsg("j"))
	if !m.visual {
		t.Fatal("v did not start visual mode")
	}

	m = update(t, m, keyMsg("2")) // the work panel
	m = openMain(t, m)
	if !m.detailOpen[panelWork] {
		t.Fatal("test setup: the work pane did not open")
	}

	m = update(t, m, keyMsg("esc"))
	if !m.visual {
		t.Fatal("esc on another panel dropped the inbox's selection instead of closing the pane")
	}
	if m.detailOpen[panelWork] {
		t.Fatal("esc did not close the work pane")
	}
}

// Done-when: a batch failure on the ticket the cursor still stands on — the
// common case, a single promote or a range's last row — is not lost behind
// the arrow: the shape stays the cursor's ▸, recoloured, since the note
// that also names the failure fades with the next clean pass and this must
// not.
func TestCursorRowStillMarksItsOwnFailure(t *testing.T) {
	forceColour(t)
	clean := attentionMark(true, false, false)
	failed := attentionMark(true, false, true)
	if clean == failed {
		t.Fatalf("a failed cursor row renders identically to a clean one: %q", clean)
	}
	if !strings.Contains(failed, "▸") {
		t.Fatalf("a failed cursor row lost its arrow: %q", failed)
	}
}

// Done-when: the recoloured-arrow branch is reachable through the real
// render path, not just as a direct call — a single promote failing leaves
// the cursor on the very ticket that failed, which is the shape
// TestCursorRowStillMarksItsOwnFailure cannot exercise on its own.
func TestSingleTicketFailureMarksTheCursorsOwnRow(t *testing.T) {
	forceColour(t)
	promoter := &recordingPromoter{err: errors.New("claimed by another lerp")}
	m, _, _ := newTestModelWith(t, 1, defaultTestStatuses, promoter, &recordingEjector{}, &recordingStarter{}, &recordingReader{})
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this", Status: "Backlog"},
	}}})

	m = update(t, m, keyMsg("p"))
	next, cmd := updateCmd(t, m, keyMsg("enter"))
	m = next
	if cmd == nil {
		t.Fatal("enter produced no promote command")
	}
	m = update(t, m, cmd())

	width := padList.inner(m.geometry().sideW)
	rows, cur := m.attentionRows(width)
	if cur.at < 0 {
		t.Fatalf("the inbox has no selection: %q", rows)
	}
	if want := attentionMark(true, false, true); !strings.Contains(rows[cur.at], want) {
		t.Fatalf("the cursor's own failed row does not carry the recoloured arrow %q:\n%q", want, rows[cur.at])
	}
}

// Done-when: v, then movement, then p promotes every row the range spans —
// not just the cursor's — through the exact same Promote call a single
// ticket takes, one per target, in order.
func TestBatchPromoteMovesEverySelectedRow(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, defaultTestStatuses)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})

	m = update(t, m, keyMsg("v"))
	m = update(t, m, keyMsg("j"))
	m = update(t, m, keyMsg("j"))
	m = update(t, m, keyMsg("p"))
	m = update(t, m, keyMsg("down")) // choose Implementing

	next, cmd := updateCmd(t, m, keyMsg("enter"))
	m = next
	if m.promoting {
		t.Fatal("enter did not close the promote picker")
	}
	if cmd == nil {
		t.Fatal("enter produced no promote command")
	}
	msg := cmd()
	promoted, ok := msg.(promotedMsg)
	if !ok {
		t.Fatalf("promote command yielded %T, want promotedMsg", msg)
	}

	wantIDs := []string{"id-1", "id-2", "id-3"}
	if len(promoter.calls) != len(wantIDs) {
		t.Fatalf("Promote called %d times, want %d: %+v", len(promoter.calls), len(wantIDs), promoter.calls)
	}
	for i, want := range wantIDs {
		if got := promoter.calls[i]; got.ticketID != want || got.status != "Implementing" {
			t.Fatalf("call %d = %+v, want {%s Implementing}", i, got, want)
		}
	}

	m = update(t, m, promoted)
	if !strings.Contains(m.View(), "promoted 3 tickets to Implementing") {
		t.Fatalf("view does not note the batch promotion:\n%s", m.View())
	}
}

// Done-when: one target failing (a race with another lerp claiming it, say)
// never stops the batch: every call still happens, the note says how many
// of the batch made it, and the failure is named.
func TestMidBatchFailureSettlesTheRest(t *testing.T) {
	promoter := &recordingPromoter{errs: map[string]error{"id-2": errors.New("claimed by another lerp")}}
	m, _, _ := newTestModelWith(t, 1, defaultTestStatuses, promoter, &recordingEjector{}, &recordingStarter{}, &recordingReader{})
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})

	m = update(t, m, keyMsg("v"))
	m = update(t, m, keyMsg("j"))
	m = update(t, m, keyMsg("j"))
	m = update(t, m, keyMsg("p"))
	m = update(t, m, keyMsg("down")) // choose Implementing

	next, cmd := updateCmd(t, m, keyMsg("enter"))
	m = next
	promoted, ok := cmd().(promotedMsg)
	if !ok {
		t.Fatal("promote command yielded no promotedMsg")
	}

	wantIDs := []string{"id-1", "id-2", "id-3"}
	if len(promoter.calls) != len(wantIDs) {
		t.Fatalf("Promote called %d times, want %d — a failure must not abort the batch: %+v",
			len(promoter.calls), len(wantIDs), promoter.calls)
	}

	m = update(t, m, promoted)
	view := m.View()
	if !strings.Contains(view, "promoted 2 of 3 to Implementing") {
		t.Fatalf("view does not note the partial batch:\n%s", view)
	}
	if !strings.Contains(view, "LERP-2") {
		t.Fatalf("view does not name the failed ticket:\n%s", view)
	}
	width := padList.inner(m.geometry().sideW)
	rows, _ := m.attentionRows(width)
	if !strings.Contains(rows[1], "✗") {
		t.Fatalf("the failed row does not carry a ✗:\n%q", rows[1])
	}
}

// Done-when: esc during a range degrades cleanly to single-ticket promote,
// acting on the cursor's own row.
func TestEscDegradesVisualToSingleTicket(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, defaultTestStatuses)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})

	m = update(t, m, keyMsg("v")) // anchor: id-1
	m = update(t, m, keyMsg("j")) // cursor: id-2
	m = update(t, m, keyMsg("esc"))
	if m.visual {
		t.Fatal("esc did not end the visual selection")
	}

	m = update(t, m, keyMsg("p"))
	next, cmd := updateCmd(t, m, keyMsg("enter"))
	m = next
	if cmd == nil {
		t.Fatal("enter produced no promote command")
	}
	cmd()
	if len(promoter.calls) != 1 {
		t.Fatalf("Promote called %d times, want 1: %+v", len(promoter.calls), promoter.calls)
	}
	if got := promoter.calls[0].ticketID; got != "id-2" {
		t.Fatalf("promoted %s, want id-2 (the cursor's row after esc)", got)
	}
}

// Done-when: the four keys that reorder or narrow the rows a range is drawn
// over drop the selection — a range whose endpoints stay put while the rows
// between them change is a promote of tickets the operator never saw.
func TestDisplayControlsDropTheSelection(t *testing.T) {
	for _, key := range []string{"s", "P", "B", "/"} {
		t.Run(key, func(t *testing.T) {
			m, _, _, promoter := newPromoteTestModel(t, 1, defaultTestStatuses)
			m = update(t, m, keyMsg("1"))
			m = update(t, m, eventMsg{ev: threeWaiting()})

			m = update(t, m, keyMsg("v"))
			m = update(t, m, keyMsg("j"))
			if !m.visual {
				t.Fatal("v did not start visual mode")
			}

			m = update(t, m, keyMsg(key))
			if m.visual {
				t.Fatalf("%q did not drop the visual selection", key)
			}
			if key == "/" {
				m = update(t, m, keyMsg("esc")) // back out of the prompt it opened
			}

			m = update(t, m, keyMsg("p"))
			next, cmd := updateCmd(t, m, keyMsg("enter"))
			m = next
			if cmd == nil {
				t.Fatal("enter produced no promote command")
			}
			cmd()
			if len(promoter.calls) != 1 {
				t.Fatalf("%q left the selection live: Promote called %d times, want 1: %+v",
					key, len(promoter.calls), promoter.calls)
			}
		})
	}
}

// Done-when: a pass that no longer lists the anchor ends visual mode, the
// same degradation esc gives, rather than promoting a range drawn over rows
// that have since changed underneath it.
func TestVisualEndsWhenTheAnchorLeaves(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, defaultTestStatuses)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})

	m = update(t, m, keyMsg("v")) // anchor: id-1
	m = update(t, m, keyMsg("j"))
	m = update(t, m, keyMsg("j")) // cursor: id-3

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-2", TicketID: "id-2", Title: "Second", Status: "Backlog"},
		{Ticket: "LERP-3", TicketID: "id-3", Title: "Third", Status: "Backlog"},
	}}})
	if m.visual {
		t.Fatal("visual mode survived the anchor leaving the board")
	}

	m = update(t, m, keyMsg("p"))
	next, cmd := updateCmd(t, m, keyMsg("enter"))
	m = next
	if cmd == nil {
		t.Fatal("enter produced no promote command")
	}
	cmd()
	if len(promoter.calls) != 1 {
		t.Fatalf("Promote called %d times, want 1: %+v", len(promoter.calls), promoter.calls)
	}
	if got := promoter.calls[0].ticketID; got != "id-3" {
		t.Fatalf("promoted %s, want id-3 (the surviving cursor row)", got)
	}
}

// Done-when: the rows between the anchor and the cursor carry the selection
// band, only the cursor's own row carries the ▸, and the panel's key line
// swaps to the promote-count hint while a range is live.
func TestVisualRangeRendersTheBandAndTheKeyLine(t *testing.T) {
	m, _, _, _ := newPromoteTestModel(t, 1, defaultTestStatuses)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})

	m = update(t, m, keyMsg("v")) // anchor: id-1 (LERP-1)
	m = update(t, m, keyMsg("j")) // cursor: id-2 (LERP-2); LERP-3 stays out of range

	// Checked before forceColour: with escapes in play the key line's words
	// render as separate spans, and this is plain text either way.
	line := lineWith(t, m.View(), "p promote")
	if !strings.Contains(line, "p promote 2") {
		t.Fatalf("the key line does not name the selection count:\n%s", line)
	}
	if !strings.Contains(line, "esc drop") {
		t.Fatalf("the key line does not offer esc to drop the selection:\n%s", line)
	}

	forceColour(t)
	width := padList.inner(m.geometry().sideW)
	rows, cur := m.attentionRows(width)
	if cur.at < 0 {
		t.Fatalf("the inbox has no selection to band: %q", rows)
	}
	var banded, arrows int
	for i, r := range rows {
		if strings.Contains(r, bandOpen()) {
			banded++
		}
		if strings.Contains(r, "▸") {
			arrows++
			if i != cur.at {
				t.Fatalf("the cursor arrow is on row %d, want the cursor's row %d: %q", i, cur.at, r)
			}
		}
	}
	if banded != 2 {
		t.Fatalf("the band covers %d rows, want 2 (the anchor and the cursor): %q", banded, rows)
	}
	if arrows != 1 {
		t.Fatalf("expected exactly one cursor arrow, got %d: %q", arrows, rows)
	}
}

// Done-when: a range still reads without colour. The band itself draws
// nothing on a 16-colour terminal (colorSelected's slots are empty on
// purpose, see theme.go) — fine while the band only ever marked the
// cursor's own row, wearing its own ▸, but a range's other rows have no
// other mark unless the gutter draws one. A selection the operator cannot
// see is a promote of tickets they never chose.
func TestVisualRangeReadsWithoutColour(t *testing.T) {
	was := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(was) })
	lipgloss.SetColorProfile(termenv.Ascii)

	m, _, _, _ := newPromoteTestModel(t, 1, defaultTestStatuses)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("v")) // anchor: id-1 (row 0)
	m = update(t, m, keyMsg("j")) // cursor: id-2 (row 1)

	width := padList.inner(m.geometry().sideW)
	rows, cur := m.attentionRows(width)
	if bandOpen() != "" {
		t.Fatal("test setup: this profile draws a band, which defeats the point of the test")
	}
	if cur.at != 1 {
		t.Fatalf("test setup: expected the cursor on row 1, got %d", cur.at)
	}
	if !strings.Contains(rows[0], "│") {
		t.Fatalf("the range's non-cursor row carries no shape without colour: %q", rows[0])
	}
	if strings.Contains(rows[0], "▸") {
		t.Fatalf("the range's non-cursor row wrongly carries the cursor's own arrow: %q", rows[0])
	}
	if strings.Contains(rows[2], "│") {
		t.Fatalf("a row outside the range carries the range's shape: %q", rows[2])
	}
}

// The status bar carries the mark, the heartbeat and the counts; the ? key
// swaps the main pane for the full keymap.
func TestStatusBarAndHelp(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	// The bar is only on screen once the board is: the splash covers the
	// first pass, mark and all. So this is the bar the board hands over to,
	// and the pass it reports is the one after the one the splash covered.
	m = pastTheSplash(t, m)
	if !strings.Contains(m.statusBar(), "lerp") {
		t.Fatalf("status bar does not carry the mark:\n%s", m.statusBar())
	}
	m = update(t, m, tickMsg{})
	if !strings.Contains(m.View(), "pass running") {
		t.Fatalf("status bar hides the pass in flight:\n%s", m.View())
	}
	m = update(t, m, tickedMsg{})

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "one"}, {Ticket: "LERP-2", Title: "two"},
	}}})
	if !strings.Contains(m.View(), "2 in the inbox") {
		t.Fatalf("status bar does not count inbox:\n%s", m.View())
	}

	m = update(t, m, keyMsg("?"))
	view := m.View()
	if !strings.Contains(view, "open in Linear") || !strings.Contains(view, "cycle back") {
		t.Fatalf("help overlay is missing bindings:\n%s", view)
	}
	m = update(t, m, keyMsg("?"))
	if strings.Contains(m.View(), "cycle back") {
		t.Fatalf("help overlay did not close:\n%s", m.View())
	}
}

// fillBoard puts n items in inbox and n tickets in one queue, so every
// panel has more rows than a small window can hold.
func fillBoard(t *testing.T, m model, n int) model {
	t.Helper()
	items := make([]loop.AttentionItem, n)
	tickets := make([]loop.QueueTicket, n)
	for i := range items {
		items[i] = loop.AttentionItem{Ticket: fmt.Sprintf("LERP-%d", i+1),
			Title: "something waits", Status: "Backlog", Reason: "no queue serves it"}
		tickets[i] = loop.QueueTicket{ID: fmt.Sprintf("t%d", i),
			Identifier: fmt.Sprintf("QUEUED-%d", i+1), Title: "work", Eligible: true}
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: items}})
	return update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: tickets},
	}}})
}

// Inbox gets the room: with 15 items waiting and every queue empty, it
// takes what work does not, work asks only for the rows it renders — and
// none of that moves when focus does. The geometry is the same screen
// whichever panel the operator is working in.
func TestInboxTakesTheRoomAndFocusDoesNotMoveIt(t *testing.T) {
	m, _, _ := newTestModel(t, 3)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = resized.(model)
	items := make([]loop.AttentionItem, 15)
	for i := range items {
		items[i] = loop.AttentionItem{Ticket: fmt.Sprintf("LERP-%d", i+1),
			Title: "something waits", Status: "Backlog"}
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: items}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "plan", Status: "Planning"},
		{Team: "LERP", Name: "implement", Status: "Todo"},
		{Team: "LERP", Name: "review", Status: "In Review"},
	}}})

	m = update(t, m, keyMsg("1"))
	g := m.geometry()
	if got := g.attnH + g.workH; got != g.bodyH {
		t.Fatalf("stack is %d lines in a %d-line body", got, g.bodyH)
	}
	rows, _ := m.workListRows(g.sideW - 2)
	if g.workH != len(rows)+2 {
		t.Fatalf("work is %d lines for %d rows it renders", g.workH, len(rows))
	}
	if g.attnH != g.bodyH-g.workH {
		t.Fatalf("needs-you is %d lines, want the other %d", g.attnH, g.bodyH-g.workH)
	}
	view := m.View()
	for i := 1; i <= 15; i++ {
		if !strings.Contains(view, fmt.Sprintf("LERP-%d ", i)) {
			t.Fatalf("inbox dropped LERP-%d with a column to spare:\n%s", i, view)
		}
	}
	if strings.Contains(view, "more") {
		t.Fatalf("inbox cut its list with room left over:\n%s", view)
	}

	// Focus moves and the stack does not: work never grows past its share
	// just because it is the panel being worked in.
	m = update(t, m, keyMsg("2"))
	if g2 := m.geometry(); g2.attnH != g.attnH || g2.workH != g.workH {
		t.Fatalf("focus moved the stack: inbox %d→%d, work %d→%d",
			g.attnH, g2.attnH, g.workH, g2.workH)
	}
}

// Padding is asymmetric on purpose: the main pane takes a space inside both
// borders, because prose pressed against a box edge reads badly, while the
// list panels take a left pad only — horizontal padding costs two columns a
// panel and the needs-you table is already truncating titles, so its right
// edge stays the truncation point it was.
// Done-when: the list owns the screen until the operator asks for the
// detail. At 120 columns a closed inbox is the whole width, and the title
// the 45% split truncates survives whole — which is the complaint this
// ticket is about, asserted directly.
func TestTheInboxStartsWithTheScreen(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = resized.(model)
	m = update(t, m, keyMsg("1"))
	const title = "Enter opens the detail pane; the list has the screen until then"
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-66", TicketID: "id-66", Title: title,
			Status: "Backlog", Reason: "unclaimed"},
	}}})

	g := m.geometry()
	if g.sideW != m.width || g.mainW != 0 || g.mainH != 0 {
		t.Fatalf("a closed inbox still left room for the main pane: %+v", g)
	}
	if !strings.Contains(ansi.Strip(m.View()), title) {
		t.Fatalf("the full-width inbox truncates its title anyway:\n%s", m.View())
	}

	// The same title against the open pane's 45% column does not fit; without
	// this the assertion above would pass on a title that never truncated.
	m = update(t, m, keyMsg("enter"))
	if strings.Contains(ansi.Strip(m.View()), title) {
		t.Fatalf("this title fits beside an open pane, so the test proves nothing:\n%s", m.View())
	}
}

// Done-when: enter opens the pane on the ticket the cursor is on, esc closes
// it, and neither key is a flip-flop.
func TestEnterOpensTheDetailAndEscCloses(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	if g := m.geometry(); g.mainH != 0 {
		t.Fatalf("the inbox pane was open before enter: %+v", g)
	}

	m = update(t, m, keyMsg("enter"))
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "the body of the first"}, nil, reader)
	g := m.geometry()
	if g.sideW != m.width*45/100 || g.mainH == 0 {
		t.Fatalf("enter did not open the pane: %+v", g)
	}
	if !strings.Contains(m.View(), "the body of the first") {
		t.Fatalf("the opened pane holds nothing:\n%s", m.View())
	}

	m = update(t, m, keyMsg("esc"))
	if g := m.geometry(); g.sideW != m.width || g.mainH != 0 {
		t.Fatalf("esc did not give the width back: %+v", g)
	}
	if strings.Contains(m.View(), "the body of the first") {
		t.Fatalf("the closed pane is still on screen:\n%s", m.View())
	}

	// Neither key flips: an operator who has lost track of the state presses
	// one and knows what they get.
	m = update(t, m, keyMsg("esc"))
	if m.detailOpen[panelAttention] {
		t.Fatal("a second esc reopened the pane")
	}
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, keyMsg("enter"))
	if !m.detailOpen[panelAttention] {
		t.Fatal("a second enter closed the pane")
	}
}

// Done-when: one toggle, two memories. Both panels start closed now, so what
// the state has to survive is the operator opening one of them and walking
// away: the answer is the panel's and not the screen's.
func TestThePaneIsRememberedPerPanel(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("enter")) // work
	m = update(t, m, keyMsg("1"))
	if m.mainOpen() {
		t.Fatal("the inbox inherited the pane work opened")
	}
	m = update(t, m, keyMsg("2"))
	if !m.mainOpen() {
		t.Fatal("work forgot that enter opened its pane")
	}

	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("esc"))
	m = update(t, m, keyMsg("1"))
	if !m.mainOpen() {
		t.Fatal("the inbox lost its pane to an esc pressed in work")
	}
}

// Done-when: work starts with the list owning the screen, exactly as the
// inbox does. The log is something the operator opens to read one run — the
// row itself already says whether that run is alive — so it waits for enter
// and gives the screen back on esc. Lerp opens on the inbox now, so work is
// a panel key away before any of that.
func TestWorkStartsWithTheListOnScreen(t *testing.T) {
	log := filepath.Join(t.TempDir(), "one.log")
	writeLog(t, log, []byte("agent one says hello\n"))

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "plan", LogPath: log}})

	if m.focus != panelAttention {
		t.Fatalf("focus starts on %v, not the inbox", m.focus)
	}
	if g := m.geometry(); g.mainH != 0 || g.sideW != m.width {
		t.Fatalf("the startup screen opened a main pane: %+v", g)
	}
	if strings.Contains(m.View(), "agent one says hello") {
		t.Fatalf("the opening screen is a log, not the inbox:\n%s", m.View())
	}

	m = update(t, m, keyMsg("2"))
	if g := m.geometry(); g.mainH != 0 || g.sideW != m.width {
		t.Fatalf("work started with its pane open: %+v", g)
	}
	if strings.Contains(m.View(), "agent one says hello") {
		t.Fatalf("the log is on screen before anything asked for it:\n%s", m.View())
	}

	m = update(t, m, keyMsg("enter"))
	if g := m.geometry(); g.mainH == 0 {
		t.Fatalf("enter did not open the log: %+v", g)
	}
	if !strings.Contains(m.View(), "agent one says hello") {
		t.Fatalf("the opened pane holds no log:\n%s", m.View())
	}

	m = update(t, m, keyMsg("esc"))
	if g := m.geometry(); g.mainH != 0 || g.sideW != m.width {
		t.Fatalf("esc did not give the width back: %+v", g)
	}
	if strings.Contains(m.View(), "agent one says hello") {
		t.Fatalf("the closed pane is still drawing the log:\n%s", m.View())
	}
}

// Done-when: the promote picker and the ? overlay live in the main pane, so
// they force it visible while they are up and hand the width back when they
// close. Both keys keep working from a closed inbox, which is the default.
func TestThePickerAndTheOverlayForceThePane(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this"},
	}}})
	if m.mainOpen() {
		t.Fatal("the inbox pane was open before anything asked for it")
	}

	m = update(t, m, keyMsg("p"))
	if !strings.Contains(m.View(), "promote LERP-4") {
		t.Fatalf("p from a closed inbox drew no picker:\n%s", m.View())
	}
	m = update(t, m, keyMsg("esc"))
	if g := m.geometry(); m.mainOpen() || g.sideW != m.width {
		t.Fatalf("cancelling the picker kept the width: %+v", g)
	}

	m = update(t, m, keyMsg("?"))
	if !strings.Contains(m.View(), "cycle back") {
		t.Fatalf("? from a closed inbox drew no overlay:\n%s", m.View())
	}
	m = update(t, m, keyMsg("?"))
	if g := m.geometry(); m.mainOpen() || g.sideW != m.width {
		t.Fatalf("closing the overlay kept the width: %+v", g)
	}
}

// Done-when: with the pane shut nobody is reading the detail, so walking the
// inbox costs no Linear calls at all. enter is what asks for one.
func TestAClosedPaneReadsNothing(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	for _, k := range []string{"j", "j", "k"} {
		var cmd tea.Cmd
		m, cmd = updateCmd(t, m, keyMsg(k))
		if cmd != nil {
			t.Fatalf("%q scheduled a read with the pane closed", k)
		}
	}
	if got := reader.fetched(); len(got) != 0 {
		t.Fatalf("a closed pane read %v", got)
	}

	// The picker and the overlay take the pane for themselves and render the
	// detail nowhere, so forcing it visible must not fetch one either.
	for _, k := range []string{"p", "esc", "?", "?"} {
		var cmd tea.Cmd
		m, cmd = updateCmd(t, m, keyMsg(k))
		if cmd != nil {
			t.Fatalf("%q scheduled a read for a detail it never draws", k)
		}
	}
	if got := reader.fetched(); len(got) != 0 {
		t.Fatalf("the picker and the overlay read %v", got)
	}

	m, cmd := updateCmd(t, m, keyMsg("enter"))
	if cmd == nil {
		t.Fatal("enter scheduled no read for the row it opened on")
	}
	id := m.shown[m.attnSel].TicketID
	m, cmd = updateCmd(t, m, detailDueMsg{ticketID: id})
	if cmd == nil {
		t.Fatalf("the settled selection %s fired no fetch", id)
	}
	m = update(t, m, cmd())
	if got := reader.fetched(); len(got) != 1 || got[0] != id {
		t.Fatalf("fetched %v, want one read of %s", got, id)
	}
}

// Done-when: the pane is current the frame it appears. The panels remember
// it separately, so focus moves the width the viewport wraps to — and prose
// wrapped to the panel the operator just left stays wrong until the next
// keystroke or the next byte of log.
func TestRefocusingRewrapsThePane(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	body := strings.TrimSpace(strings.Repeat("wrap me please ", 12))
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: body}, nil, reader)
	if !strings.Contains(m.View(), "wrap me please wrap") {
		t.Fatalf("the body is not wrapped to the pane to begin with:\n%s", m.View())
	}

	// Out to work with its pane closed and back. The inbox's pane is the
	// width it always was; what it holds has to be too.
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("esc"))
	m = update(t, m, keyMsg("1"))
	if !strings.Contains(m.View(), "wrap me please wrap") {
		t.Fatalf("the pane came back wrapped to the closed panel's width:\n%s", m.View())
	}
}

// Done-when: a window too short to hold the pane keeps its screen. The keys
// that *open* a pane — enter, p, ? — do nothing rather than trade the two
// panels for "window too small", and the promote picker, which is modal and
// writes to Linear, is never live behind that message. The panel keys are
// the deliberate exception, since they edit nobody's pane: see
// TestAFocusMoveNeverEditsAPanelsPane.
//
// The screen under test is the one lerp opens on — the inbox with its pane
// closed, which is what makes twelve lines usable at all — so the pane those
// twelve lines cannot hold is opened here and handed back with the esc that
// frame names.
func TestAWindowTooShortForThePaneKeepsItsScreen(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = openMain(t, m) // the pane 12 lines cannot hold
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = resized.(model)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this"},
	}}})
	if !strings.Contains(m.View(), "too small") {
		t.Fatalf("12 lines held the open pane, so this test is not exercising the case:\n%s", m.View())
	}
	m = update(t, m, keyMsg("esc")) // the way out the message names
	m = update(t, m, keyMsg("1"))
	if strings.Contains(m.View(), "too small") {
		t.Fatalf("a closed pane did not buy this window a screen:\n%s", m.View())
	}
	view := m.View()
	if strings.Contains(view, "enter detail") || strings.Contains(view, "? help") {
		t.Fatalf("the status bar advertises a key this window has no room for:\n%s", view)
	}
	if !strings.Contains(view, "q quit") {
		t.Fatalf("the status bar dropped the key that still works:\n%s", view)
	}

	// Not "2" or "tab": a panel key edits nobody's pane, so with every pane
	// closed it has no screen to take away. See
	// TestAFocusMoveNeverEditsAPanelsPane for the case where one does land
	// on that frame, and why that is the right end of the trade.
	for _, k := range []string{"enter", "p", "?"} {
		next := update(t, m, keyMsg(k))
		if strings.Contains(next.View(), "too small") {
			t.Fatalf("%q took the screen away:\n%s", k, next.View())
		}
		if next.promoting {
			t.Fatalf("%q left the promote picker live with nothing drawn", k)
		}
	}
}

// Done-when: a closed pane is nothing to scroll. Silently unfollowing a log
// the operator cannot see leaves it parked at the top when it reopens, with
// nothing on screen to say why.
func TestScrollingAClosedPaneIsInert(t *testing.T) {
	log := filepath.Join(t.TempDir(), "one.log")
	writeLog(t, log, []byte(strings.Repeat("a line of agent output\n", 200)))

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("2"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "plan", LogPath: log}})
	if !m.follow {
		t.Fatal("a fresh log is not being followed")
	}

	// Open the pane and shut it again: a viewport that was never filled has
	// nothing to scroll either way, and the keys would look inert on any
	// implementation.
	m = openMain(t, m)
	if m.vp.Height >= m.vp.TotalLineCount() {
		t.Fatalf("the pane holds %d lines in %d rows: there is nothing to scroll",
			m.vp.TotalLineCount(), m.vp.Height)
	}
	m = update(t, m, keyMsg("esc"))
	offset := m.vp.YOffset
	// g is the damaging one — it parks the pane at the top and stops the
	// tail — so the sequence must not end on the key that undoes it.
	for _, k := range []string{"pgup", "pgdown", "g"} {
		m = update(t, m, keyMsg(k))
		if !m.follow || m.vp.YOffset != offset {
			t.Fatalf("%q moved a closed pane: follow %v, offset %d then %d",
				k, m.follow, offset, m.vp.YOffset)
		}
	}

	// Reopening it is still the live tail, not the top of the scrollback.
	m = update(t, m, keyMsg("enter"))
	if !m.follow || !m.vp.AtBottom() {
		t.Fatalf("the reopened pane is not following: follow %v, at bottom %v",
			m.follow, m.vp.AtBottom())
	}
}

// Done-when: the pane opens onto the row the cursor is on now, not the one
// it was on when the pane was last shut. Nothing refreshes it while it is
// closed, so opening has to.
func TestTheReopenedPaneIsCurrent(t *testing.T) {
	dir := t.TempDir()
	one, two := filepath.Join(dir, "one.log"), filepath.Join(dir, "two.log")
	writeLog(t, one, []byte("agent one says hello\n"))
	writeLog(t, two, []byte("agent two says hello\n"))

	m, _, _ := newTestModel(t, 2)
	m = update(t, m, keyMsg("2"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "plan", LogPath: one}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r2", Lane: 2,
		TicketID: "id-2", Ticket: "LERP-2", Queue: "plan", LogPath: two}})

	// The pane has to have held the first row's log for reopening on the
	// second to be able to come back stale.
	m = openMain(t, m)
	if !strings.Contains(m.View(), "agent one says hello") {
		t.Fatalf("the pane never held the first row's log:\n%s", m.View())
	}
	m = update(t, m, keyMsg("esc"))
	m = update(t, m, keyMsg("down"))
	m = update(t, m, keyMsg("enter"))
	view := m.View()
	if !strings.Contains(view, "agent two says hello") {
		t.Fatalf("the pane opened on the row the cursor left, not the one it is on:\n%s", view)
	}
	if strings.Contains(view, "agent one says hello") {
		t.Fatalf("the reopened pane still holds the old row's log:\n%s", view)
	}
}

// Done-when: a focus move never edits either panel's pane, whatever the
// window can hold. Both panels start closed, so the pane in question is one
// the operator opened themselves — and `2` back to it, in a window too short
// to hold it, lands on the too-small screen rather than quietly dropping it.
// That is the same screen a shrink under an open pane lands on, naming the
// same key, and `esc` gets the board back.
//
// The alternative — closing the pane on the operator's behalf — is a change
// they did not ask for, at the moment they asked for something else, with no
// line on screen to say it happened. `enter` would bring it back, but only
// once they noticed the log was gone. A `2` typed ahead of the first
// WindowSizeMsg, when width and height are still zero and nothing has room,
// would shut a pane before any window had been measured.
func TestAFocusMoveNeverEditsAPanelsPane(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = fillBoard(t, m, 5)
	// Work's pane, opened in a window with room and then left behind.
	m = update(t, m, keyMsg("2"))
	m = openMain(t, m)
	m = update(t, m, keyMsg("1"))
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = resized.(model)

	m = update(t, m, keyMsg("2"))
	if !m.detailOpen[panelWork] {
		t.Fatal("moving focus to work closed the pane it remembers")
	}
	view := m.View()
	if !strings.Contains(view, "too small") || !strings.Contains(view, "esc") {
		t.Fatalf("the pane that does not fit is on nobody's screen and named by nothing:\n%s", view)
	}

	// esc is the key that frame names, and closing the pane is the
	// operator's own answer — so it is theirs to keep at any later size.
	m = update(t, m, keyMsg("esc"))
	if m.detailOpen[panelWork] {
		t.Fatal("esc did not close the pane the too-small screen named")
	}
	if strings.Contains(m.View(), "too small") {
		t.Fatalf("esc did not give the window its screen back:\n%s", m.View())
	}
	grown, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m = grown.(model); m.detailOpen[panelWork] {
		t.Fatal("a window with room reopened a pane the operator closed")
	}

	// The same in the other direction, which needs the inbox's pane opened
	// first: a window with room, `enter` on the inbox, away to work, and
	// then shrink under it. Coming back to the inbox still finds its answer.
	back, _, _ := newTestModel(t, 1)
	back = fillBoard(t, back, 5)
	back = update(t, back, keyMsg("enter"))
	if !back.detailOpen[panelAttention] {
		t.Fatal("enter did not open the inbox's pane in a window with room")
	}
	back = update(t, back, keyMsg("2"))
	shrunk, _ := back.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	back = update(t, shrunk.(model), keyMsg("1"))
	if !back.detailOpen[panelAttention] {
		t.Fatal("moving focus back to the inbox closed the pane it remembers")
	}

	// And a panel key before the first WindowSizeMsg — no window has room
	// for anything yet — leaves both defaults exactly where they started.
	early := newModel(t.Context(), Options{Engine: newFakeEngine(), Statuses: defaultTestStatuses,
		Lanes: 1, Events: make(chan loop.Event, 1)})
	early = update(t, early, keyMsg("2"))
	if early.detailOpen != ([2]bool{panelAttention: false, panelWork: false}) {
		t.Fatalf("a keystroke before the first size edited the pane defaults: %v", early.detailOpen)
	}
}

// Done-when: the too-small frame names the key that is actually next. esc
// resolves nearest-first, so a filter still on the list is what the first one
// takes — and that frame draws no status bar, so an esc that looks like it
// did nothing is all the operator gets.
func TestTheTooSmallScreenNamesTheFilterItWillClearFirst(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = fillBoard(t, m, 5)
	m = update(t, m, keyMsg("2"))
	m = openMain(t, m) // the pane `2` comes back to below
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("/"))
	m = update(t, m, keyMsg("something"))
	m = update(t, m, keyMsg("enter")) // keep the filter, hand the keys back
	if m.search == "" {
		t.Fatal("the filter did not survive the prompt closing")
	}

	// `2` goes back to the log the operator left open: on this window that
	// lands on the too-small screen.
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = update(t, resized.(model), keyMsg("2"))
	view := m.View()
	if !strings.Contains(view, "too small") {
		t.Fatalf("this window holds work's pane after all:\n%s", view)
	}
	if !strings.Contains(view, "clears the filter") {
		t.Fatalf("the frame promises a pane the first esc will not close:\n%s", view)
	}

	// And it is telling the truth: esc takes the filter, esc takes the pane.
	m = update(t, m, keyMsg("esc"))
	if m.search != "" || !m.detailOpen[panelWork] {
		t.Fatalf("the first esc did not take the filter alone: search %q, pane %v",
			m.search, m.detailOpen[panelWork])
	}
	m = update(t, m, keyMsg("esc"))
	if m.detailOpen[panelWork] || strings.Contains(m.View(), "too small") {
		t.Fatalf("the second esc did not give the window its screen:\n%s", m.View())
	}
}

// Done-when: a live visual selection is also named before the pane it is
// nearer than in the Close cascade — the same "esc that looks like it did
// nothing is the worse half of the trade" reasoning the filter case above
// tests, for the selection instead of a filter.
func TestTheTooSmallScreenNamesTheSelectionItWillDropFirst(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("v"))
	m = update(t, m, keyMsg("j"))
	m = openMain(t, m) // the inbox's own pane
	if !m.visual {
		t.Fatal("test setup: visual mode did not start")
	}

	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = resized.(model)
	view := m.View()
	if !strings.Contains(view, "too small") {
		t.Fatalf("this window holds the inbox's pane after all:\n%s", view)
	}
	if !strings.Contains(view, "drops the selection") {
		t.Fatalf("the frame promises a pane the first esc will not close:\n%s", view)
	}

	// And it is telling the truth: esc takes the selection, esc takes the pane.
	m = update(t, m, keyMsg("esc"))
	if m.visual || !m.detailOpen[panelAttention] {
		t.Fatalf("the first esc did not take the selection alone: visual %v, pane %v",
			m.visual, m.detailOpen[panelAttention])
	}
	m = update(t, m, keyMsg("esc"))
	if m.detailOpen[panelAttention] || strings.Contains(m.View(), "too small") {
		t.Fatalf("the second esc did not give the window its screen:\n%s", m.View())
	}
}

// Done-when: clearing a filter from outside the inbox does not leave a live
// range's endpoints standing over rows that were filtered out when the
// operator drew it — the same rule `/` itself follows to open a search, now
// followed by the path that closes one.
func TestClearingTheFilterFromAnotherPanelDropsTheSelection(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, defaultTestStatuses)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})

	// Narrow to one row, select it under the filter, then start a range —
	// LERP-2 is both the anchor and the cursor here.
	m = update(t, m, keyMsg("/"))
	m = update(t, m, keyMsg("Second"))
	m = update(t, m, keyMsg("enter")) // keep the filter, hand the keys back
	if len(m.shown) != 1 || m.shown[0].TicketID != "id-2" {
		t.Fatalf("test setup: the filter did not narrow to id-2 alone: %+v", m.shown)
	}
	m = update(t, m, keyMsg("v"))
	if !m.visual {
		t.Fatal("test setup: visual mode did not start")
	}

	// Switch panels, then clear the filter with a bare esc — not `/`, not
	// ClearSearch, the Close cascade's own rung.
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("esc"))
	if m.search != "" {
		t.Fatalf("test setup: esc did not clear the filter: %q", m.search)
	}
	if m.visual {
		t.Fatal("the selection survived the filter clearing out from under it")
	}

	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("p"))
	next, cmd := updateCmd(t, m, keyMsg("enter"))
	m = next
	if cmd == nil {
		t.Fatal("enter produced no promote command")
	}
	cmd()
	if len(promoter.calls) != 1 {
		t.Fatalf("Promote called %d times, want 1 (the cursor's own row, not a stale range): %+v",
			len(promoter.calls), promoter.calls)
	}
}

// Done-when: a pass that silently resets the project scope (because no
// ticket carries it anymore) does not leave a live range's endpoints
// standing over rows the scope no longer narrows — the same rule P itself
// follows when the operator cycles the scope by hand.
func TestProjectScopeResetDropsTheSelection(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, defaultTestStatuses)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", TicketID: "id-1", Title: "First", Status: "Backlog", Project: "A"},
		{Ticket: "LERP-2", TicketID: "id-2", Title: "Second", Status: "Backlog", Project: "A"},
	}}})

	m = update(t, m, keyMsg("P")) // the only project on the board
	if m.project != "A" {
		t.Fatalf("test setup: P did not scope to project A: %q", m.project)
	}
	m = update(t, m, keyMsg("v"))
	m = update(t, m, keyMsg("j"))
	if !m.visual {
		t.Fatal("test setup: visual mode did not start")
	}

	// A pass lands where LERP-1 survives (so the anchor has not left) but no
	// ticket carries project A anymore: the scope silently widens to every
	// project, the way apply's own project-reset already does by hand.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", TicketID: "id-1", Title: "First", Status: "Backlog", Project: "B"},
		{Ticket: "LERP-2", TicketID: "id-2", Title: "Second", Status: "Backlog", Project: "B"},
		{Ticket: "LERP-3", TicketID: "id-3", Title: "Third", Status: "Backlog", Project: "C"},
	}}})
	if m.project != "" {
		t.Fatalf("test setup: the scope did not reset: %q", m.project)
	}
	if m.visual {
		t.Fatal("the selection survived the project scope resetting out from under it")
	}

	m = update(t, m, keyMsg("p"))
	next, cmd := updateCmd(t, m, keyMsg("enter"))
	m = next
	if cmd == nil {
		t.Fatal("enter produced no promote command")
	}
	cmd()
	if len(promoter.calls) != 1 {
		t.Fatalf("Promote called %d times, want 1 (the cursor's own row, not a stale range): %+v",
			len(promoter.calls), promoter.calls)
	}
}

// Done-when: roomForMain is View's own guard, asked of the pane instead of
// the frame in hand. An off-by-one either way is a key that blanks the
// screen or a window that refuses one it could draw.
func TestThePanesFloorIsTheGuardsFloor(t *testing.T) {
	const w = 70 // stacked: the pane comes out of the same body as the panels
	for _, tc := range []struct {
		h    int
		want bool
	}{
		{h: 2*panelFloor + mainFloor, want: false},
		{h: 2*panelFloor + mainFloor + 1, want: true},
	} {
		m, _, _ := newTestModel(t, 1)
		resized, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: tc.h})
		m = fillBoard(t, resized.(model), 20)
		// No setup: lerp opens on the inbox with its pane closed, which is
		// the screen this floor is measured against.
		if strings.Contains(m.View(), "too small") {
			t.Fatalf("height %d: a closed pane did not buy this window a screen", tc.h)
		}
		next := update(t, m, keyMsg("enter"))
		// Either way the screen survives: one line under the floor the key
		// is refused, one line over it the pane it opens fits.
		if strings.Contains(next.View(), "too small") {
			t.Fatalf("height %d: enter took the screen away:\n%s", tc.h, next.View())
		}
		if next.detailOpen[panelAttention] != tc.want {
			t.Fatalf("height %d: enter opened the pane = %v, want %v",
				tc.h, next.detailOpen[panelAttention], tc.want)
		}
	}
}

// Done-when: the wide layout has a height floor of its own, and the pane's
// keys have to respect it too — the picker is modal and writes to Linear.
func TestAWideButShortWindowStillRefusesThePane(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, []string{"Planning"})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 8})
	m = resized.(model)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this"},
	}}})
	m = update(t, m, keyMsg("1"))
	if !strings.Contains(m.View(), "too small") {
		t.Fatalf("8 lines is under every floor and rendered anyway:\n%s", m.View())
	}

	m = update(t, m, keyMsg("p"))
	if m.promoting {
		t.Fatal("p opened the picker behind the too-small screen")
	}
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(model)
	if cmd != nil {
		t.Fatal("enter behind the too-small screen produced a command")
	}
	if len(promoter.calls) != 0 {
		t.Fatalf("a picker nobody could see promoted: %+v", promoter.calls)
	}
}

// Done-when: a window that shrinks under the pane's floor does not leave the
// picker or the overlay live behind "window too small" — the picker is modal
// and its enter is the TUI's one write.
func TestShrinkingTheWindowClosesWhatTookThePane(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, []string{"Planning"})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this"},
	}}})
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("p"))
	m = update(t, m, keyMsg("?"))
	if !m.promoting {
		t.Fatal("the picker is not open to begin with")
	}

	resized, _ := m.Update(tea.WindowSizeMsg{Width: 70, Height: 10})
	m = resized.(model)
	if m.promoting || m.helpOn {
		t.Fatalf("the shrunk window kept the picker (%v) and the overlay (%v)",
			m.promoting, m.helpOn)
	}
	next, cmd := m.Update(keyMsg("enter"))
	if cmd != nil {
		t.Fatal("enter after the shrink still confirmed something")
	}
	_ = next
	if len(promoter.calls) != 0 {
		t.Fatalf("a picker the shrink should have closed promoted: %+v", promoter.calls)
	}
}

// Done-when: a window too short for the pane names the key that gives it
// back. That frame draws no status bar, and the pane is the operator's own
// state across a resize — so without this the only way out is a guess.
func TestTheTooSmallScreenNamesTheWayOut(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = openMain(t, m)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 70, Height: 12})
	m = resized.(model)
	view := m.View()
	if !strings.Contains(view, "too small") || !strings.Contains(view, "esc") {
		t.Fatalf("the too-small screen does not name esc:\n%s", view)
	}
	for _, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		if lipgloss.Width(line) > 70 {
			t.Fatalf("the too-small screen overflows its window: %q", line)
		}
	}
	if m = update(t, m, keyMsg("esc")); strings.Contains(m.View(), "too small") {
		t.Fatalf("esc did not give the window its screen:\n%s", m.View())
	}

	// Under every floor there is no pane to blame, so no key to offer.
	resized, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 8})
	if view := resized.(model).View(); strings.Contains(view, "esc") {
		t.Fatalf("a window under every floor still blames the pane:\n%s", view)
	}
}

// Done-when: the status bar gives up the pane's hint before it gives up the
// inbox count. 80 columns is a standard terminal, and the count is the one
// number the needs-you panel exists for.
func TestTheStatusBarKeepsTheCountOverTheHint(t *testing.T) {
	m, _, _ := newTestModel(t, 3)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("2"))
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = openMain(t, resized.(model))
	m = update(t, m, tickedMsg{})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "one"}, {Ticket: "LERP-2", Title: "two"},
	}}})
	if !strings.Contains(m.View(), "2 in the inbox") {
		t.Fatalf("the hint truncated the inbox count away:\n%s", m.View())
	}

	// Wide enough for both, and then the hint is there.
	resized, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	view := resized.(model).View()
	if !strings.Contains(view, "2 in the inbox") || !strings.Contains(view, "esc close") {
		t.Fatalf("120 columns should carry the count and the hint:\n%s", view)
	}
}

// Done-when: the panel line stops offering p where the picker has no room to
// open — an advertised key that does nothing is how the operator finds out.
func TestAPanelWithNoRoomForThePickerDoesNotOfferIt(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m = resized.(model)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this"},
	}}})
	m = update(t, m, keyMsg("1"))
	if !strings.Contains(m.View(), "p promote") {
		t.Fatalf("a window with room does not offer promote:\n%s", m.View())
	}

	resized, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 12})
	m = resized.(model)
	m = update(t, m, keyMsg("esc"))
	view := m.View()
	if strings.Contains(view, "too small") {
		t.Fatalf("this window was supposed to have a screen:\n%s", view)
	}
	if strings.Contains(view, "p promote") {
		t.Fatalf("the panel offers a key this window has no room for:\n%s", view)
	}
	if !strings.Contains(view, "s sort") {
		t.Fatalf("the panel dropped the keys that still work:\n%s", view)
	}
}

// Done-when: the ? overlay is modal the way the picker is. esc closes it
// rather than flipping the pane behind it, and enter neither opens a pane
// nobody asked for nor reads a ticket nobody can see.
func TestTheOverlayTakesEscAndEnter(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("?"))

	next, cmd := updateCmd(t, m, keyMsg("enter"))
	if next.detailOpen[panelAttention] || cmd != nil {
		t.Fatal("enter under the overlay opened the pane behind it")
	}
	// Behind the overlay the pane has no key to offer: enter is inert and esc
	// is the overlay's.
	if view := m.View(); strings.Contains(view, "enter detail") || strings.Contains(view, "esc close") {
		t.Fatalf("the bar offers the pane's keys from behind the overlay:\n%s", view)
	}
	m = update(t, m, keyMsg("esc"))
	if m.helpOn {
		t.Fatalf("esc did not close the overlay:\n%s", m.View())
	}
	if m.detailOpen[panelAttention] {
		t.Fatal("esc flipped the pane instead of closing the overlay")
	}
	if got := reader.fetched(); len(got) != 0 {
		t.Fatalf("the overlay read %v", got)
	}
}

// Done-when: a read scheduled just before esc does not outlive the pane. The
// debounce is a quarter of a second, which is time enough to shut it.
func TestADebounceDoesNotOutliveThePane(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("j"))
	id := m.shown[m.attnSel].TicketID

	m = update(t, m, keyMsg("esc"))
	m, cmd := updateCmd(t, m, detailDueMsg{ticketID: id})
	if cmd != nil {
		t.Fatalf("the debounce for %s fired into a closed pane", id)
	}
	if got := reader.fetched(); len(got) != 0 {
		t.Fatalf("a closed pane read %v", got)
	}

	// And the row is not spent: reopening on it asks again. A dropped read
	// that left the pane still pointed at the ticket would refuse to
	// schedule a second one, and the row would stay blank however often the
	// operator opened it.
	m, cmd = updateCmd(t, m, keyMsg("enter"))
	if cmd == nil {
		t.Fatalf("reopening on %s scheduled nothing", id)
	}
	reader.returns(linear.IssueDetail{Body: "read on the second try"}, nil)
	m, cmd = updateCmd(t, m, detailDueMsg{ticketID: id})
	if cmd == nil {
		t.Fatalf("the reopened row's debounce fired no fetch for %s", id)
	}
	m = update(t, m, cmd())
	if !strings.Contains(m.View(), "read on the second try") {
		t.Fatalf("the reopened row never read its ticket:\n%s", m.View())
	}
}

// Done-when: a closed pane costs nothing on the 250ms poll — no wrapping a
// log tail to a zero-width viewport twenty times a minute.
func TestAClosedPaneIsNotRefreshed(t *testing.T) {
	log := filepath.Join(t.TempDir(), "one.log")
	writeLog(t, log, []byte("agent one says hello\n"))

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("2"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "plan", LogPath: log}})
	m = update(t, m, keyMsg("esc"))
	held := m.vp.View()

	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("and more\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m = update(t, m, pollMsg{})
	if m.vp.View() != held {
		t.Fatalf("the poll refreshed a pane nobody is looking at:\n%s", m.vp.View())
	}

	// And opening it is current all the same.
	m = update(t, m, keyMsg("enter"))
	if !strings.Contains(m.View(), "and more") {
		t.Fatalf("the reopened pane missed what arrived while it was shut:\n%s", m.View())
	}

	// The inbox lens is the same bargain, and refreshMain runs on focus, on s
	// and on P as well as on the poll — against a closed pane that is prose
	// wrapped to a zero-width viewport. A closed pane is not re-rendered at
	// all, so it is still holding the log when none of those have touched it.
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("esc"))
	const logText = "agent one says hello\nand more"
	for _, k := range []string{"1", "s", "P"} {
		m = update(t, m, keyMsg(k))
		shown := strings.TrimSpace(ansi.Strip(m.vp.View()))
		if shown == "" || !strings.Contains(logText, shown) {
			t.Fatalf("%q re-rendered a closed pane: %q", k, shown)
		}
	}
}

func TestPanelPaddingIsAsymmetric(t *testing.T) {
	row := func(pad padding) string {
		return strings.Split(panelBox("t", false, 10, 3, []string{"abcdefg"}, pad, nil), "\n")[1]
	}
	if got, want := row(padList), "│ abcdefg│"; got != want {
		t.Fatalf("list row is %q, want %q — a left pad and the right edge", got, want)
	}
	if got, want := row(padMain), "│ abcde… │"; got != want {
		t.Fatalf("main row is %q, want %q — a space inside both borders", got, want)
	}
}

// Focus is weight as well as colour: the panel with focus draws the heavy
// box, so which panel the keys are talking to still reads on a terminal
// that gives the accent back as plain text. (The promote picker and the ?
// overlay draw it too — they are lenses that have taken the keyboard.)
func TestFocusDrawsTheHeavyBox(t *testing.T) {
	if idle := panelBox("t", false, 10, 3, nil, padList, nil); !strings.Contains(idle, "╭") ||
		!strings.Contains(idle, "│") {
		t.Fatalf("an unfocused panel is not the light box:\n%s", idle)
	}
	if on := panelBox("t", true, 10, 3, nil, padList, nil); !strings.Contains(on, "┏") ||
		!strings.Contains(on, "┃") {
		t.Fatalf("a focused panel is not the heavy box:\n%s", on)
	}
}

// And the weight follows focus, both ways, in the view the operator sees.
func TestTheHeavyBoxFollowsFocus(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	for _, tc := range []struct{ key, focused, idle string }{
		{"1", "[1] inbox", "[2] work"},
		{"2", "[2] work", "[1] inbox"},
	} {
		m = update(t, m, keyMsg(tc.key))
		view := m.View()
		if got := lineWith(t, view, tc.focused); !strings.HasPrefix(got, "┏") {
			t.Fatalf("%q has focus but not the heavy box: %q", tc.focused, got)
		}
		if got := lineWith(t, view, tc.idle); !strings.HasPrefix(got, "╭") {
			t.Fatalf("%q has no focus but the heavy box: %q", tc.idle, got)
		}
	}
}

// Done-when: the keys a panel's own selection answers to are on that panel,
// so sort and project are visible without picking a row first — which is
// the whole reason they felt like they did not exist.
func TestFocusedPanelCarriesItsKeys(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = openMain(t, update(t, m, keyMsg("1"))) // the 45-column panel below
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "id-9", Identifier: "LERP-9", Title: "queued", Eligible: true,
				URL: "https://linear.app/acme/issue/LERP-9"},
		}}}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-9", Ticket: "LERP-9", Queue: "implement", LogPath: "/dev/null"}})

	view := m.View()
	for _, want := range []string{"p promote", "v select", "/ search", "o open"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the needs-you panel does not offer %q:\n%s", want, view)
		}
	}
	// In the 45-column panel a 100-column terminal leaves, the list is one
	// key over budget now v is in the mix: nothing here is in a project, so
	// P was already dead weight, and s — a display cycle, the tier project
	// is in — is what gives an action key the room.
	if strings.Contains(view, "P project") {
		t.Fatalf("P is offered over a list with no project to cycle to:\n%s", view)
	}
	if strings.Contains(view, "s sort") {
		t.Fatalf("sort is offered when the line has no room left for it:\n%s", view)
	}
	if line := lineWith(t, view, "p promote"); !strings.Contains(line, "…") {
		t.Fatalf("a key line one key over budget should say so:\n%s", line)
	}

	// The line belongs to the focused panel, so it moves with focus rather
	// than sitting on both. Work's own key acts on the pane, so the pane is
	// open for this.
	m = openMain(t, update(t, m, keyMsg("2")))
	view = m.View()
	if strings.Contains(view, "p promote") {
		t.Fatalf("the needs-you keys are still on screen with work focused:\n%s", view)
	}
	if !strings.Contains(view, "r raw") {
		t.Fatalf("the work panel does not offer its own keys:\n%s", view)
	}
}

// Done-when: what a narrow line loses is a key the operator can do without.
// Everything a real board has — a project to cycle to and a URL to open —
// is more than the panel a 100-column terminal leaves can carry, so the
// order decides: what acts on the row under the cursor stays, and the
// display cycles, whose state the title already carries in words, go first.
func TestTheKeyLineKeepsTheKeysThatAct(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: fullBoard()})
	// With the detail pane open, which is the narrower of the two panels a
	// 100-column terminal leaves — and the one this line has to fit in.
	m = update(t, m, keyMsg("enter"))

	line := lineWith(t, m.View(), "p promote")
	for _, want := range []string{"p promote", "v select", "/ search", "o open"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the key line dropped %q, which acts on the row:\n%s", want, line)
		}
	}
	// Two keys short of the room now v is in the mix, and both are display
	// cycles — sort and project — with the ellipsis to say the ? overlay
	// has the rest.
	if strings.Contains(line, "s sort") || strings.Contains(line, "P project") {
		t.Fatalf("the whole line fits after all — this window should be two keys short:\n%s", line)
	}
	if !strings.Contains(line, "…") {
		t.Fatalf("a line with a key left out does not say so:\n%s", line)
	}

	// Given the room, both keys come back.
	wide := update(t, m, tea.WindowSizeMsg{Width: 150, Height: 30})
	line = lineWith(t, wide.View(), "p promote")
	for _, want := range []string{"s sort", "P project"} {
		if !strings.Contains(line, want) {
			t.Fatalf("a wide panel still drops %q:\n%s", want, line)
		}
	}
}

// fullBoard is board() with the URL every real attention item carries, so
// the key line has every hint a row can earn.
func fullBoard() loop.Event {
	ev := board()
	for i := range ev.Attention {
		ev.Attention[i].URL = "https://linear.app/acme/issue/" + ev.Attention[i].Ticket
	}
	return ev
}

// A key on the panel is a key you can press. With nothing under the cursor
// every one of them is dead — p is gated on there being a row, o has no URL
// to open, s and P reorder and filter nothing — so the line is not drawn.
// This is the first frame a new operator sees.
func TestAPanelWithNothingSelectedOffersNoKeys(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	for _, key := range []string{"1", "2"} {
		m = update(t, m, keyMsg(key))
		for _, dead := range []string{"p promote", "s sort", "P project", "r raw", "o open"} {
			if view := m.View(); strings.Contains(view, dead) {
				t.Fatalf("panel %s offers %q with nothing under the cursor:\n%s", key, dead, view)
			}
		}
	}
}

// r is inert on a ticket that has never run, and inert while the pane it
// acts on is closed — which is the screen work now starts on. Pressing it in
// either place would flip the decoding of a log nobody is reading, and the
// operator would meet the change the next time they opened the pane. So the
// panel offers it exactly where it does something.
func TestRawIsOfferedOnlyWhereThereIsALog(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = openMain(t, update(t, m, keyMsg("2")))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "id-9", Identifier: "LERP-9", Title: "queued", Eligible: true,
				URL: "https://linear.app/acme/issue/LERP-9"},
		}}}}})

	view := m.View()
	if strings.Contains(view, "r raw") {
		t.Fatalf("a ticket that has never run offers the raw toggle:\n%s", view)
	}
	if !strings.Contains(view, "o open") {
		t.Fatalf("the row has a URL but the panel does not offer o:\n%s", view)
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-9", Ticket: "LERP-9", Queue: "implement", LogPath: "/dev/null"}})
	if view := m.View(); !strings.Contains(view, "r raw") {
		t.Fatalf("a running ticket with a log does not offer the raw toggle:\n%s", view)
	}

	// The ? overlay is in that pane and covers the log, so the same row
	// stops offering the key while it is up — the pane being open is not
	// the question, a log being on screen is.
	m = update(t, m, keyMsg("?"))
	if view := m.View(); strings.Contains(view, "r raw") {
		t.Fatalf("the overlay covers the log and the panel still offers the toggle:\n%s", view)
	}
	if next := update(t, m, keyMsg("r")); next.rawLog {
		t.Fatal("r flipped the decoding of a log the overlay was covering")
	}
	m = update(t, m, keyMsg("?"))

	// Close the pane — the startup screen — and the same row stops offering
	// it, because there is nothing on screen for it to change.
	m = update(t, m, keyMsg("esc"))
	if view := m.View(); strings.Contains(view, "r raw") {
		t.Fatalf("a closed pane still offers the key that acts on it:\n%s", view)
	}
	if m = update(t, m, keyMsg("r")); m.rawLog {
		t.Fatal("r flipped the decoding of a log the closed pane was not showing")
	}
}

// A panel squeezed too short drops the hints and keeps its rows: windowRows
// needs two lines to keep the selection visible, so a panel that spent one
// of them on the key line would show "⋯ n more" and nothing else — losing
// the cursor that j/k move and p promotes. One row further up it can afford
// both, and does. The inbox spends a line on its column header before any
// of this, so the height it can afford both at is one higher than the two
// lines windowRows needs.
func TestAShortPanelKeepsItsRowsOverItsKeys(t *testing.T) {
	// The window height a panel's three rows fall out of is the layout's
	// business, not this test's: sweep the short end and assert the rule
	// against the height needs-you actually got.
	seen := map[bool]bool{}
	for h := 8; h <= 14; h++ {
		m, _, _ := newTestModel(t, 1)
		resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: h})
		m = update(t, resized.(model), keyMsg("1"))
		var items []loop.AttentionItem
		for i := 1; i <= 8; i++ {
			items = append(items, loop.AttentionItem{
				Ticket: fmt.Sprintf("LERP-%d", i), Title: "waiting", Reason: "unclaimed"})
		}
		m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: items}})

		view := m.View()
		inner := m.geometry().attnH - 2
		if inner < 2 || inner > 4 || strings.HasPrefix(view, "lerp — window too small") {
			continue // not the squeeze this test is about
		}
		want := inner >= 4
		seen[want] = true
		if !strings.Contains(view, "LERP-1 ") {
			t.Fatalf("a %d-row panel dropped the selected row to make room:\n%s", inner, view)
		}
		if got := strings.Contains(view, "p promote"); got != want {
			t.Fatalf("%d rows: key line on screen = %v, want %v:\n%s", inner, got, want, view)
		}
	}
	if !seen[true] || !seen[false] {
		t.Fatalf("no window in the sweep produced both cases: %v", seen)
	}
}

// One ticket waiting while the queues are full is an ordinary state, and it
// gives the focused panel exactly three rows: the column header, the ticket,
// and the key line. They are enough: the row fits without windowing, so the
// key line — the whole point of putting it on the panel — is still there.
func TestOneRowStillGetsItsKeys(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention,
		Attention: []loop.AttentionItem{{Ticket: "LERP-1", Title: "waiting", Reason: "unclaimed"}}}})
	tickets := make([]loop.QueueTicket, 30)
	for i := range tickets {
		tickets[i] = loop.QueueTicket{ID: fmt.Sprintf("t%d", i),
			Identifier: fmt.Sprintf("QUEUED-%d", i+1), Title: "work", Eligible: true}
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: tickets},
	}}})

	view := m.View()
	if g := m.geometry(); g.attnH != 5 {
		t.Fatalf("needs-you is %d lines, want the 5 this case is about:\n%s", g.attnH, view)
	}
	if !strings.Contains(view, "LERP-1 ") {
		t.Fatalf("the one waiting ticket is not on screen:\n%s", view)
	}
	if !strings.Contains(view, "p promote") {
		t.Fatalf("a panel with room for the row and the keys drew only the row:\n%s", view)
	}
}

// Done-when for the line panelWant buys: a focused panel asks for one line
// more than its rows, so the key line comes out of the layout's arithmetic
// rather than out of the list. The panel cannot take the whole column to pay
// for it — its share is its share — but the line is reserved wherever the
// share has room, and the selected row survives either way.
func TestTheFocusedPanelBuysTheLineItsKeysCost(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 17})
	m = update(t, resized.(model), keyMsg("1"))
	m = fillBoard(t, m, 10)

	rows, _ := m.attentionRows(padList.inner(m.geometry().sideW))
	if !m.keyHints(panelAttention) {
		t.Fatal("the focused panel offers no keys, so there is no line to buy")
	}
	if got, bare := m.panelWant(panelAttention, len(rows)), len(rows)+2; got != bare+1 {
		t.Fatalf("focused want = %d lines for %d rows, want %d", got, len(rows), bare+1)
	}
	m2 := m
	m2.focus = panelWork
	if got, bare := m2.panelWant(panelAttention, len(rows)), len(rows)+2; got != bare {
		t.Fatalf("unfocused want = %d lines for %d rows, want %d", got, len(rows), bare)
	}
	view := m.View()
	if !strings.Contains(view, "LERP-1 ") {
		t.Fatalf("the key line cost needs-you its selected row:\n%s", view)
	}
	if !strings.Contains(view, "p promote") {
		t.Fatalf("needs-you has its rows but not its keys:\n%s", view)
	}
}

// The promote picker owns the keyboard while it is open, so the panel stops
// offering the keys it would swallow. The status bar carries the picker's.
func TestThePickerTakesTheKeyLineWithIt(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	if view := m.View(); !strings.Contains(view, "p promote") {
		t.Fatalf("the needs-you panel is not offering its keys:\n%s", view)
	}

	m = update(t, m, keyMsg("p"))
	view := m.View()
	if strings.Contains(view, "p promote") {
		t.Fatalf("the panel still offers keys the picker swallows:\n%s", view)
	}
	if !strings.Contains(view, "esc cancel") {
		t.Fatalf("the status bar lost the picker's own keys:\n%s", view)
	}
}

// lineWith is the one line of a rendered view containing want.
func lineWith(t *testing.T, view, want string) string {
	t.Helper()
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, want) {
			return l
		}
	}
	t.Fatalf("no line of the view contains %q:\n%s", want, view)
	return ""
}

// A quiet work panel is still a panel: empty, unfocused, or both, it keeps
// its border and enough rows to read as a box rather than a stray line
// above the status bar.
func TestQuietWorkPanelKeepsItsBox(t *testing.T) {
	m, _, _ := newTestModel(t, 3)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = resized.(model)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "plan", Status: "Planning"},
	}}})

	// Both panes open, so focus is the only thing that moves between the two
	// geometries below — the pane's own default would swamp what is under test.
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	g := m.geometry()
	if rows, _ := m.workListRows(padList.inner(g.sideW)); len(rows)+2 >= panelFloor {
		t.Fatalf("this board asks for %d lines: the floor is not what is under test",
			len(rows)+2)
	}
	if g.workH != panelFloor {
		t.Fatalf("a work panel with one empty queue is %d lines, want the floor %d",
			g.workH, panelFloor)
	}
	view := m.View()
	if !strings.Contains(view, "plan · Planning · LERP · empty") {
		t.Fatalf("empty work panel does not show its queues:\n%s", view)
	}
	if strings.Count(view, "╭")+strings.Count(view, "┏") != 3 {
		t.Fatalf("a panel lost its border with nothing to show:\n%s", view)
	}
	m = update(t, m, keyMsg("2"))
	if g2 := m.geometry(); g2.workH != g.workH {
		t.Fatalf("focusing the empty work panel resized it: %d then %d lines",
			g.workH, g2.workH)
	}
}

// The third is a ceiling, not a reservation: a full work list beside a
// nearly empty needs-you takes the room needs-you has nothing to put in,
// rather than truncating into a column of blank lines.
func TestWorkTakesTheRoomNeedsYouCannotUse(t *testing.T) {
	m, _, _ := newTestModel(t, 6)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = resized.(model)
	m = fillBoard(t, m, 20)
	// Two items waiting, twenty tickets queued: needs-you cannot fill two
	// thirds of this column and work has more list than a third holds.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "one", Status: "Backlog"},
		{Ticket: "LERP-2", Title: "two", Status: "Backlog"},
	}}})

	g := m.geometry()
	attn, _ := m.attentionRows(g.sideW - 2)
	work, _ := m.workListRows(g.sideW - 2)
	if g.workH <= g.bodyH/3 {
		t.Fatalf("work is %d lines of a %d-line body while needs-you held blanks",
			g.workH, g.bodyH)
	}
	if g.workH != len(work)+2 {
		t.Fatalf("work is %d lines for the %d rows it renders: it truncated anyway",
			g.workH, len(work))
	}
	if g.attnH < len(attn)+2 {
		t.Fatalf("needs-you is %d lines for the %d rows it renders", g.attnH, len(attn))
	}
	if strings.Contains(m.View(), "more") {
		t.Fatalf("a panel cut its list with the other holding blank lines:\n%s", m.View())
	}
	if got := g.attnH + g.workH; got != g.bodyH {
		t.Fatalf("stack is %d lines in a %d-line body", got, g.bodyH)
	}
}

// Work is capped at about a third of the column however long its list is,
// so a full board still leaves needs-you the other two thirds. The overflow
// is the focus window's job: the selection stays on screen as it walks past
// the bottom of the capped panel.
func TestWorkIsCappedAndScrollsUnderTheCap(t *testing.T) {
	m, _, _ := newTestModel(t, 6)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = resized.(model)
	m = fillBoard(t, m, 40)

	m = update(t, m, keyMsg("2"))
	g := m.geometry()
	if got := g.attnH + g.workH; got != g.bodyH {
		t.Fatalf("overflowing stack is %d lines in a %d-line body", got, g.bodyH)
	}
	if rows, _ := m.workListRows(g.sideW - 2); g.workH >= len(rows)+2 {
		t.Fatalf("work is %d lines for %d rows: the cap did not bite",
			g.workH, len(rows))
	}
	if g.workH > (g.bodyH+2)/3 {
		t.Fatalf("work is %d lines of a %d-line body, past its third",
			g.workH, g.bodyH)
	}
	if g.attnH < 2*g.workH {
		t.Fatalf("needs-you is %d lines against work's %d: not the larger panel",
			g.attnH, g.workH)
	}
	if lines := strings.Count(m.View(), "\n") + 1; lines > 40 {
		t.Fatalf("view is %d lines tall in a 40-line window", lines)
	}

	// Walking the selection down a list longer than the cap keeps it on
	// screen: the panel scrolls around it rather than growing.
	for i := 0; i < 30; i++ {
		m = update(t, m, keyMsg("down"))
	}
	if g2 := m.geometry(); g2.workH != g.workH {
		t.Fatalf("work grew as the selection walked: %d then %d lines", g.workH, g2.workH)
	}
	rows, cur := m.workListRows(g.sideW - 2)
	if cur.at < 0 {
		t.Fatal("work has no selection to keep on screen")
	}
	want := strings.TrimRight(ansi.Strip(rows[cur.at]), " ")
	if !strings.Contains(ansi.Strip(m.View()), want) {
		t.Fatalf("the selected row walked off the capped panel:\n%s", m.View())
	}
}

// A panel at its floor is still a panel that can be read: at the smallest
// window each layout admits, the focused work panel renders the row the
// selection is on rather than spending its only line on "⋯ n more".
func TestFlooredPanelStillShowsTheSelection(t *testing.T) {
	for _, tc := range []struct{ w, h int }{
		{120, 2*panelFloor + 1},
		{70, 2*panelFloor + mainFloor + 1},
	} {
		m, _, _ := newTestModel(t, 3)
		resized, _ := m.Update(tea.WindowSizeMsg{Width: tc.w, Height: tc.h})
		m = fillBoard(t, resized.(model), 40)
		m = update(t, m, keyMsg("2"))
		for i := 0; i < 10; i++ {
			m = update(t, m, keyMsg("down"))
		}
		g := m.geometry()
		if g.workH < panelFloor {
			t.Fatalf("%dx%d: work is %d lines, under the floor", tc.w, tc.h, g.workH)
		}
		rows, cur := m.workListRows(g.sideW - 2)
		if cur.at < 0 {
			t.Fatalf("%dx%d: work has no selection to show", tc.w, tc.h)
		}
		want := strings.TrimRight(ansi.Strip(rows[cur.at]), " ")
		if !strings.Contains(ansi.Strip(m.View()), want) {
			t.Fatalf("%dx%d: the selected row is not on screen:\n%s", tc.w, tc.h, m.View())
		}
	}
}

// Stacked, the main pane is one more claimant on the same body — but it
// never takes so much that the panels fall to their floors, and the log lens
// (which wants the whole body) does not resize the board under the operator
// when focus moves onto it.
func TestStackedLayoutKeepsBothPanelsReadable(t *testing.T) {
	m, _, _ := newTestModel(t, 3)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m = resized.(model)
	m = fillBoard(t, m, 20)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "t0", Ticket: "QUEUED-1", Queue: "implement", LogPath: "/dev/null"}})

	// Both panes open: this is about focus alone, not about the pane's
	// per-panel state.
	m = update(t, m, keyMsg("2"))
	m = openMain(t, m) // work
	m = update(t, m, keyMsg("1"))
	m = openMain(t, m) // and the inbox
	g := m.geometry()
	if g.wide {
		t.Fatal("80 columns is not the stacked layout")
	}
	if got := g.attnH + g.workH + g.mainH; got != g.bodyH {
		t.Fatalf("stacked layout is %d lines in a %d-line body", got, g.bodyH)
	}

	// Focus work: the selected row is running, so the main pane opens a log
	// tail, which asks for the whole body. It gets its half and no more —
	// the panels are exactly where the operator left them.
	m = update(t, m, keyMsg("2"))
	g2 := m.geometry()
	if g2 != g {
		t.Fatalf("focus moved the stacked layout: %+v then %+v", g, g2)
	}
	if g2.attnH+g2.workH < g.bodyH/2 {
		t.Fatalf("the board is %d lines of a %d-line body, under its half",
			g2.attnH+g2.workH, g2.bodyH)
	}
	if g2.attnH <= panelFloor || g2.workH <= panelFloor {
		t.Fatalf("focusing work floored the panels: inbox %d, work %d",
			g2.attnH, g2.workH)
	}
	view := m.View()
	if !strings.Contains(view, "QUEUED-1") {
		t.Fatalf("work panel lost its list to the log lens:\n%s", view)
	}
	attn, _ := m.attentionRows(padList.inner(g2.sideW))
	first := strings.TrimRight(ansi.Strip(attn[0]), " ")
	if !strings.Contains(ansi.Strip(view), first) {
		t.Fatalf("inbox lost its list to the log lens:\n%s", view)
	}
}

// Wide, the main pane has its column to itself, so it fills the body rather
// than floating at content height: a one-line ticket, a ticket longer than
// the screen and a running row's log tail all draw the same box, ending on
// the last line above the status bar. A pane that changed size with its
// contents read as a glitch rather than as a rule.
func TestWideMainPaneFillsTheBody(t *testing.T) {
	m, _, _ := newTestModel(t, 3)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = resized.(model)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "short", Status: "Backlog", Reason: "waiting"},
		{Ticket: "LERP-2", Title: "long", Status: "Backlog",
			Reason: strings.Repeat("a reason that runs on and on and on. ", 60)},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "t0", Ticket: "QUEUED-1", Queue: "implement", LogPath: "/dev/null"}})

	m = update(t, m, keyMsg("1"))
	// The inbox opens its detail pane on enter; the body it then has to
	// fill is the whole of the main column either way.
	m = update(t, m, keyMsg("enter"))
	if !m.geometry().wide {
		t.Fatal("140 columns is not the wide layout")
	}
	// The short ticket is the case that used to leave dead space: its detail
	// is a handful of lines in a 39-line body.
	text, _, _ := m.detail(padMain.inner(m.geometry().mainW))
	if lines := strings.Count(text, "\n") + 1; lines > 10 {
		t.Fatalf("the short ticket draws %d lines: it is not short enough to test with", lines)
	}
	for _, tc := range []struct {
		name string
		keys []string
	}{
		{"a short ticket", nil},
		{"a long ticket", []string{"down"}},
		{"a running row's log", []string{"2", "enter"}},
		{"the help overlay", []string{"?"}},
	} {
		for _, k := range tc.keys {
			m = update(t, m, keyMsg(k))
		}
		g := m.geometry()
		if g.mainH != g.bodyH {
			t.Fatalf("%s: the main pane is %d lines of a %d-line body",
				tc.name, g.mainH, g.bodyH)
		}
		body := strings.Split(ansi.Strip(m.View()), "\n")[:g.bodyH]
		if n := strings.Count(body[g.bodyH-1], "\u2570") + strings.Count(body[g.bodyH-1], "\u2517"); n != 2 {
			t.Fatalf("%s: %d panels end on the body's last line, want the side column and the main pane:\n%s",
				tc.name, n, m.View())
		}
	}
}

// The too-small guard is geometry's own arithmetic: at the smallest window
// each layout admits, the stack still fits the terminal, and one line less
// is refused rather than drawn over the status bar.
func TestSmallestWindowTheGuardAdmits(t *testing.T) {
	for _, tc := range []struct {
		w, h   int
		closed bool
	}{
		{w: 120, h: 2*panelFloor + 1},
		{w: 70, h: 2*panelFloor + mainFloor + 1},
		// A closed pane needs no floor, so the stacked layout admits a
		// window mainFloor lines shorter than it otherwise would: a short
		// terminal that is refused today gets a usable screen.
		{w: 70, h: 2*panelFloor + 1, closed: true},
	} {
		m, _, _ := newTestModel(t, 3)
		m = update(t, m, keyMsg("2"))
		resized, _ := m.Update(tea.WindowSizeMsg{Width: tc.w, Height: tc.h})
		m = fillBoard(t, resized.(model), 20)
		// Both panels start with the pane closed, which is the closed case
		// as it stands; the open ones press for it.
		if !tc.closed {
			m = openMain(t, m)
		}
		view := m.View()
		if strings.Contains(view, "too small") {
			t.Fatalf("width %d closed=%v: the guard refuses the height it admits",
				tc.w, tc.closed)
		}
		if lines := strings.Count(view, "\n") + 1; lines > tc.h {
			t.Fatalf("width %d closed=%v: view is %d lines tall in a %d-line window:\n%s",
				tc.w, tc.closed, lines, tc.h, view)
		}
		next, _ := m.Update(tea.WindowSizeMsg{Width: tc.w, Height: tc.h - 1})
		if view := next.(model).View(); !strings.Contains(view, "too small") {
			t.Fatalf("width %d closed=%v: a window below the floors rendered anyway:\n%s",
				tc.w, tc.closed, view)
		}
	}
}

// No rendered line may overflow the terminal, wide layout or stacked, from
// whichever panel has focus — the long explanatory empty states included.
func TestViewFitsTheWindow(t *testing.T) {
	for _, width := range []int{120, 100, 80, 60} {
		for _, focus := range []string{"1", "2"} {
			for _, pane := range []string{"esc", "enter"} {
				m, _, _ := newTestModel(t, 3)
				resized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
				m = resized.(model)
				m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
					TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})
				m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
				m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
					{Team: "LERP", Name: "review", Status: "A Status With A Very Long Name"},
				}}})
				m = update(t, m, keyMsg(focus))
				m = update(t, m, keyMsg(pane))
				for i, line := range strings.Split(m.View(), "\n") {
					if got := lipgloss.Width(line); got > width {
						t.Fatalf("width %d, panel %s, pane %s: line %d is %d cells wide:\n%s",
							width, focus, pane, i, got, line)
					}
				}
			}
		}
	}
}

// o without a selected URL is a no-op, not a crash or a stray command.
func TestOpenWithNothingSelected(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	if _, cmd := m.Update(keyMsg("o")); cmd != nil {
		t.Fatal("o with no URL produced a command")
	}
}

// A refused URL is not a silent no-op: the panel stops offering o on that
// row, and pressing it anyway says why on the status bar rather than
// looking like a key that is broken.
func TestARefusedURLIsSaidOutLoudAndNotAdvertised(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-7", TicketID: "id-7", Title: "waiting", Status: "Plan Review",
			URL: "file:///etc/passwd"},
	}}})
	if view := m.View(); strings.Contains(view, "o open") {
		t.Fatalf("the panel offers o for a URL the opener will refuse:\n%s", view)
	}

	m, cmd := updateCmd(t, m, keyMsg("o"))
	if cmd == nil {
		t.Fatal("o on a refused URL produced no command, so nothing tells the operator why")
	}
	msg, ok := cmd().(openErrMsg)
	if !ok {
		t.Fatalf("o on a refused URL produced %T, want an openErrMsg", cmd())
	}
	m = update(t, m, msg)
	if view := m.View(); !strings.Contains(view, "refusing to open") {
		t.Fatalf("the status bar does not carry the refusal:\n%s", view)
	}
}

func TestQuitKey(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	_, cmd := m.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatal("q produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}
}

func TestTickChainDrivesTheLoop(t *testing.T) {
	m, ticker, _ := newTestModel(t, 1)

	// One pass: the tick command calls the loop and reports back.
	msg := m.runTick()()
	if _, ok := msg.(tickedMsg); !ok {
		t.Fatalf("runTick yielded %T, want tickedMsg", msg)
	}
	if ticker.ticks.Load() != 1 {
		t.Fatalf("loop ticked %d times, want 1", ticker.ticks.Load())
	}

	// A finished pass schedules the next; the timer yields tickMsg, and
	// handling it runs the loop again. The chain is the engine.
	_, timer := m.Update(tickedMsg{})
	if timer == nil {
		t.Fatal("tickedMsg scheduled nothing")
	}
	if _, ok := timer().(tickMsg); !ok {
		t.Fatal("interval timer did not yield tickMsg")
	}
	_, next := m.Update(tickMsg{})
	if next == nil {
		t.Fatal("tickMsg started no pass")
	}
	next()
	if ticker.ticks.Load() != 2 {
		t.Fatalf("loop ticked %d times, want 2", ticker.ticks.Load())
	}
}

func TestEventSubscription(t *testing.T) {
	m, _, events := newTestModel(t, 1)
	events <- loop.Event{Type: loop.EventStarted, Lane: 1, Ticket: "LERP-7", Queue: "plan"}
	msg := m.waitEvent()()
	ev, ok := msg.(eventMsg)
	if !ok {
		t.Fatalf("waitEvent yielded %T, want eventMsg", msg)
	}
	m = update(t, m, ev)
	if !strings.Contains(m.View(), "LERP-7") {
		t.Fatal("subscribed event did not reach the board")
	}
}

func TestSelectingARunningTicketTailsItsLog(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one.log")
	two := filepath.Join(dir, "two.log")
	writeLog(t, one, []byte("agent one says hello\n"))
	writeLog(t, two, []byte("agent two says hello\n"))

	m, _, _ := newTestModel(t, 2)
	m = update(t, m, keyMsg("2"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "plan", LogPath: one}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r2", Lane: 2,
		TicketID: "id-2", Ticket: "LERP-2", Queue: "plan", LogPath: two}})
	m = openMain(t, m)

	if !strings.Contains(m.View(), "agent one says hello") {
		t.Fatalf("the selected ticket's log is not tailed:\n%s", m.View())
	}

	m = update(t, m, keyMsg("down"))
	if !strings.Contains(m.View(), "agent two says hello") {
		t.Fatalf("selecting the second running ticket did not switch the tail:\n%s", m.View())
	}

	// Live tail: appended output arrives on the next poll.
	f, err := os.OpenFile(two, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("and more\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m = update(t, m, pollMsg{})
	if !strings.Contains(m.View(), "and more") {
		t.Fatalf("appended log output did not reach the pane:\n%s", m.View())
	}
}

// A stream-json runner's log reads as what the agent is doing, and `r`
// flips to the bytes it actually wrote — the escape hatch for a formatter
// that got something wrong.
func TestRunLogRendersActivityAndTogglesToRaw(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	writeLog(t, path, []byte(claudeStream))

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("2"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		Ticket: "LERP-1", Queue: "plan", LogPath: path}})
	m = openMain(t, m)

	view := m.View()
	if !strings.Contains(view, "⏺ Read model.go") {
		t.Fatalf("the pane does not read as agent activity:\n%s", view)
	}
	if strings.Contains(view, `{"type"`) {
		t.Fatalf("raw stream JSON is on screen:\n%s", view)
	}

	m = update(t, m, keyMsg("r"))
	view = m.View()
	if !strings.Contains(view, `{"type":"system"`) {
		t.Fatalf("the raw toggle did not show the runner's own bytes:\n%s", view)
	}
	if !strings.Contains(view, "(raw)") {
		t.Fatalf("the raw pane does not say so in its title:\n%s", view)
	}

	m = update(t, m, keyMsg("r"))
	view = m.View()
	if !strings.Contains(view, "⏺ Read model.go") || strings.Contains(view, `{"type"`) {
		t.Fatalf("the raw toggle did not round-trip:\n%s", view)
	}
}

// The ? overlay renders from the keymap, so a binding declared there is
// documented for free — and a key nobody can discover may as well not exist.
func TestRawToggleIsInTheHelpOverlay(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("?"))
	if !strings.Contains(m.View(), "raw log") {
		t.Fatalf("the raw toggle is missing from the help overlay:\n%s", m.View())
	}
}

// The log keeps tailing while the operator looks elsewhere: appended output
// gathered during an inbox detour is on screen the moment the running
// panel regains focus.
func TestLogSurvivesAFocusDetour(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one.log")
	writeLog(t, one, []byte("first line\n"))

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		Ticket: "LERP-1", Queue: "plan", LogPath: one}})
	m = update(t, m, keyMsg("2"))
	m = openMain(t, m)
	m = update(t, m, keyMsg("1"))
	if strings.Contains(m.View(), "first line") {
		t.Fatalf("inbox lens still shows the log:\n%s", m.View())
	}

	f, err := os.OpenFile(one, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("written during the detour\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m = update(t, m, pollMsg{})
	m = update(t, m, keyMsg("2"))
	if !strings.Contains(m.View(), "written during the detour") {
		t.Fatalf("log output gathered off-focus is missing:\n%s", m.View())
	}
}

func TestErrorsSurfaceOnTheStatusBar(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError, Err: errors.New("linear is down")}})
	if !strings.Contains(m.View(), "linear is down") {
		t.Fatalf("loop error not surfaced:\n%s", m.View())
	}

	// A pass that itself errors keeps the line.
	m = update(t, m, tickMsg{})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError, Err: errors.New("still down")}})
	m = update(t, m, tickedMsg{})
	if !strings.Contains(m.View(), "still down") {
		t.Fatalf("erroring pass cleared the status bar:\n%s", m.View())
	}

	// A clean pass supersedes the stale error; how the last run ended is a
	// transient note of its own, cleared the same way.
	m = update(t, m, tickMsg{})
	m = update(t, m, tickedMsg{})
	if strings.Contains(m.View(), "still down") {
		t.Fatalf("clean pass left a stale error on the status bar:\n%s", m.View())
	}
}

// Selection is by ticket, not row position: a row appearing above the
// cursor, the ticket changing queue, or a run starting on it must not move
// the selection — and with it the tail — onto a different ticket.
func TestSelectionFollowsTheTicket(t *testing.T) {
	plan := func(tickets ...loop.QueueTicket) loop.QueueSnapshot {
		return loop.QueueSnapshot{Team: "LERP", Name: "plan", Status: "Planning", Tickets: tickets}
	}
	one := loop.QueueTicket{ID: "id-1", Identifier: "LERP-1", Title: "one", Eligible: true}
	two := loop.QueueTicket{ID: "id-2", Identifier: "LERP-2", Title: "two", Eligible: true}
	zero := loop.QueueTicket{ID: "id-0", Identifier: "LERP-0", Title: "zero", Eligible: true}

	m, _, _ := newTestModel(t, 2)
	m = update(t, m, keyMsg("2"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues,
		Queues: []loop.QueueSnapshot{plan(one, two), {Team: "LERP", Name: "implement", Status: "Todo"}}}})
	m = update(t, m, keyMsg("down"))
	if m.workSel != "id-2" {
		t.Fatalf("selected ticket = %q, want id-2", m.workSel)
	}

	// A pass that slots a row in above it and moves it to another queue.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		plan(zero, one),
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "id-2", Identifier: "LERP-2", Title: "two", Eligible: true}}},
	}}})
	if r := m.selectedWork(); m.workSel != "id-2" || r.queue != "implement" {
		t.Fatalf("selection after the ticket changed queue = %q in %q, want id-2 in implement",
			m.workSel, r.queue)
	}

	// A run starting on the selected ticket keeps it under the cursor — the
	// row moves to the top of its group and the selection goes with it.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r2", Lane: 1,
		TicketID: "id-2", Ticket: "LERP-2", Queue: "implement", LogPath: "/dev/null"}})
	if r := m.selectedWork(); m.workSel != "id-2" || r.lane != 1 {
		t.Fatalf("selection after the run started = %q on lane %d, want id-2 running", m.workSel, r.lane)
	}

	// Only the ticket leaving the panel moves the cursor: it falls back to
	// the nearest remaining row.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r2", Lane: 1,
		TicketID: "id-2", Ticket: "LERP-2", Queue: "implement"}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		plan(zero, one), {Team: "LERP", Name: "implement", Status: "Todo"}}}})
	if m.workSel != "id-1" {
		t.Fatalf("selection after its ticket left the panel = %q, want the fallback id-1", m.workSel)
	}
}

// Done-when: the word "lane" is nowhere an operator can see it. It stays the
// internal noun and the evidence record's field; the screen says what is
// running and how much capacity there is.
func TestTheWordLaneIsOffTheOperatorsScreen(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{
		{Width: 120, Height: 40}, {Width: 100, Height: 30}, {Width: 70, Height: 30},
	} {
		m, _, _ := newTestModel(t, 2)
		resized, _ := m.Update(size)
		m = fillBoard(t, resized.(model), 6)
		m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
			TicketID: "t0", Ticket: "QUEUED-1", Queue: "implement", LogPath: "/dev/null"}})
		m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r9", Lane: 9,
			TicketID: "adopted-one", Queue: "review", LogPath: "/dev/null"}})
		for _, focus := range []string{"1", "2"} {
			m = update(t, m, keyMsg(focus))
			for _, view := range []string{m.View(), update(t, m, keyMsg("?")).View()} {
				if strings.Contains(strings.ToLower(view), "lane") {
					t.Fatalf("%dx%d, panel %s: the screen says lane:\n%s",
						size.Width, size.Height, focus, view)
				}
			}
		}
	}
}

// Quitting must not sever an in-flight pass: runTick marks the pass in flight
// before its command runs, and clears it when the pass returns, so Run can
// await it before the caller releases the clone lock.
func TestQuitAwaitsTheInFlightPass(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	cmd := m.runTick()
	waited := make(chan struct{})
	go func() { m.passes.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("a scheduled pass is not tracked as in flight")
	case <-time.After(20 * time.Millisecond):
	}
	if _, ok := cmd().(tickedMsg); !ok {
		t.Fatal("runTick did not report the pass done")
	}
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("a finished pass is still tracked as in flight")
	}
}

// threeWaiting is an inbox board with room to walk: three items, three
// ticket IDs.
func threeWaiting() loop.Event {
	return loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", TicketID: "id-1", Title: "First",
			Status: "Backlog", Reason: "unclaimed", URL: "https://linear.app/acme/issue/LERP-1"},
		{Ticket: "LERP-2", TicketID: "id-2", Title: "Second",
			Status: "Backlog", Reason: "unclaimed", URL: "https://linear.app/acme/issue/LERP-2"},
		{Ticket: "LERP-3", TicketID: "id-3", Title: "Third",
			Status: "Backlog", Reason: "unclaimed", URL: "https://linear.app/acme/issue/LERP-3"},
	}}
}

// selectAndRead walks to the row at index sel and delivers the debounce for
// it, returning the model with the read already applied.
func selectAndRead(t *testing.T, m model, sel int, detail linear.IssueDetail, err error, reader *recordingReader) model {
	t.Helper()
	for i := 0; i < sel; i++ {
		m = update(t, m, keyMsg("j"))
	}
	reader.returns(detail, err)
	id := m.shown[sel].TicketID
	m, cmd := updateCmd(t, m, detailDueMsg{ticketID: id})
	if cmd == nil {
		t.Fatalf("settling on %s scheduled no fetch", id)
	}
	return update(t, m, cmd())
}

// Done-when: walking the list with j does not fire a fetch per row. Every
// row schedules a debounce; only the one the selection settled on reads.
func TestTicketDetailFetchesOnceTheSelectionSettles(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("j"))
	m = update(t, m, keyMsg("j"))

	if got := reader.fetched(); len(got) != 0 {
		t.Fatalf("walking the list fetched %v before any selection settled", got)
	}
	// The debounces scheduled on the way down fire against a selection that
	// has moved on, and must do nothing at all.
	for _, stale := range []string{"id-1", "id-2"} {
		var cmd tea.Cmd
		m, cmd = updateCmd(t, m, detailDueMsg{ticketID: stale})
		if cmd != nil {
			t.Fatalf("a stale debounce for %s fired a fetch", stale)
		}
	}
	m, cmd := updateCmd(t, m, detailDueMsg{ticketID: "id-3"})
	if cmd == nil {
		t.Fatal("the settled selection fired no fetch")
	}
	m = update(t, m, cmd())
	if got := reader.fetched(); len(got) != 1 || got[0] != "id-3" {
		t.Fatalf("fetched %v, want one read of id-3", got)
	}

	// Revisiting a ticket already read is instant: the cache is the whole
	// refresh story, so no second fetch is issued.
	m = update(t, m, keyMsg("k"))
	m = update(t, m, keyMsg("j"))
	if _, cmd := updateCmd(t, m, detailDueMsg{ticketID: "id-3"}); cmd != nil {
		t.Fatal("re-selecting a cached ticket fetched it again")
	}
}

// Done-when: selecting an inbox item shows its body and its comments in
// the main pane, oldest comment first, without leaving lerp.
func TestTicketDetailShowsBodyAndComments(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, eventMsg{ev: threeWaiting()})

	now := time.Now()
	m = selectAndRead(t, m, 0, linear.IssueDetail{
		Body: "the ticket body",
		Comments: []linear.Comment{
			{Author: "lerp", Body: "the plan", CreatedAt: now.Add(-3 * time.Hour)},
			{Author: "reviewer", Body: "the verdict", CreatedAt: now.Add(-5 * time.Minute)},
		},
	}, nil, reader)

	view := m.View()
	for _, want := range []string{"the ticket body", "lerp · 3h ago", "the plan", "reviewer · 5m ago", "the verdict"} {
		if !strings.Contains(view, want) {
			t.Fatalf("main pane is missing %q:\n%s", want, view)
		}
	}
	// Chronological: the verdict is the last thing written and the last
	// thing on the pane, which is where the eye lands.
	if strings.Index(view, "the plan") > strings.Index(view, "the verdict") {
		t.Fatalf("comments are not in chronological order:\n%s", view)
	}
	// The lines the pass produced still come first. (The o affordance moved
	// to the focused panel's key line — TestFocusedPanelCarriesItsKeys owns
	// it now; asserting it here would only be reading that line.)
	if strings.Index(view, "unclaimed") > strings.Index(view, "the ticket body") {
		t.Fatalf("the pass's own lines no longer render first:\n%s", view)
	}
}

// Done-when: z folds the section under the cursor and reads it back. This
// ticket is short enough to draw without scrolling, so the viewport's top
// stays pinned at 0, on the pane's own header lines — toggleFold has to
// scan forward from there to reach the first heading rather than reading
// that one line, or z would be a dead key on every ticket short enough not
// to need paging through.
func TestFoldKeyTogglesSectionUnderCursor(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("enter"))
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "# Plan\n\nstep one\nstep two"}, nil, reader)
	if !strings.Contains(m.View(), "step one") {
		t.Fatalf("the section should be open before folding:\n%s", m.View())
	}

	m = update(t, m, keyMsg("z"))
	view := m.View()
	if strings.Contains(view, "step one") {
		t.Fatalf("z did not fold the section under the cursor:\n%s", view)
	}
	if !strings.Contains(view, "hidden") {
		t.Fatalf("a folded section should say how much it hid:\n%s", view)
	}

	m = update(t, m, keyMsg("z"))
	if !strings.Contains(m.View(), "step one") {
		t.Fatalf("pressing z again should reopen the section:\n%s", m.View())
	}
}

// Done-when: fold-all turns a plan into an outline of its headings, and the
// same key opens it back up once everything is already folded.
func TestFoldAllTogglesTheWholeOutline(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("enter"))
	body := "# One\n\nfirst body\n\n# Two\n\nsecond body"
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: body}, nil, reader)

	m = update(t, m, keyMsg("Z"))
	view := m.View()
	if strings.Contains(view, "first body") || strings.Contains(view, "second body") {
		t.Fatalf("Z should have folded every section:\n%s", view)
	}
	if !strings.Contains(view, "One") || !strings.Contains(view, "Two") {
		t.Fatalf("an outline still names every heading:\n%s", view)
	}

	m = update(t, m, keyMsg("Z"))
	view = m.View()
	if !strings.Contains(view, "first body") || !strings.Contains(view, "second body") {
		t.Fatalf("Z again should reopen everything:\n%s", view)
	}
}

// The fold keys are inert without a heading to act on: a plain ticket body
// never grows a "hidden" line nobody asked for.
func TestFoldKeysAreInertWithNoHeadings(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("enter"))
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "just a paragraph, nothing to fold"}, nil, reader)

	before := m.View()
	m = update(t, m, keyMsg("z"))
	m = update(t, m, keyMsg("Z"))
	if got := m.View(); got != before {
		t.Fatalf("fold keys changed a headingless body:\n before:\n%s\n after:\n%s", before, got)
	}
}

// Done-when: the key hints show the fold keys, only where there is
// something to fold — a plain ticket body offers neither z nor Z, the way r
// drops out where there is no log to flip.
func TestFoldKeyHintsOnlyWhereThereIsSomethingToFold(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("enter"))
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "no headings here"}, nil, reader)
	if hint := m.keyHint(panelAttention, 200); strings.Contains(hint, "fold") {
		t.Fatalf("a headingless body should not offer the fold keys: %q", hint)
	}

	m2, _, reader2 := newReadingTestModel(t)
	m2 = update(t, m2, keyMsg("1"))
	m2 = update(t, m2, eventMsg{ev: threeWaiting()})
	m2 = update(t, m2, keyMsg("enter"))
	m2 = selectAndRead(t, m2, 0, linear.IssueDetail{Body: "# A heading\n\nbody"}, nil, reader2)
	if hint := m2.keyHint(panelAttention, 200); !strings.Contains(hint, "fold") {
		t.Fatalf("a body with a heading should offer the fold keys: %q", hint)
	}
}

// Done-when: the folded-length interaction with scrolling — the viewport's
// own line count shrinks with a fold, and the scroll position it clamps to
// clamps to the shorter content rather than to what the fold hid.
func TestFoldedLengthInteractsWithScrolling(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = update(t, m, keyMsg("enter"))
	body := "# Plan\n\n" + strings.Repeat("a line of the plan\n", 40)
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: body}, nil, reader)
	full := m.vp.TotalLineCount()

	m = update(t, m, keyMsg("z")) // cursor is still at the top, on the heading itself

	folded := m.vp.TotalLineCount()
	if folded >= full {
		t.Fatalf("folding did not shrink the pane's content: folded=%d full=%d", folded, full)
	}
	m.vp.GotoBottom()
	if m.vp.YOffset+m.vp.Height < folded {
		t.Fatalf("the bottom of a folded pane should clamp to its own shorter length")
	}
}

// Done-when: a comments fetch that fails never blanks the lines that work
// today, and still points at Linear.
func TestTicketDetailFailureKeepsThePaneThatWorks(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, eventMsg{ev: threeWaiting()})

	// In flight, the pane says so — and still shows everything the pass knows.
	m, cmd := updateCmd(t, m, detailDueMsg{ticketID: "id-1"})
	if cmd == nil {
		t.Fatal("the selected ticket fired no fetch")
	}
	view := m.View()
	if !strings.Contains(view, "reading the ticket…") {
		t.Fatalf("the pane does not say the read is in flight:\n%s", view)
	}
	for _, want := range []string{"LERP-1", "Backlog", "unclaimed", "https://linear.app/acme/issue/LERP-1"} {
		if !strings.Contains(view, want) {
			t.Fatalf("in flight, the pane lost %q:\n%s", want, view)
		}
	}

	reader.returns(linear.IssueDetail{}, errors.New("linear: rate limited"))
	m = update(t, m, cmd())
	view = m.View()
	if !strings.Contains(view, "couldn't read the ticket") || !strings.Contains(view, "rate limited") {
		t.Fatalf("the pane does not say the read failed:\n%s", view)
	}
	for _, want := range []string{"LERP-1", "Backlog", "unclaimed", "https://linear.app/acme/issue/LERP-1", "o opens it in Linear"} {
		if !strings.Contains(view, want) {
			t.Fatalf("a failed read cost the pane %q:\n%s", want, view)
		}
	}
}

// The same failed read on a row whose URL the opener refuses: the pane has
// nowhere to send the operator, so it does not offer to. The key line has
// already dropped o for this row, and a pane that still said "o opens it in
// Linear" would be the one place on the screen still promising the door.
func TestAFailedReadOffersNoDoorItCannotOpen(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", TicketID: "id-1", Title: "First",
			Status: "Backlog", Reason: "unclaimed", URL: "file:///etc/passwd"},
	}}})
	m = selectAndRead(t, m, 0, linear.IssueDetail{}, errors.New("linear: rate limited"), reader)

	view := m.View()
	if !strings.Contains(view, "couldn't read the ticket") {
		t.Fatalf("the pane does not say the read failed:\n%s", view)
	}
	if strings.Contains(view, "o opens it in Linear") {
		t.Fatalf("the pane offers a door the opener will refuse:\n%s", view)
	}
}

// A ticket with nothing on it still reads as answered, not as still loading.
func TestTicketDetailEmptyTicket(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = selectAndRead(t, m, 0, linear.IssueDetail{}, nil, reader)

	view := m.View()
	if !strings.Contains(view, "(no description)") || !strings.Contains(view, "(no comments)") {
		t.Fatalf("an empty ticket does not read as empty:\n%s", view)
	}
}

// Done-when: the pane shows the ticket, not its source — the plan in the
// body and the verdict in a comment render the same way, as markdown.
func TestTicketDetailRendersMarkdown(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, eventMsg{ev: threeWaiting()})

	m = selectAndRead(t, m, 0, linear.IssueDetail{
		Body: "## Plan\n\n* touch `internal/tui/model.go`",
		Comments: []linear.Comment{
			{Author: "lerp", Body: "**shipped** it", CreatedAt: time.Now()},
		},
	}, nil, reader)

	view := m.View()
	for _, want := range []string{"Plan", "• touch internal/tui/model.go", "shipped it"} {
		if !strings.Contains(view, want) {
			t.Fatalf("main pane is missing %q:\n%s", want, view)
		}
	}
	for _, bad := range []string{"## Plan", "* touch", "**shipped**", "`internal"} {
		if strings.Contains(view, bad) {
			t.Fatalf("main pane still shows the markdown source %q:\n%s", bad, view)
		}
	}
}

// Done-when: a body carrying escape sequences renders inert, and one
// carrying Linear's inline issue tags renders as bare identifiers.
func TestTicketDetailRendersHostileBodyInert(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = selectAndRead(t, m, 0, linear.IssueDetail{
		Body: hostile + ` blocked by <issue id="u-1" href="https://linear.app/acme/issue/LERP-36">LERP-36</issue>`,
		Comments: []linear.Comment{
			{Author: "agent" + hostile, Body: hostile + " verdict", CreatedAt: time.Now()},
		},
	}, nil, reader)

	view := m.View()
	escapeFree(t, "inbox detail", view)
	bidiFree(t, "inbox detail", view)
	if !strings.Contains(view, "blocked by LERP-36") || strings.Contains(view, "<issue") {
		t.Fatalf("issue tags did not reduce to identifiers:\n%s", view)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Fatalf("line %d is %d cells wide in a %d-column window:\n%s", i, got, m.width, view)
		}
	}
}

// Prose is wrapped to the pane, not truncated at it: a body longer than one
// row is readable past its first line.
func TestTicketDetailWrapsProse(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	body := strings.TrimSpace(strings.Repeat("wrap me please ", 12))
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: body}, nil, reader)

	view := m.View()
	if !strings.Contains(view, "wrap me please wrap") {
		t.Fatalf("the body is not on the pane at all:\n%s", view)
	}
	// A body too long for one row occupies several — the difference between
	// wrapping it and truncating it at the panel's edge.
	rows := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "wrap me") {
			rows++
		}
	}
	if rows < 2 {
		t.Fatalf("the body was truncated rather than wrapped (%d rows):\n%s", rows, view)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Fatalf("line %d is %d cells wide in a %d-column window:\n%s", i, got, m.width, view)
		}
	}
}

// hostile is a ticket title as an attacker would write it: an OSC title
// write, a screen erase, a cursor home, a carriage return to repaint the row
// it lands on, and a right-to-left override to reorder what is left.
const hostile = "\x1b]0;pwned\x07\x1b[2J\x1b[1;1Hpwn\rme\u202e"

// Linear-sourced text reaching the terminal is the whole finding: whatever a
// ticket is titled, every panel and every lens renders it inert, and the
// screen keeps the shape the same board with plain titles would have.
func TestHostileTitlesRenderInert(t *testing.T) {
	board := func(t *testing.T, title string) model {
		t.Helper()
		m, _, _ := newTestModel(t, 2)
		m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
			TicketID: title, Ticket: title, Queue: title, LogPath: "/dev/null"}})
		m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
			{Ticket: title, TicketID: title, Title: title,
				Status: title, Reason: title, URL: title},
		}}})
		return update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
			{Team: title, Name: title, Status: title, Tickets: []loop.QueueTicket{
				{ID: title, Identifier: title, Title: title, URL: title, Eligible: true}}},
		}}})
	}

	for _, focus := range []string{"1", "2"} {
		hm := update(t, board(t, hostile), keyMsg(focus))
		view := hm.View()
		escapeFree(t, "panel "+focus, view)
		bidiFree(t, "panel "+focus, view)

		// Geometry is the assertion that proves the injection is inert
		// rather than merely absent: a title that cannot add a row or shift
		// a column renders the same screen a plain title does.
		benign := update(t, board(t, "plain title"), keyMsg(focus)).View()
		if got, want := lipgloss.Height(view), lipgloss.Height(benign); got != want {
			t.Fatalf("panel %s: hostile board is %d lines, benign board is %d:\n%s",
				focus, got, want, view)
		}
		for i, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > hm.width {
				t.Fatalf("panel %s: line %d is %d cells wide in a %d-column window:\n%s",
					focus, i, got, hm.width, view)
			}
		}
	}

	// The promote picker renders the selected item's title too.
	m := update(t, update(t, board(t, hostile), keyMsg("1")), keyMsg("p"))
	escapeFree(t, "promote picker", m.View())
	bidiFree(t, "promote picker", m.View())
}

// The log pane carries agent output, which is legitimately colored — so it
// keeps SGR and drops everything that could move the cursor or repaint the
// chrome around it.
func TestHostileLogOutputCannotRepaintTheScreen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one.log")
	writeLog(t, path, []byte("\x1b[31mcolored output\x1b[0m\n"+hostile+"\n"))

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("2"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		Ticket: "LERP-1", Queue: "plan", LogPath: path}})
	m = openMain(t, m)

	view := m.View()
	escapeFree(t, "log pane", view)
	if !strings.Contains(view, "colored output") {
		t.Fatalf("log pane lost the agent's output:\n%s", view)
	}
	if !strings.Contains(view, "\x1b[31m") {
		t.Fatalf("log pane dropped the agent's color:\n%q", view)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Fatalf("line %d is %d cells wide in a %d-column window:\n%s", i, got, m.width, view)
		}
	}
}

// A pass error carries Linear's own status and team names into the status
// bar, which is one line and must stay one line.
func TestHostileErrorTextCannotRepaintTheStatusBar(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError, Err: errors.New(hostile)}})
	escapeFree(t, "status bar", m.View())
	bidiFree(t, "status bar", m.View())
	m = update(t, m, openErrMsg{err: errors.New(hostile)})
	escapeFree(t, "status bar", m.View())
	bidiFree(t, "status bar", m.View())
	m = update(t, m, promotedMsg{status: "Planning", results: []promoteResult{
		{ticketID: "id-1", ticket: "LERP-1", err: errors.New(hostile)},
	}})
	escapeFree(t, "status bar", m.View())
	bidiFree(t, "status bar", m.View())
}

// Running and pending rows are adjacent in one list now, so stepping past a
// pending ticket and back is constant motion rather than a rare one. It must
// not cost the operator the place they scrolled back to: one viewport serves
// both lenses, and zeroing it on the way out used to leave the log at the top
// on the way in.
func TestScrollPositionSurvivesADetourThroughAPendingRow(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "one.log")
	var body []byte
	for i := 0; i < 200; i++ {
		body = append(body, []byte(fmt.Sprintf("line %d\n", i))...)
	}
	writeLog(t, logPath, body)

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "t1", Identifier: "LERP-1", Title: "running", Assigned: true},
			{ID: "t2", Identifier: "LERP-2", Title: "waiting", Eligible: true},
		}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "t1", Ticket: "LERP-1", Queue: "implement", LogPath: logPath}})
	m = openMain(t, update(t, m, keyMsg("2")))

	// Scroll back into the log, which turns following off.
	m = update(t, m, keyMsg("pgup"))
	m = update(t, m, keyMsg("pgup"))
	if m.follow {
		t.Fatal("scrolling up left follow on, so this test is not exercising the case")
	}
	want := m.vp.YOffset
	if want == 0 {
		t.Fatal("scrolling up did not move the viewport")
	}

	m = update(t, m, keyMsg("down")) // the pending ticket, which has no log
	m = update(t, m, keyMsg("up"))   // back to the run

	if !m.showingLog() {
		t.Fatal("the log lens did not come back")
	}
	if got := m.vp.YOffset; got != want {
		t.Errorf("offset after the detour = %d, want %d — the operator lost their place", got, want)
	}
}

// A pass that landed on time is the answer to "is the board fresh?", and yes
// is silence. The countdown that used to sit here re-rendered every second
// and changed width as it counted ("9s" → "10s"), so the whole right of the
// bar shifted under a board that was doing nothing at all.
func TestStatusBarGoesQuietBetweenPasses(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, tickedMsg{})

	bar := m.statusBar()
	for _, gone := range []string{"next in", "ago", "pass running"} {
		if strings.Contains(bar, gone) {
			t.Errorf("the bar still says %q with the board fresh:\n%s", gone, bar)
		}
	}
	// The frame counter is what the spinner rides, and the poll advances it
	// four times a second whether or not anything happened.
	for i := 0; i < 3; i++ {
		m = update(t, m, pollMsg{})
		if got := m.statusBar(); got != bar {
			t.Fatalf("the bar moved between frames:\n%s\n%s", bar, got)
		}
	}
	// The mark is not the focus badge it replaced: the panel borders draw
	// which panel has the keys, and the corner stays put across the change.
	// 2 is the panel lerp does not open on, so this is a real change of
	// focus rather than a key that lands where the model already was. The
	// left side is the whole of the claim — the pane's key hint on the right
	// reports what enter does in the focused panel, which is a difference
	// between the panels rather than a badge repeating them.
	moved := update(t, m, keyMsg("2"))
	if moved.focus == m.focus {
		t.Fatalf("key 2 left focus on %v", m.focus)
	}
	for _, seg := range []string{"lerp", "0/2 running"} {
		if a, b := statusCol(moved.statusBar(), seg), statusCol(bar, seg); a != b || a < 0 {
			t.Errorf("focus moved %q from column %d to %d:\n%s\n%s", seg, b, a, bar, moved.statusBar())
		}
	}
}

// The other way the bar could shove its kept segments around: the heartbeat
// appears and vanishes once an interval, so anything drawn after it slides a
// spinner's width and back every pass. Second-cadence jitter traded for
// pass-cadence jitter is still jitter.
func TestAPassStartingDoesNotMoveTheCountsAlong(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "one"}, {Ticket: "LERP-2", Title: "two"},
	}}})
	col := func(m model, want string) int {
		at := statusCol(m.statusBar(), want)
		if at < 0 {
			t.Fatalf("the bar lost %q:\n%s", want, m.statusBar())
		}
		return at
	}

	if !m.inFlight {
		t.Fatal("the first pass is not in flight")
	}
	inFlight := [2]int{col(m, "0/2 running"), col(m, "● 2 in the inbox")}
	settled := update(t, m, tickedMsg{})
	if got := [2]int{col(settled, "0/2 running"), col(settled, "● 2 in the inbox")}; got != inFlight {
		t.Errorf("the pass landing moved the counts from %v to %v", inFlight, got)
	}
}

// statusCol is the column want starts at on the status bar. Neither offset
// into the rendered string nor into the stripped one is a column: the bar is
// full of styling, and its segments carry ●, ⠋ and … , each one byte-wide
// three times over. -1 when the bar does not carry want at all.
func statusCol(bar, want string) int {
	plain := ansi.Strip(bar)
	at := strings.Index(plain, want)
	if at < 0 {
		return -1
	}
	return lipgloss.Width(plain[:at])
}

// The counts are not the only thing right of the heartbeat: the hints are
// too, and they are sized by what the left side leaves them. Counting the
// heartbeat's width there put the jitter back at pass cadence — around
// widths 48–64 a pass starting cost the bar "enter detail · " and slid
// every remaining hint character sideways, once an interval, forever.
func TestAPassStartingDoesNotMoveTheHintsEither(t *testing.T) {
	for w := 30; w <= 120; w++ {
		m, _, _ := newTestModel(t, 2)
		m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
			{Ticket: "LERP-1", Title: "one"}, {Ticket: "LERP-2", Title: "two"},
		}}})
		sized, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		running := sized.(model)
		if !running.inFlight {
			t.Fatal("the first pass is not in flight")
		}
		settled := update(t, running, tickedMsg{})

		for _, seg := range []string{"0/2 running", "● 2 in the inbox", "enter detail", "q quit"} {
			if a, b := statusCol(running.statusBar(), seg), statusCol(settled.statusBar(), seg); a != b {
				t.Fatalf("at width %d a pass starting moved %q from %d to %d:\n%s\n%s",
					w, seg, b, a, settled.statusBar(), running.statusBar())
			}
		}
	}
}

// The heartbeat is legible or it is absent, and the width alone says which.
// Fitted into whatever gap the rest of the bar left over, it came out of a
// narrow window as "pass …" — a warning shredded to the word that is not the
// warning — and out of an ordinary 80-column one as nothing at all, having
// lost the last of the room to "enter detail · ? help · q quit".
func TestTheHeartbeatIsWholeOrAbsent(t *testing.T) {
	var attn []loop.AttentionItem
	for i := 1; i <= 12; i++ {
		attn = append(attn, loop.AttentionItem{Ticket: fmt.Sprintf("LERP-%d", i), Title: "waiting"})
	}
	bars := func(t *testing.T, w int) (running, stale string) {
		t.Helper()
		m, _, _ := newTestModel(t, 2)
		m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: attn}})
		sized, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		settled := update(t, sized.(model), tickedMsg{})
		settled.lastPass = time.Now().Add(-overdueAfter - time.Second)
		return ansi.Strip(sized.(model).statusBar()), ansi.Strip(settled.statusBar())
	}

	// A dozen in the inbox and the pane's key on the right is the crowded
	// case, and 80 columns is not a narrow window — whatever the bar gives
	// up there, it is not the one segment that reports the board is stuck.
	running, stale := bars(t, 80)
	if !strings.Contains(running, "⠋ pass running…") {
		t.Errorf("80 columns has no room for the spinner:\n%s", running)
	}
	if !strings.Contains(stale, "pass overdue") {
		t.Errorf("80 columns has no room for the alarm:\n%s", stale)
	}

	// Half a line reports nothing an empty gap does not, so the bar never
	// serves one; and the width that can hold a heartbeat can hold every
	// wider one, so dragging a window out never takes the heartbeat away.
	widest := 0
	for w := 30; w <= 120; w++ {
		running, stale := bars(t, w)
		if strings.Contains(running, "pass") && !strings.Contains(running, "⠋ pass running…") {
			t.Errorf("at width %d the spinner came out sawn off:\n%s", w, running)
		}
		if strings.Contains(stale, "pass") && !strings.Contains(stale, "pass overdue") {
			t.Errorf("at width %d the alarm came out sawn off:\n%s", w, stale)
		}
		switch {
		case strings.Contains(running, "⠋ pass running…"):
			widest = w
		case widest > 0:
			t.Fatalf("width %d carried the spinner and width %d does not:\n%s", widest, w, running)
		}
	}
}

// Silence means fresh, so it has to stop being silence when the board is
// not: a wedged tick chain — or a laptop that slept through a few hundred
// intervals — would otherwise read exactly like a board keeping up.
func TestStatusBarSaysWhenAPassIsOverdue(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, tickedMsg{})
	m.lastPass = time.Now().Add(-overdueAfter - time.Second)

	bar := m.statusBar()
	if !strings.Contains(bar, "pass overdue") {
		t.Fatalf("a stale board reads as a fresh one:\n%s", bar)
	}
	// Coarse on purpose: how overdue is not a number the operator acts on,
	// and a growing one would put the wiggle back.
	m.lastPass = m.lastPass.Add(-time.Hour)
	if got := m.statusBar(); got != bar {
		t.Errorf("the overdue note is a clock:\n%s\n%s", bar, got)
	}
	// A pass in flight is the spinner's to report, however late it is.
	m.inFlight = true
	if strings.Contains(m.statusBar(), "pass overdue") {
		t.Errorf("a running pass still reads as overdue:\n%s", m.statusBar())
	}
}

// The threshold does not follow the poll. A board polled every second or two
// missing a tick or three is ordinary scheduling slack, not news, and a
// threshold that tracked the interval would have such a board crying stale
// over it — a minute of nothing means something else went wrong.
func TestTheStaleThresholdDoesNotFollowTheInterval(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m.o.Interval = time.Second
	m = update(t, m, tickedMsg{})

	m.lastPass = time.Now().Add(-59 * time.Second)
	if strings.Contains(m.statusBar(), "pass overdue") {
		t.Errorf("a board 59s behind a 1s interval already reads as stale:\n%s", m.statusBar())
	}
	m.lastPass = time.Now().Add(-61 * time.Second)
	if !strings.Contains(m.statusBar(), "pass overdue") {
		t.Errorf("a board a minute behind still reads as fresh:\n%s", m.statusBar())
	}
}

// The notes are cleared by the next pass starting, so on a board where no
// pass is starting the last one sits there for as long as the wedge lasts —
// holding the width, and reading like a board that just did some work.
func TestAStaleNoteDoesNotHoldTheLineAgainstTheAlarm(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, tickedMsg{})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", ExitCode: 0}})

	// While the board is keeping up the note is exactly what the bar is for.
	if bar := m.statusBar(); !strings.Contains(bar, "LERP-42 exited 0") {
		t.Fatalf("the bar dropped a fresh note:\n%s", bar)
	}
	m.lastPass = time.Now().Add(-2 * time.Hour)
	if bar := m.statusBar(); !strings.Contains(bar, "pass overdue") {
		t.Errorf("a two-hour-old note still reads as the news:\n%s", bar)
	}
}

// With N lanes, two runs settling inside one interval is routine. One note
// slot dropped the first of them silently, which is the whole reason the
// status bar was chosen as the home for how a run ended.
func TestBothOutcomesInOneIntervalReachTheStatusBar(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "t1", Ticket: "LERP-1", Queue: "implement"}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r2", Lane: 2,
		TicketID: "t2", Ticket: "LERP-2", Queue: "implement"}})

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "t1", Ticket: "LERP-1", Queue: "implement", ExitCode: 1}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r2", Lane: 2,
		TicketID: "t2", Ticket: "LERP-2", Queue: "implement", ExitCode: 0}})

	view := m.View()
	for _, want := range []string{"LERP-1 exited 1", "LERP-2 exited 0"} {
		if !strings.Contains(view, want) {
			t.Errorf("status bar lost %q:\n%s", want, view)
		}
	}
}

// A pass error used to take the whole line, so an outcome set during the same
// pass never reached the screen — and with the lane rows gone there is no
// other surface that would have carried it.
func TestAPassErrorDoesNotHideHowARunEnded(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "t1", Ticket: "LERP-1", Queue: "implement"}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "t1", Ticket: "LERP-1", Queue: "implement", ExitCode: 1}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError,
		Err: errors.New("list queue implement: boom")}})

	view := m.View()
	if !strings.Contains(view, "list queue implement: boom") {
		t.Errorf("the pass error is missing:\n%s", view)
	}
	if !strings.Contains(view, "LERP-1 exited 1") {
		t.Errorf("the pass error hid how the run ended:\n%s", view)
	}
}

// The off-board group exists so an inherited run does not vanish. A second
// one from the same queue belongs under the first one's header.
func TestOffBoardRunsFromOneQueueShareAHeader(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "plan", Status: "Planning"},
	}}})
	for i, lane := range []int{1, 2} {
		m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: fmt.Sprintf("r%d", i),
			Lane: lane, TicketID: fmt.Sprintf("gone-%d", i), Queue: "implement"}})
	}
	var off int
	for _, g := range m.workGroups() {
		if g.offBoard {
			off++
			if len(g.rows) != 2 {
				t.Errorf("off-board group %q holds %d rows, want both runs", g.name, len(g.rows))
			}
		}
	}
	if off != 1 {
		t.Errorf("off-board groups = %d, want 1:\n%s", off, m.View())
	}
	view := m.View()
	for _, want := range []string{"gone-0", "gone-1"} {
		if !strings.Contains(view, want) {
			t.Errorf("adopted run %q vanished:\n%s", want, view)
		}
	}
}

// Eject is the escape hatch on a running row: "e" opens a confirm in the main
// pane, enter calls the Ejector with that row's ticket, and the reply becomes
// a sticky panel holding the workspace and the resume command — the one
// string the operator has to copy, so nothing but esc takes it away.
func TestEjectConfirmAndResult(t *testing.T) {
	ejector := &recordingEjector{ejection: loop.Ejection{
		Ticket: "LERP-42", Lane: 1, Workspace: "/tmp/lerp/lane-1", Resume: "agent --resume 'sid-42'",
	}}
	m, _ := newEjectTestModel(t, 1, ejector)
	m = update(t, m, keyMsg("2")) // eject is the work panel's key
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})
	if !strings.Contains(m.View(), "e eject") {
		t.Fatalf("the work panel does not offer eject on a running row:\n%s", m.View())
	}

	// The pane is shut — the screen work starts on — so the confirm is one
	// of the things that opens it. Live behind a closed pane it would hold
	// the keyboard, and the enter that kills the agent, with nothing on
	// screen saying so.
	if m.mainOpen() {
		t.Fatal("the pane was open before anything asked for it")
	}

	// esc backs out without touching the run.
	m = update(t, m, keyMsg("e"))
	if !m.ejecting {
		t.Fatal("e did not open the eject confirm")
	}
	if !m.mainOpen() || !strings.Contains(m.View(), "eject LERP-42") {
		t.Fatalf("the confirm is live but not on screen:\n%s", m.View())
	}
	m = update(t, m, keyMsg("esc"))
	if m.ejecting {
		t.Fatal("esc did not close the eject confirm")
	}
	if m.mainOpen() {
		t.Fatal("esc closed the confirm and left its pane behind")
	}
	if len(ejector.ejected()) != 0 {
		t.Fatalf("esc ejected anyway: %v", ejector.ejected())
	}

	m = update(t, m, keyMsg("e"))
	view := m.View()
	for _, want := range []string{"eject LERP-42", "stops", "keeps", "enter eject"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the eject confirm is missing %q:\n%s", want, view)
		}
	}

	next, cmd := m.Update(keyMsg("enter"))
	m = next.(model)
	if m.ejecting {
		t.Fatal("enter did not close the eject confirm")
	}
	if cmd == nil {
		t.Fatal("enter produced no eject command")
	}
	// Eject runs off the render loop, exactly like promote and the tick.
	msg, ok := cmd().(ejectedMsg)
	if !ok {
		t.Fatalf("eject command yielded %T, want ejectedMsg", cmd())
	}
	if got := ejector.ejected(); len(got) != 1 || got[0] != "id-42" {
		t.Fatalf("Eject calls = %v, want exactly the selected row's ticket id", got)
	}

	m = update(t, m, msg)
	view = m.View()
	for _, want := range []string{"ejected LERP-42", "/tmp/lerp/lane-1", "agent --resume 'sid-42'", "esc dismiss"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the eject result is missing %q:\n%s", want, view)
		}
	}
	// A pass arriving under the panel must not clear it: the run it reports
	// finishing is the very one that was ejected.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventEjected, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement"}})
	if m.ejection == nil {
		t.Fatal("a loop event dismissed the eject result")
	}
	if !strings.Contains(m.View(), "agent --resume 'sid-42'") {
		t.Fatalf("the resume command is gone after a pass:\n%s", m.View())
	}
	m = update(t, m, keyMsg("esc"))
	if m.ejection != nil {
		t.Fatal("esc did not dismiss the eject result")
	}
	if !strings.Contains(m.View(), "0/1 running") {
		t.Fatalf("the ejected run still holds its lane:\n%s", m.View())
	}
}

// The key is offered only where it works: not on a ticket that is waiting to
// run, and not on a run whose queue's runner has no resume command. An
// advertised key that does nothing is worse than one left out.
func TestEjectKeyOnlyOnAResumableRun(t *testing.T) {
	ejector := &recordingEjector{resumable: []string{"implement"}}
	m, _ := newEjectTestModel(t, 1, ejector)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{{
		Team: "LERP", Name: "implement", Status: "Implementing",
		Tickets: []loop.QueueTicket{{ID: "id-7", Identifier: "LERP-7", Title: "waiting", Eligible: true}},
	}}}})
	if strings.Contains(m.View(), "e eject") {
		t.Fatalf("the work panel offers eject on a ticket that is not running:\n%s", m.View())
	}
	if m.canEjectSelected() {
		t.Fatal("a waiting ticket reads as ejectable")
	}

	// A live run in a queue whose runner cannot resume: same answer, and it
	// comes from the loop rather than from the panel guessing.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-9", Ticket: "LERP-9", Queue: "plan", LogPath: "/dev/null"}})
	if m.canEjectSelected() {
		t.Fatal("a run under a runner with no resume command reads as ejectable")
	}
	if strings.Contains(m.View(), "e eject") {
		t.Fatalf("the work panel offers eject under a runner that cannot resume:\n%s", m.View())
	}
	// Pressing it anyway does nothing at all.
	m = update(t, m, keyMsg("e"))
	if m.ejecting {
		t.Fatal("e opened the confirm for a run that cannot be ejected")
	}
}

// A lane that is still provisioning has no agent to kill yet.
func TestEjectIsNotOfferedWhileProvisioning(t *testing.T) {
	m, _ := newEjectTestModel(t, 1, &recordingEjector{})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventProvisioning, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement"}})
	if m.canEjectSelected() {
		t.Fatal("a provisioning lane reads as ejectable")
	}
}

// The panel is the only place the resume command is shown, so it must survive
// a narrow pane whole: panelBox truncates rows and fitRows drops the tail, and
// half a command is not a command.
func TestEjectResultKeepsTheWholeCommand(t *testing.T) {
	m, _ := newEjectTestModel(t, 1, &recordingEjector{})
	resume := "claude --resume '1e9a4a0e-0000-4000-8000-00000000abcd' --cwd '/Users/x/src/lerp/.lerp/workspaces/11f642e6'"
	workspace := "/Users/x/src/lerp/.lerp/workspaces/11f642e6e35fe9092b7dccb0dc4b69ca"
	view := m.ejectResult(loop.Ejection{Ticket: "LERP-14", Workspace: workspace, Resume: resume}, 55, 20)
	// Both marks: panelBox cuts a row with "…", fitRows drops the tail with
	// "⋯ n more", and either one would mean a command the operator cannot use.
	for _, cut := range []string{"…", "⋯"} {
		if strings.Contains(view, cut) {
			t.Fatalf("the eject result cut something (%s):\n%s", cut, view)
		}
	}
	// Wrapped rather than cut, so the assertion is that every wrapped line of
	// both — including the last, which is what fitRows would have dropped —
	// is on screen.
	plain := ansi.Strip(view)
	width := padMain.inner(55)
	for _, want := range append(strings.Split(ansi.Wrap(resume, width, " "), "\n"),
		strings.Split(ansi.Wrap(workspace, width, "-"), "\n")...) {
		if !strings.Contains(plain, want) {
			t.Errorf("the eject result is missing %q:\n%s", want, view)
		}
	}
}

// A running row's second line is the question the first one cannot answer:
// not "did it start" but "is it still doing something". Elapsed and what the
// run has spent on the first line, the last call it made and the activity
// around it on the second.
func TestRunningRowShowsHowTheRunIsGoing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	writeLog(t, path, []byte(
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n"))

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "id-1", Identifier: "LERP-1", Title: "one", Assigned: true}}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "implement", LogPath: path,
		StartedAt: time.Now().Add(-90 * time.Second)}})

	// The first poll attaches the pulse; the agent then does something.
	m = update(t, m, pollMsg{})
	appendLog(t, path,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}],`+
			`"usage":{"input_tokens":52,"output_tokens":448,"cache_read_input_tokens":11500}}}`+"\n")
	m = update(t, m, pollMsg{})

	// The tool is a shell one, so the row spends its columns on the command
	// and not on the word "Bash"; the tokens are the call's four counts.
	view := m.View()
	for _, want := range []string{"running", "1m30s", "12k tok", "$ go test ./...", "█"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the running row is missing %q:\n%s", want, view)
		}
	}
	// The row is two lines and the panel counts them, or the list would
	// draw more rows than it has room for.
	rows, _ := m.workListRows(40)
	if len(rows) != 3 {
		t.Fatalf("work list drew %d lines, want a header and a two-line row: %q", len(rows), rows)
	}
	if !strings.Contains(rows[1], "LERP-1") || strings.Contains(rows[2], "LERP-1") {
		t.Fatalf("the run's two lines are not the ticket then its reading: %q", rows)
	}
}

// A ticket nothing is running keeps its one line: the second line is a
// reading of a run, and there is no run.
func TestWaitingRowStaysOneLine(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "id-1", Identifier: "LERP-1", Title: "one", Eligible: true}}},
	}}})
	rows, _ := m.workListRows(40)
	if len(rows) != 2 {
		t.Fatalf("work list drew %d lines, want a header and a one-line row: %q", len(rows), rows)
	}
}

// A lane still provisioning has no log to read, so its row shows the clock it
// has and claims nothing about a stream that does not exist yet.
func TestProvisioningRowClaimsNoReading(t *testing.T) {
	// The loop hands a provisioning lane the path its log will have, and the
	// runner does not create the file until the agent starts — so the row
	// has a path pointing at nothing, which is the case that matters.
	path := filepath.Join(t.TempDir(), "not-yet.log")
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventProvisioning, RunID: "r1", Lane: 1,
		TicketID: "id-9", Ticket: "LERP-9", Queue: "plan", LogPath: path, StartedAt: time.Now()}})
	m = update(t, m, pollMsg{})
	view := m.View()
	// One line, not two: the second line is a reading of a log, and there is
	// no log.
	if rows, _ := m.workListRows(40); len(rows) != 2 {
		t.Fatalf("work list drew %d lines, want a header and a one-line row: %q", len(rows), rows)
	}
	if strings.ContainsAny(view, "▁█") {
		t.Fatalf("a provisioning row draws activity for a log that does not exist:\n%s", view)
	}
	if !strings.Contains(view, "provisioning") {
		t.Fatalf("a provisioning row lost its state:\n%s", view)
	}
}

// A run whose log appears late — the ordinary case, since the lane is given
// its path while the workspace is still being provisioned — starts its
// reading when the file does. The buckets that passed before it existed are
// not quiet buckets; they are no buckets.
func TestPulseStartsWhenTheLogDoes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "late.log")
	start := time.Now()
	p := newPulse(path)
	for i := 0; i < 20; i++ {
		p.read(start.Add(time.Duration(i) * sparkBucket))
	}
	if got := p.window(); len(got) != 0 {
		t.Fatalf("a log that never appeared drew %v", got)
	}
	appendLog(t, path, "the agent starts\n")
	p.read(start.Add(20 * sparkBucket))
	if got := p.window(); len(got) != 1 {
		t.Fatalf("the run's first poll drew %d buckets, want 1: %v", len(got), got)
	}
}

// The focus window slides by line, and a run's two lines are one row. A
// window that keeps only the first cuts the reading off the very row the
// operator is looking at; one that opens on a second line strands it under
// whatever name happens to be above, where it reads as that ticket's.
func TestScrolledRunKeepsRowsWhole(t *testing.T) {
	// The log starts empty and the call arrives after the first poll, the
	// way a live run writes it.
	call := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash",` +
		`"input":{"command":"go build ./..."}}]}}` + "\n"

	for _, size := range []struct{ w, h int }{{120, 22}, {120, 25}, {120, 30}, {100, 44}} {
		path := filepath.Join(t.TempDir(), "run.log")
		writeLog(t, path, nil)
		m, _, _ := newTestModel(t, 5)
		resized, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m = fillBoard(t, resized.(model), 40)
		for lane := 1; lane <= 5; lane++ {
			m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted,
				RunID: fmt.Sprintf("r%d", lane), Lane: lane,
				TicketID: fmt.Sprintf("t%d", lane-1), Ticket: fmt.Sprintf("QUEUED-%d", lane),
				Queue: "implement", LogPath: path}})
		}
		m = update(t, m, pollMsg{})
		appendLog(t, path, call)
		m = update(t, m, pollMsg{})
		m = update(t, m, keyMsg("2"))

		// Walk the cursor onto each running row in turn. The panel is capped
		// near a third of the stack, so it is scrolling well before the last.
		for _, want := range []string{"QUEUED-1", "QUEUED-2", "QUEUED-3", "QUEUED-4", "QUEUED-5"} {
			for m.selectedWork() == nil || m.selectedWork().ticket != want {
				m = update(t, m, keyMsg("down"))
			}
			g := m.geometry()
			panel := ansi.Strip(m.workPanel(g.sideW, g.workH))
			lines := strings.Split(panel, "\n")
			at := slices.IndexFunc(lines, func(l string) bool {
				return strings.Contains(l, "▸") && strings.Contains(l, want)
			})
			if at < 0 {
				t.Fatalf("%dx%d: the selected row %s is not on the panel:\n%s", size.w, size.h, want, panel)
			}
			if at+1 >= len(lines) || !strings.Contains(lines[at+1], "$ go build") {
				t.Fatalf("%dx%d: %s lost its reading to the window:\n%s", size.w, size.h, want, panel)
			}
			// And no reading line is left under a name it does not belong
			// to: every one of them follows the row that produced it.
			for i, l := range lines {
				if !strings.Contains(l, "$ go build") {
					continue
				}
				if i == 0 || !strings.Contains(lines[i-1], "●") {
					t.Fatalf("%dx%d: a reading sits under %q, not under a run:\n%s",
						size.w, size.h, strings.TrimSpace(lines[max(i-1, 0)]), panel)
				}
			}
		}
	}
}

// The confirm is about the row the operator pressed "e" on, not about
// whatever the cursor has drifted to by the time they press enter: a pass in
// between can move rows, and a run in another lane must not be killed by an
// enter meant for this one.
func TestEjectConfirmHoldsItsRowAcrossAPass(t *testing.T) {
	ejector := &recordingEjector{}
	m, _ := newEjectTestModel(t, 2, ejector)
	m = update(t, m, keyMsg("2")) // eject is the work panel's key
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r2", Lane: 2,
		TicketID: "id-9", Ticket: "LERP-9", Queue: "implement", LogPath: "/dev/null"}})
	m = update(t, m, keyMsg("e"))
	if !m.ejecting || m.ejectRow.ticketID != "id-42" {
		t.Fatalf("e captured %+v, want the selected LERP-42 row", m.ejectRow)
	}

	// The selection moves under the overlay — here by the operator's own
	// earlier row leaving the panel is simulated with a plain move.
	m.workPos, m.workSel = 1, "id-9"
	m, cmd := updateCmd(t, m, keyMsg("enter"))
	if cmd == nil {
		t.Fatal("enter produced no eject command")
	}
	cmd() // the call off the render loop is what reaches the Ejector
	if got := ejector.ejected(); len(got) != 1 || got[0] != "id-42" {
		t.Fatalf("Eject calls = %v, want only the row the confirm was opened on", got)
	}
}

// A run that ends while the confirm is open leaves nothing to eject, so the
// overlay closes rather than sending an enter after a dead agent.
func TestEjectConfirmClosesWhenItsRunEnds(t *testing.T) {
	ejector := &recordingEjector{}
	m, _ := newEjectTestModel(t, 1, ejector)
	m = update(t, m, keyMsg("2")) // eject is the work panel's key
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})
	m = update(t, m, keyMsg("e"))
	if !m.ejecting {
		t.Fatal("e did not open the eject confirm")
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", ExitCode: 0}})
	if m.ejecting {
		t.Fatal("the eject confirm stayed open after its run finished")
	}
	m, cmd := updateCmd(t, m, keyMsg("enter"))
	if cmd != nil {
		cmd()
	}
	if got := ejector.ejected(); len(got) != 0 {
		t.Fatalf("enter ejected a finished run: %v", got)
	}
}

// A runner whose command never opens a session lerp chose cannot be resumed
// either, however its resume template reads: lerp has no id to hand back. The
// key is not offered, rather than failing after the operator has confirmed.
func TestEjectIsNotOfferedWithoutASession(t *testing.T) {
	ejector := &recordingEjector{resumable: []string{"implement"}}
	m, _ := newEjectTestModel(t, 1, ejector)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-9", Ticket: "LERP-9", Queue: "plan", LogPath: "/dev/null"}})
	if m.canEjectSelected() {
		t.Fatal("a run whose runner the loop says cannot resume reads as ejectable")
	}
}

// A runner that cannot resume is the one refusal worth a word: the row looks
// exactly like an ejectable one, so pressing "e" says why instead of doing
// nothing at all.
func TestEjectSaysWhyARunnerCannotResume(t *testing.T) {
	ejector := &recordingEjector{resumable: []string{"implement"}}
	m, _ := newEjectTestModel(t, 1, ejector)
	m = update(t, m, keyMsg("2")) // eject is the work panel's key
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-9", Ticket: "LERP-9", Queue: "plan", LogPath: "/dev/null"}})
	m = update(t, m, keyMsg("e"))
	if m.ejecting {
		t.Fatal("e opened the confirm for a run that cannot be ejected")
	}
	if !strings.Contains(m.View(), "no resume command") {
		t.Fatalf("pressing e said nothing about why it will not eject:\n%s", m.View())
	}
}

// The panel the wide layout starts at is the narrowest one it draws, and the
// call is the reading: the sparkline is what yields to a narrow panel, never
// the command.
func TestRunLineKeepsTheCallWhenNarrow(t *testing.T) {
	r := workRow{lane: 1, since: time.Now().Add(-65 * time.Minute),
		heard: time.Now().Add(-12*time.Minute - 30*time.Second),
		tool:  "Bash", target: "go test ./...",
		rate: []int{1, 0, 3, 0, 9, 0, 0, 0}}
	// What a 100-column terminal — the wide layout's own threshold — leaves
	// a list panel for its rows beside an open pane, asked of the geometry
	// rather than restated. The pane is what makes the panel narrow, so it
	// is open here: with it shut the list has the whole terminal and this
	// row has columns to spare.
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: narrowWidth, Height: 40})
	m = openMain(t, resized.(model))
	width := padList.inner(m.geometry().sideW)

	line := ansi.Strip(runLine(r, width))
	if !strings.Contains(line, "$ go test ./...") {
		t.Fatalf("the call did not survive a %d-column panel: %q", width, line)
	}
	if !strings.ContainsAny(line, "▁█") {
		t.Fatalf("the sparkline was dropped where it fits: %q", line)
	}
	if lipgloss.Width(line) > width {
		t.Fatalf("the line is %d columns wide, panel is %d: %q", lipgloss.Width(line), width, line)
	}
	// The boundary itself is in columns, not bytes: the sparkline appears at
	// exactly the width the drawn line occupies, and one column under that
	// it goes rather than the digits.
	fits := 0
	for w := 1; w <= 60 && fits == 0; w++ {
		if strings.ContainsAny(ansi.Strip(runLine(r, w)), "▁█") {
			fits = w
		}
	}
	if want := lipgloss.Width("    $ go test ./...") + 1 + len(r.rate); fits != want {
		t.Fatalf("the sparkline appears at %d columns, but the line draws in %d", fits, want)
	}
	tight := ansi.Strip(runLine(r, fits-1))
	if strings.ContainsAny(tight, "▁█") {
		t.Fatalf("a sparkline crowded out the call: %q", tight)
	}
	if !strings.Contains(tight, "$ go test ./...") {
		t.Fatalf("the reading was truncated to make room for decoration: %q", tight)
	}
}

// The first line carries the facts about the whole run — how long it has been
// going, and what it has spent — in the columns a row can spare for them. The
// figure is the run's own even for an adopted run: the pulse reads the whole
// log, so there is no partial total to hedge.
func TestRunningRowCarriesWhatTheRunHasSpent(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	r := workRow{ticket: "LERP-1", title: "one", lane: 1, state: laneRunning,
		since: time.Now().Add(-90 * time.Second), heard: time.Now(), tokens: 5_200_000}

	first := ansi.Strip(m.workRowLines(r, false, 80)[0])
	if !strings.Contains(first, "1m30s") || !strings.Contains(first, "5.2M tok") {
		t.Fatalf("the running row does not say how long or how much: %q", first)
	}
	if strings.Contains(first, "≥") {
		t.Fatalf("the row hedges a total the whole log was read for: %q", first)
	}

	// A run that has not been charged for anything yet says nothing about
	// tokens: "0 tok" is a reading nobody asked for.
	r.tokens = 0
	if got := ansi.Strip(m.workRowLines(r, false, 80)[0]); strings.Contains(got, "tok") {
		t.Fatalf("a run that has spent nothing still reports a total: %q", got)
	}
}

// The counts a run reaches span four orders of magnitude, and the row has
// about six columns for them: the decimal goes where it changes the reading
// and not where it is noise.
func TestSpendReadsInTheColumnsARowHas(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{940, "940 tok"},
		{1_400, "1.4k tok"},
		{9_990, "10.0k tok"},
		{12_499, "12k tok"},
		{847_000, "847k tok"},
		{999_900, "1.0M tok"},
		{5_200_000, "5.2M tok"},
	} {
		if got := tokenCount(tc.n); got != tc.want {
			t.Errorf("tokenCount(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// The dollar figure is a fact about the whole run, drawn beside the elapsed
// clock the same way the token count is — and, like the token count, absent
// entirely for a run whose runner never states one.
func TestRunningRowCarriesWhatTheRunHasCost(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	r := workRow{ticket: "LERP-1", title: "one", lane: 1, state: laneRunning,
		since: time.Now().Add(-90 * time.Second), heard: time.Now(), cost: 0.42}

	first := ansi.Strip(m.workRowLines(r, false, 80)[0])
	if !strings.Contains(first, "1m30s") || !strings.Contains(first, "$0.42") {
		t.Fatalf("the running row does not say how long or how much it cost: %q", first)
	}

	// A run whose runner reports no cost at all — codex, today — says
	// nothing in dollars: no gap, no zero, nothing to misread as a real
	// figure.
	r.cost = 0
	if got := ansi.Strip(m.workRowLines(r, false, 80)[0]); strings.Contains(got, "$") {
		t.Fatalf("a run with no reported cost still shows a dollar figure: %q", got)
	}

	// A run that genuinely cost a fraction of a cent is not "no cost
	// reported" — codex's case — but $0.00 beside a token count would read
	// as one anyway: a real, reported zero. Below minCost it is dropped the
	// same as the absent case.
	r.cost = 0.001
	if got := ansi.Strip(m.workRowLines(r, false, 80)[0]); strings.Contains(got, "$") {
		t.Fatalf("a sub-cent run drew a dollar figure at all: %q", got)
	}
}

func TestCostGraduatesPrecisionLikeTokenCount(t *testing.T) {
	for _, tc := range []struct {
		c    float64
		want string
	}{
		{0.08, "$0.08"},
		{9.99, "$9.99"},
		{10, "$10.0"},
		{12.3, "$12.3"},
		{99.9, "$99.9"},
		// A hair under 100: %.1f would round this to "100.0", a column away
		// from the whole-dollar branch that starts one cent later — the same
		// artifact tokenCount avoids at its own M cutover.
		{99.96, "$100"},
		{100, "$100"},
		{134, "$134"},
	} {
		if got := costLabel(tc.c); got != tc.want {
			t.Errorf("costLabel(%v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

// A log that exists but has not reached a tool call keeps its second line:
// the sparkline is still a reading, and an agent that has only been thinking
// has done nothing to name. The line goes only when there is no log at all.
func TestRunLineHoldsItsPlaceBeforeTheFirstCall(t *testing.T) {
	r := workRow{lane: 1, since: time.Now(), heard: time.Now(),
		rate: []int{0, 1, 2, 0, 1, 0, 0, 3}}
	line := ansi.Strip(runLine(r, 60))
	if !strings.ContainsAny(line, "▁█") {
		t.Fatalf("a run with no call yet lost its line: %q", line)
	}
	if strings.Contains(line, "$") {
		t.Fatalf("a run that has called nothing drew a call: %q", line)
	}
	r.heard = time.Time{}
	if got := runLine(r, 60); got != "" {
		t.Fatalf("a lane with no log at all drew %q", got)
	}
}

// Done-when: the sparkline is sized by the row it is drawn on, not by a
// constant. On the full-width list — the panel the closed pane leaves —
// a row draws the whole history the ring holds, a quarter of an hour of it;
// beside an open pane the same row draws the recent end of that history at
// the same resolution, since narrowing the panel costs buckets and never
// changes what one covers.
func TestTheSparklineTakesTheWidthItIsGiven(t *testing.T) {
	rate := make([]int, sparkCells)
	for i := range rate {
		rate[i] = i % 4
	}
	r := workRow{lane: 1, since: time.Now().Add(-65 * time.Minute),
		heard: time.Now().Add(-12*time.Minute - 30*time.Second), rate: rate}

	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = resized.(model)

	full := drawnCells(t, r, padList.inner(m.geometry().sideW))
	if full != sparkCells {
		t.Fatalf("a full-width row draws %d of the %d buckets the ring holds", full, sparkCells)
	}
	if span := time.Duration(full) * sparkBucket; span < 10*time.Minute {
		t.Fatalf("a full-width row reaches back %s, want at least ten minutes", span)
	}

	m = openMain(t, m)
	beside := drawnCells(t, r, padList.inner(m.geometry().sideW))
	if beside == 0 || beside >= full {
		t.Fatalf("beside the pane a row draws %d buckets, want some but fewer than %d", beside, full)
	}
	// The recent end, not the old one: a row too narrow for the history
	// drops what has already been read, never what just happened.
	want := sparkline(rate[len(rate)-beside:])
	if got := sparkOf(ansi.Strip(runLine(r, padList.inner(m.geometry().sideW)))); got != want {
		t.Fatalf("the narrow row drew %q, want the newest %d buckets %q", got, beside, want)
	}
}

// drawnCells is how many sparkline bars a row's second line renders at a
// given panel width.
func drawnCells(t *testing.T, r workRow, width int) int {
	t.Helper()
	return len([]rune(sparkOf(ansi.Strip(runLine(r, width)))))
}

// sparkOf picks the bars out of a rendered line: every rune on it that is
// one of the ramp's.
func sparkOf(line string) string {
	var b strings.Builder
	for _, c := range line {
		if strings.ContainsRune(string(sparkBars), c) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// Done-when, on the row itself: a run adopted from a previous lerp draws the
// history its log records rather than the short line of one that just
// started. Quitting under live agents and opening again used to hand an
// hour-old run the fresh line of a ten-second-old one — the numbers were
// right and the shape, which is the part being read, was the one thing a
// restart lost.
func TestAdoptedRunDrawsTheHistoryItsLogDates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	// A call from before the ring's whole span, then one a minute ago: the
	// run is older than anything this process watched, and its log says so.
	writeLog(t, path, []byte(
		datedCall(time.Now().Add(-20*time.Minute), "msg_01", "/a/old.go", 1000)+
			datedCall(time.Now().Add(-time.Minute), "msg_02", "/a/recent.go", 500)))
	m, _, _ := newTestModel(t, 3)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = fillBoard(t, resized.(model), 3)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r1", Lane: 1,
		TicketID: "t0", Ticket: "QUEUED-1", Queue: "implement", LogPath: path,
		StartedAt: time.Now().Add(-time.Hour)}})
	m = update(t, m, pollMsg{})
	m = update(t, m, keyMsg("2"))

	g := m.geometry()
	lines := strings.Split(ansi.Strip(m.workPanel(g.sideW, g.workH)), "\n")
	at := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "QUEUED-1") })
	if at < 0 || at+1 >= len(lines) {
		t.Fatalf("the adopted row has no reading under it:\n%s", strings.Join(lines, "\n"))
	}
	reading := lines[at+1]
	// The whole ring, where a run that really did just start draws the
	// single bucket it has: the run predates all of it and the log proves it.
	if drawn := len([]rune(sparkOf(reading))); drawn != sparkCells {
		t.Fatalf("the adopted row draws %d buckets on a full-width panel, want %d: %q",
			drawn, sparkCells, reading)
	}
	// The pickup lost nothing the log carries: the run's own last call and
	// its whole spend, unhedged.
	if !strings.Contains(reading, "recent.go") {
		t.Fatalf("the adopted row does not name the run's last call: %q", reading)
	}
	if first := lines[at]; !strings.Contains(first, "1.5k tok") || strings.Contains(first, "≥") {
		t.Fatalf("the adopted row does not carry the run's own total: %q", first)
	}
}

// An adopted run whose log dates nothing has no history to draw, and none is
// invented for it: the line starts as short as a fresh run's and grows from
// the pickup, exactly as if the run had just started.
func TestAdoptedRunWithAnUndatedLogDrawsAFreshLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	writeLog(t, path, []byte(strings.Repeat("an hour of work\n", 200)))
	m, _, _ := newTestModel(t, 3)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = fillBoard(t, resized.(model), 3)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r1", Lane: 1,
		TicketID: "t0", Ticket: "QUEUED-1", Queue: "implement", LogPath: path,
		StartedAt: time.Now().Add(-time.Hour)}})
	m = update(t, m, pollMsg{})
	m = update(t, m, keyMsg("2"))

	g := m.geometry()
	lines := strings.Split(ansi.Strip(m.workPanel(g.sideW, g.workH)), "\n")
	at := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "QUEUED-1") })
	if at < 0 || at+1 >= len(lines) {
		t.Fatalf("the adopted row has no reading under it:\n%s", strings.Join(lines, "\n"))
	}
	if drawn := len([]rune(sparkOf(lines[at+1]))); drawn != 1 {
		t.Fatalf("an undated log drew %d buckets of history nobody dated: %q",
			drawn, lines[at+1])
	}
}

// A squeezed panel cuts a row's second line first, so the line that survives
// has to carry what the row carried before this one existed: the clock.
func TestSqueezedRunRowKeepsItsClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	writeLog(t, path, []byte("agent at work\n"))
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 18}, {120, 21}} {
		m, _, _ := newTestModel(t, 3)
		resized, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m = fillBoard(t, resized.(model), 40)
		m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
			TicketID: "t0", Ticket: "QUEUED-1", Queue: "implement", LogPath: path,
			StartedAt: time.Now().Add(-90 * time.Second)}})
		m = update(t, m, pollMsg{})
		m = update(t, m, keyMsg("2"))
		g := m.geometry()
		panel := ansi.Strip(m.workPanel(g.sideW, g.workH))
		at := slices.IndexFunc(strings.Split(panel, "\n"), func(l string) bool {
			return strings.Contains(l, "QUEUED-1")
		})
		if at < 0 {
			t.Fatalf("%dx%d: the running row is not on the panel:\n%s", size.w, size.h, panel)
		}
		if line := strings.Split(panel, "\n")[at]; !strings.Contains(line, "1m30s") {
			t.Fatalf("%dx%d: the running row lost its clock: %q\n%s", size.w, size.h, line, panel)
		}
	}
}

// A log's modification time comes from the filesystem's clock, which may sit
// a moment ahead of this process's. "-1s ago" would read as a bug in the
// board rather than as the skew it is.
func TestElapsedDoesNotRunBackwards(t *testing.T) {
	if got := elapsed(time.Now().Add(2 * time.Second)); got != "0s" {
		t.Fatalf("a time in the future reads as %q, want 0s", got)
	}
}

// Force-start is the work panel's one write: S on the selected queued
// ticket sends that row's ticket ID and reports the start on the status bar.
func TestForceStartKeySendsTheSelectedTicket(t *testing.T) {
	m, _, starter := newStartingTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "t1", Identifier: "LERP-1", Title: "first", Assigned: true},
			{ID: "t2", Identifier: "LERP-2", Title: "the one to force", Eligible: true},
		}},
	}}})
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("down"))
	if r := m.selectedWork(); r == nil || r.ticketID != "t2" {
		t.Fatalf("selected row = %+v, want the second ticket", r)
	}

	m, cmd := updateCmd(t, m, keyMsg("S"))
	if cmd == nil {
		t.Fatal("S on a queued row produced no command")
	}
	msg := cmd()
	if got := starter.started(); len(got) != 1 || got[0] != "t2" {
		t.Fatalf("force-started tickets = %v, want exactly the selected one", got)
	}
	m = update(t, m, msg)
	if !strings.Contains(m.View(), "force-started LERP-2") {
		t.Fatalf("status bar does not report the force-start:\n%s", m.View())
	}
}

// Every refusal is the reconciler's — the TUI gates nothing — so a refusal
// has to arrive somewhere the operator will read it.
func TestForceStartRefusalLandsOnTheStatusBar(t *testing.T) {
	m, _, starter := newStartingTestModel(t, 1)
	starter.err = errors.New(`force-start LERP-2: blocked by LERP-1`)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "t2", Identifier: "LERP-2", Title: "gated work", BlockedBy: []string{"LERP-1"}},
		}},
	}}})
	m = update(t, m, keyMsg("2"))
	m, cmd := updateCmd(t, m, keyMsg("S"))
	if cmd == nil {
		t.Fatal("S on a queued row produced no command")
	}
	m = update(t, m, cmd())
	if !strings.Contains(m.View(), "blocked by LERP-1") {
		t.Fatalf("status bar does not carry the refusal:\n%s", m.View())
	}
	if m.lastErr == "" {
		t.Errorf("refusal did not land on lastErr: %+v", m.notes)
	}
}

// The work panel's key, and only the work panel's: S with the inbox focused
// is not a promote by another name.
func TestForceStartKeyIsInertOnTheInbox(t *testing.T) {
	m, _, starter := newStartingTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-7", TicketID: "id-7", Title: "waiting", Status: "Plan Review"},
	}}})
	m = update(t, m, keyMsg("1"))
	if _, cmd := updateCmd(t, m, keyMsg("S")); cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("S on the inbox produced %T", msg)
		}
	}
	if got := starter.started(); len(got) != 0 {
		t.Fatalf("S on the inbox force-started %v, want nothing", got)
	}
}

// A key nobody can discover may as well not exist: the binding renders from
// the keymap, so the overlay documents it for free.
func TestForceStartIsInTheHelpOverlay(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("?"))
	view := m.View()
	if !strings.Contains(view, "start past the limit") {
		t.Fatalf("the force-start key is missing from the help overlay:\n%s", view)
	}
}

// Capacity is one label in two places, and it has to be able to say the
// board is over the limit — the state force-start puts it in, and the state
// an adopted run from a bigger lerp arrives in.
func TestCapacityLabelReportsRunsAboveTheLimit(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "t1", Ticket: "LERP-1", Queue: "implement", LogPath: "/dev/null"}})
	if got := m.capacityLabel(); got != "1/1 running" {
		t.Fatalf("capacity label within the limit = %q", got)
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r2", Lane: 2,
		TicketID: "t2", Ticket: "LERP-2", Queue: "implement", LogPath: "/dev/null"}})
	if got := m.capacityLabel(); got != "1/1 running · +1 over" {
		t.Fatalf("capacity label over the limit = %q, want the fraction plus the overage", got)
	}
	if !strings.Contains(m.View(), "1/1 running · +1 over") {
		t.Fatalf("the screen does not say the board is over capacity:\n%s", m.View())
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r2", Lane: 2,
		TicketID: "t2", Ticket: "LERP-2", Queue: "implement", ExitCode: 0}})
	if got := m.capacityLabel(); got != "1/1 running" {
		t.Fatalf("capacity label after the forced run ended = %q, want the overage gone", got)
	}
}

// The fraction is the loop's own arithmetic, not a count of lane numbers in
// range. When a lane inside the limit frees while a forced run is still up
// above it, freeLanes still returns nothing — so the label must still read
// full. Counting only lanes 1..N here said "1/2 running", which is a lane
// the operator would wait on and never get.
func TestCapacityLabelStaysFullWhileAForcedRunHoldsTheBudget(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	for _, ev := range []loop.Event{
		{Type: loop.EventStarted, RunID: "r1", Lane: 1, TicketID: "t1", Ticket: "LERP-1", Queue: "implement", LogPath: "/dev/null"},
		{Type: loop.EventStarted, RunID: "r2", Lane: 2, TicketID: "t2", Ticket: "LERP-2", Queue: "implement", LogPath: "/dev/null"},
		{Type: loop.EventStarted, RunID: "r3", Lane: 3, TicketID: "t3", Ticket: "LERP-3", Queue: "implement", LogPath: "/dev/null"},
	} {
		m = update(t, m, eventMsg{ev: ev})
	}
	if got := m.capacityLabel(); got != "2/2 running · +1 over" {
		t.Fatalf("capacity label with a forced run past N = %q", got)
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r1", Lane: 1,
		TicketID: "t1", Ticket: "LERP-1", Queue: "implement", ExitCode: 0}})
	if got := m.capacityLabel(); got != "2/2 running" {
		t.Fatalf("capacity label after a lane inside the limit freed = %q, want it still full: "+
			"two runs are live against a budget of two, so nothing can start", got)
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventExited, RunID: "r3", Lane: 3,
		TicketID: "t3", Ticket: "LERP-3", Queue: "implement", ExitCode: 0}})
	if got := m.capacityLabel(); got != "1/2 running" {
		t.Fatalf("capacity label once the forced run ended = %q, want a lane free again", got)
	}
}

// Done-when: an open pane is a surface tab reaches, and the chrome says
// which surface has the keys. The panels have 1 and 2; the pane has the
// cycle it is already in exactly when it is open.
func TestTabPutsTheKeysInTheOpenPane(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = openMain(t, m)
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "the body of the first"}, nil, reader)
	if m.mainFocused() {
		t.Fatal("the pane took the keys the moment it opened; enter opens, it does not move focus")
	}

	m = update(t, m, keyMsg("tab"))
	if !m.mainFocused() {
		t.Fatal("tab from a panel with an open pane did not put the keys in it")
	}
	if m.focus != panelAttention {
		t.Fatalf("the keys moving into the pane moved the panel focus to %v: "+
			"the pane is the inbox's lens, and it stays the inbox's lens", m.focus)
	}
	// Wide layout: the two panels and the pane share every line, so the top
	// border carries both boxes — the inbox's on the left, the pane's on the
	// right — and exactly one of them is heavy.
	top := strings.Split(m.View(), "\n")[0]
	if !strings.HasPrefix(top, "╭") {
		t.Fatalf("the inbox kept the heavy box after the keys left it: %q", top)
	}
	if !strings.HasSuffix(top, "┓") {
		t.Fatalf("the pane holds the keys but not the heavy box: %q", top)
	}
	// Non-goal: the pane still follows the selected row, so that row is
	// still marked while the keys are in what it opened.
	if !strings.Contains(m.View(), "▸ ") {
		t.Fatalf("the inbox stopped marking the row the pane is reading:\n%s", m.View())
	}

	// And on round the cycle: the pane, then the other panel — whose own
	// pane is shut, so the cycle there is two surfaces and not three.
	m = update(t, m, keyMsg("tab"))
	if m.focus != panelWork || m.mainFocused() {
		t.Fatalf("tab out of the pane landed on %v with the keys in the pane %v, want work's list",
			m.focus, m.mainFocused())
	}
	m = update(t, m, keyMsg("tab"))
	if m.focus != panelAttention || m.mainFocused() {
		t.Fatalf("tab past a panel with no pane open landed on %v/%v, want the inbox's list",
			m.focus, m.mainFocused())
	}
}

// Done-when: shift+tab is tab's exact inverse, which is what makes a third
// surface safe to add to the cycle — stepping back off a panel lands in the
// pane that panel has open, not past it.
func TestShiftTabWalksTheCycleBackwards(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = openMain(t, m)
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "the body of the first"}, nil, reader)

	type surface struct {
		focus panel
		pane  bool
	}
	forward := []surface{{panelAttention, true}, {panelWork, false}, {panelAttention, false}}
	for i, want := range forward {
		m = update(t, m, keyMsg("tab"))
		if got := (surface{m.focus, m.mainFocused()}); got != want {
			t.Fatalf("tab %d landed on %+v, want %+v", i+1, got, want)
		}
	}
	for i := len(forward) - 2; i >= 0; i-- {
		m = update(t, m, keyMsg("shift+tab"))
		if got := (surface{m.focus, m.mainFocused()}); got != forward[i] {
			t.Fatalf("shift+tab back to step %d landed on %+v, want %+v", i+1, got, forward[i])
		}
	}
}

// unband strips the selection band back out of a rendered line, leaving the
// row's own styling. lipgloss renders an underlined span one rune at a time,
// so a banded row carries a re-open between the runes of a search match; an
// assertion about the mark itself has to look past them.
func unband(line string) string {
	if open := bandOpen(); open != "" {
		return strings.ReplaceAll(line, open, "")
	}
	return line
}

// assertBanded checks one line is banded end to end: it opens with the band,
// it is padded out to the panel's width, and every reset the row's own spans
// emit is followed by a re-open. The last is the whole difference between
// this and wrapping a background around the outside — that stops at the
// row's first coloured cell and leaves the rest of the line bare.
func assertBanded(t *testing.T, line string, width int) {
	t.Helper()
	open := bandOpen()
	if open == "" {
		t.Fatal("the profile renders no colour: forceColour first")
	}
	if !strings.HasPrefix(line, open) {
		t.Fatalf("the line does not open with the band:\n%q", line)
	}
	if got := lipgloss.Width(line); got != width {
		t.Fatalf("the band runs %d columns, the panel is %d wide:\n%q", got, width, line)
	}
	for rest := line; ; {
		i := strings.Index(rest, ansiReset)
		if i < 0 {
			t.Fatalf("the band never closes:\n%q", line)
		}
		if rest = rest[i+len(ansiReset):]; rest == "" {
			return // the band's own closing reset, at the end of the line
		}
		if !strings.HasPrefix(rest, open) {
			t.Fatalf("the band stops at a span the row draws, and the line goes bare from there:\n%q", line)
		}
	}
}

// Done-when: j/k scroll the pane a line at a time while it holds the keys —
// the movement the page keys never had — and the tail follows the same rule
// the page keys follow: a line off the bottom stops it, a line back on
// resumes it. The selection they would otherwise move stays where it is.
func TestTheFocusedPaneScrollsALineAtATime(t *testing.T) {
	log := filepath.Join(t.TempDir(), "one.log")
	writeLog(t, log, []byte(strings.Repeat("a line of agent output\n", 200)))

	m, _, _ := newTestModel(t, 2)
	m = update(t, m, keyMsg("2"))
	for _, ev := range []loop.Event{
		{Type: loop.EventStarted, RunID: "r1", Lane: 1, TicketID: "id-1", Ticket: "LERP-1", Queue: "plan", LogPath: log},
		{Type: loop.EventStarted, RunID: "r2", Lane: 2, TicketID: "id-2", Ticket: "LERP-2", Queue: "plan", LogPath: log},
	} {
		m = update(t, m, eventMsg{ev: ev})
	}
	m = openMain(t, m)
	m = update(t, m, keyMsg("tab"))
	if !m.mainFocused() {
		t.Fatal("tab did not put the keys in the log pane")
	}
	bottom, sel := m.vp.YOffset, m.workSel

	m = update(t, m, keyMsg("k"))
	if got := m.vp.YOffset; got != bottom-1 {
		t.Fatalf("k moved the pane to %d, want one line up from %d", got, bottom)
	}
	if m.follow {
		t.Fatal("a line back off the bottom left the tail following")
	}
	if m.workSel != sel {
		t.Fatalf("k moved the work selection to %q while the keys were in the pane", m.workSel)
	}

	m = update(t, m, keyMsg("j"))
	if got := m.vp.YOffset; got != bottom {
		t.Fatalf("j moved the pane to %d, want back to %d", got, bottom)
	}
	if !m.follow {
		t.Fatal("a line back onto the bottom did not pick the tail up again")
	}

	// And with the keys back on the list — shift+tab, the way back out of
	// the pane the panel opened — j is the row key it has always been.
	m = update(t, m, keyMsg("shift+tab"))
	if m.focus != panelWork || m.mainFocused() {
		t.Fatalf("shift+tab out of the pane landed on %v/%v, want work's list",
			m.focus, m.mainFocused())
	}
	m = update(t, m, keyMsg("j"))
	if m.workSel == sel {
		t.Fatalf("j on the list did not move the selection off %q", sel)
	}
}

// Done-when: esc means what it always meant. It closes the pane, and the
// keys it was holding go back to the list — including for the next pane the
// operator opens, which enter opens to read, not to move focus into.
func TestClosingThePaneHandsTheKeysBack(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = openMain(t, m)
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "the body of the first"}, nil, reader)
	m = update(t, m, keyMsg("tab"))

	m = update(t, m, keyMsg("esc"))
	if m.mainOpen() {
		t.Fatal("esc did not close the pane the keys were in")
	}
	if m.mainFocused() {
		t.Fatal("the keys stayed in a closed pane")
	}
	m = update(t, m, keyMsg("j"))
	if m.attnSel != 1 {
		t.Fatalf("j after esc moved the selection to %d, want the second row", m.attnSel)
	}

	m = update(t, m, keyMsg("enter"))
	if m.mainFocused() {
		t.Fatalf("enter reopened the pane with the keys already in it: " +
			"enter opens the pane, tab is what moves into it")
	}
}

// Done-when: the ? overlay borrows the pane, not the keys in it. It draws
// in that same viewport, so the keys stay where the operator put them and
// move what the pane is holding — which is the help while it is up. The
// panel still lit behind the overlay is what says the keys are on the list,
// so the two states are not the same screen.
//
// The alternative — falling through to the list while the overlay covers it
// — walks a selection nobody can see, and the pane comes back reading a
// ticket the operator never chose.
func TestTheOverlayBorrowsThePaneNotItsKeys(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = openMain(t, m)
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "the body of the first"}, nil, reader)
	m = update(t, m, keyMsg("tab"))
	// Short enough that the overlay is longer than the pane, so scrolling it
	// is a fact and not an accident of how many bindings there are.
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 18})

	m = update(t, m, keyMsg("?"))
	if !m.mainFocused() {
		t.Fatal("the overlay took the pane's keys as well as its room")
	}
	top := strings.Split(m.View(), "\n")[0]
	if !strings.HasPrefix(top, "╭") {
		t.Fatalf("the inbox is lit behind the overlay while the keys are in the pane: %q", top)
	}
	if m.vp.TotalLineCount() <= m.vp.Height {
		t.Fatalf("the overlay fits its pane (%d lines in %d rows): nothing here scrolls",
			m.vp.TotalLineCount(), m.vp.Height)
	}

	m = update(t, m, keyMsg("j"))
	if m.vp.YOffset != 1 {
		t.Fatalf("j behind the overlay moved the pane to %d, want one line into the help",
			m.vp.YOffset)
	}
	if m.attnSel != 0 {
		t.Fatalf("j behind the overlay walked the hidden list to row %d, "+
			"re-aiming the pane the keys were in", m.attnSel)
	}

	m = update(t, m, keyMsg("?"))
	if !m.mainFocused() {
		t.Fatal("closing the overlay moved the keys the operator had left in the pane")
	}
	if !strings.Contains(m.View(), "the body of the first") {
		t.Fatalf("the pane came back on another row:\n%s", m.View())
	}

	// And with the keys on the list, the overlay is what it always was: the
	// selection moves behind it, and the pane comes back on the new row.
	m = update(t, m, keyMsg("shift+tab"))
	if m.focus != panelAttention || m.mainFocused() {
		t.Fatalf("the keys are not back on the inbox list: %v/%v", m.focus, m.mainFocused())
	}
	m = update(t, m, keyMsg("?"))
	if got := strings.Split(m.View(), "\n")[0]; !strings.HasPrefix(got, "┏") {
		t.Fatalf("the inbox is dark behind the overlay while the keys are on it: %q", got)
	}
	m = update(t, m, keyMsg("j"))
	if m.attnSel != 1 {
		t.Fatalf("j behind the overlay from the list moved to row %d, want the second row: "+
			"re-aiming the lens behind the overlay is what it has always done", m.attnSel)
	}
}

// Done-when: tab reaches a pane that is a lens on a row. A panel with no row
// under its cursor has a pane holding a state sentence — the first pass has
// not landed, or the inbox is empty and that is the goal state — and the
// keys would arrive there with nothing to scroll and no selection left to
// move, which is the one thing rowKeys exists to prevent.
func TestTabSkipsAPaneWithNoRowUnderIt(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	// A pass that reported an empty queue, not a pass that has not reported:
	// the splash covers the second one, and enter does not open a pane under
	// it. What is left is a work panel with a queue and no tickets in it.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo"},
	}}})
	m = update(t, m, keyMsg("2"))
	m = openMain(t, m)
	if m.hasRow(panelWork) {
		t.Fatalf("the work panel has a row after an empty pass:\n%s", m.View())
	}
	m = update(t, m, keyMsg("tab"))
	if m.mainFocused() {
		t.Fatal("tab put the keys in a pane with no row under it")
	}
	if m.focus != panelAttention {
		t.Fatalf("tab past the rowless pane landed on %v, want the inbox", m.focus)
	}

	// And it reaches the same pane the moment a pass gives it a row.
	m = update(t, m, keyMsg("2"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "id-9", Identifier: "LERP-9", Title: "queued", Eligible: true},
		}}}}})
	m = update(t, m, keyMsg("tab"))
	if !m.mainFocused() {
		t.Fatalf("tab did not reach the pane once it had a row to be a lens on:\n%s", m.View())
	}

	// Only arriving asks. A panel that empties under a pane the operator is
	// already reading keeps its keys — a pass whose Linear calls all failed
	// reports an empty queue exactly like an empty one, and a rule that let
	// a read move the keyboard would move it every interval an outage
	// lasted, then again on the way back, into a pane aimed at whatever
	// queued next.
	for _, ev := range []loop.Event{
		{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
			{Team: "LERP", Name: "implement", Status: "Todo"}}},
		{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
			{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
				{ID: "id-10", Identifier: "LERP-10", Title: "queued later", Eligible: true},
			}}}},
	} {
		m = update(t, m, eventMsg{ev: ev})
		if !m.mainFocused() {
			t.Fatalf("a pass moved the keys out of the pane the operator put them in:\n%s",
				m.View())
		}
	}
}

// Done-when: tab behind the ? overlay means what it always meant. The
// overlay leaves the keys where they are — see the test above — but it
// covers the pane, and a tab that moved them into a surface the operator
// cannot see them arrive in would land them somewhere they never chose:
// the overlay closes, and j scrolls a ticket body instead of walking rows.
func TestTabBehindTheOverlayStillMeansTheNextPanel(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = openMain(t, m)
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "the body of the first"}, nil, reader)

	m = update(t, m, keyMsg("?"))
	m = update(t, m, keyMsg("tab"))
	if m.mainFocused() {
		t.Fatal("tab behind the overlay put the keys in a pane nobody can see")
	}
	if m.focus != panelWork {
		t.Fatalf("tab behind the overlay landed on %v, want the work panel", m.focus)
	}
	m = update(t, m, keyMsg("?"))
	if m.mainFocused() || m.focus != panelWork {
		t.Fatalf("closing the overlay left the keys at %v/%v", m.focus, m.mainFocused())
	}
}

// Done-when: the bar names the way out of the surface the keys have just
// landed in — and esc still says "close", because from in the pane it still
// closes rather than having grown a second meaning.
func TestTheBarNamesTheWayOutOfThePane(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = openMain(t, m)
	m = selectAndRead(t, m, 0, linear.IssueDetail{Body: "the body of the first"}, nil, reader)
	if got := m.statusBar(); !strings.Contains(got, "esc close") || strings.Contains(got, "tab next") {
		t.Fatalf("the bar offers the pane's own keys before the pane has them:\n%s", got)
	}

	m = update(t, m, keyMsg("tab"))
	got := m.statusBar()
	if !strings.Contains(got, "tab next") || !strings.Contains(got, "esc close") {
		t.Fatalf("the bar does not name the way out of the pane:\n%s", got)
	}
}

// Done-when: the selected row reads as one object across the full width of
// the panel — the whole row, not the two characters of the marker — and
// nothing else on the panel carries the band.
func TestSelectedInboxRowTakesTheWholeWidth(t *testing.T) {
	forceColour(t)
	m, _, _ := newTestModel(t, 1)
	// The selected row is the one with a coloured cell of its own: an
	// "Urgent" priority mid-row is exactly where a band wrapped around the
	// outside would stop.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "first", Status: "Todo", Priority: 1},
		{Ticket: "LERP-2", Title: "second", Status: "Todo", BlockedBy: []string{"LERP-1"}},
	}}})
	width := padList.inner(m.geometry().sideW)
	rows, cur := m.attentionRows(width)
	if cur.at < 0 {
		t.Fatalf("the inbox has no selection to band: %q", rows)
	}
	assertBanded(t, rows[cur.at], width)
	for i, r := range rows {
		if i != cur.at && strings.Contains(r, bandOpen()) {
			t.Fatalf("row %d is banded with the cursor on row %d: %q", i, cur.at, r)
		}
	}
}

// Done-when: a two-line running row highlights both its lines, and a row a
// squeezed panel cuts down to one highlights the one that survived.
func TestSelectionBandCoversEveryLineTheRowDrew(t *testing.T) {
	forceColour(t)
	path := filepath.Join(t.TempDir(), "run.log")
	writeLog(t, path, []byte(
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n"))

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "id-1", Identifier: "LERP-1", Title: "one", Assigned: true}}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "implement", LogPath: path,
		StartedAt: time.Now().Add(-90 * time.Second)}})
	m = update(t, m, pollMsg{})
	m = update(t, m, keyMsg("2"))

	g := m.geometry()
	width := padList.inner(g.sideW)
	rows, cur := m.workListRows(width)
	if cur.span != 2 {
		t.Fatalf("the running row draws %d lines, want the row and its reading: %q", cur.span, rows)
	}
	for i := cur.at; i < cur.at+cur.span; i++ {
		assertBanded(t, rows[i], width)
	}

	// And down the heights the panel actually draws at: whichever of the
	// row's lines survive the cut carry the band, and a band on the reading
	// line alone would be marking a run under a name that is no longer on
	// screen. The heights are walked rather than picked so the cut and the
	// whole row are both covered.
	cut := false
	for h := 4; h <= 12; h++ {
		lines := strings.Split(m.workPanel(g.sideW, h), "\n")
		at := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "LERP-1") })
		if at < 0 {
			continue
		}
		if !strings.Contains(lines[at], bandOpen()) {
			t.Fatalf("h=%d: the selected row lost its band:\n%s", h, m.workPanel(g.sideW, h))
		}
		if at+1 < len(lines) && strings.Contains(lines[at+1], "heard") {
			if !strings.Contains(lines[at+1], bandOpen()) {
				t.Fatalf("h=%d: the row's reading is outside the band:\n%s", h, m.workPanel(g.sideW, h))
			}
			continue
		}
		cut = true
	}
	if !cut {
		t.Fatal("no height cut the reading line, so nothing here tested the cut row")
	}
}

// A row a lane does not hold draws one line, and the band is that line — the
// band covers what the row drew, never a blank line under it.
func TestAWaitingRowBandsTheOneLineItDraws(t *testing.T) {
	forceColour(t)
	m, _, _ := newTestModel(t, 1)
	lines := m.workRowLines(workRow{ticket: "LERP-1", title: "one", pos: 1, of: 3}, true, 60)
	if len(lines) != 1 {
		t.Fatalf("a waiting row drew %d lines: %q", len(lines), lines)
	}
	assertBanded(t, lines[0], 60)
}

// The band is the cursor's, and the cursor is in the focused panel: an
// unfocused list must not draw a second one for the operator to read. Both
// panels, and the inbox especially — it is the one lerp opens on, so it is
// the one that spends most of a session with the focus somewhere else.
func TestAnUnfocusedPanelDrawsNoBand(t *testing.T) {
	forceColour(t)
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "id-1", Identifier: "LERP-1", Title: "one"}}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-2", Title: "waiting", Status: "Todo"},
	}}})
	g := m.geometry()
	for _, tc := range []struct {
		name  string
		focus panel
		draw  func() string
	}{
		{"work", panelAttention, func() string { return m.workPanel(g.sideW, g.workH) }},
		{"inbox", panelWork, func() string { return m.attentionPanel(g.sideW, g.attnH) }},
	} {
		m.focus = tc.focus
		if panel := tc.draw(); strings.Contains(panel, bandOpen()) {
			t.Fatalf("the unfocused %s panel bands a row:\n%s", tc.name, panel)
		}
	}
}

// The band is a background and nothing else: a foreground would paint over
// the very colours a row uses to say what it is. Adaptive, because one
// picked tint reads as a band on a dark terminal and a smudge on a light
// one — and spelled out per profile, because a background is the one thing
// lipgloss's own degradation cannot approximate quietly.
func TestTheSelectionBandIsBackgroundOnlyAndAdaptive(t *testing.T) {
	if fg := styleSelected.GetForeground(); fg != lipgloss.TerminalColor(lipgloss.NoColor{}) {
		t.Fatalf("the selection band paints a foreground %v over the row's own colours", fg)
	}
	if colorSelected.Light == colorSelected.Dark {
		t.Fatalf("the selection band is not adaptive: %+v", colorSelected)
	}
	for _, c := range []struct {
		name string
		lipgloss.CompleteColor
	}{{"light", colorSelected.Light}, {"dark", colorSelected.Dark}} {
		if c.TrueColor == "" || c.ANSI256 == "" {
			t.Fatalf("the %s band has no tint of its own to fall back to: %+v", c.name, c.CompleteColor)
		}
		// Empty on purpose: 16 colours holds nothing quiet enough to lay
		// under a row, so that profile draws no band and keeps the marker.
		if c.ANSI != "" {
			t.Fatalf("the %s band names a 16-colour tint %q; there is no quiet one", c.name, c.ANSI)
		}
	}
	// And which tint goes to which terminal, which is the whole of being
	// adaptive. Swapped, the dark terminal's band is near-white and a
	// selected row's identifier — bold, in the terminal's own text colour —
	// vanishes into it at about 1.06:1, so the test is on the tints
	// themselves and not on which field the renderer read: comparing the
	// rendered band against the field it came from would pass either way
	// round.
	if l := brightness(t, colorSelected.Dark.TrueColor); l > 0.35 {
		t.Fatalf("the dark terminal's band is %.2f bright: it is a light tint on a dark screen", l)
	}
	if l := brightness(t, colorSelected.Light.TrueColor); l < 0.65 {
		t.Fatalf("the light terminal's band is %.2f bright: it is a dark tint on a light screen", l)
	}
	// The 256-colour pair rides the grey ramp, where the index is the
	// lightness, so the same reading holds by number.
	if index(t, colorSelected.Dark.ANSI256) >= index(t, colorSelected.Light.ANSI256) {
		t.Fatalf("the 256-colour tints run the wrong way: dark %s, light %s",
			colorSelected.Dark.ANSI256, colorSelected.Light.ANSI256)
	}
	// And the renderer reads the field that matches the terminal.
	profile, dark := lipgloss.ColorProfile(), lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetColorProfile(profile); lipgloss.SetHasDarkBackground(dark) })
	lipgloss.SetColorProfile(termenv.TrueColor)
	for _, tc := range []struct {
		dark bool
		tint string
	}{
		{true, colorSelected.Dark.TrueColor},
		{false, colorSelected.Light.TrueColor},
	} {
		lipgloss.SetHasDarkBackground(tc.dark)
		want := lipgloss.NewStyle().Background(lipgloss.Color(tc.tint)).Render("")
		if got := styleSelected.Render(""); got != want {
			t.Fatalf("on a dark=%v terminal the band renders %q, want the %s tint %q",
				tc.dark, got, tc.tint, want)
		}
	}
}

// brightness is how light a #rrggbb tint reads, 0 for black and 1 for white.
// Perceived rather than plain average, which is the difference between
// calling a saturated blue dark and calling it light.
func brightness(t *testing.T, hex string) float64 {
	t.Helper()
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil || len(hex) != 7 {
		t.Fatalf("%q is not a #rrggbb tint: %v", hex, err)
	}
	r, g, b := float64((v>>16)&0xff), float64((v>>8)&0xff), float64(v&0xff)
	return (0.299*r + 0.587*g + 0.114*b) / 255
}

// index reads a 256-colour palette slot as the number it is.
func index(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("%q is not a 256-colour index: %v", s, err)
	}
	return n
}

// Done-when: legible on both terminal backgrounds — which on a profile with
// no quiet tint to offer means no band at all. A 16-colour terminal
// quantises this background to a solid ANSI blue or magenta: a bar across
// the row that takes every faint cell on it to about 1.3:1. The ▸ marker is
// the cursor there, and it is on the row whether the band is or not.
func TestTheBandIsDrawnOnlyWhereItCanBeQuiet(t *testing.T) {
	was := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(was) })
	for _, tc := range []struct {
		profile termenv.Profile
		band    bool
	}{
		{termenv.TrueColor, true},
		{termenv.ANSI256, true},
		{termenv.ANSI, false},
		{termenv.Ascii, false},
	} {
		lipgloss.SetColorProfile(tc.profile)
		plain := marker(true) + "LERP-1 one"
		row := selectRow(plain, 40)
		if got := bandOpen() != ""; got != tc.band {
			t.Fatalf("%v: band drawn = %v, want %v", tc.profile, got, tc.band)
		}
		if !tc.band && row != padTo(plain, 40) {
			t.Fatalf("%v: the row is not the bare padded row: %q", tc.profile, row)
		}
		if !strings.HasPrefix(ansi.Strip(row), "▸ ") {
			t.Fatalf("%v: the band swallowed the marker: %q", tc.profile, row)
		}
		if got := lipgloss.Width(row); got != 40 {
			t.Fatalf("%v: the row is %d columns, want 40: %q", tc.profile, got, row)
		}
	}
}
