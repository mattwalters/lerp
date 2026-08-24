package linear

import (
	"context"
	"errors"
	"reflect"
	"testing"
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
	ctx := context.Background()
	if err := f.CommentOnIssue(ctx, "iss-1", "first"); err != nil {
		t.Fatalf("CommentOnIssue: %v", err)
	}
	if err := f.CommentOnIssue(ctx, "iss-1", "second"); err != nil {
		t.Fatalf("CommentOnIssue: %v", err)
	}
	if got, want := f.Comments("iss-1"), []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Comments = %v, want %v", got, want)
	}
}

// What CommentOnIssue writes, GetIssueDetail reads back — the fake\'s half
// of the pane\'s promise that a parked ticket shows the verdict that parked
// it.
func TestFakeIssueDetail(t *testing.T) {
	f := newTestFake()
	ctx := context.Background()
	f.SetDescription("iss-1", "the body")
	if err := f.CommentOnIssue(ctx, "iss-1", "the verdict"); err != nil {
		t.Fatalf("CommentOnIssue: %v", err)
	}
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

func TestFakeNotFound(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	ops := map[string]func() error{
		"GetIssue":       func() error { _, err := f.GetIssue(ctx, "nope"); return err },
		"MoveIssue":      func() error { return f.MoveIssue(ctx, "nope", "Todo") },
		"AssignIssue":    func() error { return f.AssignIssue(ctx, "nope", "u") },
		"UnassignIssue":  func() error { return f.UnassignIssue(ctx, "nope") },
		"CommentOnIssue": func() error { return f.CommentOnIssue(ctx, "nope", "hi") },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s err = %v, want ErrNotFound", name, err)
		}
	}
}
