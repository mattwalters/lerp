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

// claimForQueue runs the claim protocol for a ticket sitting in a queue's
// status, then confirms the ticket is still there: a move may have raced the
// claim, and a ticket that left the queue must not be provisioned or run.
// When the ticket has moved, the claim is released — an assigned ticket is
// never eligible, so keeping it would strand the ticket wherever it now sits
// until a human intervenes.
//
// The returned viewerID identifies the operating user for later claim
// bookkeeping, whether or not the claim was won.
func claimForQueue(ctx context.Context, client linear.Client, issueID, status string) (viewerID string, won bool, err error) {
	won, err = Claim(ctx, client, issueID)
	if err != nil || !won {
		return "", false, err
	}
	claimed, err := client.GetIssue(ctx, issueID)
	if err != nil {
		return "", false, fmt.Errorf("read claimed issue %s: %w", issueID, err)
	}
	viewerID, err = client.Viewer(ctx)
	if err != nil {
		return "", false, fmt.Errorf("read claimed viewer: %w", err)
	}
	if claimed.AssigneeID != viewerID {
		// Someone else owns it now. Leave their claim alone.
		return viewerID, false, nil
	}
	if claimed.Status != status {
		if err := client.UnassignIssue(ctx, issueID); err != nil {
			return viewerID, false, fmt.Errorf("release moved issue %s: %w", issueID, err)
		}
		return viewerID, false, nil
	}
	return viewerID, true, nil
}

// releaseClaim unassigns an issue this process claimed but never ran, so the
// queued ticket remains eligible for a later attempt. The claim is verified
// first: if someone else holds the issue now, their claim is left alone.
func releaseClaim(ctx context.Context, client linear.Client, issueID, viewerID string) error {
	current, err := client.GetIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("verify claim before release: %w", err)
	}
	if current.AssigneeID != viewerID {
		return nil
	}
	if err := client.UnassignIssue(ctx, issueID); err != nil {
		return fmt.Errorf("release claim: %w", err)
	}
	return nil
}
