package logfmt

import "testing"

// Real codex-cli 0.148.0 `exec --json` output, captured from a run that read
// a file, ran a command that failed, wrote a file, and finished.
const (
	codexStart   = `{"type":"thread.started","thread_id":"01a03575-0a83-7601-bdcc-1a734ee2b1b2"}`
	codexMessage = `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"The note says hello."}}`
	codexShell   = `{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'cat note.txt'","aggregated_output":"","exit_code":null,"status":"in_progress"}}`
	codexFailed  = `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'cat missing.txt'","aggregated_output":"cat: missing.txt: No such file or directory\n","exit_code":1,"status":"failed"}}`
	codexTurn    = `{"type":"turn.completed","usage":{"input_tokens":31101,"cached_input_tokens":26112,"output_tokens":119,"reasoning_output_tokens":0}}`
)

func TestCodexDecodesTheStream(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Event
	}{
		{"thread start", codexStart, Event{Kind: KindInit, Text: "01a03575"}},
		{"prose", codexMessage, Event{Kind: KindText, Text: "The note says hello."}},
		{"reasoning collapses without a count",
			`{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"**Planning patch-based todo update**"}}`,
			Event{Kind: KindThinking}},
		{"command starts", codexShell,
			Event{Kind: KindToolCall, Tool: "shell", Text: "/bin/zsh -lc 'cat note.txt'"}},
		{"command fails", codexFailed,
			Event{Kind: KindToolResult, Text: "cat: missing.txt: No such file or directory", IsError: true}},
		{"file change starts",
			`{"type":"item.started","item":{"id":"item_6","type":"file_change","changes":[{"path":"/tmp/spike/fib.txt","kind":"add"}],"status":"in_progress"}}`,
			Event{Kind: KindToolCall, Tool: "edit", Text: "add fib.txt"}},
		{"file change completes",
			`{"type":"item.completed","item":{"id":"item_6","type":"file_change","changes":[{"path":"/tmp/spike/fib.txt","kind":"add"}],"status":"completed"}}`,
			Event{Kind: KindToolResult, Text: "completed"}},
		{"an item-level error", `{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Model metadata not found."}}`,
			Event{Kind: KindToolResult, Text: "Model metadata not found.", IsError: true}},
		// The turn is where the run's usage arrives; cached input is part of
		// the input Codex reports, so the total is 31,101 + 119.
		{"turn completes", codexTurn,
			Event{Kind: KindResult, Text: "turn complete · 119 output tokens", Usage: 31220}},
		{"a turn that reports no usage costs nothing",
			`{"type":"turn.completed"}`, Event{Kind: KindResult, Text: "turn complete"}},
		{"turn fails", `{"type":"turn.failed","error":{"message":"the model is not supported"}}`,
			Event{Kind: KindResult, Text: "the model is not supported", IsError: true}},
		{"stream error", `{"type":"error","message":"the model is not supported"}`,
			Event{Kind: KindResult, Text: "the model is not supported", IsError: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := codex{}.Decode(tc.line)
			if !ok {
				t.Fatalf("line was not decoded: %s", tc.line)
			}
			if got != tc.want {
				t.Fatalf("decoded %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCodexDropsWhatItDoesNotRender(t *testing.T) {
	lines := []string{
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"item_8","type":"todo_list","items":[{"text":"Write the result","completed":true}]}}`,
		`{"type":"item.completed","item":{"id":"item_8","type":"todo_list","items":[{"text":"Write the result","completed":true}]}}`,
		`{"type":"item.started","item":{"id":"item_0","type":"agent_message","text":""}}`,
		`{"item":{"id":"item_1","type":"command_execution"}}`,
		"not json at all",
	}
	for _, line := range lines {
		if ev, ok := (codex{}).Decode(line); ok {
			t.Fatalf("line %q decoded to %+v, want it dropped", line, ev)
		}
	}
}
