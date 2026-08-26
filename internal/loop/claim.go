//go:build unix

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
// error when Linear could not complete the protocol. The read-back issue and
// the operating user's ID are returned so callers need not fetch them again;
// viewerID is filled on every path that got far enough to know it.
func Claim(ctx context.Context, client linear.Client, issueID string) (issue linear.Issue, viewerID string, won bool, err error) {
	viewerID, err = client.Viewer(ctx)
	if err != nil {
		return issue, "", false, fmt.Errorf("claim issue %s: get viewer: %w", issueID, err)
	}
	if err := client.AssignIssue(ctx, issueID, viewerID); err != nil {
		return issue, viewerID, false, fmt.Errorf("claim issue %s: assign: %w", issueID, err)
	}

	timer := time.NewTimer(claimSettleDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return issue, viewerID, false, fmt.Errorf("claim issue %s: settle: %w", issueID, ctx.Err())
	case <-timer.C:
	}

	issue, err = client.GetIssue(ctx, issueID)
	if err != nil {
		return issue, viewerID, false, fmt.Errorf("claim issue %s: read back: %w", issueID, err)
	}
	return issue, viewerID, issue.AssigneeID == viewerID, nil
}

// claimForQueue runs the claim protocol for a ticket sitting in a queue's
// status, then confirms the ticket is still there: a move may have raced the
// claim, and a ticket that left the queue must not be provisioned or run.
// When the ticket has moved, the claim is released — an assigned ticket is
// never eligible, so keeping it would strand the ticket wherever it now sits
// until a human intervenes. A protocol error after the assign is released the
// same way, best-effort: an error must not leave the ticket claimed, and
// therefore invisible to every later pass, either.
//
// The returned viewerID identifies the operating user for later claim
// bookkeeping, whether or not the claim was won.
func claimForQueue(ctx context.Context, client linear.Client, issueID, status string) (viewerID string, won bool, err error) {
	claimed, viewerID, won, err := Claim(ctx, client, issueID)
	if err != nil {
		if viewerID != "" {
			if releaseErr := releaseClaim(ctx, client, issueID, viewerID); releaseErr != nil {
				err = fmt.Errorf("%w (%v)", err, releaseErr)
			}
		}
		return viewerID, false, err
	}
	if !won {
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

// releaseClaim unassigns an issue so it becomes eligible again — for a run
// this process claimed but never started, and for a ticket promote moved
// back into a queue. The claim is verified first: if someone else holds the
// issue now, their claim is left alone.
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
