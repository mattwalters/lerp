package logfmt

import "testing"

// Real agy (Antigravity CLI) 1.1.22 `-p --output-format stream-json` output,
// captured from runs that gave a bare prose reply, read a file, and ran a
// tool that errored.
const (
	antigravityInitLine    = `{"event":"init","conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98","init":{"cwd":"/tmp/agyprobe1","tools":["view_file"],"permission_mode":"request-review"}}`
	antigravityUserInput   = `{"event":"step_update","step_update":{"conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98","step_index":0,"state":"DONE","step_type":"user_input"}}`
	antigravityProseStart  = `{"event":"step_update","step_update":{"conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98","step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"Write a failing test,  \nFix the code to make it green,  \nShip with confidence."}}`
	antigravityProseDone   = `{"event":"step_update","step_update":{"conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98","step_index":1,"state":"DONE","step_type":"agent_response","text_delta":"\n","duration_seconds":3.570224,"usage":{"input_tokens":13817,"output_tokens":332,"thinking_tokens":311,"cache_read_tokens":0,"total_tokens":14149}}}`
	antigravityThinkOnly   = `{"event":"step_update","step_update":{"conversation_id":"c50b4e3f-6b7d-4521-8821-5558448eda5e","step_index":1,"state":"DONE","step_type":"agent_response","duration_seconds":1.27139,"usage":{"input_tokens":13823,"output_tokens":112,"thinking_tokens":67,"cache_read_tokens":0,"total_tokens":13935}}}`
	antigravityToolStart   = `{"event":"step_update","step_update":{"conversation_id":"c50b4e3f-6b7d-4521-8821-5558448eda5e","step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"view_file","tool_info":{"name":"view_file","parameters":{"AbsolutePath":"/tmp/agyprobe2/note.txt"}}}}`
	antigravityToolDone    = `{"event":"step_update","step_update":{"conversation_id":"c50b4e3f-6b7d-4521-8821-5558448eda5e","step_index":2,"state":"DONE","step_type":"tool","tool_name":"view_file","duration_seconds":0.039356,"tool_info":{"name":"view_file","parameters":{"AbsolutePath":"/tmp/agyprobe2/note.txt"},"output":"2 lines, 21 bytes"}}}`
	antigravityToolError   = `{"event":"step_update","step_update":{"conversation_id":"69fdb1c8-99f3-4a96-809e-5c3253aecff6","step_index":4,"state":"ERROR","step_type":"tool","tool_name":"write_to_file","duration_seconds":0.048459,"tool_info":{"name":"write_to_file","parameters":{"TargetFile":"/Users/matt/.gemini/antigravity-cli/scratch/marker.txt"},"error":{"type":"TOOL_ERROR","message":"declaring permissions: cortex tool write_to_file: convert tool call for permissions: model output error: invalid tool call error (invalid_args) /Users/matt/.gemini/antigravity-cli/scratch/marker.txt is not a valid artifact path"}}}}`
	antigravityResultLine1 = `{"event":"result","result":{"conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98","status":"SUCCESS","response":"Write a failing test,  \nFix the code to make it green,  \nShip with confidence.\n","duration_seconds":3.630288,"num_turns":1,"usage":{"input_tokens":13817,"output_tokens":332,"thinking_tokens":311,"cache_read_tokens":0,"total_tokens":14149}}}`
)

func TestAntigravityDecodesTheStream(t *testing.T) {
	t.Run("init", func(t *testing.T) {
		got, ok := newAntigravity().Decode(antigravityInitLine)
		if !ok || got != (Event{Kind: KindInit, Text: "ffd2f49a"}) {
			t.Fatalf("decoded %+v, ok=%v", got, ok)
		}
	})

	t.Run("prose split across ACTIVE and DONE is buffered then flushed once", func(t *testing.T) {
		a := newAntigravity()
		if ev, ok := a.Decode(antigravityProseStart); ok {
			t.Fatalf("the ACTIVE chunk decoded to %+v, want it buffered", ev)
		}
		got, ok := a.Decode(antigravityProseDone)
		want := Event{Kind: KindText,
			Text:  "Write a failing test,  \nFix the code to make it green,  \nShip with confidence.\n",
			Usage: 14149}
		if !ok || got != want {
			t.Fatalf("decoded %+v, ok=%v, want %+v", got, ok, want)
		}
	})

	t.Run("a DONE with no text and thinking tokens is thinking, not text", func(t *testing.T) {
		got, ok := newAntigravity().Decode(antigravityThinkOnly)
		want := Event{Kind: KindThinking, Tokens: 67, Usage: 13935}
		if !ok || got != want {
			t.Fatalf("decoded %+v, ok=%v, want %+v", got, ok, want)
		}
	})

	t.Run("tool call starts", func(t *testing.T) {
		got, ok := newAntigravity().Decode(antigravityToolStart)
		want := Event{Kind: KindToolCall, Tool: "view_file", Text: "note.txt"}
		if !ok || got != want {
			t.Fatalf("decoded %+v, ok=%v, want %+v", got, ok, want)
		}
	})

	t.Run("tool call completes", func(t *testing.T) {
		got, ok := newAntigravity().Decode(antigravityToolDone)
		want := Event{Kind: KindToolResult, Text: "2 lines, 21 bytes"}
		if !ok || got != want {
			t.Fatalf("decoded %+v, ok=%v, want %+v", got, ok, want)
		}
	})

	t.Run("tool call errors", func(t *testing.T) {
		got, ok := newAntigravity().Decode(antigravityToolError)
		if !ok || got.Kind != KindToolResult || !got.IsError {
			t.Fatalf("decoded %+v, ok=%v, want an error tool result", got, ok)
		}
		if got.Text == "" {
			t.Fatal("an error tool result carried no message")
		}
	})

	t.Run("the result line carries the run's own status, not usage", func(t *testing.T) {
		got, ok := newAntigravity().Decode(antigravityResultLine1)
		want := Event{Kind: KindResult, Text: "success · 1 turns · 3.6s"}
		if !ok || got != want {
			t.Fatalf("decoded %+v, ok=%v, want %+v", got, ok, want)
		}
	})

	t.Run("a failed result is an error", func(t *testing.T) {
		line := `{"event":"result","result":{"status":"ERROR","num_turns":1,"duration_seconds":376.590509}}`
		got, ok := newAntigravity().Decode(line)
		if !ok || !got.IsError {
			t.Fatalf("decoded %+v, ok=%v, want IsError", got, ok)
		}
	})
}

func TestAntigravityDropsWhatItDoesNotRender(t *testing.T) {
	a := newAntigravity()
	lines := []string{
		antigravityUserInput,
		`{"event":"step_update","step_update":{"step_index":5,"state":"DONE","step_type":"system_message"}}`,
		`{"event":"step_update","step_update":{"step_index":8,"state":"DONE","step_type":"error_message"}}`,
		`{"event":"turn_something","turn_something":{}}`,
		"not json at all",
	}
	for _, line := range lines {
		if ev, ok := a.Decode(line); ok {
			t.Fatalf("line %q decoded to %+v, want it dropped", line, ev)
		}
	}
}

func TestAntigravityBufferDoesNotLeakAcrossSteps(t *testing.T) {
	a := newAntigravity()
	a.Decode(antigravityProseStart) // buffers step 1's prose
	// A later step's DONE with its own text must not inherit step 1's buffer.
	line := `{"event":"step_update","step_update":{"step_index":9,"state":"DONE","step_type":"agent_response","text_delta":"unrelated","usage":{"total_tokens":5}}}`
	got, ok := a.Decode(line)
	want := Event{Kind: KindText, Text: "unrelated", Usage: 5}
	if !ok || got != want {
		t.Fatalf("decoded %+v, ok=%v, want %+v (no leakage from an earlier step's buffer)", got, ok, want)
	}
}
