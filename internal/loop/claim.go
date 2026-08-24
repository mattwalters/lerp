// Package loop contains lerp's reconciler.
package loop

import (
	"context"
	"fmt"
	"time"

	"github.com/mattwalters/lerp/internal/linear"
)

const claimSettleDelay = 100 * time.Millisecond

// Eligible reports whether issue can be picked up by a queue. An eligible
// issue sits in one of queueStatuses, has no assignee, and has no unfinished
// blocker.
func Eligible(issue linear.Issue, queueStatuses map[string]bool) bool {
	return queueStatuses[issue.Status] && issue.AssigneeID == "" && !issue.Blocked
}

// Claim assigns issue to the operating Linear user, waits briefly for the
// assignment to settle, then reads it back. It reports won when the assignee
// remains the operating user, lost when another user owns the issue, and an
// error when Linear could not complete the protocol.
func Claim(ctx context.Context, client linear.Client, issueID string) (won bool, err error) {
	viewerID, err := client.Viewer(ctx)
	if err != nil {
		return false, fmt.Errorf("claim issue %s: get viewer: %w", issueID, err)
	}
	if err := client.AssignIssue(ctx, issueID, viewerID); err != nil {
		return false, fmt.Errorf("claim issue %s: assign: %w", issueID, err)
	}

	timer := time.NewTimer(claimSettleDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, fmt.Errorf("claim issue %s: settle: %w", issueID, ctx.Err())
	case <-timer.C:
	}

	issue, err := client.GetIssue(ctx, issueID)
	if err != nil {
		return false, fmt.Errorf("claim issue %s: read back: %w", issueID, err)
	}
	return issue.AssigneeID == viewerID, nil
}
