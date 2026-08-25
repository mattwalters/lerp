package loop

// Eject (LERP-14): the one escape hatch. Stopping a lane's agent hands the
// operator the runner's own resume command and changes nothing else — not the
// board, not the workspace. The agents here are real processes in their own
// groups, because the kill is the point.

import (
	"context"
	"errors"
	"os"
	"strings"
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
	resumableRunner(h)
	h.fake.AddIssue("LERP", linear.Issue{ID: "tkt", Identifier: "LERP-1", Status: "Todo"})
	h.fake.AddIssue("LERP", linear.Issue{ID: "next", Identifier: "LERP-2", Status: "Todo"})
	ctx := context.Background()

	h.rec.Tick(ctx)
	started := h.waitEvents(t, EventStarted, 1)
	agent := agents()

	ejection, err := h.rec.Eject(ctx, "tkt")
	if err != nil {
		t.Fatal(err)
	}
	agent.waitKilled()

	// The session the agent was told to open is the one the operator is
	// handed: minted before the run, recorded, and substituted into both.
	if !strings.HasPrefix(ejection.Resume, "agent --resume '") {
		t.Errorf("Resume = %q, want the runner's resume template expanded", ejection.Resume)
	}
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
	agents().kill9()
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

// liveExecute is an Execute stub that really starts a process: one fake agent
// per run, its PID reported through Started the way run.Execute reports the
// shell it spawns, and waited on so the run ends when — and only when — the
// agent dies. agents returns the newest one, once it exists.
func liveExecute(t *testing.T) (ExecuteFunc, func() *fakeAgent) {
	t.Helper()
	started := make(chan *fakeAgent, 4)
	execute := func(_ context.Context, inv run.Invocation) (run.Result, error) {
		agent := startFakeAgent(t)
		if inv.Started != nil {
			inv.Started(agent.pid())
		}
		started <- agent
		_ = agent.cmd.Wait()
		return run.Result{ExitCode: agent.cmd.ProcessState.ExitCode(), SessionID: inv.SessionID}, nil
	}
	next := func() *fakeAgent {
		t.Helper()
		select {
		case agent := <-started:
			return agent
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a run to start its agent")
			return nil
		}
	}
	return execute, next
}
