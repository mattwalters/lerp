package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Done-when: a document with no headings at all is exactly what
// renderMarkdown already draws — folding has nothing to do and gets out of
// the way rather than reshaping prose that was never sectioned.
func TestFoldWithNoHeadingsIsPlainMarkdown(t *testing.T) {
	body := "just a paragraph\n\nand another one"
	lines, owner, count := foldBody(body, 40, nil)
	if count != 0 {
		t.Fatalf("a headingless body reports %d headings, want 0", count)
	}
	want := strings.Join(renderMarkdown(body, 40), "\n")
	if got := strings.Join(lines, "\n"); got != want {
		t.Fatalf("folding changed a headingless body:\n got %q\nwant %q", got, want)
	}
	for i, o := range owner {
		if o != -1 {
			t.Fatalf("line %d is owned by heading %d, want none: %q", i, o, lines[i])
		}
	}
}

// A heading inside a fence is code, not structure — the same rule fenced()
// already holds renderMarkdown to (markdown_test.go's own fence tests), and
// headings() has to honor it too or a code sample would fold the plan
// around it.
func TestFoldIgnoresHeadingsInFences(t *testing.T) {
	body := "# Real Heading\n\n```\n# not a heading\n```\n\nafter"
	_, _, count := foldBody(body, 40, nil)
	if count != 1 {
		t.Fatalf("found %d headings, want 1 (the fence's # must not count)", count)
	}
}

// Done-when: a long plan opens foldable. Nothing is hidden until something
// is folded — the default is the exact same read renderMarkdown already
// gave, not just one that contains the same words. Composing a document
// section by section must not quietly drop the blank line between a
// heading and its body: that line is the first line of the next chunk's own
// independent renderMarkdown call, whose output starts empty, so the same
// suppression that skips a redundant blank before an empty pane would also
// skip a real one there if nothing corrected for it.
func TestFoldNothingFoldedShowsEverything(t *testing.T) {
	body := "# A\n\nbody of A\n\n## B\n\nbody of B"
	lines, owner, count := foldBody(body, 40, nil)
	if want := strings.Join(renderMarkdown(body, 40), "\n"); strings.Join(lines, "\n") != want {
		t.Fatalf("folding nothing should render identically to renderMarkdown:\n got  %q\nwant %q",
			strings.Join(lines, "\n"), want)
	}
	if count != 2 {
		t.Fatalf("found %d headings, want 2", count)
	}
	if len(owner) != len(lines) {
		t.Fatalf("owner has %d entries for %d lines", len(owner), len(lines))
	}
}

// The text between two flat (unnested) headings belongs to the one it
// trails, not to whichever heading happens to still be on the stack after
// popping for the *next* one — popping too early credits a section's own
// body to its predecessor's parent (or to no one), and toggleFold then
// folds the wrong section for any cursor sitting in that body.
func TestFoldOwnerOfASectionsOwnBodyIsThatSection(t *testing.T) {
	body := "# One\n\nfirst body\n\n# Two\n\nsecond body"
	lines, owner, _ := foldBody(body, 40, nil)
	firstBody, secondBody := -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "first body"):
			firstBody = i
		case strings.Contains(l, "second body"):
			secondBody = i
		}
	}
	if firstBody == -1 || secondBody == -1 {
		t.Fatalf("could not find both bodies in %q", lines)
	}
	if owner[firstBody] != 0 {
		t.Fatalf("\"first body\" is owned by heading %d, want 0 (One)", owner[firstBody])
	}
	if owner[secondBody] != 1 {
		t.Fatalf("\"second body\" is owned by heading %d, want 1 (Two)", owner[secondBody])
	}

	// The failure mode this guards against: folding whatever the cursor's
	// scan-forward lands on from inside One's body must fold One, not Two.
	lines, _, _ = foldBody(body, 40, map[int]bool{0: true})
	got := strings.Join(lines, "\n")
	if strings.Contains(got, "first body") {
		t.Fatalf("folding heading 0 should hide \"first body\":\n%s", got)
	}
	if !strings.Contains(got, "second body") {
		t.Fatalf("folding heading 0 must not touch Two's section:\n%s", got)
	}
}

// Folding a heading hides its body and reports how much it hid, but leaves
// the heading itself — and anything before or after its section — on
// screen. The count is the collapsed content only, never the heading's own
// line.
func TestFoldHidesOnlyItsOwnSection(t *testing.T) {
	body := "before\n\n# A\n\nbody of A\n\n# C\n\nbody of C"
	hs := headings(strings.Split(body, "\n"))
	if len(hs) != 2 {
		t.Fatalf("expected 2 headings, got %d", len(hs))
	}
	lines, _, _ := foldBody(body, 40, map[int]bool{0: true})
	got := strings.Join(lines, "\n")
	for _, want := range []string{"before", "A", "C", "body of C"} {
		if !strings.Contains(got, want) {
			t.Fatalf("folding A should not touch %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "body of A") {
		t.Fatalf("A's body is still showing while A is folded:\n%s", got)
	}
	if !strings.Contains(got, "hidden") {
		t.Fatalf("a folded heading should say how much it hid:\n%s", got)
	}
}

// Nested heading levels fold as one subtree: closing a level-1 section hides
// every level-2 section under it, and reopening it does not implicitly
// reopen those — vim's fold model, where a fold remembers its own state.
func TestFoldNestingHidesTheWholeSubtree(t *testing.T) {
	body := "# Top\n\ntop text\n\n## Child\n\nchild text\n\n# Sibling\n\nsibling text"
	hs := headings(strings.Split(body, "\n"))
	if len(hs) != 3 {
		t.Fatalf("expected 3 headings (Top, Child, Sibling), got %d", len(hs))
	}
	// Fold Top (index 0): Child is nested inside it and must vanish too,
	// not just be left dangling with no parent drawn.
	lines, owner, _ := foldBody(body, 40, map[int]bool{0: true})
	got := strings.Join(lines, "\n")
	if strings.Contains(got, "top text") || strings.Contains(got, "Child") || strings.Contains(got, "child text") {
		t.Fatalf("folding Top left part of its subtree visible:\n%s", got)
	}
	if !strings.Contains(got, "Sibling") || !strings.Contains(got, "sibling text") {
		t.Fatalf("folding Top hid its sibling too:\n%s", got)
	}
	for i, o := range owner {
		if o == 1 {
			t.Fatalf("line %d is owned by the hidden Child heading, which drew nothing: %q", i, lines[i])
		}
	}
}

// Every visible line's owner is the heading whose section it is part of —
// the mapping toggleFold reads to know which heading a viewport position
// belongs to.
func TestFoldOwnerTracksSection(t *testing.T) {
	body := "# A\n\ntext under A"
	lines, owner, _ := foldBody(body, 40, nil)
	var headingLine, textLine = -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "A") && headingLine == -1:
			headingLine = i
		case strings.Contains(l, "text under A"):
			textLine = i
		}
	}
	if headingLine == -1 || textLine == -1 {
		t.Fatalf("could not find both lines in %q", lines)
	}
	if owner[headingLine] != 0 || owner[textLine] != 0 {
		t.Fatalf("heading line owner=%d, text line owner=%d, want both 0", owner[headingLine], owner[textLine])
	}
}

// Every line still fits the pane it was rendered for, folded or not — the
// per-section calls into renderMarkdown carry the same width contract as a
// single whole-document call.
func TestFoldFitsTheWidth(t *testing.T) {
	body := "# A heading long enough that it might have to wrap somewhere\n\n" +
		"a paragraph long enough that it definitely wraps across more than one line of a narrow pane"
	const width = 24
	lines, _, _ := foldBody(body, width, nil)
	for i, l := range lines {
		if w := lipgloss.Width(l); w > width {
			t.Fatalf("line %d is %d cells wide in a %d-column pane: %q", i, w, width, l)
		}
	}
}

// The "⋯ N hidden" suffix has to fit inside the same width as everything
// else — appended after a heading is already wrapped to the full width, it
// would push the last line over, and panelBox truncates rather than
// rewraps: the count would vanish and take the tail of the heading's own
// text with it. A heading long enough to fill the pane on its own is the
// case that forces the point.
func TestFoldFitsTheWidthWhenFolded(t *testing.T) {
	body := "# A heading that on its own runs the full width of a narrow pane\n\n" +
		strings.Repeat("hidden content\n", 5)
	const width = 30
	lines, _, _ := foldBody(body, width, map[int]bool{0: true})
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "hidden") {
		t.Fatalf("a folded heading should say how much it hid:\n%s", got)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > width {
			t.Fatalf("line %d is %d cells wide in a %d-column pane: %q", i, w, width, l)
		}
	}
}
