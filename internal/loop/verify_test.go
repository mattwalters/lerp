//go:build unix

package loop

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mattwalters/lerp/internal/linear"
)

func TestVerifyStatusesPassesWhenEveryStatusExists(t *testing.T) {
	fake := linear.NewFake()
	fake.SetTeamStates("LERP", "Backlog", "Todo", "Done", "Needs Help")
	if err := VerifyStatuses(context.Background(), fake, testRepo()); err != nil {
		t.Fatalf("VerifyStatuses = %v, want nil", err)
	}
}

func TestVerifyStatusesNamesEveryMiss(t *testing.T) {
	fake := linear.NewFake()
	// The queue status exists; both move targets are missing.
	fake.SetTeamStates("LERP", "Backlog", "Todo", "Doen", "Halp")
	err := VerifyStatuses(context.Background(), fake, testRepo())
	if err == nil {
		t.Fatal("VerifyStatuses = nil, want an error")
	}
	msg := err.Error()
	for _, want := range []string{
		// The lead line counts the misses (plural).
		"team LERP is missing 2 statuses referenced by lerp.toml:",
		// One line per missing status, naming the reference that points at it.
		`"Done" (todo.on_success)`,
		`"Needs Help" (todo.on_failure)`,
		// The team's actual names, so the operator sees the near-miss.
		"team LERP has: Backlog, Todo, Doen, Halp",
		// The way out.
		"edit lerp.toml or run `lerp init`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q\nmissing %q", msg, want)
		}
	}
}

func TestVerifyStatusesGroupsReferencesByMissingStatus(t *testing.T) {
	fake := linear.NewFake()
	// Two queues point at the same missing status; the report gets one line
	// for it listing both references, and the team's status list once.
	fake.SetTeamStates("LERP", "Todo", "Done")
	repo := testRepo()
	review := repo.Queues["todo"]
	review.Status = "Done"
	repo.Queues["review"] = review
	err := VerifyStatuses(context.Background(), fake, repo)
	if err == nil {
		t.Fatal("VerifyStatuses = nil, want an error")
	}
	msg := err.Error()
	if want := `"Needs Help" (review.on_failure, todo.on_failure)`; !strings.Contains(msg, want) {
		t.Errorf("error %q\nmissing %q", msg, want)
	}
	if got := strings.Count(msg, "team LERP has: Todo, Done"); got != 1 {
		t.Errorf("status list printed %d times, want once\nerror %q", got, msg)
	}
}

func TestVerifyStatusesReportsMissingQueueStatus(t *testing.T) {
	fake := linear.NewFake()
	fake.SetTeamStates("LERP", "Backlog", "Doing", "Done", "Needs Help")
	err := VerifyStatuses(context.Background(), fake, testRepo())
	if err == nil {
		t.Fatal("VerifyStatuses = nil, want an error")
	}
	for _, want := range []string{
		// A single miss reads singular and still names its reference.
		"team LERP is missing 1 status referenced by lerp.toml:",
		`"Todo" (todo.status)`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q\nmissing %q", err, want)
		}
	}
}

func TestVerifyStatusesToleratesAbsentOnFailure(t *testing.T) {
	fake := linear.NewFake()
	fake.SetTeamStates("LERP", "Todo", "Done")
	repo := testRepo()
	q := repo.Queues["todo"]
	q.OnFailure = ""
	repo.Queues["todo"] = q
	if err := VerifyStatuses(context.Background(), fake, repo); err != nil {
		t.Fatalf("VerifyStatuses = %v, want nil", err)
	}
}

func TestVerifyStatusesFailsOnUnknownTeam(t *testing.T) {
	err := VerifyStatuses(context.Background(), linear.NewFake(), testRepo())
	if err == nil || !strings.Contains(err.Error(), `team "LERP"`) {
		t.Fatalf("error = %v, want the unknown team named", err)
	}
}

// countingStates wraps the fake to count TeamStates reads.
type countingStates struct {
	linear.Client
	calls atomic.Int64
}

func (c *countingStates) TeamStates(ctx context.Context, teamKey string) ([]string, error) {
	c.calls.Add(1)
	return c.Client.TeamStates(ctx, teamKey)
}

func TestVerifyStatusesReadsEachTeamOnce(t *testing.T) {
	fake := linear.NewFake()
	fake.SetTeamStates("LERP", "Todo", "Done", "Needs Help")
	repo := testRepo()
	// A second queue on the same team must not cost a second read.
	repo.Queues["review"] = repo.Queues["todo"]
	q := repo.Queues["review"]
	q.Status = "Done"
	repo.Queues["review"] = q
	client := &countingStates{Client: fake}
	if err := VerifyStatuses(context.Background(), client, repo); err != nil {
		t.Fatalf("VerifyStatuses = %v, want nil", err)
	}
	if got := client.calls.Load(); got != 1 {
		t.Errorf("TeamStates reads = %d, want 1", got)
	}
}
