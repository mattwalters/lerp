package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
// and returns whatever err is set to.
type recordingPromoter struct {
	mu    sync.Mutex
	calls []promoteCall
	err   error
}

type promoteCall struct {
	ticketID string
	status   string
}

func (p *recordingPromoter) Promote(_ context.Context, ticketID, status string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, promoteCall{ticketID, status})
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

func newTestModel(t *testing.T, lanes int) (model, *countingTicker, chan loop.Event) {
	t.Helper()
	m, ticker, events := newTestModelWith(t, lanes, defaultTestStatuses, &recordingPromoter{}, &recordingReader{})
	return m, ticker, events
}

// newPromoteTestModel is newTestModel plus the recording promoter, for tests
// that drive the promote picker and need to see what it sent.
func newPromoteTestModel(t *testing.T, lanes int, statuses []string) (model, *countingTicker, chan loop.Event, *recordingPromoter) {
	t.Helper()
	promoter := &recordingPromoter{}
	m, ticker, events := newTestModelWith(t, lanes, statuses, promoter, &recordingReader{})
	return m, ticker, events, promoter
}

// newReadingTestModel is newTestModel plus the recording reader, for tests
// that drive the inbox pane's read of the selected ticket.
func newReadingTestModel(t *testing.T) (model, chan loop.Event, *recordingReader) {
	t.Helper()
	reader := &recordingReader{}
	m, _, events := newTestModelWith(t, 1, defaultTestStatuses, &recordingPromoter{}, reader)
	return m, events, reader
}

func newTestModelWith(t *testing.T, lanes int, statuses []string, promoter *recordingPromoter, reader *recordingReader) (model, *countingTicker, chan loop.Event) {
	t.Helper()
	ticker := &countingTicker{}
	events := make(chan loop.Event, 8)
	m := newModel(context.Background(), Options{
		Ticker:   ticker,
		Promoter: promoter,
		Reader:   reader,
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
	if !strings.Contains(view, "adopted") || !strings.Contains(view, "2h0m") {
		t.Fatalf("adopted row does not show the run's true age:\n%s", view)
	}
}

func TestAdoptedRunOccupiesAndFreesItsRow(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r9", Lane: 5,
		TicketID: "abcdef1234567890", Queue: "review", LogPath: "/dev/null"}})
	view := m.View()
	if !strings.Contains(view, "adopted") {
		t.Fatalf("adopted run not on the board:\n%s", view)
	}
	if len(m.workRows()) != 1 {
		t.Fatalf("panel has %d rows, want the one adopted run", len(m.workRows()))
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
	if m.focus != panelWork {
		t.Fatalf("lerp opens focused on %v, want work", m.focus)
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
	if view := m.View(); !strings.Contains(view, "claimed") {
		t.Fatalf("claimed ticket's lens does not say so:\n%s", view)
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
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "id-1", Identifier: "LERP-1", Title: "running one", Assigned: true},
			{ID: "id-2", Identifier: "LERP-2", Title: "waiting two", Eligible: true,
				URL: "https://linear.app/acme/issue/LERP-2/two"},
		}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "implement", LogPath: path}})
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

// An adopted run may sit on a lane above N — outside the capacity fraction,
// but never off the panel. Its queue is not on the board, so it keeps a
// group of its own, and it selects like any other row.
func TestAdoptedRunAboveCapacityIsOnThePanel(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "plan", Status: "Planning", Tickets: []loop.QueueTicket{
			{ID: "id-1", Identifier: "LERP-1", Title: "one", Eligible: true}}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r9", Lane: 4,
		TicketID: "abcdef1234567890", Queue: "review", LogPath: "/dev/null"}})
	view := m.View()
	for _, want := range []string{"adopted", "review · off the board", "0/1 running"} {
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
	m = update(t, m, keyMsg("1"))

	if view := m.View(); strings.Contains(view, "nothing needs you") {
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
		`claimed in "Needs Help" — no queue serves it`,
		"https://linear.app/acme/issue/LERP-42/fix",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("inbox view is missing %q:\n%s", want, view)
		}
	}

	// A later pass with nothing waiting clears the list and says so.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	view = m.View()
	if !strings.Contains(view, "nothing needs you") {
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
			Reason: `claimed in "Needs Attention" — a run failed here`},
		{Ticket: "LERP-22", TicketID: "id-22", Title: "GoReleaser: tagged releases", Status: "Backlog",
			Project: "Open-source readiness", Relevance: loop.StatusUnnamed, Priority: 2,
			Unblocks: 2, Blocks: []string{"LERP-23"}},
		{Ticket: "LERP-23", TicketID: "id-23", Title: "curl install", Status: "Backlog",
			Project: "Open-source readiness", Relevance: loop.StatusUnnamed, Priority: 3,
			Unblocks: 1, BlockedBy: []string{"LERP-22"}},
		{Ticket: "LERP-48", TicketID: "id-48", Title: "Read the ticket in the TUI", Status: "In Review",
			Project: "TUI redesign", Relevance: loop.StatusFinished, Priority: 1},
		{Ticket: "LERP-60", TicketID: "id-60", Title: "Unfiled work", Status: "Backlog",
			Relevance: loop.StatusUnnamed, Priority: 4},
		// Priority 0 is Linear's "No priority", not its highest: it must sort
		// below Low, never above Urgent. This is the only fixture carrying it,
		// and the order assertions below are what guard priorityRank.
		{Ticket: "LERP-70", TicketID: "id-70", Title: "Unprioritized work", Status: "Backlog",
			Relevance: loop.StatusUnnamed, Priority: 0},
	}}
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
	// Backlog is named by no queue and by no on_success or on_failure
	// target, so every ticket resting in one is marked; the statuses the
	// pipeline does name are not.
	if !strings.Contains(rowOf(t, panel, "LERP-22"), "⚠") {
		t.Fatalf("a status the pipeline never names is unmarked:\n%s", panel)
	}
	for _, named := range []string{"LERP-1", "LERP-48"} {
		if strings.Contains(rowOf(t, panel, named), "⚠") {
			t.Fatalf("%s rests in a status the pipeline names, but the row marks it:\n%s", named, panel)
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

// Done-when: four sort modes cycle on one key, the two grouped ones draw
// headers and the two flat ones do not, and the panel title says which is
// in force. Sorting is the only grouping control there is.
func TestInboxSortModesCycle(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})

	// Leverage is the default: what promoting frees first, then priority,
	// then the identifier — and a blocked ticket below every routable one.
	panel := m.attentionPanel(96, 14)
	want := []string{"LERP-22", "LERP-48", "LERP-1", "LERP-60", "LERP-70", "LERP-23"}
	if got := order(panel, want...); !slices.Equal(got, want) {
		t.Fatalf("leverage order = %v, want %v:\n%s", got, want, panel)
	}
	if !strings.Contains(panel, "by leverage") {
		t.Fatalf("the panel title does not name the sort mode:\n%s", panel)
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

	// Status: pipeline-relevance first — a failure route, then where a
	// clean run comes to rest, then the statuses the pipeline never names —
	// with a header per status carrying the note that explains the rank.
	m = update(t, m, keyMsg("s"))
	panel = m.attentionPanel(96, 16)
	want = []string{"LERP-1", "LERP-48", "LERP-22", "LERP-60", "LERP-70", "LERP-23"}
	if got := order(panel, want...); !slices.Equal(got, want) {
		t.Fatalf("status order = %v, want %v:\n%s", got, want, panel)
	}
	for _, note := range []string{"a run failed here", "a run finished here", "the pipeline never names it"} {
		if !strings.Contains(panel, note) {
			t.Fatalf("status headers do not carry %q:\n%s", note, panel)
		}
	}

	// Project, alphabetically, with the unfiled ticket last.
	m = update(t, m, keyMsg("s"))
	panel = m.attentionPanel(96, 14)
	want = []string{"LERP-22", "LERP-1", "LERP-23", "LERP-48", "LERP-60", "LERP-70"}
	if got := order(panel, want...); !slices.Equal(got, want) {
		t.Fatalf("project order = %v, want %v:\n%s", got, want, panel)
	}
	if !strings.Contains(panel, "TUI redesign") || !strings.Contains(panel, "no project") {
		t.Fatalf("project mode draws no project headers:\n%s", panel)
	}

	// One more press is back to the flat default, headers and all.
	m = update(t, m, keyMsg("s"))
	panel = m.attentionPanel(96, 14)
	if !strings.Contains(panel, "by leverage") {
		t.Fatalf("the sort key does not cycle back to the default:\n%s", panel)
	}
	if strings.Contains(panel, "a run failed here") || strings.Contains(panel, "no project") {
		t.Fatalf("a flat mode still draws headers:\n%s", panel)
	}
}

// Done-when: the sort key moves the rows, not the cursor. The selection is
// a ticket, so re-sorting keeps the operator on the one they were reading.
func TestSortKeepsTheSelectedTicket(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})
	m = update(t, m, keyMsg("j")) // LERP-48, second under leverage

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

// Done-when: leverage, priority and blocked-ness are readable on the row
// itself, without selecting it — and the columns elide from the right, so a
// narrow panel truncates the title first, then drops the project, and never
// costs the identifier, the leverage or the status.
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
	narrow := m.attentionPanel(44, 8)
	if strings.Contains(narrow, "Open-source readiness") {
		t.Fatalf("a narrow panel kept the project column:\n%s", narrow)
	}
	for _, want := range []string{"Urgent", "↓3", "Backlog", "LERP-22"} {
		if !strings.Contains(narrow, want) {
			t.Fatalf("a narrow panel dropped %q:\n%s", want, narrow)
		}
	}
	if strings.Contains(narrow, "GoReleaser: tagged releases") {
		t.Fatalf("a narrow panel did not truncate the title:\n%s", narrow)
	}

	// Narrower than a title-less row: the priority goes too, and the three
	// columns a routing decision cannot start without are the last things
	// standing.
	tiny := m.attentionPanel(30, 8)
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

// The status bar carries the heartbeat and the counts; the ? key swaps the
// main pane for the full keymap.
func TestStatusBarAndHelp(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	if !strings.Contains(m.View(), "WORK") {
		t.Fatalf("status bar does not name the focused panel:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "pass running") {
		t.Fatalf("status bar hides the in-flight first pass:\n%s", m.View())
	}
	m = update(t, m, tickedMsg{})
	if !strings.Contains(m.View(), "next in") {
		t.Fatalf("status bar after a pass shows no countdown:\n%s", m.View())
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "one"}, {Ticket: "LERP-2", Title: "two"},
	}}})
	if !strings.Contains(m.View(), "2 inbox") {
		t.Fatalf("status bar does not count inbox:\n%s", m.View())
	}

	m = update(t, m, keyMsg("?"))
	view := m.View()
	if !strings.Contains(view, "open in Linear") || !strings.Contains(view, "next panel") {
		t.Fatalf("help overlay is missing bindings:\n%s", view)
	}
	m = update(t, m, keyMsg("?"))
	if strings.Contains(m.View(), "next panel") {
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

// Every panel asks for the rows it will render and the focused one absorbs
// the slack: with 15 items waiting and every queue empty, inbox gets the
// whole column and renders all 15. Moving focus moves the space — there is
// no expand key, only focus.
func TestFocusedPanelTakesTheSlack(t *testing.T) {
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
	if g.workH != collapsedH {
		t.Fatalf("an empty work panel reserved a body: %d lines", g.workH)
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

	// Focus moves, and the slack moves with it: inbox falls back to the
	// rows it renders, work takes what is left.
	m = update(t, m, keyMsg("2"))
	g2 := m.geometry()
	rows, _ := m.attentionRows(g2.sideW - 2)
	if g2.attnH != len(rows)+2 {
		t.Fatalf("unfocused inbox is %d lines for %d rows", g2.attnH, len(rows))
	}
	if g2.workH <= g.workH || g2.workH != g.bodyH-g2.attnH {
		t.Fatalf("work did not take the slack on focus: %d lines", g2.workH)
	}
}

// A panel with nothing to show costs one line — its own title row — and
// takes its body back the moment the operator focuses it. Content drives
// this; no toggle.
func TestEmptyPanelsCostOneLine(t *testing.T) {
	m, _, _ := newTestModel(t, 3)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = resized.(model)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "plan", Status: "Planning"},
	}}})

	m = update(t, m, keyMsg("1"))
	view := m.View()
	if want := "[2] work · 0/3 running — nothing queued"; !strings.Contains(view, want) {
		t.Fatalf("collapsed panel is missing %q:\n%s", want, view)
	}
	if strings.Contains(view, "plan · Planning") {
		t.Fatalf("collapsed work panel still reserved a body:\n%s", view)
	}

	// Focusing a collapsed panel opens it: the queue is back, empty and
	// named, so the operator can see what would have to move to fill it.
	m = update(t, m, keyMsg("2"))
	view = m.View()
	if strings.Contains(view, "nothing queued") {
		t.Fatalf("focused work panel stayed collapsed:\n%s", view)
	}
	if !strings.Contains(view, "plan · Planning · LERP · empty") {
		t.Fatalf("focused work panel does not show its queues:\n%s", view)
	}
}

// When the two panels want more than the body between them, the unfocused
// one is squeezed to the floor before the panel being worked in gives up a
// row — and the stack still fits, so the status bar stays on screen.
func TestOverflowSqueezesTheUnfocusedPanelFirst(t *testing.T) {
	m, _, _ := newTestModel(t, 6)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = resized.(model)
	m = fillBoard(t, m, 40)

	m = update(t, m, keyMsg("1"))
	g := m.geometry()
	if got := g.attnH + g.workH; got != g.bodyH {
		t.Fatalf("overflowing stack is %d lines in a %d-line body", got, g.bodyH)
	}
	if g.workH != panelFloor {
		t.Fatalf("the unfocused panel is not at the floor: %d lines", g.workH)
	}
	if want := g.bodyH - panelFloor; g.attnH != want {
		t.Fatalf("focused inbox is %d lines, want %d", g.attnH, want)
	}
	if lines := strings.Count(m.View(), "\n") + 1; lines > 24 {
		t.Fatalf("view is %d lines tall in a 24-line window", lines)
	}

	// The squeeze follows focus, like the slack does.
	m = update(t, m, keyMsg("2"))
	g = m.geometry()
	if g.attnH != panelFloor || g.workH != g.bodyH-panelFloor {
		t.Fatalf("focus did not move the squeeze: inbox %d, work %d", g.attnH, g.workH)
	}
}

// The too-small guard is geometry's own arithmetic: at the smallest window
// each layout admits, the stack still fits the terminal, and one line less
// is refused rather than drawn over the status bar.
func TestSmallestWindowTheGuardAdmits(t *testing.T) {
	for _, tc := range []struct{ w, h int }{
		{120, 2*panelFloor + 1},
		{70, 2*panelFloor + mainFloor + 1},
	} {
		m, _, _ := newTestModel(t, 3)
		resized, _ := m.Update(tea.WindowSizeMsg{Width: tc.w, Height: tc.h})
		m = fillBoard(t, resized.(model), 20)
		view := m.View()
		if strings.Contains(view, "too small") {
			t.Fatalf("width %d: the guard refuses the height it admits", tc.w)
		}
		if lines := strings.Count(view, "\n") + 1; lines > tc.h {
			t.Fatalf("width %d: view is %d lines tall in a %d-line window:\n%s",
				tc.w, lines, tc.h, view)
		}
		resized, _ = m.Update(tea.WindowSizeMsg{Width: tc.w, Height: tc.h - 1})
		if view := resized.(model).View(); !strings.Contains(view, "too small") {
			t.Fatalf("width %d: a window below the floors rendered anyway:\n%s", tc.w, view)
		}
	}
}

// No rendered line may overflow the terminal, wide layout or stacked, from
// whichever panel has focus — the long explanatory empty states included.
func TestViewFitsTheWindow(t *testing.T) {
	for _, width := range []int{120, 100, 80, 60} {
		for _, focus := range []string{"1", "2"} {
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
			for i, line := range strings.Split(m.View(), "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("width %d, panel %s: line %d is %d cells wide:\n%s",
						width, focus, i, got, line)
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
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "plan", LogPath: one}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r2", Lane: 2,
		TicketID: "id-2", Ticket: "LERP-2", Queue: "plan", LogPath: two}})

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
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		Ticket: "LERP-1", Queue: "plan", LogPath: path}})

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
	m = update(t, m, keyMsg("1"))
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
	m = update(t, m, keyMsg("1"))
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
	// The lines the pass produced still come first, and o is still offered.
	if strings.Index(view, "unclaimed") > strings.Index(view, "the ticket body") {
		t.Fatalf("the pass's own lines no longer render first:\n%s", view)
	}
	if !strings.Contains(view, "o open in Linear") {
		t.Fatalf("the o hint is gone:\n%s", view)
	}
}

// Done-when: a comments fetch that fails never blanks the lines that work
// today, and still points at Linear.
func TestTicketDetailFailureKeepsThePaneThatWorks(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
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

// A ticket with nothing on it still reads as answered, not as still loading.
func TestTicketDetailEmptyTicket(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = selectAndRead(t, m, 0, linear.IssueDetail{}, nil, reader)

	view := m.View()
	if !strings.Contains(view, "(no description)") || !strings.Contains(view, "(no comments)") {
		t.Fatalf("an empty ticket does not read as empty:\n%s", view)
	}
}

// Done-when: a body carrying escape sequences renders inert, and one
// carrying Linear's inline issue tags renders as bare identifiers.
func TestTicketDetailRendersHostileBodyInert(t *testing.T) {
	m, _, reader := newReadingTestModel(t)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: threeWaiting()})
	m = selectAndRead(t, m, 0, linear.IssueDetail{
		Body: hostile + ` blocked by <issue id="u-1" href="https://linear.app/acme/issue/LERP-36">LERP-36</issue>`,
		Comments: []linear.Comment{
			{Author: "agent" + hostile, Body: hostile + " verdict", CreatedAt: time.Now()},
		},
	}, nil, reader)

	view := m.View()
	escapeFree(t, "inbox detail", view)
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
	m = update(t, m, keyMsg("1"))
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
// write, a screen erase, a cursor home, and a carriage return to repaint the
// row it lands on.
const hostile = "\x1b]0;pwned\x07\x1b[2J\x1b[1;1Hpwn\rme"

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
}

// The log pane carries agent output, which is legitimately colored — so it
// keeps SGR and drops everything that could move the cursor or repaint the
// chrome around it.
func TestHostileLogOutputCannotRepaintTheScreen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one.log")
	writeLog(t, path, []byte("\x1b[31mcolored output\x1b[0m\n"+hostile+"\n"))

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		Ticket: "LERP-1", Queue: "plan", LogPath: path}})

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
	m = update(t, m, openErrMsg{err: errors.New(hostile)})
	escapeFree(t, "status bar", m.View())
	m = update(t, m, promotedMsg{ticket: "LERP-1", status: "Planning", err: errors.New(hostile)})
	escapeFree(t, "status bar", m.View())
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
	m = update(t, m, keyMsg("2"))

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
