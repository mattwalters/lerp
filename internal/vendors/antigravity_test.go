package vendors

import (
	"strings"
	"testing"
)

func TestAntigravityCommandDefault(t *testing.T) {
	got := antigravity{}.Command(Options{})
	want := "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h"
	if got != want {
		t.Errorf("Command(Options{}) = %q, want %q", got, want)
	}
}

func TestAntigravityCommandModelAndEffortOnlyWhenSet(t *testing.T) {
	got := antigravity{}.Command(Options{Model: "gemini-3-pro", Effort: "high"})
	want := "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h" +
		" --model 'gemini-3-pro' --effort 'high'"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}

	got = antigravity{}.Command(Options{Effort: "high"})
	want = "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h --effort 'high'"
	if got != want {
		t.Errorf("Command with only Effort = %q, want %q", got, want)
	}
}

func TestAntigravityCommandQuotesMetacharactersInModel(t *testing.T) {
	got := antigravity{}.Command(Options{Model: "gemini[3]"})
	want := "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h --model 'gemini[3]'"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

// Args is appended last and verbatim, never quoted — the same rule claude's
// adapter follows, so an operator relies on it to override an earlier flag on
// agy's last-wins parsing.
func TestAntigravityCommandArgsAppendedLastAndVerbatim(t *testing.T) {
	got := antigravity{}.Command(Options{Model: "gemini-3-pro", Args: "--sandbox --print-timeout 1h0m0s"})
	want := "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h" +
		" --model 'gemini-3-pro' --sandbox --print-timeout 1h0m0s"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

// No permission-skipping flag anywhere in a generated command, under any
// combination of options: like every vendor lerp ships, that grant reaches a
// command only by being written into a runner's own checked-in args.
func TestAntigravityCommandNeverSkipsPermissions(t *testing.T) {
	got := antigravity{}.Command(Options{Model: "m", Effort: "high", Args: "--sandbox"})
	if want := "--dangerously-skip-permissions"; strings.Contains(got, want) {
		t.Errorf("Command = %q, contains %q", got, want)
	}
}

func TestAntigravityResume(t *testing.T) {
	got := antigravity{}.Resume(Options{})
	want := "cd {{workdir}} && agy --conversation {{session}}"
	if got != want {
		t.Errorf("Resume = %q, want %q", got, want)
	}
}

func TestAntigravitySessionReadsInit(t *testing.T) {
	line := `{"event":"init","conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98","init":{"cwd":"/tmp"}}`
	got, ok := antigravity{}.Session(line)
	if !ok || got != "ffd2f49a-85bf-45ab-bfad-80aed96a9b98" {
		t.Errorf("Session = (%q, %v), want the conversation id and true", got, ok)
	}
}

func TestAntigravitySessionIgnoresOtherLines(t *testing.T) {
	lines := []string{
		`{"event":"step_update","step_update":{"conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98"}}`,
		`{"event":"result","result":{"conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98"}}`,
		`{"event":"init"}`,
		"not json",
		"",
	}
	for _, line := range lines {
		if got, ok := (antigravity{}).Session(line); ok {
			t.Errorf("Session(%q) = (%q, true), want it dropped", line, got)
		}
	}
}

func TestAntigravityImplementsSessionNamer(t *testing.T) {
	var _ SessionNamer = antigravity{}
}

func TestAntigravityRegisteredByName(t *testing.T) {
	a, ok := Lookup("antigravity")
	if !ok {
		t.Fatal(`Lookup("antigravity") = false, want true`)
	}
	if _, ok := a.(SessionNamer); !ok {
		t.Error("the registered antigravity adapter does not implement SessionNamer")
	}
}
