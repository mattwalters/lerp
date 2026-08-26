//go:build unix

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

	// provisionErr, when set, is what the stub provision command returns. It
	// is written before the pass that reads it, so the lane goroutine's read
	// is ordered after the write by the go statement that starts it.
	provisionErr error

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
			return h.provisionErr
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

// newSecondLerp is another reconciler over the same clone — same evidence
// store, same board — with its own lanes, events, and liveness map. It is
// how a test plays the successor process that finds another lerp's records
// on disk.
func newSecondLerp(t *testing.T, h *harness, lanes int) *harness {
	t.Helper()
	next := &harness{
		fake:     h.fake,
		evidence: h.evidence,
		root:     h.root,
		events:   make(chan Event, 64),
		alive:    map[string]bool{},
		logs:     &logBuffer{},
	}
	rec, err := NewReconciler(ReconcilerOptions{
		Client:   h.fake,
		Repo:     testRepo(),
		RepoDir:  "/repo",
		Evidence: next.evidence,
		Lanes:    lanes,
		Events:   func(ev Event) { next.events <- ev },
		Log:      next.logs,
		Execute: func(context.Context, run.Invocation) (run.Result, error) {
			return run.Result{ExitCode: 0}, nil
		},
		Provision: func(context.Context, string, string, workspace.Identity, io.Writer) error {
			return nil
		},
		Dispose: func(_ context.Context, _ string, _ string, id workspace.Identity, _ io.Writer) {
			next.mu.Lock()
			next.disposed = append(next.disposed, id)
			next.mu.Unlock()
		},
		Alive: func(record evidence.Record) bool { return next.alive[record.RunID] },
	})
	if err != nil {
		t.Fatal(err)
	}
	next.rec = rec
	t.Cleanup(func() { waitIdle(t, rec) })
	return next
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
	// The work panel draws an adopted run as running, so the loop log is the
	// only record that a successor took this run over.
	// Once, across both ticks: a run already adopted is not adopted again.
	if got := h.logs.String(); strings.Count(got, "adopted run "+record.RunID) != 1 {
		t.Errorf("loop log does not record the adoption exactly once:\n%s", got)
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

// The move rule, on the live path: a non-zero exit routes to on_failure.
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

// Provisioning never starts a lane, so the claim it won has to go back:
// an assigned ticket is never eligible, so keeping it would strand the ticket
// in the very queue it came from. The workspace is disposed either way — a
// provision command can fail after creating it, and the next attempt would
// collide with what it left behind.
func TestRunProvisionFailureReleasesTheClaimAndDisposes(t *testing.T) {
	h := newHarness(t, 1, func(context.Context, run.Invocation) (run.Result, error) {
		t.Error("the agent ran even though provisioning failed")
		return run.Result{}, nil
	})
	h.provisionErr = errors.New("no workspace")
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	h.rec.Tick(context.Background())
	if ev := h.waitEvents(t, EventError, 1)[0]; ev.TicketID != "one" {
		t.Errorf("error event = %+v, want it to name the ticket", ev)
	}
	waitIdle(t, h.rec)
	got := h.issue(t, "one")
	if got.Status != "Todo" || got.AssigneeID != "" {
		t.Errorf("provision failure left issue = %+v, want it queued and unclaimed", got)
	}
	if len(h.disposedIdentities()) == 0 {
		t.Error("provision failure did not dispose the workspace")
	}
}

// An agent that moved its own ticket has already decided; the loop respects
// whatever it finds.
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
	// at all, and one Linear files under its backlog category, so the
	// ticket has not entered the pipeline rather than fallen out of it.
	h.fake.AddIssue("LERP", linear.Issue{ID: "loose", Identifier: "LERP-4", Title: "Nobody's routed this",
		Status: "Backlog", URL: "https://linear.app/l/LERP-4"})
	// Unclaimed in a status the pipeline never names that Linear files as
	// started: something moved this one out of the pipeline.
	h.fake.SetStatusCategory("started", "In Progress")
	h.fake.AddIssue("LERP", linear.Issue{ID: "adrift", Identifier: "LERP-6", Title: "Someone dragged this",
		Status: "In Progress", URL: "https://linear.app/l/LERP-6"})
	// Neither: finished tickets wait on nobody.
	h.fake.AddIssue("LERP", linear.Issue{ID: "done", Identifier: "LERP-5", Status: "Done",
		AssigneeID: "fake-viewer"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	got := h.waitEvents(t, EventAttention, 1)[0]
	want := []AttentionItem{
		{
			Ticket: "LERP-1", TicketID: "help", Title: "Fix the build", Status: "Needs Help",
			Project: "Open-source readiness", Relevance: StatusFailed, Claimed: true,
			Reason: `claimed in "Needs Help" — a run failed here`,
			URL:    "https://linear.app/l/LERP-1",
		},
		{
			Ticket: "LERP-4", TicketID: "loose", Title: "Nobody's routed this", Status: "Backlog",
			Relevance: StatusBacklog,
			Reason:    `unassigned in "Backlog" — waiting to enter the pipeline`,
			URL:       "https://linear.app/l/LERP-4",
		},
		{
			Ticket: "LERP-6", TicketID: "adrift", Title: "Someone dragged this", Status: "In Progress",
			Relevance: StatusUnnamed,
			Reason:    `unassigned in "In Progress" — the pipeline never names it`,
			URL:       "https://linear.app/l/LERP-6",
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
	if len(got.Attention) != 2 || got.Attention[0].Ticket != "LERP-4" {
		t.Errorf("attention after the operator routed it = %+v, want the two unrouted tickets", got.Attention)
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
	if it := items["LERP-22"]; it.Relevance != StatusBacklog || it.Project != "" {
		t.Errorf("LERP-22 = %+v, want a ticket waiting to enter the pipeline and no project", it)
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
		StatusBacklog:  "waiting to enter the pipeline",
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
	for k, want := range map[[2]string]StatusRelevance{
		{"Needs Attention", "unstarted"}: StatusFailed,
		{"In Review", "started"}:         StatusFinished,
		{"Done", "completed"}:            StatusFinished,
		// Unnamed by the pipeline, and never routed anywhere by anything:
		// Linear's own category is what tells the two apart.
		{"Backlog", linear.CategoryBacklog}: StatusBacklog,
		{"Inbox", linear.CategoryTriage}:    StatusBacklog,
		{"In Progress", "started"}:          StatusUnnamed,
		// A board that takes its intake in an unstarted Todo column rather
		// than a backlog is the same board: nothing has routed these
		// either, and marking every one of them would be the noise this
		// rank exists to stop.
		{"Todo", linear.CategoryUnstarted}: StatusBacklog,
		{"Implementing", "started"}:        StatusOther,
		// Served by the review queue, though the plan queue's on_success
		// also points at it.
		{"Plan Review", "started"}: StatusOther,
		// A queue's own status, and another queue's failure route: the
		// pipeline serves it, so a ticket there is not resting.
		{"Planning", "started"}: StatusOther,
		// A status the pipeline serves stays served whatever Linear files
		// it under: config outranks the category, and only a status config
		// never mentions is read off the board at all.
		{"Implementing", linear.CategoryBacklog}: StatusOther,
	} {
		if got := relevance(k[0], k[1]); got != want {
			t.Errorf("relevance of %q (%s) = %d, want %d", k[0], k[1], got, want)
		}
	}
	// The ranks order the way the table groups by them.
	if !(StatusFailed < StatusFinished && StatusFinished < StatusUnnamed &&
		StatusUnnamed < StatusBacklog && StatusBacklog < StatusOther) {
		t.Error("status relevance ranks do not order failure, finished, unnamed, backlog, served")
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

// The rework loop, end to end. A finished run no longer parks its claim
// (LERP-113), so the claim promote has to clear is the one a human takes:
// reading the verdict in Linear and assigning the ticket to themselves is
// exactly how an operator says "mine". Promoting it back into a queue has to
// release that claim or nothing will ever pick it up again — silently, with
// no run and no error (LERP-59).
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
	if parked.Status != "Needs Help" || parked.AssigneeID != "" {
		t.Fatalf("parked ticket = %+v, want it resting in Needs Help unclaimed", parked)
	}
	// The operator picks it up in Linear while they read the verdict.
	if err := h.fake.AssignIssue(ctx, "one", viewerID); err != nil {
		t.Fatal(err)
	}

	// What the operator does after reading the verdict.
	if err := h.rec.Promote(ctx, "one", "Todo"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	back := h.issue(t, "one")
	if !Eligible(back, map[string]bool{"Todo": true}) {
		t.Fatalf("promoted ticket is not eligible, so no pass will ever run it: %+v", back)
	}
	listings, err := listQueues(ctx, h.fake, repo)
	if err != nil {
		t.Fatal(err)
	}
	cands := candidatesFrom(listings)
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

// resyncEvery sets how many delta reads the attention pass makes per team
// before re-listing that team's board in full. Call it before the first
// Tick, the way the option it stands for is set before NewReconciler.
func (h *harness) resyncEvery(n int) { h.rec.o.ResyncEvery = n }

// serveTeams replaces the teams the harness's repo config names. Call it
// before the first Tick, as with resyncEvery.
func (h *harness) serveTeams(teams ...string) { h.rec.o.Repo.Teams = teams }

// countingBoardClient counts the reads behind the attention pass — the
// mechanical form of "a pass costs one query per team, not the team's whole
// backlog". listings counts both full listings together, since the pass
// always runs them as a pair.
type countingBoardClient struct {
	linear.Client
	deltas   atomic.Int64
	listings atomic.Int64
	// deltaErr, when set, is returned instead of the delta read — the pass
	// that could not see what changed. failTeam narrows it to one team,
	// which is how a test plays one team failing while another does not.
	deltaErr error
	failTeam string

	mu sync.Mutex
	// sinces records every cursor the delta was asked from, in order.
	sinces []time.Time
}

// asked returns the cursors the delta read was given, oldest call first.
func (c *countingBoardClient) asked() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.sinces...)
}

func (c *countingBoardClient) ListTeamIssuesUpdatedSince(ctx context.Context, teamKey string, since time.Time) ([]linear.Issue, error) {
	c.deltas.Add(1)
	c.mu.Lock()
	c.sinces = append(c.sinces, since)
	c.mu.Unlock()
	if c.deltaErr != nil && (c.failTeam == "" || c.failTeam == teamKey) {
		return nil, c.deltaErr
	}
	return c.Client.ListTeamIssuesUpdatedSince(ctx, teamKey, since)
}

func (c *countingBoardClient) ListUnassignedIssues(ctx context.Context, teamKey string) ([]linear.Issue, error) {
	c.listings.Add(1)
	return c.Client.ListUnassignedIssues(ctx, teamKey)
}

func (c *countingBoardClient) ListAssignedIssues(ctx context.Context, teamKey, assigneeID string) ([]linear.Issue, error) {
	c.listings.Add(1)
	return c.Client.ListAssignedIssues(ctx, teamKey, assigneeID)
}

// newCountingHarness is a one-team harness whose board reads are counted.
func newCountingHarness(t *testing.T) (*harness, *countingBoardClient) {
	t.Helper()
	fake := linear.NewFake()
	counting := &countingBoardClient{Client: fake}
	return newHarnessWith(t, 1, nil, fake, counting), counting
}

// Done-when: the pass stops paying for the whole backlog every 12 seconds.
// After the cold start's one full listing, each pass reads one delta per team
// and nothing else — on an established board that is the difference between
// ~25 requests a pass and one.
func TestAttentionReadsOneDeltaPerPass(t *testing.T) {
	h, counting := newCountingHarness(t)
	h.resyncEvery(20)
	h.fake.AddIssue("LERP", linear.Issue{ID: "loose", Identifier: "LERP-1", Status: "Backlog"})
	ctx := context.Background()

	for range 5 {
		h.rec.Tick(ctx)
		h.waitEvents(t, EventAttention, 1)
	}
	// One pair on the cold start and none after: five passes inside the
	// resync window re-list once, not five times.
	if got := counting.listings.Load(); got != 2 {
		t.Errorf("full listings = %d, want the cold start's pair and no more", got)
	}
	// The four passes after the cold start each cost exactly one read.
	if got := counting.deltas.Load(); got != 4 {
		t.Errorf("delta reads = %d, want one per pass after the first", got)
	}
}

// Done-when: what a delta structurally cannot report still heals. An
// archived or deleted ticket changes nothing — no query returns it at all —
// so only the periodic full re-list can notice it is gone.
func TestAttentionResyncDropsAnArchivedTicket(t *testing.T) {
	h, counting := newCountingHarness(t)
	h.resyncEvery(1)
	h.fake.AddIssue("LERP", linear.Issue{ID: "gone", Identifier: "LERP-1", Status: "Backlog"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "stays", Identifier: "LERP-2", Status: "Backlog"})
	ctx := context.Background()

	h.rec.Tick(ctx) // cold start: both listed
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 2 {
		t.Fatalf("cold start = %+v, want both tickets", got.Attention)
	}
	h.fake.DropIssue("gone")

	// The delta pass cannot see it: nothing arrived saying so.
	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 2 {
		t.Fatalf("delta pass = %+v, want the archived ticket still listed", got.Attention)
	}
	// The resync pass rebuilds from the listings, which no longer mention it.
	h.rec.Tick(ctx)
	got := h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 1 || got[0].Ticket != "LERP-2" {
		t.Errorf("resync pass = %+v, want only the ticket that still exists", got)
	}
	if counting.listings.Load() != 4 {
		t.Errorf("full listings = %d, want the cold start's pair and the resync's", counting.listings.Load())
	}
}

// Done-when: the two ways a ticket leaves the inbox without anyone here
// touching it — a colleague claims it, or it finishes — both land on the very
// next pass. Neither would, if the delta query were filtered the way the
// listings it replaces are: the row would simply never arrive.
func TestAttentionEvictsThroughTheDelta(t *testing.T) {
	h, _ := newCountingHarness(t)
	h.fake.AddIssue("LERP", linear.Issue{ID: "taken", Identifier: "LERP-1", Status: "Backlog"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "finished", Identifier: "LERP-2", Status: "Backlog"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "stays", Identifier: "LERP-3", Status: "Backlog"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 3 {
		t.Fatalf("cold start = %+v, want all three", got.Attention)
	}
	if err := h.fake.AssignIssue(ctx, "taken", "somebody-else"); err != nil {
		t.Fatal(err)
	}
	if err := h.fake.MoveIssue(ctx, "finished", "Done"); err != nil {
		t.Fatal(err)
	}

	h.rec.Tick(ctx)
	got := h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 1 || got[0].Ticket != "LERP-3" {
		t.Errorf("after the delta = %+v, want only the ticket still waiting on the operator", got)
	}
}

// Done-when: lerp's own writes reach the inbox as fast as anyone else's. A
// promote moves the ticket, which bumps its updatedAt, so the next delta
// carries it — no special case anywhere for changes this process made.
func TestAttentionSeesOurOwnPromoteOnTheNextPass(t *testing.T) {
	h, _ := newCountingHarness(t)
	h.fake.AddIssue("LERP", linear.Issue{ID: "parked", Identifier: "LERP-1", Status: "Needs Help",
		AssigneeID: "fake-viewer"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 1 {
		t.Fatalf("cold start = %+v, want the parked ticket", got.Attention)
	}
	if err := h.rec.Promote(ctx, "parked", "Todo"); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 0 {
		t.Errorf("after promoting into a queue = %+v, want an empty inbox", got.Attention)
	}
	// The same pass filled a lane with the ticket it just promoted, which is
	// the point of promoting into a queue. Let that run finish before the
	// test's temporary evidence store goes away underneath it.
	waitIdle(t, h.rec)
}

// Done-when: a failed delta reads as a failed pass and nothing else. It emits
// no inbox — a partial read must never look like an empty one — and it leaves
// the cursor where it was, so the change it missed arrives on the retry
// rather than being skipped past.
func TestAttentionKeepsItsCursorWhenTheDeltaFails(t *testing.T) {
	h, counting := newCountingHarness(t)
	h.fake.AddIssue("LERP", linear.Issue{ID: "loose", Identifier: "LERP-1", Status: "Backlog"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	h.waitEvents(t, EventAttention, 1)

	// A ticket arrives well past the cursor — further than the trailing
	// window a delta re-reads anyway, so only a cursor that stayed put can
	// still be asking a question this ticket is inside of.
	h.fake.Advance(10 * time.Minute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "new", Identifier: "LERP-2", Status: "Backlog"})
	counting.deltaErr = errors.New("boom")
	h.rec.Tick(ctx)
	ev := h.waitEvents(t, EventError, 1)[0]
	if !strings.Contains(ev.Err.Error(), "boom") {
		t.Errorf("error event = %v, want the delta's own failure", ev.Err)
	}
	select {
	case ev := <-h.events:
		if ev.Type == EventAttention {
			t.Fatalf("a failed pass emitted an inbox: %+v", ev.Attention)
		}
	default:
	}

	// The retry asks from the same cursor, so the ticket it missed is in it.
	counting.deltaErr = nil
	h.rec.Tick(ctx)
	got := h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 2 {
		t.Errorf("after the retry = %+v, want both tickets: nothing was skipped past", got)
	}
	if counting.listings.Load() != 2 {
		t.Errorf("full listings = %d, want no re-list: a failed delta is retried, not escalated", counting.listings.Load())
	}
}

// laggingDeltaClient hides one issue from the first delta read, standing in
// for the replica that has a newer write but not an older one. Everything
// after that read answers truthfully.
type laggingDeltaClient struct {
	linear.Client
	hide  string
	reads int
}

func (c *laggingDeltaClient) ListTeamIssuesUpdatedSince(ctx context.Context, teamKey string, since time.Time) ([]linear.Issue, error) {
	issues, err := c.Client.ListTeamIssuesUpdatedSince(ctx, teamKey, since)
	if err != nil {
		return nil, err
	}
	c.reads++
	if c.reads > 1 {
		return issues, nil
	}
	kept := issues[:0]
	for _, is := range issues {
		if is.ID != c.hide {
			kept = append(kept, is)
		}
	}
	return kept, nil
}

// Done-when: a row a lagging replica hid still reaches the inbox on a later
// pass, without waiting for the resync. Linear answers these reads from
// replicas that do not all hold the same writes, so a delta can come back
// with a newer row and not an older one — and a cursor that then jumped to
// the newer row would leave the older one unread for as long as the resync
// interval. Each read asking from behind the cursor is what closes that.
func TestAttentionRereadsBehindItsCursor(t *testing.T) {
	fake := linear.NewFake()
	lagging := &laggingDeltaClient{Client: fake, hide: "hidden"}
	h := newHarnessWith(t, 1, nil, fake, lagging)
	h.resyncEvery(20)
	h.fake.AddIssue("LERP", linear.Issue{ID: "first", Identifier: "LERP-1", Status: "Backlog"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 1 {
		t.Fatalf("cold start = %+v, want the one ticket", got.Attention)
	}

	// Two tickets arrive a minute apart; the pass that reads them sees only
	// the newer. The gap is what pins the window's size: an overlap trimmed
	// to seconds would no longer reach back over it.
	h.fake.AddIssue("LERP", linear.Issue{ID: "hidden", Identifier: "LERP-2", Status: "Backlog"})
	h.fake.Advance(time.Minute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "newer", Identifier: "LERP-3", Status: "Backlog"})
	h.rec.Tick(ctx)
	got := h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 2 || got[1].Ticket != "LERP-3" {
		t.Fatalf("lagging pass = %+v, want it to have missed LERP-2 and seen LERP-3", got)
	}

	// The next read asks from behind the cursor the newer row moved it to,
	// so the row that was hidden is inside the window.
	h.rec.Tick(ctx)
	got = h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 3 {
		t.Errorf("next pass = %+v, want the hidden ticket to have arrived without a resync", got)
	}
	if lagging.reads != 2 {
		t.Errorf("delta reads = %d, want the two passes after the cold start", lagging.reads)
	}
}

// Done-when: the cursor and the resync counter are per team, and a team whose
// read failed does not take the others' boards down with it. The pass emits
// nothing — a partial inbox must never read as a whole one — but the team
// that answered keeps everything it had.
func TestAttentionKeepsATeamPerBoard(t *testing.T) {
	h, counting := newCountingHarness(t)
	h.serveTeams("LERP", "PROSE")
	h.resyncEvery(20)
	h.fake.AddIssue("LERP", linear.Issue{ID: "ours", Identifier: "LERP-1", Status: "Backlog"})
	h.fake.AddIssue("PROSE", linear.Issue{ID: "theirs", Identifier: "PROSE-1", Status: "Backlog"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 2 {
		t.Fatalf("cold start = %+v, want a ticket from each team", got.Attention)
	}
	if counting.listings.Load() != 4 || counting.deltas.Load() != 0 {
		t.Fatalf("cold start = %d listings, %d deltas, want the pair per team and no delta",
			counting.listings.Load(), counting.deltas.Load())
	}

	h.rec.Tick(ctx)
	h.waitEvents(t, EventAttention, 1)
	if counting.deltas.Load() != 2 {
		t.Errorf("delta reads = %d, want one per team", counting.deltas.Load())
	}
	if counting.listings.Load() != 4 {
		t.Errorf("full listings = %d, want no team re-listed inside its window", counting.listings.Load())
	}

	// The second team's read fails; the first team's already succeeded.
	counting.deltaErr, counting.failTeam = errors.New("boom"), "PROSE"
	h.fake.AddIssue("LERP", linear.Issue{ID: "fresh", Identifier: "LERP-2", Status: "Backlog"})
	h.rec.Tick(ctx)
	h.waitEvents(t, EventError, 1)
	select {
	case ev := <-h.events:
		if ev.Type == EventAttention {
			t.Fatalf("a pass that lost a team emitted an inbox: %+v", ev.Attention)
		}
	default:
	}

	counting.deltaErr = nil
	h.rec.Tick(ctx)
	got := h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 3 {
		t.Errorf("after the recovery = %+v, want both teams' tickets and the new one", got)
	}
	// Without this the assertion above proves nothing: a pass that threw
	// every board away on any team's failure would re-list both teams here
	// and rebuild exactly the same three rows.
	if counting.listings.Load() != 4 {
		t.Errorf("full listings = %d, want the cold start's pair per team and no more: "+
			"a failed read on one team must not cost the others their boards",
			counting.listings.Load())
	}
}

// Done-when: the cursor only ever moves forward, across a re-list included.
// The two full listings return what the inbox can draw, which on a team that
// churns on other people's tickets is older than the last thing the delta
// saw — so a resync that took its cursor from them alone would walk it
// backwards, and the next delta would ask for every issue the team touched
// in between, completed ones included. That is the paging this exists to
// stop, arriving once every resync interval.
func TestAttentionCursorNeverWalksBackwards(t *testing.T) {
	h, counting := newCountingHarness(t)
	h.resyncEvery(1)
	h.fake.AddIssue("LERP", linear.Issue{ID: "ours", Identifier: "LERP-1", Status: "Backlog"})
	ctx := context.Background()

	h.rec.Tick(ctx) // cold start: the cursor is the inbox ticket's stamp
	h.waitEvents(t, EventAttention, 1)

	// An hour later the team churns on a ticket the inbox never draws. The
	// delta sees it — it filters on nothing but the team — so the cursor
	// moves to it, while the two listings still top out an hour earlier.
	h.fake.Advance(time.Hour)
	h.fake.AddIssue("LERP", linear.Issue{ID: "theirs", Identifier: "LERP-9", Status: "Backlog",
		AssigneeID: "somebody-else"})
	h.rec.Tick(ctx) // delta
	h.waitEvents(t, EventAttention, 1)
	h.rec.Tick(ctx) // resync
	h.waitEvents(t, EventAttention, 1)
	h.rec.Tick(ctx) // delta, from wherever the resync left the cursor
	h.waitEvents(t, EventAttention, 1)

	// The delta that ran before the resync moved the cursor to the churned
	// ticket's stamp, so the one after it must ask from there — not from the
	// hour-older stamp the two listings top out at.
	churned := h.issue(t, "theirs").UpdatedAt
	asked := counting.asked()
	if len(asked) != 2 {
		t.Fatalf("delta reads = %d, want the two passes between the re-lists", len(asked))
	}
	if want := churned.Add(-deltaOverlap); asked[1].Before(want) {
		t.Errorf("the delta after the resync asked from %s, want no earlier than %s: "+
			"the re-list walked the cursor back, so this pass re-pages everything the "+
			"team touched in the hour between", asked[1], want)
	}
}

// Done-when: the drift a delta cannot report is bounded by the resync, and
// this is the second kind of it. Linear stamps a completed blocker, not the
// ticket it was blocking, so the blocked row's relations are refreshed by
// nothing until the full re-list rebuilds it. Pinned rather than fixed: the
// window is ResyncEvery passes, nothing in the loop acts on these fields, and
// fill still lists every queue fresh, so what a run picks up is unaffected.
func TestAttentionHealsStaleBlockersOnResync(t *testing.T) {
	h, _ := newCountingHarness(t)
	h.resyncEvery(1)
	h.fake.AddIssue("LERP", linear.Issue{ID: "blocked", Identifier: "LERP-1", Status: "Backlog"})
	// Far enough back that the trailing window a delta re-reads does not
	// reach it. Inside that window a row is re-read every pass and its
	// relations come back fresh by accident; the ticket sitting in the inbox
	// untouched for days is the case this is about.
	h.fake.Advance(time.Hour)
	h.fake.AddIssue("LERP", linear.Issue{ID: "blocker", Identifier: "LERP-2", Status: "Backlog"})
	h.fake.Block("blocked", "blocker")
	ctx := context.Background()

	h.rec.Tick(ctx)
	got := h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 2 || !slices.Equal(got[0].BlockedBy, []string{"LERP-2"}) {
		t.Fatalf("cold start = %+v, want LERP-1 blocked by LERP-2", got)
	}

	// The blocker finishes. It leaves the inbox at once, because the delta
	// carries its own change — but nothing carries the blocked ticket's.
	h.fake.Advance(time.Hour)
	if err := h.fake.MoveIssue(ctx, "blocker", "Done"); err != nil {
		t.Fatal(err)
	}
	h.rec.Tick(ctx)
	got = h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 1 || got[0].Ticket != "LERP-1" {
		t.Fatalf("delta pass = %+v, want the finished blocker gone", got)
	}
	if !slices.Equal(got[0].BlockedBy, []string{"LERP-2"}) {
		t.Errorf("delta pass left LERP-1 blockedBy = %v; if this now reads empty the "+
			"delta learned to refresh a row it was not told about, and the resync "+
			"below is no longer the only healer", got[0].BlockedBy)
	}

	// The re-list rebuilds the row from the board, which no longer says so.
	h.rec.Tick(ctx)
	got = h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 1 || len(got[0].BlockedBy) != 0 {
		t.Errorf("resync pass = %+v, want LERP-1 unblocked", got)
	}
}

// staleListingClient serves the two full listings from a replica that has not
// caught up. It can hold a row back, or hand one over as it was before a
// change the delta has already reported — the two ways the read behind a
// resync can be wrong.
type staleListingClient struct {
	linear.Client
	hide      string        // id the unassigned listing leaves out
	resurrect *linear.Issue // row the unassigned listing hands back anyway
}

func (c *staleListingClient) ListUnassignedIssues(ctx context.Context, teamKey string) ([]linear.Issue, error) {
	issues, err := c.Client.ListUnassignedIssues(ctx, teamKey)
	if err != nil {
		return nil, err
	}
	kept := issues[:0]
	for _, is := range issues {
		if is.ID != c.hide {
			kept = append(kept, is)
		}
	}
	if c.resurrect != nil {
		kept = append(kept, *c.resurrect)
	}
	return kept, nil
}

// Done-when: a resync served from behind does not strand the board. The full
// listings lag exactly as much as the delta does — they are the same replicas
// — and this one hands back a ticket a colleague has since claimed, which the
// delta had already evicted. Because a replica only disagrees about a change
// made recently, that change is inside the window each delta re-reads, so the
// next pass evicts it again rather than the mistake standing until the resync
// after.
func TestAttentionReEvictsWhatAStaleResyncResurrected(t *testing.T) {
	fake := linear.NewFake()
	stale := &staleListingClient{Client: fake}
	h := newHarnessWith(t, 1, nil, fake, stale)
	h.resyncEvery(1)
	fake.AddIssue("LERP", linear.Issue{ID: "taken", Identifier: "LERP-1", Status: "Backlog"})
	fake.AddIssue("LERP", linear.Issue{ID: "stays", Identifier: "LERP-2", Status: "Backlog"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 2 {
		t.Fatalf("cold start = %+v, want both", got.Attention)
	}
	// The row as the lagging replica still holds it, before the claim.
	before := h.issue(t, "taken")
	if err := fake.AssignIssue(ctx, "taken", "somebody-else"); err != nil {
		t.Fatal(err)
	}
	// Half a minute later the team touches something the inbox never draws,
	// which carries the cursor past the claim. Only the window reaches back
	// over it now.
	fake.Advance(30 * time.Second)
	fake.AddIssue("LERP", linear.Issue{ID: "churn", Identifier: "LERP-9", Status: "Backlog",
		AssigneeID: "somebody-else"})

	h.rec.Tick(ctx) // delta: the claim arrives and the row is evicted
	if got := h.waitEvents(t, EventAttention, 1)[0].Attention; len(got) != 1 {
		t.Fatalf("delta pass = %+v, want the claimed ticket gone", got)
	}

	stale.resurrect = &before
	h.rec.Tick(ctx) // resync, from a replica that never saw the claim
	if got := h.waitEvents(t, EventAttention, 1)[0].Attention; len(got) != 2 {
		t.Fatalf("stale resync = %+v, want the resurrection this test is about", got)
	}

	stale.resurrect = nil
	h.rec.Tick(ctx) // delta: the claim is inside the window, so it lands again
	got := h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 1 || got[0].Ticket != "LERP-2" {
		t.Errorf("pass after the stale resync = %+v, want the colleague's ticket evicted "+
			"again; promoting it would move a ticket that is not the operator's", got)
	}
}

// The other direction of the same lag: a re-list that comes back short drops
// a row the inbox should still be drawing. The row is one changed recently —
// that is what makes replicas disagree about it — so it is inside the window
// too, and the next pass puts it back.
func TestAttentionRestoresWhatAShortResyncDropped(t *testing.T) {
	fake := linear.NewFake()
	stale := &staleListingClient{Client: fake}
	h := newHarnessWith(t, 1, nil, fake, stale)
	h.resyncEvery(1)
	fake.AddIssue("LERP", linear.Issue{ID: "flaps", Identifier: "LERP-1", Status: "Backlog"})
	fake.AddIssue("LERP", linear.Issue{ID: "stays", Identifier: "LERP-2", Status: "Backlog"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 2 {
		t.Fatalf("cold start = %+v, want both", got.Attention)
	}
	h.rec.Tick(ctx) // delta
	h.waitEvents(t, EventAttention, 1)

	stale.hide = "flaps"
	h.rec.Tick(ctx) // resync, from a replica missing the row
	if got := h.waitEvents(t, EventAttention, 1)[0].Attention; len(got) != 1 {
		t.Fatalf("short resync = %+v, want the dropped row this test is about", got)
	}

	stale.hide = ""
	h.rec.Tick(ctx) // delta: the row is inside the window, so it comes back
	got := h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 2 {
		t.Errorf("pass after the short resync = %+v, want the dropped ticket back in the "+
			"inbox rather than waiting for the resync after", got)
	}
}

// Done-when: a delta that keeps failing falls back to the two listings
// instead of retrying forever. Nothing here consumes RateLimitError — that is
// another ticket — so a 429, a gateway error or a query Linear stopped
// accepting is a hard failure on every pass, and a counter that only counted
// successes would never let the re-list run again. The inbox would freeze at
// whatever the cold start saw for the life of the process.
func TestAttentionFallsBackToListingWhenTheDeltaKeepsFailing(t *testing.T) {
	h, counting := newCountingHarness(t)
	h.resyncEvery(2)
	h.fake.AddIssue("LERP", linear.Issue{ID: "first", Identifier: "LERP-1", Status: "Backlog"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	h.waitEvents(t, EventAttention, 1)

	// From here the delta never works again, and a ticket arrives.
	counting.deltaErr = errors.New("boom")
	h.fake.AddIssue("LERP", linear.Issue{ID: "arrived", Identifier: "LERP-2", Status: "Needs Help",
		AssigneeID: "fake-viewer"})

	// Two failed attempts, then the pass gives up on the delta and re-lists.
	var got []AttentionItem
	attentions := 0
	for pass := 0; pass < 3; pass++ {
		h.rec.Tick(ctx)
		// A pass emits everything it has to say before Tick returns, so
		// whatever is in the channel now is the whole of this pass.
		for drained := false; !drained; {
			select {
			case ev := <-h.events:
				switch ev.Type {
				case EventAttention:
					got, attentions = ev.Attention, attentions+1
				case EventError:
					if !strings.Contains(ev.Err.Error(), "boom") {
						t.Fatalf("error event = %v, want the delta's own failure", ev.Err)
					}
				}
			default:
				drained = true
			}
		}
	}
	if attentions != 1 {
		t.Errorf("inboxes emitted across the three passes = %d, want only the fallback's: "+
			"a pass whose read failed must emit nothing", attentions)
	}
	if len(got) != 2 {
		t.Errorf("inbox after the fallback = %+v, want the ticket that arrived while the "+
			"delta was down; a delta counted only when it succeeds never lets the "+
			"re-list run again", got)
	}
	if counting.listings.Load() != 4 {
		t.Errorf("full listings = %d, want the cold start's pair and the fallback's",
			counting.listings.Load())
	}
	if counting.deltas.Load() != 2 {
		t.Errorf("delta attempts = %d, want ResyncEvery of them before giving up",
			counting.deltas.Load())
	}
}

// Done-when: a ticket moved between two served teams is drawn twice until the
// team it left re-lists — the third entry in relistBoard's catalogue of drift
// only a re-list repairs. The delta filters on the team, so the row it left
// behind is never mentioned again.
func TestAttentionDrawsACrossTeamMoveTwiceUntilTheResync(t *testing.T) {
	h, _ := newCountingHarness(t)
	h.serveTeams("LERP", "PROSE")
	h.resyncEvery(2)
	h.fake.AddIssue("LERP", linear.Issue{ID: "moves", Identifier: "LERP-1", Status: "Backlog"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 1 {
		t.Fatalf("cold start = %+v, want the one ticket", got.Attention)
	}

	// The same issue, now filed under the other team and renumbered.
	h.fake.AddIssue("PROSE", linear.Issue{ID: "moves", Identifier: "PROSE-7", Status: "Backlog"})

	h.rec.Tick(ctx) // delta: PROSE gains it, LERP is never told
	got := h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 2 {
		t.Fatalf("after the move = %+v, want the same work drawn under both identifiers", got)
	}

	h.rec.Tick(ctx) // second delta, still both
	h.waitEvents(t, EventAttention, 1)
	h.rec.Tick(ctx) // the re-list drops the row LERP no longer holds
	got = h.waitEvents(t, EventAttention, 1)[0].Attention
	if len(got) != 1 || got[0].Ticket != "PROSE-7" {
		t.Errorf("after the re-list = %+v, want only the identifier it lives under now", got)
	}
}

// Done-when: an empty board never sends a delta. Its cursor comes from the
// rows the listing returned, so an empty listing leaves it at the zero time —
// and "everything since the beginning of time", against a query filtered by
// neither state nor assignee, is the team's entire history including every
// completed ticket. The two listings for that board are two empty pages, so
// asking them again is the cheap answer as well as the correct one.
func TestAttentionDoesNotDeltaFromAZeroCursor(t *testing.T) {
	h, counting := newCountingHarness(t)
	h.resyncEvery(20)
	// On the board but never in the inbox: somebody else's, and finished.
	h.fake.AddIssue("LERP", linear.Issue{ID: "theirs", Identifier: "LERP-1", Status: "Backlog",
		AssigneeID: "somebody-else"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "done", Identifier: "LERP-2", Status: "Done"})
	ctx := context.Background()

	for range 3 {
		h.rec.Tick(ctx)
		if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 0 {
			t.Fatalf("inbox = %+v, want it empty", got.Attention)
		}
	}
	if got := counting.deltas.Load(); got != 0 {
		t.Errorf("delta reads = %d, want none: there is no cursor to ask from", got)
	}
	if got := counting.listings.Load(); got != 6 {
		t.Errorf("full listings = %d, want the pair each pass", got)
	}

	// The moment a ticket the inbox can draw appears, the listing has a row
	// to take a cursor from and the pass goes back on the delta.
	h.fake.AddIssue("LERP", linear.Issue{ID: "loose", Identifier: "LERP-3", Status: "Backlog"})
	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventAttention, 1)[0]; len(got.Attention) != 1 {
		t.Fatalf("inbox = %+v, want the new ticket", got.Attention)
	}
	h.rec.Tick(ctx)
	h.waitEvents(t, EventAttention, 1)
	if got := counting.deltas.Load(); got != 1 {
		t.Errorf("delta reads = %d, want the one pass that had a cursor", got)
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

// Done-when: with every lane busy, force-starting a queued ticket runs it
// past the limit — and the forced run concludes exactly as an ordinary one
// does: claimed, moved by the queue's rule, record removed, workspace
// disposed.
func TestForceStartRunsPastTheLaneLimitAndConcludes(t *testing.T) {
	execute, release, ran := blockingExecute(t, "")
	h := newHarness(t, 1, execute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "two", Identifier: "LERP-2", Status: "Todo"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	if got := h.waitEvents(t, EventStarted, 1); got[0].Lane != 1 || got[0].TicketID != "one" {
		t.Fatalf("first start = %+v, want LERP-1 in lane 1", got[0])
	}
	if got := h.issue(t, "two"); got.AssigneeID != "" {
		t.Fatalf("ticket beyond the limit = %+v, want it still waiting unclaimed", got)
	}

	if err := h.rec.ForceStart(ctx, "two"); err != nil {
		t.Fatalf("ForceStart: %v", err)
	}
	forced := h.waitEvents(t, EventStarted, 1)[0]
	if forced.TicketID != "two" || forced.Lane != 2 {
		t.Fatalf("forced start = %+v, want LERP-2 on lane 2, one above the limit", forced)
	}
	record, err := h.evidence.Read(forced.RunID)
	if err != nil {
		t.Fatalf("forced run's record: %v", err)
	}
	if record.Lane != 2 || record.Queue != "todo" || record.StartingStatus != "Todo" {
		t.Errorf("forced run's record = %+v, want an ordinary record on lane 2", record)
	}
	if got := h.issue(t, "two"); got.AssigneeID != "fake-viewer" {
		t.Errorf("forced ticket = %+v, want it claimed: the claim protocol still runs", got)
	}

	release()
	h.waitEvents(t, EventExited, 2)
	waitIdle(t, h.rec)

	// Settled by the queue's own rule, claim and all: "Done" is a pipeline
	// exit no queue serves, so conclude parks the ticket there and releases
	// the claim — exactly what an ordinary run in this queue does.
	if got := h.issue(t, "two"); got.Status != "Done" || got.AssigneeID != "" {
		t.Errorf("forced ticket after its run = %+v, want it parked in Done unclaimed", got)
	}
	assertReaped(t, h, record)
	if got := ran(); len(got) != 2 || !slices.Contains(got, "LERP-2") {
		t.Errorf("executed runs = %v, want both tickets", got)
	}
}

// A forced run is an ordinary run in every later pass: the lerp that finds
// its record knows nothing about how it started, adopts it on its
// out-of-range lane, and reaps it when the process dies.
func TestForceStartRunIsAdoptedAndReapedOnItsOutOfRangeLane(t *testing.T) {
	execute, release, _ := blockingExecute(t, "")
	h := newHarness(t, 1, execute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "two", Identifier: "LERP-2", Status: "Todo"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	h.waitEvents(t, EventStarted, 1)
	if err := h.rec.ForceStart(ctx, "two"); err != nil {
		t.Fatalf("ForceStart: %v", err)
	}
	forced := h.waitEvents(t, EventStarted, 1)[0]
	record, err := h.evidence.Read(forced.RunID)
	if err != nil {
		t.Fatal(err)
	}

	// A second lerp over the same clone, with a smaller idea of capacity than
	// the lane it is about to inherit.
	next := newSecondLerp(t, h, 1)
	next.alive[record.RunID] = true
	next.rec.Tick(ctx)
	adopted := waitEventsOn(t, next.events, EventAdopted, 1)[0]
	if adopted.RunID != record.RunID || adopted.Lane != 2 {
		t.Fatalf("adopted event = %+v, want the forced run on lane 2", adopted)
	}
	if free := next.rec.freeLanes(); len(free) != 0 {
		t.Errorf("free lanes under an adopted out-of-range run = %v, want none", free)
	}

	// Its process dies: the reap is the ordinary one, workspace and all.
	next.alive[record.RunID] = false
	next.rec.Tick(ctx)
	waitEventsOn(t, next.events, EventReaped, 1)
	if _, err := h.evidence.Read(record.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reaped forced record read error = %v, want not exist", err)
	}
	release()
	waitIdle(t, h.rec)
}

// The one claim force-start may take back: the operating user's own, left on a
// ticket in a status a queue serves by a run that nothing was left to reap. No
// pass picks an assigned ticket up and the inbox does not list a served status,
// so without this the ticket is reachable by nothing lerp has (LERP-113).
func TestForceStartTakesOverTheOperatingUsersOwnClaim(t *testing.T) {
	ctx := context.Background()
	execute, ran := recordingExecute("")
	h := newHarness(t, 2, execute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	if err := h.fake.AssignIssue(ctx, "one", "fake-viewer"); err != nil {
		t.Fatal(err)
	}
	if Eligible(h.issue(t, "one"), map[string]bool{"Todo": true}) {
		t.Fatal("the fixture is not the stranded state: an ordinary pass would pick this up")
	}

	if err := h.rec.ForceStart(ctx, "one"); err != nil {
		t.Fatalf("ForceStart on the operator's own claim = %v, want it to run", err)
	}
	h.waitEvents(t, EventExited, 1)
	waitIdle(t, h.rec)

	if got := ran(); len(got) != 1 || got[0] != "LERP-1" {
		t.Errorf("executed runs = %v, want the stranded ticket re-run", got)
	}
	if got := h.issue(t, "one"); got.Status != "Done" {
		t.Errorf("ticket after the takeover = %+v, want the queue's own move rule applied", got)
	}
}

// The fence: force-start overrides the lane count and nothing else. Each
// refusal must leave the board and the evidence store exactly as it found
// them — no claim, no record, no lane.
func TestForceStartRefusals(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(t *testing.T, h *harness)
		want  string
		// lanes still occupied once the refusal has come back — zero
		// everywhere except the case whose setup occupies one itself.
		lanes int
	}{{
		name: "blocked names its blocker",
		setup: func(_ *testing.T, h *harness) {
			h.fake.AddIssue("LERP", linear.Issue{ID: "blocker", Identifier: "LERP-9", Status: "Todo"})
			h.fake.Block("one", "blocker")
		},
		want: "blocked by LERP-9",
	}, {
		name: "claimed by someone else",
		setup: func(_ *testing.T, h *harness) {
			if err := h.fake.AssignIssue(ctx, "one", "someone-else"); err != nil {
				panic(err)
			}
		},
		want: "claimed by someone else",
	}, {
		name: "already occupying a lane here",
		setup: func(t *testing.T, h *harness) {
			if _, ok := h.rec.register(1, "one"); !ok {
				t.Fatal("register: the lane was not free")
			}
		},
		want:  "already running here",
		lanes: 1,
	}, {
		name: "resting in a status no queue serves",
		setup: func(_ *testing.T, h *harness) {
			if err := h.fake.MoveIssue(ctx, "one", "Needs Help"); err != nil {
				panic(err)
			}
		},
		want: `no queue serves "Needs Help"`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execute, ran := recordingExecute("")
			h := newHarness(t, 2, execute)
			h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
			tc.setup(t, h)

			err := h.rec.ForceStart(ctx, "one")
			if err == nil {
				t.Fatalf("ForceStart on a %s ticket = nil, want a refusal", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %q, want it to contain %q", err, tc.want)
			}
			waitIdle(t, h.rec)
			records, listErr := h.evidence.List()
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(records) != 0 {
				t.Errorf("run records after a refusal = %d, want 0", len(records))
			}
			if got := ran(); len(got) != 0 {
				t.Errorf("executed runs after a refusal = %v, want none", got)
			}
			if got := h.issue(t, "one"); got.AssigneeID == "fake-viewer" {
				t.Errorf("refused ticket = %+v, want no claim made", got)
			}
			// A refusal that left a lane behind would shrink capacity for
			// the rest of the session without failing anything else here:
			// waitIdle waits on the WaitGroup, which a leaked laneRun never
			// touches.
			h.rec.mu.Lock()
			lanes := len(h.rec.active)
			h.rec.mu.Unlock()
			if lanes != tc.lanes {
				t.Errorf("lanes occupied after a refusal = %d, want %d", lanes, tc.lanes)
			}
		})
	}
}

// Over capacity throttles harder, never wraps into starting more: fill
// computes capacity as Lanes - len(active), and a negative capacity must
// yield no free lanes rather than every lane.
func TestFillStartsNothingWhileOverCapacity(t *testing.T) {
	execute, release, ran := blockingExecute(t, "")
	h := newHarness(t, 1, execute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "two", Identifier: "LERP-2", Status: "Todo"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	h.waitEvents(t, EventStarted, 1)
	if err := h.rec.ForceStart(ctx, "two"); err != nil {
		t.Fatalf("ForceStart: %v", err)
	}
	h.waitEvents(t, EventStarted, 1)

	// A third ticket arrives while two runs occupy a one-lane board.
	h.fake.AddIssue("LERP", linear.Issue{ID: "three", Identifier: "LERP-3", Status: "Todo"})
	h.drainEvents()
	if free := h.rec.freeLanes(); len(free) != 0 {
		t.Fatalf("free lanes over capacity = %v, want none", free)
	}
	h.rec.Tick(ctx)
	for _, ev := range h.drainEvents() {
		if ev.Type == EventStarted {
			t.Fatalf("an over-capacity pass started %+v", ev)
		}
	}
	if got := h.issue(t, "three"); got.Status != "Todo" || got.AssigneeID != "" {
		t.Errorf("ticket queued behind an over-capacity board = %+v, want unclaimed in Todo", got)
	}

	release()
	h.waitEvents(t, EventExited, 2)
	waitIdle(t, h.rec)
	if got := ran(); slices.Contains(got, "LERP-3") {
		t.Errorf("executed runs = %v, want the third ticket never started", got)
	}
}

// A lane number is one run's, and force-start is the first thing that
// chooses one off the tick goroutine. fill takes its numbers from a
// freeLanes snapshot and registers them one at a time, so a force-start
// landing in that window can take a lane the snapshot still calls free. Two
// live runs on one number would share the LERP_LANE a project's provision
// isolates on, and the TUI would lose the first run's row to the second.
func TestRegisterRefusesALaneAForceStartTook(t *testing.T) {
	h := newHarness(t, 2, nil)

	if _, ok := h.rec.register(1, "one"); !ok {
		t.Fatal("register lane 1: the lane was not free")
	}
	lanes := h.rec.freeLanes() // what a pass would be holding
	if len(lanes) != 1 || lanes[0] != 2 {
		t.Fatalf("free lanes = %v, want [2]", lanes)
	}

	lr, ok := h.rec.registerForce("forced", nil)
	if !ok {
		t.Fatal("registerForce: no lane")
	}
	if lr.lane != 2 {
		t.Fatalf("forced lane = %d, want the free one", lr.lane)
	}

	// The pass proceeds from its now-stale snapshot.
	if _, ok := h.rec.register(lanes[0], "candidate"); ok {
		t.Fatal("register handed out a lane a forced run already holds")
	}
	// And the pass is not over: fill reads the lanes again per candidate, so
	// a taken number costs one candidate rather than every one behind it.
	if free := h.rec.freeLanes(); len(free) != 0 {
		t.Fatalf("free lanes with both lanes occupied = %v, want none", free)
	}

	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	held := map[int]string{}
	for _, a := range h.rec.active {
		if prev, dup := held[a.lane]; dup {
			t.Fatalf("lane %d held by both %s and %s", a.lane, prev, a.ticketID)
		}
		held[a.lane] = a.ticketID
	}
}

// An orphan's lane is occupied whether or not this process has adopted it
// yet. Every other lane number is chosen on the tick goroutine, after
// reconcileEvidence has adopted everything live; force-start chooses between
// passes, so it must read the same evidence — otherwise pressing S in that
// window puts a second run on the lane an orphan is already using, and both
// get the same LERP_LANE a project's provision isolates on.
func TestForceStartAvoidsALaneAnUnadoptedOrphanHolds(t *testing.T) {
	execute, release, _ := blockingExecute(t, "")
	h := newHarness(t, 2, execute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "two", Identifier: "LERP-2", Status: "Todo"})
	ctx := context.Background()

	// A previous lerp's run, live on lane 1, that no pass here has adopted.
	orphan, err := h.evidence.Create(evidence.Record{
		Lane: 1, TicketID: "orphan-ticket", Queue: "todo", StartingStatus: "Todo",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.alive[orphan.RunID] = true

	if err := h.rec.ForceStart(ctx, "two"); err != nil {
		t.Fatalf("ForceStart: %v", err)
	}
	forced := h.waitEvents(t, EventStarted, 1)[0]
	if forced.Lane == orphan.Lane {
		t.Fatalf("forced run took lane %d, which a live orphan already holds", forced.Lane)
	}
	if forced.Lane != 2 {
		t.Errorf("forced lane = %d, want the lowest one no live run holds", forced.Lane)
	}

	// And the pass that follows adopts the orphan onto its own lane, with no
	// two live runs sharing a number.
	h.rec.Tick(ctx)
	h.waitEvents(t, EventAdopted, 1)
	h.rec.mu.Lock()
	held := map[int]string{}
	for _, a := range h.rec.active {
		if prev, dup := held[a.lane]; dup {
			t.Errorf("lane %d held by both %s and %s", a.lane, prev, a.ticketID)
		}
		held[a.lane] = a.ticketID
	}
	h.rec.mu.Unlock()

	release()
	waitIdle(t, h.rec)
}

// Evidence this process cannot read is the one state reconcileEvidence bails
// on, because live orphans may hold lanes it knows nothing about — and the
// pass that bailed adopted nothing, so r.active is empty too. Force-start is
// not allowed to be the one path that fills anyway: it refuses, and the
// operator gets the reason.
func TestForceStartRefusesWhenTheEvidenceCannotBeRead(t *testing.T) {
	execute, ran := recordingExecute("")
	h := newHarness(t, 2, execute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "two", Identifier: "LERP-2", Status: "Todo"})
	ctx := context.Background()

	// A live run from a previous lerp holds lane 1, and then the store stops
	// being readable.
	orphan, err := h.evidence.Create(evidence.Record{
		Lane: 1, TicketID: "orphan-ticket", Queue: "todo", StartingStatus: "Todo",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.alive[orphan.RunID] = true
	runs := filepath.Join(h.root, ".lerp", "runs")
	if err := os.Chmod(runs, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(runs, 0o700) })
	if _, err := h.evidence.List(); err == nil {
		t.Skip("the run store is still readable; not running as this test's user")
	}

	err = h.rec.ForceStart(ctx, "two")
	if err == nil {
		t.Fatal("ForceStart with unreadable evidence = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "list run evidence") {
		t.Errorf("refusal = %q, want it to name the read that failed", err)
	}
	waitIdle(t, h.rec)
	if got := ran(); len(got) != 0 {
		t.Errorf("executed runs after the refusal = %v, want none", got)
	}
	h.rec.mu.Lock()
	lanes := len(h.rec.active)
	h.rec.mu.Unlock()
	if lanes != 0 {
		t.Errorf("lanes occupied after the refusal = %d, want 0", lanes)
	}
	if got := h.issue(t, "two"); got.AssigneeID == "fake-viewer" {
		t.Errorf("refused ticket = %+v, want no claim made", got)
	}
}

// A forced run occupies a lane, it does not close the board: the pass that
// follows still fills what is left.
func TestFillFillsTheLanesAForcedRunLeaves(t *testing.T) {
	execute, release, ran := blockingExecute(t, "")
	h := newHarness(t, 3, execute)
	for _, id := range []string{"one", "two", "three"} {
		h.fake.AddIssue("LERP", linear.Issue{ID: id, Identifier: "LERP-" + id, Status: "Todo"})
	}
	ctx := context.Background()

	if err := h.rec.ForceStart(ctx, "three"); err != nil {
		t.Fatalf("ForceStart: %v", err)
	}
	h.waitEvents(t, EventStarted, 1)

	h.rec.Tick(ctx)
	started := h.waitEvents(t, EventStarted, 2)
	lanes := map[int]bool{}
	for _, ev := range started {
		if lanes[ev.Lane] {
			t.Fatalf("two runs started on lane %d", ev.Lane)
		}
		lanes[ev.Lane] = true
	}
	if free := h.rec.freeLanes(); len(free) != 0 {
		t.Errorf("free lanes on a full board = %v, want none", free)
	}

	release()
	waitIdle(t, h.rec)
	if got := ran(); len(got) != 3 {
		t.Errorf("executed runs = %v, want all three", got)
	}
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
