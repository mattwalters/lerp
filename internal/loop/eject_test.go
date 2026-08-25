package loop

// Eject (LERP-14): the one escape hatch. Stopping a lane's agent hands the
// operator the runner's own resume command and changes nothing else — not the
// board, not the workspace. The agents here are real processes in their own
// groups, because the kill is the point.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/run"
)

// resumableRunner is the test repo's runner with a session and a resume
// template — the shape eject needs, and the shape the stock config ships.
func resumableRunner(h *harness) {
	h.rec.o.Repo.Runners["agent"] = config.Runner{
		Command: "agent {{prompt}} --session {{session}}",
		Resume:  "agent --resume {{session}}",
	}
}

// A run inherited from a previous lerp is ejectable, because its session id
// was recorded before its agent started: the agent dies, the lane frees, the
// run directory goes, and everything the operator might now want — the
// workspace, the claim, the status — is exactly as the run left it. The loop
// does not pick the ticket up again, because it is still assigned.
func TestEjectAdoptedRun(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.rec.o.Alive = evidence.Alive // the eject below must be a real kill
	resumableRunner(h)
	agent := startFakeAgent(t)
	record := orphanRun(t, h, evidence.Record{
		Lane: 1, TicketID: "tkt", Ticket: "LERP-1", Queue: "todo", StartingStatus: "Todo",
		SessionID: "1e9a4a0e-0000-4000-8000-00000000abcd",
	}, agent)
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "tkt", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})
	ctx := context.Background()

	h.rec.Tick(ctx)
	h.waitEvents(t, EventAdopted, 1)

	ejection, err := h.rec.Eject(ctx, "tkt")
	if err != nil {
		t.Fatal(err)
	}
	agent.waitKilled()

	if want := "agent --resume '1e9a4a0e-0000-4000-8000-00000000abcd'"; ejection.Resume != want {
		t.Errorf("Resume = %q, want %q", ejection.Resume, want)
	}
	if ejection.Workspace != record.Workspace || ejection.Lane != 1 || ejection.Ticket != "LERP-1" {
		t.Errorf("Ejection = %+v, want the run's own lane, workspace and ticket", ejection)
	}
	if _, err := h.evidence.Read(record.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("run record after eject: read error = %v, want not exist", err)
	}
	// The workspace is the operator's now — disposing it is exactly what
	// eject must not do.
	if got := h.disposedIdentities(); len(got) != 0 {
		t.Errorf("disposed %v, want nothing disposed by an eject", got)
	}
	if got := h.issue(t, "tkt"); got.Status != "Todo" || got.AssigneeID != "fake-viewer" {
		t.Errorf("ejected ticket = %+v, want it still claimed in Todo", got)
	}
	ejected := h.waitEvents(t, EventEjected, 1)
	if ejected[0].RunID != record.RunID || ejected[0].Lane != 1 || ejected[0].Ticket != "LERP-1" {
		t.Errorf("ejected event = %+v, want the ejected run's own", ejected[0])
	}

	// The lane is free, but the ticket is not picked up again: it is still
	// assigned, and an assigned ticket is never eligible. Nothing is reaped
	// either — a reap here would dispose the workspace just handed over.
	h.rec.Tick(ctx)
	for _, ev := range h.drainEvents() {
		if ev.Type == EventStarted || ev.Type == EventAdopted || ev.Type == EventReaped {
			t.Errorf("tick after eject emitted %s: %+v", ev.Type, ev)
		}
	}
	if got := h.disposedIdentities(); len(got) != 0 {
		t.Errorf("disposed %v after the pass following an eject, want nothing", got)
	}
}

// A run this process started ends as an eject rather than as an exit: no
// conclude, so the ticket keeps the claim and the status the run had it in,
// no dispose, and the lane frees for the next ticket.
func TestEjectRunThisProcessStarted(t *testing.T) {
	execute, agents := liveExecute(t)
	h := newHarness(t, 1, execute)
	h.rec.o.Alive = evidence.Alive // eject only takes over an agent it can see running
	resumableRunner(h)
	h.fake.AddIssue("LERP", linear.Issue{ID: "tkt", Identifier: "LERP-1", Status: "Todo"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "next", Identifier: "LERP-2", Status: "Todo"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	started := h.waitEvents(t, EventStarted, 1)
	agents() // the agent exists before the eject; its death ends the run below

	ejection, err := h.rec.Eject(ctx, "tkt")
	if err != nil {
		t.Fatal(err)
	}

	// The session the agent was told to open is the one the operator is
	// handed: minted before the run, recorded, and substituted into both.
	if !strings.HasPrefix(ejection.Resume, "agent --resume '") {
		t.Errorf("Resume = %q, want the runner's resume template expanded", ejection.Resume)
	}
	// The event only arrives once Execute returns, and Execute returns only
	// once the agent it started is dead: waiting for it is the proof that
	// eject killed the agent, without a second waiter on the same process.
	ejected := h.waitEvents(t, EventEjected, 1)
	if ejected[0].RunID != started[0].RunID || ejected[0].Ticket != "LERP-1" {
		t.Errorf("ejected event = %+v, want the started run's own", ejected[0])
	}
	waitIdle(t, h.rec)
	for _, ev := range h.drainEvents() {
		if ev.Type == EventExited {
			t.Errorf("ejected run also reported an exit: %+v", ev)
		}
	}
	if got := h.disposedIdentities(); len(got) != 0 {
		t.Errorf("disposed %v, want nothing disposed by an eject", got)
	}
	if got := h.issue(t, "tkt"); got.Status != "Todo" || got.AssigneeID != "fake-viewer" {
		t.Errorf("ejected ticket = %+v, want it still claimed in Todo — ejecting is taking over", got)
	}
	records, err := h.evidence.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("run records after eject = %d, want none", len(records))
	}

	// The lane the ejected run held is free for the next ticket.
	h.rec.Tick(ctx)
	next := h.waitEvents(t, EventStarted, 1)
	if next[0].Ticket != "LERP-2" || next[0].Lane != 1 {
		t.Errorf("next run = %+v, want LERP-2 in the freed lane 1", next[0])
	}
	// End the second run the way its agent dying ends every run here. The
	// raw signal is safe: the process has not been waited on yet — its own
	// run goroutine is the only waiter — so its PID cannot have been reused.
	if err := syscall.Kill(agents(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, h.rec)
}

// Every refusal happens before anything is killed: eject either hands back a
// command or leaves the run exactly as it found it.
func TestEjectRefusesAndKillsNothing(t *testing.T) {
	session := "1e9a4a0e-0000-4000-8000-00000000abcd"
	cases := []struct {
		name    string
		ticket  string
		session string
		runner  config.Runner
		want    string
	}{
		{
			name:    "no lane is running the ticket",
			ticket:  "other",
			session: session,
			runner:  config.Runner{Command: "agent {{session}}", Resume: "agent --resume {{session}}"},
			want:    "no lane is running other",
		},
		{
			name:    "the runner cannot resume",
			ticket:  "tkt",
			session: session,
			runner:  config.Runner{Command: "agent"},
			want:    "no resume command",
		},
		{
			name:    "the run recorded no session",
			ticket:  "tkt",
			session: "",
			runner:  config.Runner{Command: "agent {{session}}", Resume: "agent --resume {{session}}"},
			want:    "no session to resume",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 1, nil)
			h.rec.o.Alive = evidence.Alive
			h.rec.o.Repo.Runners["agent"] = tc.runner
			agent := startFakeAgent(t)
			record := orphanRun(t, h, evidence.Record{
				Lane: 1, TicketID: "tkt", Ticket: "LERP-1", Queue: "todo",
				StartingStatus: "Todo", SessionID: tc.session,
			}, agent)
			h.fake.AddIssue("LERP", linear.Issue{
				ID: "tkt", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
			})
			ctx := context.Background()
			h.rec.Tick(ctx)
			h.waitEvents(t, EventAdopted, 1)

			_, err := h.rec.Eject(ctx, tc.ticket)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Eject error = %v, want one mentioning %q", err, tc.want)
			}
			if !agent.alive() {
				t.Error("a refused eject killed the agent")
			}
			if _, err := h.evidence.Read(record.RunID); err != nil {
				t.Errorf("run record after a refused eject: read error = %v, want it intact", err)
			}
		})
	}
}

// liveExecute is an Execute stub that really starts a process: one agent per
// run, in its own process group as run.Execute starts one, its PID reported
// through Started, and waited on here so the run ends when — and only when —
// that agent dies. Killing the agent is therefore the only way to end a run,
// which is what makes an eject observable.
//
// The stub is the process's only waiter: two goroutines calling Wait on one
// exec.Cmd race, and the loser learns nothing about how the process died. So
// tests read a PID from agents and signal it, rather than holding a handle
// they might wait on themselves.
func liveExecute(t *testing.T) (ExecuteFunc, func() int) {
	t.Helper()
	started := make(chan int, 4)
	execute := func(_ context.Context, inv run.Invocation) (run.Result, error) {
		cmd := exec.Command("cat")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// Held open, so cat runs until something kills it.
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return run.Result{}, err
		}
		defer stdin.Close()
		if err := cmd.Start(); err != nil {
			return run.Result{}, err
		}
		if inv.Started != nil {
			inv.Started(cmd.Process.Pid)
		}
		started <- cmd.Process.Pid
		_ = cmd.Wait()
		return run.Result{ExitCode: cmd.ProcessState.ExitCode(), SessionID: inv.SessionID}, nil
	}
	next := func() int {
		t.Helper()
		select {
		case pid := <-started:
			return pid
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a run to start its agent")
			return 0
		}
	}
	return execute, next
}

// A run whose agent has already died is not ejectable: there is no session to
// take over, and the recorded PID may since have been handed to something
// else — signalling its process group would kill whatever the operator is
// running in it.
func TestEjectRefusesADeadAgent(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.rec.o.Alive = evidence.Alive
	resumableRunner(h)
	agent := startFakeAgent(t)
	record := orphanRun(t, h, evidence.Record{
		Lane: 1, TicketID: "tkt", Ticket: "LERP-1", Queue: "todo", StartingStatus: "Todo",
		SessionID: "1e9a4a0e-0000-4000-8000-00000000abcd",
	}, agent)
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "tkt", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})
	ctx := context.Background()
	h.rec.Tick(ctx)
	h.waitEvents(t, EventAdopted, 1)

	agent.kill9()
	if _, err := h.rec.Eject(ctx, "tkt"); err == nil || !strings.Contains(err.Error(), "no longer running") {
		t.Fatalf("Eject error = %v, want a refusal naming the dead agent", err)
	}
	// Refused means unchanged: the record is still there for the reaper, and
	// the reap that follows is the ordinary one — workspace disposed, claim
	// released — because this run was never ejected.
	if _, err := h.evidence.Read(record.RunID); err != nil {
		t.Fatalf("record after a refused eject: read error = %v, want it intact", err)
	}
	h.rec.Tick(ctx)
	h.waitEvents(t, EventReaped, 1)
	assertReaped(t, h, record)
	// The reap released the claim, so the same pass picked the ticket up
	// again: wait for that run before the test's temporary directory goes.
	waitIdle(t, h.rec)
}

// The two ways a run can end are decided under one lock: an eject that
// arrives while the run is already being concluded is refused, rather than
// reporting a session the operator cannot resume and stranding the workspace
// its dispose would then skip.
func TestEjectRefusesARunAlreadySettling(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.rec.o.Alive = evidence.Alive
	resumableRunner(h)
	agent := startFakeAgent(t)
	lr := &laneRun{lane: 1, ticketID: "tkt", record: evidence.Record{
		RunID: "run", Lane: 1, TicketID: "tkt", Ticket: "LERP-1", Queue: "todo",
		PID: agent.pid(), SessionID: "1e9a4a0e-0000-4000-8000-00000000abcd",
	}}
	h.rec.active = append(h.rec.active, lr)

	if !h.rec.beginSettling(lr) {
		t.Fatal("a run nobody ejected could not claim its own settling")
	}
	if _, err := h.rec.Eject(context.Background(), "tkt"); err == nil ||
		!strings.Contains(err.Error(), "already finished") {
		t.Fatalf("Eject error = %v, want a refusal naming the settling run", err)
	}
	if !agent.alive() {
		t.Error("a refused eject killed the agent")
	}
}

// A pass that read the board before an eject must not put the ejected run
// back in a lane: adopting a dead run leads the pass after it to reap — which
// would dispose the workspace eject has already handed over and release the
// claim the operator took the work over with.
func TestEjectedRunIsNeitherAdoptedNorReaped(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.rec.o.Alive = evidence.Alive
	resumableRunner(h)
	agent := startFakeAgent(t)
	record := orphanRun(t, h, evidence.Record{
		Lane: 1, TicketID: "tkt", Ticket: "LERP-1", Queue: "todo", StartingStatus: "Todo",
		SessionID: "1e9a4a0e-0000-4000-8000-00000000abcd",
	}, agent)
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "tkt", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})
	ctx := context.Background()
	h.rec.Tick(ctx)
	h.waitEvents(t, EventAdopted, 1)
	if _, err := h.rec.Eject(ctx, "tkt"); err != nil {
		t.Fatal(err)
	}
	agent.waitKilled()

	// The pass that ran before the eject, replayed: adopt is called with the
	// record it had listed, and reap with the same record. Neither may act.
	h.rec.adopt(record)
	if got := h.rec.adoptedRecords(); len(got) != 0 {
		t.Errorf("adopted %+v after it was ejected, want the lane left free", got)
	}
	h.rec.reap(ctx, record)
	if got := h.disposedIdentities(); len(got) != 0 {
		t.Errorf("disposed %v, want an ejected workspace left alone", got)
	}
	if got := h.issue(t, "tkt"); got.Status != "Todo" || got.AssigneeID != "fake-viewer" {
		t.Errorf("ejected ticket = %+v, want it still claimed in Todo", got)
	}
}

// A record left behind by an eject — its removal failed, or lerp died between
// the kill and the removal — is disowned on disk first, so the next lerp,
// which knows nothing of the eject, reads a run with no workspace to dispose
// and no ticket to settle.
func TestEjectDisownsTheRecordBeforeKilling(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.rec.o.Alive = evidence.Alive
	resumableRunner(h)
	agent := startFakeAgent(t)
	record := orphanRun(t, h, evidence.Record{
		Lane: 1, TicketID: "tkt", Ticket: "LERP-1", Queue: "todo", StartingStatus: "Todo",
		SessionID: "1e9a4a0e-0000-4000-8000-00000000abcd",
	}, agent)
	h.fake.AddIssue("LERP", linear.Issue{
		ID: "tkt", Identifier: "LERP-1", Status: "Todo", AssigneeID: "fake-viewer",
	})
	if err := h.evidence.Disown(record.RunID); err != nil {
		t.Fatal(err)
	}
	disowned, err := h.evidence.Read(record.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if disowned.Workspace != "" || disowned.TicketID != "" {
		t.Fatalf("disowned record = %+v, want no workspace and no ticket", disowned)
	}

	// A fresh lerp reaping that leftover, its agent long dead.
	agent.kill9()
	ctx := context.Background()
	h.rec.Tick(ctx)
	h.waitEvents(t, EventReaped, 1)
	if got := h.disposedIdentities(); len(got) != 0 {
		t.Errorf("disposed %v while reaping a disowned record, want nothing", got)
	}
	if got := h.issue(t, "tkt"); got.Status != "Todo" || got.AssigneeID != "fake-viewer" {
		t.Errorf("ticket after reaping a disowned record = %+v, want it untouched", got)
	}
	if _, err := h.evidence.Read(record.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("disowned record after the reap: read error = %v, want it gone", err)
	}
}
