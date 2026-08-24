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

// VerifyStatuses checks, with one states-read per configured team, that every
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
func VerifyStatuses(ctx context.Context, client linear.Client, repo *config.RepoConfig) error {
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
