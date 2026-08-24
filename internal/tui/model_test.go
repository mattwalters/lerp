package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mattwalters/lerp/internal/loop"
)

// countingTicker stands in for the reconciler: the TUI only needs something
// to tick.
type countingTicker struct {
	ticks atomic.Int64
}

func (c *countingTicker) Tick(context.Context) { c.ticks.Add(1) }

func newTestModel(t *testing.T, lanes int) (model, *countingTicker, chan loop.Event) {
	t.Helper()
	ticker := &countingTicker{}
	events := make(chan loop.Event, 8)
	m := newModel(context.Background(), Options{
		Ticker:   ticker,
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

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestBoardShowsLaneLifecycle(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	view := m.View()
	for _, want := range []string{"lerp", "2 board", "idle", "q quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("initial view is missing %q:\n%s", want, view)
		}
	}

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})
	view = m.View()
	for _, want := range []string{"LERP-42", "implement", "running"} {
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
	if strings.Contains(view, "running") {
		t.Fatalf("view after exit still shows a running lane:\n%s", view)
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
	if view := m.View(); !strings.Contains(view, "adopted 2h") {
		t.Fatalf("adopted row does not show the run's true age:\n%s", view)
	}
}

func TestAdoptedRunOccupiesAndFreesItsRow(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAdopted, RunID: "r9", Lane: 5,
		TicketID: "abcdef1234567890", Queue: "review", LogPath: "/dev/null"}})
	view := m.View()
	if !strings.Contains(view, "adopted") || !strings.Contains(view, "review") {
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

func TestViewSwitching(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("3"))
	if !strings.Contains(m.View(), "waiting for the first pass") {
		t.Fatalf("key 3 did not show the queue view:\n%s", m.View())
	}
	m = update(t, m, keyMsg("1"))
	if !strings.Contains(m.View(), "reading the board") {
		t.Fatalf("key 1 did not show the attention view:\n%s", m.View())
	}
	m = update(t, m, keyMsg("tab"))
	if m.view != viewBoard {
		t.Fatalf("tab from attention landed on %v, want board", m.view)
	}
}

// The queue view is the loop's own snapshot, verbatim: eligible tickets in
// pickup order, blocked and claimed ones visibly gated, empty queues named,
// the whole body replaced on every pass.
func TestQueueViewShowsWhatRunsNext(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("3"))

	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "t1", Identifier: "LERP-1", Title: "ship the thing", Eligible: true},
			{ID: "t2", Identifier: "LERP-2", Title: "gated work", BlockedBy: []string{"LERP-1", "LERP-9"}},
			{ID: "t3", Identifier: "LERP-3", Title: "already picked up", Assigned: true},
		}},
		{Team: "LERP", Name: "review", Status: "In Review"},
	}}})
	view := m.View()
	for _, want := range []string{
		"implement", "Todo", "team LERP",
		"LERP-1", "ship the thing",
		"blocked by LERP-1, LERP-9",
		"LERP-3", "claimed",
		"review", `empty — tickets enter when moved to "In Review"`,
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("queue view is missing %q:\n%s", want, view)
		}
	}

	// The next pass's snapshot replaces the whole view: a ticket that left
	// the queue leaves the screen.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo"},
	}}})
	view = m.View()
	if strings.Contains(view, "LERP-1") {
		t.Fatalf("stale ticket survived a refresh:\n%s", view)
	}
	if !strings.Contains(view, `empty — tickets enter when moved to "Todo"`) {
		t.Fatalf("emptied queue does not read empty:\n%s", view)
	}
}

// Narrow terminals truncate the explanatory empty states instead of letting
// them soft-wrap, which would defeat the height cap's line accounting.
func TestNarrowWidthTruncatesEmptyStates(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, tea.WindowSizeMsg{Width: 40, Height: 30})

	m = update(t, m, keyMsg("3"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "review", Status: "A Status With A Very Long Name"},
	}}})
	view := m.View()
	if strings.Contains(view, `moved to "A Status With A Very Long Name"`) {
		t.Fatalf("empty-queue line was not truncated at narrow width:\n%s", view)
	}
	if !strings.Contains(view, "…") {
		t.Fatalf("truncated empty-queue line shows no ellipsis:\n%s", view)
	}

	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	view = m.View()
	if strings.Contains(view, "no queue serves)") {
		t.Fatalf("attention hint was not truncated at narrow width:\n%s", view)
	}
	if !strings.Contains(view, "…") {
		t.Fatalf("truncated attention hint shows no ellipsis:\n%s", view)
	}
}

// A backlog deeper than the terminal is capped, not allowed to push the
// footer off screen.
func TestQueueViewCapsToTheWindow(t *testing.T) {
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
		t.Fatalf("overflowing queue view shows no cap line:\n%s", view)
	}
	if got := strings.Count(view, "\n"); got > 30 {
		t.Fatalf("queue view is %d lines tall in a 30-line window", got)
	}
	if !strings.Contains(view, "q quit") {
		t.Fatalf("cap pushed the help line off screen:\n%s", view)
	}
}

// The attention view folds attention events: it lists each waiting ticket
// with the loop's reason and its Linear URL, and renders the empty list as
// the goal state — never as a bare blank.
func TestAttentionViewListsWhatWaits(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))

	// Before the first pass reports, the view must not claim the goal state.
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
			t.Fatalf("attention view is missing %q:\n%s", want, view)
		}
	}

	// A later pass with nothing waiting clears the list and says so.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	view = m.View()
	if !strings.Contains(view, "nothing needs you") {
		t.Fatalf("empty attention list does not read as the goal state:\n%s", view)
	}
	if !strings.Contains(view, "shows your claimed tickets sitting in statuses no queue serves") {
		t.Fatalf("empty attention list does not explain what would make items appear:\n%s", view)
	}
	if strings.Contains(view, "LERP-42") {
		t.Fatalf("cleared item still rendered:\n%s", view)
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

func TestErrorsSurfaceOnTheStatusLine(t *testing.T) {
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
		t.Fatalf("erroring pass cleared the status line:\n%s", m.View())
	}

	// A clean pass supersedes the stale error; lane-level outcomes live on as
	// lane notes, not here.
	m = update(t, m, tickMsg{})
	m = update(t, m, tickedMsg{})
	if strings.Contains(m.View(), "still down") {
		t.Fatalf("clean pass left a stale error on the status line:\n%s", m.View())
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
