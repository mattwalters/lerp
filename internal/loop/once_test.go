package loop

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/run"
	"github.com/mattwalters/lerp/internal/workspace"
)

func TestOnceRunsOneEligibleTicketEndToEnd(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	fake.AddIssue("LERP", linear.Issue{ID: "blocked", Identifier: "LERP-2", Status: "Todo"})
	fake.AddIssue("LERP", linear.Issue{ID: "blocker", Identifier: "LERP-3", Status: "In Progress"})
	fake.Block("blocked", "blocker")

	var provisioned, disposed workspace.Identity
	var gotInvocation run.Invocation
	ran, err := Once(context.Background(), onceOptions(fake, func(_ context.Context, inv run.Invocation) (run.Result, error) {
		gotInvocation = inv
		if inv.Queue.Prompt != "do the work" || inv.Workdir != "/work/one" || inv.LogPath == "" {
			t.Fatalf("Execute invocation = %+v", inv)
		}
		return run.Result{ExitCode: 0}, nil
	}, func(_ context.Context, _ string, _ string, id workspace.Identity, _ io.Writer) error {
		provisioned = id
		return nil
	}, func(_ context.Context, _ string, _ string, id workspace.Identity, _ io.Writer) { disposed = id }))
	if err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("Once ran = false, want true")
	}
	if gotInvocation.Runner.Command != "agent" {
		t.Errorf("runner = %+v, want configured runner", gotInvocation.Runner)
	}
	// Without the identifier the agent cannot know which ticket it was started
	// for, and a clean exit would advance a ticket nobody worked on.
	if gotInvocation.Ticket != "LERP-1" {
		t.Errorf("invocation ticket = %q, want LERP-1", gotInvocation.Ticket)
	}
	if provisioned != disposed || provisioned.TicketID != "one" || provisioned.Lane != 1 {
		t.Errorf("workspace lifecycle = provisioned %+v, disposed %+v", provisioned, disposed)
	}
	if got, _ := fake.GetIssue(context.Background(), "one"); got.Status != "Done" {
		t.Errorf("successful issue status = %q, want Done", got.Status)
	}
	if got, _ := fake.GetIssue(context.Background(), "blocked"); got.AssigneeID != "" {
		t.Errorf("blocked issue assignee = %q, want empty", got.AssigneeID)
	}
}

func TestOnceFailureMovesOnlyWhenConfigured(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	ran, err := Once(context.Background(), onceOptions(fake, func(context.Context, run.Invocation) (run.Result, error) {
		return run.Result{ExitCode: 3}, nil
	}, nil, nil))
	if err != nil || !ran {
		t.Fatalf("Once = (%v, %v), want (true, nil)", ran, err)
	}
	if got, _ := fake.GetIssue(context.Background(), "one"); got.Status != "Needs Help" {
		t.Errorf("failure status = %q, want Needs Help", got.Status)
	}
}

// A ticket that fails with nowhere to go keeps its claim, so the next pass does
// not pick it straight back up and re-run it forever.
func TestOnceFailureWithoutRouteKeepsTheClaim(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	o := onceOptions(fake, func(context.Context, run.Invocation) (run.Result, error) {
		return run.Result{ExitCode: 3}, nil
	}, nil, nil)
	queue := o.Repo.Queues["todo"]
	queue.OnFailure = ""
	o.Repo.Queues["todo"] = queue

	ran, err := Once(context.Background(), o)
	if err != nil || !ran {
		t.Fatalf("Once = (%v, %v), want (true, nil)", ran, err)
	}
	got, _ := fake.GetIssue(context.Background(), "one")
	if got.Status != "Todo" {
		t.Errorf("unrouted failure status = %q, want Todo", got.Status)
	}
	if got.AssigneeID == "" {
		t.Error("unrouted failure released the claim, so the ticket would be re-run immediately")
	}
	if Eligible(got, map[string]bool{"Todo": true}) {
		t.Error("unrouted failure left the ticket eligible, which spins the reconciler")
	}
}

func TestOnceRespectsAgentMove(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	ran, err := Once(context.Background(), onceOptions(fake, func(context.Context, run.Invocation) (run.Result, error) {
		return run.Result{}, fake.MoveIssue(context.Background(), "one", "Escalated")
	}, nil, nil))
	if err != nil || !ran {
		t.Fatalf("Once = (%v, %v), want (true, nil)", ran, err)
	}
	if got, _ := fake.GetIssue(context.Background(), "one"); got.Status != "Escalated" {
		t.Errorf("agent move was overwritten: status = %q", got.Status)
	}
}

func TestOnceProvisionFailureReleasesClaimAndDisposes(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	called := false
	disposed := false
	ran, err := Once(context.Background(), onceOptions(fake, func(context.Context, run.Invocation) (run.Result, error) {
		called = true
		return run.Result{}, nil
	}, func(context.Context, string, string, workspace.Identity, io.Writer) error {
		return errors.New("no workspace")
	}, func(context.Context, string, string, workspace.Identity, io.Writer) { disposed = true }))
	if err == nil || ran || called {
		t.Errorf("Once = (%v, %v), execute called = %v", ran, err, called)
	}
	// A provision command can fail after creating its workspace, so cleanup has
	// to run anyway or the next attempt collides with the leftovers.
	if !disposed {
		t.Error("provision failure did not dispose the workspace")
	}
	if got, _ := fake.GetIssue(context.Background(), "one"); got.Status != "Todo" || got.AssigneeID != "" {
		t.Errorf("provision failure changed issue = %+v", got)
	}
}

// A ticket that leaves the queue while being claimed must not keep the claim:
// an assigned ticket is never eligible, so it would be stranded.
func TestOnceReleasesClaimWhenTicketMovesDuringClaim(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})

	called := false
	client := movedOnAssign{Client: fake, move: func(issueID string) {
		if err := fake.MoveIssue(context.Background(), issueID, "Escalated"); err != nil {
			t.Error(err)
		}
	}}
	ran, err := Once(context.Background(), onceOptions(client, func(context.Context, run.Invocation) (run.Result, error) {
		called = true
		return run.Result{}, nil
	}, nil, nil))
	if err != nil || ran || called {
		t.Fatalf("Once = (%v, %v), execute called = %v", ran, err, called)
	}
	got, _ := fake.GetIssue(context.Background(), "one")
	if got.Status != "Escalated" {
		t.Errorf("status = %q, want Escalated", got.Status)
	}
	if got.AssigneeID != "" {
		t.Errorf("assignee = %q, want the claim released so the ticket is not stranded", got.AssigneeID)
	}
}

// movedOnAssign is a Client whose AssignIssue succeeds and then lets the test
// move the ticket, standing in for a human or agent racing the claim.
type movedOnAssign struct {
	linear.Client
	move func(issueID string)
}

func (c movedOnAssign) AssignIssue(ctx context.Context, issueID, userID string) error {
	if err := c.Client.AssignIssue(ctx, issueID, userID); err != nil {
		return err
	}
	c.move(issueID)
	return nil
}

func TestOnceDerivesPathsAfterSelectingTicket(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	o := onceOptions(fake, func(_ context.Context, inv run.Invocation) (run.Result, error) {
		if inv.Workdir != "/work/one" || inv.LogPath != "/log/one" {
			t.Errorf("derived paths = (%q, %q), want (/work/one, /log/one)", inv.Workdir, inv.LogPath)
		}
		return run.Result{}, nil
	}, nil, nil)
	o.Workspace = ""
	o.LogPath = ""
	o.WorkspaceFor = func(issue linear.Issue) string { return "/work/" + issue.ID }
	o.LogPathFor = func(issue linear.Issue) string { return "/log/" + issue.ID }
	if ran, err := Once(context.Background(), o); err != nil || !ran {
		t.Fatalf("Once = (%v, %v), want (true, nil)", ran, err)
	}
}

func onceOptions(client linear.Client, execute ExecuteFunc, provision ProvisionFunc, dispose DisposeFunc) OnceOptions {
	if provision == nil {
		provision = func(context.Context, string, string, workspace.Identity, io.Writer) error { return nil }
	}
	if dispose == nil {
		dispose = func(context.Context, string, string, workspace.Identity, io.Writer) {}
	}
	return OnceOptions{
		Client: client,
		Repo: &config.RepoConfig{
			Teams:     []string{"LERP"},
			Provision: "provision",
			Dispose:   "dispose",
			Runners:   map[string]config.Runner{"agent": {Command: "agent"}},
			Queues: map[string]config.Queue{"todo": {
				Status: "Todo", Prompt: "do the work", Runner: "agent", OnSuccess: "Done", OnFailure: "Needs Help",
			}},
		},
		RepoDir:   "/repo",
		Lane:      1,
		Workspace: "/work/one",
		LogPath:   filepath.Join("/tmp", "lerp-once-test.log"),
		Execute:   execute,
		Provision: provision,
		Dispose:   dispose,
	}
}

// The pipeline only chains if a finished run releases its claim: an assigned
// ticket is never eligible, so a ticket that keeps its claim through a move
// into a served status is stranded there permanently, with nothing reporting
// an error.
func TestOnceChainsThroughTwoQueues(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Planning"})

	var ran []string
	o := onceOptions(fake, func(_ context.Context, inv run.Invocation) (run.Result, error) {
		ran = append(ran, inv.Queue.Status)
		return run.Result{ExitCode: 0}, nil
	}, nil, nil)
	o.Repo.Queues = map[string]config.Queue{
		"plan":      {Status: "Planning", Prompt: "plan it", Runner: "agent", OnSuccess: "Implementing", OnFailure: "Needs Help"},
		"implement": {Status: "Implementing", Prompt: "build it", Runner: "agent", OnSuccess: "Done", OnFailure: "Needs Help"},
	}

	for pass := 1; pass <= 2; pass++ {
		started, err := Once(context.Background(), o)
		if err != nil || !started {
			t.Fatalf("pass %d: Once = (%v, %v), want (true, nil)", pass, started, err)
		}
	}
	if len(ran) != 2 || ran[0] != "Planning" || ran[1] != "Implementing" {
		t.Fatalf("stages run = %v, want [Planning Implementing]", ran)
	}
	if got, _ := fake.GetIssue(context.Background(), "one"); got.Status != "Done" {
		t.Errorf("chained status = %q, want Done", got.Status)
	}
}

// Finishing into a status no queue serves keeps the claim: that is what parks
// the ticket on the operator in the needs-you view, and it is how an unserved
// status works as a human gate.
func TestOnceKeepsClaimWhenFinishingOutsideEveryQueue(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	started, err := Once(context.Background(), onceOptions(fake, func(context.Context, run.Invocation) (run.Result, error) {
		return run.Result{ExitCode: 0}, nil
	}, nil, nil))
	if err != nil || !started {
		t.Fatalf("Once = (%v, %v), want (true, nil)", started, err)
	}
	got, _ := fake.GetIssue(context.Background(), "one")
	if got.Status != "Done" {
		t.Fatalf("status = %q, want Done", got.Status)
	}
	if got.AssigneeID == "" {
		t.Error("finishing outside every queue released the claim, so the ticket is not parked on the operator")
	}
}

// An agent that moves its own ticket into another queue's status must not
// leave it stranded: lerp respects the move and still releases the claim, so
// the queue serving that status can pick the ticket up.
func TestOnceReleasesClaimWhenAgentMovesIntoServedStatus(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Planning"})
	o := onceOptions(fake, func(context.Context, run.Invocation) (run.Result, error) {
		return run.Result{}, fake.MoveIssue(context.Background(), "one", "Implementing")
	}, nil, nil)
	o.Repo.Queues = map[string]config.Queue{
		"plan":      {Status: "Planning", Prompt: "plan it", Runner: "agent", OnSuccess: "Plan Review", OnFailure: "Needs Help"},
		"implement": {Status: "Implementing", Prompt: "build it", Runner: "agent", OnSuccess: "Done", OnFailure: "Needs Help"},
	}
	started, err := Once(context.Background(), o)
	if err != nil || !started {
		t.Fatalf("Once = (%v, %v), want (true, nil)", started, err)
	}
	got, _ := fake.GetIssue(context.Background(), "one")
	if got.Status != "Implementing" {
		t.Fatalf("agent move was overwritten: status = %q", got.Status)
	}
	if !Eligible(got, map[string]bool{"Implementing": true}) {
		t.Errorf("ticket is stranded after the agent's move: %+v", got)
	}
}
