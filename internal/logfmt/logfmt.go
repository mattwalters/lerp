package logfmt

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind is what an agent did, normalized across runners. The set is
// deliberately the intersection of what agent CLIs emit, not the union: a
// kind here must be something every decoder can plausibly fill, because a
// board that renders one vendor's specialties is a board that reads
// differently per vendor.
type Kind int

const (
	KindNone Kind = iota
	KindInit
	KindThinking
	KindText
	KindToolCall
	KindToolResult
	KindResult
)

// Event is one line of a runner's log, normalized. Fields not relevant to a
// Kind are zero:
//
//   - KindInit: Text names the run — model and session, when the runner says.
//   - KindThinking: Tokens is the running count for the thinking stretch in
//     progress, zero for a runner that does not stream one.
//   - KindText: Text is the agent's prose, which may hold newlines.
//   - KindToolCall: Tool is the tool's name, Text a short target.
//   - KindToolResult: Text is the head of the result, IsError its verdict.
//   - KindResult: Text is the run's one-line summary.
//
// Usage is the exception: it is not a kind's field but the line's, and it
// rides whatever kind the line decoded to.
type Event struct {
	Kind Kind
	Text string
	Tool string
	// Tokens is the thinking heartbeat's running count (KindThinking only).
	Tokens int
	// Usage is the tokens the runner says the API call behind this line
	// spent — every kind of token it bills for, since that is the number
	// "how much has this run used" is asking for. It is a delta, not a
	// total: a reader sums it over the stream, and a runner that writes one
	// call as several lines reports it on the first of them and zero on the
	// rest, so that sum is per call rather than per line. Zero on a line
	// that reports no usage, which is most of them, and on a runner that
	// reports none at all. A line whose content decodes to nothing worth
	// showing reports no usage either: where the call has further lines they
	// still carry it, and where it has none — a single-line runner, or a
	// call whose every line decoded to nothing — one call's worth is lost,
	// which the next line's arrival makes invisible.
	Usage int
	// Cost is what the runner's own stream says this line's call cost in
	// dollars. Lerp carries no price table — model prices churn and a wrong
	// one reads as confident — so a vendor whose stream is silent on cost
	// stays silent here too, forever, rather than being priced from Usage.
	//
	// Like Usage, a reader sums it over the stream, so it is a decoder's job
	// to hand back a delta, zero after the first line of a call that repeats
	// itself the way Usage's doc describes — never the running total a
	// stream may carry instead. A decoder that reports a stream's cumulative
	// figure as-is on every line it appears on inflates every run behind it.
	// claude's decoder gets this for free rather than by discipline: its
	// stream states cost on exactly one line for the whole run, so treating
	// that one figure as the delta is correct without any bookkeeping to get
	// wrong — see claude.go's result case.
	Cost float64
	// Context is how full the fullest agent in the run is as of this line, in
	// input-side tokens (input + cache creation + cache read) — the reading a
	// configured window turns into a percentage. Like Usage it is the
	// line's rather than a kind's, but it is a latest value, not a delta: a
	// reader takes the newest one rather than summing them. Zero when the
	// runner does not say — a vendor whose stream carries no per-call
	// input-side figure at all, or any line before the first one that does.
	Context int
	IsError bool
	// Time is when the runner says the line was written, zero for a runner
	// that does not date its lines. Like Usage it is the line's rather than
	// a kind's, and it is what lets a reader that attached late put the
	// events it is catching up on where they actually happened.
	Time time.Time
	// Model is the model name a runner's init line named, KindInit only.
	// Empty for a runner that does not say.
	Model string
}

// Decoder turns one line of a runner's log into an event. A line a decoder
// does not recognize is dropped (ok false) rather than rendered as itself: a
// formatted pane with occasional JSON lines in it is worse than either pure
// form.
//
// A decoder may carry state across the lines of one stream — claude does, to
// bill an API call once — so detect hands every Stream its own. Sharing one
// between streams would let the log a lane is being read by twice decide what
// the other read sees.
type Decoder interface {
	Decode(line string) (Event, bool)
}

// maxLine bounds the buffer held for a line still being written. Agent
// streams put whole tool results on one line, so lines are legitimately
// large; a line larger than this is not something the pane could show, and
// buffering it without limit would be the only unbounded thing here.
const maxLine = 1 << 20

// Stream decodes a runner's byte stream incrementally: bytes in, events out,
// with the trailing partial line held back until it is finished. The final
// line of a live log is routinely half-written, and decoding it as-is would
// produce garbage.
//
// The format is sniffed, never configured — the runner command already
// implies the vendor, and a config key would be one more thing to
// misconfigure. Nothing gates on the outcome: an unrecognized stream decodes
// as raw text, which is the floor.
type Stream struct {
	dec     Decoder
	pending []byte   // a line the runner has not finished writing
	held    []string // unclaimed JSON events, kept back while sniffing
	seen    int      // lines read while sniffing
	frozen  bool     // the format is settled; no line changes it now
	skip    bool     // dropping the rest of a line: attached mid-line, or oversized
}

// sniffWindow is how many lines the format stays open to question for. It is
// not the first line, because a real stream rarely opens with the line that
// names it: Claude Code leads with a rate-limit notice, and Codex writes a
// line to standard error — into the same log — before its first event.
const sniffWindow = 8

// SkipLine drops bytes up to the next newline. A board that attaches to a log
// already being written starts mid-line, and half a line decodes as neither
// its format nor plain text — it would only ever be garbage on the pane or a
// vote for the wrong format.
func (s *Stream) SkipLine() {
	s.skip = true
}

// Feed decodes whatever the tail just read. Bytes that do not complete a line
// are held for the next call.
func (s *Stream) Feed(p []byte) []Event {
	var events []Event
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.hold(p)
			return events
		}
		line := p[:i]
		p = p[i+1:]
		if s.skip {
			s.skip = false
			continue
		}
		full := string(line)
		if len(s.pending) > 0 {
			full = string(s.pending) + full
			s.pending = s.pending[:0]
		}
		events = append(events, s.decode(full)...)
	}
	return events
}

// Pending is the line the runner has started but not finished writing. It is
// only meaningful to a caller rendering a raw stream, where showing a
// half-written line as it arrives is the behaviour operators already have.
func (s *Stream) Pending() string {
	if s.skip {
		return ""
	}
	return string(s.pending)
}

// Raw reports whether the stream is being read as plain text — either for
// good, or provisionally while the format is still open to question.
func (s *Stream) Raw() bool {
	_, ok := s.dec.(raw)
	return ok
}

func (s *Stream) hold(p []byte) {
	if s.skip {
		return
	}
	if len(s.pending)+len(p) > maxLine {
		s.skip, s.pending = true, s.pending[:0]
		return
	}
	s.pending = append(s.pending, p...)
}

// decode runs one complete line through the chosen decoder, sniffing for one
// while the format is still open to question.
func (s *Stream) decode(line string) []Event {
	if !s.frozen {
		return s.sniff(line)
	}
	if ev, ok := s.dec.Decode(line); ok {
		return []Event{ev}
	}
	return nil
}

// sniff reads a line while the format is still in question.
//
// A line carrying a type a decoder knows names the stream, and that is the
// end of it. Anything that is not a JSON event at all is text, and text goes
// straight to the pane — a runner that writes one line and falls quiet must
// not be held waiting on a format that is not coming — but the window stays
// open behind it, so a banner on standard error cannot cost a runner its
// decoder. What is held is the awkward middle: JSON events nobody claims,
// which would be raw JSON on screen if they were shown and may be sitting in
// front of the line that names the format.
//
// The window closes after sniffWindow lines and the choice is final. Nothing
// re-opens it: a stream does not change format halfway down the pane. A log
// that ends while events are still held never shows them — at most a handful
// of lines, and the raw toggle still has every one of them.
func (s *Stream) sniff(line string) []Event {
	s.seen++
	d, event := detect(line)
	switch {
	case d != nil:
		// The stream named itself. Whatever came before it was noise.
		s.dec, s.held, s.frozen = d, nil, true
	case event:
		s.held = append(s.held, line)
		line = "" // nothing to show for it yet, and maybe never
	default:
		s.dec = raw{}
	}

	var lines []string
	if s.seen >= sniffWindow && !s.frozen {
		// The window closed with no decoder: the held events were text all
		// along, and they go on the pane ahead of the line that closed it.
		if s.dec == nil {
			s.dec = raw{}
		}
		s.frozen, lines, s.held = true, s.held, nil
	}
	if line != "" {
		lines = append(lines, line)
	}
	var events []Event
	for _, l := range lines {
		if ev, ok := s.dec.Decode(l); ok {
			events = append(events, ev)
		}
	}
	return events
}

// detect reads one line for the decoder it implies, and reports separately
// whether the line is a JSON event at all — a stream of events nobody claims
// is still a stream, and worth waiting a few lines on. Adding a vendor is one
// file plus one case here.
//
// Two probe fields, not one: claude and codex both discriminate on `type`,
// but agy discriminates on `event` — a line naming neither, or naming one
// with a value no case here knows, is still a JSON event and falls to the
// event-but-unclaimed branch below rather than being read as plain text.
func detect(line string) (Decoder, bool) {
	if !strings.HasPrefix(strings.TrimSpace(line), "{") {
		return nil, false
	}
	var probe struct {
		Type string `json:"type"`
		// Event is raw rather than string: claude's own --include-partial-
		// messages stream carries a *nested object* under this same key
		// ({"type":"stream_event","event":{...}}), and unmarshalling that into
		// a string field would fail the whole probe — misreading a real
		// claude line as not a JSON event at all, rather than one this
		// switch simply does not claim.
		Event json.RawMessage `json:"event"`
	}
	if json.Unmarshal([]byte(line), &probe) != nil {
		return nil, false
	}
	var event string
	json.Unmarshal(probe.Event, &event) // leaves event empty for a nested object, which is fine below
	if probe.Type == "" && event == "" {
		return nil, false
	}
	switch probe.Type {
	case "system", "assistant", "user", "result":
		return &claude{}, true
	case "thread.started", "turn.started", "turn.completed", "item.started", "item.completed":
		return codex{}, true
	}
	switch event {
	case "init", "step_update", "result":
		return newAntigravity(), true
	}
	return nil, true
}

// short flattens a value to the single line an event carries: the first line
// of it, truncated. Everything the board shows for a tool call or a tool
// result is one line, so the trimming belongs here rather than in every
// caller.
func short(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	cut := 0
	for i := range s {
		if cut == n-1 {
			return s[:i] + "…"
		}
		cut++
	}
	return s
}

const (
	// maxTarget bounds a tool call's target, maxResult a tool result's head.
	maxTarget = 80
	maxResult = 120
)

// sessionTag shortens a session or thread UUID to the prefix a human uses to
// tell two runs apart.
func sessionTag(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	return short(id, 8)
}
