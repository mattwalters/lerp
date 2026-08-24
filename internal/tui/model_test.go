package tui

import (
	"context"
	"errors"
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
	if !strings.Contains(m.View(), "queue view is not built yet") {
		t.Fatalf("key 3 did not show the queue placeholder:\n%s", m.View())
	}
	m = update(t, m, keyMsg("1"))
	if !strings.Contains(m.View(), "attention view is not built yet") {
		t.Fatalf("key 1 did not show the attention placeholder:\n%s", m.View())
	}
	m = update(t, m, keyMsg("tab"))
	if m.view != viewBoard {
		t.Fatalf("tab from attention landed on %v, want board", m.view)
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
