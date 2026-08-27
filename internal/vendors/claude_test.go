package vendors

import "testing"

func TestClaudeCommandDefault(t *testing.T) {
	got := claude{}.Command(Options{})
	want := "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose"
	if got != want {
		t.Errorf("Command(Options{}) = %q, want %q", got, want)
	}
}

func TestClaudeCommandModelAndEffortOnlyWhenSet(t *testing.T) {
	got := claude{}.Command(Options{Model: "sonnet", Effort: "high"})
	want := "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose" +
		" --model 'sonnet' --effort 'high'"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}

	got = claude{}.Command(Options{Effort: "high"})
	want = "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose --effort 'high'"
	if got != want {
		t.Errorf("Command with only Effort = %q, want %q", got, want)
	}
}

// A model alias can carry glob characters ("sonnet[1m]"), and the command
// this becomes is expanded and run by a shell — unquoted, that shell would
// try to glob it.
func TestClaudeCommandQuotesMetacharactersInModel(t *testing.T) {
	got := claude{}.Command(Options{Model: "sonnet[1m]"})
	want := "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose --model 'sonnet[1m]'"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

// Args is appended last and verbatim, never quoted: it is shell text, not a
// value, and an operator relies on that to override an earlier flag on a
// last-wins CLI.
func TestClaudeCommandArgsAppendedLastAndVerbatim(t *testing.T) {
	got := claude{}.Command(Options{Model: "sonnet", Args: "--allowedTools Read --model opus"})
	want := "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose" +
		" --model 'sonnet' --allowedTools Read --model opus"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestClaudeResume(t *testing.T) {
	got := claude{}.Resume(Options{Model: "sonnet"})
	want := "cd {{workdir}} && claude --resume {{session}}"
	if got != want {
		t.Errorf("Resume = %q, want %q", got, want)
	}
}
