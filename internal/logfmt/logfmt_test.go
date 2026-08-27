package logfmt

import (
	"fmt"
	"strings"
	"testing"
)

func feed(s *Stream, chunks ...string) []Event {
	var events []Event
	for _, c := range chunks {
		events = append(events, s.Feed([]byte(c))...)
	}
	return events
}

func kinds(events []Event) []Kind {
	out := make([]Kind, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

func wantKinds(t *testing.T, events []Event, want ...Kind) {
	t.Helper()
	got := kinds(events)
	if len(got) != len(want) {
		t.Fatalf("decoded %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decoded %v, want %v", got, want)
		}
	}
}

func TestStreamSniffsClaude(t *testing.T) {
	var s Stream
	events := feed(&s, claudeInit+"\n"+claudeThinking+"\n", claudeRead+"\n"+claudeResult+"\n")
	wantKinds(t, events, KindInit, KindThinking, KindToolCall, KindResult)
	if s.Raw() {
		t.Fatal("a recognized stream reports itself as raw")
	}
}

func TestStreamSniffsCodex(t *testing.T) {
	var s Stream
	events := feed(&s, codexStart+"\n"+codexShell+"\n"+codexTurn+"\n")
	wantKinds(t, events, KindInit, KindToolCall, KindResult)
}

// agy discriminates on `event`, not `type` — the field claude and codex both
// use — so this is the regression detect's second probe field exists to
// prevent: an agy stream must still pick its decoder, and a claude or codex
// stream must still pick theirs.
func TestStreamSniffsAntigravity(t *testing.T) {
	var s Stream
	events := feed(&s, antigravityInitLine+"\n"+antigravityToolStart+"\n"+antigravityToolDone+"\n")
	wantKinds(t, events, KindInit, KindToolCall, KindToolResult)
	if s.Raw() {
		t.Fatal("a recognized antigravity stream reports itself as raw")
	}
}

func TestStreamStillSniffsClaudeAndCodexAlongsideAntigravity(t *testing.T) {
	var claudeStream Stream
	events := feed(&claudeStream, claudeInit+"\n")
	wantKinds(t, events, KindInit)
	if claudeStream.Raw() {
		t.Fatal("claude stream fell back to raw once agy's probe field was added")
	}

	var codexStream Stream
	events = feed(&codexStream, codexStart+"\n")
	wantKinds(t, events, KindInit)
	if codexStream.Raw() {
		t.Fatal("codex stream fell back to raw once agy's probe field was added")
	}
}

// An `event`-keyed line whose value no case here knows is still a JSON event,
// not text — the same rule a `type`-keyed line already gets.
func TestStreamHoldsAnUnclaimedEventValue(t *testing.T) {
	var s Stream
	lead := `{"event":"something_else","something_else":{}}`
	if events := feed(&s, lead+"\n"); len(events) != 0 {
		t.Fatalf("an undecided stream emitted %v", kinds(events))
	}
	wantKinds(t, feed(&s, antigravityInitLine+"\n"), KindInit)
	if s.Raw() {
		t.Fatal("a leading unclaimed event value made the stream give up on the format")
	}
}

// The board attaches to a log already being written, so the first bytes it
// reads are the tail of a line. Skipping to the next newline is what keeps
// that fragment from deciding the format — or from reaching the pane.
func TestStreamSkipsThePartialLineItAttachedTo(t *testing.T) {
	var s Stream
	s.SkipLine()
	events := feed(&s, `_tokens":100,"session_id":"7420e6f8"}`+"\n"+claudeRead+"\n")
	wantKinds(t, events, KindToolCall)
	if s.Raw() {
		t.Fatal("a half first line made the stream give up on the format")
	}
}

// A skip spanning reads keeps skipping: the fragment it lands in may be
// bigger than one poll's read.
func TestStreamSkipsAcrossReads(t *testing.T) {
	var s Stream
	s.SkipLine()
	if events := feed(&s, "half a line", " and more of it"); len(events) != 0 {
		t.Fatalf("the skipped line produced %v", kinds(events))
	}
	if s.Pending() != "" {
		t.Fatalf("pending = %q while skipping, want empty", s.Pending())
	}
	wantKinds(t, feed(&s, "\n"+claudeText+"\n"), KindText)
}

// A recognized stream does not always open with a line that names itself: a
// real Claude log routinely leads with a rate-limit notice. An unclaimed JSON
// event is held rather than taken as evidence of plain text.
func TestStreamSniffsPastAnUnclaimedLeadingEvent(t *testing.T) {
	var s Stream
	lead := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`
	if events := feed(&s, lead+"\n"); len(events) != 0 {
		t.Fatalf("an undecided stream emitted %v", kinds(events))
	}
	wantKinds(t, feed(&s, claudeInit+"\n"), KindInit)
	if s.Raw() {
		t.Fatal("a leading unclaimed event made the stream give up on the format")
	}
}

// The wait is bounded: a stream of events no decoder claims is text in the
// end, and the held lines are text too — nothing the runner wrote is lost.
func TestStreamGivesUpAfterAHandfulOfUnclaimedEvents(t *testing.T) {
	var s Stream
	var b strings.Builder
	for i := 0; i <= sniffWindow; i++ {
		fmt.Fprintf(&b, `{"type":"something.else","n":%d}`+"\n", i)
	}
	events := feed(&s, b.String())
	if !s.Raw() {
		t.Fatal("a stream nobody claims never fell back to raw")
	}
	if len(events) != sniffWindow+1 {
		t.Fatalf("decoded %d lines, want all %d held ones as text", len(events), sniffWindow+1)
	}
	if events[0].Text != `{"type":"something.else","n":0}` {
		t.Fatalf("a held line was altered on its way out: %q", events[0].Text)
	}
}

// Raw is the floor: an unrecognized stream is every line as text, which is
// what the pane showed before any decoder existed.
func TestStreamFallsBackToRawText(t *testing.T) {
	var s Stream
	events := feed(&s, "building…\nok\tgithub.com/mattwalters/lerp\n")
	wantKinds(t, events, KindText, KindText)
	if events[0].Text != "building…" || events[1].Text != "ok\tgithub.com/mattwalters/lerp" {
		t.Fatalf("raw lines were altered: %+v", events)
	}
	if !s.Raw() {
		t.Fatal("an unrecognized stream does not report itself as raw")
	}
}

// Text goes on the pane the moment it arrives — a runner that writes one line
// and falls quiet is not left waiting on a format that is not coming — and
// the window stays open behind it, so a banner on standard error (Codex
// writes one into the same log) cannot cost a runner its decoder.
func TestStreamShowsTextWithoutClosingTheWindow(t *testing.T) {
	var s Stream
	wantKinds(t, feed(&s, "Reading additional input from stdin...\n"), KindText)
	if !s.Raw() {
		t.Fatal("a line of plain text was not treated as text")
	}
	wantKinds(t, feed(&s, codexStart+"\n"+codexShell+"\n"), KindInit, KindToolCall)
	if s.Raw() {
		t.Fatal("the stream named itself and was still read as raw")
	}
}

// Once the window closes the choice is final: a stream does not change format
// halfway down the pane.
func TestStreamNeverRetriesAClosedSniff(t *testing.T) {
	var s Stream
	var b strings.Builder
	for i := 0; i < sniffWindow; i++ {
		fmt.Fprintf(&b, "building step %d\n", i)
	}
	feed(&s, b.String())
	events := feed(&s, claudeInit+"\n")
	wantKinds(t, events, KindText)
	if !strings.HasPrefix(events[0].Text, `{"type":"system"`) {
		t.Fatalf("a settled raw stream decoded a later line: %+v", events[0])
	}
}

// The last line of a live log is routinely half-written: it is held until the
// runner finishes it, and reported as pending in the meantime.
func TestStreamHoldsAPartialFinalLine(t *testing.T) {
	var s Stream
	head, tail := claudeRead[:40], claudeRead[40:]
	if events := feed(&s, claudeInit+"\n"+head); len(events) != 1 {
		t.Fatalf("a partial line decoded early: %v", kinds(events))
	}
	if s.Pending() != head {
		t.Fatalf("pending = %q, want the partial line %q", s.Pending(), head)
	}
	events := feed(&s, tail+"\n")
	wantKinds(t, events, KindToolCall)
	if s.Pending() != "" {
		t.Fatalf("pending = %q after the line completed, want empty", s.Pending())
	}
}

// A line longer than anything the pane could show is dropped rather than
// buffered without bound; the stream picks up at the next one.
func TestStreamDropsAnOversizedLine(t *testing.T) {
	var s Stream
	feed(&s, claudeInit+"\n")
	giant := strings.Repeat("x", maxLine+1)
	if events := feed(&s, giant, "still the same line\n"); len(events) != 0 {
		t.Fatalf("an oversized line produced %v", kinds(events))
	}
	if s.Pending() != "" {
		t.Fatalf("pending = %d bytes, want the oversized line dropped", len(s.Pending()))
	}
	wantKinds(t, feed(&s, claudeResult+"\n"), KindResult)
}

func TestShortKeepsOneLine(t *testing.T) {
	if got := short("  first\nsecond  ", 40); got != "first …" {
		t.Fatalf("short = %q, want the first line marked as cut", got)
	}
	if got := short("héllo wörld", 8); got != "héllo w…" {
		t.Fatalf("short = %q, want 8 runes ending in an ellipsis", got)
	}
	if got := short("héllo", 8); got != "héllo" {
		t.Fatalf("short = %q, want it untouched", got)
	}
}
