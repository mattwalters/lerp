package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Done-when: a ticket reads as a ticket — the markers Linear stores are
// structure on the screen, not literal text in the middle of a sentence.
func TestMarkdownRendersStructureNotSource(t *testing.T) {
	body := strings.Join([]string{
		"## Plan",
		"",
		"Read **SCOPE.md** first, then `internal/tui/model.go`.",
		"",
		"* one thing",
		"* another thing",
		"",
		"1. first step",
		"2. second step",
	}, "\n")

	got := strings.Join(renderMarkdown(body, 60), "\n")
	for _, want := range []string{
		"Plan",
		"Read SCOPE.md first, then internal/tui/model.go.",
		"• one thing",
		"• another thing",
		"1. first step",
		"2. second step",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered body is missing %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"##", "**", "`", "* one"} {
		if strings.Contains(got, bad) {
			t.Fatalf("rendered body still shows the source marker %q:\n%s", bad, got)
		}
	}
}

// A heading is findable: it gets a blank line above it even when the source
// ran the sections together, and the pane never opens on an empty line.
func TestMarkdownHeadingsGetAir(t *testing.T) {
	lines := renderMarkdown("# Title\nprose\n## Next\nmore prose", 40)
	want := []string{"Title", "prose", "", "Next", "more prose"}
	if len(lines) != len(want) {
		t.Fatalf("rendered %d lines, want %d:\n%q", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d is %q, want %q:\n%q", i, lines[i], want[i], lines)
		}
	}
}

// Done-when: a plan hard-wrapped at some other width reads as prose here.
// Rendering the source's own line breaks is what left the ragged right edge
// that made the pane look like a file.
func TestMarkdownRewrapsParagraphs(t *testing.T) {
	body := "a plan written\nat some narrow width\nby whoever wrote it\nlast"
	lines := renderMarkdown(body, 40)
	if len(lines) != 2 {
		t.Fatalf("a re-wrapped paragraph should fill the width, got %d lines:\n%q", len(lines), lines)
	}
	if joined := strings.Join(lines, " "); joined != "a plan written at some narrow width by whoever wrote it last" {
		t.Fatalf("the paragraph lost or gained words: %q", joined)
	}
}

// A code block is the one thing not re-wrapped: its lines are what was
// written, indentation and all, behind a gutter that marks them as code.
func TestMarkdownCodeBlockKeepsItsLines(t *testing.T) {
	body := "before\n\n```go\nfunc main() {\n\tif ok {\n\t\treturn\n\t}\n}\n```\n\nafter"
	lines := renderMarkdown(body, 40)
	var code []string
	for _, line := range lines {
		if strings.Contains(line, "│ ") {
			code = append(code, strings.SplitN(line, "│ ", 2)[1])
		}
	}
	want := []string{"func main() {", "\tif ok {", "\t\treturn", "\t}", "}"}
	if len(code) != len(want) {
		t.Fatalf("the block rendered %d lines, want %d:\n%q", len(code), len(want), lines)
	}
	for i := range want {
		if code[i] != want[i] {
			t.Fatalf("code line %d is %q, want %q", i, code[i], want[i])
		}
	}
	if strings.Contains(strings.Join(lines, "\n"), "```") {
		t.Fatalf("the fence itself is on the pane:\n%q", lines)
	}
}

// An unclosed fence is still a code block — the rest of the body is what the
// source says it is, rather than a renderer's guess.
func TestMarkdownUnclosedFenceRunsToTheEnd(t *testing.T) {
	lines := renderMarkdown("```\nstill code\nand more", 40)
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "still code") || !strings.HasSuffix(lines[1], "and more") {
		t.Fatalf("an unclosed fence did not run to the end:\n%q", lines)
	}
}

// Prose is not markup: asterisks that multiply, underscores inside a name,
// and a marker that never closes stay the text the author typed.
func TestMarkdownLeavesProseAlone(t *testing.T) {
	body := "2 * 3 = 6, snake_case_name stays, and a **stray opener"
	got := strings.Join(renderMarkdown(body, 60), " ")
	if got != body {
		t.Fatalf("prose was rewritten:\n got %q\nwant %q", got, body)
	}
}

// A link is followed by where it goes: nothing in this pane is clickable, so
// a link that hid its target would be prose the operator cannot follow.
func TestMarkdownLinkShowsItsTarget(t *testing.T) {
	got := strings.Join(renderMarkdown("see [the PR](https://github.com/x/y/pull/1) for it", 70), " ")
	if want := "see the PR (https://github.com/x/y/pull/1) for it"; got != want {
		t.Fatalf("link rendered as %q, want %q", got, want)
	}
}

// A quoted stretch reads as the background it is: one faint paragraph behind
// a gutter, however many lines the source spent on it.
func TestMarkdownQuoteIsOneParagraph(t *testing.T) {
	lines := renderMarkdown("> context one\n> context two\n\nthe reply", 60)
	if len(lines) != 3 {
		t.Fatalf("rendered %d lines, want 3:\n%q", len(lines), lines)
	}
	if !strings.HasSuffix(lines[0], "context one context two") {
		t.Fatalf("the quote is not one paragraph: %q", lines[0])
	}
	if lines[2] != "the reply" {
		t.Fatalf("the reply is not on its own: %q", lines[2])
	}
}

// Every line fits the pane it was rendered for — a wrapped bullet hangs
// under its text, and a URL with no space in it is broken rather than
// allowed to push the panel out of shape.
func TestMarkdownFitsTheWidth(t *testing.T) {
	body := strings.Join([]string{
		"# a heading long enough that it has to wrap somewhere sensible",
		"",
		"* a bullet whose text runs well past the width it was given here",
		"    * a nested bullet that also runs past the width it was given",
		"",
		"https://example.com/" + strings.Repeat("segment/", 12),
		"",
		"> a quoted line that also runs well past the width it was given",
	}, "\n")

	const width = 30
	lines := renderMarkdown(body, width)
	for i, line := range lines {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line %d is %d cells wide in a %d-column pane: %q", i, got, width, line)
		}
		if strings.Contains(line, "\n") {
			t.Fatalf("line %d carries a newline of its own, which would break the height count: %q", i, line)
		}
	}
	// The bullet's second line hangs under its text, not under the marker.
	for i, line := range lines {
		if strings.HasPrefix(line, "• ") && i+1 < len(lines) && !strings.HasPrefix(lines[i+1], "  ") {
			t.Fatalf("a wrapped bullet did not hang under its text:\n%q", lines)
		}
	}
}

// The styles are the board's own: code borrows the faint ramp the chrome
// uses, and emphasis is an attribute rather than a color, so a rendered
// ticket looks like lerp rather than like a markdown viewer.
func TestMarkdownStylesSpans(t *testing.T) {
	lines := renderMarkdown("a `path/to/file` here", 40)
	if len(lines) != 1 {
		t.Fatalf("rendered %d lines, want 1: %q", len(lines), lines)
	}
	if want := "a " + styleCode.Render("path/to/file") + " here"; lines[0] != want {
		t.Fatalf("inline code is not styled as code:\n got %q\nwant %q", lines[0], want)
	}
	// A styled span is rendered whole on the line it sits on: lipgloss closes
	// what it opens, so nothing bleeds through the panel's border.
	if strings.Contains(styleCode.Render("x"), "\x1b[") && !strings.HasSuffix(styleCode.Render("x"), sgrReset) {
		t.Fatalf("a styled span does not close itself: %q", styleCode.Render("x"))
	}
}

// Nothing on the pane is a partial render of something: emphasis inside a
// heading, code inside a bullet, and an empty body all come out whole.
func TestMarkdownNests(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"## the **plan** for `lerp`", "the plan for lerp"},
		{"* run `make check` **before** pushing", "• run make check before pushing"},
		{"", ""},
		{"   ", ""},
	} {
		got := strings.Join(renderMarkdown(tc.body, 60), "\n")
		if got != tc.want {
			t.Fatalf("rendering %q gave %q, want %q", tc.body, got, tc.want)
		}
	}
}
