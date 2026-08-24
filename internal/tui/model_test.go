package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// that drive the needs-you pane's read of the selected ticket.
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

func TestLanesShowTheRunLifecycle(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	view := m.View()
	for _, want := range []string{"needs you", "running", "up next", "idle", "q quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("initial view is missing %q:\n%s", want, view)
		}
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})
	view = m.View()
	for _, want := range []string{"LERP-42", "implement", "1/2 busy"} {
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
	if !strings.Contains(view, "0/2 busy") {
		t.Fatalf("view after exit still counts a busy lane:\n%s", view)
	}
}

func TestProvisioningLaneIsOccupied(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventProvisioning, RunID: "r1", Lane: 1,
		TicketID: "id-9", Ticket: "LERP-9", Queue: "plan", StartedAt: time.Now()}})
	view := m.View()
	if !strings.Contains(view, "provisioning") || !strings.Contains(view, "LERP-9") {
		t.Fatalf("provisioning lane not on the board:\n%s", view)
	}
	if strings.Contains(view, "idle") {
		t.Fatalf("provisioning lane still reads idle:\n%s", view)
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
	if len(m.order) != 3 {
		t.Fatalf("board has %d rows, want 3 (two lanes + one adopted)", len(m.order))
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventReaped, RunID: "r9", Lane: 5,
		TicketID: "abcdef1234567890", Queue: "review"}})
	if len(m.order) != 2 {
		t.Fatalf("reaped adopted lane still on the board: %d rows", len(m.order))
	}
}

// Focus moves between panels — every panel stays on screen; the main pane is
// a lens on whichever one has focus.
func TestFocusSwitching(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("3"))
	if m.focus != panelNext {
		t.Fatalf("key 3 focused %v, want up next", m.focus)
	}
	if !strings.Contains(m.View(), "waiting for the first pass") {
		t.Fatalf("empty up-next lens missing its state text:\n%s", m.View())
	}
	m = update(t, m, keyMsg("1"))
	if m.focus != panelAttention {
		t.Fatalf("key 1 focused %v, want needs you", m.focus)
	}
	if !strings.Contains(m.View(), "reading the board") {
		t.Fatalf("needs-you lens before the first pass missing its state text:\n%s", m.View())
	}
	m = update(t, m, keyMsg("tab"))
	if m.focus != panelLanes {
		t.Fatalf("tab from needs-you landed on %v, want running", m.focus)
	}
}

// The up-next panel is the loop's own snapshot, verbatim: eligible tickets
// in pickup order, blocked and claimed ones visibly gated, empty queues
// named, the whole body replaced on every pass. The main pane details the
// selected ticket, including why it will not run.
func TestUpNextShowsWhatRunsNext(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("3"))

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
		"implement", "Todo",
		"LERP-1", "ship the thing",
		"review", "In Review", "empty",
		"team LERP", "position 1 of 3", // the selected ticket's detail lens
		"https://linear.app/acme/issue/LERP-1/ship",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("up-next view is missing %q:\n%s", want, view)
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

// A backlog deeper than the terminal is capped inside its panel, not allowed
// to push the status bar off screen.
func TestUpNextCapsToItsPanel(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("3"))
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
		t.Fatalf("overflowing up-next panel shows no cap line:\n%s", view)
	}
	if got := strings.Count(view, "\n"); got > 30 {
		t.Fatalf("view is %d lines tall in a 30-line window", got)
	}
	if !strings.Contains(view, "q quit") {
		t.Fatalf("cap pushed the status bar off screen:\n%s", view)
	}
}

// The needs-you panel folds attention events; its lens shows the loop's
// reason and Linear's URL for the selected item. The empty state is the goal
// state — but never claimed before the first pass has reported, and it says
// what would make items appear.
func TestNeedsYouListsWhatWaits(t *testing.T) {
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
			t.Fatalf("needs-you view is missing %q:\n%s", want, view)
		}
	}

	// A later pass with nothing waiting clears the list and says so.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	view = m.View()
	if !strings.Contains(view, "nothing needs you") {
		t.Fatalf("empty needs-you list does not read as the goal state:\n%s", view)
	}
	if !strings.Contains(view, "shows unclaimed tickets") {
		t.Fatalf("empty needs-you lens does not explain what would make items appear:\n%s", view)
	}
	if strings.Contains(view, "LERP-42") {
		t.Fatalf("cleared item still rendered:\n%s", view)
	}
}

// needs-you widens attention into two groups: unclaimed work to route, and
// the operator's own claimed tickets parked outside every queue. The panel
// renders each group heading above its tickets, in the loop's order.
func TestNeedsYouGroupsToRouteAndParked(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Group: loop.ToRoute, Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this", Status: "Backlog",
			Reason: `unassigned in "Backlog" — no queue serves it`},
		{Group: loop.Parked, Ticket: "LERP-1", TicketID: "help", Title: "Fix the build", Status: "Needs Help",
			Reason: `claimed in "Needs Help" — no queue serves it`},
	}}})
	// Assert on the panel itself: in the full view the lens also names the
	// selected ticket, which would shadow the ordering check.
	panel := m.attentionPanel(40, 8)
	toRoute := strings.Index(panel, "to route")
	parked := strings.Index(panel, "parked on you")
	lerp4 := strings.Index(panel, "LERP-4")
	lerp1 := strings.Index(panel, "LERP-1")
	if toRoute < 0 || parked < 0 || lerp4 < 0 || lerp1 < 0 {
		t.Fatalf("panel is missing a group heading or ticket:\n%s", panel)
	}
	if !(toRoute < lerp4 && lerp4 < parked && parked < lerp1) {
		t.Fatalf("groups are not rendered to-route-then-parked, each above its ticket:\n%s", panel)
	}
	if !strings.Contains(m.View(), string(loop.ToRoute)) {
		t.Fatalf("group heading missing from the full view:\n%s", m.View())
	}
}

// Done-when: leverage, priority and blocked-ness are readable on the row
// itself, without selecting it — and on a narrow panel the title is what
// gets truncated, not the facts the operator chooses a promote by.
func TestNeedsYouRowsCarryLeverageAndPriority(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Group: loop.ToRoute, Ticket: "LERP-22", Title: "GoReleaser: tagged releases", Priority: 2,
			Unblocks: 3, Blocks: []string{"LERP-23", "LERP-38"}},
		{Group: loop.ToRoute, Ticket: "LERP-36", Title: "Sanitize control characters", Priority: 1},
		{Group: loop.ToRoute, Ticket: "LERP-23", Title: "curl install", Priority: 3,
			Unblocks: 1, BlockedBy: []string{"LERP-22"}},
	}}})

	panel := m.attentionPanel(60, 8)
	for _, want := range []string{"↓3", "↓0", "⊘", "High", "Urgent", "Medium"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("needs-you row is missing %q:\n%s", want, panel)
		}
	}
	// The selection sits on the first row, so the second row's Urgent and
	// the third row's ⊘ are both facts no selection revealed.
	lines := strings.Split(panel, "\n")
	for _, want := range []struct{ ticket, mark string }{
		{"LERP-22", "↓3"}, {"LERP-36", "Urgent"}, {"LERP-23", "⊘"},
	} {
		found := false
		for _, line := range lines {
			if strings.Contains(line, want.ticket) && strings.Contains(line, want.mark) {
				found = true
			}
		}
		if !found {
			t.Fatalf("no row carries both %s and %s:\n%s", want.ticket, want.mark, panel)
		}
	}

	narrow := m.attentionPanel(30, 8)
	if !strings.Contains(narrow, "Urgent") || !strings.Contains(narrow, "↓3") {
		t.Fatalf("a narrow panel dropped the leverage or the priority:\n%s", narrow)
	}
	if strings.Contains(narrow, "GoReleaser: tagged releases") {
		t.Fatalf("a narrow panel did not truncate the title:\n%s", narrow)
	}
}

// Selecting a needs-you item and pressing "p" opens the promote picker in
// the main pane; choosing a status and confirming calls Promote with the
// ticket's Linear id and the chosen status, and settles into a transient
// note on the status bar. Cancelling touches nothing.
func TestPromotePicker(t *testing.T) {
	m, _, _, promoter := newPromoteTestModel(t, 1, []string{"Planning", "Implementing"})
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Group: loop.ToRoute, Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this", Status: "Backlog"},
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

// A pass that reports the promoted ticket gone (it moved out of needs-you)
// while the picker is still open must not leave a dangling selection.
func TestPromotePickerClosesWhenTheListEmpties(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Group: loop.ToRoute, Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this"},
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
	if !strings.Contains(m.View(), "RUNNING") {
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
	if !strings.Contains(m.View(), "2 need you") {
		t.Fatalf("status bar does not count needs-you:\n%s", m.View())
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

// fillBoard puts n items in needs-you and n tickets in one queue, so every
// panel has more rows than a small window can hold.
func fillBoard(t *testing.T, m model, n int) model {
	t.Helper()
	items := make([]loop.AttentionItem, n)
	tickets := make([]loop.QueueTicket, n)
	for i := range items {
		items[i] = loop.AttentionItem{Group: loop.ToRoute, Ticket: fmt.Sprintf("LERP-%d", i+1),
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
// the slack: with 15 items waiting, idle lanes and empty queues, needs-you
// gets the whole column and renders all 15. Moving focus moves the space —
// there is no expand key, only focus.
func TestFocusedPanelTakesTheSlack(t *testing.T) {
	m, _, _ := newTestModel(t, 3)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = resized.(model)
	items := make([]loop.AttentionItem, 15)
	for i := range items {
		items[i] = loop.AttentionItem{Group: loop.ToRoute, Ticket: fmt.Sprintf("LERP-%d", i+1),
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
	if got := g.attnH + g.lanesH + g.nextH; got != g.bodyH {
		t.Fatalf("stack is %d lines in a %d-line body", got, g.bodyH)
	}
	if g.lanesH != collapsedH || g.nextH != collapsedH {
		t.Fatalf("idle lanes and empty queues reserved a body: lanes %d, next %d", g.lanesH, g.nextH)
	}
	view := m.View()
	for i := 1; i <= 15; i++ {
		if !strings.Contains(view, fmt.Sprintf("LERP-%d ", i)) {
			t.Fatalf("needs-you dropped LERP-%d with a column to spare:\n%s", i, view)
		}
	}
	if strings.Contains(view, "more") {
		t.Fatalf("needs-you cut its list with room left over:\n%s", view)
	}

	// Focus moves, and the slack moves with it: needs-you falls back to the
	// rows it renders, up-next takes what is left.
	m = update(t, m, keyMsg("3"))
	g2 := m.geometry()
	rows, _ := m.attentionRows(g2.sideW - 2)
	if g2.attnH != len(rows)+2 {
		t.Fatalf("unfocused needs-you is %d lines for %d rows", g2.attnH, len(rows))
	}
	if g2.nextH <= g.nextH || g2.nextH != g.bodyH-g2.attnH-g2.lanesH {
		t.Fatalf("up-next did not take the slack on focus: %d lines", g2.nextH)
	}
}

// A panel with nothing to show costs one line — its own title row — and
// takes its body back the moment the operator focuses it. Content drives
// this; no toggle, and the selection still works.
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
	for _, want := range []string{
		"[2] running · 0/3 busy — all lanes idle",
		"[3] up next — all queues empty",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("collapsed panel is missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "○ idle") {
		t.Fatalf("collapsed lanes panel still reserved a body:\n%s", view)
	}

	// Focusing a collapsed panel opens it: the lane rows are back, and with
	// them the selection.
	m = update(t, m, keyMsg("2"))
	view = m.View()
	if strings.Contains(view, "all lanes idle") {
		t.Fatalf("focused lanes panel stayed collapsed:\n%s", view)
	}
	if !strings.Contains(view, "○ idle") {
		t.Fatalf("focused lanes panel does not show its lanes:\n%s", view)
	}
	m = update(t, m, keyMsg("down"))
	if m.selected != 2 {
		t.Fatalf("selection in a reopened panel = lane %d, want 2", m.selected)
	}
}

// When the three panels want more than the body between them, the unfocused
// ones are squeezed to the floor before the panel being worked in gives up a
// row — and the stack still fits, so the status bar stays on screen.
func TestOverflowSqueezesTheUnfocusedPanelsFirst(t *testing.T) {
	m, _, _ := newTestModel(t, 6)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = resized.(model)
	m = fillBoard(t, m, 40)
	// A busy lane keeps the running panel from collapsing, so all three
	// panels are asking for a body.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})

	m = update(t, m, keyMsg("1"))
	g := m.geometry()
	if got := g.attnH + g.lanesH + g.nextH; got != g.bodyH {
		t.Fatalf("overflowing stack is %d lines in a %d-line body", got, g.bodyH)
	}
	if g.lanesH != panelFloor || g.nextH != panelFloor {
		t.Fatalf("unfocused panels are not at the floor: lanes %d, next %d", g.lanesH, g.nextH)
	}
	if want := g.bodyH - 2*panelFloor; g.attnH != want {
		t.Fatalf("focused needs-you is %d lines, want %d", g.attnH, want)
	}
	if lines := strings.Count(m.View(), "\n") + 1; lines > 24 {
		t.Fatalf("view is %d lines tall in a 24-line window", lines)
	}

	// The squeeze follows focus, like the slack does.
	m = update(t, m, keyMsg("3"))
	g = m.geometry()
	if g.attnH != panelFloor || g.nextH != g.bodyH-2*panelFloor {
		t.Fatalf("focus did not move the squeeze: needs-you %d, up-next %d", g.attnH, g.nextH)
	}
}

// The too-small guard is geometry's own arithmetic: at the smallest window
// each layout admits, the stack still fits the terminal, and one line less
// is refused rather than drawn over the status bar.
func TestSmallestWindowTheGuardAdmits(t *testing.T) {
	for _, tc := range []struct{ w, h int }{
		{120, 3*panelFloor + 1},
		{70, 3*panelFloor + mainFloor + 1},
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
		for _, focus := range []string{"1", "2", "3"} {
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

func TestSelectingALaneTailsItsLog(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one.log")
	two := filepath.Join(dir, "two.log")
	writeLog(t, one, []byte("agent one says hello\n"))
	writeLog(t, two, []byte("agent two says hello\n"))

	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		Ticket: "LERP-1", Queue: "plan", LogPath: one}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r2", Lane: 2,
		Ticket: "LERP-2", Queue: "plan", LogPath: two}})

	if !strings.Contains(m.View(), "agent one says hello") {
		t.Fatalf("selected lane 1's log is not tailed:\n%s", m.View())
	}

	m = update(t, m, keyMsg("down"))
	if !strings.Contains(m.View(), "agent two says hello") {
		t.Fatalf("selecting lane 2 did not switch the tail:\n%s", m.View())
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

// A stream-json runner's lane reads as what the agent is doing, and `r`
// flips to the bytes it actually wrote — the escape hatch for a formatter
// that got something wrong.
func TestLaneLogRendersActivityAndTogglesToRaw(t *testing.T) {
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
// gathered during a needs-you detour is on screen the moment the running
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
		t.Fatalf("needs-you lens still shows the log:\n%s", m.View())
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

	// A clean pass supersedes the stale error; lane-level outcomes live on as
	// lane notes, not here.
	m = update(t, m, tickMsg{})
	m = update(t, m, tickedMsg{})
	if strings.Contains(m.View(), "still down") {
		t.Fatalf("clean pass left a stale error on the status bar:\n%s", m.View())
	}
}

// Selection is by lane number, not row position: rows appearing or vanishing
// above the selected lane must not silently move the selection — and with it
// the tail — to a different lane's log.
func TestSelectionFollowsLaneAcrossReorders(t *testing.T) {
	dir := t.TempDir()
	seven := filepath.Join(dir, "seven.log")
	writeLog(t, seven, []byte("lane seven speaking\n"))

	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r7", Lane: 7,
		TicketID: "t7", Queue: "review", LogPath: seven}})
	m = update(t, m, keyMsg("down"))
	m = update(t, m, keyMsg("down"))
	if m.selected != 7 {
		t.Fatalf("selected lane = %d, want 7", m.selected)
	}

	// A second adopted run slots in between the fixed lanes and lane 7.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r5", Lane: 5,
		TicketID: "t5", Queue: "review", LogPath: filepath.Join(dir, "five.log")}})
	if m.selected != 7 {
		t.Fatalf("selected lane after a row appeared above = %d, want still 7", m.selected)
	}
	if m.tail.path != seven {
		t.Fatalf("tail retargeted to %q, want lane 7's log", m.tail.path)
	}

	// Only the selected row itself vanishing moves the selection: it falls
	// back to the nearest remaining row.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventReaped, RunID: "r7", Lane: 7,
		TicketID: "t7", Queue: "review"}})
	if m.selected != 5 {
		t.Fatalf("selected lane after its row vanished = %d, want the fallback 5", m.selected)
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

// threeWaiting is a needs-you board with room to walk: three items, three
// ticket IDs.
func threeWaiting() loop.Event {
	return loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Group: loop.ToRoute, Ticket: "LERP-1", TicketID: "id-1", Title: "First",
			Status: "Backlog", Reason: "unclaimed", URL: "https://linear.app/acme/issue/LERP-1"},
		{Group: loop.ToRoute, Ticket: "LERP-2", TicketID: "id-2", Title: "Second",
			Status: "Backlog", Reason: "unclaimed", URL: "https://linear.app/acme/issue/LERP-2"},
		{Group: loop.ToRoute, Ticket: "LERP-3", TicketID: "id-3", Title: "Third",
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
	id := m.attention[sel].TicketID
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

// Done-when: selecting a needs-you item shows its body and its comments in
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
	if !strings.Contains(view, "o opens it in Linear") {
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
	escapeFree(t, "needs-you detail", view)
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
			{Group: loop.ToRoute, Ticket: title, TicketID: title, Title: title,
				Status: title, Reason: title, URL: title},
		}}})
		return update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
			{Team: title, Name: title, Status: title, Tickets: []loop.QueueTicket{
				{ID: title, Identifier: title, Title: title, URL: title, Eligible: true}}},
		}}})
	}

	for _, focus := range []string{"1", "2", "3"} {
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
