package logfmt

import (
	"strings"
	"testing"
)

// The lines here are real Claude Code stream-json output, trimmed of the
// fields the decoder ignores.
const (
	claudeInit     = `{"type":"system","subtype":"init","cwd":"/w","session_id":"7420e6f8-8718-4e8e-83d2-df9aa5b735d5","model":"claude-opus-5","permissionMode":"bypassPermissions"}`
	claudeThinking = `{"type":"system","subtype":"thinking_tokens","estimated_tokens":5850,"estimated_tokens_delta":100,"session_id":"7420e6f8-8718-4e8e-83d2-df9aa5b735d5"}`
	claudeText     = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll start by reading the ticket."}]},"session_id":"7420e6f8"}`
	claudeRead     = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Read","input":{"file_path":"/repo/internal/tui/model.go"}}]},"session_id":"7420e6f8"}`
	claudeResult   = `{"type":"result","subtype":"success","is_error":false,"duration_ms":7320,"num_turns":3,"session_id":"7420e6f8","total_cost_usd":0.08}`
)

func TestClaudeDecodesTheStream(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Event
	}{
		{"init", claudeInit, Event{Kind: KindInit, Text: "claude-opus-5 · 7420e6f8"}},
		{"thinking tokens", claudeThinking, Event{Kind: KindThinking, Tokens: 5850}},
		{"prose", claudeText, Event{Kind: KindText, Text: "I'll start by reading the ticket."}},
		{"tool call on a path", claudeRead, Event{Kind: KindToolCall, Tool: "Read", Text: "model.go"}},
		// Every kind of token the call billed for, and the four counts are
		// disjoint: 6 + 91 + 4,096 + 12,000.
		{"an assistant line carries what its call spent",
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the ticket."}],"usage":{"input_tokens":6,"output_tokens":91,"cache_creation_input_tokens":4096,"cache_read_input_tokens":12000}}}`,
			Event{Kind: KindText, Text: "Reading the ticket.", Usage: 16193}},
		{"the run's own total is not counted again",
			`{"type":"result","subtype":"success","num_turns":3,"usage":{"input_tokens":6,"output_tokens":91}}`,
			Event{Kind: KindResult, Text: "success · 3 turns"}},
		{"tool call on a command",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...","description":"Run the tests"}}]}}`,
			Event{Kind: KindToolCall, Tool: "Bash", Text: "go test ./..."}},
		{"a finished thinking block stays collapsed",
			`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"Let me work through the invariants…"}]}}`,
			Event{Kind: KindThinking}},
		{"tool result",
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","is_error":false,"content":"hello from a file\nsecond line"}]}}`,
			Event{Kind: KindToolResult, Text: "hello from a file …"}},
		{"failed tool result",
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"File does not exist."}]}}`,
			Event{Kind: KindToolResult, Text: "File does not exist.", IsError: true}},
		{"tool result carrying blocks",
			`{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"{\"comments\":[]}"}]}]}}`,
			Event{Kind: KindToolResult, Text: `{"comments":[]}`}},
		{"result", claudeResult, Event{Kind: KindResult, Text: "success · 3 turns · 7.3s"}},
		{"failed result",
			`{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":40}`,
			Event{Kind: KindResult, Text: "error_max_turns · 40 turns", IsError: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := claude{}.Decode(tc.line)
			if !ok {
				t.Fatalf("line was not decoded: %s", tc.line)
			}
			if got != tc.want {
				t.Fatalf("decoded %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Everything the pane does not render is dropped where it is decoded, so no
// renderer has to know what a rate limit event is.
func TestClaudeDropsWhatItDoesNotRender(t *testing.T) {
	lines := []string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"},"session_id":"7420e6f8"}`,
		`{"type":"system","subtype":"compact_boundary","session_id":"7420e6f8"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"  "}]}}`,
		`{"type":"assistant","message":{"content":[]}}`,
		// The half line a tail sees when it attaches mid-file.
		`_tokens":100,"session_id":"7420e6f8"}`,
		"",
	}
	for _, line := range lines {
		if ev, ok := (claude{}).Decode(line); ok {
			t.Fatalf("line %q decoded to %+v, want it dropped", line, ev)
		}
	}
}

func TestClaudeShortensLongTargets(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"` +
		strings.Repeat("go test ./... ", 20) + `"}}]}}`
	ev, ok := claude{}.Decode(line)
	if !ok {
		t.Fatal("line was not decoded")
	}
	if len([]rune(ev.Text)) != maxTarget {
		t.Fatalf("target is %d runes, want it cut to %d: %q", len([]rune(ev.Text)), maxTarget, ev.Text)
	}
	if !strings.HasSuffix(ev.Text, "…") {
		t.Fatalf("a cut target does not say so: %q", ev.Text)
	}
}
