package tui

import (
	"errors"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattwalters/lerp/internal/loop"
	"github.com/muesli/termenv"
)

// hasMark reports whether the large mark is on screen whole: every row of
// it, in order, on consecutive lines, all at one indent. A wordmark missing
// a row is a rendering bug and so is one sheared into columns, and neither
// is caught by looking for the rows one at a time anywhere on screen — the
// first row is " _", which is a substring of the second.
func hasMark(view string) bool {
	block := strings.Split(markBlock, "\n")
	lines := strings.Split(view, "\n")
	for i := 0; i+len(block) <= len(lines); i++ {
		// Where the figure starts, read off its first row: the centring pads
		// every row of the block by the same amount, which is the whole of
		// what keeps the letters lined up.
		at := strings.Index(lines[i], strings.TrimLeft(block[0], " "))
		if at < 0 {
			continue
		}
		indent := at - (len(block[0]) - len(strings.TrimLeft(block[0], " ")))
		if indent < 0 {
			continue
		}
		whole := true
		for j, row := range block {
			want := strings.TrimRight(strings.Repeat(" ", indent)+row, " ")
			if strings.TrimRight(lines[i+j], " ") != want {
				whole = false
				break
			}
		}
		if whole {
			return true
		}
	}
	return false
}

// The first thing anyone sees of lerp is the whole of the first Linear round
// trip, and an empty board says nothing about whether that is working or
// broken.
func TestLerpOpensOnTheMarkAndASpinner(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	view := m.View()
	if !hasMark(view) {
		t.Fatalf("the opening screen is not the mark:\n%s", view)
	}
	if !strings.Contains(view, heartbeatFrames[0]) {
		t.Errorf("the opening screen has no spinner:\n%s", view)
	}
	// Nothing else: two empty panels behind a spinner are the blank board
	// this replaces.
	for _, gone := range []string{"[1] inbox", "[2] work", "q quit"} {
		if strings.Contains(view, gone) {
			t.Errorf("the opening screen still draws %q:\n%s", gone, view)
		}
	}
}

// The spinner is the whole of the difference between "working" and "wedged",
// so it has to actually turn — on the poll, which is the clock the status
// bar's heartbeat already runs on.
func TestTheSplashSpins(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	first := m.View()
	m = update(t, m, pollMsg{})
	if second := m.View(); second == first {
		t.Fatalf("the splash is a still frame:\n%s", second)
	}
	if !hasMark(m.View()) {
		t.Errorf("the mark moved with the spinner:\n%s", m.View())
	}
}

// The two reads that populate the first pass fail independently, so either
// one landing alone must not cut to the board — that would draw it with the
// other half still zero, which is the flicker the splash exists to hide.
func TestASingleReadDoesNotEndTheSplash(t *testing.T) {
	for _, ev := range []loop.Event{
		{Type: loop.EventAttention, Attention: []loop.AttentionItem{{Ticket: "LERP-1", Title: "one"}}},
		{Type: loop.EventQueues},
	} {
		m, _, _ := newTestModel(t, 2)
		m = update(t, m, eventMsg{ev: ev})
		if !hasMark(m.View()) {
			t.Fatalf("%s alone ended the splash:\n%s", ev.Type, m.View())
		}
	}
}

// The board replaces the splash the moment both of the first pass's reads
// have reported, whatever order they arrive in — the loop runs them
// sequentially, but nothing here may depend on which finishes first. And it
// never gives the screen back: a later slow pass is the status bar's to
// report, over a board that has something on it.
func TestTheBoardTakesTheScreenWhenBothReadsReport(t *testing.T) {
	attention := loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "one"},
	}}
	queues := loop.Event{Type: loop.EventQueues}

	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: attention})
	m = update(t, m, eventMsg{ev: queues})
	view := m.View()
	if hasMark(view) {
		t.Fatalf("the splash outlived both reads landing:\n%s", view)
	}
	if !strings.Contains(view, "LERP-1") {
		t.Fatalf("the board did not take the screen:\n%s", view)
	}
	// The real cold start: the loop reports both reads from inside the first
	// pass, so the first board frame anyone ever sees still has that pass in
	// flight — and the status bar, which the splash was covering until this
	// frame, has to pick the heartbeat up where the spinner left off.
	if !m.inFlight {
		t.Fatal("the first pass ended before it reported")
	}
	if !strings.Contains(view, "pass running") {
		t.Fatalf("the bar the splash handed over to says nothing about the pass:\n%s", view)
	}
	// A later pass: in flight, then landed, then in flight again.
	for _, msg := range []tea.Msg{tickedMsg{}, tickMsg{}, tickedMsg{}, tickMsg{}} {
		m = update(t, m, msg)
		if hasMark(m.View()) {
			t.Fatalf("a later pass put the splash back:\n%s", m.View())
		}
	}
}

// The same as above with the reads in the other order: the fix is at the
// "have both landed" seam, not at which one happened to arrive first.
func TestTheBoardTakesTheScreenWhenBothReadsReportInTheOtherOrder(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues}})
	if !hasMark(m.View()) {
		t.Fatalf("queues alone ended the splash:\n%s", m.View())
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "one"},
	}}})
	view := m.View()
	if hasMark(view) {
		t.Fatalf("the splash outlived both reads landing:\n%s", view)
	}
	if !strings.Contains(view, "LERP-1") {
		t.Fatalf("the board did not take the screen:\n%s", view)
	}
}

// A pass that says nothing at all — no queues configured, an empty board —
// still ends the splash once both reads have reported: there is nothing
// left to wait for, and the panels' own empty states are what have
// something to say about it.
func TestAPassThatReportsNothingStillEndsTheSplash(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	if hasMark(m.View()) {
		t.Fatalf("the splash spins on past a finished pass:\n%s", m.View())
	}
}

// A spinner that never stops is the blank screen with extra motion. When the
// first pass fails rather than lands, the splash gives way once the pass
// itself is over (EventTicked) and the error goes where errors go — the
// status bar's transient line. It cannot give way on the error alone: see
// TestAQueuesErrorDoesNotPreemptAttentionStillPending for why.
func TestAFailedFirstPassSaysSoRatherThanSpinning(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError,
		Err: errors.New("attention: read viewer: no route to host")}})
	if !hasMark(m.View()) {
		t.Fatalf("the error alone took the screen from the splash:\n%s", m.View())
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventTicked}})
	view := m.View()
	if hasMark(view) {
		t.Fatalf("a failed first pass is still spinning once it has ended:\n%s", view)
	}
	if !strings.Contains(view, "no route to host") {
		t.Fatalf("the failure never reached the status bar:\n%s", view)
	}
}

// A lane's run failing is not one of the first pass's two reads, so it must
// not settle the splash on its own — the pass itself is still in flight, and
// nothing here says either read is done.
func TestALaneFailureDoesNotEndTheSplash(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError, Lane: 1, Ticket: "LERP-2",
		Err: errors.New("exit status 1")}})
	if !hasMark(m.View()) {
		t.Fatalf("a lane failure ended the splash:\n%s", m.View())
	}
}

// tickedMsg and EventTicked come from two different goroutines racing a
// buffered channel (runTick's return versus waitEvent's read of the same
// pass's own EventTicked), and Tick returning is the cheaper of the two —
// it can win that race and reach Update first. If tickedMsg alone ended the
// splash, that win would cut to the board before this pass's real
// EventQueues or EventAttention — already sitting in the channel behind
// EventTicked — had been applied: the half-populated frame this ticket
// exists to remove, just moved to a rarer window. Only EventTicked, ordered
// after them by the channel itself, may end it.
func TestTickedMsgAloneDoesNotEndTheSplash(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, tickedMsg{})
	if !hasMark(m.View()) {
		t.Fatalf("tickedMsg alone ended the splash:\n%s", m.View())
	}
}

// A row can be on screen from attention alone, before queues has reported —
// the gap between the two reads landing, not just the time before either
// has. Promote, Search and Eject each open a modal that would make View
// fall through to the board early (see View's splashing && !modal guard),
// so each has to check splashing itself rather than trust that a row means
// the pass is over.
func TestModalKeysStayShutDuringTheSplashGap(t *testing.T) {
	attention := loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", TicketID: "id-1", Title: "one"},
	}}

	m, _, _, _ := newPromoteTestModel(t, 1, []string{"Planning"})
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: attention})
	if !hasMark(m.View()) {
		t.Fatalf("attention alone ended the splash:\n%s", m.View())
	}
	m = update(t, m, keyMsg("p"))
	if m.promoting || !hasMark(m.View()) {
		t.Fatalf("p opened the promote picker before queues reported: promoting=%v\n%s",
			m.promoting, m.View())
	}
	m = update(t, m, keyMsg("/"))
	if m.searching || !hasMark(m.View()) {
		t.Fatalf("/ opened the search box before queues reported: searching=%v\n%s",
			m.searching, m.View())
	}

	ejector := &recordingEjector{}
	e, _ := newEjectTestModel(t, 1, ejector)
	e = update(t, e, keyMsg("2"))
	e = update(t, e, eventMsg{ev: loop.Event{Type: loop.EventStarted, RunID: "r1", Lane: 1,
		TicketID: "id-1", Ticket: "LERP-1", Queue: "implement", LogPath: "/dev/null"}})
	if !hasMark(e.View()) {
		t.Fatalf("a running lane alone ended the splash:\n%s", e.View())
	}
	e = update(t, e, keyMsg("e"))
	if e.ejecting || !hasMark(e.View()) {
		t.Fatalf("e opened the eject confirm before attention reported: ejecting=%v\n%s",
			e.ejecting, e.View())
	}
}

// Unlike Promote, Search and Eject, force-start opens no modal — nothing
// about pressing S flips View's splashing && !modal gate — so the splash
// stays on screen and the operator cannot see the row they are about to
// force-start. Without its own check that row is still real: queues alone
// is enough for selectedWork to return one, before attention has reported.
// S would claim a ticket and start an agent on a row nobody has seen drawn.
func TestForceStartStaysShutDuringTheSplashGap(t *testing.T) {
	m, _, starter := newStartingTestModel(t, 1)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "t1", Identifier: "LERP-1", Title: "one", Eligible: true},
		}},
	}}})
	if !hasMark(m.View()) {
		t.Fatalf("queues alone ended the splash:\n%s", m.View())
	}
	m = update(t, m, keyMsg("2"))
	m, cmd := updateCmd(t, m, keyMsg("S"))
	if cmd != nil {
		t.Fatal("S produced a force-start command before attention reported")
	}
	if got := starter.started(); len(got) != 0 {
		t.Fatalf("force-started tickets = %v, want none before attention reported", got)
	}
	if !hasMark(m.View()) {
		t.Fatalf("S ended the splash on a row queues alone drew:\n%s", m.View())
	}
}

// fill emits its own EventQueues right behind a partial-listing error,
// regardless of whether that error means anything about the pass as a whole
// (reconciler.go's fill). An error from one read must not preempt the other
// read that has not answered yet — success or failure — or the board draws
// with that other half still zero, which is the flicker this whole ticket
// removes. Only the pass ending, once both reads have had their turn, may
// fall back to the error.
func TestAQueuesErrorDoesNotPreemptAttentionStillPending(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError,
		Err: errors.New("list queues: no route to host")}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues}})
	if !hasMark(m.View()) {
		t.Fatalf("the queues error ended the splash before attention reported:\n%s", m.View())
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "one"},
	}}})
	view := m.View()
	if hasMark(view) {
		t.Fatalf("attention landing for real did not end the splash:\n%s", view)
	}
	if !strings.Contains(view, "LERP-1") {
		t.Fatalf("the board did not take the screen:\n%s", view)
	}
}

// The mirror of the above: if attention also fails this pass, nothing more
// is coming for either read, and only then — once EventTicked, the pass's
// own last word, actually arrives — does the splash give way to the error.
func TestAPassEndingWithBothReadsUnresolvedFallsBackToTheError(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError,
		Err: errors.New("list queues: no route to host")}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues}})
	if !hasMark(m.View()) {
		t.Fatal("the first queues error ended the splash early")
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError,
		Err: errors.New("attention: read viewer: no route to host")}})
	if !hasMark(m.View()) {
		t.Fatal("attention's error ended the splash before the pass was over")
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventTicked}})
	view := m.View()
	if hasMark(view) {
		t.Fatalf("the splash spins on past a finished, failed pass:\n%s", view)
	}
	if !strings.Contains(view, "no route to host") {
		t.Fatalf("the failure never reached the status bar:\n%s", view)
	}
}

// The ? overlay is the one thing that takes the screen from the splash: it
// is the operator asking a question the splash cannot answer, and a key that
// visibly does nothing is worse than the empty board they read it over.
func TestTheOverlayTakesTheScreenFromTheSplash(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, keyMsg("?"))
	if !strings.Contains(m.View(), "cycle back") {
		t.Fatalf("? over the splash opened nothing:\n%s", m.View())
	}
	m = update(t, m, keyMsg("?"))
	if !hasMark(m.View()) {
		t.Fatalf("closing the overlay did not give the splash back:\n%s", m.View())
	}
}

// enter is the key that would otherwise open something under the splash.
// The pane it opens draws nothing there — but it is in the geometry, and a
// window narrowed after that invisible keystroke comes back as "window too
// small · esc closes the pane" about a pane that has never been on screen.
func TestEnterDoesNotOpenAPaneUnderTheSplash(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m, cmd := updateCmd(t, m, keyMsg("enter"))
	if m.detailOpen[panelAttention] {
		t.Error("enter opened a pane the splash is covering")
	}
	if m.mainOpen() {
		t.Error("the splash is up with the main pane in the geometry")
	}
	if cmd != nil {
		t.Error("enter under the splash scheduled a read")
	}
	if !hasMark(m.View()) {
		t.Fatalf("enter took the screen from the splash:\n%s", m.View())
	}
	// And it is the splash holding it back, not the key: the same press on
	// the board it hands over to opens the pane.
	m = update(t, pastTheSplash(t, m), keyMsg("enter"))
	if !m.detailOpen[panelAttention] {
		t.Error("enter on the board opened nothing")
	}
}

// The mark is the one figure here with a fixed size, so it is the one that
// can overrun its window. The smallest window View will draw a board in is
// the size it has to survive — below that the too-small screen has the frame
// and there is no splash — and a figure that grew a column past it would go
// out in pieces on a terminal nothing here would refuse.
func TestTheMarkFitsTheSmallestBoard(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, tea.WindowSizeMsg{Width: minWidth, Height: m.minHeight(false)})
	view := m.View()
	if !hasMark(view) {
		t.Fatalf("the smallest board window does not draw the whole mark:\n%s", view)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > minWidth {
			t.Fatalf("line %d is %d columns wide in a %d-column window:\n%s",
				i, got, minWidth, view)
		}
	}
	if got := lipgloss.Height(view); got > m.height {
		t.Errorf("the splash is %d lines tall in a %d-line window", got, m.height)
	}
}

// hasDimMark is hasMark for the empty-board decoration. Two things hasMark
// itself cannot see past: styleWordmark wraps every line of the block in its
// own colour escapes, which a byte-offset search would count as indent, so
// this strips them first. And hasMark checks a figure drawn over a bare
// canvas — the splash's whole screen — by requiring each row to be the rest
// of its line once trailing spaces are gone; the decoration sits inside a
// bordered panel instead, so every row has a border character after it
// rather than nothing, and that whole-line equality would never match. This
// checks the same thing hasMark does — every row, in order, at one shared
// indent — as a substring at that position instead, so whatever the panel
// draws to its right of the figure does not matter.
func hasDimMark(view string) bool {
	view = ansi.Strip(view)
	block := strings.Split(markBlock, "\n")
	lines := strings.Split(view, "\n")
	first := strings.TrimLeft(block[0], " ")
	for i := 0; i+len(block) <= len(lines); i++ {
		at := strings.Index(lines[i], first)
		if at < 0 {
			continue
		}
		indent := at - (len(block[0]) - len(first))
		if indent < 0 {
			continue
		}
		whole := true
		for j, row := range block {
			line := lines[i+j]
			if indent+len(row) > len(line) || line[indent:indent+len(row)] != row {
				whole = false
				break
			}
		}
		if whole {
			return true
		}
	}
	return false
}

// emptyBoard is a model past the splash with both panels genuinely empty —
// nothing waiting on the operator and no ticket, running or queued, in any
// lane — and settled: the pass that reported that has also concluded, which
// is what actually licenses the mark (see model.boardEmptySettled). The
// window is the test default, 100x30, wide enough that the inbox panel
// alone clears the mark's fit test many times over (see
// TestWordmarkFitsRequiresRoomOnEverySide).
func emptyBoard(t *testing.T) model {
	t.Helper()
	// wordmarkVisible (mark.go) is what keeps the mark off a NO_COLOR
	// terminal — and a test binary writing to a pipe hits the same profile
	// detection, so without this every "the mark shows" assertion here would
	// fail for a reason that has nothing to do with the thing under test.
	forceColour(t)
	m, _, _ := newTestModel(t, 2)
	m = pastTheSplash(t, m)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: nil}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: nil}})
	m = update(t, m, tickedMsg{})
	if !m.boardEmptySettled {
		t.Fatal("setup: the empty pass never settled")
	}
	return m
}

// Reconciler.Tick only reaches fill (and so only ever emits EventQueues) if
// the evidence reconcile it gates on succeeds — so a pass that keeps failing
// there leaves m.queues exactly as empty, forever, as a board that has
// genuinely never had a ticket. Without queuesSeen, contentEmpty could not
// tell that apart from the goal state, and the mark would decorate a work
// panel that was never actually read (see model.contentEmpty).
func TestTheWordmarkWaitsToSeeTheWorkPanelToo(t *testing.T) {
	forceColour(t)
	m, _, _ := newTestModel(t, 2)
	m = pastTheSplash(t, m)
	// The inbox settles empty, but no EventQueues has ever landed — a pass
	// stuck failing before fill, not a board that was ever confirmed idle.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: nil}})
	m = update(t, m, tickedMsg{})
	if m.boardEmptySettled {
		t.Fatal("boardEmptySettled promoted without the work panel ever being read")
	}
	if hasDimMark(m.View()) {
		t.Fatalf("the mark decorated a work panel that was never reported:\n%s", m.View())
	}
	// The work panel finally reports (empty) in a later pass: now the board
	// has actually been read whole, and the mark is licensed.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: nil}})
	m = update(t, m, tickedMsg{})
	if !hasDimMark(m.View()) {
		t.Fatalf("the mark did not show once both panels had actually reported:\n%s", m.View())
	}
}

// The board's own goal state — nothing on the operator, nothing running or
// queued — is precisely what an idle board looks like whether lerp is
// working or has nothing left to do, and the wordmark is what tells the two
// apart from a glance, the way the splash tells a cold start from a wedge.
func TestTheWordmarkDecoratesTheEmptyBoard(t *testing.T) {
	m := emptyBoard(t)
	view := m.View()
	if !hasDimMark(view) {
		t.Fatalf("an empty board does not show the wordmark:\n%s", view)
	}
	// Rule 1 is decoration only, and the Done-when is explicit that nothing
	// about it changes what an informational element renders: the plain
	// empty-state line stays on screen beside the mark, so a NO_COLOR
	// terminal or one where colorWordmark quantizes toward invisible still
	// gets the fact in words, not just a blank box.
	if !strings.Contains(view, "the inbox is empty") {
		t.Errorf("the plain empty-state text did not survive alongside the mark:\n%s", view)
	}
}

// The mark's own fit test is asked about the room left under that line, not
// the panel's whole interior — a regression that fit the mark against the
// full height would draw it overlapping the line above it.
func TestTheWordmarkLeavesRoomForTheEmptyStateLine(t *testing.T) {
	m := emptyBoard(t)
	view := ansi.Strip(m.View())
	lines := strings.Split(view, "\n")
	textAt, markAt := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "the inbox is empty") {
			textAt = i
		}
		if strings.Contains(line, strings.TrimLeft(strings.Split(markBlock, "\n")[0], " ")) {
			markAt = i
		}
	}
	if textAt < 0 || markAt < 0 {
		t.Fatalf("expected both the empty-state line and the mark on screen:\n%s", view)
	}
	if markAt <= textAt {
		t.Errorf("the mark drew at or above the empty-state line (text at %d, mark at %d):\n%s", textAt, markAt, view)
	}
}

// A pass reports the inbox and the work panel as two separate events, one
// Linear round trip apart, and rule 3 is explicit that the mark's fit
// condition is evaluated on settled layout changes, never mid-report: a
// frame caught between the two must not show the mark on the strength of
// only one of them having landed empty, or a run concluding — work empties
// before the inbox reports the finished ticket — would flash it on for the
// gap.
func TestTheWordmarkWaitsForBothEventsToSettle(t *testing.T) {
	forceColour(t)
	m, _, _ := newTestModel(t, 2)
	m = pastTheSplash(t, m)
	// One ticket running: neither panel is empty yet.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "t1", Identifier: "LERP-1", Title: "ship the thing", Eligible: true},
		}},
	}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: nil}})
	m = update(t, m, tickedMsg{})
	if hasDimMark(m.View()) {
		t.Fatal("setup: the mark is already showing with a ticket in flight")
	}
	// The run concludes: the work panel empties first, mid-pass — the inbox
	// has not yet reported the ticket the run just finished.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo"},
	}}})
	if hasDimMark(m.View()) {
		t.Fatalf("the mark showed mid-pass, before the inbox reported:\n%s", m.View())
	}
	// The inbox reports the finished ticket a beat later, in the same pass —
	// the board is not empty at all, so tickedMsg must not promote it either.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention,
		Attention: []loop.AttentionItem{{Ticket: "LERP-1", Title: "ship the thing"}}}})
	m = update(t, m, tickedMsg{})
	if hasDimMark(m.View()) {
		t.Fatalf("the mark showed once the pass settled non-empty:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "LERP-1") {
		t.Fatalf("the finished ticket never reached the inbox:\n%s", m.View())
	}
}

// loop.listQueues drops a queue it cannot read rather than skipping the
// event, so a Linear outage on the work panel's read publishes an empty
// snapshot indistinguishable from an actually idle one. Promoting on that
// pass would light the mark up over an error and hide it again the moment a
// later, successful pass reads the same tickets back — the "reappears
// mid-churn" flap rule 3 rules out. passHadErr is what tells the two apart.
func TestTheWordmarkDoesNotPromoteOverAFailedPass(t *testing.T) {
	forceColour(t)
	m, _, _ := newTestModel(t, 2)
	m = pastTheSplash(t, m)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: nil}})
	// The queue listing fails: contentEmpty reads true (both sides have
	// "reported", and the failed read came back empty) but the pass carried
	// an error.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: nil}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError, Err: errors.New("list queues: no route to host")}})
	m = update(t, m, tickedMsg{})
	if m.boardEmptySettled {
		t.Fatal("boardEmptySettled promoted over a pass that errored")
	}
	if hasDimMark(m.View()) {
		t.Fatalf("the mark showed over a failed pass:\n%s", m.View())
	}
	// A later pass reads the same (still empty) board cleanly: tickMsg
	// resets passHadErr the way a real pass boundary would, and only now is
	// the board actually confirmed idle.
	m = update(t, m, tickMsg{})
	m = update(t, m, tickedMsg{})
	if !hasDimMark(m.View()) {
		t.Fatalf("the mark did not show once a pass actually succeeded:\n%s", m.View())
	}
}

// Opening a pane is the operator asking for something specific on screen,
// and the mark is not it: rule 3 hides the decoration the moment there is no
// more centre space for it to sit in, the same discrete change that would
// also cover a resize.
func TestTheWordmarkHidesBehindAnOpenPane(t *testing.T) {
	m := emptyBoard(t)
	if !hasDimMark(m.View()) {
		t.Fatal("setup: the empty board is not showing the mark")
	}
	m = openMain(t, m)
	if hasDimMark(m.View()) {
		t.Fatalf("the mark is still on screen with the main pane open:\n%s", m.View())
	}
}

// A board that fills is a board with something to say for itself, and the
// mark carries no information (rule 1) — so the moment the inbox has a row,
// it is that row's screen again, not the mark's.
func TestTheWordmarkHidesWhenTheInboxHasSomething(t *testing.T) {
	m := emptyBoard(t)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention,
		Attention: []loop.AttentionItem{{Ticket: "LERP-1", Title: "one"}}}})
	view := m.View()
	if hasDimMark(view) {
		t.Fatalf("the mark outlived an inbox row landing:\n%s", view)
	}
	if !strings.Contains(view, "LERP-1") {
		t.Fatalf("the inbox row never made it to the panel:\n%s", view)
	}
}

// The same rule from the work side: a queued ticket is exactly as much "the
// board has something on it" as a row in the inbox, even with the inbox
// itself still clear.
func TestTheWordmarkHidesWhenWorkHasATicket(t *testing.T) {
	m := emptyBoard(t)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: []loop.QueueSnapshot{
		{Team: "LERP", Name: "implement", Status: "Todo", Tickets: []loop.QueueTicket{
			{ID: "t1", Identifier: "LERP-1", Title: "ship the thing", Eligible: true},
		}},
	}}})
	if hasDimMark(m.View()) {
		t.Fatalf("the mark outlived a ticket landing in the work panel:\n%s", m.View())
	}
}

// A pass can empty the inbox out from under a search box the operator still
// has open — the EventAttention handler deliberately leaves the query alone
// while m.searching, rather than snatch it out from under whatever they are
// mid-typing. mainOpen says nothing about that box (it lives in the inbox
// panel's own footer, not the main pane), so the wordmark must know to stay
// off screen on its own rather than draw over it. The pass is explicitly
// settled with tickedMsg here — otherwise the assertion would pass for the
// wrong reason, boardEmptySettled never having caught up rather than the
// search guard actually holding.
func TestTheWordmarkStaysOffScreenBehindAnOpenSearch(t *testing.T) {
	forceColour(t)
	m, _, _ := newTestModel(t, 2)
	m = pastTheSplash(t, m)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention,
		Attention: []loop.AttentionItem{{Ticket: "LERP-1", Title: "one"}}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: nil}})
	m = update(t, m, keyMsg("/"))
	if !m.searching {
		t.Fatal("setup: / did not open the search prompt")
	}
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: nil}})
	m = update(t, m, tickedMsg{})
	if !m.boardEmptySettled {
		t.Fatal("setup: the emptied pass never settled")
	}
	view := m.View()
	if hasDimMark(view) {
		t.Fatalf("the mark drew over an open search box:\n%s", view)
	}
	if !m.searching {
		t.Fatal("the emptied pass closed the search box out from under the operator")
	}
}

// The EventAttention handler skips clearing the query while m.searching is
// true, precisely so a pass landing mid-word never snatches it away — but
// that leaves a gap right after: the operator closes the box themselves
// with enter, which accepts the query and keeps it (closeSearch(true)), and
// no further pass has arrived since to run the clearing logic at all. The
// query outlives the box, unresolved, until the next pass — and boardEmpty
// must keep deferring to the panel's own key hint (which still offers to
// clear it) for exactly that long.
func TestTheWordmarkStaysOffScreenBehindALeftoverQuery(t *testing.T) {
	forceColour(t)
	m, _, _ := newTestModel(t, 2)
	m = pastTheSplash(t, m)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention,
		Attention: []loop.AttentionItem{{Ticket: "LERP-1", Title: "one"}}}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: nil}})
	m = update(t, m, keyMsg("/"))
	m = typeSearch(t, m, "one")
	// The pass empties the inbox while the box is still open: the handler's
	// own !m.searching guard skips the clear.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: nil}})
	m = update(t, m, tickedMsg{})
	if m.search == "" {
		t.Fatal("setup: the query was cleared while the box was still open")
	}
	// Only now does the operator close it — accepting, which keeps the query
	// — with no pass having landed since to clear it.
	m = update(t, m, keyMsg("enter"))
	if m.searching || m.search == "" {
		t.Fatal("setup: enter did not accept the query and close the box")
	}
	if hasDimMark(m.View()) {
		t.Fatalf("the mark drew over a leftover search filter:\n%s", m.View())
	}
}

// A profile NO_COLOR (or an unsupportive terminal) downgrades to no colour
// turns colorWordmark to plain text the same as every other style here
// (TestNoColorLeavesTheTextBare) — fine for text that carries information,
// wrong for a figure whose only claim to being decoration is dimness.
// wordmarkVisible is what keeps the mark off screen in exactly that case
// rather than drawing it at full brightness. The ANSI (16-colour) case is
// the sharper version of the same bug: colorWordmark's ANSI slots are left
// empty specifically so this profile renders no colour at all rather than
// termenv's nearest 16-colour match, which for #4A4750 is bright-black —
// most terminals draw that around #7E7E7E, well above the contrast floor
// and exactly the "full-brightness wall of ASCII art" this function exists
// to keep off screen.
func TestWordmarkVisibleReadsTheColorProfile(t *testing.T) {
	render := func(env fakeEnviron, opts ...termenv.OutputOption) bool {
		opts = append(opts, termenv.WithEnvironment(env), termenv.WithTTY(true))
		r := lipgloss.NewRenderer(io.Discard, opts...)
		r.SetHasDarkBackground(true)
		return wordmarkVisible(r)
	}
	if !render(fakeEnviron{"TERM": "xterm-256color"}) {
		t.Fatal("wordmarkVisible = false with colour available, want true")
	}
	if render(fakeEnviron{"TERM": "xterm-256color", "NO_COLOR": "1"}) {
		t.Fatal("wordmarkVisible = true under NO_COLOR, want false")
	}
	if render(fakeEnviron{"TERM": "xterm"}, termenv.WithProfile(termenv.ANSI)) {
		t.Fatal("wordmarkVisible = true on a 16-colour profile, want false (the ANSI slot is deliberately empty)")
	}
}

// wordmarkFits is the guard against a clipped figure (rule 3): the fixed-size
// block needs its full height and width, margin included, on both sides.
// Shaving one cell off either dimension has to fail it — a fit test that
// rounds in the mark's favour is the one that lets a corner of it touch the
// border it is meant to keep clear of.
func TestWordmarkFitsRequiresRoomOnEverySide(t *testing.T) {
	need := len(markLines) + 2*wordmarkMargin
	wide := lipgloss.Width(markBlock) + 2*wordmarkMargin
	if !wordmarkFits(wide, need) {
		t.Fatalf("wordmarkFits(%d, %d) = false, want true at exactly the room it asks for", wide, need)
	}
	if wordmarkFits(wide-1, need) {
		t.Error("wordmarkFits allowed a box one column too narrow")
	}
	if wordmarkFits(wide, need-1) {
		t.Error("wordmarkFits allowed a box one row too short")
	}
}

// The fallback for a board too tight to hold the mark: the fit test says no,
// and the panel keeps its ordinary one-line empty state rather than drawing
// the figure clipped into whatever room is actually there. The pass is
// settled with tickedMsg so the assertion is actually exercising the fit
// test rather than passing on unsettled state alone.
func TestTheWordmarkNeverClipsOnATightBoard(t *testing.T) {
	forceColour(t)
	m, _, _ := newTestModel(t, 2)
	// Wide enough to stay clear of the too-small screen, but short enough
	// that the inbox panel's own floor is nowhere near the mark's height.
	m = update(t, m, tea.WindowSizeMsg{Width: minWidth, Height: m.minHeight(false)})
	m = pastTheSplash(t, m)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventQueues, Queues: nil}})
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: nil}})
	m = update(t, m, tickedMsg{})
	if !m.boardEmptySettled {
		t.Fatal("setup: the empty pass never settled")
	}
	view := m.View()
	if hasDimMark(view) {
		t.Fatalf("the mark rendered on a board too tight to hold it whole:\n%s", view)
	}
	if !strings.Contains(view, "the inbox is empty") {
		t.Fatalf("the tight board lost its plain empty-state text along with the mark:\n%s", view)
	}
}
