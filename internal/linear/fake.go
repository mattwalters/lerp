package linear

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Fake is an in-memory Client for driving loop tests. State is plain
// mutable data behind a mutex: issues with status, assignee, and
// blockers. It is not safe for anything but tests.
type Fake struct {
	mu           sync.Mutex
	viewerID     string
	issues       map[string]*fakeIssue
	comments     map[string][]string
	doneStatuses map[string]bool
}

type fakeIssue struct {
	issue    Issue
	team     string
	blockers []string // issue IDs blocking this issue
}

var _ Client = (*Fake)(nil)

// NewFake returns an empty fake whose viewer is "fake-viewer" and whose
// completed statuses are "Done" and "Canceled".
func NewFake() *Fake {
	return &Fake{
		viewerID:     "fake-viewer",
		issues:       map[string]*fakeIssue{},
		comments:     map[string][]string{},
		doneStatuses: map[string]bool{"Done": true, "Canceled": true},
	}
}

// AddIssue puts an issue on the fake board under the given team key.
// The Blocked and BlockedBy fields of is are ignored; blocking is
// declared with Block and computed from blocker statuses.
func (f *Fake) AddIssue(teamKey string, is Issue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	is.Blocked = false
	is.BlockedBy = nil
	f.issues[is.ID] = &fakeIssue{issue: is, team: teamKey}
}

// Block declares that blockerID blocks issueID.
func (f *Fake) Block(issueID, blockerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fi, ok := f.issues[issueID]; ok {
		fi.blockers = append(fi.blockers, blockerID)
	}
}

// SetViewer sets the id Viewer returns.
func (f *Fake) SetViewer(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.viewerID = id
}

// SetDoneStatuses replaces the set of status names that count as
// complete when deciding whether a blocker still blocks.
func (f *Fake) SetDoneStatuses(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.doneStatuses = map[string]bool{}
	for _, n := range names {
		f.doneStatuses[n] = true
	}
}

// Comments returns the comments posted on an issue, oldest first.
func (f *Fake) Comments(issueID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.comments[issueID]...)
}

// view materializes the caller-visible Issue, computing Blocked from the
// current statuses of the declared blockers. Callers hold f.mu.
func (f *Fake) view(fi *fakeIssue) Issue {
	is := fi.issue
	is.Blocked = false
	is.BlockedBy = nil
	for _, id := range fi.blockers {
		b, ok := f.issues[id]
		if !ok || f.doneStatuses[b.issue.Status] {
			continue
		}
		is.BlockedBy = append(is.BlockedBy, b.issue.Identifier)
	}
	is.Blocked = len(is.BlockedBy) > 0
	return is
}

func (f *Fake) ListIssues(_ context.Context, teamKey, statusName string) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var issues []Issue
	for _, fi := range f.issues {
		if fi.team == teamKey && fi.issue.Status == statusName {
			issues = append(issues, f.view(fi))
		}
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Identifier < issues[j].Identifier })
	return issues, nil
}

// ListAssignedIssues mirrors the real query: the team's issues assigned to
// the user, in any status, minus the ones whose status counts as complete
// (the real client filters completed and canceled state types server-side).
func (f *Fake) ListAssignedIssues(_ context.Context, teamKey, assigneeID string) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var issues []Issue
	for _, fi := range f.issues {
		if fi.team == teamKey && fi.issue.AssigneeID == assigneeID && !f.doneStatuses[fi.issue.Status] {
			issues = append(issues, f.view(fi))
		}
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Identifier < issues[j].Identifier })
	return issues, nil
}

func (f *Fake) GetIssue(_ context.Context, issueID string) (Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, ok := f.issues[issueID]
	if !ok {
		return Issue{}, fmt.Errorf("get issue %s: %w", issueID, ErrNotFound)
	}
	return f.view(fi), nil
}

func (f *Fake) MoveIssue(_ context.Context, issueID, statusName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, ok := f.issues[issueID]
	if !ok {
		return fmt.Errorf("move issue %s: %w", issueID, ErrNotFound)
	}
	fi.issue.Status = statusName
	return nil
}

func (f *Fake) AssignIssue(_ context.Context, issueID, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, ok := f.issues[issueID]
	if !ok {
		return fmt.Errorf("assign issue %s: %w", issueID, ErrNotFound)
	}
	fi.issue.AssigneeID = userID
	return nil
}

func (f *Fake) UnassignIssue(_ context.Context, issueID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, ok := f.issues[issueID]
	if !ok {
		return fmt.Errorf("unassign issue %s: %w", issueID, ErrNotFound)
	}
	fi.issue.AssigneeID = ""
	return nil
}

func (f *Fake) CommentOnIssue(_ context.Context, issueID, bodyMarkdown string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.issues[issueID]; !ok {
		return fmt.Errorf("comment on issue %s: %w", issueID, ErrNotFound)
	}
	f.comments[issueID] = append(f.comments[issueID], bodyMarkdown)
	return nil
}

func (f *Fake) Viewer(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.viewerID, nil
}
