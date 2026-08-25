package linear

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Fake is an in-memory Client for driving loop tests. State is plain
// mutable data behind a mutex: issues with status, assignee, and
// blockers. It is not safe for anything but tests.
type Fake struct {
	mu           sync.Mutex
	viewerID     string
	issues       map[string]*fakeIssue
	comments     map[string][]Comment
	doneStatuses map[string]bool
	categories   map[string]string
	teamStates   map[string][]string
	automations  map[string][]GitAutomation
}

type fakeIssue struct {
	issue    Issue
	team     string
	body     string   // the description GetIssueDetail reads
	blockers []string // issue IDs blocking this issue
}

var _ Client = (*Fake)(nil)

// NewFake returns an empty fake whose viewer is "fake-viewer", whose
// completed statuses are "Done" and "Canceled", and whose "Backlog" and
// "Triage" statuses carry Linear's categories of those names — the stock
// board every Linear team starts with.
func NewFake() *Fake {
	return &Fake{
		viewerID:     "fake-viewer",
		issues:       map[string]*fakeIssue{},
		comments:     map[string][]Comment{},
		doneStatuses: map[string]bool{"Done": true, "Canceled": true},
		categories:   map[string]string{"Backlog": CategoryBacklog, "Triage": CategoryTriage},
		teamStates:   map[string][]string{},
		automations:  map[string][]GitAutomation{},
	}
}

// SetStatusCategory declares Linear's state category for status names, the
// way a real board does: the category is a property of the status, so it
// follows a ticket moved into one rather than being set per issue.
func (f *Fake) SetStatusCategory(category string, statuses ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range statuses {
		f.categories[s] = category
	}
}

// AddIssue puts an issue on the fake board under the given team key.
// The Blocked, BlockedBy and Blocks fields of is are ignored; blocking is
// declared with Block and computed from blocker statuses.
func (f *Fake) AddIssue(teamKey string, is Issue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	is.Blocked = false
	is.BlockedBy = nil
	is.Blocks = nil
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

// SetTeamStates declares the workflow state names TeamStates reports
// for a team, in board order.
func (f *Fake) SetTeamStates(teamKey string, names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teamStates[teamKey] = append([]string(nil), names...)
}

// TeamStates mirrors the real client: the declared state names for the
// team, or ErrNotFound for a team never declared.
func (f *Fake) TeamStates(_ context.Context, teamKey string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	names, ok := f.teamStates[teamKey]
	if !ok {
		return nil, fmt.Errorf("team states: team %q: %w", teamKey, ErrNotFound)
	}
	return append([]string(nil), names...), nil
}

// SetGitAutomations declares the git automations TeamGitAutomations reports
// for a team, the way a team's git settings hold them.
func (f *Fake) SetGitAutomations(teamKey string, automations ...GitAutomation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.automations[teamKey] = append([]GitAutomation(nil), automations...)
}

// TeamGitAutomations mirrors the real client for the declared automations. A
// team with none declared reports none rather than an error: a team whose git
// settings are untouched is the ordinary case, not a missing team.
func (f *Fake) TeamGitAutomations(_ context.Context, teamKey string) ([]GitAutomation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]GitAutomation(nil), f.automations[teamKey]...), nil
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

// Comments returns the bodies of the comments posted on an issue, oldest
// first — what the loop tests assert on. GetIssueDetail is the reading
// counterpart, with authors and times.
func (f *Fake) Comments(issueID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	bodies := make([]string, 0, len(f.comments[issueID]))
	for _, c := range f.comments[issueID] {
		bodies = append(bodies, c.Body)
	}
	return bodies
}

// SetDescription sets the body GetIssueDetail reads back for an issue.
func (f *Fake) SetDescription(issueID, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fi, ok := f.issues[issueID]; ok {
		fi.body = body
	}
}

// view materializes the caller-visible Issue, computing Blocked from the
// current statuses of the declared blockers and Blocks by reading the same
// declarations backwards, and reading the state category off the status the
// issue is in now. Callers hold f.mu.
func (f *Fake) view(fi *fakeIssue) Issue {
	is := fi.issue
	is.StatusType = f.categories[is.Status]
	is.Blocked = false
	is.BlockedBy = nil
	is.Blocks = nil
	for _, id := range fi.blockers {
		b, ok := f.issues[id]
		if !ok || f.doneStatuses[b.issue.Status] {
			continue
		}
		is.BlockedBy = append(is.BlockedBy, b.issue.Identifier)
	}
	is.Blocked = len(is.BlockedBy) > 0
	// The real client reads the forward relation off the issue itself; here
	// the only record of it is the other issue's blockers list. A finished
	// issue is held up by nothing, so it drops out — the mirror of the
	// blocker rule above.
	for _, other := range f.issues {
		if f.doneStatuses[other.issue.Status] {
			continue
		}
		for _, id := range other.blockers {
			if id == fi.issue.ID {
				is.Blocks = append(is.Blocks, other.issue.Identifier)
				break
			}
		}
	}
	sort.Strings(is.Blocks)
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

// ListUnassignedIssues mirrors the real query: the team's issues with no
// assignee, minus the ones whose status counts as complete.
func (f *Fake) ListUnassignedIssues(_ context.Context, teamKey string) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var issues []Issue
	for _, fi := range f.issues {
		if fi.team == teamKey && fi.issue.AssigneeID == "" && !f.doneStatuses[fi.issue.Status] {
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

// GetIssueDetail mirrors the real read: the issue's body and its comments,
// oldest first.
func (f *Fake) GetIssueDetail(_ context.Context, issueID string) (IssueDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, ok := f.issues[issueID]
	if !ok {
		return IssueDetail{}, fmt.Errorf("get issue detail %s: %w", issueID, ErrNotFound)
	}
	return IssueDetail{Body: fi.body, Comments: append([]Comment(nil), f.comments[issueID]...)}, nil
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
	f.comments[issueID] = append(f.comments[issueID],
		Comment{Author: f.viewerID, Body: bodyMarkdown, CreatedAt: time.Now()})
	return nil
}

func (f *Fake) Viewer(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.viewerID, nil
}
