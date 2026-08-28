//go:build unix

package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/credentials"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/run"
	"github.com/mattwalters/lerp/internal/workspace"
)

// ExecuteFunc runs one configured coding-agent command. It is a function type
// so the loop can be tested without starting a real agent.
type ExecuteFunc func(context.Context, run.Invocation) (run.Result, error)

// ProvisionFunc and DisposeFunc create and remove a workspace. They are
// function types for the same reason as ExecuteFunc.
type ProvisionFunc func(context.Context, string, string, workspace.Identity, io.Writer) error
type DisposeFunc func(context.Context, string, string, workspace.Identity, io.Writer)

// statusRelevance reads the configured pipeline for what each status means
// to a ticket resting in it: on_failure targets are where runs fail,
// on_success targets are where they finish, a queue's own status is served,
// and a status the config never names at all is one the pipeline did not
// put the ticket in. It is derived, never declared — there is no new key in
// lerp.toml, and rewriting the on_success pointers rewrites this with them.
//
// The one thing config cannot say is which of the statuses it never names
// a ticket was already sitting in before anything routed it. Linear's own
// state category answers that, so it is the second argument: a status the
// pipeline does not name is only news when Linear files it as started.
// Every board keeps its intake somewhere — triage, a backlog, a Todo
// column — and a whole board's worth of tickets resting where they were
// filed is not something to mark; a ticket moved into a status that means
// work is under way, by something the pipeline knows nothing about, is.
func statusRelevance(repo *config.RepoConfig) func(status, category string) StatusRelevance {
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
	return func(status, category string) StatusRelevance {
		if r, ok := rank[status]; ok {
			return r
		}
		switch category {
		case linear.CategoryTriage, linear.CategoryBacklog, linear.CategoryUnstarted:
			return StatusBacklog
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
// conclude also returns the status the ticket came to rest in, as observed at
// settlement — telemetry's status field. It is best-effort past the first
// read: a failure that leaves what happened next unknown returns "" rather
// than guessing.
//
// The claim is released wherever the ticket comes to rest — whether lerp moved
// it there or the agent did, and whether or not a queue serves that status. An
// assigned ticket is never eligible, so a ticket still holding its claim is
// stranded the moment anybody moves it into a served status: no later pass can
// pick it up, and nothing reports an error.
//
// Parking the claim at a gate used to look free, on the reasoning that holding
// it is what rests the ticket on the operator in the inbox. It was not: the
// inbox lists unassigned tickets in unserved statuses too, so the gate was
// always the status and never the claim — while the parked claim stranded
// every ticket a human then moved on in Linear instead of with `p`, which is
// the routing the docs document (LERP-113). The claim is a lock on work in
// progress; a ticket resting at a gate is not in progress.
//
// The one exception returns above: an exit code this queue has no route for
// keeps its claim, in the queue's own served status, to stop the re-run spin.
//
// viewerID is the operating user. A ticket assigned to somebody else by the
// time the run ends was taken over mid-flight, and conclude leaves the whole
// rule alone for them — no move and no release, reported rather than silent.
// A human who took the run over keeps what they took, and the hop is part of
// it. This is the one place in the codebase that decides whether a finished
// run releases its claim — the rule has been got wrong twice by being written
// out a second time (LERP-50, LERP-59), so reap calls this rather than
// carrying its own copy.
func conclude(ctx context.Context, client linear.Client, issue linear.Issue, queue config.Queue, repo *config.RepoConfig, exitCode int, viewerID string, log io.Writer) (string, string, error) {
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
		// No GetIssue here — the ticket is assumed still in queue.Status for
		// the claim-holding decision above, but an agent that moved it itself
		// before exiting non-zero would make that a guess, not an
		// observation. Best-effort past the first read means "" here too.
		return "", "", nil
	}

	current, err := client.GetIssue(ctx, issue.ID)
	if err != nil {
		return "", "", fmt.Errorf("read completed issue %s: %w", issue.ID, err)
	}
	var note string
	if current.AssigneeID != "" && current.AssigneeID != viewerID {
		// Somebody else holds the ticket now — most often a human who took
		// the run over while it ran, sometimes an automation. The hop is
		// theirs to make along with the claim: making it here and then
		// declining to release, as the rule below would, pushes the ticket
		// into a served status still assigned, where no queue can pick it up
		// (an assigned ticket is never eligible) and no inbox lists it — a
		// state strictly worse than either half of the rule alone. So the
		// takeover skips the whole of it, and says so.
		note = takenOverNote(issue, rule, target)
		if log != nil {
			fmt.Fprintln(log, note)
		}
		return note, current.Status, nil
	}
	final := current.Status
	switch {
	case final == queue.Status:
		if _, err := client.MoveIssue(ctx, issue.ID, target); err != nil {
			return "", final, fmt.Errorf("move issue %s to %q: %w", issue.ID, target, err)
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
	if current.AssigneeID != viewerID {
		// Nobody holds it: whoever released the claim mid-run already did
		// what this would do. (A ticket somebody else holds returned above,
		// without its move.)
		return note, final, nil
	}
	if err := client.UnassignIssue(ctx, issue.ID); err != nil {
		return note, final, fmt.Errorf("release issue %s in %q: %w", issue.ID, final, err)
	}
	return note, final, nil
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

// takenOverNote is what conclude reports when the ticket belongs to somebody
// else by the time the run ends. Like a skipped hop, a stage silently not run
// is a stage nobody notices was never run — and here the run really did the
// work, so what it cost is worth naming.
func takenOverNote(issue linear.Issue, rule, target string) string {
	return fmt.Sprintf("%s was reassigned during its run — the %s hop to %q was skipped"+
		" and the claim left with whoever took it.", issue.Identifier, rule, target)
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
				if errors.Is(err, credentials.ErrLoginRequired) {
					return nil, err
				}
				errs = append(errs, fmt.Errorf("list %s queue for team %s: %w", queue.Status, team, err))
				continue
			}
			listings = append(listings, queueListing{team: team, name: name, queue: queue, issues: issues})
		}
	}
	return listings, errors.Join(errs...)
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
