package linear

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func newTestFake() *Fake {
	f := NewFake()
	f.AddIssue("LERP", Issue{ID: "iss-1", Identifier: "LERP-1", Title: "First", Status: "Todo"})
	f.AddIssue("LERP", Issue{ID: "iss-2", Identifier: "LERP-2", Title: "Second", Status: "Todo"})
	f.AddIssue("LERP", Issue{ID: "iss-3", Identifier: "LERP-3", Title: "Third", Status: "In Progress"})
	f.AddIssue("PROSE", Issue{ID: "iss-4", Identifier: "PROSE-1", Title: "Other team", Status: "Todo"})
	return f
}

func TestFakeListIssues(t *testing.T) {
	f := newTestFake()
	issues, err := f.ListIssues(context.Background(), "LERP", "Todo")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	var ids []string
	for _, is := range issues {
		ids = append(ids, is.Identifier)
	}
	if want := []string{"LERP-1", "LERP-2"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("identifiers = %v, want %v", ids, want)
	}
}

func TestFakeListAssignedIssues(t *testing.T) {
	f := newTestFake()
	ctx := context.Background()
	for _, id := range []string{"iss-1", "iss-3", "iss-4"} {
		if err := f.AssignIssue(ctx, id, "user-9"); err != nil {
			t.Fatalf("AssignIssue(%s): %v", id, err)
		}
	}
	// A finished assigned issue waits on nobody, exactly as the real
	// query's state-type filter behaves.
	if err := f.MoveIssue(ctx, "iss-3", "Done"); err != nil {
		t.Fatalf("MoveIssue: %v", err)
	}

	issues, err := f.ListAssignedIssues(ctx, "LERP", "user-9")
	if err != nil {
		t.Fatalf("ListAssignedIssues: %v", err)
	}
	var ids []string
	for _, is := range issues {
		ids = append(ids, is.Identifier)
	}
	if want := []string{"LERP-1"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("identifiers = %v, want %v", ids, want)
	}
}

func TestFakeListUnassignedIssues(t *testing.T) {
	f := newTestFake()
	ctx := context.Background()
	if err := f.AssignIssue(ctx, "iss-1", "user-9"); err != nil {
		t.Fatalf("AssignIssue: %v", err)
	}
	// An unassigned but finished issue waits on nobody, exactly as the real
	// query's state-type filter behaves.
	if err := f.MoveIssue(ctx, "iss-2", "Done"); err != nil {
		t.Fatalf("MoveIssue: %v", err)
	}

	issues, err := f.ListUnassignedIssues(ctx, "LERP")
	if err != nil {
		t.Fatalf("ListUnassignedIssues: %v", err)
	}
	var ids []string
	for _, is := range issues {
		ids = append(ids, is.Identifier)
	}
	if want := []string{"LERP-3"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("identifiers = %v, want %v", ids, want)
	}
}

func TestFakeBlocking(t *testing.T) {
	f := newTestFake()
	f.Block("iss-1", "iss-3") // LERP-3 (In Progress) blocks LERP-1

	is, err := f.GetIssue(context.Background(), "iss-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if !is.Blocked || !reflect.DeepEqual(is.BlockedBy, []string{"LERP-3"}) {
		t.Errorf("issue = %+v, want blocked by LERP-3", is)
	}

	// The blocker sees the same relation from the other side.
	blocker, err := f.GetIssue(context.Background(), "iss-3")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if !reflect.DeepEqual(blocker.Blocks, []string{"LERP-1"}) {
		t.Errorf("blocker = %+v, want it blocking LERP-1", blocker)
	}

	// Completing the blocker unblocks the issue.
	if err := f.MoveIssue(context.Background(), "iss-3", "Done"); err != nil {
		t.Fatalf("MoveIssue: %v", err)
	}
	is, err = f.GetIssue(context.Background(), "iss-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if is.Blocked || is.BlockedBy != nil {
		t.Errorf("issue = %+v, want unblocked after blocker done", is)
	}
	// A finished issue is held up by nothing, so it drops off what its
	// blocker blocks — the forward half of the same rule.
	if err := f.MoveIssue(context.Background(), "iss-1", "Done"); err != nil {
		t.Fatalf("MoveIssue: %v", err)
	}
	blocker, err = f.GetIssue(context.Background(), "iss-3")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if blocker.Blocks != nil {
		t.Errorf("blocker = %+v, want it blocking nothing once LERP-1 is done", blocker)
	}
}

func TestFakeClaimProtocol(t *testing.T) {
	f := newTestFake()
	ctx := context.Background()

	me, err := f.Viewer(ctx)
	if err != nil {
		t.Fatalf("Viewer: %v", err)
	}
	if err := f.AssignIssue(ctx, "iss-1", me); err != nil {
		t.Fatalf("AssignIssue: %v", err)
	}
	is, err := f.GetIssue(ctx, "iss-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if is.AssigneeID != me {
		t.Errorf("read-back assignee = %q, want %q", is.AssigneeID, me)
	}

	if err := f.UnassignIssue(ctx, "iss-1"); err != nil {
		t.Fatalf("UnassignIssue: %v", err)
	}
	is, _ = f.GetIssue(ctx, "iss-1")
	if is.AssigneeID != "" {
		t.Errorf("assignee after unassign = %q, want empty", is.AssigneeID)
	}
}

func TestFakeMoveIssue(t *testing.T) {
	f := newTestFake()
	ctx := context.Background()
	if err := f.MoveIssue(ctx, "iss-1", "In Progress"); err != nil {
		t.Fatalf("MoveIssue: %v", err)
	}
	is, _ := f.GetIssue(ctx, "iss-1")
	if is.Status != "In Progress" {
		t.Errorf("status = %q, want In Progress", is.Status)
	}
	issues, _ := f.ListIssues(ctx, "LERP", "Todo")
	if len(issues) != 1 {
		t.Errorf("Todo issues after move = %d, want 1", len(issues))
	}
}

func TestFakeComments(t *testing.T) {
	f := newTestFake()
	f.AddComment("iss-1", "first")
	f.AddComment("iss-1", "second")
	if got, want := f.Comments("iss-1"), []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Comments = %v, want %v", got, want)
	}
}

// What AddComment writes, GetIssueDetail reads back — the fake's half
// of the pane's promise that a parked ticket shows the verdict that parked
// it.
func TestFakeIssueDetail(t *testing.T) {
	f := newTestFake()
	ctx := context.Background()
	f.SetDescription("iss-1", "the body")
	f.AddComment("iss-1", "the verdict")
	detail, err := f.GetIssueDetail(ctx, "iss-1")
	if err != nil {
		t.Fatalf("GetIssueDetail: %v", err)
	}
	if detail.Body != "the body" {
		t.Errorf("body = %q, want %q", detail.Body, "the body")
	}
	if len(detail.Comments) != 1 {
		t.Fatalf("comments = %+v, want one", detail.Comments)
	}
	c := detail.Comments[0]
	if c.Body != "the verdict" || c.Author != "fake-viewer" || c.CreatedAt.IsZero() {
		t.Errorf("comment = %+v, want the verdict from fake-viewer with a time", c)
	}
	if _, err := f.GetIssueDetail(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetIssueDetail(nope) = %v, want ErrNotFound", err)
	}
}

// SetViewer is how a test puts the fake's claim protocol in someone else's
// name; AddComment is the other reader of viewerID.
func TestFakeSetViewer(t *testing.T) {
	f := NewFake()
	f.AddIssue("LERP", Issue{ID: "iss-1", Identifier: "LERP-1", Title: "First", Status: "Todo"})
	f.SetViewer("someone-else")

	ctx := context.Background()
	id, err := f.Viewer(ctx)
	if err != nil {
		t.Fatalf("Viewer: %v", err)
	}
	if id != "someone-else" {
		t.Errorf("Viewer = %q, want someone-else", id)
	}
	f.AddComment("iss-1", "hi")
	detail, err := f.GetIssueDetail(ctx, "iss-1")
	if err != nil {
		t.Fatalf("GetIssueDetail: %v", err)
	}
	if len(detail.Comments) != 1 || detail.Comments[0].Author != "someone-else" {
		t.Errorf("comments = %+v, want one authored someone-else", detail.Comments)
	}
}

// SetTeamStates and TeamStates are the fake's mirror of the real client's
// startup read (loop.Verify) — declared per team, ErrNotFound for a team
// nothing has declared.
func TestFakeTeamStates(t *testing.T) {
	f := NewFake()
	f.SetTeamStates("LERP", "Backlog", "Todo", "Done")

	names, err := f.TeamStates(context.Background(), "LERP")
	if err != nil {
		t.Fatalf("TeamStates: %v", err)
	}
	want := []string{"Backlog", "Todo", "Done"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("TeamStates = %v, want %v", names, want)
	}

	if _, err := f.TeamStates(context.Background(), "NOPE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("TeamStates(undeclared) = %v, want ErrNotFound", err)
	}
}

// The refusal itself — MoveIssue mirroring the real client's rejection of a
// name the team does not have, the semantic drift LERP-90 exists to close —
// is covered by the "move to a status absent from the team" contract case,
// against both clients. What only the fake needs to prove is the opt-in: a
// team nothing has declared states for stays permissive, keyed on the
// issue's own team rather than on whether any team has declared anything —
// so the many tests that move issues through ad hoc statuses without
// modeling a whole board are unaffected even when another team in the same
// fake does declare states.
func TestFakeMoveIssueUndeclaredTeamStaysPermissive(t *testing.T) {
	f := newTestFake()
	f.SetTeamStates("LERP", "Todo", "In Progress", "Done")
	// iss-4 belongs to PROSE, which has declared nothing.
	if err := f.MoveIssue(context.Background(), "iss-4", "Whatever"); err != nil {
		t.Errorf("MoveIssue on an undeclared team: %v, want no error", err)
	}
}

// SetStatusCategory is how a test declares Linear's category for a status
// beyond NewFake's stock four — the property that drives filtering and
// blocked derivation everywhere else in this file.
func TestFakeSetStatusCategory(t *testing.T) {
	f := newTestFake()
	f.SetStatusCategory(CategoryCanceled, "Won't Fix")
	ctx := context.Background()

	if err := f.MoveIssue(ctx, "iss-1", "Won't Fix"); err != nil {
		t.Fatalf("MoveIssue: %v", err)
	}
	is, err := f.GetIssue(ctx, "iss-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if is.StatusType != CategoryCanceled {
		t.Errorf("StatusType = %q, want %q", is.StatusType, CategoryCanceled)
	}
	// A custom-declared canceled status waits on nobody, same as the stock one.
	if err := f.AssignIssue(ctx, "iss-1", "user-9"); err != nil {
		t.Fatalf("AssignIssue: %v", err)
	}
	issues, err := f.ListAssignedIssues(ctx, "LERP", "user-9")
	if err != nil {
		t.Fatalf("ListAssignedIssues: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("ListAssignedIssues = %+v, want none: a canceled issue waits on nobody", issues)
	}
}

// Every op's ErrNotFound on a nonexistent issue, against both clients, is
// the "issue not found" contract case in contract_test.go — this file no
// longer duplicates it.

// The delta read is the fake's stand-in for the real one, so it has to be
// unfiltered in the same way: whatever the team holds, whoever it belongs
// to and whatever state it is in, as long as it changed at or after the
// cursor.
func TestFakeListTeamIssuesUpdatedSince(t *testing.T) {
	f := newTestFake()
	ctx := context.Background()
	all, err := f.ListTeamIssuesUpdatedSince(ctx, "LERP", fakeEpoch)
	if err != nil {
		t.Fatalf("ListTeamIssuesUpdatedSince: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("from the epoch = %d issues, want the team's three", len(all))
	}
	cursor := all[len(all)-1].UpdatedAt
	for _, is := range all {
		if is.UpdatedAt.After(cursor) {
			cursor = is.UpdatedAt
		}
	}

	// Nothing has changed since, so a cursor past the newest row is empty.
	if got, err := f.ListTeamIssuesUpdatedSince(ctx, "LERP", cursor.Add(time.Second)); err != nil || len(got) != 0 {
		t.Fatalf("quiet board = %v, %v, want nothing", got, err)
	}
	// The bound is inclusive, matching the real query's gte.
	if got, err := f.ListTeamIssuesUpdatedSince(ctx, "LERP", cursor); err != nil || len(got) != 1 {
		t.Fatalf("at the boundary = %v, %v, want the one issue stamped there", got, err)
	}

	// Finishing a ticket is a change like any other, and it comes back —
	// which is the only way a caller holding a cached board learns to drop
	// it.
	if err := f.MoveIssue(ctx, "iss-1", "Done"); err != nil {
		t.Fatal(err)
	}
	got, err := f.ListTeamIssuesUpdatedSince(ctx, "LERP", cursor.Add(time.Second))
	if err != nil {
		t.Fatalf("ListTeamIssuesUpdatedSince: %v", err)
	}
	if len(got) != 1 || got[0].ID != "iss-1" || got[0].StatusType != CategoryCompleted {
		t.Errorf("after the move = %+v, want the finished LERP-1 with its completed category", got)
	}
}

// Every write to an issue moves its updatedAt, the way Linear does — a fake
// that left it alone would report a board on which nothing ever happened,
// and every delta read against it would come back empty.
func TestFakeMutationsBumpUpdatedAt(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		write func(*Fake) error
	}{
		{"move", func(f *Fake) error { return f.MoveIssue(ctx, "iss-1", "Done") }},
		{"assign", func(f *Fake) error { return f.AssignIssue(ctx, "iss-1", "user-9") }},
		{"unassign", func(f *Fake) error { return f.UnassignIssue(ctx, "iss-1") }},
		{"comment", func(f *Fake) error { f.AddComment("iss-1", "hello"); return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFake()
			before, err := f.GetIssue(ctx, "iss-1")
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.write(f); err != nil {
				t.Fatal(err)
			}
			after, err := f.GetIssue(ctx, "iss-1")
			if err != nil {
				t.Fatal(err)
			}
			if !after.UpdatedAt.After(before.UpdatedAt) {
				t.Errorf("UpdatedAt after %s = %v, want it past %v", tc.name, after.UpdatedAt, before.UpdatedAt)
			}
		})
	}
}

// A dropped issue is the archived or deleted one: no listing mentions it and
// no delta reports it, because a delta reports changes to issues that still
// exist. Only a full re-list can notice it is gone.
func TestFakeDropIssue(t *testing.T) {
	f := newTestFake()
	ctx := context.Background()
	f.DropIssue("iss-1")

	if _, err := f.GetIssue(ctx, "iss-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetIssue after DropIssue = %v, want ErrNotFound", err)
	}
	got, err := f.ListTeamIssuesUpdatedSince(ctx, "LERP", fakeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range got {
		if is.ID == "iss-1" {
			t.Errorf("the delta still reports the dropped issue: %+v", is)
		}
	}
}
