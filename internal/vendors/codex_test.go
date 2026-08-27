package vendors

import "testing"

func TestCodexCommandDefault(t *testing.T) {
	got := codex{}.Command(Options{})
	want := "codex exec --json -- {{prompt}}"
	if got != want {
		t.Errorf("Command(Options{}) = %q, want %q", got, want)
	}
}

func TestCodexCommandModelAndEffortOnlyWhenSet(t *testing.T) {
	got := codex{}.Command(Options{Model: "o3", Effort: "high"})
	want := "codex exec --json --model 'o3' -c 'model_reasoning_effort=high' -- {{prompt}}"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}

	got = codex{}.Command(Options{Effort: "high"})
	want = "codex exec --json -c 'model_reasoning_effort=high' -- {{prompt}}"
	if got != want {
		t.Errorf("Command with only Effort = %q, want %q", got, want)
	}
}

// A model alias can carry shell metacharacters, and the command this becomes
// is expanded and run by a shell — unquoted, that shell would try to
// interpret them.
func TestCodexCommandQuotesMetacharactersInModel(t *testing.T) {
	got := codex{}.Command(Options{Model: "o3[preview]"})
	want := "codex exec --json --model 'o3[preview]' -- {{prompt}}"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

// Args sits after the modeled flags and before the prompt, and is never
// quoted: it is shell text, not a value, the same rule claude's Args
// follows.
func TestCodexCommandArgsAppendedBeforePromptAndVerbatim(t *testing.T) {
	got := codex{}.Command(Options{Model: "o3", Args: "--sandbox workspace-write"})
	want := "codex exec --json --model 'o3' --sandbox workspace-write -- {{prompt}}"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestCodexResume(t *testing.T) {
	got := codex{}.Resume(Options{Model: "o3"})
	want := "cd {{workdir}} && codex resume {{session}}"
	if got != want {
		t.Errorf("Resume = %q, want %q", got, want)
	}
}

func TestCodexSessionReadsThreadStarted(t *testing.T) {
	got, ok := codex{}.Session(`{"type":"thread.started","thread_id":"01a03575-0a83-7601-bdcc-1a734ee2b1b2"}`)
	if !ok || got != "01a03575-0a83-7601-bdcc-1a734ee2b1b2" {
		t.Errorf("Session = (%q, %v), want the thread id and true", got, ok)
	}
}

func TestCodexSessionIgnoresOtherEvents(t *testing.T) {
	lines := []string{
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"hi"}}`,
		`{"type":"turn.completed"}`,
		`{"type":"thread.started"}`, // no thread_id
		"not json at all",
		"",
	}
	for _, line := range lines {
		if got, ok := (codex{}).Session(line); ok {
			t.Errorf("Session(%q) = (%q, true), want it dropped", line, got)
		}
	}
}
