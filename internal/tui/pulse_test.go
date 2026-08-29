package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newest is the bucket now filling, which is where a poll's events land.
func newest(t *testing.T, p *pulse) int {
	t.Helper()
	w := p.window()
	if len(w) == 0 {
		t.Fatal("the window has no buckets at all")
	}
	return w[len(w)-1]
}

func appendLog(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// The count is of decoded events, not bytes: a runner logfmt understands is
// counted by what its agent did, and one it does not is counted by lines.
func TestPulseCountsDecodedEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, `{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n")

	now := time.Now()
	p := newPulse(path)
	p.read(now)
	// Three lines, one of them a tool result the pane draws under its call:
	// three events, because three things happened.
	appendLog(t, path,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a/b.go"}}]}}`+"\n"+
			`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`+"\n")
	p.read(now)

	got := p.window()
	if got[len(got)-1] != 3 {
		t.Fatalf("newest bucket = %d, want 3: %v", got[len(got)-1], got)
	}
	for _, c := range got[:len(got)-1] {
		if c != 0 {
			t.Fatalf("activity landed outside the bucket it arrived in: %v", got)
		}
	}
}

// A half-written line is not an event yet. The pane holds it back and so does
// the count, or a burst would be counted once as a fragment and again whole.
func TestPulseWaitsForAWholeLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "first line\n")
	now := time.Now()
	p := newPulse(path)
	p.read(now)

	appendLog(t, path, "half a ")
	p.read(now)
	if got := newest(t, p); got != 0 {
		t.Fatalf("a half-written line counted as %d events", got)
	}
	appendLog(t, path, "line\n")
	p.read(now)
	if got := newest(t, p); got != 1 {
		t.Fatalf("the finished line counted as %d events, want 1", got)
	}
}

// History a log does not date has no place in the ring: those lines happened
// at times nobody recorded, and dating them to now would draw a burst that
// never happened. The line stays as short as a fresh run's — a shape nobody
// measured is not drawn — while the totals still carry what the lines say.
func TestPulseLeavesUndatedHistoryOffTheLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, strings.Repeat("an hour of work\n", 200))

	now := time.Now()
	p := newPulse(path)
	p.read(now)
	got := p.window()
	if len(got) != 1 {
		t.Fatalf("undated history stretched the line to %d buckets, want 1: %v", len(got), got)
	}
	if got[0] != 0 {
		t.Fatalf("history nobody dated was counted as activity now: %v", got)
	}
}

// Time moves the ring, not just bytes: a run that falls quiet flattens its
// own line while nothing at all is being read.
func TestPulseQuietGoesFlat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "x\n")
	start := time.Now()
	p := newPulse(path)
	p.read(start)
	appendLog(t, path, "busy\nbusy\n")
	p.read(start)

	p.read(start.Add(2 * pulseBucket))
	got := p.window()
	if len(got) != 3 {
		t.Fatalf("window = %v, want the three buckets that have existed", got)
	}
	if got[1] != 0 || got[2] != 0 {
		t.Fatalf("two quiet buckets still show activity: %v", got)
	}
	if got[0] == 0 {
		t.Fatalf("the busy bucket did not slide back with the clock: %v", got)
	}
	if bars := sparkline(got); !strings.HasSuffix(bars, "▁▁") {
		t.Fatalf("a quiet stretch does not read flat: %q", bars)
	}

	// Quiet for longer than the whole window leaves nothing to draw.
	p.read(start.Add(time.Hour))
	if bars := sparkline(p.window()); bars != strings.Repeat("▁", pulseBuckets) {
		t.Fatalf("a long-dead run does not read flat: %q", bars)
	}
}

// Last-heard-from is the file's own mtime, so it is right on the first poll —
// including for a run adopted from a previous process, where waiting for a
// byte of our own could mean waiting forever.
func TestPulseHeardIsTheFilesOwnTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "wrote this a while ago\n")
	old := time.Now().Add(-7 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p := newPulse(path)
	p.read(now)
	if p.heard.IsZero() || p.heard.Sub(old).Abs() > time.Second {
		t.Fatalf("heard = %v, want the file's mtime %v", p.heard, old)
	}
}

// A log that is not there yet is not a run that has gone quiet: the row shows
// no reading rather than a made-up one.
func TestPulseWithoutAFileHasNothingToSay(t *testing.T) {
	now := time.Now()
	p := newPulse(filepath.Join(t.TempDir(), "not-yet"))
	p.read(now)
	p.read(now.Add(time.Minute))
	if !p.heard.IsZero() {
		t.Fatalf("a missing log claimed it was last heard at %v", p.heard)
	}
	if bars := sparkline(p.window()); bars != "" {
		t.Fatalf("a log that does not exist drew a line: %q", bars)
	}
}

// A log deleted under a live agent — which invariant 1 says may cost compute,
// never correctness — is not an agent that has gone quiet. The row stops
// reporting rather than freezing the last time the file was there.
func TestPulseStopsReadingALogThatVanished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "working\n")
	now := time.Now()
	p := newPulse(path)
	p.read(now)
	if p.heard.IsZero() {
		t.Fatal("a log that is there reports no time")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	p.read(now)
	if !p.heard.IsZero() {
		t.Fatalf("a deleted log still reports %v as when it last spoke", p.heard)
	}
	if bars := sparkline(p.window()); bars != "" {
		t.Fatalf("a deleted log still draws a line: %q", bars)
	}
}

// A run picked up seconds ago has no history to draw, and a long flat line is
// the picture of one that died. The window grows with the run instead.
func TestPulseWindowGrowsWithTheRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "started\n")
	start := time.Now()
	p := newPulse(path)
	p.read(start)
	if got := len(p.window()); got != 1 {
		t.Fatalf("a run one poll old draws %d buckets, want 1", got)
	}
	p.read(start.Add(3 * pulseBucket))
	if got := len(p.window()); got != 4 {
		t.Fatalf("a run four buckets old draws %d, want 4", got)
	}
	p.read(start.Add(time.Hour))
	if got := len(p.window()); got != pulseBuckets {
		t.Fatalf("an old run draws %d buckets, want the whole window of %d", got, pulseBuckets)
	}
}

// datedCall is one Claude-stream tool call, dated the way the real stream
// dates its lines, spending what the test says it spent.
func datedCall(ts time.Time, id, file string, usage int) string {
	return `{"type":"assistant","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) + `",` +
		`"message":{"id":"` + id + `","content":[{"type":"tool_use","name":"Read","input":{"file_path":"` + file + `"}}],` +
		`"usage":{"input_tokens":` + strconv.Itoa(usage) + `,"output_tokens":0}}}` + "\n"
}

// Done-when: a run adopted from a previous process draws the history its log
// records rather than the short line of one that just started. The stream
// dates its lines, so the events this process never watched still land in the
// buckets where they happened — and the totals are the run's own: the tokens
// its whole log was billed for, and the last call it made before the pickup.
func TestPulseRebuildsAnAdoptedRunsHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	now := time.Now()
	appendLog(t, path,
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n"+
			datedCall(now.Add(-5*time.Minute), "msg_01", "/a/old.go", 1000)+
			// An undated line between dated ones — the thinking heartbeat —
			// is read at the last dated time, which it follows closely.
			`{"type":"system","subtype":"thinking_tokens","estimated_tokens":12}`+"\n"+
			datedCall(now.Add(-2*time.Minute), "msg_02", "/a/recent.go", 500))

	p := newPulse(path)
	p.read(now)

	got := p.window()
	// The oldest event is five minutes — 100 buckets — back, and the
	// reading reaches exactly that far: no further, which would claim quiet
	// nobody measured, and no shorter, which would be the fresh line.
	if want := 101; len(got) != want {
		t.Fatalf("a run with five minutes of history draws %d buckets, want %d: %v", len(got), want, got)
	}
	if got[0] != 2 {
		t.Fatalf("the old call and its heartbeat hold %d, want 2: %v", got[0], got)
	}
	if at := len(got) - 1 - 40; got[at] != 1 {
		t.Fatalf("the recent call landed at %d, want bucket %d of %v", got[at], at, got)
	}
	if p.tokens != 1500 {
		t.Fatalf("the run's history billed %d tokens, want 1500", p.tokens)
	}
	if p.tool != "Read" || p.target != "recent.go" {
		t.Fatalf("the last call is %q %q, want Read recent.go", p.tool, p.target)
	}
}

// History older than the ring still stretches the line: an adopted run whose
// log went quiet twenty minutes ago draws the long flat line of a run that
// has been quiet, not the short line of one that just started — the shape is
// the reading, and it is the one thing a restart used to lose.
func TestPulseHistoryBeyondTheRingStretchesTheLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	now := time.Now()
	appendLog(t, path, datedCall(now.Add(-20*time.Minute), "msg_01", "/a/b.go", 700))

	p := newPulse(path)
	p.read(now)
	got := p.window()
	if len(got) != pulseBuckets {
		t.Fatalf("a run quiet since before the ring draws %d buckets, want %d", len(got), pulseBuckets)
	}
	for i, c := range got {
		if c != 0 {
			t.Fatalf("an event from before the ring was counted into bucket %d: %v", i, got)
		}
	}
	if p.tokens != 700 || p.tool != "Read" {
		t.Fatalf("history beyond the ring lost the totals: %d tokens, call %q", p.tokens, p.tool)
	}
}

// Adoption catches up in one poll, not one chunk: a log bigger than a single
// read hands back is swallowed by the same read call, so the row never spends
// its first seconds drawing half a history.
func TestPulseCatchesUpOnABigLogInOnePoll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	now := time.Now()
	var b strings.Builder
	const lines, each = 2000, 10
	for i := 0; i < lines; i++ {
		back := time.Duration(lines-i) * (5 * time.Minute) / lines
		b.WriteString(datedCall(now.Add(-back), "msg_"+strconv.Itoa(i), "/a/b.go", each))
	}
	if int64(b.Len()) <= tailChunk {
		t.Fatalf("the log is %d bytes, no bigger than one %d-byte chunk", b.Len(), tailChunk)
	}
	appendLog(t, path, b.String())

	p := newPulse(path)
	p.read(now)
	if p.tokens != lines*each {
		t.Fatalf("one poll read %d tokens of history, want all %d", p.tokens, lines*each)
	}
}

func TestSparkline(t *testing.T) {
	tests := []struct {
		name   string
		counts []int
		want   string
	}{
		{"nothing at all", []int{0, 0, 0}, "▁▁▁"},
		{"one empty bucket", []int{0}, "▁"},
		{"the busiest bucket tops the ramp", []int{0, 4, 0}, "▁█▁"},
		// The scale is the row's own busiest bucket, so a steady rate reads
		// as a level line: the shape is the signal, and bar heights are not
		// comparable between one row and the next.
		{"a steady rate reads level", []int{4, 4, 4}, "███"},
		{"the rest scale under the peak", []int{1, 5, 9}, "▂▄█"},
		{"one event never reads as none", []int{1, 0, 20}, "▂▁█"},
		{"no window, no line", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sparkline(tc.counts); got != tc.want {
				t.Fatalf("sparkline(%v) = %q, want %q", tc.counts, got, tc.want)
			}
		})
	}
}

// A log rewritten in place is a new log. Folding what was already in it into
// the bucket now filling would draw one spike and flatten every real bucket
// beside it, since the bars scale to the window's own tallest.
func TestPulseStartsOverWhenTheLogIsRewritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "one\ntwo\nthree\n")
	start := time.Now()
	p := newPulse(path)
	p.read(start)
	appendLog(t, path, "four\nfive\n")
	p.read(start.Add(pulseBucket))
	if got := len(p.window()); got != 2 {
		t.Fatalf("window is %d buckets before the rewrite, want 2", got)
	}

	writeLog(t, path, []byte("a wholly new log\n"))
	p.read(start.Add(2 * pulseBucket))
	got := p.window()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("the rewritten log carries the old ring: %v", got)
	}
}

// The row's two other readings come off the same events the buckets are
// counted from: what the run has spent, summed as the log reports it, and the
// last call it made, which stays until another one replaces it. A run does
// not stop having run a command while it thinks about the output.
func TestPulseTracksSpendAndTheLastCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, `{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n")
	now := time.Now()
	p := newPulse(path)
	p.read(now)
	if p.tokens != 0 || p.tool != "" {
		t.Fatalf("a run that has done nothing reports %d tokens and %q", p.tokens, p.tool)
	}

	appendLog(t, path,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a/b.go"}}],`+
			`"usage":{"input_tokens":100,"output_tokens":900}}}`+"\n")
	p.read(now)
	if p.tokens != 1000 {
		t.Fatalf("the first call spent %d tokens, want 1000", p.tokens)
	}
	if p.tool != "Read" || p.target != "b.go" {
		t.Fatalf("the last call is %q %q, want Read b.go", p.tool, p.target)
	}

	// Prose costs tokens and is not a call: the total moves, the call does
	// not.
	appendLog(t, path,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"that file is fine"}],`+
			`"usage":{"input_tokens":500,"output_tokens":10,"cache_read_input_tokens":1490}}}`+"\n")
	p.read(now)
	if p.tokens != 3000 {
		t.Fatalf("the run has spent %d tokens, want 3000", p.tokens)
	}
	if p.tool != "Read" {
		t.Fatalf("prose replaced the last call with %q", p.tool)
	}

	// A rewritten log takes both with it: they are readings of a file that
	// is gone, and the command on screen would be one nobody can look up.
	writeLog(t, path, []byte("a wholly new log\n"))
	p.read(now.Add(pulseBucket))
	if p.tokens != 0 || p.tool != "" || p.target != "" {
		t.Fatalf("the rewritten log kept %d tokens and the call %q %q", p.tokens, p.tool, p.target)
	}
}

// Cost only ever arrives on claude's result line, once, at the very end of
// the run — nothing mid-run reports it — so a live pulse shows nothing in
// dollars until that line lands, and then the run's whole figure at once.
// That line is also the one that ends the log, so a poll landing between it
// and the run settling is the only chance the row itself ever gets to draw
// the figure. The operator's second chance does not go through pulse at all:
// internal/loop's runCost reads the same log independently, before the run's
// evidence (log included) is discarded, and the result rides loop.Event.Cost
// onto the exit note that apply (model.go) builds on EventExited.
func TestPulseTracksCostFromTheResultLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, `{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n")
	now := time.Now()
	p := newPulse(path)
	p.read(now)
	if p.cost != 0 {
		t.Fatalf("a run with no result line reports $%.2f, want 0", p.cost)
	}

	appendLog(t, path,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}],`+
			`"usage":{"input_tokens":100,"output_tokens":100}}}`+"\n")
	p.read(now)
	if p.cost != 0 {
		t.Fatalf("a mid-run line reports $%.2f, want 0 until the result line", p.cost)
	}

	appendLog(t, path,
		`{"type":"result","subtype":"success","num_turns":1,"total_cost_usd":0.42}`+"\n")
	p.read(now)
	if p.cost != 0.42 {
		t.Fatalf("the result line's cost read as $%.2f, want $0.42", p.cost)
	}

	// A rewritten log loses the figure with everything else it read: it is a
	// reading of a file that is gone.
	writeLog(t, path, []byte("a wholly new log\n"))
	p.read(now.Add(pulseBucket))
	if p.cost != 0 {
		t.Fatalf("the rewritten log kept a cost of $%.2f", p.cost)
	}
}

// A runner whose stream never states a cost — codex, today — must never
// have one invented for it from its token usage: the pulse just keeps
// summing zero.
func TestPulseReportsNoCostForARunnerThatDoesNotStateOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	now := time.Now()
	p := newPulse(path)
	appendLog(t, path,
		`{"type":"turn.completed","usage":{"input_tokens":31101,"cached_input_tokens":26112,"output_tokens":119}}`+"\n")
	p.read(now)
	if p.cost != 0 {
		t.Fatalf("codex usage priced itself at $%.2f, want 0", p.cost)
	}
	if p.tokens == 0 {
		t.Fatal("the token total did not move, so the fixture is not exercising the decoder")
	}
}

// The row's figure is a sum of what the log reports, so a runner that repeats
// one call's usage on every line of it inflates the row directly: Claude Code
// writes a content block per line, and a thinking-then-tool call read 3x.
func TestPulseBillsOneCallOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, `{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n")
	now := time.Now()
	p := newPulse(path)
	p.read(now) // consume what is already there as history

	// Three lines of one call, each repeating the same 1,000-token usage,
	// and read a poll apart: a live board reads a message's blocks as they
	// are written, not all at once, so the count has to survive the gap.
	const usage = `"usage":{"input_tokens":100,"output_tokens":900}`
	appendLog(t, path,
		`{"type":"assistant","message":{"id":"msg_01","content":[{"type":"thinking","thinking":"weighing it"}],`+usage+`}}`+"\n"+
			`{"type":"assistant","message":{"id":"msg_01","content":[{"type":"text","text":"reading it"}],`+usage+`}}`+"\n")
	p.read(now)
	appendLog(t, path,
		`{"type":"assistant","message":{"id":"msg_01","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a/b.go"}}],`+usage+`}}`+"\n")
	p.read(now.Add(pulseBucket))
	if p.tokens != 1000 {
		t.Fatalf("one call across three lines billed %d tokens, want 1000", p.tokens)
	}
	if p.tool != "Read" || p.target != "b.go" {
		t.Fatalf("the last call is %q %q, want Read b.go", p.tool, p.target)
	}
}

// context is a reading, not a sum like tokens and cost: it survives the
// history/live boundary as whatever the log's latest line said, replaced by
// a later one rather than accumulated, and it is lost with everything else
// when the log is rewritten under the pulse.
func TestPulseTracksContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}],`+
			`"usage":{"input_tokens":1000,"output_tokens":10}}}`+"\n")
	now := time.Now()
	p := newPulse(path)
	p.read(now) // history: the line above predates this pulse's attach
	if p.context != 1000 {
		t.Fatalf("history reported context %d, want 1000", p.context)
	}

	appendLog(t, path,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"still working"}],`+
			`"usage":{"input_tokens":40000,"output_tokens":10}}}`+"\n")
	p.read(now)
	if p.context != 40000 {
		t.Fatalf("a live line reported context %d, want 40000", p.context)
	}

	// A rewritten log takes it with everything else: it is a reading of a
	// file that is gone.
	writeLog(t, path, []byte("a wholly new log\n"))
	p.read(now.Add(pulseBucket))
	if p.context != 0 {
		t.Fatalf("the rewritten log kept a context of %d", p.context)
	}
}

// model is taken from the init line, and cleared with everything else when
// the log is rewritten under the pulse.
func TestPulseTracksModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, `{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n")
	now := time.Now()
	p := newPulse(path)
	p.read(now)
	if p.model != "claude-opus-5" {
		t.Fatalf("model = %q, want claude-opus-5", p.model)
	}

	writeLog(t, path, []byte("a wholly new log\n"))
	p.read(now.Add(pulseBucket))
	if p.model != "" {
		t.Fatalf("the rewritten log kept a model of %q", p.model)
	}
}

func TestDownsample(t *testing.T) {
	tests := []struct {
		name   string
		counts []int
		want   []int
	}{
		{"empty", nil, nil},
		{"single fine bucket", []int{3}, []int{3}},
		{"five fine buckets", []int{1, 2, 3, 4, 5}, []int{15}},
		{"six fine buckets", []int{10, 1, 2, 3, 4, 5}, []int{10, 15}},
		{"eight fine buckets", []int{1, 0, 3, 0, 9, 0, 0, 0}, []int{4, 9}},
		{"ten fine buckets", []int{1, 1, 1, 1, 1, 2, 2, 2, 2, 2}, []int{5, 10}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := downsample(tc.counts)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("downsample(%v) = %v, want %v", tc.counts, got, tc.want)
			}
		})
	}
}

// timedWindow dates each bucket relative to p.at.
func TestPulseTimedWindowDatesBuckets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "started\n")
	start := time.Now().Truncate(time.Second)
	p := newPulse(path)
	p.read(start)

	p.read(start.Add(4 * pulseBucket))
	tb := p.timedWindow()
	if len(tb) != 5 {
		t.Fatalf("timedWindow len = %d, want 5", len(tb))
	}
	for i, b := range tb {
		wantTime := p.at.Add(-time.Duration(len(tb)-1-i) * pulseBucket)
		if !b.at.Equal(wantTime) {
			t.Errorf("bucket %d at = %v, want %v", i, b.at, wantTime)
		}
	}
	if !tb[len(tb)-1].at.Equal(p.at) {
		t.Errorf("newest bucket at = %v, want %v", tb[len(tb)-1].at, p.at)
	}
}
