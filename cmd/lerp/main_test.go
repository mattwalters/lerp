package main

import (
	"io"
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

// A startup warning that scrolls past unread is the same as no warning: the
// TUI takes the alternate screen a moment later. So announce holds the run
// until the operator acknowledges it.
func TestAnnounceWaitsForTheOperator(t *testing.T) {
	var out strings.Builder
	// Two keystrokes the TUI wants follow the acknowledgement; announce must
	// leave them alone.
	in := strings.NewReader("\n2j")
	if err := announce(&out, in, []string{"team LERP: trouble", "fix: do the thing"}); err != nil {
		t.Fatalf("announce = %v, want nil", err)
	}
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
	if err := announce(&out, in, nil); err != nil {
		t.Fatalf("announce = %v, want nil", err)
	}
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
	if err := announce(&out, strings.NewReader(""), []string{"team LERP: trouble"}); err != nil {
		t.Fatalf("announce = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "team LERP: trouble") {
		t.Errorf("output %q missing the warning", out.String())
	}
}
