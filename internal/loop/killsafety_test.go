package loop

// Kill-safety acceptance tests (LERP-10): SCOPE invariant 3 — every queue
// run is safe to kill and restart from its beginning — proven end to end
// against the fake Linear client and stub runners, with invariants 1 and 4
// verified along the way. Each scenario asserts on final board state.
//
// A kill -9 of lerp itself cannot be staged in-process, but everything it
// leaves behind can: run records under .lerp/runs whose agent processes are
// still alive. Where a scenario's point is a real kill, the agent is a real
// operating-system process and liveness is judged by the real evidence.Alive
// check; everything else stays stubbed and deterministic.

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/run"
	"github.com/mattwalters/lerp/internal/workspace"
)

// fakeAgent is a real process standing in for a coding agent: cat with its
// stdin held open runs until told otherwise. kill9 is the crash; finish is a
// clean exit. Both wait for the process so its PID is released before the
// test goes on — no timing, no flakes.
type fakeAgent struct {
	t     *testing.T
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func startFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	cmd := exec.Command("cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	a := &fakeAgent{t: t, cmd: cmd, stdin: stdin}
	t.Cleanup(a.stop)
	return a
}

func (a *fakeAgent) pid() int { return a.cmd.Process.Pid }

func (a *fakeAgent) alive() bool { return syscall.Kill(a.pid(), 0) == nil }

// kill9 is the crash mid-run: SIGKILL, then reap the child so the PID is
// gone and the real Alive check reads the run as dead.
func (a *fakeAgent) kill9() {
	a.t.Helper()
	if err := syscall.Kill(a.pid(), syscall.SIGKILL); err != nil {
		a.t.Fatal(err)
	}
	_ = a.cmd.Wait()
}

// finish is a clean exit: closing stdin lets cat run to completion.
func (a *fakeAgent) finish() {
	a.t.Helper()
	if err := a.stdin.Close(); err != nil {
		a.t.Fatal(err)
	}
	if err := a.cmd.Wait(); err != nil {
		a.t.Fatal(err)
	}
}

// stop reaps the process at test end in whatever state the test left it.
func (a *fakeAgent) stop() {
	_ = syscall.Kill(a.pid(), syscall.SIGKILL)
	_ = a.cmd.Wait()
}

// orphanRecord is the evidence a previous lerp would have left behind for a
// still-live agent: a record created and then attached to the agent's PID.
func orphanRecord(t *testing.T, h *harness, lane int, ticketID string, agent *fakeAgent) evidence.Record {
	t.Helper()
	record, err := h.evidence.Create(evidence.Record{
		Lane: lane, TicketID: ticketID, Queue: "todo", StartingStatus: "Todo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.evidence.Attach(record.RunID, agent.pid()); err != nil {
		t.Fatal(err)
	}
	return record
}

// Scenario 1 — kill -9 the agent mid-run. The next tick reaps the dead run:
// workspace disposed, claim released, ticket still in its queue status. The
// tick after picks the ticket straight back up; the worst case is a re-run
// stage.
func TestKillSafetyAgentKilledMidRun(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var reruns []string
	execute := func(_ context.Context, inv run.Invocation) (run.Result, error) {
		mu.Lock()
		reruns = append(reruns, inv.Ticket)
		mu.Unlock()
		<-release
		return run.Result{ExitCode: 0}, nil
	}
	h := newHarness(t, 1, execute)
	h.rec.o.Alive = evidence.Alive // the kill below must register as a real death
	agent := startFakeAgent(t)
	record := orphanRecord(t, h, 1, "tkt", agent)
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "tkt", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})
	ctx := context.Background()

	// While the agent lives, the loop adopts it — remembering, not touching.
	h.rec.Tick(ctx)
	adopted := h.waitEvents(t, EventAdopted, 1)
	if adopted[0].RunID != record.RunID {
		t.Fatalf("adopted event = %+v, want the live run's record", adopted[0])
	}
	if !agent.alive() {
		t.Fatal("adoption killed the agent")
	}

	agent.kill9()

	// The next tick reaps: workspace disposed, record removed, and the ticket
	// exactly where the dead run found it — its queue status, claim released.
	h.rec.Tick(ctx)
	h.waitEvents(t, EventReaped, 1)
	if _, err := h.evidence.Read(record.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reaped record read error = %v, want not exist", err)
	}
	if got := h.disposedIdentities(); len(got) == 0 || got[0].Workspace != record.Workspace {
		t.Errorf("reap disposed %v, want the dead run's workspace %q", got, record.Workspace)
	}

	// The same pass re-picks the ticket. The run starts from its beginning:
	// while the new agent works, the ticket is back in its queue status,
	// claimed again — never lost, never in an in-between state.
	h.waitEvents(t, EventStarted, 1)
	if got := h.issue(t, "tkt"); got.Status != "Todo" || got.AssigneeID != "fake-viewer" {
		t.Errorf("re-picked ticket = %+v, want claimed in Todo", got)
	}
	close(release)
	h.waitEvents(t, EventExited, 1)

	if got := h.issue(t, "tkt"); got.Status != "Done" {
		t.Errorf("final status = %q, want Done", got.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reruns) != 1 || reruns[0] != "LERP-1" {
		t.Errorf("re-runs = %v, want exactly one re-run of LERP-1", reruns)
	}
}

// Scenario 2 — kill -9 lerp itself mid-run. The restarted lerp adopts the
// still-live agents, and each finished run's move rule is applied correctly:
// an agent that moved its own ticket has decided, and the reap leaves that
// decision alone; an agent that exited without concluding cannot report an
// exit code to a lerp that was not its parent, so its ticket is released back
// to its queue and re-run from the beginning — the tolerated worst case.
func TestKillSafetyLerpKilledMidRun(t *testing.T) {
	var mu sync.Mutex
	var reruns []string
	execute := func(_ context.Context, inv run.Invocation) (run.Result, error) {
		mu.Lock()
		reruns = append(reruns, inv.Ticket)
		mu.Unlock()
		return run.Result{ExitCode: 0}, nil
	}
	h := newHarness(t, 2, execute)
	h.rec.o.Alive = evidence.Alive
	mover := startFakeAgent(t)
	silent := startFakeAgent(t)
	moverRecord := orphanRecord(t, h, 1, "moved", mover)
	silentRecord := orphanRecord(t, h, 2, "silent", silent)
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "moved", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "silent", Identifier: "LERP-2", Status: "Todo", AssigneeID: "fake-viewer",
	})
	ctx := context.Background()

	// The restart: both live runs adopted, neither restarted or reaped.
	h.rec.Tick(ctx)
	adopted := map[string]bool{}
	for _, ev := range h.waitEvents(t, EventAdopted, 2) {
		adopted[ev.RunID] = true
	}
	if !adopted[moverRecord.RunID] || !adopted[silentRecord.RunID] {
		t.Fatalf("adopted runs = %v, want both orphans", adopted)
	}
	mu.Lock()
	if len(reruns) != 0 {
		t.Fatalf("adoption executed %v, want nothing: the live agents keep their runs", reruns)
	}
	mu.Unlock()

	// One agent concludes by moving its ticket — a branch, not on_success —
	// and exits cleanly. The other exits cleanly without concluding.
	if err := h.fake.MoveIssue(ctx, "moved", "Escalated"); err != nil {
		t.Fatal(err)
	}
	mover.finish()
	silent.finish()

	// Both runs are reaped. The concluded ticket keeps the agent's move —
	// on_success is "only if the agent didn't", never a verdict — while the
	// unconcluded one is released and re-run through the whole stage.
	h.rec.Tick(ctx)
	h.waitEvents(t, EventReaped, 2)
	started := h.waitEvents(t, EventStarted, 1)
	if started[0].TicketID != "silent" {
		t.Fatalf("restarted ticket = %+v, want the unconcluded one", started[0])
	}
	h.waitEvents(t, EventExited, 1)

	if got := h.issue(t, "moved"); got.Status != "Escalated" || got.AssigneeID != "fake-viewer" {
		t.Errorf("concluded ticket = %+v, want the agent's own move and claim kept", got)
	}
	if got := h.issue(t, "silent"); got.Status != "Done" {
		t.Errorf("re-run ticket status = %q, want Done", got.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reruns) != 1 || reruns[0] != "LERP-2" {
		t.Errorf("re-runs = %v, want exactly the unconcluded ticket", reruns)
	}
	if records, _ := h.evidence.List(); len(records) != 0 {
		t.Errorf("run records after everything settled = %d, want 0", len(records))
	}
}

// Scenario 3 — rm -rf .lerp/runs under a live agent. Local evidence is
// disposable (SCOPE invariant 1): losing it may cost compute — an orphaned
// process, a re-run stage — but never a ticket. Two consequences, both
// asserted: the lerp that owns the run still settles it correctly, and a
// lerp restarted after the deletion leaves the claimed ticket untouched on
// the board while the recordless agent runs on as a documented orphan.
func TestKillSafetyRunEvidenceDeletedUnderLiveAgent(t *testing.T) {
	// The owning lerp: its agent is mid-run when the evidence vanishes. The
	// run settles from memory — ticket moved by the move rule, no reap, no
	// double start.
	release := make(chan struct{})
	execute := func(context.Context, run.Invocation) (run.Result, error) {
		<-release
		return run.Result{ExitCode: 0}, nil
	}
	h := newHarness(t, 1, execute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	h.waitEvents(t, EventStarted, 1)
	if err := os.RemoveAll(filepath.Join(h.root, ".lerp", "runs")); err != nil {
		t.Fatal(err)
	}
	h.rec.Tick(ctx)
	h.rec.Tick(ctx)
	for _, ev := range h.drainEvents() {
		t.Errorf("tick over deleted evidence emitted %s: %+v", ev.Type, ev)
	}
	close(release)
	h.waitEvents(t, EventExited, 1)
	if got := h.issue(t, "one"); got.Status != "Done" {
		t.Errorf("owned run's ticket = %+v, want Done despite the deleted evidence", got)
	}

	// A lerp restarted after the deletion: the agent is alive but recordless,
	// so nothing can adopt it — an orphaned process, the documented cost. The
	// ticket is neither lost nor corrupted: still claimed, still in its queue
	// status, and ineligible until someone settles it. Lerp never guesses.
	var mu sync.Mutex
	var reruns []string
	h2 := newHarness(t, 1, func(_ context.Context, inv run.Invocation) (run.Result, error) {
		mu.Lock()
		reruns = append(reruns, inv.Ticket)
		mu.Unlock()
		return run.Result{ExitCode: 0}, nil
	})
	h2.rec.o.Alive = evidence.Alive
	orphan := startFakeAgent(t)
	h2.fake.AddIssue("LERP", linear.Issue{
		ID: "claimed", Identifier: "LERP-2", Status: "Todo", AssigneeID: "fake-viewer",
	})

	h2.rec.Tick(ctx)
	h2.rec.Tick(ctx)
	for _, ev := range h2.drainEvents() {
		t.Errorf("restarted lerp emitted %s over the orphan's ticket: %+v", ev.Type, ev)
	}
	if !orphan.alive() {
		t.Error("orphaned agent was killed; lerp must leave processes it cannot adopt alone")
	}
	if got := h2.issue(t, "claimed"); got.Status != "Todo" || got.AssigneeID != "fake-viewer" {
		t.Errorf("orphan's ticket = %+v, want still claimed in Todo", got)
	}

	// Recovery is ordinary board work: a human releases the claim and the
	// stage simply re-runs — tickets survive local state loss completely.
	orphan.kill9()
	if err := h2.fake.UnassignIssue(ctx, "claimed"); err != nil {
		t.Fatal(err)
	}
	h2.rec.Tick(ctx)
	h2.waitEvents(t, EventStarted, 1)
	h2.waitEvents(t, EventExited, 1)
	if got := h2.issue(t, "claimed"); got.Status != "Done" {
		t.Errorf("recovered ticket status = %q, want Done", got.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reruns) != 1 || reruns[0] != "LERP-2" {
		t.Errorf("re-runs = %v, want exactly one after the human released the claim", reruns)
	}
}

// racingClient gives one of two lerps sharing a board its own identity and
// choreographs its claim, so the duplicate-claim race interleaves the same
// way on every test run.
type racingClient struct {
	linear.Client
	viewerID string
	assign   func(do func() error) error
}

func (c *racingClient) Viewer(context.Context) (string, error) { return c.viewerID, nil }

func (c *racingClient) AssignIssue(ctx context.Context, issueID, userID string) error {
	return c.assign(func() error { return c.Client.AssignIssue(ctx, issueID, userID) })
}

// Scenario 4 — the duplicate claim race. Two lerps (two clones, two Linear
// users, one board — the multiplayer model) claim the same ticket at once.
// The claim protocol is assign, settle, read back (SCOPE invariant 4): both
// assigns land before either read-back, the second overwrites the first, and
// the loser walks away without disturbing the winner's claim. Here the loser
// never even starts an agent; the residual window where both run costs only
// duplicated compute, which scenarios 1 and 2 prove is safe.
func TestKillSafetyDuplicateClaimRace(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "contested", Identifier: "LERP-1", Status: "Todo"})

	// Choreography: A may not assign until both lerps have listed the ticket
	// as eligible; B assigns only after A, so B always overwrites and wins;
	// neither read-back proceeds until both assigns have landed.
	aMayAssign := make(chan struct{})
	aAssigned := make(chan struct{})
	bAssigned := make(chan struct{})
	clientA := &racingClient{Client: fake, viewerID: "lerp-a", assign: func(do func() error) error {
		<-aMayAssign
		err := do()
		close(aAssigned)
		<-bAssigned
		return err
	}}
	clientB := &racingClient{Client: fake, viewerID: "lerp-b", assign: func(do func() error) error {
		<-aAssigned
		err := do()
		close(bAssigned)
		return err
	}}

	var mu sync.Mutex
	var executed []string
	newLerp := func(client linear.Client, who string) (*Reconciler, *evidence.Evidence, chan Event) {
		store := evidence.New(t.TempDir())
		events := make(chan Event, 64)
		rec, err := NewReconciler(ReconcilerOptions{
			Client:   client,
			Repo:     testRepo(),
			RepoDir:  "/repo",
			Evidence: store,
			Lanes:    1,
			Events:   func(ev Event) { events <- ev },
			Execute: func(context.Context, run.Invocation) (run.Result, error) {
				mu.Lock()
				executed = append(executed, who)
				mu.Unlock()
				return run.Result{ExitCode: 0}, nil
			},
			Provision: func(context.Context, string, string, workspace.Identity, io.Writer) error {
				return nil
			},
			Dispose: func(context.Context, string, string, workspace.Identity, io.Writer) {},
			Alive:   func(evidence.Record) bool { return false },
		})
		if err != nil {
			t.Fatal(err)
		}
		return rec, store, events
	}
	aRec, aEvidence, aEvents := newLerp(clientA, "lerp-a")
	bRec, bEvidence, bEvents := newLerp(clientB, "lerp-b")
	ctx := context.Background()

	aRec.Tick(ctx) // A lists the ticket, then blocks before assigning
	bRec.Tick(ctx) // B lists the same ticket while it is still unassigned
	close(aMayAssign)

	waitEventsOn(t, bEvents, EventExited, 1)
	aRec.wg.Wait()
	bRec.wg.Wait()

	// The loser walked away silently: no run, no events, no leftover record,
	// and — critically — no unassign of the claim it lost.
	for _, ev := range drainEventsOn(aEvents) {
		t.Errorf("losing lerp emitted %s: %+v", ev.Type, ev)
	}
	mu.Lock()
	if len(executed) != 1 || executed[0] != "lerp-b" {
		t.Errorf("executed runs = %v, want the winner exactly once", executed)
	}
	mu.Unlock()
	for who, store := range map[string]*evidence.Evidence{"lerp-a": aEvidence, "lerp-b": bEvidence} {
		if records, _ := store.List(); len(records) != 0 {
			t.Errorf("%s run records after the race = %d, want 0", who, len(records))
		}
	}
	issue, err := fake.GetIssue(ctx, "contested")
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != "Done" || issue.AssigneeID != "lerp-b" {
		t.Errorf("contested ticket = %+v, want Done and held by the winner", issue)
	}
}
