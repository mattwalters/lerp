package telemetry

import (
	"strings"
	"testing"
)

// A captured claude log: init, one assistant call, a tool round trip, and
// the result line carrying the run's total cost. Usage must not be double
// counted against the result line's own totals (logfmt's claude decoder
// already guards this; Summarize just has to not undo it by summing the
// result line too).
const claudeLog = `{"type":"system","subtype":"init","session_id":"7420e6f8","model":"claude-opus-5"}
{"type":"assistant","message":{"id":"msg_01","content":[{"type":"text","text":"Reading the ticket."}],"usage":{"input_tokens":100,"output_tokens":200,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
{"type":"assistant","message":{"id":"msg_02","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a.go"}}],"usage":{"input_tokens":50,"output_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
{"type":"result","subtype":"success","num_turns":2,"total_cost_usd":0.08}
`

func TestSummarizeTotalsAClaudeLog(t *testing.T) {
	sum := Summarize(strings.NewReader(claudeLog))
	if sum.Tokens != 360 {
		t.Errorf("tokens = %d, want 360 (100+200+50+10)", sum.Tokens)
	}
	if sum.Cost != 0.08 {
		t.Errorf("cost = %v, want 0.08", sum.Cost)
	}
	if sum.Model != "claude-opus-5" {
		t.Errorf("model = %q, want claude-opus-5", sum.Model)
	}
}

// Codex reports usage but no cost and no model; Summarize must not invent
// either.
const codexLog = `{"type":"thread.started","thread_id":"01a03575"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Done."}}
{"type":"turn.completed","usage":{"input_tokens":300,"output_tokens":50}}
`

func TestSummarizeTotalsACodexLogWithNoCost(t *testing.T) {
	sum := Summarize(strings.NewReader(codexLog))
	if sum.Tokens != 350 {
		t.Errorf("tokens = %d, want 350", sum.Tokens)
	}
	if sum.Cost != 0 {
		t.Errorf("cost = %v, want 0 (codex reports none)", sum.Cost)
	}
	if sum.Model != "" {
		t.Errorf("model = %q, want empty (codex names none)", sum.Model)
	}
}

// A command-template runner's log — or any log logfmt has no decoder for —
// decodes as raw text, which carries no usage at all.
func TestSummarizeTotalsARawLogAsNothing(t *testing.T) {
	sum := Summarize(strings.NewReader("just some plain output\nmore output\n"))
	if sum != (Summary{}) {
		t.Errorf("summary of a raw log = %+v, want the zero value", sum)
	}
}

func TestSummarizeTotalsAnEmptyLogAsNothing(t *testing.T) {
	sum := Summarize(strings.NewReader(""))
	if sum != (Summary{}) {
		t.Errorf("summary of an empty log = %+v, want the zero value", sum)
	}
}

// A log truncated mid-line — the ordinary shape of a run killed while
// writing — must not error: the trailing partial line is simply not there
// to total.
func TestSummarizeSurvivesALogTruncatedMidLine(t *testing.T) {
	torn := claudeLog[:len(claudeLog)-20] // cuts into the result line
	sum := Summarize(strings.NewReader(torn))
	if sum.Tokens != 360 {
		t.Errorf("tokens = %d, want 360 from the complete lines alone", sum.Tokens)
	}
	if sum.Cost != 0 {
		t.Errorf("cost = %v, want 0: the result line never completed", sum.Cost)
	}
}
