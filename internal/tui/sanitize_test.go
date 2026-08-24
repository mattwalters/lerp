package tui

import (
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/loop"
)

// Anyone who can title a ticket can put escape sequences in one. clean is
// what makes them inert before they become model state.
func TestCleanDefusesEscapeSequences(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"osc title", "\x1b]0;pwned\x07LERP-1", "LERP-1"},
		{"osc clipboard", "\x1b]52;c;cGF5bG9hZA==\x1b\\LERP-1", "LERP-1"},
		{"erase screen", "\x1b[2Jwipe", "wipe"},
		{"cursor home", "\x1b[1;1Hhome", "home"},
		{"eight-bit csi", "\x9b2Jwipe", "wipe"},
		// ansi.Strip keeps the C0 bytes it walks past, so these are the ones
		// a naive strip would let through.
		{"carriage return", "innocent\rEVIL", "innocent EVIL"},
		{"backspace", "back\bspace", "backspace"},
		{"delete", "del\x7fete", "delete"},
		{"newline", "two\nrows", "two rows"},
		{"tab", "a\tb", "a b"},
		{"bell", "ring\x07ring", "ringring"},
		{"reset charset", "\x1bcreset", "reset"},
		// Ordinary titles survive untouched — sanitizing must not cost the
		// operator the em dash or the emoji their tickets are named with.
		{"unicode passes through", "Fix ✅ the — 日本語 test", "Fix ✅ the — 日本語 test"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clean(tc.in); got != tc.want {
				t.Fatalf("clean(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The log pane is the one place color is legitimate: agent output is
// colored on purpose, so SGR stays and everything else goes.
func TestCleanLogKeepsColorAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"sgr survives", "\x1b[31mred\x1b[0m\n", "\x1b[31mred\x1b[0m\n"},
		{"true color survives", "\x1b[38;2;255;0;0mred\x1b[m\n", "\x1b[38;2;255;0;0mred\x1b[m\n"},
		// An SGR left open would paint the rest of the pane, borders
		// included; the line closes itself instead.
		{"open sgr is closed at the line", "\x1b[31mred\nplain\n", "\x1b[31mred\x1b[0m\nplain\n"},
		{"open sgr is closed at the end", "\x1b[31mred", "\x1b[31mred\x1b[0m"},
		{"osc title", "\x1b]0;pwned\x07log line\n", "log line\n"},
		{"osc clipboard", "\x1b]52;c;cGF5bG9hZA==\x1b\\log\n", "log\n"},
		{"erase and home", "\x1b[2J\x1b[1;1Hwiped\n", "wiped\n"},
		{"cursor hide is not color", "\x1b[?25lhidden\n", "hidden\n"},
		{"private sgr-alike is not color", "\x1b[>4;2mmode\n", "mode\n"},
		{"apc payload", "\x1b_apc\x1b\\after\n", "after\n"},
		{"carriage return", "progress\rdone\n", "progressdone\n"},
		{"tabs and newlines are the pane's own", "a\tb\nc\n", "a\tb\nc\n"},
		// A sequence cut off by the scrollback window must not swallow the
		// terminal's state, and must not spin the decoder either.
		{"unterminated osc", "before \x1b]0;xyz", "before "},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanLog(tc.in); got != tc.want {
				t.Fatalf("cleanLog(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// cleanEvent is the boundary itself: every Linear-sourced string on an
// event, including the ones that are never rendered — the URL handed to the
// OS opener, the ticket ID handed to Promote.
func TestCleanEventScrubsEveryLinearString(t *testing.T) {
	hostile := "\x1b]0;pwned\x07\x1b[2Jgotcha\r"
	ev := cleanEvent(loop.Event{
		Type: loop.EventQueues, Ticket: hostile, TicketID: hostile, Queue: hostile,
		Queues: []loop.QueueSnapshot{{
			Team: hostile, Name: hostile, Status: hostile,
			Tickets: []loop.QueueTicket{{
				ID: hostile, Identifier: hostile, Title: hostile, URL: hostile,
				BlockedBy: []string{hostile},
			}},
		}},
		Attention: []loop.AttentionItem{{
			Group: loop.AttentionGroup(hostile), Ticket: hostile, TicketID: hostile,
			Title: hostile, Status: hostile, Reason: hostile, URL: hostile,
		}},
	})

	q, tk, it := ev.Queues[0], ev.Queues[0].Tickets[0], ev.Attention[0]
	for name, got := range map[string]string{
		"Ticket": ev.Ticket, "TicketID": ev.TicketID, "Queue": ev.Queue,
		"Queues.Team": q.Team, "Queues.Name": q.Name, "Queues.Status": q.Status,
		"Tickets.ID": tk.ID, "Tickets.Identifier": tk.Identifier,
		"Tickets.Title": tk.Title, "Tickets.URL": tk.URL,
		"Tickets.BlockedBy": tk.BlockedBy[0],
		"Attention.Group":   string(it.Group), "Attention.Ticket": it.Ticket,
		"Attention.TicketID": it.TicketID, "Attention.Title": it.Title,
		"Attention.Status": it.Status, "Attention.Reason": it.Reason,
		"Attention.URL": it.URL,
	} {
		if got != "gotcha " {
			t.Errorf("%s = %q, want the cleaned string", name, got)
		}
	}
}

// The event's own slices belong to the loop, which emits them from its
// goroutines; cleaning must copy rather than write back into them.
func TestCleanEventDoesNotWriteBackIntoTheEvent(t *testing.T) {
	ev := loop.Event{
		Queues: []loop.QueueSnapshot{{Name: "q\rx", Tickets: []loop.QueueTicket{
			{Title: "t\rx", BlockedBy: []string{"b\rx"}}}}},
		Attention: []loop.AttentionItem{{Title: "a\rx"}},
	}
	cleanEvent(ev)
	if got := ev.Queues[0].Name; got != "q\rx" {
		t.Errorf("queue name was mutated in place: %q", got)
	}
	if got := ev.Queues[0].Tickets[0].Title; got != "t\rx" {
		t.Errorf("ticket title was mutated in place: %q", got)
	}
	if got := ev.Queues[0].Tickets[0].BlockedBy[0]; got != "b\rx" {
		t.Errorf("blocker was mutated in place: %q", got)
	}
	if got := ev.Attention[0].Title; got != "a\rx" {
		t.Errorf("attention title was mutated in place: %q", got)
	}
}

// escapeFree is the property every view has to hold: whatever Linear says,
// the rendered screen carries no sequence that can move the cursor, erase,
// or talk to the emulator.
func escapeFree(t *testing.T, what, view string) {
	t.Helper()
	for _, bad := range []string{"\x1b]", "\x1b[2J", "\x1b[1;1H", "\x1b_", "\x1bP", "\r", "\x07", "\x08", "\x7f"} {
		if strings.Contains(view, bad) {
			t.Fatalf("%s contains %q:\n%q", what, bad, view)
		}
	}
}
