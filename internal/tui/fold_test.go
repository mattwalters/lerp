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
// is folded — the default is the same full read as before this ticket.
func TestFoldNothingFoldedShowsEverything(t *testing.T) {
	body := "# A\n\nbody of A\n\n## B\n\nbody of B"
	lines, owner, count := foldBody(body, 40, nil)
	got := strings.Join(lines, "\n")
	for _, want := range []string{"A", "body of A", "B", "body of B"} {
		if !strings.Contains(got, want) {
			t.Fatalf("nothing should be hidden yet, missing %q:\n%s", want, got)
		}
	}
	if count != 2 {
		t.Fatalf("found %d headings, want 2", count)
	}
	if len(owner) != len(lines) {
		t.Fatalf("owner has %d entries for %d lines", len(owner), len(lines))
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
