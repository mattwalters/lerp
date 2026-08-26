package logfmt

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// claude decodes Claude Code's `--output-format stream-json --verbose`
// stream: one JSON object per line, one assistant content block per object.
type claude struct{}

type claudeLine struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	SessionID       string `json:"session_id"`
	Model           string `json:"model"`
	EstimatedTokens int    `json:"estimated_tokens"`
	NumTurns        int    `json:"num_turns"`
	DurationMS      int64  `json:"duration_ms"`
	IsError         bool   `json:"is_error"`
	Message         struct {
		Content []claudeBlock `json:"content"`
		Usage   claudeUsage   `json:"usage"`
	} `json:"message"`
}

// claudeUsage is what one API call cost, as the assistant line reports it.
// The four counts are disjoint — cache reads are not part of input_tokens,
// the way Codex's cached_input_tokens are part of its input — so the total is
// their sum.
type claudeUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
}

func (u claudeUsage) total() int {
	return u.InputTokens + u.OutputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

type claudeBlock struct {
	Type     string                     `json:"type"`
	Text     string                     `json:"text"`
	Thinking string                     `json:"thinking"`
	Name     string                     `json:"name"`
	Input    map[string]json.RawMessage `json:"input"`
	Content  json.RawMessage            `json:"content"`
	IsError  bool                       `json:"is_error"`
}

func (claude) Decode(line string) (Event, bool) {
	var l claudeLine
	if json.Unmarshal([]byte(line), &l) != nil {
		return Event{}, false
	}
	switch l.Type {
	case "system":
		switch l.Subtype {
		case "init":
			return Event{Kind: KindInit, Text: runHeader(l.Model, l.SessionID)}, true
		case "thinking_tokens":
			// The stream's heartbeat: a running count for the thinking block
			// in progress, restarting at each one.
			return Event{Kind: KindThinking, Tokens: l.EstimatedTokens}, true
		}
	case "assistant", "user":
		// The stream emits one content block per line; a line carrying
		// several renders the first block it knows, which is the one the
		// operator would have read first anyway.
		for _, b := range l.Message.Content {
			if ev, ok := block(b); ok {
				// The usage belongs to the call, not to the block that came
				// back from it: an assistant line reports what that call
				// spent whatever it chose to say. A user line — a tool
				// result — reports none, so this is zero there.
				ev.Usage = l.Message.Usage.total()
				return ev, true
			}
		}
	case "result":
		// The result line carries the run's own total, which is the sum this
		// stream has been keeping all along. Reporting it as usage would
		// count the whole run twice at the end of it.
		return Event{Kind: KindResult, Text: resultLine(l), IsError: l.IsError}, true
	}
	return Event{}, false
}

func block(b claudeBlock) (Event, bool) {
	switch b.Type {
	case "thinking":
		return Event{Kind: KindThinking}, true
	case "text":
		if strings.TrimSpace(b.Text) == "" {
			return Event{}, false
		}
		return Event{Kind: KindText, Text: b.Text}, true
	case "tool_use":
		return Event{Kind: KindToolCall, Tool: b.Name, Text: target(b.Input)}, true
	case "tool_result":
		text := short(resultText(b.Content), maxResult)
		if text == "" {
			return Event{}, false
		}
		return Event{Kind: KindToolResult, Text: text, IsError: b.IsError}, true
	}
	return Event{}, false
}

// target is the short "what" beside a tool's name. The keys are the ones the
// stock tools carry; a path shows as its base name, which is what fits a lane
// pane and what a reader is scanning for.
func target(input map[string]json.RawMessage) string {
	for _, key := range []string{"file_path", "path", "notebook_path"} {
		if s, ok := inputString(input, key); ok {
			return short(filepath.Base(s), maxTarget)
		}
	}
	for _, key := range []string{"command", "pattern", "query", "url", "prompt", "description"} {
		if s, ok := inputString(input, key); ok {
			return short(s, maxTarget)
		}
	}
	return ""
}

func inputString(input map[string]json.RawMessage, key string) (string, bool) {
	v, ok := input[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(v, &s) != nil || strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

// resultText reads a tool result's content, which the API allows to be either
// a plain string or a list of blocks.
func resultText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) != "" {
			return b.Text
		}
	}
	return ""
}

func runHeader(model, session string) string {
	parts := []string{}
	if model != "" {
		parts = append(parts, model)
	}
	if session != "" {
		parts = append(parts, sessionTag(session))
	}
	return strings.Join(parts, " · ")
}

func resultLine(l claudeLine) string {
	parts := []string{l.Subtype}
	if l.Subtype == "" {
		parts[0] = "done"
	}
	if l.NumTurns > 0 {
		parts = append(parts, fmt.Sprintf("%d turns", l.NumTurns))
	}
	if l.DurationMS > 0 {
		parts = append(parts, fmt.Sprintf("%.1fs", float64(l.DurationMS)/1000))
	}
	return strings.Join(parts, " · ")
}

// sessionTag shortens a session or thread UUID to the prefix a human uses to
// tell two runs apart.
func sessionTag(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	return short(id, 8)
}
