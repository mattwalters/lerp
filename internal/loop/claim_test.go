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

type failingClient struct {
	linear.Client
	viewerErr error
}

func (c failingClient) Viewer(context.Context) (string, error) { return "", c.viewerErr }
