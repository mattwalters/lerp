package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
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
	events   chan Event
	alive    map[string]bool // run ID → the recorded process is "alive"

	mu       sync.Mutex
	disposed []workspace.Identity
}

func newHarness(t *testing.T, lanes int, execute ExecuteFunc) *harness {
	t.Helper()
	h := &harness{
		fake:     linear.NewFake(),
		evidence: evidence.New(t.TempDir()),
		events:   make(chan Event, 64),
		alive:    map[string]bool{},
	}
	if execute == nil {
		execute = func(context.Context, run.Invocation) (run.Result, error) {
			return run.Result{ExitCode: 0}, nil
		}
	}
	rec, err := NewReconciler(ReconcilerOptions{
		Client:   h.fake,
		Repo:     testRepo(),
		RepoDir:  "/repo",
		Evidence: h.evidence,
		Lanes:    lanes,
		Events:   func(ev Event) { h.events <- ev },
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
	deadline := time.After(5 * time.Second)
	var got []Event
	for len(got) < n {
		select {
		case ev := <-h.events:
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
	var got []Event
	for {
		select {
		case ev := <-h.events:
			got = append(got, ev)
		default:
			return got
		}
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
	if records, _ := h.evidence.List(); len(records) != 0 {
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
	var mu sync.Mutex
	var executed []string
	execute := func(_ context.Context, inv run.Invocation) (run.Result, error) {
		mu.Lock()
		executed = append(executed, inv.Ticket)
		mu.Unlock()
		return run.Result{ExitCode: 0}, nil
	}
	h := newHarness(t, 1, execute)
	record, err := h.evidence.Create(evidence.Record{
		Lane: 1, TicketID: "orphan", Queue: "todo", StartingStatus: "Todo",
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
		case EventAdopted:
			adopted++
			if ev.RunID != record.RunID || ev.Lane != 1 || ev.LogPath != record.LogPath {
				t.Errorf("adopted event = %+v, want the orphan's record with its log path", ev)
			}
		case EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		default:
			t.Fatalf("unexpected %s event while the adopted run fills the only lane: %+v", ev.Type, ev)
		}
	}
	if adopted != 1 {
		t.Fatalf("adopted events across two ticks = %d, want exactly 1", adopted)
	}
	mu.Lock()
	if len(executed) != 0 {
		t.Fatalf("executed runs = %v, want none: adoption must not restart, and the lane is full", executed)
	}
	mu.Unlock()
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
	if _, err := h.evidence.Read(record.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reaped record read error = %v, want not exist", err)
	}
	if got := h.disposedIdentities(); len(got) == 0 || got[0].Workspace != record.Workspace {
		t.Errorf("reap disposed %v, want the dead run's workspace %q", got, record.Workspace)
	}
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
	if _, err := h.evidence.Read(record.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reaped record read error = %v, want not exist", err)
	}
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
	for _, runID := range []string{moved.RunID, theirs.RunID} {
		if _, err := h.evidence.Read(runID); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("record %s read error = %v, want not exist", runID, err)
		}
	}
	if got := h.issue(t, "moved"); got.Status != "Escalated" || got.AssigneeID != "fake-viewer" {
		t.Errorf("moved ticket = %+v, want its post-move state untouched", got)
	}
	if got := h.issue(t, "theirs"); got.Status != "Todo" || got.AssigneeID != "somebody-else" {
		t.Errorf("someone else's claim = %+v, want untouched", got)
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
