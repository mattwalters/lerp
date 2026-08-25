package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/run"
	"github.com/mattwalters/lerp/internal/workspace"
)

// harness wires a Reconciler to the fake Linear client, a temporary evidence
// store, stub workspace commands, and a controllable liveness check keyed by
// run ID — no real agent processes anywhere.
type harness struct {
	fake     *linear.Fake
	rec      *Reconciler
	evidence *evidence.Evidence
	root     string // the evidence store's repository root
	events   chan Event
	alive    map[string]bool // run ID → the recorded process is "alive"
	logs     *logBuffer      // everything the loop wrote to its Log

	mu       sync.Mutex
	disposed []workspace.Identity
}

func newHarness(t *testing.T, lanes int, execute ExecuteFunc) *harness {
	t.Helper()
	fake := linear.NewFake()
	return newHarnessWith(t, lanes, execute, fake, fake)
}

// newHarnessWith is newHarness with the board and the reconciler's client
// supplied by the caller — for tests that run two reconcilers against one
// shared fake board through per-lerp client wrappers.
func newHarnessWith(t *testing.T, lanes int, execute ExecuteFunc, fake *linear.Fake, client linear.Client) *harness {
	t.Helper()
	root := t.TempDir()
	h := &harness{
		fake:     fake,
		evidence: evidence.New(root),
		root:     root,
		events:   make(chan Event, 64),
		alive:    map[string]bool{},
		logs:     &logBuffer{},
	}
	if execute == nil {
		execute = func(context.Context, run.Invocation) (run.Result, error) {
			return run.Result{ExitCode: 0}, nil
		}
	}
	rec, err := NewReconciler(ReconcilerOptions{
		Client:   client,
		Repo:     testRepo(),
		RepoDir:  "/repo",
		Evidence: h.evidence,
		Lanes:    lanes,
		Events:   func(ev Event) { h.events <- ev },
		Log:      h.logs,
		Execute:  execute,
		Provision: func(context.Context, string, string, workspace.Identity, io.Writer) error {
			return nil
		},
		Dispose: func(_ context.Context, _ string, _ string, id workspace.Identity, _ io.Writer) {
			h.mu.Lock()
			h.disposed = append(h.disposed, id)
			h.mu.Unlock()
		},
		Alive: func(record evidence.Record) bool { return h.alive[record.RunID] },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.rec = rec
	return h
}

// logBuffer is a Log a test can read while lanes are still writing to it.
type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func testRepo() *config.RepoConfig {
	return &config.RepoConfig{
		Teams:     []string{"LERP"},
		Provision: "provision",
		Dispose:   "dispose",
		Runners:   map[string]config.Runner{"agent": {Command: "agent"}},
		Queues: map[string]config.Queue{"todo": {
			Status: "Todo", Prompt: "do the work", Runner: "agent", OnSuccess: "Done", OnFailure: "Needs Help",
		}},
	}
}

// waitEvents collects the next n events of the wanted type, failing on any
// EventError seen along the way (unless errors are what is wanted).
func (h *harness) waitEvents(t *testing.T, want EventType, n int) []Event {
	t.Helper()
	return waitEventsOn(t, h.events, want, n)
}

// waitEventsOn is waitEvents against any event channel, for tests that run
// more than one reconciler.
func waitEventsOn(t *testing.T, events <-chan Event, want EventType, n int) []Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	var got []Event
	for len(got) < n {
		select {
		case ev := <-events:
			if ev.Type == EventError && want != EventError {
				t.Fatalf("unexpected error event: %v", ev.Err)
			}
			if ev.Type == want {
				got = append(got, ev)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %d %s event(s), got %d", n, want, len(got))
		}
	}
	return got
}

// drainEvents returns every event already emitted, without waiting.
func (h *harness) drainEvents() []Event {
	return drainEventsOn(h.events)
}

// drainEventsOn is drainEvents against any event channel.
func drainEventsOn(events <-chan Event) []Event {
	var got []Event
	for {
		select {
		case ev := <-events:
			got = append(got, ev)
		default:
			return got
		}
	}
}

// recordingExecute returns a stub ExecuteFunc that exits cleanly, recording
// what it ran, and a function returning the recording. Each run records the
// invocation's ticket identifier — or label, when non-empty, for tests that
// run two lerps and need to know which one executed.
func recordingExecute(label string) (ExecuteFunc, func() []string) {
	var mu sync.Mutex
	var runs []string
	execute := func(_ context.Context, inv run.Invocation) (run.Result, error) {
		mu.Lock()
		if label != "" {
			runs = append(runs, label)
		} else {
			runs = append(runs, inv.Ticket)
		}
		mu.Unlock()
		return run.Result{ExitCode: 0}, nil
	}
	recorded := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), runs...)
	}
	return execute, recorded
}

// blockingExecute is recordingExecute with every run held open until release
// is called. release is idempotent and registered as a test cleanup, so a
// test that fails before releasing cannot strand its runs' goroutines.
func blockingExecute(t *testing.T, label string) (execute ExecuteFunc, release func(), recorded func() []string) {
	t.Helper()
	gate := make(chan struct{})
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	record, recorded := recordingExecute(label)
	execute = func(ctx context.Context, inv run.Invocation) (run.Result, error) {
		result, err := record(ctx, inv)
		<-gate
		return result, err
	}
	return execute, release, recorded
}

// assertReaped asserts the local half of a reap: the run's record is gone and
// its workspace was disposed.
func assertReaped(t *testing.T, h *harness, record evidence.Record) {
	t.Helper()
	if _, err := h.evidence.Read(record.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reaped record %s read error = %v, want not exist", record.RunID, err)
	}
	for _, id := range h.disposedIdentities() {
		if id.Workspace == record.Workspace {
			return
		}
	}
	t.Errorf("disposed workspaces = %v, want the reaped run's %q among them",
		h.disposedIdentities(), record.Workspace)
}

// waitIdle waits for a reconciler's runs to wind down, on the same 5-second
// deadline every other wait in these tests uses.
func waitIdle(t *testing.T, rec *Reconciler) {
	t.Helper()
	done := make(chan struct{})
	go func() { rec.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the reconciler's runs to wind down")
	}
}

func (h *harness) disposedIdentities() []workspace.Identity {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]workspace.Identity(nil), h.disposed...)
}

func (h *harness) issue(t *testing.T, id string) linear.Issue {
	t.Helper()
	issue, err := h.fake.GetIssue(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return issue
}

// Done-when: with N=3 and five eligible tickets, three run and two wait, and
// lanes refill as runs finish.
func TestTickRunsAtMostNLanesAndRefills(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	running, maxRunning := 0, 0
	execute := func(_ context.Context, inv run.Invocation) (run.Result, error) {
		mu.Lock()
		running++
		if running > maxRunning {
			maxRunning = running
		}
		mu.Unlock()
		<-release
		mu.Lock()
		running--
		mu.Unlock()
		return run.Result{ExitCode: 0}, nil
	}
	h := newHarness(t, 3, execute)
	for i := 1; i <= 5; i++ {
		h.fake.AddIssue("LERP", linear.Issue{
			ID: fmt.Sprintf("t%d", i), Identifier: fmt.Sprintf("LERP-%d", i), Status: "Todo",
		})
	}
	ctx := context.Background()

	h.rec.Tick(ctx)
	started := h.waitEvents(t, EventStarted, 3)
	lanes := map[int]bool{}
	for _, ev := range started {
		lanes[ev.Lane] = true
	}
	if !lanes[1] || !lanes[2] || !lanes[3] {
		t.Errorf("started lanes = %v, want lanes 1..3", started)
	}
	// The two tickets beyond N wait untouched: still queued, still unclaimed.
	for _, id := range []string{"t4", "t5"} {
		if got := h.issue(t, id); got.Status != "Todo" || got.AssigneeID != "" {
			t.Errorf("waiting ticket %s = %+v, want unclaimed in Todo", id, got)
		}
	}

	// A tick with full lanes starts nothing — and must not mistake its own
	// live runs' records (whose stub agents never report a PID) for dead
	// orphans to reap.
	h.rec.Tick(ctx)
	records, err := h.evidence.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("run records while 3 agents run = %d, want 3", len(records))
	}
	for _, ev := range h.drainEvents() {
		if ev.Type == EventReaped || ev.Type == EventStarted {
			t.Fatalf("full-lane tick emitted %s: %+v", ev.Type, ev)
		}
	}

	close(release)
	h.waitEvents(t, EventExited, 3)

	// Lanes are free again; the next tick refills them with the two waiters.
	// Waiting on the exits alone: the first waiter can finish before the
	// second starts, so interleaving start and exit waits would lose events.
	h.rec.Tick(ctx)
	h.waitEvents(t, EventExited, 2)

	for i := 1; i <= 5; i++ {
		if got := h.issue(t, fmt.Sprintf("t%d", i)); got.Status != "Done" {
			t.Errorf("ticket t%d status = %q, want Done", i, got.Status)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if maxRunning != 3 {
		t.Errorf("max concurrent runs = %d, want exactly 3", maxRunning)
	}
	records, err = h.evidence.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("run records after all runs settled = %d, want 0", len(records))
	}
	if got := h.disposedIdentities(); len(got) != 5 {
		t.Errorf("disposed workspaces = %d, want one per run", len(got))
	}
}

// Done-when: starting lerp over a live orphan from a previous process adopts
// the run — the lane is occupied, the log path is announced for tailing, and
// the ticket is not restarted.
func TestTickAdoptsLiveOrphans(t *testing.T) {
	execute, executed := recordingExecute("")
	h := newHarness(t, 1, execute)
	started := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	record, err := h.evidence.Create(evidence.Record{
		Lane: 1, TicketID: "orphan", Queue: "todo", StartingStatus: "Todo", StartedAt: started,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.alive[record.RunID] = true
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "orphan", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})
	h.fake.AddIssue("LERP", linear.Issue{ID: "other", Identifier: "LERP-2", Status: "Todo"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	h.rec.Tick(ctx)
	adopted := 0
	for _, ev := range h.drainEvents() {
		switch ev.Type {
		case EventAttention:
			// Recomputed every pass; nothing here waits on the operator.
		case EventAdopted:
			adopted++
			if ev.RunID != record.RunID || ev.Lane != 1 || ev.LogPath != record.LogPath {
				t.Errorf("adopted event = %+v, want the orphan's record with its log path", ev)
			}
			if !ev.StartedAt.Equal(started) {
				t.Errorf("adopted event StartedAt = %v, want the run's original start %v", ev.StartedAt, started)
			}
		case EventQueues:
			// Every pass publishes its queue snapshot, full lanes or not.
		case EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		default:
			t.Fatalf("unexpected %s event while the adopted run fills the only lane: %+v", ev.Type, ev)
		}
	}
	if adopted != 1 {
		t.Fatalf("adopted events across two ticks = %d, want exactly 1", adopted)
	}
	if got := executed(); len(got) != 0 {
		t.Fatalf("executed runs = %v, want none: adoption must not restart, and the lane is full", got)
	}
	if _, err := h.evidence.Read(record.RunID); err != nil {
		t.Fatalf("adopted run's record: %v", err)
	}
	if got := h.disposedIdentities(); len(got) != 0 {
		t.Fatalf("adoption disposed workspaces %v, want none", got)
	}

	// The adopted process dies: the lane's occupant is reaped, its claim is
	// released, and the freed lane picks the ticket straight back up.
	h.alive[record.RunID] = false
	h.rec.Tick(ctx)
	h.waitEvents(t, EventReaped, 1)
	assertReaped(t, h, record)
	restarted := h.waitEvents(t, EventStarted, 1)
	if restarted[0].TicketID != "orphan" {
		t.Errorf("restarted ticket = %+v, want the reaped orphan", restarted[0])
	}
	h.waitEvents(t, EventExited, 1)
	if got := h.issue(t, "orphan"); got.Status != "Done" {
		t.Errorf("orphan status after re-run = %q, want Done", got.Status)
	}
}

// Done-when: starting lerp over a dead orphan reaps it — workspace disposed,
// record removed, ticket left in its status — and the ticket, eligible again,
// is picked back up.
func TestTickReapsDeadOrphansAndRepicksTheTicket(t *testing.T) {
	h := newHarness(t, 1, nil)
	record, err := h.evidence.Create(evidence.Record{
		Lane: 1, TicketID: "orphan", Queue: "todo", StartingStatus: "Todo",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "orphan", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})
	ctx := context.Background()

	h.rec.Tick(ctx)
	reaped := h.waitEvents(t, EventReaped, 1)
	if reaped[0].RunID != record.RunID {
		t.Errorf("reaped event = %+v, want the dead orphan's record", reaped[0])
	}
	assertReaped(t, h, record)
	if got := h.disposedIdentities(); len(got) == 0 || got[0] != (workspace.Identity{
		Lane: 1, TicketID: "orphan", Workspace: record.Workspace,
	}) {
		t.Errorf("reap disposed %v, want the dead run's identity", got)
	}

	// Reaping released the dead run's claim, so the ticket is eligible again
	// and the loop runs it through the queue.
	h.waitEvents(t, EventStarted, 1)
	h.waitEvents(t, EventExited, 1)
	if got := h.issue(t, "orphan"); got.Status != "Done" {
		t.Errorf("orphan status after reap and re-run = %q, want Done", got.Status)
	}
}

// Reaping repairs only what the dead run left behind. A ticket that moved
// since — by the agent before it died, a human, or an automation — and a
// ticket claimed by someone else are both left exactly as found.
func TestReapLeavesOtherPeoplesBoardStateAlone(t *testing.T) {
	h := newHarness(t, 3, nil)
	moved, err := h.evidence.Create(evidence.Record{
		Lane: 1, TicketID: "moved", Queue: "todo", StartingStatus: "Todo",
	})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := h.evidence.Create(evidence.Record{
		Lane: 2, TicketID: "theirs", Queue: "todo", StartingStatus: "Todo",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "moved", Identifier: "LERP-1", Status: "Escalated", AssigneeID: "fake-viewer",
	})
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "theirs", Identifier: "LERP-2", Status: "Todo", AssigneeID: "somebody-else",
	})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventReaped, 2)
	assertReaped(t, h, moved)
	assertReaped(t, h, theirs)
	if got := h.issue(t, "moved"); got.Status != "Escalated" || got.AssigneeID != "fake-viewer" {
		t.Errorf("moved ticket = %+v, want its post-move state untouched", got)
	}
	if got := h.issue(t, "theirs"); got.Status != "Todo" || got.AssigneeID != "somebody-else" {
		t.Errorf("someone else's claim = %+v, want untouched", got)
	}
}

// Done-when: a claimed lane announces provisioning before its agent starts,
// both events name the same run, and the started event carries the record's
// start time for subscribers' elapsed clocks.
func TestRunAnnouncesProvisioningBeforeStart(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	h.rec.Tick(context.Background())
	deadline := time.After(5 * time.Second)
	var seq []Event
	for len(seq) == 0 || seq[len(seq)-1].Type != EventExited {
		select {
		case ev := <-h.events:
			if ev.Type == EventError {
				t.Fatalf("unexpected error event: %v", ev.Err)
			}
			if ev.Type == EventQueues || ev.Type == EventAttention {
				continue // every pass emits both; the run's own sequence is under test
			}
			seq = append(seq, ev)
		case <-deadline:
			t.Fatalf("timed out waiting for the run to finish; events so far: %+v", seq)
		}
	}
	types := make([]EventType, len(seq))
	for i, ev := range seq {
		types[i] = ev.Type
	}
	if want := []EventType{EventProvisioning, EventStarted, EventExited}; !slices.Equal(types, want) {
		t.Fatalf("event sequence = %v, want %v", types, want)
	}
	prov, started := seq[0], seq[1]
	if prov.RunID == "" || prov.RunID != started.RunID {
		t.Errorf("provisioning names run %q, started names %q; want one shared non-empty ID", prov.RunID, started.RunID)
	}
	if prov.Lane != 1 || prov.TicketID != "one" || prov.Ticket != "LERP-1" || prov.Queue != "todo" {
		t.Errorf("provisioning event = %+v, want lane 1's claim of LERP-1", prov)
	}
	if started.StartedAt.IsZero() {
		t.Error("started event carries no start time")
	}
}

// The loop applies the same move rule as the single-lane flow: a non-zero
// exit routes to on_failure.
func TestRunFailureRoutesToOnFailure(t *testing.T) {
	h := newHarness(t, 1, func(context.Context, run.Invocation) (run.Result, error) {
		return run.Result{ExitCode: 3}, nil
	})
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	h.rec.Tick(context.Background())
	exited := h.waitEvents(t, EventExited, 1)
	if exited[0].ExitCode != 3 {
		t.Errorf("exit event = %+v, want exit code 3", exited[0])
	}
	if got := h.issue(t, "one"); got.Status != "Needs Help" {
		t.Errorf("failed run status = %q, want Needs Help", got.Status)
	}
}

// An agent that moved its own ticket has already decided; the loop respects
// whatever it finds, exactly as the single-lane flow does.
func TestRunRespectsAgentMove(t *testing.T) {
	var h *harness
	h = newHarness(t, 1, func(context.Context, run.Invocation) (run.Result, error) {
		return run.Result{ExitCode: 0}, h.fake.MoveIssue(context.Background(), "one", "Escalated")
	})
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventExited, 1)
	if got := h.issue(t, "one"); got.Status != "Escalated" {
		t.Errorf("agent move was overwritten: status = %q", got.Status)
	}
}

// Respecting a move is right; saying nothing about it is not. A ticket that
// left the queue status mid-run cost its pipeline a stage, so the skipped hop
// is named — in the run log and on the exit event the TUI reads.
func TestRunReportsTheHopItSkipped(t *testing.T) {
	var h *harness
	h = newHarness(t, 1, func(context.Context, run.Invocation) (run.Result, error) {
		return run.Result{ExitCode: 0}, h.fake.MoveIssue(context.Background(), "one", "In Progress")
	})
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	h.rec.Tick(context.Background())
	exited := h.waitEvents(t, EventExited, 1)

	want := `LERP-1 left "Todo" for "In Progress" during its run — the on_success hop to "Done" was skipped.`
	if !strings.Contains(exited[0].Note, want) {
		t.Errorf("exit event note = %q, want it to contain %q", exited[0].Note, want)
	}
	// "In Progress" is a status lerp.toml never names, which is what an
	// external automation looks like from here — say so, because the operator
	// cannot read it off the board.
	if !strings.Contains(exited[0].Note, `"In Progress" is not a status your pipeline names`) ||
		!strings.Contains(exited[0].Note, "external automation") {
		t.Errorf("exit event note = %q, want the external-automation cause named", exited[0].Note)
	}
	if !strings.Contains(h.logs.String(), want) {
		t.Errorf("run log = %q, want it to contain %q", h.logs.String(), want)
	}
}

// A destination the pipeline does name is an agent escalating, not an
// automation: report the skipped hop without guessing at a cause.
func TestRunReportsASkippedHopWithoutBlamingAnAutomation(t *testing.T) {
	var h *harness
	h = newHarness(t, 1, func(context.Context, run.Invocation) (run.Result, error) {
		return run.Result{ExitCode: 0}, h.fake.MoveIssue(context.Background(), "one", "Needs Help")
	})
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	h.rec.Tick(context.Background())
	exited := h.waitEvents(t, EventExited, 1)

	want := `LERP-1 left "Todo" for "Needs Help" during its run — the on_success hop to "Done" was skipped.`
	if exited[0].Note != want {
		t.Errorf("exit event note = %q, want exactly %q", exited[0].Note, want)
	}
}

// The report must not become noise: a run whose ticket stayed put says
// nothing, and neither does one whose ticket was moved to the very status the
// rule would have moved it to.
func TestRunReportsNothingWhenNoHopWasSkipped(t *testing.T) {
	for name, move := range map[string]func(*harness) error{
		"ticket did not move": func(*harness) error { return nil },
		"agent made the hop itself": func(h *harness) error {
			return h.fake.MoveIssue(context.Background(), "one", "Done")
		},
	} {
		t.Run(name, func(t *testing.T) {
			var h *harness
			h = newHarness(t, 1, func(context.Context, run.Invocation) (run.Result, error) {
				return run.Result{ExitCode: 0}, move(h)
			})
			h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

			h.rec.Tick(context.Background())
			exited := h.waitEvents(t, EventExited, 1)
			if exited[0].Note != "" {
				t.Errorf("exit event note = %q, want nothing on the happy path", exited[0].Note)
			}
			if strings.Contains(h.logs.String(), "skipped") {
				t.Errorf("run log reports a skipped hop that never happened: %q", h.logs.String())
			}
		})
	}
}

// The whole adopted-run repair rests on real runs actually being told where to
// write their exit status: without this, every reap in production silently
// falls back to releasing the claim, and the tests that stage an exit file by
// hand would go on passing. The path must be the record's own, so it lands
// beside the log in the run directory Remove takes away.
func TestRunsAreToldWhereToRecordTheirExitStatus(t *testing.T) {
	var mu sync.Mutex
	var exitPath, logPath string
	h := newHarness(t, 1, func(_ context.Context, inv run.Invocation) (run.Result, error) {
		mu.Lock()
		exitPath, logPath = inv.ExitPath, inv.LogPath
		mu.Unlock()
		return run.Result{ExitCode: 0}, nil
	})
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventExited, 1)

	mu.Lock()
	defer mu.Unlock()
	if exitPath == "" {
		t.Fatal("the run was given no exit path, so no reap of it could ever apply the move rule")
	}
	if filepath.Dir(exitPath) != filepath.Dir(logPath) {
		t.Errorf("exit path %q is not beside the run's log %q", exitPath, logPath)
	}
}

// Run is Tick on an interval, nothing more: it polls until cancelled and then
// waits for its own runs to wind down.
func TestRunPollsUntilCancelled(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.rec.o.Interval = 5 * time.Millisecond
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.rec.Run(ctx) }()

	h.waitEvents(t, EventExited, 1)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if got := h.issue(t, "one"); got.Status != "Done" {
		t.Errorf("ticket status = %q, want Done", got.Status)
	}
}

// Done-when: every pass publishes a queue snapshot — the raw listing with
// eligibility per ticket, blocked ones carrying their blockers — and the
// lane fills with the snapshot's first eligible ticket, so what the view
// shows is literally what the loop picks next.
func TestTickPublishesQueueSnapshotEveryPass(t *testing.T) {
	execute, release, _ := blockingExecute(t, "")
	h := newHarness(t, 1, execute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "t1", Identifier: "LERP-1", Title: "first up", Status: "Todo"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "t2", Identifier: "LERP-2", Title: "second", Status: "Todo"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "t3", Identifier: "LERP-3", Title: "gated", Status: "Todo"})
	h.fake.Block("t3", "t1")
	ctx := context.Background()

	h.rec.Tick(ctx)
	snap := h.waitEvents(t, EventQueues, 1)[0]
	if len(snap.Queues) != 1 {
		t.Fatalf("snapshot has %d queues, want 1: %+v", len(snap.Queues), snap.Queues)
	}
	q := snap.Queues[0]
	if q.Team != "LERP" || q.Name != "todo" || q.Status != "Todo" {
		t.Errorf("queue = %+v, want team LERP, queue todo, status Todo", q)
	}
	if len(q.Tickets) != 3 {
		t.Fatalf("queue lists %d tickets, want all 3: %+v", len(q.Tickets), q.Tickets)
	}
	for i, want := range []QueueTicket{
		{ID: "t1", Identifier: "LERP-1", Title: "first up", Eligible: true},
		{ID: "t2", Identifier: "LERP-2", Title: "second", Eligible: true},
		{ID: "t3", Identifier: "LERP-3", Title: "gated", BlockedBy: []string{"LERP-1"}},
	} {
		got := q.Tickets[i]
		if got.ID != want.ID || got.Eligible != want.Eligible || got.Title != want.Title ||
			!slices.Equal(got.BlockedBy, want.BlockedBy) {
			t.Errorf("ticket %d = %+v, want %+v", i, got, want)
		}
	}

	// The only lane fills with the snapshot's first eligible ticket.
	started := h.waitEvents(t, EventStarted, 1)
	if started[0].TicketID != q.Tickets[0].ID {
		t.Errorf("started %s, want the snapshot's first eligible ticket %s",
			started[0].TicketID, q.Tickets[0].ID)
	}

	// A pass with every lane full still publishes, now showing the running
	// ticket claimed and therefore ineligible.
	h.rec.Tick(ctx)
	snap = h.waitEvents(t, EventQueues, 1)[0]
	var running QueueTicket
	for _, tk := range snap.Queues[0].Tickets {
		if tk.ID == "t1" {
			running = tk
		}
	}
	if running.ID != "t1" || !running.Assigned || running.Eligible {
		t.Errorf("running ticket in the full-lane snapshot = %+v, want claimed and ineligible", running)
	}

	release()
	h.waitEvents(t, EventExited, 1)
}

// Done-when: the attention pass reports exactly the unclaimed tickets and
// the operator's claimed tickets sitting in statuses no queue serves — with
// the project, the status and what the pipeline says about that status on
// every item, so the table can be sorted, grouped and filtered without a
// second read. Once neither query has anything it reports an empty list, so
// a subscriber can show the goal state.
func TestTickEmitsAttention(t *testing.T) {
	h := newHarness(t, 1, nil)
	// Resting on the operator: claimed, in a status no queue serves — and
	// the one this pipeline's on_failure points at.
	h.fake.AddIssue("LERP", linear.Issue{ID: "help", Identifier: "LERP-1", Title: "Fix the build",
		Status: "Needs Help", AssigneeID: "fake-viewer", URL: "https://linear.app/l/LERP-1",
		Project: "Open-source readiness"})
	// Neither: claimed but in a queue's own status — a run may hold it.
	h.fake.AddIssue("LERP", linear.Issue{ID: "queued", Identifier: "LERP-2", Status: "Todo",
		AssigneeID: "fake-viewer"})
	// Neither: someone else's claim is someone else's work.
	h.fake.AddIssue("LERP", linear.Issue{ID: "theirs", Identifier: "LERP-3", Status: "Needs Help",
		AssigneeID: "somebody-else"})
	// Unclaimed, in a status no queue serves — one the pipeline never names
	// at all, so nothing lerp ran put it there.
	h.fake.AddIssue("LERP", linear.Issue{ID: "loose", Identifier: "LERP-4", Title: "Nobody's routed this",
		Status: "Backlog", URL: "https://linear.app/l/LERP-4"})
	// Neither: finished tickets wait on nobody.
	h.fake.AddIssue("LERP", linear.Issue{ID: "done", Identifier: "LERP-5", Status: "Done",
		AssigneeID: "fake-viewer"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	got := h.waitEvents(t, EventAttention, 1)[0]
	want := []AttentionItem{
		{
			Ticket: "LERP-1", TicketID: "help", Title: "Fix the build", Status: "Needs Help",
			Project: "Open-source readiness", Relevance: StatusFailed,
			Reason: `claimed in "Needs Help" — a run failed here`,
			URL:    "https://linear.app/l/LERP-1",
		},
		{
			Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this", Status: "Backlog",
			Relevance: StatusUnnamed,
			Reason:    `unassigned in "Backlog" — the pipeline never names it`,
			URL:       "https://linear.app/l/LERP-4",
		},
	}
	if !reflect.DeepEqual(got.Attention, want) {
		t.Errorf("attention = %+v, want %+v", got.Attention, want)
	}

	// The operator routes the ticket (in Linear, not in lerp) into a queue's
	// own status, and the next pass agrees only the unrouted ticket remains.
	if err := h.fake.MoveIssue(ctx, "help", "Todo"); err != nil {
		t.Fatal(err)
	}
	h.rec.Tick(ctx)
	got = h.waitEvents(t, EventAttention, 1)[0]
	if len(got.Attention) != 1 || got.Attention[0].Ticket != "LERP-4" {
		t.Errorf("attention after the operator routed it = %+v, want only the unrouted LERP-4", got.Attention)
	}
}

// Done-when: every item carries the facts the table sorts, groups and marks
// by — the transitive unblock count, the priority, the project, and the
// pipeline-relevance of the status — computed once per pass so no view has
// to read the board or the config a second time. The list itself comes back
// in identifier order: how it is ordered on screen is the panel's business.
func TestAttentionCarriesLeverageAndRelevance(t *testing.T) {
	h := newHarness(t, 1, nil)
	backlog := func(id, identifier string, priority int) {
		h.fake.AddIssue("LERP", linear.Issue{ID: id, Identifier: identifier,
			Title: identifier + " work", Status: "Backlog", Priority: priority})
	}
	// One chain — 22 blocks 38 and 23; 23 blocks 24 — and a field of roots.
	backlog("t22", "LERP-22", 2)
	backlog("t23", "LERP-23", 3)
	backlog("t24", "LERP-24", 3)
	backlog("t38", "LERP-38", 4)
	h.fake.Block("t38", "t22")
	h.fake.Block("t23", "t22")
	h.fake.Block("t24", "t23")
	// A blocker outside the listing: claimed by somebody else, so neither
	// query lists it. Its ticket still reads as blocked.
	backlog("t50", "LERP-50", 1)
	h.fake.AddIssue("LERP", linear.Issue{ID: "theirs", Identifier: "LERP-99",
		Status: "Backlog", AssigneeID: "somebody-else"})
	h.fake.Block("t50", "theirs")
	// Claimed by the operator, in the status this pipeline's on_failure
	// points at — the one place a run is known to have failed.
	h.fake.AddIssue("LERP", linear.Issue{ID: "t3", Identifier: "LERP-3", Status: "Needs Help",
		AssigneeID: "fake-viewer", Priority: 1, Project: "Open-source readiness"})

	h.rec.Tick(context.Background())
	got := h.waitEvents(t, EventAttention, 1)[0].Attention

	var order []string
	items := map[string]AttentionItem{}
	for _, it := range got {
		order = append(order, it.Ticket)
		items[it.Ticket] = it
	}
	want := []string{"LERP-22", "LERP-23", "LERP-24", "LERP-3", "LERP-38", "LERP-50"}
	if !slices.Equal(order, want) {
		t.Errorf("attention order = %v, want the identifier order %v", order, want)
	}
	// Transitive: 22 frees 38, 23 and — through 23 — 24.
	for ticket, count := range map[string]int{
		"LERP-22": 3, "LERP-23": 1, "LERP-24": 0, "LERP-38": 0, "LERP-50": 0, "LERP-3": 0,
	} {
		if items[ticket].Unblocks != count {
			t.Errorf("%s unblocks %d, want %d", ticket, items[ticket].Unblocks, count)
		}
	}
	if it := items["LERP-22"]; it.Priority != 2 || len(it.BlockedBy) != 0 || !slices.Contains(it.Blocks, "LERP-23") {
		t.Errorf("LERP-22 = %+v, want an unblocked High blocking LERP-23", it)
	}
	// The blocker is outside the listing, so it earns no leverage there —
	// but it still blocks, and the item says so.
	if it := items["LERP-50"]; !slices.Equal(it.BlockedBy, []string{"LERP-99"}) {
		t.Errorf("LERP-50 blocked by %v, want the unlisted LERP-99", it.BlockedBy)
	}
	if it := items["LERP-3"]; it.Relevance != StatusFailed || it.Project != "Open-source readiness" {
		t.Errorf("LERP-3 = %+v, want a failure status in the Open-source readiness project", it)
	}
	if it := items["LERP-22"]; it.Relevance != StatusUnnamed || it.Project != "" {
		t.Errorf("LERP-22 = %+v, want a status the pipeline never names and no project", it)
	}
}

// Done-when: the ordering the status mode groups by is derived from
// lerp.toml and nothing else — failure routes first, then where clean runs
// come to rest, then statuses the pipeline never names, then the statuses it
// serves. Rewriting the on_success pointers rewrites this with them; there
// is no key in the config that says any of it.
// Every rank must describe itself truthfully, including the two that are
// unreachable today: the zero value, which an AttentionItem built without an
// explicit Relevance carries, and StatusOther, which means a queue *does*
// serve the status. Both used to claim the opposite of the truth.
func TestStatusRelevanceNotesAreTrue(t *testing.T) {
	if StatusUnknown != 0 {
		t.Fatalf("StatusUnknown = %d, want the zero value so an unset Relevance is not a real rank", StatusUnknown)
	}
	if StatusUnknown >= StatusFailed {
		t.Errorf("the zero value sorts at or after StatusFailed, so an unset item impersonates a failed run")
	}
	for rank, want := range map[StatusRelevance]string{
		StatusUnknown:  "relevance unknown",
		StatusFailed:   "a run failed here",
		StatusFinished: "a run finished here",
		StatusUnnamed:  "the pipeline never names it",
		StatusOther:    "a queue serves it",
	} {
		if got := rank.Note(); got != want {
			t.Errorf("StatusRelevance(%d).Note() = %q, want %q", rank, got, want)
		}
	}
}

func TestStatusRelevanceIsDerivedFromTheQueues(t *testing.T) {
	repo := &config.RepoConfig{
		Teams:     []string{"LERP"},
		Provision: "provision",
		Dispose:   "dispose",
		Runners:   map[string]config.Runner{"agent": {Command: "agent"}},
		Queues: map[string]config.Queue{
			"plan": {Status: "Planning", Prompt: "p", Runner: "agent",
				OnSuccess: "Plan Review", OnFailure: "Needs Attention"},
			"implement": {Status: "Implementing", Prompt: "p", Runner: "agent",
				OnSuccess: "In Review", OnFailure: "Needs Attention"},
			// One queue's exit is another's status: serving it wins.
			"review": {Status: "Plan Review", Prompt: "p", Runner: "agent",
				OnSuccess: "Done", OnFailure: "Planning"},
		},
	}
	relevance := statusRelevance(repo)
	for status, want := range map[string]StatusRelevance{
		"Needs Attention": StatusFailed,
		"In Review":       StatusFinished,
		"Done":            StatusFinished,
		"Backlog":         StatusUnnamed,
		"In Progress":     StatusUnnamed,
		"Implementing":    StatusOther,
		// Served by the review queue, though the plan queue's on_success
		// also points at it.
		"Plan Review": StatusOther,
		// A queue's own status, and another queue's failure route: the
		// pipeline serves it, so a ticket there is not resting.
		"Planning": StatusOther,
	} {
		if got := relevance(status); got != want {
			t.Errorf("relevance of %q = %d, want %d", status, got, want)
		}
	}
	// The ranks order the way the table groups by them.
	if !(StatusFailed < StatusFinished && StatusFinished < StatusUnnamed && StatusUnnamed < StatusOther) {
		t.Error("status relevance ranks do not order failure, finished, unnamed, served")
	}
}

// A failed run's on_failure move lands the ticket — still claimed — in a
// status no queue serves; the next pass surfaces it in attention, marked as
// somewhere a run failed. Neither half of the inbox needs machinery of its
// own.
func TestFailedRunLandsInAttention(t *testing.T) {
	h := newHarness(t, 1, func(context.Context, run.Invocation) (run.Result, error) {
		return run.Result{ExitCode: 3}, nil
	})
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Title: "Flaky", Status: "Todo"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	h.waitEvents(t, EventExited, 1)
	h.drainEvents()

	h.rec.Tick(ctx)
	got := h.waitEvents(t, EventAttention, 1)[0]
	if len(got.Attention) != 1 || got.Attention[0].Ticket != "LERP-1" ||
		got.Attention[0].Status != "Needs Help" || got.Attention[0].Relevance != StatusFailed {
		t.Errorf("attention after a failed run = %+v, want LERP-1 resting in Needs Help, a status a run failed in", got.Attention)
	}
}

// Promote is the TUI's one write action: a plain MoveIssue through the same
// client the loop reads with, nothing else touched.
func TestReconcilerPromote(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.fake.AddIssue("LERP", linear.Issue{ID: "loose", Identifier: "LERP-4", Status: "Backlog"})
	ctx := context.Background()

	if err := h.rec.Promote(ctx, "loose", "Todo"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got := h.issue(t, "loose"); got.Status != "Todo" {
		t.Errorf("status after Promote = %q, want Todo", got.Status)
	}
}

// The rework loop, end to end: a failed run parks the ticket with its claim
// intact, and promoting it back into the queue has to release that claim or
// nothing will ever pick it up again — silently, with no run and no error.
func TestPromoteReleasesTheClaimThatParkedTheTicket(t *testing.T) {
	h := newHarness(t, 1, nil)
	ctx := context.Background()
	repo := testRepo()
	queue := repo.Queues["todo"]
	issue := linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"}
	h.fake.AddIssue("LERP", issue)

	viewerID, won, err := claimForQueue(ctx, h.fake, issue.ID, queue.Status)
	if err != nil || !won {
		t.Fatalf("claimForQueue = (%v, %v), want the claim won", won, err)
	}
	if _, err := conclude(ctx, h.fake, issue, queue, repo, 1, viewerID, nil); err != nil {
		t.Fatalf("conclude: %v", err)
	}
	parked := h.issue(t, "one")
	if parked.Status != "Needs Help" || parked.AssigneeID == "" {
		t.Fatalf("parked ticket = %+v, want it resting in Needs Help still claimed", parked)
	}

	// What the operator does after reading the verdict.
	if err := h.rec.Promote(ctx, "one", "Todo"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	back := h.issue(t, "one")
	if !Eligible(back, map[string]bool{"Todo": true}) {
		t.Fatalf("promoted ticket is not eligible, so no pass will ever run it: %+v", back)
	}
	cands, err := candidates(ctx, h.fake, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].issue.ID != "one" {
		t.Fatalf("candidates after the promote = %+v, want the reworked ticket", cands)
	}
}

// The other half of the same rule: promoting into a status no queue serves
// is how work gets parked on purpose, so the claim stays exactly where it is.
func TestPromoteIntoAnUnservedStatusKeepsTheClaim(t *testing.T) {
	h := newHarness(t, 1, nil)
	ctx := context.Background()
	viewerID, err := h.fake.Viewer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo", AssigneeID: viewerID})

	if err := h.rec.Promote(ctx, "one", "Needs Help"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	got := h.issue(t, "one")
	if got.Status != "Needs Help" {
		t.Errorf("status = %q, want Needs Help", got.Status)
	}
	if got.AssigneeID != viewerID {
		t.Error("parking the ticket released its claim, so it no longer shows as waiting on the operator")
	}
}

// Promote runs against a ticket the operator merely selected, which may be
// running under someone else on another machine. Their claim is not ours to
// clear.
func TestPromoteLeavesAnotherUsersClaimAlone(t *testing.T) {
	h := newHarness(t, 1, nil)
	ctx := context.Background()
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Backlog", AssigneeID: "someone-else"})

	if err := h.rec.Promote(ctx, "one", "Todo"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got := h.issue(t, "one"); got.AssigneeID != "someone-else" {
		t.Errorf("assignee after Promote = %q, want the colleague's claim untouched", got.AssigneeID)
	}
}

// countingDetailClient counts the detail reads made through it — the
// mechanical form of "no per-item comment query entered attention()".
type countingDetailClient struct {
	linear.Client
	details atomic.Int64
}

func (c *countingDetailClient) GetIssueDetail(ctx context.Context, issueID string) (linear.IssueDetail, error) {
	c.details.Add(1)
	return c.Client.GetIssueDetail(ctx, issueID)
}

func TestReconcilerIssueDetail(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.fake.AddIssue("LERP", linear.Issue{ID: "loose", Identifier: "LERP-4", Status: "Backlog"})
	h.fake.SetDescription("loose", "the body")
	ctx := context.Background()
	if err := h.fake.CommentOnIssue(ctx, "loose", "the verdict"); err != nil {
		t.Fatalf("CommentOnIssue: %v", err)
	}

	detail, err := h.rec.IssueDetail(ctx, "loose")
	if err != nil {
		t.Fatalf("IssueDetail: %v", err)
	}
	if detail.Body != "the body" {
		t.Errorf("body = %q, want %q", detail.Body, "the body")
	}
	if len(detail.Comments) != 1 || detail.Comments[0].Body != "the verdict" {
		t.Errorf("comments = %+v, want the one verdict", detail.Comments)
	}
}

// The pane\'s read is issued on selection, never by a pass: a board full of
// inbox items must cost the same queries it always did.
func TestPassNeverReadsTicketDetail(t *testing.T) {
	fake := linear.NewFake()
	client := &countingDetailClient{Client: fake}
	h := newHarnessWith(t, 1, nil, fake, client)
	for _, id := range []string{"a", "b", "c"} {
		fake.AddIssue("LERP", linear.Issue{ID: id, Identifier: "LERP-" + id, Status: "Backlog"})
		fake.SetDescription(id, "a body nobody selected")
	}

	h.rec.Tick(context.Background())
	if items := h.waitEvents(t, EventAttention, 1)[0].Attention; len(items) != 3 {
		t.Fatalf("attention listed %d items, want 3", len(items))
	}
	if n := client.details.Load(); n != 0 {
		t.Errorf("a pass made %d detail reads, want 0", n)
	}
}

func TestNewReconcilerValidatesOptions(t *testing.T) {
	valid := func() ReconcilerOptions {
		return ReconcilerOptions{
			Client:   linear.NewFake(),
			Repo:     testRepo(),
			RepoDir:  "/repo",
			Evidence: evidence.New(t.TempDir()),
			Lanes:    3,
		}
	}
	if _, err := NewReconciler(valid()); err != nil {
		t.Fatalf("NewReconciler with valid options: %v", err)
	}
	for name, corrupt := range map[string]func(*ReconcilerOptions){
		"client":   func(o *ReconcilerOptions) { o.Client = nil },
		"repo":     func(o *ReconcilerOptions) { o.Repo = nil },
		"repo dir": func(o *ReconcilerOptions) { o.RepoDir = "" },
		"evidence": func(o *ReconcilerOptions) { o.Evidence = nil },
		"lanes":    func(o *ReconcilerOptions) { o.Lanes = 0 },
	} {
		o := valid()
		corrupt(&o)
		if _, err := NewReconciler(o); err == nil {
			t.Errorf("NewReconciler accepted options missing %s", name)
		}
	}
}
