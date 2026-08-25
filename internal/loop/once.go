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
		Queue:   queue,
		Ticket:  issue.Identifier,
		Workdir: workdir,
		LogPath: logPath,
	})
	if err != nil {
		return true, fmt.Errorf("run issue %s: %w", issue.ID, err)
	}
	if _, err := conclude(ctx, o.Client, issue, queue, o.Repo, result.ExitCode, viewerID, o.Log); err != nil {
		return true, err
	}
	return true, nil
}

// servedStatuses is the set of Linear statuses some configured queue picks up
// from. Two decisions turn on it: which tickets the attention view calls
// waiting on a human, and whether a concluded run releases its claim.
func servedStatuses(repo *config.RepoConfig) map[string]bool {
	served := make(map[string]bool, len(repo.Queues))
	for _, q := range repo.Queues {
		served[q.Status] = true
	}
	return served
}

// statusRelevance reads the configured pipeline for what each status means
// to a ticket resting in it: on_failure targets are where runs fail,
// on_success targets are where they finish, a queue's own status is served,
// and a status the config never names at all is one the pipeline did not
// put the ticket in. It is derived, never declared — there is no new key in
// lerp.toml, and rewriting the on_success pointers rewrites this with them.
func statusRelevance(repo *config.RepoConfig) func(string) StatusRelevance {
	rank := make(map[string]StatusRelevance, len(repo.Queues)*3)
	// The worse news wins: a status that is one queue's exit and another's
	// failure route is somewhere a run failed.
	set := func(status string, r StatusRelevance) {
		if status == "" {
			return
		}
		if prev, ok := rank[status]; ok && prev <= r {
			return
		}
		rank[status] = r
	}
	for _, q := range repo.Queues {
		set(q.OnFailure, StatusFailed)
		set(q.OnSuccess, StatusFinished)
	}
	// A queue's own status outranks whatever points at it: the pipeline
	// serves it, so a ticket there is not resting anywhere.
	for _, q := range repo.Queues {
		rank[q.Status] = StatusOther
	}
	return func(status string) StatusRelevance {
		if r, ok := rank[status]; ok {
			return r
		}
		return StatusUnnamed
	}
}

// conclude applies the queue's move rule after a run exited, then settles the
// claim. A clean exit moves the ticket to OnSuccess, a non-zero exit to
// OnFailure when configured; either move happens only while the ticket remains
// in the queue status, so an agent or human move wins.
//
// A move that did not happen is reported rather than passed over in silence:
// conclude returns the note naming the hop it skipped, empty when the ticket
// stayed put and the rule applied normally. The caller carries it to the TUI;
// conclude has already written it to the log.
//
// The claim is released exactly when the ticket comes to rest somewhere a queue
// picks up from — whether lerp moved it there or the agent did. An assigned
// ticket is never eligible, so a ticket still holding its claim in a served
// status is stranded there permanently: no later pass can pick it up, and
// nothing reports an error. Coming to rest in a status no queue serves keeps
// the claim on purpose, because that is what parks the ticket on the operator
// in the inbox view.
//
// viewerID is the operating user, and the claim is released only when the
// ticket is still assigned to them: a human who took the run over mid-flight
// keeps what they took. This is the one place in the codebase that decides
// whether a finished run releases its claim — the rule has been got wrong
// twice by being written out a second time (LERP-50, LERP-59), so reap calls
// this rather than carrying its own copy.
func conclude(ctx context.Context, client linear.Client, issue linear.Issue, queue config.Queue, repo *config.RepoConfig, exitCode int, viewerID string, log io.Writer) (string, error) {
	target, rule := queue.OnFailure, "on_failure"
	if exitCode == 0 {
		target, rule = queue.OnSuccess, "on_success"
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
		return "", nil
	}

	current, err := client.GetIssue(ctx, issue.ID)
	if err != nil {
		return "", fmt.Errorf("read completed issue %s: %w", issue.ID, err)
	}
	final := current.Status
	var note string
	switch {
	case final == queue.Status:
		if err := client.MoveIssue(ctx, issue.ID, target); err != nil {
			return "", fmt.Errorf("move issue %s to %q: %w", issue.ID, target, err)
		}
		final = target
	case final != target:
		// Skipping the move is on purpose — but a hop nobody reports is a
		// stage nobody notices was never run. Whoever moved the ticket keeps
		// their move; the log and the TUI only learn what it cost. A ticket
		// already sitting in the target is not this case: whoever moved it
		// made the very hop the rule would have, so nothing was skipped.
		note = skippedHopNote(issue, queue, rule, target, final, namedStatuses(repo))
		if log != nil {
			fmt.Fprintln(log, note)
		}
	}
	if !servedStatuses(repo)[final] {
		return note, nil
	}
	if current.AssigneeID != viewerID {
		// Somebody else holds the ticket now — most often a human who took
		// the run over while it ran. Unassigning would take their claim.
		return note, nil
	}
	if err := client.UnassignIssue(ctx, issue.ID); err != nil {
		return note, fmt.Errorf("release issue %s in %q: %w", issue.ID, final, err)
	}
	return note, nil
}

// namedStatuses is every Linear status lerp.toml names — each queue's status
// and every on_success/on_failure target, which is exactly the promote menu.
// A ticket that comes to rest outside it left the pipeline altogether.
func namedStatuses(repo *config.RepoConfig) map[string]bool {
	named := make(map[string]bool)
	for _, status := range repo.PromoteTargets() {
		named[status] = true
	}
	return named
}

// skippedHopNote is the one line conclude reports when a ticket left its queue
// status mid-run, so the move rule found nothing to apply. A destination the
// pipeline never names earns a second sentence: agents move tickets between
// configured statuses, so a status lerp.toml has never heard of is the
// fingerprint of something else — most often Linear's own GitHub integration,
// which moves a ticket the moment its PR opens.
func skippedHopNote(issue linear.Issue, queue config.Queue, rule, target, final string, named map[string]bool) string {
	note := fmt.Sprintf("%s left %q for %q during its run — the %s hop to %q was skipped.",
		issue.Identifier, queue.Status, final, rule, target)
	if !named[final] {
		note += fmt.Sprintf(" %q is not a status your pipeline names; an external automation"+
			" (e.g. Linear's GitHub integration) may be moving tickets.", final)
	}
	return note
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

// queueListing is one team+queue's raw slice of the board: every ticket
// sitting in the queue's status, eligible or not, in Linear's listing order.
// The fill path narrows it to candidates; the queue view shows all of it.
type queueListing struct {
	team   string
	name   string // queue name
	queue  config.Queue
	issues []linear.Issue
}

// listQueues lists every configured queue across the repo's teams, in a
// deterministic order: configured team order, then queue name, then whatever
// order Linear lists issues in.
//
// A listing that fails does not discard the rest: the queues that were listed
// are returned alongside the joined errors, so one broken queue cannot starve
// every lane while the outage lasts. Callers act on what was found and report
// the error.
func listQueues(ctx context.Context, client linear.Client, repo *config.RepoConfig) ([]queueListing, error) {
	queueNames := make([]string, 0, len(repo.Queues))
	for name := range repo.Queues {
		queueNames = append(queueNames, name)
	}
	sort.Strings(queueNames)
	var listings []queueListing
	var errs []error
	for _, team := range repo.Teams {
		for _, name := range queueNames {
			queue := repo.Queues[name]
			issues, err := client.ListIssues(ctx, team, queue.Status)
			if err != nil {
				errs = append(errs, fmt.Errorf("list %s queue for team %s: %w", queue.Status, team, err))
				continue
			}
			listings = append(listings, queueListing{team: team, name: name, queue: queue, issues: issues})
		}
	}
	return listings, errors.Join(errs...)
}

// candidates lists every eligible ticket across the repo's teams and queues,
// in listQueues's deterministic order.
func candidates(ctx context.Context, client linear.Client, repo *config.RepoConfig) ([]candidate, error) {
	listings, err := listQueues(ctx, client, repo)
	return candidatesFrom(listings), err
}

// candidatesFrom narrows raw listings to the tickets a queue could pick up
// right now, preserving the listings' order. The queue view and the fill path
// must agree on what runs next, so both derive from the same listing.
func candidatesFrom(listings []queueListing) []candidate {
	var cands []candidate
	for _, l := range listings {
		for _, issue := range l.issues {
			if Eligible(issue, map[string]bool{l.queue.Status: true}) {
				cands = append(cands, candidate{issue: issue, name: l.name, queue: l.queue})
			}
		}
	}
	return cands
}
