package logfmt

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// claude decodes Claude Code's `--output-format stream-json --verbose`
// stream: one JSON object per line, one assistant content block per object.
//
// It is the one stateful decoder, and only to keep the token count honest:
// see counted.
type claude struct {
	// counted holds the ids of the messages whose usage has already been
	// reported, newest overwriting oldest.
	counted [countedMessages]string
	next    int
	// agents holds each live agent's latest input-side reading, keyed by
	// parent_tool_use_id ("" for the top-level agent) — a reading, not a
	// sum, so a later line for the same agent simply replaces its entry.
	// agentOrder is the same set's insertion order, oldest first: a queue
	// rather than a fixed ring, because a completion (retire) has to remove
	// an entry from the middle of it, not just overwrite the newest. Past
	// trackedAgents live at once, the oldest is evicted to bound memory; an
	// entry retired before then is cut out directly, so it never lingers
	// as a stale slot nothing can reach, and a run whose subagents come and
	// go one at a time, however many over its life, never forces the
	// top-level agent's own entry out to make room for one of them.
	agents     map[string]int
	agentOrder []string
	names      map[string]string
	agentSeq   int
}

// trackedAgents bounds the agent map the same way countedMessages bounds the
// counted ring: a day-long run's fan-out is unbounded and this is a board
// that stays up.
const trackedAgents = 32

// countedMessages is how many messages the decoder remembers having billed.
// One API call is written as several lines — one per content block — and each
// of them repeats the call's identical usage, so a call that thought, spoke
// and called a tool would be counted three times.
//
// It is a set rather than the last id alone because nothing promises a
// message's lines are contiguous: parallel subagents write into one log, and
// on the logs to hand their lines happen not to interleave, which is a
// property of a writer rather than of the format. A fixed ring rather than a
// growing set because a day-long run's message ids are unbounded and this is
// a board that stays up. Past its size the oldest id is forgotten and its
// next line bills again — one call over, on a fan-out wider than any seen.
const countedMessages = 32

type claudeLine struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	SessionID       string `json:"session_id"`
	Model           string `json:"model"`
	Timestamp       string `json:"timestamp"`
	EstimatedTokens int    `json:"estimated_tokens"`
	NumTurns        int    `json:"num_turns"`
	DurationMS      int64  `json:"duration_ms"`
	IsError         bool   `json:"is_error"`
	// TotalCostUSD is the run's own running total, in dollars, as the result
	// line reports it. No other line carries a cost of any kind — the stream
	// settles it only here — so unlike Usage it needs no guard against being
	// billed twice.
	TotalCostUSD float64 `json:"total_cost_usd"`
	// ParentToolUseID names the subagent an assistant or user line belongs
	// to — the tool_use_id of the Task call that started it — empty for the
	// top-level agent. ToolUseID and Status belong to a "task_notification"
	// system line: ToolUseID is the same id, and Status "completed" is what
	// retires that agent's entry once it is done.
	ParentToolUseID string `json:"parent_tool_use_id"`
	ToolUseID       string `json:"tool_use_id"`
	Status          string `json:"status"`
	Message         struct {
		ID      string        `json:"id"`
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

// inputSide is what the call's context window holds: everything it read
// going in, output excluded — the number "how full is the agent" is asking
// for, where total() (what the run is billed for) counts output too.
func (u claudeUsage) inputSide() int {
	return u.InputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

type claudeBlock struct {
	Type     string                     `json:"type"`
	ID       string                     `json:"id"`
	Text     string                     `json:"text"`
	Thinking string                     `json:"thinking"`
	Name     string                     `json:"name"`
	Input    map[string]json.RawMessage `json:"input"`
	Content  json.RawMessage            `json:"content"`
	IsError  bool                       `json:"is_error"`
}

func (c *claude) Decode(line string) (Event, bool) {
	var l claudeLine
	if json.Unmarshal([]byte(line), &l) != nil {
		return Event{}, false
	}
	switch l.Type {
	case "system":
		switch l.Subtype {
		case "init":
			return Event{Kind: KindInit, Text: runHeader(l.Model, l.SessionID), Model: l.Model}, true
		case "thinking_tokens":
			// The stream's heartbeat: a running count for the thinking block
			// in progress, restarting at each one.
			return Event{Kind: KindThinking, Tokens: l.EstimatedTokens}, true
		case "task_notification":
			// A finished subagent's high-water mark must not haunt the row
			// once it is gone: retire its entry so the worst-of figure falls
			// back to whatever is still running. The lowered figure appears
			// on the next assistant line rather than instantly, which is the
			// calm rule, not a gap. ToolUseID != "" guards the one id that
			// must never be retired this way: "" is the top-level agent's
			// own key, and a malformed or unrecognized completion line —
			// one naming no subagent at all — must not be read as its.
			if l.Status == "completed" && l.ToolUseID != "" {
				c.retire(l.ToolUseID)
			}
		}
	case "assistant", "user":
		if l.Type == "assistant" {
			// Every assistant line repeats its call's usage, so tracking it
			// here even for a line that renders nothing keeps the reading
			// current without waiting for one that does.
			c.track(l.ParentToolUseID, l.Message.Usage.inputSide())
			for _, b := range l.Message.Content {
				if b.Type == "tool_use" && b.ID != "" {
					c.recordAgent(b.ID, b.Input)
				}
			}
		}
		// The stream emits one content block per line; a line carrying
		// several renders the first block it knows, which is the one the
		// operator would have read first anyway.
		for _, b := range l.Message.Content {
			if ev, ok := block(b); ok {
				// The usage belongs to the call, not to the block that came
				// back from it: an assistant line reports what that call
				// spent whatever it chose to say. A user line — a tool
				// result — reports none, so this is zero there.
				ev.Usage = c.usage(l.Message.ID, l.Message.Usage)
				ev.Context = c.contextMax()
				ev.Time = l.time()
				ev.Agent = l.ParentToolUseID
				ev.AgentName = c.name(l.ParentToolUseID)
				return ev, true
			}
		}
	case "result":
		// The result line carries the run's own total, which is the sum this
		// stream has been keeping all along. Reporting it as usage would
		// count the whole run twice at the end of it. Cost is different: the
		// stream never reports it anywhere else, so there is nothing to
		// double here — this is the one and only place a claude run's dollar
		// figure becomes knowable at all.
		return Event{Kind: KindResult, Text: resultLine(l), IsError: l.IsError, Time: l.time(),
			Cost: l.TotalCostUSD}, true
	}
	return Event{}, false
}

// usage is what this line adds to the run's total: what the call spent the
// first time one of its lines is decoded, and zero on the rest of them. It is
// called only for a line that is about to be reported, so a line dropped for
// having nothing to show leaves its call's usage for the next line of the
// same message to carry.
//
// A line naming no message is counted as it comes. Nothing identifies it as a
// repeat, and undercounting a real call is the worse of the two errors.
func (c *claude) usage(id string, u claudeUsage) int {
	total := u.total()
	if total == 0 || id == "" {
		return total
	}
	for _, seen := range c.counted {
		if seen == id {
			return 0
		}
	}
	c.counted[c.next] = id
	c.next = (c.next + 1) % len(c.counted)
	return total
}

// track records agent's latest input-side reading, evicting the oldest live
// agent once trackedAgents are tracked at the same time. A repeat of an
// agent already tracked only updates its value and does not move it in
// agentOrder — eviction order is insertion order among the currently live,
// not recency of use, the same as counted's ring. Evicting is reserved for
// a run with that many agents genuinely in flight at once: a run whose
// subagents come and go one at a time, however many over its life, never
// grows past a couple of live entries, so the top-level agent's own entry —
// alive for the whole run — is never the one squeezed out to make room.
func (c *claude) track(agent string, tokens int) {
	if c.agents == nil {
		c.agents = make(map[string]int, trackedAgents)
	}
	if _, seen := c.agents[agent]; !seen {
		if len(c.agentOrder) >= trackedAgents {
			oldest := c.agentOrder[0]
			c.agentOrder = c.agentOrder[1:]
			delete(c.agents, oldest)
			delete(c.names, oldest)
		}
		c.agentOrder = append(c.agentOrder, agent)
	}
	c.agents[agent] = tokens
}

// retire drops agent from the live set entirely — from the map and from
// agentOrder — rather than merely deleting it from the map. Leaving its
// name sitting in agentOrder would hold its place in line for nothing: the
// slot could only ever be freed by outliving trackedAgents more distinct
// agents, never by the completion that already said it was done.
func (c *claude) retire(agent string) {
	delete(c.names, agent)
	if _, live := c.agents[agent]; !live {
		return
	}
	delete(c.agents, agent)
	for i, id := range c.agentOrder {
		if id == agent {
			c.agentOrder = append(c.agentOrder[:i], c.agentOrder[i+1:]...)
			break
		}
	}
}

// recordAgent records a subagent's name from a tool_use block whose input
// contains a subagent_type key.
func (c *claude) recordAgent(id string, input map[string]json.RawMessage) {
	if id == "" || input == nil {
		return
	}
	if _, ok := input["subagent_type"]; !ok {
		return
	}
	if c.names == nil {
		c.names = make(map[string]string, trackedAgents)
	}
	name, _ := inputString(input, "subagent_type")
	if name == "" {
		if desc, ok := inputString(input, "description"); ok {
			name = short(desc, maxTarget)
		}
	}
	if name == "" {
		c.agentSeq++
		name = fmt.Sprintf("agent %d", c.agentSeq)
	}
	c.names[id] = name
}

// name returns the human-readable name for the given subagent ID, assigning
// an ordinal fallback ("agent 1", "agent 2", ...) if the starting block was
// never seen.
func (c *claude) name(id string) string {
	if id == "" {
		return ""
	}
	if name, ok := c.names[id]; ok && name != "" {
		return name
	}
	if c.names == nil {
		c.names = make(map[string]string, trackedAgents)
	}
	c.agentSeq++
	name := fmt.Sprintf("agent %d", c.agentSeq)
	c.names[id] = name
	return name
}

// contextMax is the worst live agent's latest reading — a drowning subagent
// must not hide behind a healthy top-level agent.
func (c *claude) contextMax() int {
	max := 0
	for _, tokens := range c.agents {
		if tokens > max {
			max = tokens
		}
	}
	return max
}

// time is when the stream says the line was written. The system lines carry
// no timestamp and a malformed one is no time at all — zero, which readers
// treat as "the runner does not say".
func (l claudeLine) time() time.Time {
	t, err := time.Parse(time.RFC3339, l.Timestamp)
	if err != nil {
		return time.Time{}
	}
	return t
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
