package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mattwalters/lerp/internal/logfmt"
)

// logRows bounds the rendered scrollback in lines, the way tailScrollback
// bounds the raw bytes: the firehose is ephemeral (SCOPE invariant 7), and
// the operator wants the recent tail, not an archive.
const logRows = 400

// logRow is one rendered row of the pane, already styled and already inert.
type logRow struct {
	text     string
	wrap     bool // prose, rewrapped to whatever width the pane has now
	thinking bool // the collapsed heartbeat, rewritten in place
	tokens   int  // the count that heartbeat carries
}

// logView is the lane pane's rendered form of a runner's log: activity, not
// stream JSON. It decodes what the tail read (see internal/logfmt) and keeps
// the rendered rows; the raw bytes stay in the tail, both for the raw toggle
// and because nothing about the file on disk changes.
//
// Agent output is untrusted the same way Linear text is — a tool result holds
// whatever a command printed — so every decoded field goes through clean on
// its way into a row, and a raw line through cleanLog, which keeps the colour
// a plain-text runner meant.
type logView struct {
	stream logfmt.Stream
	rows   []logRow
}

// skipLine drops the bytes up to the next newline, for a tail that attached
// partway through one.
func (v *logView) skipLine() {
	v.stream.SkipLine()
}

// feed decodes bytes the tail just read.
func (v *logView) feed(p []byte) {
	for _, ev := range v.stream.Feed(p) {
		v.add(ev)
	}
	if n := len(v.rows) - logRows; n > 0 {
		v.rows = slices.Delete(v.rows, 0, n)
	}
}

func (v *logView) add(ev logfmt.Event) {
	if v.stream.Raw() {
		// The floor: an unrecognized stream is the bytes it wrote, which is
		// what this pane showed before any of this existed.
		v.rows = append(v.rows, logRow{text: cleanLog(ev.Text)})
		return
	}
	row, ok := logRowFor(ev)
	if !ok {
		return
	}
	if n := len(v.rows) - 1; row.thinking && n >= 0 && v.rows[n].thinking {
		// One line per thinking stretch, rewritten in place — a line per
		// event would bury the pane in heartbeat. The count only ever grows
		// within a stretch, so a countless event (the finished block, or a
		// runner that streams no count) never erases one.
		if row.tokens >= v.rows[n].tokens {
			v.rows[n] = row
		}
		return
	}
	v.rows = append(v.rows, row)
}

// render lays the rows out for a pane this wide. Prose is rewrapped, because
// an agent's paragraph is not a line; everything else is one line by
// construction and panelBox cuts what does not fit.
func (v *logView) render(width int) string {
	var b strings.Builder
	for i, row := range v.rows {
		if i > 0 {
			b.WriteString("\n")
		}
		if row.wrap && width > 0 {
			b.WriteString(ansi.Wrap(row.text, width, ""))
			continue
		}
		b.WriteString(row.text)
	}
	// A raw line appears as it is written, half-finished, the way it does
	// today. A half-decoded event is garbage, so it waits for its newline.
	if p := v.stream.Pending(); p != "" && v.stream.Raw() {
		if len(v.rows) > 0 {
			b.WriteString("\n")
		}
		b.WriteString(cleanLog(p))
	}
	return b.String()
}

// logRowFor renders one event. The six kinds it knows are the pane; anything
// else was already dropped by the decoder.
func logRowFor(ev logfmt.Event) (logRow, bool) {
	switch ev.Kind {
	case logfmt.KindInit:
		return logRow{text: styleFaint.Render("⏵ " + clean(ev.Text))}, true
	case logfmt.KindThinking:
		text := "✻ thinking…"
		if ev.Tokens > 0 {
			text += " " + commas(ev.Tokens) + " tokens"
		}
		return logRow{text: styleFaint.Render(text), thinking: true, tokens: ev.Tokens}, true
	case logfmt.KindText:
		text := cleanProse(ev.Text)
		if text == "" {
			return logRow{}, false
		}
		return logRow{text: text, wrap: true}, true
	case logfmt.KindToolCall:
		text := styleRunning.Render("⏺ ") + styleTicket.Render(clean(ev.Tool))
		if ev.Text != "" {
			text += " " + styleFaint.Render(clean(ev.Text))
		}
		return logRow{text: text}, true
	case logfmt.KindToolResult:
		text := clean(ev.Text)
		if text == "" {
			return logRow{}, false
		}
		return logRow{text: "  " + resultStyle(ev.IsError).Render("⎿ "+text)}, true
	case logfmt.KindResult:
		return logRow{text: resultStyle(ev.IsError).Render("⏹ " + clean(ev.Text))}, true
	}
	return logRow{}, false
}

func resultStyle(isErr bool) lipgloss.Style {
	if isErr {
		return styleErr
	}
	return styleFaint
}

// cleanProse is clean for text that is allowed to be a paragraph: every line
// is made inert on its own, and the blank runs an agent's markdown leaves
// behind collapse, so a lane pane is not mostly empty rows.
func cleanProse(s string) string {
	var lines []string
	blank := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(clean(line), " ")
		if line == "" {
			if blank || len(lines) == 0 {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		lines = append(lines, line)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n ")
}

// commas groups a token count for reading: 5850 is 5,850.
func commas(n int) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
