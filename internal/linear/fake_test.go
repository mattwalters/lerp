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
