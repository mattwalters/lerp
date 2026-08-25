package loop

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

// Verify runs lerp's startup checks against the board, once, before the first
// reconciler pass — the one place that says whether the board and the config
// agree, in one report format. It returns two kinds of finding, because the
// two disagreements it knows about are not the same kind of broken:
//
//   - An error refuses the run. A configured status that does not exist is
//     unambiguously broken, and nothing lerp does afterwards can be right.
//   - Warnings are printed and the run starts anyway. A team git automation
//     that would move a ticket mid-stage is a strong smell that may be
//     deliberate in ways lerp cannot see, so it names itself and the fix and
//     gets out of the way.
//
// Warnings are only computed once the statuses check has passed: a board
// missing the statuses the config names has nothing to say about which of
// them an automation collides with.
func Verify(ctx context.Context, client linear.Client, repo *config.RepoConfig) ([]string, error) {
	if err := verifyStatuses(ctx, client, repo); err != nil {
		return nil, err
	}
	return verifyGitAutomations(ctx, client, repo), nil
}

// verifyStatuses checks, with one states-read per configured team, that every
// status name the repo config points at — each queue's status and its
// on_success and on_failure targets — names an existing workflow state on
// that team. It runs once at startup, before the first reconciler pass, in
// the same refuse-to-run spirit as SCOPE invariant 2's team → repo check: a
// misspelled or renamed queue status would otherwise read as a permanently
// empty queue (ListIssues filters by exact state name and reports no error),
// and a missing on_success target would fail only after an agent's whole run.
//
// The report is grouped per team: a lead line with the miss count, one line
// per missing status naming every config reference that points at it
// (queue.key), and the team's actual state names once — so a near-miss is
// visible at a glance without repetition. The loop's regular passes never
// re-check.
func verifyStatuses(ctx context.Context, client linear.Client, repo *config.RepoConfig) error {
	var report []string
	for _, team := range repo.Teams {
		names, err := client.TeamStates(ctx, team)
		if err != nil {
			return fmt.Errorf("verify statuses for team %s: %w", team, err)
		}
		exists := make(map[string]bool, len(names))
		for _, name := range names {
			exists[name] = true
		}
		// Missing status → the config references pointing at it, gathered in
		// sorted queue order (and status, on_success, on_failure within one).
		refs := make(map[string][]string)
		for _, qname := range slices.Sorted(maps.Keys(repo.Queues)) {
			q := repo.Queues[qname]
			for _, ref := range []struct{ key, status string }{
				{"status", q.Status},
				{"on_success", q.OnSuccess},
				{"on_failure", q.OnFailure},
			} {
				if ref.status == "" || exists[ref.status] {
					continue // on_failure is optional; config validation rejects other empties
				}
				refs[ref.status] = append(refs[ref.status], qname+"."+ref.key)
			}
		}
		if len(refs) == 0 {
			continue
		}
		plural := "statuses"
		if len(refs) == 1 {
			plural = "status"
		}
		report = append(report, fmt.Sprintf("team %s is missing %d %s referenced by %s:",
			team, len(refs), plural, config.RepoConfigFile))
		for _, status := range slices.Sorted(maps.Keys(refs)) {
			report = append(report, fmt.Sprintf("  %q (%s)", status, strings.Join(refs[status], ", ")))
		}
		report = append(report, fmt.Sprintf("team %s has: %s", team, strings.Join(names, ", ")))
	}
	if len(report) == 0 {
		return nil
	}
	report = append(report, fmt.Sprintf("edit %s or run `lerp init` to create the missing statuses",
		config.RepoConfigFile))
	return errors.New(strings.Join(report, "\n"))
}

// midStageEvents are the Linear git automation events that fire while a pull
// request is open — which, for a ticket a queue is running, is mid-stage —
// paired with the way this warning names them. Listed in the order they fire,
// which is the order the report walks them in.
//
// "merge" is deliberately absent: it fires once the pull request lands, after
// the pipeline is finished with the ticket, and SCOPE names that the benign
// case. So is any event Linear may add that lerp has never heard of — a rule
// lerp cannot place in a run's life is one it cannot honestly advise on, and
// a guess here would be the heuristic this check exists to avoid.
var midStageEvents = []struct{ event, label string }{
	{linear.GitEventDraft, "On draft PR open"},
	{linear.GitEventStart, "On PR open"},
	{linear.GitEventReview, "On review requested"},
	{linear.GitEventMergeable, "On PR ready for merge"},
}

// verifyGitAutomations reports the team git automations that would move a
// ticket out from under a live run. Linear's git automations are configured
// per team; one that fires mid-stage moves the ticket while an agent is still
// working it, and conclude then respects that move and skips the queue's
// on_success hop — the collision LERP-54 catches after the fact.
//
// An automation whose target status the config does name is silent. That is
// the deliberate configuration an adopter reaches for when they want both
// (see LERP-55): they have pointed a queue at the automation's target on
// purpose, and warning would punish them for having understood the problem.
// That rule is what keeps this quiet — the check fires only when the board
// and the config genuinely disagree.
//
// A team whose automations cannot be read warns rather than refusing: this
// check never blocks a run, and one that failed to run is a thing to say, not
// a reason to stop.
func verifyGitAutomations(ctx context.Context, client linear.Client, repo *config.RepoConfig) []string {
	// Every status lerp.toml names: each queue's own status plus every
	// on_success and on_failure target, which is exactly the list promote
	// offers.
	named := make(map[string]bool)
	for _, status := range repo.PromoteTargets() {
		named[status] = true
	}
	var report []string
	for _, team := range repo.Teams {
		automations, err := client.TeamGitAutomations(ctx, team)
		if err != nil {
			report = append(report, fmt.Sprintf(
				"team %s: could not read git automations, so this run is not checked for them: %v", team, err))
			continue
		}
		for _, ev := range midStageEvents {
			// One automation per event team-wide, plus one per target branch
			// scoped to it; branch order keeps the report stable whatever
			// order Linear lists them in.
			var colliding []linear.GitAutomation
			for _, a := range automations {
				if a.Event == ev.event && a.Status != "" && !named[a.Status] {
					colliding = append(colliding, a)
				}
			}
			slices.SortFunc(colliding, func(x, y linear.GitAutomation) int {
				return strings.Compare(x.Branch, y.Branch)
			})
			for _, a := range colliding {
				report = append(report, gitAutomationWarning(team, ev.label, a, repo)...)
			}
		}
	}
	return report
}

// gitAutomationWarning is one collision as the operator reads it: what the
// automation does, what it costs each stage, and the two ways out. It names
// every queue because lerp cannot know which stage opens the pull request
// that trips the automation — any run the rule reaches loses its hop.
func gitAutomationWarning(team, label string, a linear.GitAutomation, repo *config.RepoConfig) []string {
	trigger, scope := fmt.Sprintf("%q", label), fmt.Sprintf("for team %s", team)
	if a.Branch != "" {
		// A branch-scoped rule lives on its own settings row and overrides
		// the team-wide one; an operator sent to the team default would find
		// it already saying something else.
		trigger = fmt.Sprintf("%q (target branch %q)", label, a.Branch)
		scope = fmt.Sprintf("for target branch %q on team %s", a.Branch, team)
	}
	lines := []string{fmt.Sprintf("team %s: %s moves tickets to %q, which %s never names:",
		team, trigger, a.Status, config.RepoConfigFile)}
	for _, qname := range slices.Sorted(maps.Keys(repo.Queues)) {
		q := repo.Queues[qname]
		lines = append(lines, fmt.Sprintf(
			"  a run in %q will be moved there mid-stage and its on_success hop to %q skipped",
			q.Status, q.OnSuccess))
	}
	return append(lines, fmt.Sprintf(
		"fix: set that automation to No action %s, or point a queue at %q", scope, a.Status))
}
