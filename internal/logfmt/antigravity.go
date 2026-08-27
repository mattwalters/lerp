package logfmt

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// antigravity decodes Antigravity CLI's (`agy`) `-p --output-format
// stream-json` stream: one JSON object per line, discriminated by an `event`
// field rather than `type` (the field claude and codex both use).
//
// The mapping was taken from real runs (agy 1.1.21/1.1.22), not guessed: init
// carries the conversation id, a step_update announces a step starting and
// again finishing, and a result ends the run. Two things this decoder alone
// needs: prose can arrive split across a step's ACTIVE and DONE lines, so it
// is stateful like claude's — one buffer, kept only for the step it belongs
// to — and thinking is reported only as a token count on a step that produced
// no text at all, never as its own stretch the way claude's is.
type antigravity struct {
	// step is the index of the agent_response step buf belongs to; -1 when
	// nothing is buffered.
	step int
	buf  strings.Builder
}

func newAntigravity() *antigravity {
	return &antigravity{step: -1}
}

type antigravityLine struct {
	Event string `json:"event"`
	// ConversationID sits on the init line itself, a sibling of the "init"
	// object rather than a field inside it — unlike step_update and result,
	// which carry their own nested copy.
	ConversationID string            `json:"conversation_id"`
	StepUpdate     antigravityStep   `json:"step_update"`
	Result         antigravityResult `json:"result"`
}

type antigravityStep struct {
	StepIndex int                 `json:"step_index"`
	State     string              `json:"state"`
	StepType  string              `json:"step_type"`
	TextDelta string              `json:"text_delta"`
	ToolName  string              `json:"tool_name"`
	ToolInfo  antigravityToolInfo `json:"tool_info"`
	Usage     *antigravityUsage   `json:"usage"`
}

type antigravityToolInfo struct {
	Parameters map[string]json.RawMessage `json:"parameters"`
	Output     string                     `json:"output"`
	Error      struct {
		Message string `json:"message"`
	} `json:"error"`
}

// antigravityUsage is what one agent_response step cost. total_tokens already
// sums input and output (thinking is a breakdown of output, not additive on
// top of it — confirmed against captured runs where total == input+output
// exactly), so nothing here needs claude's four-field sum.
type antigravityUsage struct {
	ThinkingTokens int `json:"thinking_tokens"`
	TotalTokens    int `json:"total_tokens"`
}

type antigravityResult struct {
	Status          string  `json:"status"`
	NumTurns        int     `json:"num_turns"`
	DurationSeconds float64 `json:"duration_seconds"`
}

func (a *antigravity) Decode(line string) (Event, bool) {
	var l antigravityLine
	if json.Unmarshal([]byte(line), &l) != nil {
		return Event{}, false
	}
	switch l.Event {
	case "init":
		return Event{Kind: KindInit, Text: sessionTag(l.ConversationID)}, true
	case "step_update":
		return a.stepUpdate(l.StepUpdate)
	case "result":
		return Event{Kind: KindResult, Text: antigravityResultLine(l.Result), IsError: l.Result.Status != "SUCCESS"}, true
	}
	return Event{}, false
}

func (a *antigravity) stepUpdate(su antigravityStep) (Event, bool) {
	switch su.StepType {
	case "agent_response":
		return a.agentResponse(su)
	case "tool":
		return antigravityTool(su)
	}
	return Event{}, false
}

// agentResponse tracks one step's prose across its ACTIVE and DONE lines.
// text_delta is a true delta — one probed run split a reply across an ACTIVE
// chunk and the words that followed on DONE, another delivered the whole
// thing on DONE alone — so the buffer is keyed to the step it belongs to and
// flushed once, when that step finishes. A step that produced no text at all
// but spent thinking tokens is the only case that reports KindThinking; agy
// has no separate thinking event the way claude and codex do.
func (a *antigravity) agentResponse(su antigravityStep) (Event, bool) {
	if su.State == "ACTIVE" {
		if su.StepIndex != a.step {
			a.buf.Reset()
			a.step = su.StepIndex
		}
		a.buf.WriteString(su.TextDelta)
		return Event{}, false
	}
	if su.State != "DONE" {
		return Event{}, false
	}
	text := su.TextDelta
	if su.StepIndex == a.step {
		text = a.buf.String() + su.TextDelta
	}
	a.buf.Reset()
	a.step = -1

	usage := 0
	if su.Usage != nil {
		usage = su.Usage.TotalTokens
	}
	if strings.TrimSpace(text) == "" {
		if su.Usage != nil && su.Usage.ThinkingTokens > 0 {
			return Event{Kind: KindThinking, Tokens: su.Usage.ThinkingTokens, Usage: usage}, true
		}
		return Event{}, false
	}
	return Event{Kind: KindText, Text: text, Usage: usage}, true
}

func antigravityTool(su antigravityStep) (Event, bool) {
	switch su.State {
	case "ACTIVE":
		return Event{Kind: KindToolCall, Tool: su.ToolName, Text: antigravityTarget(su.ToolInfo.Parameters)}, true
	case "DONE":
		return Event{Kind: KindToolResult, Text: short(su.ToolInfo.Output, maxResult)}, true
	case "ERROR":
		return Event{Kind: KindToolResult, Text: short(su.ToolInfo.Error.Message, maxResult), IsError: true}, true
	}
	return Event{}, false
}

// antigravityTarget is the short "what" beside a tool's name, over agy's own
// parameter keys: a path-like key shows as its base name, a command-like key
// shortened as-is. Captured from real tool calls — view_file's AbsolutePath,
// write_to_file's TargetFile, run_command's CommandLine, grep_search's Query,
// find_by_name's Pattern.
func antigravityTarget(params map[string]json.RawMessage) string {
	for _, key := range []string{"AbsolutePath", "TargetFile"} {
		if s, ok := antigravityParam(params, key); ok {
			return short(filepath.Base(s), maxTarget)
		}
	}
	for _, key := range []string{"CommandLine", "Query", "Pattern"} {
		if s, ok := antigravityParam(params, key); ok {
			return short(s, maxTarget)
		}
	}
	return ""
}

func antigravityParam(params map[string]json.RawMessage, key string) (string, bool) {
	v, ok := params[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(v, &s) != nil || strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

func antigravityResultLine(r antigravityResult) string {
	parts := []string{strings.ToLower(r.Status)}
	if r.NumTurns > 0 {
		parts = append(parts, fmt.Sprintf("%d turns", r.NumTurns))
	}
	if r.DurationSeconds > 0 {
		parts = append(parts, fmt.Sprintf("%.1fs", r.DurationSeconds))
	}
	return strings.Join(parts, " · ")
}
