package tui

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/loop"
)

// Everything Linear hands the TUI is untrusted text. Anyone who can title a
// ticket in a served workspace writes into these strings, and Bubble Tea
// hands the View string to the terminal as-is — so a title carrying escape
// sequences would repaint rows, rewrite the chrome, or reach for the OSC
// commands some emulators still implement badly.
//
// clean is that boundary for a single-line field. ansi.Strip alone is not
// enough: its parser deliberately keeps the C0 bytes it walks past, so
// "innocent\rEVIL" survives it and still rewrites its row. Strip the
// sequences, then drop the control runes they were built from.
func clean(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range ansi.Strip(s) {
		switch {
		case r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r':
			// A one-line field stays one line: whitespace that would add
			// rows the layout never budgeted for becomes a plain space.
			b.WriteRune(' ')
		case control(r) || format(r) || r == utf8.RuneError:
			// Dropped: C0, DEL, the 8-bit C1 introducers, the format
			// characters that reorder or hide the text around them, and the
			// bytes of a broken UTF-8 sequence.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cleanText is clean's counterpart for the prose the inbox pane reads
// out of Linear: a ticket body and the comments on it, which are many lines
// by design, so newlines survive. Nothing else does — unlike cleanLog there
// is no SGR exemption, because a ticket body has no legitimate reason to
// paint the operator's terminal.
func cleanText(s string) string {
	// A CRLF body would otherwise leave a trailing space on every line.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range ansi.Strip(s) {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t' || r == '\v' || r == '\f' || r == '\r':
			// Rows the layout budgeted for stay budgeted for: the whitespace
			// that would move the cursor becomes a plain space.
			b.WriteRune(' ')
		case control(r) || format(r) || r == utf8.RuneError:
			// Dropped: C0, DEL, the 8-bit C1 introducers, the format
			// characters that reorder or hide the text around them, and the
			// bytes of a broken UTF-8 sequence.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// issueTag matches the inline issue reference Linear puts in body text:
// <issue id="…" href="…">LERP-22</issue>. Only a closed tag matches, so a
// truncated one is left as the visible text it is rather than swallowing
// the rest of the body.
var issueTag = regexp.MustCompile(`<issue\b[^>]*>([^<]*)</issue>`)

// reduceIssueTags rewrites those references to the identifier they name.
// Rendered raw they are unreadable, and the pane deliberately has no
// markdown renderer to do it. It runs after cleanText, so the pattern only
// ever sees text that is already inert.
func reduceIssueTags(s string) string {
	return issueTag.ReplaceAllString(s, "$1")
}

// cleanDetail returns d with every Linear-sourced string made inert. The
// detail fetch calls it on the way in, the same rule cleanEvent follows:
// model state is safe before it is stored, so no view has to remember.
func cleanDetail(d linear.IssueDetail) linear.IssueDetail {
	out := linear.IssueDetail{Body: reduceIssueTags(cleanText(d.Body))}
	out.Comments = make([]linear.Comment, len(d.Comments))
	for i, c := range d.Comments {
		out.Comments[i] = linear.Comment{
			Author:    clean(c.Author),
			Body:      reduceIssueTags(cleanText(c.Body)),
			CreatedAt: c.CreatedAt,
		}
	}
	return out
}

// cleanLog is clean's counterpart for the log pane, where the bytes come
// from an agent rather than from Linear and color is legitimate. It keeps
// SGR — the one sequence class that paints without moving the cursor — and
// drops everything else: OSC title and clipboard writes, cursor motion,
// erases, DCS/APC/PM payloads, bare C0. Newlines and tabs stay; the pane is
// many lines by design.
//
// This runs at render time rather than inside tail.read because a sequence
// split across two polled reads would walk straight through an incremental
// sanitizer.
func cleanLog(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	state := ansi.NormalState
	painted := false // an SGR is open on the line being written
	for len(s) > 0 {
		seq, _, n, next := ansi.DecodeSequence(s, state, nil)
		if n == 0 {
			// A cancelled sequence consumes nothing. Re-read the byte in
			// the ground state, where decoding always advances.
			if state == ansi.NormalState {
				break
			}
			state = ansi.NormalState
			continue
		}
		s, state = s[n:], next
		switch {
		case seq == "\n":
			// An unterminated SGR would bleed its color off the end of the
			// line and out through the panel's border, so every line that
			// painted closes itself.
			if painted {
				b.WriteString(sgrReset)
				painted = false
			}
			b.WriteString(seq)
		case seq == "\t":
			b.WriteString(seq)
		case sgr(seq):
			painted = paints(seq)
			b.WriteString(seq)
		case printable(seq):
			b.WriteString(seq)
		}
	}
	if painted {
		b.WriteString(sgrReset)
	}
	return b.String()
}

const sgrReset = "\x1b[0m"

// sgr reports whether seq is a plain Select Graphic Rendition sequence:
// ESC [, digits and separators, final "m". The private and intermediate
// forms (CSI ? … m and friends) are not color and do not get the
// exemption; neither does the 8-bit introducer, which is not valid UTF-8.
func sgr(seq string) bool {
	if !strings.HasPrefix(seq, "\x1b[") || !strings.HasSuffix(seq, "m") {
		return false
	}
	for _, c := range []byte(seq[2 : len(seq)-1]) {
		if (c < '0' || c > '9') && c != ';' && c != ':' {
			return false
		}
	}
	return true
}

// paints reports whether an SGR sequence leaves an attribute set. An empty
// or all-zero parameter list is a reset, which closes the line instead of
// opening it, so it needs no reset of its own.
func paints(seq string) bool {
	return strings.Trim(seq[2:len(seq)-1], "0;:") != ""
}

// printable reports whether seq is ordinary text: valid UTF-8 carrying no
// control rune. Every escape sequence opens with a control byte, so this is
// also what rejects whatever cleanLog does not explicitly keep.
func printable(seq string) bool {
	if !utf8.ValidString(seq) {
		return false
	}
	for _, r := range seq {
		if control(r) {
			return false
		}
	}
	return true
}

// control reports whether r is a control character: C0, DEL, or the C1
// range that carries the 8-bit forms of the escape introducers.
func control(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// format reports whether r is a Unicode format character (category Cf).
// These carry no glyph and execute nothing; what they do is rearrange and
// hide the text they sit in, which is enough to make a row lie about the
// ticket it names. U+202E RIGHT-TO-LEFT OVERRIDE and the U+2066-2069
// isolates reverse the identifier, status and title an inbox row puts in a
// fixed order; the zero-width joiners and the tag characters hide runes
// inside a word that the operator then reads as a shorter one. The whole
// category goes rather than the bidi set alone, because the difference is
// a hand-maintained list against the next Unicode release.
//
// Linear text and an agent's decoded log fields both go through clean, so
// this is the rule for both — a lane pane names which ticket a run is on,
// and the run's own output must not be able to reorder that either. The
// cost is display fidelity in text that meant them: a ZWJ emoji comes apart
// into its components, an Arabic word loses the joiner that shaped it.
// cleanLog is the one path that keeps them, because the raw stream it
// renders is the fallback that reproduces what a runner printed verbatim.
// That is a real residual: an override written into a raw log line can
// reorder the rest of that row, the pane's border included, the way an
// unterminated SGR would. Unlike the SGR, closing it needs the nesting
// tracked rather than a reset appended, and the row it can spoil names no
// ticket — so the exemption stands and this is what it costs.
func format(r rune) bool {
	return unicode.Is(unicode.Cf, r)
}

// cleanEvent returns ev with every Linear-sourced string cleaned. apply
// calls it on the way in, which is the whole point: model state is inert
// before it is stored, so no view has to remember to sanitize — and the
// strings that are not rendered at all, the URL handed to the OS opener and
// the ticket ID handed to Promote, are covered by the same pass.
func cleanEvent(ev loop.Event) loop.Event {
	ev.TicketID = clean(ev.TicketID)
	ev.Ticket = clean(ev.Ticket)
	ev.Queue = clean(ev.Queue)
	ev.Note = clean(ev.Note)
	ev.Queues = slices.Clone(ev.Queues)
	for i := range ev.Queues {
		q := &ev.Queues[i]
		q.Team, q.Name, q.Status = clean(q.Team), clean(q.Name), clean(q.Status)
		q.Tickets = slices.Clone(q.Tickets)
		for j := range q.Tickets {
			tk := &q.Tickets[j]
			tk.ID, tk.Identifier = clean(tk.ID), clean(tk.Identifier)
			tk.Title, tk.URL = clean(tk.Title), clean(tk.URL)
			tk.BlockedBy = slices.Clone(tk.BlockedBy)
			for k, id := range tk.BlockedBy {
				tk.BlockedBy[k] = clean(id)
			}
		}
	}
	ev.Attention = slices.Clone(ev.Attention)
	for i := range ev.Attention {
		it := &ev.Attention[i]
		it.Ticket, it.TicketID = clean(it.Ticket), clean(it.TicketID)
		it.Title, it.Status = clean(it.Title), clean(it.Status)
		it.Project = clean(it.Project)
		it.Reason, it.URL = clean(it.Reason), clean(it.URL)
	}
	return ev
}
