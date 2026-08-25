package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Linear stores descriptions and comments as markdown, so the main pane used
// to read as a paste of a file: literal ** around a heading, literal
// backticks, `*` for a bullet. This renders the handful of constructs a
// ticket actually uses — headings, emphasis, code, lists, quotes, links,
// rules — in the styles the rest of the board already speaks.
//
// Not Glamour: it is the same ecosystem, but it would carry goldmark, chroma
// and a theme system for one pane of ticket prose, and its themes paint a
// screen that looks like Glow rather than like this board. What is here
// instead is bounded — it renders what Linear emits and leaves the rest as
// the text it is.
//
// Two constraints shape it. It runs at render time on text cleanText has
// already made inert (see sanitize.go), so it is the only thing in the pane
// emitting escapes — and every styled span is rendered whole on the line it
// sits on, closed there, so nothing bleeds past the panel's border. And it
// does its own wrapping to the width it is handed — the pane's inner width —
// so panelBox, which truncates rather than wraps, is never handed a line to
// cut.

// Markdown is structure, not state, so it is drawn in attributes — bold,
// italic, strikethrough — rather than in the palette's colors, which mark
// state. Code is the exception: it borrows the faint ramp the chrome already
// uses, so an identifier in prose sits apart without inventing a color.
var styleCode = lipgloss.NewStyle().Foreground(colorFaint)

var (
	mdHeading = regexp.MustCompile(`^ {0,3}(#{1,6}) +(.*?) *#* *$`)
	mdItem    = regexp.MustCompile(`^( *)(?:([-*+])|([0-9]{1,9})[.)]) +(.*)$`)
	mdQuote   = regexp.MustCompile(`^ {0,3}> ?(.*)$`)
	mdLink    = regexp.MustCompile(`^!?\[([^\]]*)\]\(([^)\s]*)(?: +"[^"]*")?\)`)
)

// renderMarkdown renders s as the lines the pane draws, wrapped to width.
// panelBox truncates its rows instead of wrapping — right for a one-line
// list row, wrong for a ticket body, where it would throw away everything
// past the first line.
func renderMarkdown(s string, width int) []string {
	r := &markdown{width: max(8, width)}
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		i = r.block(lines, i)
	}
	r.flush()
	return r.out
}

// markdown is one render in progress: the lines drawn so far, and the
// paragraph still being read, which only ends at a blank line or another
// block.
type markdown struct {
	width int
	out   []string
	para  []string
}

// block draws whatever starts at lines[i] and returns the last line it
// consumed — one line for most blocks, several for the ones that run on.
func (r *markdown) block(lines []string, i int) int {
	line := lines[i]
	trim := strings.TrimSpace(line)
	switch {
	case trim == "":
		r.flush()
		r.blank()
	case fenceMark(trim) != "":
		r.flush()
		return r.fenced(lines, i)
	case horizontalRule(trim):
		r.flush()
		r.push(styleFaint.Render(strings.Repeat("─", r.width)))
	case mdHeading.MatchString(line):
		r.flush()
		// A heading is the one thing that buys itself air: the blank line
		// above it is what makes a plan's sections findable at a glance.
		r.blank()
		r.push(r.wrap(bold(inline(mdHeading.FindStringSubmatch(line)[2])), "", "")...)
	case mdQuote.MatchString(line):
		r.flush()
		return r.quote(lines, i)
	case mdItem.MatchString(line):
		r.flush()
		return r.item(lines, i)
	default:
		r.para = append(r.para, trim)
	}
	return i
}

// flush draws the paragraph being read. Its source lines are joined before
// wrapping, because a plan hard-wrapped at 72 columns is still one
// paragraph, and reading it as the pane's own lines is what leaves the
// ragged right edge that made this look like a file.
func (r *markdown) flush() {
	if len(r.para) == 0 {
		return
	}
	text := strings.Join(r.para, " ")
	r.para = nil
	r.push(r.wrap(inline(text), "", "")...)
}

func (r *markdown) push(lines ...string) {
	r.out = append(r.out, lines...)
}

// blank separates blocks with at most one empty line, however many the
// source used, and never opens the pane with one.
func (r *markdown) blank() {
	if n := len(r.out); n > 0 && r.out[n-1] != "" {
		r.out = append(r.out, "")
	}
}

// fenced draws a code block: the lines as written, behind a faint gutter and
// never rewrapped — a wrapped line of code is a lie about the code. A line
// too wide for the pane is cut by panelBox, the way a log line is. An
// unclosed fence runs to the end of the body, which is what the source says.
func (r *markdown) fenced(lines []string, i int) int {
	mark := fenceMark(strings.TrimSpace(lines[i]))
	gutter := styleFaint.Render("│ ")
	for i++; i < len(lines); i++ {
		if t := strings.TrimSpace(lines[i]); strings.HasPrefix(t, mark) && strings.Trim(t, mark[:1]) == "" {
			return i
		}
		r.push(gutter + lines[i])
	}
	return i - 1
}

// quote draws a run of quoted lines as one faint paragraph behind a gutter:
// in a ticket a quote is nearly always context somebody is answering, so it
// reads as background to the reply under it.
func (r *markdown) quote(lines []string, i int) int {
	var text []string
	for ; i < len(lines); i++ {
		m := mdQuote.FindStringSubmatch(lines[i])
		if m == nil {
			break
		}
		text = append(text, strings.TrimSpace(m[1]))
	}
	gutter := styleFaint.Render("│ ")
	r.push(r.wrap(dim(inline(strings.Join(text, " "))), gutter, gutter)...)
	return i - 1
}

// item draws one list item, marker and all, hanging its continuation lines
// under the text rather than under the marker. Lines below it that start no
// block of their own belong to the item (markdown's lazy continuation), which
// is how a wrapped bullet in a Linear body arrives.
func (r *markdown) item(lines []string, i int) int {
	m := mdItem.FindStringSubmatch(lines[i])
	text := []string{m[4]}
	for i+1 < len(lines) && plainLine(lines[i+1]) {
		text = append(text, strings.TrimSpace(lines[i+1]))
		i++
	}
	marker := "• "
	if m[3] != "" {
		marker = m[3] + ". "
	}
	// A deeply nested list still has to leave room for prose, so the source's
	// indent is honored only up to a quarter of the pane.
	indent := strings.Repeat(" ", min(len(m[1]), r.width/4))
	r.push(r.wrap(inline(strings.Join(text, " ")),
		indent+marker, indent+strings.Repeat(" ", lipgloss.Width(marker)))...)
	return i
}

// plainLine reports whether line is ordinary text — neither blank nor the
// start of a block.
func plainLine(line string) bool {
	trim := strings.TrimSpace(line)
	return trim != "" && fenceMark(trim) == "" && !horizontalRule(trim) &&
		!mdHeading.MatchString(line) && !mdQuote.MatchString(line) && !mdItem.MatchString(line)
}

// horizontalRule reports whether a line is a thematic break: three or more
// of the same marker, with nothing but spaces between them. Regexp is no use
// here — RE2 has no backreference to say "the same one again".
func horizontalRule(trim string) bool {
	marker, n := byte(0), 0
	for i := 0; i < len(trim); i++ {
		switch c := trim[i]; c {
		case '-', '*', '_':
			if marker == 0 {
				marker = c
			}
			if c != marker {
				return false
			}
			n++
		case ' ':
		default:
			return false
		}
	}
	return n >= 3
}

// fenceMark returns the fence a line opens with, or "" for a line that opens
// none.
func fenceMark(trim string) string {
	for _, mark := range []string{"```", "~~~"} {
		if strings.HasPrefix(trim, mark) {
			return mark
		}
	}
	return ""
}

// span is a run of text carrying one style. Inline markup is parsed into
// spans and only rendered once the wrap has decided which line each sits on,
// so a style never spans a line break and never leaves an escape open.
type span struct {
	text  string
	style lipgloss.Style
}

// word is one whitespace-delimited token, in the spans it is made of —
// `**bo**ld` is one word of two.
type word []span

// inline parses one line's markup: code, strong, emphasis, strikethrough and
// links. Anything that does not close is the literal text it was, which is
// the right answer for prose that merely contains an asterisk.
func inline(s string) []span {
	var out []span
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			out = append(out, span{text: plain.String()})
			plain.Reset()
		}
	}
	for i := 0; i < len(s); {
		rest := s[i:]
		switch {
		case rest[0] == '\\' && len(rest) > 1 && punctuation(rest[1]):
			plain.WriteByte(rest[1])
			i += 2
			continue
		case rest[0] == '`':
			if text, n := codeSpan(rest); n > 0 {
				flush()
				out = append(out, span{text: text, style: styleCode})
				i += n
				continue
			}
		case strings.HasPrefix(rest, "**"), strings.HasPrefix(rest, "__"):
			if text, n := delimited(rest, rest[:2]); n > 0 {
				flush()
				out = append(out, bold(inline(text))...)
				i += n
				continue
			}
		case strings.HasPrefix(rest, "~~"):
			if text, n := delimited(rest, rest[:2]); n > 0 {
				flush()
				out = append(out, struck(inline(text))...)
				i += n
				continue
			}
		case rest[0] == '*' || (rest[0] == '_' && !wordByte(byteAt(s, i-1))):
			// An underscore inside a word is a name, not emphasis:
			// snake_case_like_this stays as written.
			if text, n := delimited(rest, rest[:1]); n > 0 && !wordByte(byteAt(rest, n)) {
				flush()
				out = append(out, italic(inline(text))...)
				i += n
				continue
			}
		case rest[0] == '[' || strings.HasPrefix(rest, "!["):
			if m := mdLink.FindStringSubmatch(rest); m != nil {
				flush()
				out = append(out, linkSpans(m[1], m[2])...)
				i += len(m[0])
				continue
			}
		}
		plain.WriteByte(rest[0])
		i++
	}
	flush()
	return out
}

// linkSpans renders a link as its text followed by where it goes. Nothing in
// this pane is clickable — `o` opens the ticket in Linear and that is the
// whole of it — so a link that hid its target would just be prose the
// operator could not follow.
func linkSpans(text, url string) []span {
	if strings.TrimSpace(text) == "" {
		text = url
	}
	out := inline(text)
	if url != "" && url != text {
		out = append(out, span{text: " (" + url + ")", style: styleFaint})
	}
	return out
}

// codeSpan reads a backtick span: a run of backticks closed by a run the
// same length. Returns the code and the bytes it consumed, or 0 for an
// opener that never closes.
func codeSpan(s string) (string, int) {
	n := 0
	for n < len(s) && s[n] == '`' {
		n++
	}
	j := strings.Index(s[n:], s[:n])
	if j <= 0 {
		return "", 0
	}
	return strings.TrimSpace(s[n : n+j]), n + j + n
}

// delimited reads a stretch opened by d and closed by the next d that closes
// anything: not immediately, and not after a space, which in markdown is a
// literal asterisk rather than a closer.
func delimited(s, d string) (string, int) {
	body := s[len(d):]
	if body == "" || body[0] == ' ' {
		return "", 0
	}
	for j := 1; j+len(d) <= len(body); j++ {
		if strings.HasPrefix(body[j:], d) && body[j-1] != ' ' {
			return body[:j], len(d) + j + len(d)
		}
	}
	return "", 0
}

func byteAt(s string, i int) byte {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

// wordByte reports whether c is a byte a word is made of, for the flanking
// rules that keep an underscore inside an identifier out of the renderer's
// way.
func wordByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= 0x80
}

func punctuation(c byte) bool {
	return strings.IndexByte("\\`*_{}[]()#+-.!<>~|", c) >= 0
}

// bold, italic, struck and dim apply one attribute to an already-parsed
// stretch, so `**bold with `code`**` keeps both.
func bold(sp []span) []span {
	return restyle(sp, func(s lipgloss.Style) lipgloss.Style { return s.Bold(true) })
}
func italic(sp []span) []span {
	return restyle(sp, func(s lipgloss.Style) lipgloss.Style { return s.Italic(true) })
}

func struck(sp []span) []span {
	return restyle(sp, func(s lipgloss.Style) lipgloss.Style { return s.Strikethrough(true) })
}

func dim(sp []span) []span {
	return restyle(sp, func(s lipgloss.Style) lipgloss.Style { return s.Foreground(colorFaint) })
}

func restyle(sp []span, f func(lipgloss.Style) lipgloss.Style) []span {
	for i := range sp {
		sp[i].style = f(sp[i].style)
	}
	return sp
}

// wrap lays spans out as lines no wider than the pane, breaking at spaces:
// first prefixes the opening line, rest every line under it.
func (r *markdown) wrap(sp []span, first, rest string) []string {
	var out []string
	var b strings.Builder
	prefix, lineW := first, 0
	flush := func() {
		out = append(out, strings.TrimRight(prefix+b.String(), " "))
		b.Reset()
		prefix, lineW = rest, 0
	}
	words := split(sp)
	for i := 0; i < len(words); {
		budget := max(1, r.width-lipgloss.Width(prefix))
		w := words[i]
		width := spansWidth(w)
		switch {
		case lineW > 0 && lineW+1+width > budget:
			flush()
			continue
		case width > budget:
			// A word wider than the line — a URL, a path — is broken rather
			// than allowed to push the pane out of shape.
			pieces := breakWord(w, budget)
			for _, p := range pieces[:len(pieces)-1] {
				b.WriteString(render(p))
				flush()
			}
			w = pieces[len(pieces)-1]
			width = spansWidth(w)
		}
		if lineW > 0 {
			b.WriteString(" ")
			lineW++
		}
		b.WriteString(render(w))
		lineW += width
		i++
	}
	flush()
	return out
}

// split cuts spans into words, keeping a word whole across a style boundary.
func split(sp []span) []word {
	var out []word
	joined := false
	for _, s := range sp {
		for i, part := range strings.Split(s.text, " ") {
			if i > 0 || part == "" {
				joined = false
			}
			if part == "" {
				continue
			}
			piece := span{text: part, style: s.style}
			if joined {
				out[len(out)-1] = append(out[len(out)-1], piece)
			} else {
				out = append(out, word{piece})
			}
			joined = true
		}
	}
	return out
}

// breakWord cuts a word into pieces of at most budget cells.
func breakWord(w word, budget int) []word {
	var out []word
	var cur word
	curW := 0
	for _, s := range w {
		var b strings.Builder
		for _, r := range s.text {
			rw := ansi.StringWidth(string(r))
			if curW+rw > budget {
				if b.Len() > 0 {
					cur = append(cur, span{text: b.String(), style: s.style})
					b.Reset()
				}
				out = append(out, cur)
				cur, curW = nil, 0
			}
			b.WriteRune(r)
			curW += rw
		}
		if b.Len() > 0 {
			cur = append(cur, span{text: b.String(), style: s.style})
		}
	}
	return append(out, cur)
}

func spansWidth(w word) int {
	n := 0
	for _, s := range w {
		n += ansi.StringWidth(s.text)
	}
	return n
}

func render(w word) string {
	var b strings.Builder
	for _, s := range w {
		b.WriteString(s.style.Render(s.text))
	}
	return b.String()
}
