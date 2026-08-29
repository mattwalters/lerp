//go:build unix

package loop

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/gitauto"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/vendors"
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
//     gets out of the way. A vendor runner CLI without a Linear MCP server
//     configured warns that its runs cannot read tickets or leave verdicts.
//
// Warnings are only computed once the statuses check has passed: a board
// missing the statuses the config names has nothing to say about which of
// them an automation collides with.
func Verify(ctx context.Context, client linear.Client, repo *config.RepoConfig, repoDir string) ([]string, error) {
	if err := verifyStatuses(ctx, client, repo); err != nil {
		return nil, err
	}
	warnings := verifyGitAutomations(ctx, client, repo)
	warnings = append(warnings, verifyLinearMCP(repo, repoDir)...)
	return warnings, nil
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
			team, len(refs), plural, "the repo config"))
		for _, status := range slices.Sorted(maps.Keys(refs)) {
			report = append(report, fmt.Sprintf("  %q (%s)", status, strings.Join(refs[status], ", ")))
		}
		report = append(report, fmt.Sprintf("team %s has: %s", team, strings.Join(names, ", ")))
	}
	if len(report) == 0 {
		return nil
	}
	report = append(report, fmt.Sprintf("edit %s or run `lerp init` to create the missing statuses",
		"the repo config"))
	return errors.New(strings.Join(report, "\n"))
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
	var report []string
	for _, team := range repo.Teams {
		report = append(report, gitauto.Check(ctx, client, repo, team)...)
	}
	return report
}

// verifyLinearMCP reports any vendor runner whose CLI does not have a Linear
// MCP server configured. Each named vendor runner is checked (command runners
// are skipped as opaque). It warns rather than refusing: runs might still
// succeed if the operator has other mechanisms, but without MCP the runs
// cannot read tickets or leave verdicts.
func verifyLinearMCP(repo *config.RepoConfig, repoDir string) []string {
	runnerQueues := make(map[string][]string)
	for _, qname := range slices.Sorted(maps.Keys(repo.Queues)) {
		q := repo.Queues[qname]
		if q.Runner != "" && q.Status != "" {
			runnerQueues[q.Runner] = append(runnerQueues[q.Runner], q.Status)
		}
	}

	var report []string
	for _, rname := range slices.Sorted(maps.Keys(repo.Runners)) {
		r := repo.Runners[rname]
		if r.Vendor == "" {
			continue // command runners are opaque — skip them
		}
		adapter, ok := vendors.Lookup(r.Vendor)
		if !ok {
			continue
		}
		if adapter.HasLinearMCP(repoDir) {
			continue
		}

		queues := runnerQueues[rname]
		slices.Sort(queues)
		queues = slices.Compact(queues)

		var queueLabel string
		if len(queues) == 1 {
			queueLabel = fmt.Sprintf(" (queue %q)", queues[0])
		} else if len(queues) > 1 {
			quoted := make([]string, len(queues))
			for i, q := range queues {
				quoted[i] = fmt.Sprintf("%q", q)
			}
			queueLabel = fmt.Sprintf(" (queues %s)", strings.Join(quoted, ", "))
		}

		fixCmd := strings.Join(adapter.MCPRegisterHTTP(), " ")
		report = append(report, fmt.Sprintf(
			"runner %q%s names the %s CLI, which has no Linear MCP server configured — its runs cannot read tickets or leave verdicts. Fix: %s, then authenticate via %s.",
			rname, queueLabel, adapter.CLIName(), fixCmd, adapter.AuthInstruction(),
		))
	}
	return report
}
