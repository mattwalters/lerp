package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattwalters/lerp/internal/loop"
)

// hasMark reports whether the large mark is on screen, every line of it: a
// wordmark missing a row is a rendering bug, not a wordmark.
func hasMark(view string) bool {
	for _, line := range markLines {
		if !strings.Contains(view, line) {
			return false
		}
	}
	return true
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

// The board replaces the splash the moment the first pass reports — whatever
// it reported. And it never gives the screen back: a later slow pass is the
// status bar's to report, over a board that has something on it.
func TestTheBoardTakesTheScreenTheMomentThePassReports(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", Title: "one"},
	}}})
	view := m.View()
	if hasMark(view) {
		t.Fatalf("the splash outlived the pass that reported the inbox:\n%s", view)
	}
	if !strings.Contains(view, "LERP-1") {
		t.Fatalf("the board did not take the screen:\n%s", view)
	}
	// The real cold start: the loop reports the inbox from inside the first
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

// A pass that says nothing at all — no queues configured, an empty board —
// still ends the splash: there is nothing left to wait for, and the panels'
// own empty states are what have something to say about it.
func TestAPassThatReportsNothingStillEndsTheSplash(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, tickedMsg{})
	if hasMark(m.View()) {
		t.Fatalf("the splash spins on past a finished pass:\n%s", m.View())
	}
}

// A spinner that never stops is the blank screen with extra motion. When the
// first pass fails rather than lands, the splash gives way and the error
// goes where errors go — the status bar's transient line.
func TestAFailedFirstPassSaysSoRatherThanSpinning(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventError,
		Err: errors.New("attention: read viewer: no route to host")}})
	view := m.View()
	if hasMark(view) {
		t.Fatalf("a failed first pass is still spinning:\n%s", view)
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
	if !strings.Contains(m.View(), "next panel") {
		t.Fatalf("? over the splash opened nothing:\n%s", m.View())
	}
	m = update(t, m, keyMsg("?"))
	if !hasMark(m.View()) {
		t.Fatalf("closing the overlay did not give the splash back:\n%s", m.View())
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
