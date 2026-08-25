package loop

import (
	"context"
	"errors"
	"testing"

	"github.com/mattwalters/lerp/internal/linear"
)

func TestClaimWin(t *testing.T) {
	f := linear.NewFake()
	f.SetViewer("me")
	f.AddIssue("LERP", linear.Issue{ID: "iss-1", Identifier: "LERP-1", Status: "Todo"})

	issue, viewerID, won, err := Claim(context.Background(), f, "iss-1")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !won {
		t.Error("Claim won = false, want true")
	}
	if viewerID != "me" || issue.AssigneeID != "me" {
		t.Errorf("Claim read-back = (%q, %+v), want the operating user", viewerID, issue)
	}
}

func TestClaimLostRace(t *testing.T) {
	f := linear.NewFake()
	f.SetViewer("me")
	f.AddIssue("LERP", linear.Issue{ID: "iss-1", Identifier: "LERP-1", Status: "Todo"})

	_, _, won, err := Claim(context.Background(), losingClient{Fake: f, rivalID: "someone-else"}, "iss-1")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if won {
		t.Error("Claim won = true, want false")
	}
}

func TestClaimAPIError(t *testing.T) {
	errBoom := errors.New("temporary Linear failure")
	_, _, won, err := Claim(context.Background(), failingClient{Client: linear.NewFake(), viewerErr: errBoom}, "iss-1")
	if won {
		t.Error("Claim won = true, want false")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("Claim error = %v, want wrapped %v", err, errBoom)
	}
}

// A ticket that leaves the queue while being claimed must not keep the claim:
// an assigned ticket is never eligible, so it would be stranded wherever it
// now sits until a human noticed.
func TestClaimForQueueReleasesATicketMovedDuringTheClaim(t *testing.T) {
	ctx := context.Background()
	f := linear.NewFake()
	f.SetViewer("me")
	f.AddIssue("LERP", linear.Issue{ID: "iss-1", Identifier: "LERP-1", Status: "Todo"})

	client := movedOnAssign{Client: f, move: func(issueID string) {
		if err := f.MoveIssue(ctx, issueID, "Escalated"); err != nil {
			t.Error(err)
		}
	}}
	viewerID, won, err := claimForQueue(ctx, client, "iss-1", "Todo")
	if err != nil || won {
		t.Fatalf("claimForQueue = (%v, %v), want the claim given up", won, err)
	}
	if viewerID != "me" {
		t.Errorf("viewerID = %q, want the operating user for later claim bookkeeping", viewerID)
	}
	got, _ := f.GetIssue(ctx, "iss-1")
	if got.Status != "Escalated" {
		t.Errorf("status = %q, want Escalated", got.Status)
	}
	if got.AssigneeID != "" {
		t.Errorf("assignee = %q, want the claim released so the ticket is not stranded", got.AssigneeID)
	}
}

func TestEligible(t *testing.T) {
	statuses := map[string]bool{"Todo": true}
	tests := []struct {
		name  string
		issue linear.Issue
		want  bool
	}{
		{name: "candidate", issue: linear.Issue{Status: "Todo"}, want: true},
		{name: "outside queue", issue: linear.Issue{Status: "Elsewhere"}},
		{name: "assigned", issue: linear.Issue{Status: "Todo", AssigneeID: "user-1"}},
		{name: "blocked", issue: linear.Issue{Status: "Todo", Blocked: true, BlockedBy: []string{"LERP-2"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Eligible(tt.issue, statuses); got != tt.want {
				t.Errorf("Eligible(%+v) = %v, want %v", tt.issue, got, tt.want)
			}
		})
	}
}

type losingClient struct {
	*linear.Fake
	rivalID string
}

func (c losingClient) AssignIssue(ctx context.Context, issueID, userID string) error {
	if err := c.Fake.AssignIssue(ctx, issueID, userID); err != nil {
		return err
	}
	return c.Fake.AssignIssue(ctx, issueID, c.rivalID)
}

// movedOnAssign is a Client whose AssignIssue succeeds and then lets the test
// move the ticket, standing in for a human or agent racing the claim.
type movedOnAssign struct {
	linear.Client
	move func(issueID string)
}

func (c movedOnAssign) AssignIssue(ctx context.Context, issueID, userID string) error {
	if err := c.Client.AssignIssue(ctx, issueID, userID); err != nil {
		return err
	}
	c.move(issueID)
	return nil
}

type failingClient struct {
	linear.Client
	viewerErr error
}

func (c failingClient) Viewer(context.Context) (string, error) { return "", c.viewerErr }
