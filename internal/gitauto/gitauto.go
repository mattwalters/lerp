package gitauto

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

// Client is the Linear surface needed to check git automations.
type Client interface {
	TeamGitAutomations(ctx context.Context, teamKey string) ([]linear.GitAutomation, error)
}

// MidStageEvent is one Linear git automation event that fires while a pull request is open.
type MidStageEvent struct {
	Event string
	Label string
}

// MidStageEvents are the Linear git automation events that fire while a pull
// request is open — which, for a ticket a queue is running, is mid-stage —
// paired with the way this warning names them. Listed in the order they fire,
// which is the order the report walks them in.
//
// "merge" is deliberately absent: it fires once the pull request lands, after
// the pipeline is finished with the ticket, and SCOPE names that the benign
// case. So is any event Linear may add that lerp has never heard of — a rule
// lerp cannot place in a run's life is one it cannot honestly advise on, and
// a guess here would be the heuristic this check exists to avoid.
var MidStageEvents = []MidStageEvent{
	{linear.GitEventDraft, "On draft PR open"},
	{linear.GitEventStart, "On PR open"},
	{linear.GitEventReview, "On PR review request or activity"},
	{linear.GitEventMergeable, "On PR ready for merge"},
}

// Collision is one mid-stage automation that would move a ticket to a status
// the repo config never names, paired with the report label for its event.
type Collision struct {
	Automation linear.GitAutomation
	Label      string
}

// Collisions returns the automations that would move a ticket out from under
// a live run, in report order (events in MidStageEvents order, then branch-scoped
// rules sorted by branch).
func Collisions(repo *config.RepoConfig, automations []linear.GitAutomation) []Collision {
	named := make(map[string]bool)
	for _, status := range repo.PromoteTargets() {
		named[status] = true
	}
	var result []Collision
	for _, ev := range MidStageEvents {
		// One automation per event team-wide, plus one per target branch
		// scoped to it; branch order keeps the report stable whatever
		// order Linear lists them in.
		var colliding []linear.GitAutomation
		for _, a := range automations {
			if a.Event == ev.Event && a.Status != "" && !named[a.Status] {
				colliding = append(colliding, a)
			}
		}
		slices.SortFunc(colliding, func(x, y linear.GitAutomation) int {
			return strings.Compare(x.Branch, y.Branch)
		})
		for _, a := range colliding {
			result = append(result, Collision{Automation: a, Label: ev.Label})
		}
	}
	return result
}

// Check reads the team's automations and passes them to Findings.
func Check(ctx context.Context, client Client, repo *config.RepoConfig, team string) []string {
	automations, err := client.TeamGitAutomations(ctx, team)
	return Findings(repo, team, automations, err)
}

// Findings names the automations that would move a ticket out from under a
// live run. Pure: the caller supplies what it read, and readErr non-nil
// produces the one line saying the check did not run.
func Findings(repo *config.RepoConfig, team string, automations []linear.GitAutomation, readErr error) []string {
	if readErr != nil {
		return []string{fmt.Sprintf(
			"team %s: could not read git automations, so they are not checked: %v", team, readErr)}
	}
	var report []string
	for _, c := range Collisions(repo, automations) {
		report = append(report, GitAutomationWarning(team, c.Label, c.Automation, repo)...)
	}
	return report
}

// GitAutomationWarning is one collision as the operator reads it: what the
// automation does, what it costs each stage, and the two ways out. It names
// every queue because lerp cannot know which stage opens the pull request
// that trips the automation — so each line says what a run in that status
// loses if it is the one that opens it, rather than asserting that it will.
func GitAutomationWarning(team, label string, a linear.GitAutomation, repo *config.RepoConfig) []string {
	trigger, scope := fmt.Sprintf("%q", label), fmt.Sprintf("for team %s", team)
	if a.Branch != "" {
		// A branch-scoped rule lives on its own settings row and overrides
		// the team-wide one; an operator sent to the team default would find
		// it already saying something else.
		trigger = fmt.Sprintf("%q (target branch %q)", label, a.Branch)
		scope = fmt.Sprintf("for target branch %q on team %s", a.Branch, team)
	}
	lines := []string{fmt.Sprintf("team %s: %s moves tickets to %q, which %s never names:",
		team, trigger, a.Status, "the repo config")}
	for _, qname := range slices.Sorted(maps.Keys(repo.Queues)) {
		q := repo.Queues[qname]
		lines = append(lines, fmt.Sprintf(
			"  a run in %q that opens a pull request will be moved there mid-stage, losing its on_success hop to %q",
			q.Status, q.OnSuccess))
	}
	return append(lines, fmt.Sprintf(
		"fix: set that automation to No action %s, or point a queue at %q", scope, a.Status))
}
