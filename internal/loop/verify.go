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
// Every miss is reported, each naming the team, the queue and config key, the
// missing status, and the team's actual state names — so a near-miss is
// visible at a glance. The loop's regular passes never re-check.
func VerifyStatuses(ctx context.Context, client linear.Client, repo *config.RepoConfig) error {
	var missing []error
	for _, team := range repo.Teams {
		names, err := client.TeamStates(ctx, team)
		if err != nil {
			return fmt.Errorf("verify statuses for team %s: %w", team, err)
		}
		exists := make(map[string]bool, len(names))
		for _, name := range names {
			exists[name] = true
		}
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
				missing = append(missing, fmt.Errorf("team %s has no status %q (queue %q, %s); it has: %s",
					team, ref.status, qname, ref.key, strings.Join(names, ", ")))
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w\nedit %s or run `lerp init` to create the missing statuses",
		errors.Join(missing...), config.RepoConfigFile)
}
