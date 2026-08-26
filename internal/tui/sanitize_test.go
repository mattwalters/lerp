package tui

import (
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/linear"
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
		// Category Cf: no glyph, no execution, but a row that reads as a
		// different ticket than the one it names.
		{"rtl override", "LERP-1 \u202egnitratS", "LERP-1 gnitratS"},
		{"bidi isolates", "\u2066LERP-1\u2069 \u2068title\u2069", "LERP-1 title"},
		{"left-to-right mark", "LERP-\u200e1", "LERP-1"},
		{"zero-width joiner", "LERP\u200d-1", "LERP-1"},
		{"zero-width space", "LE\u200bRP-1", "LERP-1"},
		{"byte order mark", "\ufeffLERP-1", "LERP-1"},
		{"soft hyphen", "LERP\u00ad-1", "LERP-1"},
		{"tag characters", "LERP-1\U000e0074\U000e0061\U000e0067", "LERP-1"},
		// Ordinary titles survive untouched — sanitizing must not cost the
		// operator the em dash or the emoji their tickets are named with.
		{"unicode passes through", "Fix ✅ the — 日本語 test", "Fix ✅ the — 日本語 test"},
		// A variation selector is a combining mark, not a format character:
		// dropping it would change how the emoji beside it renders.
		{"emoji variation selector survives", "warn \u26a0\ufe0f now", "warn \u26a0\ufe0f now"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clean(tc.in); got != tc.want {
				t.Fatalf("clean(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A ticket body is many lines and no color: cleanText keeps the newlines
// clean drops, and drops the paint cleanLog keeps.
func TestCleanTextKeepsRowsAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"newlines survive", "one\ntwo\nthree", "one\ntwo\nthree"},
		{"crlf collapses", "one\r\ntwo", "one\ntwo"},
		{"bare carriage return", "innocent\rEVIL", "innocent EVIL"},
		{"tab", "a\tb", "a b"},
		// Unlike the log pane, Linear text never gets to paint.
		{"sgr goes", "\x1b[31mred\x1b[0m", "red"},
		{"osc title", "\x1b]0;pwned\x07LERP-1", "LERP-1"},
		{"osc clipboard", "\x1b]52;c;cGF5bG9hZA==\x1b\\LERP-1", "LERP-1"},
		{"erase screen", "\x1b[2Jwipe", "wipe"},
		{"eight-bit csi", "\x9b2Jwipe", "wipe"},
		{"delete", "del\x7fete", "delete"},
		{"broken utf-8", "bad\xffbytes", "badbytes"},
		// A body is prose, and prose is where a spoofed identifier reads
		// most like the real thing.
		{"rtl override", "see \u202e63-PREL", "see 63-PREL"},
		{"bidi isolate spans a line", "one\n\u2066two\u2069\nthree", "one\ntwo\nthree"},
		{"zero-width joiner", "LERP\u200d-36", "LERP-36"},
		{"markdown passes through", "## Plan\n\n- one — 日本語", "## Plan\n\n- one — 日本語"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanText(tc.in); got != tc.want {
				t.Fatalf("cleanText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Linear writes inline issue references as tags. Rendered raw they are
// unreadable, and the pane has no markdown renderer to do better.
func TestReduceIssueTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"one tag", `blocked by <issue id="u-1" href="https://x/LERP-36">LERP-36</issue>.`,
			"blocked by LERP-36."},
		{"several tags", `<issue id="a" href="h">LERP-1</issue> and <issue id="b" href="h">LERP-2</issue>`,
			"LERP-1 and LERP-2"},
		{"attribute order", `<issue href="h" id="a">LERP-3</issue>`, "LERP-3"},
		// An unclosed tag is left as the text it is; swallowing the rest of
		// the body would be worse than showing the tag.
		{"unclosed tag", `<issue id="a">LERP-4 and the rest of the body`,
			`<issue id="a">LERP-4 and the rest of the body`},
		{"no tags", "plain body", "plain body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reduceIssueTags(tc.in); got != tc.want {
				t.Fatalf("reduceIssueTags(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// cleanDetail is the fetch\'s boundary, as cleanEvent is the pass\'s: what it
// stores is inert, whichever field the payload hid in.
func TestCleanDetailMakesTheWholeTicketInert(t *testing.T) {
	got := cleanDetail(linear.IssueDetail{
		Body: "body\x1b]0;pwned\x07 <issue id=\"a\" href=\"h\">LERP-9</issue>",
		Comments: []linear.Comment{{
			Author: "agent\rEVIL",
			Body:   "verdict\x1b[2J\nsecond line",
		}},
	})
	if strings.ContainsRune(got.Body, '\x1b') || !strings.Contains(got.Body, "LERP-9") ||
		strings.Contains(got.Body, "<issue") {
		t.Errorf("body = %q, want it inert with the tag reduced", got.Body)
	}
	c := got.Comments[0]
	if c.Author != "agent EVIL" {
		t.Errorf("author = %q, want the control byte gone", c.Author)
	}
	if strings.ContainsRune(c.Body, '\x1b') || !strings.Contains(c.Body, "\nsecond line") {
		t.Errorf("comment body = %q, want it inert with its rows kept", c.Body)
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
		Type: loop.EventQueues, Ticket: hostile, TicketID: hostile, Queue: hostile, Note: hostile,
		Queues: []loop.QueueSnapshot{{
			Team: hostile, Name: hostile, Status: hostile,
			Tickets: []loop.QueueTicket{{
				ID: hostile, Identifier: hostile, Title: hostile, URL: hostile,
				BlockedBy: []string{hostile},
			}},
		}},
		Attention: []loop.AttentionItem{{
			Ticket: hostile, TicketID: hostile, Title: hostile, Status: hostile,
			Project: hostile, Reason: hostile, URL: hostile,
		}},
	})

	q, tk, it := ev.Queues[0], ev.Queues[0].Tickets[0], ev.Attention[0]
	for name, got := range map[string]string{
		"Ticket": ev.Ticket, "TicketID": ev.TicketID, "Queue": ev.Queue, "Note": ev.Note,
		"Queues.Team": q.Team, "Queues.Name": q.Name, "Queues.Status": q.Status,
		"Tickets.ID": tk.ID, "Tickets.Identifier": tk.Identifier,
		"Tickets.Title": tk.Title, "Tickets.URL": tk.URL,
		"Tickets.BlockedBy": tk.BlockedBy[0],
		"Attention.Ticket":  it.Ticket, "Attention.TicketID": it.TicketID,
		"Attention.Title": it.Title, "Attention.Status": it.Status,
		"Attention.Project": it.Project, "Attention.Reason": it.Reason,
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
