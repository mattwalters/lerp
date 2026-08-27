package logfmt

import (
	"fmt"
	"strings"
	"testing"
	"time"
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
		// disjoint: 6 + 91 + 4,096 + 12,000. Context is the input side only
		// (6 + 4,096 + 12,000), output excluded — how full the window is,
		// not what the run was billed for.
		{"an assistant line carries what its call spent",
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the ticket."}],"usage":{"input_tokens":6,"output_tokens":91,"cache_creation_input_tokens":4096,"cache_read_input_tokens":12000}}}`,
			Event{Kind: KindText, Text: "Reading the ticket.", Usage: 16193, Context: 16102}},
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
		{"result", claudeResult, Event{Kind: KindResult, Text: "success · 3 turns · 7.3s", Cost: 0.08}},
		{"failed result",
			`{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":40}`,
			Event{Kind: KindResult, Text: "error_max_turns · 40 turns", IsError: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := (&claude{}).Decode(tc.line)
			if !ok {
				t.Fatalf("line was not decoded: %s", tc.line)
			}
			if got != tc.want {
				t.Fatalf("decoded %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The stream dates its assistant and user lines, and the event carries the
// date: it is what lets a reader that attached late put the events it is
// catching up on where they happened. A line that carries none — the system
// lines do not — reports no time rather than a made-up one.
func TestClaudeDatesItsLines(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-26T23:42:44.170Z",` +
		`"message":{"content":[{"type":"text","text":"Reading the ticket."}]}}`
	ev, ok := (&claude{}).Decode(line)
	if !ok {
		t.Fatalf("line was not decoded: %s", line)
	}
	if want := time.Date(2026, 8, 26, 23, 42, 44, 170_000_000, time.UTC); !ev.Time.Equal(want) {
		t.Fatalf("the line's time decoded as %v, want %v", ev.Time, want)
	}
	if ev, _ := (&claude{}).Decode(claudeText); !ev.Time.IsZero() {
		t.Fatalf("an undated line invented the time %v", ev.Time)
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
	dec := &claude{}
	for _, line := range lines {
		if ev, ok := dec.Decode(line); ok {
			t.Fatalf("line %q decoded to %+v, want it dropped", line, ev)
		}
	}
}

func TestClaudeShortensLongTargets(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"` +
		strings.Repeat("go test ./... ", 20) + `"}}]}}`
	ev, ok := (&claude{}).Decode(line)
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

// Claude Code writes one API call as several lines — one per content block —
// and every one of them repeats the call's identical usage. Summing them all
// billed a thinking-plus-prose-plus-tool call three times; against a real run
// log it read 2.08x high.
func TestClaudeCountsOneCallOnce(t *testing.T) {
	dec := &claude{}
	lines := []string{
		claudeBlockLine("msg_01", "thinking", `"thinking":"weighing it"`),
		claudeBlockLine("msg_01", "text", `"text":"Reading the ticket."`),
		claudeBlockLine("msg_01", "tool_use", `"name":"Read","input":{"file_path":"/repo/a.go"}`),
	}
	var total int
	for _, line := range lines {
		ev, ok := dec.Decode(line)
		if !ok {
			t.Fatalf("line was not decoded: %s", line)
		}
		total += ev.Usage
	}
	if total != 1000 {
		t.Fatalf("three lines of one call billed %d tokens, want 1000", total)
	}
	// A second call is a second charge; the guard is against repeats, not
	// against spending.
	ev, _ := dec.Decode(claudeBlockLine("msg_02", "text", `"text":"Done."`))
	if ev.Usage != 1000 {
		t.Fatalf("the next call billed %d tokens, want 1000", ev.Usage)
	}
}

// Parallel subagents write into one log, so nothing keeps the lines of two
// calls from arriving interleaved. Remembering only the last message id would
// bill both of them once per line all over again.
func TestClaudeCountsInterleavedCallsOnce(t *testing.T) {
	dec := &claude{}
	var total int
	for _, id := range []string{"msg_a", "msg_b", "msg_a", "msg_b", "msg_a"} {
		ev, ok := dec.Decode(claudeBlockLine(id, "text", `"text":"working"`))
		if !ok {
			t.Fatal("line was not decoded")
		}
		total += ev.Usage
	}
	if total != 2000 {
		t.Fatalf("two interleaved calls billed %d tokens, want 2000", total)
	}
}

// The ring is what holds interleaved calls apart. This does not pin its size
// — countedMessages has room to spare over what is asserted here — it pins
// the floor: a run fans out to several subagents at once, so a dozen calls in
// flight is the order of fan-out to hold against, and a ring that could not
// hold that many would forget the oldest and bill its next line again, which
// is the bug back for one call.
func TestClaudeCountsManyCallsInFlightOnce(t *testing.T) {
	const inFlight = 12
	dec := &claude{}
	var total int
	// Every call opens, then every one of them writes a second line: the
	// widest interleaving of that many messages there is.
	for range 2 {
		for i := range inFlight {
			ev, ok := dec.Decode(claudeBlockLine(fmt.Sprintf("msg_%02d", i), "text", `"text":"working"`))
			if !ok {
				t.Fatal("line was not decoded")
			}
			total += ev.Usage
		}
	}
	if total != 1000*inFlight {
		t.Fatalf("%d interleaved calls billed %d tokens, want %d", inFlight, total, 1000*inFlight)
	}
}

// A line that decodes to nothing worth showing never reports its usage, so it
// must not spend the call's one charge either: the next line of the same
// message still has to carry it.
func TestClaudeKeepsUsageBehindADroppedLine(t *testing.T) {
	dec := &claude{}
	if ev, ok := dec.Decode(claudeBlockLine("msg_01", "text", `"text":"   "`)); ok {
		t.Fatalf("a blank block decoded to %+v, want it dropped", ev)
	}
	ev, ok := dec.Decode(claudeBlockLine("msg_01", "tool_use", `"name":"Read","input":{"file_path":"/a.go"}`))
	if !ok {
		t.Fatal("line was not decoded")
	}
	if ev.Usage != 1000 {
		t.Fatalf("the call billed %d tokens after a dropped line, want 1000", ev.Usage)
	}
}

// A line naming no message cannot be told apart from a repeat of itself, and
// dropping its usage would undercount a real call — the worse of the two
// errors, and the one nobody would spot.
func TestClaudeCountsLinesWithNoMessageID(t *testing.T) {
	dec := &claude{}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}],` +
		`"usage":{"input_tokens":400,"output_tokens":600}}}`
	var total int
	for range 2 {
		ev, ok := dec.Decode(line)
		if !ok {
			t.Fatal("line was not decoded")
		}
		total += ev.Usage
	}
	if total != 2000 {
		t.Fatalf("two unidentified lines billed %d tokens, want 2000", total)
	}
}

// claudeBlockLine is one assistant line: a message id, one content block, and
// the 1,000-token usage every line of that call repeats.
func claudeBlockLine(id, kind, body string) string {
	return `{"type":"assistant","message":{"id":"` + id + `","role":"assistant","content":[{"type":"` +
		kind + `",` + body + `}],"usage":{"input_tokens":100,"output_tokens":200,` +
		`"cache_creation_input_tokens":300,"cache_read_input_tokens":400}}}`
}

// claudeAgentLine is one assistant line from the given agent — "" for the
// top-level agent, a subagent's Task tool_use_id otherwise — whose usage
// reads inputTokens on the input side.
func claudeAgentLine(parentToolUseID string, inputTokens int) string {
	return fmt.Sprintf(`{"type":"assistant","parent_tool_use_id":%q,`+
		`"message":{"content":[{"type":"text","text":"working"}],"usage":{"input_tokens":%d}}}`,
		parentToolUseID, inputTokens)
}

// claudeTaskNotification is a subagent lifecycle line: a "task_notification"
// naming the Task tool_use_id and its status.
func claudeTaskNotification(toolUseID, status string) string {
	return fmt.Sprintf(`{"type":"system","subtype":"task_notification","tool_use_id":%q,"status":%q}`,
		toolUseID, status)
}

// The worst context reading among a run's agents is what the row shows, so a
// subagent that outruns its own parent must not hide behind the parent's
// smaller figure — the log LERP-150 verified against read 274k top-level
// against 132k and 141k subagents; this is the inverted case the ticket asks
// for, a small parent and a subagent that outgrows it.
func TestClaudeAttributesContextPerAgent(t *testing.T) {
	dec := &claude{}
	ev, ok := dec.Decode(claudeAgentLine("", 1000))
	if !ok {
		t.Fatal("line was not decoded")
	}
	if ev.Context != 1000 {
		t.Fatalf("the top-level agent's line reported Context %d, want 1000", ev.Context)
	}

	ev, ok = dec.Decode(claudeAgentLine("toolu_sub", 5000))
	if !ok {
		t.Fatal("line was not decoded")
	}
	if ev.Context != 5000 {
		t.Fatalf("a bigger subagent's line reported Context %d, want the worst-of 5000", ev.Context)
	}

	ev, ok = dec.Decode(claudeAgentLine("", 1200))
	if !ok {
		t.Fatal("line was not decoded")
	}
	if ev.Context != 5000 {
		t.Fatalf("the top-level agent growing to 1200 reported Context %d, want the subagent's 5000 to stay the worst", ev.Context)
	}
}

// A finished subagent's high-water mark must not haunt the row once it is
// gone: the completion notification retires its entry, so the next line
// falls back to whatever is still running.
func TestClaudeRetiresAFinishedSubagent(t *testing.T) {
	dec := &claude{}
	dec.Decode(claudeAgentLine("", 1000))
	dec.Decode(claudeAgentLine("toolu_sub", 9000))

	if ev, ok := dec.Decode(claudeTaskNotification("toolu_sub", "completed")); ok {
		t.Fatalf("a task_notification line decoded to %+v, want it dropped", ev)
	}

	ev, ok := dec.Decode(claudeAgentLine("", 1100))
	if !ok {
		t.Fatal("line was not decoded")
	}
	if ev.Context != 1100 {
		t.Fatalf("after the subagent's completion the figure read %d, want the parent's own 1100", ev.Context)
	}
}

// A status other than "completed" — the subagent has only started — must not
// retire anything: only a genuine completion frees the slot.
func TestClaudeUncompletedNotificationDoesNotRetire(t *testing.T) {
	dec := &claude{}
	dec.Decode(claudeAgentLine("toolu_sub", 9000))
	dec.Decode(claudeTaskNotification("toolu_sub", "started"))

	ev, ok := dec.Decode(claudeAgentLine("", 100))
	if !ok {
		t.Fatal("line was not decoded")
	}
	if ev.Context != 9000 {
		t.Fatalf("a non-completion notification changed the worst-of to %d, want the subagent's 9000 untouched", ev.Context)
	}
}

// A stream that never states usage reports Context zero throughout — the
// same "the runner does not say" floor Usage and Cost already keep.
func TestClaudeContextZeroWithoutUsage(t *testing.T) {
	ev, ok := (&claude{}).Decode(claudeText)
	if !ok {
		t.Fatal("line was not decoded")
	}
	if ev.Context != 0 {
		t.Fatalf("a line with no usage reported Context %d, want 0", ev.Context)
	}
}

// The agent map is capped the way the counted-messages ring is: a day-long
// run's fan-out is unbounded and this is a board that stays up. Past the cap
// the oldest tracked agent is evicted, so its stale high-water mark cannot
// keep winning the worst-of forever.
func TestClaudeTracksAgentCapEvicts(t *testing.T) {
	dec := &claude{}
	dec.Decode(claudeAgentLine("agent-0", 999999))

	var ev Event
	for i := 1; i <= trackedAgents; i++ {
		var ok bool
		ev, ok = dec.Decode(claudeAgentLine(fmt.Sprintf("agent-%d", i), 1))
		if !ok {
			t.Fatal("line was not decoded")
		}
	}
	if ev.Context != 1 {
		t.Fatalf("the worst-of figure read %d once the cap evicted the huge agent, want 1", ev.Context)
	}
}

// A run whose subagents come and go one at a time — never more than a
// couple live together — must never evict the top-level agent to make room,
// however many subagents it runs through over its life: each completion
// already frees the slot the next subagent needs, so the cap only ever
// engages for agents genuinely in flight at once. The gate is the map's
// live size, not a count of every agent ever seen — the latter would make
// the top-level agent, alive for the whole run and inserted first, the
// deterministic first eviction once enough subagents had passed through.
func TestClaudeSequentialSubagentsNeverEvictTheTopLevelAgent(t *testing.T) {
	dec := &claude{}
	dec.Decode(claudeAgentLine("", 150000))

	for i := range trackedAgents * 3 {
		id := fmt.Sprintf("toolu_sub_%d", i)
		dec.Decode(claudeAgentLine(id, 500))
		dec.Decode(claudeTaskNotification(id, "completed"))
	}

	ev, ok := dec.Decode(claudeAgentLine("", 151000))
	if !ok {
		t.Fatal("line was not decoded")
	}
	if ev.Context != 151000 {
		t.Fatalf("after %d sequential subagents the top-level agent's own line read Context %d, want its own 151000 — its entry was evicted", trackedAgents*3, ev.Context)
	}
}
