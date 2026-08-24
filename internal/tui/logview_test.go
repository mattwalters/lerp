package tui

import (
	"fmt"
	"strings"
	"testing"
)

// A slice of a real Claude Code stream-json log, trimmed of the fields the
// decoder ignores.
var claudeStream = strings.Join([]string{
	`{"type":"system","subtype":"init","session_id":"7420e6f8-8718-4e8e-83d2-df9aa5b735d5","model":"claude-opus-5"}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll read the model first."}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/repo/internal/tui/model.go"}}]}}`,
	`{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":"package tui\nimport \"fmt\""}]}}`,
	`{"type":"system","subtype":"thinking_tokens","estimated_tokens":5850,"estimated_tokens_delta":100}`,
	`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
	`{"type":"result","subtype":"success","is_error":false,"duration_ms":7320,"num_turns":3}`,
	"",
}, "\n")

func feedView(chunks ...string) *logView {
	v := &logView{}
	for _, c := range chunks {
		v.feed([]byte(c))
	}
	return v
}

func TestLogViewReadsAsActivityNotJSON(t *testing.T) {
	out := feedView(claudeStream).render(80)
	for _, want := range []string{
		"⏵ claude-opus-5 · 7420e6f8",
		"I'll read the model first.",
		"⏺ Read model.go",
		"⎿ package tui …",
		"✻ thinking… 5,850 tokens",
		"⏹ success · 3 turns · 7.3s",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered log is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `{"type"`) {
		t.Fatalf("raw stream JSON reached the pane:\n%s", out)
	}
	// The rate limit event is not activity; it is dropped, not printed raw.
	if strings.Contains(out, "rate_limit") {
		t.Fatalf("an unrendered event leaked into the pane:\n%s", out)
	}
}

// Codex's stream renders as the same six kinds, from the same pane code —
// and the line it writes to standard error before its first event, which
// lands in the same log, does not cost it its decoder. Both are real
// codex-cli output.
func TestLogViewReadsCodexToo(t *testing.T) {
	stream := strings.Join([]string{
		"Reading additional input from stdin...",
		`{"type":"thread.started","thread_id":"01a03575-0a83-7601-bdcc-1a734ee2b1b2"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"**Planning the read**"}}`,
		`{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'cat missing.txt'","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'cat missing.txt'","aggregated_output":"cat: missing.txt: No such file or directory\n","exit_code":1,"status":"failed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":31101,"output_tokens":119}}`,
		"",
	}, "\n")
	out := feedView(stream).render(80)
	for _, want := range []string{
		"Reading additional input from stdin...",
		"⏵ 01a03575",
		"✻ thinking…",
		"⏺ shell /bin/zsh -lc 'cat missing.txt'",
		"⎿ cat: missing.txt: No such file or directory",
		"⏹ turn complete · 119 output tokens",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered log is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `{"type"`) {
		t.Fatalf("raw stream JSON reached the pane:\n%s", out)
	}
}

// The heartbeat is one line that gets rewritten, not one line per event —
// nine tenths of a stream-json log is this event.
func TestLogViewCollapsesThinking(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-opus-5"}`,
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens":100}`,
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens":700}`,
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens":1200}`,
		// The finished block arrives without a count; it must not erase one.
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"…"}]}}`,
		"",
	}
	out := feedView(strings.Join(lines, "\n")).render(80)
	if got := strings.Count(out, "thinking…"); got != 1 {
		t.Fatalf("thinking is on %d lines, want 1:\n%s", got, out)
	}
	if !strings.Contains(out, "✻ thinking… 1,200 tokens") {
		t.Fatalf("the collapsed line does not carry the latest count:\n%s", out)
	}

	// A new stretch after a tool call is a new line.
	v := feedView(strings.Join(lines, "\n"),
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test"}}]}}`+"\n",
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens":50}`+"\n")
	if got := strings.Count(v.render(80), "thinking…"); got != 2 {
		t.Fatalf("a second thinking stretch did not get its own line:\n%s", v.render(80))
	}
}

// The floor: a runner whose format is not recognized renders exactly as it
// did before any of this existed — colour kept, lines whole, and the
// half-written last line on screen as it is written.
func TestLogViewKeepsPlainTextAsItIs(t *testing.T) {
	v := feedView("go: downloading deps\n\x1b[32mPASS\x1b[0m\tinternal/tui\n", "half a li")
	out := v.render(80)
	want := cleanLog("go: downloading deps\n\x1b[32mPASS\x1b[0m\tinternal/tui\nhalf a li")
	if out != want {
		t.Fatalf("raw rendering diverged from the bytes:\n got %q\nwant %q", out, want)
	}
}

// A decoded stream's partial line is garbage, not text: it waits for its
// newline instead of reaching the pane half-formed.
func TestLogViewHidesAHalfDecodedLine(t *testing.T) {
	v := feedView(claudeStream, `{"type":"assistant","message":{"content":[{"type":"tool_`)
	if strings.Contains(v.render(80), "tool_") {
		t.Fatalf("a half-written event reached the pane:\n%s", v.render(80))
	}
}

func TestLogViewWrapsProse(t *testing.T) {
	text := strings.Repeat("plan the change ", 12)
	line := fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`, text)
	out := feedView(`{"type":"system","subtype":"init","model":"m"}` + "\n" + line + "\n").render(40)
	for _, row := range strings.Split(out, "\n") {
		if len(row) > 40 {
			t.Fatalf("prose row is %d columns wide, want at most 40:\n%s", len(row), out)
		}
	}
	if !strings.Contains(strings.ReplaceAll(out, "\n", " "), "plan the change plan the change") {
		t.Fatalf("wrapping lost the prose:\n%s", out)
	}
}

// Agent output is untrusted the same way Linear text is: a tool result holds
// whatever some command printed, escape sequences and all.
func TestLogViewMakesAgentOutputInert(t *testing.T) {
	evil := `{"type":"user","message":{"content":[{"type":"tool_result","content":"innocent\u001b[2J\u0007EVIL\rrewritten"}]}}`
	out := feedView(claudeStream + evil + "\n").render(80)
	if strings.ContainsAny(out, "\x1b\r\a") {
		t.Fatalf("a control sequence from a tool result reached the pane: %q", out)
	}
	// Inert, not censored: the text still reads, minus what would have moved
	// the cursor — and a carriage return cannot repaint the row it is on
	// because a tool result is cut to its first line.
	if !strings.Contains(out, "innocentEVIL") {
		t.Fatalf("the text itself was dropped rather than made inert:\n%s", out)
	}
}

// Rendered scrollback is bounded the way the raw scrollback is.
func TestLogViewBoundsScrollback(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"type":"system","subtype":"init","model":"m"}` + "\n")
	for i := 0; i < logRows*2; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"step %d"}}]}}`+"\n", i)
	}
	v := feedView(b.String())
	if len(v.rows) > logRows {
		t.Fatalf("kept %d rows, cap is %d", len(v.rows), logRows)
	}
	out := v.render(80)
	if !strings.Contains(out, fmt.Sprintf("step %d", logRows*2-1)) {
		t.Fatal("the newest row fell out of the scrollback")
	}
	if strings.Contains(out, "step 0 ") {
		t.Fatal("the oldest row survived the cap")
	}
}
