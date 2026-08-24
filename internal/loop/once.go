package loop

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/run"
	"github.com/mattwalters/lerp/internal/workspace"
)

// ExecuteFunc runs one configured coding-agent command. It is a function type
// so the vertical slice can be tested without starting a real agent.
type ExecuteFunc func(context.Context, config.Runner, string, string, string) (run.Result, error)

// PathFunc derives an ephemeral path from the issue chosen for this run.
type PathFunc func(linear.Issue) string

// ProvisionFunc and DisposeFunc create and remove a workspace. They are
// function types for the same reason as ExecuteFunc.
type ProvisionFunc func(context.Context, string, string, workspace.Identity, io.Writer) error
type DisposeFunc func(context.Context, string, string, workspace.Identity, io.Writer)

// OnceOptions supplies the one lane used by Once. Workspace and LogPath are
// intentionally caller-owned: durable run evidence belongs to the later run
// record implementation, not this vertical slice.
type OnceOptions struct {
	Client       linear.Client
	Global       *config.Global
	Repo         *config.RepoConfig
	RepoDir      string
	Lane         int
	Workspace    string
	WorkspaceFor PathFunc
	LogPath      string
	LogPathFor   PathFunc
	Log          io.Writer
	Execute      ExecuteFunc
	Provision    ProvisionFunc
	Dispose      DisposeFunc
}

// Once runs at most one eligible ticket through claim, provision, execution,
// disposal, and its configured queue transition. It is a narrow development
// harness for the single-lane vertical slice; the reconciler will later own
// lane selection and durable run evidence.
//
// A clean runner exit moves the ticket to OnSuccess, and a non-zero exit moves
// it to OnFailure when configured. In either case, the transition happens only
// when the ticket remains in the queue status; an agent or human move wins.
func Once(ctx context.Context, o OnceOptions) (bool, error) {
	if err := o.validate(); err != nil {
		return false, err
	}
	if o.Execute == nil {
		o.Execute = run.Execute
	}
	if o.Provision == nil {
		o.Provision = workspace.Provision
	}
	if o.Dispose == nil {
		o.Dispose = workspace.Dispose
	}

	issue, queue, found, err := next(ctx, o.Client, o.Global, o.Repo.Teams)
	if err != nil || !found {
		return false, err
	}

	won, err := Claim(ctx, o.Client, issue.ID)
	if err != nil {
		return false, err
	}
	if !won {
		return false, nil
	}

	// A move may have raced the claim. Do not provision or run a ticket which
	// has left this queue.
	claimed, err := o.Client.GetIssue(ctx, issue.ID)
	if err != nil {
		return false, fmt.Errorf("read claimed issue %s: %w", issue.ID, err)
	}
	viewerID, err := o.Client.Viewer(ctx)
	if err != nil {
		return false, fmt.Errorf("read claimed viewer: %w", err)
	}
	if claimed.Status != queue.Status || claimed.AssigneeID != viewerID {
		return false, nil
	}

	workdir := o.Workspace
	if o.WorkspaceFor != nil {
		workdir = o.WorkspaceFor(issue)
	}
	logPath := o.LogPath
	if o.LogPathFor != nil {
		logPath = o.LogPathFor(issue)
	}
	if workdir == "" || logPath == "" {
		return false, fmt.Errorf("once: workspace and log path are required")
	}
	id := workspace.Identity{Lane: o.Lane, TicketID: issue.ID, Workspace: workdir}
	if err := o.Provision(ctx, o.RepoDir, o.Repo.Provision, id, o.Log); err != nil {
		// Provisioning never starts a lane. Release our claim so the queued
		// ticket remains eligible for a later attempt.
		viewerID, viewerErr := o.Client.Viewer(ctx)
		current, readErr := o.Client.GetIssue(ctx, issue.ID)
		if viewerErr != nil || readErr != nil {
			return false, fmt.Errorf("provision issue %s: %w (verify claim before release: viewer=%v, issue=%v)", issue.ID, err, viewerErr, readErr)
		}
		if current.AssigneeID == viewerID {
			if unassignErr := o.Client.UnassignIssue(ctx, issue.ID); unassignErr != nil {
				return false, fmt.Errorf("provision issue %s: %w (release claim: %v)", issue.ID, err, unassignErr)
			}
		}
		return false, fmt.Errorf("provision issue %s: %w", issue.ID, err)
	}
	defer o.Dispose(context.WithoutCancel(ctx), o.RepoDir, o.Repo.Dispose, id, o.Log)

	result, err := o.Execute(ctx, o.Global.Runners[queue.Runner], queue.Prompt, workdir, logPath)
	if err != nil {
		return true, fmt.Errorf("run issue %s: %w", issue.ID, err)
	}

	target := queue.OnFailure
	if result.ExitCode == 0 {
		target = queue.OnSuccess
	}
	if target == "" {
		// No failure route means leave the ticket in this queue for another
		// attempt. Release only our unchanged claim; an agent or human may
		// have moved or reassigned it while the runner was active.
		current, err := o.Client.GetIssue(ctx, issue.ID)
		if err != nil {
			return true, fmt.Errorf("read failed issue %s: %w", issue.ID, err)
		}
		if current.Status == queue.Status && current.AssigneeID == viewerID {
			if err := o.Client.UnassignIssue(ctx, issue.ID); err != nil {
				return true, fmt.Errorf("release failed issue %s: %w", issue.ID, err)
			}
		}
		return true, nil
	}

	current, err := o.Client.GetIssue(ctx, issue.ID)
	if err != nil {
		return true, fmt.Errorf("read completed issue %s: %w", issue.ID, err)
	}
	if current.Status != queue.Status {
		return true, nil
	}
	if err := o.Client.MoveIssue(ctx, issue.ID, target); err != nil {
		return true, fmt.Errorf("move issue %s to %q: %w", issue.ID, target, err)
	}
	return true, nil
}

func (o OnceOptions) validate() error {
	switch {
	case o.Client == nil:
		return fmt.Errorf("once: client is required")
	case o.Global == nil:
		return fmt.Errorf("once: global config is required")
	case o.Repo == nil:
		return fmt.Errorf("once: repo config is required")
	case o.RepoDir == "":
		return fmt.Errorf("once: repo directory is required")
	case o.Lane < 1:
		return fmt.Errorf("once: lane must be at least 1")
	case o.Workspace == "" && o.WorkspaceFor == nil:
		return fmt.Errorf("once: workspace is required")
	case o.LogPath == "" && o.LogPathFor == nil:
		return fmt.Errorf("once: log path is required")
	}
	return nil
}

func next(ctx context.Context, client linear.Client, global *config.Global, teams []string) (linear.Issue, config.Queue, bool, error) {
	queueNames := make([]string, 0, len(global.Queues))
	for name := range global.Queues {
		queueNames = append(queueNames, name)
	}
	sort.Strings(queueNames)
	for _, team := range teams {
		for _, name := range queueNames {
			queue := global.Queues[name]
			issues, err := client.ListIssues(ctx, team, queue.Status)
			if err != nil {
				return linear.Issue{}, config.Queue{}, false, fmt.Errorf("list %s queue for team %s: %w", queue.Status, team, err)
			}
			for _, issue := range issues {
				if Eligible(issue, map[string]bool{queue.Status: true}) {
					return issue, queue, true, nil
				}
			}
		}
	}
	return linear.Issue{}, config.Queue{}, false, nil
}
