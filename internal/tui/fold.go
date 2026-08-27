package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Folding lets a long ticket body read first as an outline of its headings
// and then, on demand, in full — vim's fold model, applied to markdown. It
// has to work on the source, not on renderMarkdown's output: by the time a
// heading has become a bold line of text, nothing says which lines under it
// belong to it. So this file re-scans the same source ticketLines already
// has, finds the headings renderMarkdown finds and then throws away, and
// composes the pane's lines section by section instead of in one call.
//
// Session-only and per ticket: folded state lives on ticketDetail (see
// model.go), which is fetched once per ticket for the process's lifetime —
// there is no refresh to lose it to, and a different ticket is simply a
// different map.

// heading is one heading line found outside a code fence, in document
// order: its level (the number of leading #s) and the source line it
// starts at. The render paths re-render src[h.line] directly rather than a
// captured copy, so the heading itself carries no text.
type heading struct {
	level int
	line  int
}

// headings finds every heading line in src, the way mdHeading and fenced
// (markdown.go) would together: a line inside a fenced block is code, never
// structure, so fold has to skip fences exactly as the renderer does, or a
// `#` in a code sample would cut sections the reader never wrote.
func headings(src []string) []heading {
	var out []heading
	for i := 0; i < len(src); i++ {
		trim := strings.TrimSpace(src[i])
		if mark := fenceMark(trim); mark != "" {
			i = skipFence(src, i, mark)
			continue
		}
		if m := mdHeading.FindStringSubmatch(src[i]); m != nil {
			out = append(out, heading{level: len(m[1]), line: i})
		}
	}
	return out
}

// skipFence returns the index of the line closing the fence opened at i, or
// the source's last line if it never closes — the same rule fenced() uses,
// so a heading search and a render agree about where a fence ends.
func skipFence(src []string, i int, mark string) int {
	for i++; i < len(src); i++ {
		if t := strings.TrimSpace(src[i]); strings.HasPrefix(t, mark) && strings.Trim(t, mark[:1]) == "" {
			return i
		}
	}
	return len(src) - 1
}

// headingEnds computes, for each heading, the source line its section runs
// to (exclusive): the next heading at the same level or shallower, or the
// end of the document. A heading nested inside another's section always
// ends at or before its parent's own end, since "same level or shallower"
// is a stricter test the deeper a heading sits — which is what lets foldBody
// walk the list once, flat, rather than build a tree.
func headingEnds(hs []heading, total int) []int {
	ends := make([]int, len(hs))
	for i, h := range hs {
		end := total
		for j := i + 1; j < len(hs); j++ {
			if hs[j].level <= h.level {
				end = hs[j].line
				break
			}
		}
		ends[i] = end
	}
	return ends
}

// foldBody renders a ticket body the way ticketLines wants it: as lines
// ready for the pane, a per-line owner saying which heading (by position in
// document order, -1 for none) that line belongs to, and how many headings
// the body has at all. folded says which of those headings are collapsed —
// a collapsed heading still shows its own line, with a count of what it is
// hiding, but nothing inside its section renders at all, including a nested
// heading, which is how one fold covers its whole subtree.
func foldBody(body string, width int, folded map[int]bool) (lines []string, owner []int, headingCount int) {
	src := strings.Split(body, "\n")
	hs := headings(src)
	if len(hs) == 0 {
		lines = renderMarkdown(body, width)
		return lines, negOwner(len(lines)), 0
	}
	ends := headingEnds(hs, len(src))

	emit := func(text string, ownerIdx int) {
		for _, l := range renderMarkdown(text, width) {
			lines = append(lines, l)
			owner = append(owner, ownerIdx)
		}
	}
	// blank inserts the air renderMarkdown's own heading case puts above a
	// heading (markdown.go's r.blank()) — lost here because each heading is
	// rendered by its own isolated call, with no out slice behind it to test.
	blank := func() {
		if n := len(lines); n > 0 && lines[n-1] != "" {
			lines = append(lines, "")
			owner = append(owner, -1)
		}
	}

	pos := 0
	var stack []int // open ancestor headings, innermost last
	for i, h := range hs {
		if h.line < pos {
			continue // inside a folded ancestor's section — invisible
		}
		// The text between pos and this heading belongs to whichever
		// section was still open when it was written — the current top of
		// stack — which has to be read before popping anything: popping
		// first would credit this text to an ancestor two levels up, since
		// the section it actually trails is exactly the one about to close.
		parent := -1
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		emit(strings.Join(src[pos:h.line], "\n"), parent)
		for len(stack) > 0 && ends[stack[len(stack)-1]] <= h.line {
			stack = stack[:len(stack)-1]
		}
		blank()
		if folded[i] {
			hidden := len(renderMarkdown(strings.Join(src[h.line+1:ends[i]], "\n"), width))
			// The same leading-blank loss the unfolded branch works around
			// below applies here too: renderMarkdown's fresh call drops a
			// blank line immediately at its own start, so the count would
			// otherwise read one line short of what unfolding actually
			// reveals. The two cases are mutually exclusive, not additive:
			// unfolding only ever inserts one blank line before whatever
			// comes next, whether the source already wrote it (case one) or
			// a subheading buys its own air the way every heading does
			// (case two, blank()'s unconditional rule in markdown.go) — a
			// tight subheading right after this one, with no source blank
			// between them, still gets that air once unfolded, but the
			// isolated call above never draws it: its own out starts empty,
			// so the subheading's blank() call is the no-op start-of-output
			// case rather than the between-blocks case it would be in the
			// composed render.
			switch {
			case h.line+1 < ends[i] && strings.TrimSpace(src[h.line+1]) == "":
				hidden++
			case h.line+1 < ends[i] && mdHeading.MatchString(src[h.line+1]):
				hidden++
			}
			suffix := styleFaint.Render(fmt.Sprintf(" ⋯ %d hidden", hidden))
			// Budgeted before wrapping, not appended after: appending to an
			// already-wrapped line can push it past width, and panelBox
			// truncates rather than rewraps — the count would vanish and
			// take the tail of the heading's own text with it.
			headingWidth := width
			if hidden > 0 {
				headingWidth = max(1, width-lipgloss.Width(suffix))
			}
			headingLines := renderMarkdown(src[h.line], headingWidth)
			if n := len(headingLines); n > 0 && hidden > 0 {
				headingLines[n-1] += suffix
			}
			for _, l := range headingLines {
				lines = append(lines, l)
				owner = append(owner, i)
			}
			pos = ends[i]
			continue
		}
		for _, l := range renderMarkdown(src[h.line], width) {
			lines = append(lines, l)
			owner = append(owner, i)
		}
		// The blank line renderMarkdown would draw between this heading and
		// its body (r.blank(), called as the continuous parser reaches that
		// blank source line) is otherwise lost: the body is a fresh,
		// independent renderMarkdown call whose own out starts empty, so
		// its leading blank line no-ops exactly the way blank() above
		// exists to work around. Unlike blank(), this one is conditional on
		// the source actually having one — a heading tight against its
		// body ("# A\nbody", no blank) draws no air here either, matching
		// the continuous render exactly.
		if h.line+1 < len(src) && strings.TrimSpace(src[h.line+1]) == "" {
			lines = append(lines, "")
			owner = append(owner, -1)
		}
		pos = h.line + 1
		stack = append(stack, i)
	}
	parent := -1
	if len(stack) > 0 {
		parent = stack[len(stack)-1]
	}
	emit(strings.Join(src[pos:], "\n"), parent)
	return lines, owner, len(hs)
}

// negOwner is n lines' worth of "no heading owns this" — the whole of a
// document with nothing to fold, or a stretch (a comment, the pane's own
// header lines) that folding was never asked to reach.
func negOwner(n int) []int {
	o := make([]int, n)
	for i := range o {
		o[i] = -1
	}
	return o
}

// foldable reports whether z/Z do anything right now: the pane must be
// showing the inbox's own ticket detail — not the work panel, not a log,
// not the ? overlay, not a modal — and that ticket's body must have at
// least one heading. foldCount is set by refreshMain alongside foldOwner,
// so this never re-parses the body just to answer.
func (m *model) foldable() bool {
	return m.mainOpen() && !m.modal() && !m.helpOn && m.focus == panelAttention && m.foldCount > 0
}

// toggleFold flips the fold on the heading nearest the top of the viewport,
// at or after it — the nearest thing this pane has to "the section under
// the cursor," since the prose it shows has no selectable line of its own,
// only a scroll position. Scanning forward rather than reading that one
// line is what makes it work on a ticket short enough to need no
// scrolling: the viewport's top is pinned to the pane's own header lines
// then, which own no heading themselves, and the first one reading would
// reach is the answer, not a dead key.
//
// A forward scan alone goes dead once the viewport is scrolled past the
// last heading's section — into the comments, or the pane's own trailing
// lines, all owned by nobody — even though foldable() is still true and
// still advertising the key. Falling back to the nearest heading behind the
// cursor covers that case: there is always one, since foldable() already
// means the document has at least one heading.
//
// Returns the heading to re-anchor the viewport to, or -1 if the caller
// should leave the viewport alone. These are not the same thing as "which
// heading got toggled": re-anchoring only matters when the cursor's own
// position already sat inside that heading's section — that is the one
// case where folding shifts everything the viewport was showing and leaves
// it pointed at whatever scrolled up to fill the gap. Landing on the
// section's own gap lines counts as inside it too: the blank fold.go's
// blank() puts before a heading, or the one between a heading and its own
// body, are unowned (-1) but still fall inside the section on either side
// of them, closer to one heading than any other — a nearest-owned-neighbor
// check on both sides of top, forward and backward, is what "inside"
// actually means when most of a section's own lines carry its owner
// directly and only these seams do not.
//
// The pane's header lines and any run of comments past the last heading
// are a different kind of unowned: nothing behind them (searching
// backward) is ever owned by the heading found ahead, because there is no
// heading behind them at all (top of document) or the forward scan found
// nothing (past every section, the fallback below). Both read as "not
// inside," which is what leaves the viewport alone in exactly the two
// cases the round-4 review found broken: jumping to the very top on the
// first fold of a ticket still parked at its opening scroll position, and
// yanking the pane away from the comments the backward fallback exists to
// leave undisturbed.
func (m *model) toggleFold() int {
	it := m.selectedAttention()
	if it == nil {
		return -1
	}
	d := m.details[it.TicketID]
	if d == nil || len(m.foldOwner) == 0 {
		return -1
	}
	top := clampIndex(m.vp.YOffset, len(m.foldOwner))
	ahead := -1
	for i := top; i < len(m.foldOwner); i++ {
		if m.foldOwner[i] >= 0 {
			ahead = m.foldOwner[i]
			break
		}
	}
	behind := -1
	for i := top; i >= 0; i-- {
		if m.foldOwner[i] >= 0 {
			behind = m.foldOwner[i]
			break
		}
	}
	idx, direct := ahead, ahead >= 0 && ahead == behind
	if idx < 0 {
		idx = behind // the fallback: nothing ahead, so act on whatever is behind
	}
	if idx < 0 {
		return -1
	}
	if d.folded == nil {
		d.folded = make(map[int]bool)
	}
	d.folded[idx] = !d.folded[idx]
	if !direct {
		return -1
	}
	return idx
}

// reanchorFold points the viewport at the heading identified by idx, after
// a refreshMain has rebuilt foldOwner around a fold that just changed —
// otherwise the pane would show whatever scrolled up to fill the gap the
// fold left, with the fold itself off-screen and a second press landing on
// the wrong section entirely (see toggleFold). A no-op for idx < 0, the
// value toggleFold returns whenever re-anchoring is not called for.
func (m *model) reanchorFold(idx int) {
	if idx < 0 {
		return
	}
	for i, o := range m.foldOwner {
		if o == idx {
			m.vp.SetYOffset(i)
			return
		}
	}
}

// foldAll swaps the whole document between its outline — every heading
// collapsed — and the full text. One key serves both directions: pressed
// again once everything is already folded, it finds that and opens
// everything back up instead.
func (m *model) foldAll() {
	it := m.selectedAttention()
	if it == nil {
		return
	}
	d := m.details[it.TicketID]
	if d == nil {
		return
	}
	// TrimSpace to match foldBody's own parse (via ticketLines): a body
	// that is all leading blank lines before its first heading must count
	// the same headings either way, or Z's hint and Z's action disagree.
	hs := headings(strings.Split(strings.TrimSpace(d.body), "\n"))
	if len(hs) == 0 {
		return
	}
	allFolded := true
	for i := range hs {
		if !d.folded[i] {
			allFolded = false
			break
		}
	}
	if allFolded {
		d.folded = nil
		return
	}
	d.folded = make(map[int]bool, len(hs))
	for i := range hs {
		d.folded[i] = true
	}
}
