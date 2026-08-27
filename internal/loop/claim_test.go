//go:build unix

package loop

import (
	"context"
	"errors"
	"strings"
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
		if _, err := f.MoveIssue(ctx, issueID, "Escalated"); err != nil {
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

// The moved-mid-claim release goes through releaseClaim, so a colleague who
// overwrites the claim in the window after the read-back keeps it. That path
// is racy by definition — a move has already beaten the claim — which is
// exactly where a raw unassign clears the wrong person's claim.
func TestClaimForQueueLeavesAColleaguesClaimOnAMovedTicket(t *testing.T) {
	ctx := context.Background()
	f := linear.NewFake()
	f.SetViewer("me")
	f.AddIssue("LERP", linear.Issue{ID: "iss-1", Identifier: "LERP-1", Status: "Todo"})

	moved := movedOnAssign{Client: f, move: func(issueID string) {
		if _, err := f.MoveIssue(ctx, issueID, "Escalated"); err != nil {
			t.Error(err)
		}
	}}
	client := &stolenAfterReadBack{Client: moved, fake: f, rivalID: "colleague"}

	viewerID, won, err := claimForQueue(ctx, client, "iss-1", "Todo")
	if err != nil || won {
		t.Fatalf("claimForQueue = (%v, %v), want the claim given up", won, err)
	}
	if viewerID != "me" {
		t.Errorf("viewerID = %q, want the operating user for later claim bookkeeping", viewerID)
	}
	got, _ := f.GetIssue(ctx, "iss-1")
	if got.AssigneeID != "colleague" {
		t.Errorf("assignee = %q, want the colleague's claim left alone", got.AssigneeID)
	}
}

// A protocol error after the assign landed must not leave the ticket
// claimed: an assigned ticket is never eligible, so it would sit in its
// queue invisible to every later pass, with no error anywhere saying so.
// The read-back is the failure Linear actually hands out here — the assign
// succeeded, the confirming GetIssue did not.
func TestClaimForQueueReleasesTheClaimWhenTheReadBackFails(t *testing.T) {
	ctx := context.Background()
	errBoom := errors.New("temporary Linear failure")
	f := linear.NewFake()
	f.SetViewer("me")
	f.AddIssue("LERP", linear.Issue{ID: "iss-1", Identifier: "LERP-1", Status: "Todo"})

	// Only the read-back fails; the verify inside releaseClaim gets through,
	// which is what a transient failure looks like.
	client := &failOnceGetIssue{Client: f, err: errBoom}
	viewerID, won, err := claimForQueue(ctx, client, "iss-1", "Todo")
	if won {
		t.Error("claimForQueue won = true, want false")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("claimForQueue error = %v, want wrapped %v", err, errBoom)
	}
	if viewerID != "me" {
		t.Errorf("viewerID = %q, want the operating user for later claim bookkeeping", viewerID)
	}
	got, getErr := f.GetIssue(ctx, "iss-1")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got.AssigneeID != "" {
		t.Errorf("assignee = %q, want the claim released so the ticket stays eligible", got.AssigneeID)
	}
	if got.Status != "Todo" {
		t.Errorf("status = %q, want the ticket left in its queue", got.Status)
	}
}

// When the release cannot be made to stick either, the protocol error still
// comes back — carrying the release failure — rather than being swallowed
// into a silent success. The ticket really is stranded at that point, and the
// error is the only thing that says so.
func TestClaimForQueueReportsAReleaseItCouldNotMake(t *testing.T) {
	ctx := context.Background()
	errBoom := errors.New("Linear is down")
	f := linear.NewFake()
	f.SetViewer("me")
	f.AddIssue("LERP", linear.Issue{ID: "iss-1", Identifier: "LERP-1", Status: "Todo"})

	client := brokenGetIssue{Client: f, err: errBoom}
	_, won, err := claimForQueue(ctx, client, "iss-1", "Todo")
	if won {
		t.Error("claimForQueue won = true, want false")
	}
	if !errors.Is(err, errBoom) {
		t.Fatalf("claimForQueue error = %v, want wrapped %v", err, errBoom)
	}
	if !strings.Contains(err.Error(), "verify claim before release") {
		t.Errorf("claimForQueue error = %v, want it to name the release it could not make", err)
	}
	if got, _ := f.GetIssue(ctx, "iss-1"); got.AssigneeID != "me" {
		t.Errorf("assignee = %q, want the claim still stuck on the ticket the error reports", got.AssigneeID)
	}
}

// The other half of the moved-ticket release: when the unassign itself fails,
// the ticket is left claimed in a status no queue serves, and the caller has
// to hear about it.
func TestClaimForQueueReportsAFailedReleaseOfAMovedTicket(t *testing.T) {
	ctx := context.Background()
	errBoom := errors.New("unassign refused")
	f := linear.NewFake()
	f.SetViewer("me")
	f.AddIssue("LERP", linear.Issue{ID: "iss-1", Identifier: "LERP-1", Status: "Todo"})

	moved := movedOnAssign{Client: f, move: func(issueID string) {
		if _, err := f.MoveIssue(ctx, issueID, "Escalated"); err != nil {
			t.Error(err)
		}
	}}
	_, won, err := claimForQueue(ctx, brokenUnassign{Client: moved, err: errBoom}, "iss-1", "Todo")
	if won {
		t.Error("claimForQueue won = true, want false")
	}
	// Fatal, not Errorf: the message check below reads err, and the very
	// regression this test names is the one that makes err nil.
	if !errors.Is(err, errBoom) {
		t.Fatalf("claimForQueue error = %v, want wrapped %v", err, errBoom)
	}
	if !strings.Contains(err.Error(), "release moved issue") {
		t.Errorf("claimForQueue error = %v, want it to name the moved ticket it could not release", err)
	}
	if got, _ := f.GetIssue(ctx, "iss-1"); got.AssigneeID != "me" {
		t.Errorf("assignee = %q, want the claim still on the ticket the error reports", got.AssigneeID)
	}
}

// The unassign half of the same property: a release that was attempted, and
// failed, must say so. Only the read-back fails here, so releaseClaim gets
// past its verify and reaches the unassign it cannot make.
func TestClaimForQueueReportsAnUnassignThatFailed(t *testing.T) {
	ctx := context.Background()
	errRead := errors.New("temporary Linear failure")
	errUnassign := errors.New("unassign refused")
	f := linear.NewFake()
	f.SetViewer("me")
	f.AddIssue("LERP", linear.Issue{ID: "iss-1", Identifier: "LERP-1", Status: "Todo"})

	client := brokenUnassign{
		Client: &failOnceGetIssue{Client: f, err: errRead},
		err:    errUnassign,
	}
	_, won, err := claimForQueue(ctx, client, "iss-1", "Todo")
	if won {
		t.Error("claimForQueue won = true, want false")
	}
	if err == nil {
		t.Fatal("claimForQueue error = nil, want the read-back failure and the release it could not make")
	}
	if !errors.Is(err, errRead) {
		t.Errorf("claimForQueue error = %v, want wrapped %v", err, errRead)
	}
	if !strings.Contains(err.Error(), "release claim") {
		t.Errorf("claimForQueue error = %v, want it to report the unassign that failed", err)
	}
	if got, _ := f.GetIssue(ctx, "iss-1"); got.AssigneeID != "me" {
		t.Errorf("assignee = %q, want the claim still on the ticket the error reports", got.AssigneeID)
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

// stolenAfterReadBack hands back the read-back the claim protocol expects,
// then lets a colleague overwrite the assignment before anything reads the
// issue again — the window releaseClaim's verify exists for.
type stolenAfterReadBack struct {
	linear.Client
	fake    *linear.Fake
	rivalID string
	stolen  bool
}

func (c *stolenAfterReadBack) GetIssue(ctx context.Context, issueID string) (linear.Issue, error) {
	issue, err := c.Client.GetIssue(ctx, issueID)
	if err != nil || c.stolen {
		return issue, err
	}
	c.stolen = true
	if assignErr := c.fake.AssignIssue(ctx, issueID, c.rivalID); assignErr != nil {
		return issue, assignErr
	}
	return issue, nil
}

type failingClient struct {
	linear.Client
	viewerErr error
}

func (c failingClient) Viewer(context.Context) (string, error) { return "", c.viewerErr }

// failOnceGetIssue fails the first GetIssue and serves every later one from
// the board, standing in for a transient read failure mid-protocol.
type failOnceGetIssue struct {
	linear.Client
	err    error
	failed bool
}

func (c *failOnceGetIssue) GetIssue(ctx context.Context, issueID string) (linear.Issue, error) {
	if !c.failed {
		c.failed = true
		return linear.Issue{}, c.err
	}
	return c.Client.GetIssue(ctx, issueID)
}

// brokenGetIssue fails every GetIssue, so the claim's read-back and the
// release that answers it both fail.
type brokenGetIssue struct {
	linear.Client
	err error
}

func (c brokenGetIssue) GetIssue(context.Context, string) (linear.Issue, error) {
	return linear.Issue{}, c.err
}

// brokenUnassign is a Client that can take a claim but never give one back.
type brokenUnassign struct {
	linear.Client
	err error
}

func (c brokenUnassign) UnassignIssue(context.Context, string) error { return c.err }
