package logfmt

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// codex decodes Codex CLI's `exec --json` stream: one JSON object per line,
// items announced when they start and again when they finish.
//
// The mapping was taken from real runs (codex-cli 0.148.0), not guessed: a
// thread start carries the id, agent_message is prose, reasoning is a
// thinking stretch, command_execution and file_change are tool calls that
// report back, and a turn ends the run. Nothing here needed a field the
// normalized event did not already have — the one asymmetry is that Codex
// does not stream a running thinking-token count, so its thinking line
// collapses without one.
type codex struct{}

type codexLine struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Item struct {
		Type             string `json:"type"`
		Text             string `json:"text"`
		Message          string `json:"message"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregated_output"`
		Status           string `json:"status"`
		Changes          []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
	} `json:"item"`
}

func (codex) Decode(line string) (Event, bool) {
	var l codexLine
	if json.Unmarshal([]byte(line), &l) != nil {
		return Event{}, false
	}
	switch l.Type {
	case "thread.started":
		return Event{Kind: KindInit, Text: sessionTag(l.ThreadID)}, true
	case "item.started":
		return codexItemStarted(l)
	case "item.completed":
		return codexItemCompleted(l)
	case "turn.completed":
		return Event{Kind: KindResult, Text: turnLine(l)}, true
	case "turn.failed":
		return Event{Kind: KindResult, Text: short(l.Error.Message, maxResult), IsError: true}, true
	case "error":
		return Event{Kind: KindResult, Text: short(l.Message, maxResult), IsError: true}, true
	}
	return Event{}, false
}

// codexItemStarted renders the half of an item that says work began. Prose
// and reasoning arrive complete, so only the tool-shaped items report here.
func codexItemStarted(l codexLine) (Event, bool) {
	switch l.Item.Type {
	case "command_execution":
		return Event{Kind: KindToolCall, Tool: "shell", Text: short(l.Item.Command, maxTarget)}, true
	case "file_change":
		return Event{Kind: KindToolCall, Tool: "edit", Text: changed(l)}, true
	}
	return Event{}, false
}

func codexItemCompleted(l codexLine) (Event, bool) {
	switch l.Item.Type {
	case "agent_message":
		if strings.TrimSpace(l.Item.Text) == "" {
			return Event{}, false
		}
		return Event{Kind: KindText, Text: l.Item.Text}, true
	case "reasoning":
		return Event{Kind: KindThinking}, true
	case "command_execution":
		return Event{Kind: KindToolResult, Text: short(l.Item.AggregatedOutput, maxResult),
			IsError: l.Item.Status == "failed"}, true
	case "file_change":
		return Event{Kind: KindToolResult, Text: l.Item.Status,
			IsError: l.Item.Status == "failed"}, true
	case "error":
		return Event{Kind: KindToolResult, Text: short(l.Item.Message, maxResult), IsError: true}, true
	}
	return Event{}, false
}

// changed names a file_change's target the way a tool call's target reads
// elsewhere: what happened, to which file.
func changed(l codexLine) string {
	if len(l.Item.Changes) == 0 {
		return ""
	}
	c := l.Item.Changes[0]
	text := filepath.Base(c.Path)
	if c.Kind != "" {
		text = c.Kind + " " + text
	}
	if n := len(l.Item.Changes) - 1; n > 0 {
		text += fmt.Sprintf(" (+%d)", n)
	}
	return short(text, maxTarget)
}

func turnLine(l codexLine) string {
	if l.Usage != nil && l.Usage.OutputTokens > 0 {
		return fmt.Sprintf("turn complete · %d output tokens", l.Usage.OutputTokens)
	}
	return "turn complete"
}
