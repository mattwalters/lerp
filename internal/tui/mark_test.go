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
// can overrun its window. Below the room it needs it falls back to the small
// mark — the same word, which is what the status bar's corner carries.
func TestAWindowTooNarrowForTheBlockGetsTheSmallMark(t *testing.T) {
	m, _, _ := newTestModel(t, 2)
	m.width = lipgloss.Width(markBlock) - 1
	view := m.splash()
	if hasMark(view) {
		t.Fatalf("the block was drawn into a window with no room for it:\n%s", view)
	}
	if !strings.Contains(view, markWord) {
		t.Fatalf("the narrow splash lost the mark altogether:\n%s", view)
	}
	if got := lipgloss.Width(view); got > m.width {
		t.Errorf("the splash is %d columns wide in a %d-column window", got, m.width)
	}
}
