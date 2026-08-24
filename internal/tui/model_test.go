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
	"github.com/charmbracelet/lipgloss"

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

func TestLanesShowTheRunLifecycle(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	view := m.View()
	for _, want := range []string{"lanes", "attention", "up next", "idle", "q quit"} {
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
		t.Fatalf("key 1 focused %v, want attention", m.focus)
	}
	if !strings.Contains(m.View(), "reading the board") {
		t.Fatalf("attention lens before the first pass missing its state text:\n%s", m.View())
	}
	m = update(t, m, keyMsg("tab"))
	if m.focus != panelLanes {
		t.Fatalf("tab from attention landed on %v, want lanes", m.focus)
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
	// the queue leaves the screen.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo"},
	}}})
	view = m.View()
	if strings.Contains(view, "LERP-1") {
		t.Fatalf("stale ticket survived a refresh:\n%s", view)
	}
	if !strings.Contains(view, "empty") {
		t.Fatalf("emptied queue does not read empty:\n%s", view)
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

// The attention panel folds attention events; its lens shows the loop's
// reason and Linear's URL for the selected item. The empty state is the goal
// state — but never claimed before the first pass has reported.
func TestAttentionListsWhatWaits(t *testing.T) {
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
			t.Fatalf("attention view is missing %q:\n%s", want, view)
		}
	}

	// A later pass with nothing waiting clears the list and says so.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	view = m.View()
	if !strings.Contains(view, "nothing needs you") {
		t.Fatalf("empty attention list does not read as the goal state:\n%s", view)
	}
	if strings.Contains(view, "LERP-42") {
		t.Fatalf("cleared item still rendered:\n%s", view)
	}
}

// The status bar carries the heartbeat and the counts; the ? key swaps the
// main pane for the full keymap.
func TestStatusBarAndHelp(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	if !strings.Contains(m.View(), "LANES") {
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
		t.Fatalf("status bar does not count attention:\n%s", m.View())
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

// No rendered line may overflow the terminal, wide layout or stacked.
func TestViewFitsTheWindow(t *testing.T) {
	for _, width := range []int{120, 100, 80, 60} {
		m, _, _ := newTestModel(t, 3)
		resized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m = resized.(model)
		m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
			TicketID: "id-42", Ticket: "LERP-42", Queue: "implement", LogPath: "/dev/null"}})
		for i, line := range strings.Split(m.View(), "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width %d: line %d is %d cells wide:\n%s", width, i, got, line)
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

// The log keeps tailing while the operator looks elsewhere: appended output
// gathered during an attention detour is on screen the moment lanes regain
// focus.
func TestLogSurvivesAFocusDetour(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one.log")
	writeLog(t, one, []byte("first line\n"))

	m, _, _ := newTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		Ticket: "LERP-1", Queue: "plan", LogPath: one}})
	m = update(t, m, keyMsg("1"))
	if strings.Contains(m.View(), "first line") {
		t.Fatalf("attention lens still shows the log:\n%s", m.View())
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
