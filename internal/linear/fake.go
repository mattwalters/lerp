package linear

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"
)

// Fake is an in-memory Client for driving loop tests. State is plain
// mutable data behind a mutex: issues with status, assignee, and
// blockers. It is not safe for anything but tests.
type Fake struct {
	mu       sync.Mutex
	viewerID string
	issues   map[string]*fakeIssue
	comments map[string][]Comment
	// categories is the one record of which statuses are finished: a status
	// counts as complete exactly when Linear's category for it does, which
	// is how a real board decides it too. Keeping a second set beside this
	// one let the fake's listings and its state categories disagree about
	// which statuses are done — a disagreement the real API cannot produce,
	// and one that would show up as a ticket the delta evicts and the next
	// full re-list resurrects.
	categories  map[string]string
	teamStates  map[string][]string
	automations map[string][]GitAutomation
	// clock is what stamps UpdatedAt, advancing a fixed step per write so
	// that "later" is decidable in a test without sleeping and without a
	// real clock's resolution deciding it.
	clock time.Time
}

type fakeIssue struct {
	issue    Issue
	team     string
	body     string   // the description GetIssueDetail reads
	blockers []string // issue IDs blocking this issue
}

var _ Client = (*Fake)(nil)

// NewFake returns an empty fake whose viewer is "fake-viewer" and whose
// "Backlog", "Triage", "Done" and "Canceled" statuses carry Linear's
// categories of those names — the stock board every Linear team starts
// with, on which the last two are what counts as finished.
func NewFake() *Fake {
	return &Fake{
		viewerID: "fake-viewer",
		issues:   map[string]*fakeIssue{},
		comments: map[string][]Comment{},
		categories: map[string]string{
			"Backlog":  CategoryBacklog,
			"Triage":   CategoryTriage,
			"Done":     CategoryCompleted,
			"Canceled": CategoryCanceled,
		},
		teamStates:  map[string][]string{},
		automations: map[string][]GitAutomation{},
		clock:       fakeEpoch,
	}
}

// fakeEpoch is where the fake's clock starts — a fixed instant, so the
// timestamps a failing test prints are the same ones every time.
var fakeEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// tick advances the fake's clock and returns the new time: the stamp for one
// write. Callers hold f.mu.
func (f *Fake) tick() time.Time {
	f.clock = f.clock.Add(time.Second)
	return f.clock
}

// Advance moves the fake's clock forward, so the next write is stamped that
// much later than the last. It is how a test opens a gap wider than a
// reader's own tolerances — a delta that re-reads a trailing window, say —
// without waiting for one in real time.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(d)
}

// done reports whether a status counts as finished — the fake's whole
// definition of it, read off the status's Linear category. Callers hold f.mu.
func (f *Fake) done(status string) bool {
	c := f.categories[status]
	return c == CategoryCompleted || c == CategoryCanceled
}

// touch stamps an issue as changed now, the way any write to a Linear issue
// moves its updatedAt. It is what makes the fake usable for delta reads at
// all: a fake whose mutations left updatedAt alone would report a board on
// which nothing ever happened. Callers hold f.mu.
func (f *Fake) touch(fi *fakeIssue) {
	fi.issue.UpdatedAt = f.tick()
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
// declared with Block and computed from blocker statuses. An issue added
// without an UpdatedAt is stamped with the fake's clock; supplying one sets
// where the board's clock stands if it is later than the clock is now, so
// that no later write to the issue can be stamped before it.
func (f *Fake) AddIssue(teamKey string, is Issue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	is.Blocked = false
	is.BlockedBy = nil
	is.Blocks = nil
	if is.UpdatedAt.IsZero() {
		is.UpdatedAt = f.tick()
	} else if is.UpdatedAt.After(f.clock) {
		// A stamp the caller chose carries the clock with it. Otherwise the
		// next write to this issue would stamp it earlier than it is now —
		// time running backwards on one issue, which no real board does, and
		// which a delta reader would answer by never reporting it again.
		f.clock = is.UpdatedAt
	}
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
		if !ok || f.done(b.issue.Status) {
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
		if f.done(other.issue.Status) {
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
// the user, in any status, minus the ones whose status Linear files as
// finished (the real client filters completed and canceled state types
// server-side).
func (f *Fake) ListAssignedIssues(_ context.Context, teamKey, assigneeID string) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var issues []Issue
	for _, fi := range f.issues {
		if fi.team == teamKey && fi.issue.AssigneeID == assigneeID && !f.done(fi.issue.Status) {
			issues = append(issues, f.view(fi))
		}
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Identifier < issues[j].Identifier })
	return issues, nil
}

// ListUnassignedIssues mirrors the real query: the team's issues with no
// assignee, minus the ones whose status Linear files as finished.
func (f *Fake) ListUnassignedIssues(_ context.Context, teamKey string) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var issues []Issue
	for _, fi := range f.issues {
		if fi.team == teamKey && fi.issue.AssigneeID == "" && !f.done(fi.issue.Status) {
			issues = append(issues, f.view(fi))
		}
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Identifier < issues[j].Identifier })
	return issues, nil
}

// ListTeamIssuesUpdatedSince mirrors the real delta read: every issue of the
// team stamped at or after since, in any status and with any assignee. The
// bound is inclusive, as the real query's gte is.
func (f *Fake) ListTeamIssuesUpdatedSince(_ context.Context, teamKey string, since time.Time) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var issues []Issue
	for _, fi := range f.issues {
		if fi.team == teamKey && !fi.issue.UpdatedAt.Before(since) {
			issues = append(issues, f.view(fi))
		}
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Identifier < issues[j].Identifier })
	return issues, nil
}

// DropIssue removes an issue from the fake board without a trace, the way
// archiving or deleting one in Linear does: no listing mentions it again and
// no delta reports it, because a delta reports changes to issues that still
// exist. It is how a test provokes the drift only a full re-list can heal.
func (f *Fake) DropIssue(issueID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.issues, issueID)
	delete(f.comments, issueID)
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

// MoveIssue mirrors the real client's refusal of a status name absent from
// the issue's team — but only once a test has declared that team's states
// with SetTeamStates. A team nothing has declared accepts any name, so the
// many tests that move issues through ad hoc statuses without modeling a
// whole board keep working unchanged.
func (f *Fake) MoveIssue(_ context.Context, issueID, statusName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, ok := f.issues[issueID]
	if !ok {
		return fmt.Errorf("move issue %s: %w", issueID, ErrNotFound)
	}
	if states, declared := f.teamStates[fi.team]; declared && !slices.Contains(states, statusName) {
		return fmt.Errorf("move issue %s: no state named %q on its team", issueID, statusName)
	}
	fi.issue.Status = statusName
	f.touch(fi)
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
	f.touch(fi)
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
	f.touch(fi)
	return nil
}

func (f *Fake) CommentOnIssue(_ context.Context, issueID, bodyMarkdown string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, ok := f.issues[issueID]
	if !ok {
		return fmt.Errorf("comment on issue %s: %w", issueID, ErrNotFound)
	}
	f.comments[issueID] = append(f.comments[issueID],
		Comment{Author: f.viewerID, Body: bodyMarkdown, CreatedAt: time.Now()})
	f.touch(fi)
	return nil
}

func (f *Fake) Viewer(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.viewerID, nil
}
