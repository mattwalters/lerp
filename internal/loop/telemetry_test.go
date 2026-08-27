//go:build unix

package loop

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/run"
	"github.com/mattwalters/lerp/internal/workspace"
)

// A live run's exit code and the status conclude actually moved the ticket
// to both ride on the same telemetry line.
func TestTelemetryRecordsALiveRunsExitCodeAndStatus(t *testing.T) {
	h := newHarness(t, 1, nil) // default Execute: a clean exit
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventExited, 1)

	runs := h.telemetryRuns()
	if len(runs) != 1 {
		t.Fatalf("telemetry runs = %d, want exactly 1", len(runs))
	}
	got := runs[0]
	if got.Ticket != "LERP-1" || got.Team != "LERP" || got.Queue != "todo" || got.Runner != "agent" {
		t.Errorf("run = %+v, want it naming the ticket, team, queue and runner", got)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", got.ExitCode)
	}
	if got.Status != "Done" {
		t.Errorf("status = %q, want Done (the queue's on_success)", got.Status)
	}
	if got.Vendor != "" {
		t.Errorf("vendor = %q, want empty: testRepo's runner is a command template", got.Vendor)
	}
}

// A run started under runner A and settled after the config renames or
// replaces that queue's runner reports runner A in its telemetry line.
// Telemetry exists to compare vendors under real load; runner identity
// comes from the run's start-time evidence record rather than settle-time config.
func TestTelemetryRunnerStampedFromRecordNotSettleTimeConfig(t *testing.T) {
	execute, release, _ := blockingExecute(t, "")
	h := newHarness(t, 1, execute)
	h.rec.o.Repo.Runners = map[string]config.Runner{
		"runner-a": {Command: "runner-a-cmd", Vendor: "vendor-a", Model: "model-a"},
		"runner-b": {Command: "runner-b-cmd", Vendor: "vendor-b", Model: "model-b"},
	}
	h.rec.o.Repo.Queues["todo"] = config.Queue{
		Status: "Todo", Prompt: "do work", Runner: "runner-a", OnSuccess: "Done", OnFailure: "Needs Help",
	}
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	ctx := context.Background()
	h.rec.Tick(ctx)
	h.waitEvents(t, EventStarted, 1)

	// Swap the queue's runner in config before the run settles.
	h.rec.o.Repo.Queues["todo"] = config.Queue{
		Status: "Todo", Prompt: "do work", Runner: "runner-b", OnSuccess: "Done", OnFailure: "Needs Help",
	}

	release()
	h.waitEvents(t, EventExited, 1)

	runs := h.telemetryRuns()
	if len(runs) != 1 {
		t.Fatalf("telemetry runs = %d, want exactly 1", len(runs))
	}
	got := runs[0]
	if got.Runner != "runner-a" {
		t.Errorf("runner = %q, want runner-a (from start-time record)", got.Runner)
	}
	if got.Vendor != "vendor-a" {
		t.Errorf("vendor = %q, want vendor-a", got.Vendor)
	}
	if got.Model != "model-a" {
		t.Errorf("model = %q, want model-a", got.Model)
	}
}

// A record predating the Runner, Vendor, and Model fields (Runner empty)
// falls back to reading them from settle-time config.
func TestTelemetryFallsBackToConfigForRecordsPredatingRunnerFields(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.rec.o.Repo.Runners["agent"] = config.Runner{
		Command: "agent", Vendor: "anthropic", Model: "claude-sonnet",
	}
	record, err := h.evidence.Create(evidence.Record{
		Lane: 1, TicketID: "orphan", Ticket: "LERP-1", Queue: "todo", StartingStatus: "Todo",
		StartedAt: time.Now().Add(-time.Minute),
		// Runner, Vendor, Model deliberately left empty (predating fields)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record.ExitPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "orphan", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventExited, 1)

	runs := h.telemetryRuns()
	if len(runs) != 1 {
		t.Fatalf("telemetry runs = %d, want exactly 1", len(runs))
	}
	got := runs[0]
	if got.Runner != "agent" {
		t.Errorf("runner = %q, want agent (from config fallback)", got.Runner)
	}
	if got.Vendor != "anthropic" {
		t.Errorf("vendor = %q, want anthropic (from config fallback)", got.Vendor)
	}
	if got.Model != "claude-sonnet" {
		t.Errorf("model = %q, want claude-sonnet (from config fallback)", got.Model)
	}
}

// A ticket the agent moved mid-run rests wherever the agent left it, not
// wherever the queue's own rule would have sent it — conclude's own test
// coverage establishes this; here it is telemetry's status field that must
// agree.
func TestTelemetryRecordsWhereAMovedTicketCameToRest(t *testing.T) {
	var h *harness
	h = newHarness(t, 1, func(context.Context, run.Invocation) (run.Result, error) {
		_, err := h.fake.MoveIssue(context.Background(), "one", "Escalated")
		return run.Result{ExitCode: 0}, err
	})
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventExited, 1)

	runs := h.telemetryRuns()
	if len(runs) != 1 {
		t.Fatalf("telemetry runs = %d, want exactly 1", len(runs))
	}
	if got := runs[0].Status; got != "Escalated" {
		t.Errorf("status = %q, want Escalated: where the agent's own move actually left it", got)
	}
}

// A reaped run that recorded its own exit status gets conclude's move rule
// exactly as a live run would, and telemetry records it the same way: an
// exit code, a resting status, and a duration measured from the exit file's
// own mtime rather than invented.
func TestTelemetryRecordsAReapedRunWithAnExitFile(t *testing.T) {
	h := newHarness(t, 1, nil)
	record, err := h.evidence.Create(evidence.Record{
		Lane: 1, TicketID: "orphan", Ticket: "LERP-1", Queue: "todo", StartingStatus: "Todo",
		StartedAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record.ExitPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "orphan", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventExited, 1)

	runs := h.telemetryRuns()
	if len(runs) != 1 {
		t.Fatalf("telemetry runs = %d, want exactly 1", len(runs))
	}
	got := runs[0]
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", got.ExitCode)
	}
	if got.Status != "Done" {
		t.Errorf("status = %q, want Done", got.Status)
	}
	if got.DurationMS <= 0 {
		t.Errorf("duration = %dms, want positive: measured from the exit file's mtime", got.DurationMS)
	}
}

// A run killed before it could write its own exit status still gets a
// telemetry line — reap falls back to releasing the claim rather than
// running conclude, so there is no exit code, no duration and no status to
// report, and none of the three is faked as zero.
func TestTelemetryRecordsAKilledRunWithNoExitFile(t *testing.T) {
	h := newHarness(t, 1, nil)
	// PID set, as Attach would have left it once the agent actually started —
	// the fact this run has none to distinguish it from a claim protocol
	// failure, which also reaps through the same fallback branch but never
	// got this far (see TestTelemetryWritesNothingForARecordWhoseAgentNeverStarted).
	if _, err := h.evidence.Create(evidence.Record{
		Lane: 1, PID: 99999, TicketID: "orphan", Ticket: "LERP-1", Queue: "todo", StartingStatus: "Todo",
	}); err != nil {
		t.Fatal(err)
	}
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "orphan", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventReaped, 1)
	// The claim the reap released makes the ticket eligible again, and the
	// freed lane re-runs it to a real exit — a second, unrelated telemetry
	// line landing alongside the reap's.
	h.waitEvents(t, EventExited, 1)
	waitIdle(t, h.rec)

	var withoutExitCode int
	for _, run := range h.telemetryRuns() {
		if run.Ticket != "LERP-1" || run.ExitCode != nil {
			continue
		}
		withoutExitCode++
		if run.DurationMS != 0 {
			t.Errorf("duration = %dms, want 0 (absent): no exit file to measure from", run.DurationMS)
		}
		if run.Status != "" {
			t.Errorf("status = %q, want empty: no move rule ran", run.Status)
		}
	}
	if withoutExitCode != 1 {
		t.Fatalf("telemetry entries with no exit code = %d, want exactly 1 (the reap)", withoutExitCode)
	}
}

// A claim protocol failure keeps a record for the next pass to repair, but
// provisionAndRun and its Execute callback never ran for it: no PID was ever
// attached. Reaping that record must not report a run that never happened —
// distinct from TestTelemetryRecordsAKilledRunWithNoExitFile, whose record
// does carry a PID because its agent genuinely started.
func TestTelemetryWritesNothingForARecordWhoseAgentNeverStarted(t *testing.T) {
	h := newHarness(t, 1, nil)
	if _, err := h.evidence.Create(evidence.Record{
		Lane: 1, TicketID: "orphan", Ticket: "LERP-1", Queue: "todo", StartingStatus: "Todo",
	}); err != nil {
		t.Fatal(err)
	}
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "orphan", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventReaped, 1)
	waitIdle(t, h.rec)

	// The released claim makes the ticket eligible again, and its re-run gets
	// its own (unrelated) telemetry line with a real exit code — the
	// assertion below is scoped to entries that look like the reap itself.
	for _, run := range h.telemetryRuns() {
		if run.Ticket == "LERP-1" && run.ExitCode == nil {
			t.Errorf("telemetry recorded a run whose agent never started: %+v", run)
		}
	}
}

// Eject's Disown blanks a record's TicketID and Workspace, leaving it behind
// for reap if Remove never runs (a crash between the two, or a Remove that
// itself fails). Its ejected mark does not survive a restart, so a successor
// process reaps it as an ordinary dead record — and must write nothing for
// it, the same as the eject that meant to own it would have.
func TestTelemetryWritesNothingForADisownedRecord(t *testing.T) {
	h := newHarness(t, 1, nil)
	record, err := h.evidence.Create(evidence.Record{
		Lane: 1, PID: 99999, TicketID: "tkt", Ticket: "LERP-1", Queue: "todo", StartingStatus: "Todo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.evidence.Disown(record.RunID); err != nil {
		t.Fatal(err)
	}

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventReaped, 1)
	waitIdle(t, h.rec)

	if got := h.telemetryRuns(); len(got) != 0 {
		t.Errorf("telemetry runs after reaping a disowned record = %+v, want none", got)
	}
}

// Ejecting is taking the work over, not finishing it: the run's own session
// carries on interactively, so any number telemetry wrote for it would be a
// lie about a run that never actually ended.
func TestTelemetryWritesNothingForAnEjectedRun(t *testing.T) {
	execute, agents := liveExecute(t)
	h := newHarness(t, 1, execute)
	h.rec.o.Alive = evidence.Alive
	resumableRunner(h)
	h.fake.AddIssue("LERP", linear.Issue{ID: "tkt", Identifier: "LERP-1", Status: "Todo"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	h.waitEvents(t, EventStarted, 1)
	agents()

	if _, err := h.rec.Eject(ctx, "tkt"); err != nil {
		t.Fatal(err)
	}
	h.waitEvents(t, EventEjected, 1)
	waitIdle(t, h.rec)

	if got := h.telemetryRuns(); len(got) != 0 {
		t.Errorf("telemetry runs after an eject = %+v, want none", got)
	}
}

// A deliberate stop repairs identically to a crash (SCOPE invariant 3): the
// record stays, and it is the next lerp's reap that writes the line, so a
// process that stops mid-run must write none of its own.
func TestTelemetryWritesNothingForAShutdown(t *testing.T) {
	execute, release, _ := blockingExecute(t, "")
	h := newHarness(t, 1, execute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	ctx, cancel := context.WithCancel(context.Background())
	h.rec.Tick(ctx)
	h.waitEvents(t, EventStarted, 1)

	cancel()
	release()
	waitIdle(t, h.rec)

	if got := h.telemetryRuns(); len(got) != 0 {
		t.Errorf("telemetry runs after a shutdown = %+v, want none", got)
	}
}

// The default Telemetry option, backed by the real internal/telemetry
// package, must not let a write failure touch the run it describes: the
// board still moves, the record still goes, and the failure lands only in
// the log. This is the one test in the package that exercises the real
// default rather than a harness's collecting stub.
func TestTelemetryWriteFailureLeavesSettlementUntouched(t *testing.T) {
	dir := t.TempDir()
	// A plain file sitting where the telemetry directory needs to go: every
	// Append this reconciler makes fails on MkdirAll.
	if err := os.WriteFile(filepath.Join(dir, "lerp"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", dir)

	fake := linear.NewFake()
	logs := &logBuffer{}
	events := make(chan Event, 16)
	rec, err := NewReconciler(ReconcilerOptions{
		Client: fake, Repo: testRepo(), RepoDir: "/repo", Evidence: evidence.New(t.TempDir()),
		Lanes: 1, Events: func(ev Event) { events <- ev }, Log: logs,
		Execute: func(context.Context, run.Invocation) (run.Result, error) {
			return run.Result{ExitCode: 0}, nil
		},
		Provision: func(context.Context, string, string, workspace.Identity, io.Writer) error { return nil },
		Dispose:   func(context.Context, string, string, workspace.Identity, io.Writer) {},
		Alive:     func(evidence.Record) bool { return false },
		// Telemetry left nil, so NewReconciler's default — backed by the
		// real telemetry package — is what this test exercises.
	})
	if err != nil {
		t.Fatal(err)
	}
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	rec.Tick(context.Background())
	got := waitEventsOn(t, events, EventExited, 1)[0]
	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0: a telemetry failure must not touch the run's own outcome", got.ExitCode)
	}
	issue, err := fake.GetIssue(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != "Done" {
		t.Errorf("ticket status = %q, want Done: settlement proceeds despite the telemetry write failing", issue.Status)
	}
	if !strings.Contains(logs.String(), "telemetry:") {
		t.Errorf("log = %q, want the telemetry write failure reported", logs.String())
	}
}

// exitTiming must never turn "no real measurement" into a number: Sub
// against a zero StartedAt would otherwise saturate to Duration's ~292-year
// max, and clock skew that puts the exit file's mtime before StartedAt would
// otherwise go negative. Both are wrong in the same way zero-faking is —
// they claim a measurement telemetry does not have.
func TestExitTimingOmitsAnImpossibleDuration(t *testing.T) {
	dir := t.TempDir()
	exitPath := filepath.Join(dir, "exit")
	if err := os.WriteFile(exitPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("StartedAt predates the field", func(t *testing.T) {
		record := evidence.Record{ExitPath: exitPath}
		at, durationMS := exitTiming(record, true)
		if at.IsZero() {
			t.Error("at is zero, want the exit file's mtime")
		}
		if durationMS != 0 {
			t.Errorf("duration = %dms, want 0 (absent), not a saturated ~292-year span", durationMS)
		}
	})

	t.Run("clock skew puts the exit file before StartedAt", func(t *testing.T) {
		record := evidence.Record{ExitPath: exitPath, StartedAt: time.Now().Add(time.Hour)}
		_, durationMS := exitTiming(record, true)
		if durationMS != 0 {
			t.Errorf("duration = %dms, want 0 (absent), not negative", durationMS)
		}
	})

	t.Run("an ordinary run reports a positive duration", func(t *testing.T) {
		record := evidence.Record{ExitPath: exitPath, StartedAt: time.Now().Add(-time.Minute)}
		_, durationMS := exitTiming(record, true)
		if durationMS <= 0 {
			t.Errorf("duration = %dms, want positive", durationMS)
		}
	})
}
