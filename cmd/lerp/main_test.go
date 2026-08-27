package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The operator's surface says concurrency, never lane. Lane stays the
// internal noun and the evidence record's field; two names for one number
// is exactly the clutter this project refuses.
func TestUsageDoesNotSayLane(t *testing.T) {
	if strings.Contains(strings.ToLower(usage), "lane") {
		t.Fatalf("usage names a lane:\n%s", usage)
	}
}

// Both new subcommands are discoverable from -h, not just from reading the
// source.
func TestUsageListsLoginAndLogout(t *testing.T) {
	for _, want := range []string{"lerp login", "lerp logout"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage does not mention %q:\n%s", want, usage)
		}
	}
}

// cliPage is the manual's reference page for the command line, which opens
// with this usage text verbatim.
const cliPage = "docs/content/docs/cli.md"

// The manual quotes the usage block as the whole of lerp's surface, and a
// quoted string with nothing holding it to its source goes stale on the
// first flag, with a green gate — the same reasoning that pins the
// skipped-hop note to the page that quotes it, and lerp.example.toml to the
// stock config. Add a subcommand or move -concurrency's default and this
// fails here rather than on a reader's screen.
//
// The block is found by its own first line rather than by position, so
// rewriting the prose around it costs nothing.
func TestTheManualQuotesTheUsage(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", cliPage))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), strings.TrimSuffix(usage, "\n")) {
		t.Errorf("%s does not quote main.go's usage. It should read:\n\n%s\n"+
			"main.go is the source. Change the usage there, then update the\n"+
			"block that opens %s.", cliPage, usage, cliPage)
	}
}

// A startup warning that scrolls past unread is the same as no warning: the
// TUI takes the alternate screen a moment later. So announce holds the run
// until the operator acknowledges it.
func TestAnnounceWaitsForTheOperator(t *testing.T) {
	var out strings.Builder
	// Type-ahead before the acknowledgement must not release the gate, and
	// the two keystrokes the TUI wants after it must survive.
	in := strings.NewReader("abc\n2j")
	announce(&out, in, []string{"team LERP: trouble", "fix: do the thing"}, true)
	for _, want := range []string{"team LERP: trouble", "fix: do the thing", "press enter"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q missing %q", out.String(), want)
		}
	}
	rest, err := io.ReadAll(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "2j" {
		t.Errorf("announce consumed past the newline, leaving %q for the TUI", rest)
	}
}

func TestAnnounceIsSilentWithoutWarnings(t *testing.T) {
	var out strings.Builder
	// Nothing is read either: a clean startup must not eat the first
	// keystroke the operator aims at the board.
	in := strings.NewReader("2")
	announce(&out, in, nil, true)
	if out.String() != "" {
		t.Errorf("output = %q, want nothing", out.String())
	}
	if in.Len() != 1 {
		t.Errorf("announce read %d bytes, want none", 1-in.Len())
	}
}

// An unreadable stdin is not a reason to refuse: the warning is on screen,
// and refusing here would turn a warning into the refusal it is not.
func TestAnnounceStartsAnywayWhenStdinIsClosed(t *testing.T) {
	var out strings.Builder
	announce(&out, strings.NewReader(""), []string{"team LERP: trouble"}, true)
	if !strings.Contains(out.String(), "team LERP: trouble") {
		t.Errorf("output %q missing the warning", out.String())
	}
}

// A terminal that is not translating carriage returns delivers enter as \r.
// A gate that only knows \n would swallow it and never open.
func TestAnnounceAcceptsACarriageReturn(t *testing.T) {
	var out strings.Builder
	in := strings.NewReader("\r2j")
	announce(&out, in, []string{"team LERP: trouble"}, true)
	rest, err := io.ReadAll(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "2j" {
		t.Errorf("a carriage return left %q for the TUI, want %q", rest, "2j")
	}
}

// Warnings redirected away with `lerp 2>/dev/null` reach nobody, so there is
// nothing for a keystroke to acknowledge — waiting for one would hang the
// launch behind a blank screen.
func TestAnnounceDoesNotWaitWhenNobodyCanSeeIt(t *testing.T) {
	var out strings.Builder
	in := strings.NewReader("2j")
	announce(&out, in, []string{"team LERP: trouble"}, false)
	if strings.Contains(out.String(), "press enter") {
		t.Errorf("output %q prompts for an answer nobody was asked for", out.String())
	}
	if in.Len() != 2 {
		t.Errorf("announce read %d bytes, want none", 2-in.Len())
	}
}
