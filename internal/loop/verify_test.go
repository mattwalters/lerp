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
		// Precision per problem: team, missing name, queue and config key.
		`team LERP has no status "Done" (queue "todo", on_success)`,
		`team LERP has no status "Needs Help" (queue "todo", on_failure)`,
		// The team's actual names, so the operator sees the near-miss.
		"it has: Backlog, Todo, Doen, Halp",
		// The way out.
		"edit lerp.toml or run `lerp init`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q\nmissing %q", msg, want)
		}
	}
}

func TestVerifyStatusesReportsMissingQueueStatus(t *testing.T) {
	fake := linear.NewFake()
	fake.SetTeamStates("LERP", "Backlog", "Doing", "Done", "Needs Help")
	err := VerifyStatuses(context.Background(), fake, testRepo())
	if err == nil {
		t.Fatal("VerifyStatuses = nil, want an error")
	}
	if want := `team LERP has no status "Todo" (queue "todo", status)`; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q\nmissing %q", err, want)
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
