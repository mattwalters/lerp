package loop

import (
	"context"
	"errors"
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
type ExecuteFunc func(context.Context, run.Invocation) (run.Result, error)

// PathFunc derives an ephemeral path from the issue chosen for this run.
type PathFunc func(linear.Issue) string

// ProvisionFunc and DisposeFunc create and remove a workspace. They are
// function types for the same reason as ExecuteFunc.
type ProvisionFunc func(context.Context, string, string, workspace.Identity, io.Writer) error
type DisposeFunc func(context.Context, string, string, workspace.Identity, io.Writer)

// OnceOptions supplies the one lane used by Once. Workspace and LogPath are
// intentionally caller-owned: run evidence belongs to the reconciler, not
// this vertical slice.
type OnceOptions struct {
	Client       linear.Client
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
// harness for the single-lane vertical slice; the reconciler owns lane
// selection and run evidence.
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

	cands, listErr := candidates(ctx, o.Client, o.Repo)
	if len(cands) == 0 {
		return false, listErr
	}
	if listErr != nil && o.Log != nil {
		// A ticket was found, so run it; the broken listings only narrowed
		// the choice.
		fmt.Fprintf(o.Log, "some queues could not be listed: %v\n", listErr)
	}
	issue, queue := cands[0].issue, cands[0].queue

	viewerID, won, err := claimForQueue(ctx, o.Client, issue.ID, queue.Status)
	if err != nil || !won {
		return false, err
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
	// Registered before provisioning, not after: a provision command that
	// created its workspace and then failed partway must still be cleaned up,
	// or the next attempt collides with what it left behind. Dispose reports
	// its own failures to the log and never blocks the caller, so running it
	// against a workspace that was never created is harmless.
	defer o.Dispose(context.WithoutCancel(ctx), o.RepoDir, o.Repo.Dispose, id, o.Log)
	if err := o.Provision(ctx, o.RepoDir, o.Repo.Provision, id, o.Log); err != nil {
		// Provisioning never starts a lane. Release our claim so the queued
		// ticket remains eligible for a later attempt.
		if releaseErr := releaseClaim(ctx, o.Client, issue.ID, viewerID); releaseErr != nil {
			return false, fmt.Errorf("provision issue %s: %w (%v)", issue.ID, err, releaseErr)
		}
		return false, fmt.Errorf("provision issue %s: %w", issue.ID, err)
	}

	result, err := o.Execute(ctx, run.Invocation{
		Runner:  o.Repo.Runners[queue.Runner],
		Prompt:  queue.Prompt,
		Ticket:  issue.Identifier,
		Workdir: workdir,
		LogPath: logPath,
	})
	if err != nil {
		return true, fmt.Errorf("run issue %s: %w", issue.ID, err)
	}
	if err := conclude(ctx, o.Client, issue, queue, result.ExitCode, o.Log); err != nil {
		return true, err
	}
	return true, nil
}

// conclude applies the queue's move rule after a run exited: a clean exit
// moves the ticket to OnSuccess, a non-zero exit to OnFailure when
// configured. In either case, the transition happens only when the ticket
// remains in the queue status; an agent or human move wins.
func conclude(ctx context.Context, client linear.Client, issue linear.Issue, queue config.Queue, exitCode int, log io.Writer) error {
	target := queue.OnFailure
	if exitCode == 0 {
		target = queue.OnSuccess
	}
	if target == "" {
		// A failed run with no failure route stays where it is, and stays
		// claimed. Releasing it would make it eligible again immediately, so a
		// ticket that fails every time would be re-run on every pass, spending
		// agent compute forever and starving the lane. Holding the claim stops
		// the spin and leaves the ticket visibly waiting on a human.
		if log != nil {
			fmt.Fprintf(log, "%s exited %d and its queue has no on_failure route: leaving it claimed for a human\n",
				issue.Identifier, exitCode)
		}
		return nil
	}

	current, err := client.GetIssue(ctx, issue.ID)
	if err != nil {
		return fmt.Errorf("read completed issue %s: %w", issue.ID, err)
	}
	if current.Status != queue.Status {
		return nil
	}
	if err := client.MoveIssue(ctx, issue.ID, target); err != nil {
		return fmt.Errorf("move issue %s to %q: %w", issue.ID, target, err)
	}
	return nil
}

func (o OnceOptions) validate() error {
	switch {
	case o.Client == nil:
		return fmt.Errorf("once: client is required")
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

// candidate is one ticket a queue could pick up right now.
type candidate struct {
	issue linear.Issue
	name  string // queue name, for events and run records
	queue config.Queue
}

// candidates lists every eligible ticket across the repo's teams and queues,
// in a deterministic order: configured team order, then queue name, then
// whatever order Linear lists issues in.
//
// A listing that fails does not discard the rest: the tickets that were
// listed are returned alongside the joined errors, so one broken queue cannot
// starve every lane while the outage lasts. Callers act on what was found and
// report the error.
func candidates(ctx context.Context, client linear.Client, repo *config.RepoConfig) ([]candidate, error) {
	queueNames := make([]string, 0, len(repo.Queues))
	for name := range repo.Queues {
		queueNames = append(queueNames, name)
	}
	sort.Strings(queueNames)
	var cands []candidate
	var errs []error
	for _, team := range repo.Teams {
		for _, name := range queueNames {
			queue := repo.Queues[name]
			issues, err := client.ListIssues(ctx, team, queue.Status)
			if err != nil {
				errs = append(errs, fmt.Errorf("list %s queue for team %s: %w", queue.Status, team, err))
				continue
			}
			for _, issue := range issues {
				if Eligible(issue, map[string]bool{queue.Status: true}) {
					cands = append(cands, candidate{issue: issue, name: name, queue: queue})
				}
			}
		}
	}
	return cands, errors.Join(errs...)
}
