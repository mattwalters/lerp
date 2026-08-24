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
	var gotRunner config.Runner
	ran, err := Once(context.Background(), onceOptions(fake, func(_ context.Context, runner config.Runner, prompt, dir, log string) (run.Result, error) {
		gotRunner = runner
		if prompt != "do the work" || dir != "/work/one" || log == "" {
			t.Fatalf("Execute args = (%q, %q, %q)", prompt, dir, log)
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
	if gotRunner.Command != "agent" {
		t.Errorf("runner = %+v, want configured runner", gotRunner)
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
	ran, err := Once(context.Background(), onceOptions(fake, func(context.Context, config.Runner, string, string, string) (run.Result, error) {
		return run.Result{ExitCode: 3}, nil
	}, nil, nil))
	if err != nil || !ran {
		t.Fatalf("Once = (%v, %v), want (true, nil)", ran, err)
	}
	if got, _ := fake.GetIssue(context.Background(), "one"); got.Status != "Needs Help" {
		t.Errorf("failure status = %q, want Needs Help", got.Status)
	}
}

func TestOnceFailureWithoutRouteLeavesTicketEligible(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	o := onceOptions(fake, func(context.Context, config.Runner, string, string, string) (run.Result, error) {
		return run.Result{ExitCode: 3}, nil
	}, nil, nil)
	queue := o.Global.Queues["todo"]
	queue.OnFailure = ""
	o.Global.Queues["todo"] = queue

	ran, err := Once(context.Background(), o)
	if err != nil || !ran {
		t.Fatalf("Once = (%v, %v), want (true, nil)", ran, err)
	}
	if got, _ := fake.GetIssue(context.Background(), "one"); got.Status != "Todo" || got.AssigneeID != "" {
		t.Errorf("unrouted failure left issue = %+v, want queued and unassigned", got)
	}
}

func TestOnceRespectsAgentMove(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	ran, err := Once(context.Background(), onceOptions(fake, func(_ context.Context, _ config.Runner, _ string, _ string, _ string) (run.Result, error) {
		return run.Result{}, fake.MoveIssue(context.Background(), "one", "Escalated")
	}, nil, nil))
	if err != nil || !ran {
		t.Fatalf("Once = (%v, %v), want (true, nil)", ran, err)
	}
	if got, _ := fake.GetIssue(context.Background(), "one"); got.Status != "Escalated" {
		t.Errorf("agent move was overwritten: status = %q", got.Status)
	}
}

func TestOnceProvisionFailureReleasesClaim(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	called := false
	ran, err := Once(context.Background(), onceOptions(fake, func(context.Context, config.Runner, string, string, string) (run.Result, error) {
		called = true
		return run.Result{}, nil
	}, func(context.Context, string, string, workspace.Identity, io.Writer) error {
		return errors.New("no workspace")
	}, nil))
	if err == nil || ran || called {
		t.Errorf("Once = (%v, %v), execute called = %v", ran, err, called)
	}
	if got, _ := fake.GetIssue(context.Background(), "one"); got.Status != "Todo" || got.AssigneeID != "" {
		t.Errorf("provision failure changed issue = %+v", got)
	}
}

func TestOnceDerivesPathsAfterSelectingTicket(t *testing.T) {
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	o := onceOptions(fake, func(_ context.Context, _ config.Runner, _ string, dir, log string) (run.Result, error) {
		if dir != "/work/one" || log != "/log/one" {
			t.Errorf("derived paths = (%q, %q), want (/work/one, /log/one)", dir, log)
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
		Global: &config.Global{
			Runners: map[string]config.Runner{"agent": {Command: "agent"}},
			Queues: map[string]config.Queue{"todo": {
				Status: "Todo", Prompt: "do the work", Runner: "agent", OnSuccess: "Done", OnFailure: "Needs Help",
			}},
		},
		Repo:      &config.RepoConfig{Teams: []string{"LERP"}, Provision: "provision", Dispose: "dispose"},
		RepoDir:   "/repo",
		Lane:      1,
		Workspace: "/work/one",
		LogPath:   filepath.Join("/tmp", "lerp-once-test.log"),
		Execute:   execute,
		Provision: provision,
		Dispose:   dispose,
	}
}
