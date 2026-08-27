//go:build unix

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
// check; everything else stays stubbed and deterministic. The harness and
// its helpers are shared with reconciler_test.go.

import (
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/linear"
)

// fakeAgent is a real process standing in for a coding agent: cat with its
// stdin held open runs until told otherwise. kill9 is the crash; finish is a
// clean exit. Both wait for the process so its PID is released before the
// test goes on — no timing, no flakes.
//
// It leads its own process group, exactly as a real run does (run.Execute
// sets Setpgid), so a test may signal the group the way lerp's two kill sites
// do rather than only the process.
type fakeAgent struct {
	t     *testing.T
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func startFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	cmd := exec.Command("cat")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
// gone and the real Alive check reads the run as dead. The raw signal is
// safe here because the child has not been waited on yet — at worst it is a
// zombie we still own, so the PID cannot have been reused.
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

// waitKilled reaps an agent somebody else killed — eject, rather than the
// test — so its PID cannot be reused while the test goes on. An agent still
// running is the failure it is written to catch, on the same 5-second
// deadline every other wait in these tests uses.
func (a *fakeAgent) waitKilled() {
	a.t.Helper()
	done := make(chan struct{})
	go func() { _ = a.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		a.t.Fatal("the agent is still running")
	}
}

// stop reaps the process at test end in whatever state the test left it.
// os.Process.Kill — unlike a raw syscall.Kill — refuses to signal once the
// process has been waited on, so a test that already reaped its agent can
// never SIGKILL an unrelated process that reused the PID.
func (a *fakeAgent) stop() {
	_ = a.cmd.Process.Kill()
	_ = a.cmd.Wait()
}

// orphanRecord is the evidence a previous lerp would have left behind for a
// still-live agent: a record created and then attached to the agent's PID.
func orphanRecord(t *testing.T, h *harness, lane int, ticketID string, agent *fakeAgent) evidence.Record {
	t.Helper()
	return orphanRun(t, h, evidence.Record{
		Lane: lane, TicketID: ticketID, Queue: "todo", StartingStatus: "Todo",
	}, agent)
}

// orphanRun is orphanRecord for a test that needs more on the record than
// adoption itself reads — a session id, without which a run cannot be
// ejected. It returns the record as Attach left it, PID and all.
func orphanRun(t *testing.T, h *harness, record evidence.Record, agent *fakeAgent) evidence.Record {
	t.Helper()
	created, err := h.evidence.Create(record)
	if err != nil {
		t.Fatal(err)
	}
	attached, err := h.evidence.Attach(created.RunID, agent.pid())
	if err != nil {
		t.Fatal(err)
	}
	return attached
}

// Scenario 1 — kill -9 the agent mid-run. The next tick reaps the dead run:
// workspace disposed, claim released, ticket still in its queue status. The
// following tick picks the ticket straight back up; the worst case is a
// re-run stage. A blocker holds the re-pick off for one pass so the released
// claim itself is visible on the board, not just inferred from the re-pick
// succeeding.
func TestKillSafetyAgentKilledMidRun(t *testing.T) {
	execute, release, reruns := blockingExecute(t, "")
	h := newHarness(t, 1, execute)
	h.rec.o.Alive = evidence.Alive // the kill below must register as a real death
	agent := startFakeAgent(t)
	record := orphanRecord(t, h, 1, "tkt", agent)
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "tkt", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})
	h.fake.AddIssue("LERP", linear.Issue{ID: "blocker", Identifier: "LERP-2", Status: "In Progress"})
	h.fake.Block("tkt", "blocker")
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
	// The blocker keeps the ticket ineligible, so nothing re-picks it yet and
	// the released claim can be read straight off the board.
	h.rec.Tick(ctx)
	h.waitEvents(t, EventReaped, 1)
	assertReaped(t, h, record)
	if got := h.issue(t, "tkt"); got.Status != "Todo" || got.AssigneeID != "" {
		t.Errorf("reaped ticket = %+v, want released back to unclaimed Todo", got)
	}

	// With the blocker done, the next pass re-picks the ticket. The run
	// starts from its beginning: while the new agent works, the ticket is
	// back in its queue status, claimed again — never lost, never in an
	// in-between state.
	if err := h.fake.MoveIssue(ctx, "blocker", "Done"); err != nil {
		t.Fatal(err)
	}
	h.rec.Tick(ctx)
	h.waitEvents(t, EventStarted, 1)
	if got := h.issue(t, "tkt"); got.Status != "Todo" || got.AssigneeID != "fake-viewer" {
		t.Errorf("re-picked ticket = %+v, want claimed in Todo", got)
	}
	release()
	h.waitEvents(t, EventExited, 1)

	if got := h.issue(t, "tkt"); got.Status != "Done" {
		t.Errorf("final status = %q, want Done", got.Status)
	}
	if got := reruns(); len(got) != 1 || got[0] != "LERP-1" {
		t.Errorf("re-runs = %v, want exactly one re-run of LERP-1", got)
	}
}

// Scenario 2 — kill -9 lerp itself mid-run. The restarted lerp adopts the
// still-live agents, and each finished run's move rule is applied correctly:
// an agent that moved its own ticket has decided, and the reap leaves that
// decision alone; an agent that exited without concluding and without
// recording an exit status told its successor nothing about how it ended, so
// its ticket is released back to its queue and re-run from the beginning —
// the tolerated worst case. A run that did record a status is settled instead;
// that is TestAdoptedRunSettlesFromItsRecordedExitStatus.
func TestKillSafetyLerpKilledMidRun(t *testing.T) {
	execute, reruns := recordingExecute("")
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
	if got := reruns(); len(got) != 0 {
		t.Fatalf("adoption executed %v, want nothing: the live agents keep their runs", got)
	}

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

	// The claim-kept half of this assertion traces to the LERP-9 house rule
	// that the loop leaves other people's work alone (releaseDead,
	// reconciler.go): the board no longer looks as the dead run left it, so
	// nothing is released. SCOPE invariants 3 and 4 do not themselves require
	// claim retention — changing releaseDead's behavior here means revisiting
	// this assertion deliberately, not a broken invariant.
	if got := h.issue(t, "moved"); got.Status != "Escalated" || got.AssigneeID != "fake-viewer" {
		t.Errorf("concluded ticket = %+v, want the agent's own move and claim kept", got)
	}
	if got := h.issue(t, "silent"); got.Status != "Done" {
		t.Errorf("re-run ticket status = %q, want Done", got.Status)
	}
	if got := reruns(); len(got) != 1 || got[0] != "LERP-2" {
		t.Errorf("re-runs = %v, want exactly the unconcluded ticket", got)
	}
	records, err := h.evidence.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
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
	// double start — and its lane still counts as occupied: a second eligible
	// ticket must wait until the remembered run finishes.
	execute, release, ran := blockingExecute(t, "")
	h := newHarness(t, 1, execute)
	h.fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	h.waitEvents(t, EventStarted, 1)
	if err := os.RemoveAll(h.evidence.RunsDir()); err != nil {
		t.Fatal(err)
	}
	h.fake.AddIssue("LERP", linear.Issue{ID: "two", Identifier: "LERP-2", Status: "Todo"})
	h.rec.Tick(ctx)
	h.rec.Tick(ctx)
	for _, ev := range h.drainEvents() {
		if ev.Type == EventQueues || ev.Type == EventAttention || ev.Type == EventTicked {
			continue // every pass publishes all three; not a board action
		}
		t.Errorf("tick over deleted evidence emitted %s: %+v", ev.Type, ev)
	}
	if got := h.issue(t, "two"); got.Status != "Todo" || got.AssigneeID != "" {
		t.Errorf("second ticket while the lane is occupied from memory = %+v, want untouched", got)
	}
	release()
	h.waitEvents(t, EventExited, 1)
	if got := h.issue(t, "one"); got.Status != "Done" {
		t.Errorf("owned run's ticket = %+v, want Done despite the deleted evidence", got)
	}
	// The settled run freed its lane; the ticket that waited runs on the
	// next pass, from its beginning.
	h.rec.Tick(ctx)
	h.waitEvents(t, EventExited, 1)
	if got := h.issue(t, "two"); got.Status != "Done" {
		t.Errorf("waiting ticket after the lane freed = %+v, want Done", got)
	}
	if got := ran(); len(got) != 2 || got[0] != "LERP-1" || got[1] != "LERP-2" {
		t.Errorf("runs = %v, want LERP-1 then LERP-2, once each", got)
	}

	// A lerp restarted after the deletion: the agent is alive but recordless,
	// so nothing can adopt it — an orphaned process, the documented cost. No
	// record names the orphan's PID, so this lerp cannot even see the
	// process, let alone signal it; that blindness is the orphan behavior
	// itself, and the meaningful assertions are on the board. The ticket is
	// neither lost nor corrupted: still claimed, still in its queue status,
	// and ineligible until someone settles it. Lerp never guesses.
	execute2, reruns := recordingExecute("")
	h2 := newHarness(t, 1, execute2)
	h2.rec.o.Alive = evidence.Alive
	orphan := startFakeAgent(t)
	h2.fake.AddIssue("LERP", linear.Issue{
		ID: "claimed", Identifier: "LERP-2", Status: "Todo", AssigneeID: "fake-viewer",
	})

	h2.rec.Tick(ctx)
	h2.rec.Tick(ctx)
	for _, ev := range h2.drainEvents() {
		if ev.Type == EventQueues || ev.Type == EventAttention || ev.Type == EventTicked {
			continue // every pass publishes all three; not a board action
		}
		t.Errorf("restarted lerp emitted %s over the orphan's ticket: %+v", ev.Type, ev)
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
	if got := reruns(); len(got) != 1 || got[0] != "LERP-2" {
		t.Errorf("re-runs = %v, want exactly one after the human released the claim", got)
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

// Scenario 4 — the duplicate claim race, loser-detects branch. Two lerps (two
// clones, two Linear users, one board — the multiplayer model) claim the same
// ticket at once. The claim protocol is assign, settle, read back (SCOPE
// invariant 4): both assigns land before either read-back, the second
// overwrites the first, and the loser walks away without disturbing the
// winner's claim — here the loser never even starts an agent. The other
// branch, where both read-backs report a win and both agents run, is
// TestKillSafetyBothWinClaimRace.
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

	aExecute, aRan := recordingExecute("lerp-a")
	bExecute, bRan := recordingExecute("lerp-b")
	ha := newHarnessWith(t, 1, aExecute, fake, clientA)
	hb := newHarnessWith(t, 1, bExecute, fake, clientB)
	ctx := context.Background()

	ha.rec.Tick(ctx) // A lists the ticket, then blocks before assigning
	hb.rec.Tick(ctx) // B lists the same ticket while it is still unassigned
	close(aMayAssign)

	hb.waitEvents(t, EventExited, 1)
	waitIdle(t, ha.rec)
	waitIdle(t, hb.rec)

	// The loser walked away silently: no run, no events, no leftover record,
	// and — critically — no unassign of the claim it lost.
	for _, ev := range ha.drainEvents() {
		if ev.Type == EventQueues || ev.Type == EventAttention || ev.Type == EventTicked {
			continue // every pass publishes all three; not a board action
		}
		t.Errorf("losing lerp emitted %s: %+v", ev.Type, ev)
	}
	if got := aRan(); len(got) != 0 {
		t.Errorf("losing lerp executed %v, want nothing", got)
	}
	if got := bRan(); len(got) != 1 {
		t.Errorf("winning lerp executed %v, want exactly one run", got)
	}
	for who, hh := range map[string]*harness{"lerp-a": ha, "lerp-b": hb} {
		records, err := hh.evidence.List()
		if err != nil {
			t.Fatalf("%s evidence list: %v", who, err)
		}
		if len(records) != 0 {
			t.Errorf("%s run records after the race = %d, want 0", who, len(records))
		}
	}
	// The winner's settled run disposed its workspace; the loser never
	// provisioned one.
	if got := hb.disposedIdentities(); len(got) != 1 || got[0].TicketID != "contested" {
		t.Errorf("winner disposed %v, want exactly its own run's workspace", got)
	}
	if got := ha.disposedIdentities(); len(got) != 0 {
		t.Errorf("loser disposed %v, want nothing", got)
	}
	// The winner's claim did its job while the run held it; finishing gives it
	// back, so the board converges on Done with nobody holding it. Which lerp
	// won is what bRan/aRan above assert — the claim is a lock, not a record.
	if got := hb.issue(t, "contested"); got.Status != "Done" || got.AssigneeID != "" {
		t.Errorf("contested ticket = %+v, want Done and released by the winner", got)
	}
}

// Scenario 5 — the both-win interleaving of the duplicate claim race: A's
// assign, settle, and read-back all complete before B assigns, so A starts
// its agent believing it won; B then overwrites the claim, reads itself
// back, and wins too. Both agents run — duplicated compute, the tolerated
// worst case — but the board converges on the winner's state: whichever run
// concludes first moves the ticket, and the other's late move is refused by
// conclude's ticket-still-in-queue-status guard (once.go), so the loser
// cannot disturb the winner's board.
func TestKillSafetyBothWinClaimRace(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "contested", Identifier: "LERP-1", Status: "Todo"})

	// Choreography: B lists the ticket while it is unassigned, then holds its
	// assign until A has fully claimed the ticket and started its agent.
	bMayAssign := make(chan struct{})
	clientA := &racingClient{Client: fake, viewerID: "lerp-a",
		assign: func(do func() error) error { return do() }}
	clientB := &racingClient{Client: fake, viewerID: "lerp-b",
		assign: func(do func() error) error { <-bMayAssign; return do() }}

	aExecute, aRelease, aRan := blockingExecute(t, "lerp-a")
	bExecute, bRan := recordingExecute("lerp-b")
	ha := newHarnessWith(t, 1, aExecute, fake, clientA)
	hb := newHarnessWith(t, 1, bExecute, fake, clientB)
	ctx := context.Background()

	hb.rec.Tick(ctx)                  // B lists the unassigned ticket, then parks before assigning
	ha.rec.Tick(ctx)                  // A claims uncontested: assign, settle, read-back, all its own
	ha.waitEvents(t, EventStarted, 1) // A is mid-run, its agent held open
	close(bMayAssign)                 // B assigns over A's claim and reads itself back a winner
	hb.waitEvents(t, EventExited, 1)  // B's whole run lands while A is still working

	// A's agent finishes after the winner settled the ticket. Its late
	// conclude finds the ticket no longer in the queue status and leaves the
	// winner's board alone.
	aRelease()
	ha.waitEvents(t, EventExited, 1)
	waitIdle(t, ha.rec)
	waitIdle(t, hb.rec)

	// Both ran — the cost is at most duplicated compute — and both settled
	// cleanly: no leftover records, both workspaces disposed.
	if got := aRan(); len(got) != 1 {
		t.Errorf("first claimant executed %v, want exactly one run", got)
	}
	if got := bRan(); len(got) != 1 {
		t.Errorf("second claimant executed %v, want exactly one run", got)
	}
	for who, hh := range map[string]*harness{"lerp-a": ha, "lerp-b": hb} {
		records, err := hh.evidence.List()
		if err != nil {
			t.Fatalf("%s evidence list: %v", who, err)
		}
		if len(records) != 0 {
			t.Errorf("%s run records after the race = %d, want 0", who, len(records))
		}
		if got := hh.disposedIdentities(); len(got) != 1 || got[0].TicketID != "contested" {
			t.Errorf("%s disposed %v, want exactly its own run's workspace", who, got)
		}
	}
	// The final board is the winner's: the ticket sits where B's conclude
	// moved it and B's conclude released it, untouched by A — whose own
	// conclude found somebody else holding the ticket and left the whole rule
	// alone, hop and claim together.
	if got := hb.issue(t, "contested"); got.Status != "Done" || got.AssigneeID != "" {
		t.Errorf("contested ticket = %+v, want Done, settled once by the winner", got)
	}
}

// Scenario 6 — an adopted run that finishes. Adoption remembers a run, and
// remembering used to be all it did: the successor was not the agent's parent,
// so it could never wait() for an exit code, and a run that finished under it
// silently cost the whole stage its on_success hop. The run now records its own
// exit status beside its log, so a lerp that restarted across the finish
// applies the queue's move rule exactly as the process that started the run
// would have.
//
// The fallback is the point of the design: anything the successor cannot read
// as a clean status — no file, a torn one, a queue the config no longer names —
// settles the way it always did, claim released and status untouched, so the
// worst case is the same worst case as before. Each case is one adopted run: a
// record and a real agent this lerp never started. A blocker keeps the settled
// ticket from being re-picked in the same pass, so the claim the reap left is
// read straight off the board rather than inferred.
func TestAdoptedRunSettlesFromItsRecordedExitStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		// exit is what the run left in its exit file; unrecorded means it
		// left no file at all.
		exit       string
		unrecorded bool
		repo       *config.RepoConfig
		// movedTo is somebody else moving the ticket while the run was in
		// flight, when non-empty.
		movedTo string
		// reassignedTo is somebody else taking the run over while it was in
		// flight; unclaimedMidRun is the claim being dropped instead.
		reassignedTo    string
		unclaimedMidRun bool
		wantEvent       EventType
		wantExitCode    int
		wantStatus      string
		wantAssignee    string
		wantNote        bool
	}{{
		// The whole ticket: a clean exit hops to on_success, and because a
		// queue serves the destination the claim goes with it — an assigned
		// ticket is never eligible, so keeping it would strand the ticket in
		// a queue that could never pick it up.
		name: "a clean exit takes the on_success hop and releases the claim",
		exit: "0\n", repo: chainedRepo(),
		wantEvent: EventExited, wantStatus: "Done", wantAssignee: "",
	}, {
		name: "a non-zero exit takes the on_failure hop",
		exit: "1\n",
		// Nothing serves "Needs Help", so the ticket rests there on the
		// operator — unclaimed, because the status is the gate and the claim
		// only ever locked work in progress (LERP-113).
		wantEvent: EventExited, wantExitCode: 1, wantStatus: "Needs Help", wantAssignee: "",
	}, {
		// A shell reports a signalled child as 128+n. It is non-zero, so it
		// is a failure, with no special case anywhere for the number.
		name:      "a signalled agent is a failure",
		exit:      "137\n",
		wantEvent: EventExited, wantExitCode: 137, wantStatus: "Needs Help", wantAssignee: "",
	}, {
		name: "an on_success destination no queue serves releases its claim",
		exit: "0\n",
		// testRepo's "Done" is nobody's queue status: the ticket has finished
		// the pipeline and waits on a human. The status is what says so — and
		// a claim left here is what strands the ticket if anybody moves it on.
		wantEvent: EventExited, wantStatus: "Done", wantAssignee: "",
	}, {
		// LERP-54 unchanged: whoever moved the ticket keeps their move, and
		// the stage that did not run is reported rather than forced.
		name: "a ticket moved mid-run keeps the move and reports the skipped hop",
		exit: "0\n", movedTo: "Escalated",
		wantEvent: EventExited, wantStatus: "Escalated", wantAssignee: "", wantNote: true,
	}, {
		// SIGKILL, or a laptop that lost power: the run never reached its own
		// last line. Precisely today's behaviour — status untouched, claim
		// released, re-run from the beginning.
		name: "a run that recorded nothing falls back to releasing the claim", unrecorded: true,
		wantEvent: EventReaped, wantStatus: "Todo", wantAssignee: "",
	}, {
		name:      "a torn status is not guessed at",
		exit:      "boom\n",
		wantEvent: EventReaped, wantStatus: "Todo", wantAssignee: "",
	}, {
		// The queue was renamed in lerp.toml while the run was in flight, so
		// there is no move rule left to apply to it.
		name: "a queue the config no longer names falls back too",
		exit: "0\n", repo: renamedQueueRepo(),
		wantEvent: EventReaped, wantStatus: "Todo", wantAssignee: "",
	}, {
		// Same queue name, different status: this run picked its ticket up
		// from somewhere the queue no longer serves, so its move rule says
		// nothing about it. Concluding anyway would report a hop nobody
		// skipped and withhold the release the fallback makes.
		name: "a queue repointed at another status falls back too",
		exit: "0\n", repo: repointedQueueRepo(),
		wantEvent: EventReaped, wantStatus: "Todo", wantAssignee: "",
	}, {
		// A human takes the run over mid-flight. The claim rule declines to
		// take their claim, so the hop has to go with it: hopping anyway
		// would push the ticket into a served status still assigned, where
		// no queue can pick it up and no inbox lists it — a state neither
		// half of the rule produces on its own.
		name: "a ticket taken over mid-run keeps neither the hop nor the claim",
		exit: "0\n", repo: chainedRepo(), reassignedTo: "colleague",
		wantEvent: EventExited, wantStatus: "Todo", wantAssignee: "colleague", wantNote: true,
	}, {
		// The other side of the same guard: a claim dropped mid-run was
		// nobody taking anything over, so the run that did the work still
		// gets its hop, and the release is already done.
		name: "a ticket unclaimed mid-run still takes its hop",
		exit: "0\n", repo: chainedRepo(), unclaimedMidRun: true,
		wantEvent: EventExited, wantStatus: "Done", wantAssignee: "",
	}, {
		// conclude's rule, inherited whole: a run that really failed in a
		// queue with nowhere to fail to keeps its claim and stays put, rather
		// than being released to fail again on every pass forever. This is
		// the one case where settling differs from the old reap, and it is
		// deliberate.
		name: "a failed run with no on_failure route parks claimed",
		exit: "1\n", repo: noFailureRouteRepo(),
		wantEvent: EventExited, wantExitCode: 1, wantStatus: "Todo", wantAssignee: "fake-viewer",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 1, nil)
			h.rec.o.Alive = evidence.Alive // the agent below really exits
			if tc.repo != nil {
				h.rec.o.Repo = tc.repo
			}
			agent := startFakeAgent(t)
			record := orphanRecord(t, h, 1, "tkt", agent)
			h.fake.AddIssue("LERP", linear.Issue{
				ID: "tkt", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
			})
			h.fake.AddIssue("LERP", linear.Issue{ID: "blocker", Identifier: "LERP-2", Status: "In Progress"})
			h.fake.Block("tkt", "blocker")
			ctx := context.Background()

			// The restart: the live run is adopted, not restarted.
			h.rec.Tick(ctx)
			h.waitEvents(t, EventAdopted, 1)
			h.drainEvents()

			if tc.movedTo != "" {
				if err := h.fake.MoveIssue(ctx, "tkt", tc.movedTo); err != nil {
					t.Fatal(err)
				}
			}
			if tc.reassignedTo != "" {
				if err := h.fake.AssignIssue(ctx, "tkt", tc.reassignedTo); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unclaimedMidRun {
				if err := h.fake.UnassignIssue(ctx, "tkt"); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.unrecorded {
				// What the run's own epilogue writes just before the shell
				// lerp recorded a PID for goes away.
				if err := os.WriteFile(record.ExitPath, []byte(tc.exit), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			agent.finish()

			// Reaping is synchronous on the tick, so everything this pass
			// decided is in the channel by the time Tick returns.
			h.rec.Tick(ctx)
			var terminal []Event
			for _, ev := range h.drainEvents() {
				if ev.Type == EventExited || ev.Type == EventReaped {
					terminal = append(terminal, ev)
				}
				if ev.Type == EventError {
					t.Fatalf("unexpected error event: %v", ev.Err)
				}
			}
			if len(terminal) != 1 || terminal[0].Type != tc.wantEvent {
				t.Fatalf("terminal events = %+v, want exactly one %s", terminal, tc.wantEvent)
			}
			ev := terminal[0]
			if ev.RunID != record.RunID || ev.Lane != 1 || ev.TicketID != "tkt" {
				t.Errorf("terminal event = %+v, want the adopted run's own identity", ev)
			}
			if tc.wantEvent == EventExited {
				if ev.ExitCode != tc.wantExitCode {
					t.Errorf("ExitCode = %d, want %d", ev.ExitCode, tc.wantExitCode)
				}
				// The reap knows the ticket's human identifier because it
				// read the ticket; the TUI's status line prints it.
				if ev.Ticket != "LERP-1" {
					t.Errorf("Ticket = %q, want LERP-1", ev.Ticket)
				}
			}
			if (ev.Note != "") != tc.wantNote {
				t.Errorf("Note = %q, want a skipped-hop note: %v", ev.Note, tc.wantNote)
			}
			assertReaped(t, h, record)
			if got := h.issue(t, "tkt"); got.Status != tc.wantStatus || got.AssigneeID != tc.wantAssignee {
				t.Errorf("settled ticket = %+v, want %q assigned to %q",
					got, tc.wantStatus, tc.wantAssignee)
			}
		})
	}
}

// chainedRepo is testRepo with a second queue serving the first's on_success
// target: the stock pipeline's shape, where a finished stage hands the ticket
// to the next one. It is the only arrangement in which the claim-release rule
// bites — LERP-50 and LERP-59 were both this case, silently stranding tickets
// in a queue that could never pick them up.
func chainedRepo() *config.RepoConfig {
	repo := testRepo()
	repo.Queues["review"] = config.Queue{
		Status: "Done", Prompt: "review the work", Runner: "agent", OnSuccess: "Shipped",
	}
	return repo
}

// renamedQueueRepo is testRepo with its queue under a different name, standing
// in for a lerp.toml edited while a run was in flight: the record names a queue
// this config no longer has.
func renamedQueueRepo() *config.RepoConfig {
	repo := testRepo()
	repo.Queues["backlog"] = repo.Queues["todo"]
	delete(repo.Queues, "todo")
	return repo
}

// repointedQueueRepo is the other half of that edit: the queue keeps its name
// and is given a different status, so the name still resolves but no longer
// describes the run the record remembers.
func repointedQueueRepo() *config.RepoConfig {
	repo := testRepo()
	queue := repo.Queues["todo"]
	queue.Status = "Doing"
	repo.Queues["todo"] = queue
	return repo
}

// noFailureRouteRepo is testRepo with nowhere for a failed run to go.
func noFailureRouteRepo() *config.RepoConfig {
	repo := testRepo()
	queue := repo.Queues["todo"]
	queue.OnFailure = ""
	repo.Queues["todo"] = queue
	return repo
}
