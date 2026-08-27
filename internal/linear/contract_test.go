package linear

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// contractCase is one scenario run against both the Fake and an
// httptest-backed HTTP client. Interface drift between the two is
// compile-checked; semantic drift — ErrNotFound, an unknown-status move,
// completed/canceled filtering, blocked derivation — was only ever
// documented in comments and asserted separately in fake_test.go and
// client_test.go, which lets the two suites quietly stop agreeing. Sharing
// one scenario and one expectation between both arms turns that drift into a
// single failing test.
//
// handler is written independently of Fake — it must never call into the
// Fake under test, or agreement between the two arms would be circular.
type contractCase struct {
	name     string
	seedFake func(f *Fake)
	handler  func(t *testing.T) http.HandlerFunc
	run      func(ctx context.Context, c Client) (any, error)

	// Exactly one of these three is set, in precedence order.
	wantErr         error  // checked with errors.Is
	wantErrContains string // checked with strings.Contains(err.Error(), ...)
	want            any    // checked with reflect.DeepEqual when err is nil
}

func TestContract(t *testing.T) {
	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("fake", func(t *testing.T) {
				f := NewFake()
				tc.seedFake(f)
				checkContract(t, f, tc)
			})
			t.Run("http", func(t *testing.T) {
				c := testClient(t, tc.handler(t))
				checkContract(t, c, tc)
			})
		})
	}
}

func checkContract(t *testing.T, c Client, tc contractCase) {
	t.Helper()
	got, err := tc.run(context.Background(), c)
	switch {
	case tc.wantErr != nil:
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("err = %v, want %v", err, tc.wantErr)
		}
	case tc.wantErrContains != "":
		if err == nil || !strings.Contains(err.Error(), tc.wantErrContains) {
			t.Errorf("err = %v, want it to mention %q", err, tc.wantErrContains)
		}
	default:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("got %#v, want %#v", got, tc.want)
		}
	}
}

func identifiers(issues []Issue) []string {
	ids := make([]string, len(issues))
	for i, is := range issues {
		ids[i] = is.Identifier
	}
	return ids
}

// blockingSnapshot is the shape both arms of the blocking-derivation case
// report, taken at two points in the scenario so the same result also
// proves the fake releases a block the way the real client does.
type blockingSnapshot struct {
	aBlockedBefore   bool
	aBlockedByBefore []string
	bBlocks          []string
	aBlockedAfter    bool
}

// notFoundOps is every Client method that takes an issue ID, run against one
// that does not exist. Both arms are seeded with nothing.
func notFoundOps() map[string]func(ctx context.Context, c Client) error {
	return map[string]func(ctx context.Context, c Client) error{
		"GetIssue":       func(ctx context.Context, c Client) error { _, err := c.GetIssue(ctx, "nope"); return err },
		"GetIssueDetail": func(ctx context.Context, c Client) error { _, err := c.GetIssueDetail(ctx, "nope"); return err },
		"MoveIssue":      func(ctx context.Context, c Client) error { return c.MoveIssue(ctx, "nope", "Todo") },
		"AssignIssue":    func(ctx context.Context, c Client) error { return c.AssignIssue(ctx, "nope", "u") },
		"UnassignIssue":  func(ctx context.Context, c Client) error { return c.UnassignIssue(ctx, "nope") },
		"CommentOnIssue": func(ctx context.Context, c Client) error { return c.CommentOnIssue(ctx, "nope", "hi") },
	}
}

// notFoundHandler answers every read this suite makes of a nonexistent issue
// the way Linear does: a null issue for the queries that ask for one by id,
// and the "entity not found" GraphQL error the mutations get back instead —
// the message c.do's error path recognizes and turns into ErrNotFound.
func notFoundHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		switch {
		case strings.Contains(req.Query, "GetIssue"),
			strings.Contains(req.Query, "IssueStates"),
			strings.Contains(req.Query, "IssueDetail"):
			writeData(t, w, `{"issue":null}`)
		default:
			fmt.Fprint(w, `{"errors":[{"message":"Entity not found: Issue - Could not find referenced Issue."}]}`)
		}
	}
}

// blockingHandler scripts the blocking-derivation scenario over GraphQL,
// independently of Fake: LERP-1 is blocked by LERP-2 (in progress) and
// LERP-3 (already done, so it does not count), until LERP-2 itself moves to
// Done — mirroring how a real board recomputes the relation on every read
// rather than caching it.
func blockingHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	var bDone atomic.Bool
	return func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		switch {
		case strings.Contains(req.Query, "query GetIssue"):
			switch req.Variables["id"] {
			case "iss-a":
				if bDone.Load() {
					writeData(t, w, `{"issue":{"id":"iss-a","identifier":"LERP-1","title":"","state":{"name":"Todo"},
						"inverseRelations":{"nodes":[]},"relations":{"nodes":[]}}}`)
				} else {
					writeData(t, w, `{"issue":{"id":"iss-a","identifier":"LERP-1","title":"","state":{"name":"Todo"},
						"inverseRelations":{"nodes":[
							{"type":"blocks","issue":{"identifier":"LERP-2","state":{"type":"started"}}},
							{"type":"blocks","issue":{"identifier":"LERP-3","state":{"type":"completed"}}}
						]},
						"relations":{"nodes":[]}}}`)
				}
			case "iss-b":
				writeData(t, w, `{"issue":{"id":"iss-b","identifier":"LERP-2","title":"","state":{"name":"In Progress"},
					"inverseRelations":{"nodes":[]},
					"relations":{"nodes":[
						{"type":"blocks","relatedIssue":{"identifier":"LERP-1","state":{"type":"unstarted"}}}
					]}}}`)
			default:
				t.Fatalf("unexpected GetIssue id %v", req.Variables["id"])
			}
		case strings.Contains(req.Query, "IssueStates"):
			writeData(t, w, `{"issue":{"team":{"states":{"nodes":[{"id":"st-done","name":"Done"}]}}}}`)
		case strings.Contains(req.Query, "MoveIssue"):
			bDone.Store(true)
			writeData(t, w, `{"issueUpdate":{"success":true}}`)
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}
}

func contractCases() []contractCase {
	var cases []contractCase

	for name, op := range notFoundOps() {
		cases = append(cases, contractCase{
			name:     "issue not found: " + name,
			seedFake: func(f *Fake) {},
			handler:  notFoundHandler,
			run: func(ctx context.Context, c Client) (any, error) {
				return nil, op(ctx, c)
			},
			wantErr: ErrNotFound,
		})
	}

	cases = append(cases, contractCase{
		name: "move to a status absent from the team",
		seedFake: func(f *Fake) {
			f.AddIssue("LERP", Issue{ID: "iss-1", Identifier: "LERP-1", Status: "Todo"})
			f.SetTeamStates("LERP", "Todo", "In Progress", "Done")
		},
		handler: func(t *testing.T) http.HandlerFunc {
			t.Helper()
			return func(w http.ResponseWriter, r *http.Request) {
				req := decodeRequest(t, r)
				if !strings.Contains(req.Query, "IssueStates") {
					t.Fatalf("unexpected query: %s", req.Query)
				}
				writeData(t, w, `{"issue":{"team":{"states":{"nodes":[
					{"id":"st-1","name":"Todo"},
					{"id":"st-2","name":"In Progress"},
					{"id":"st-3","name":"Done"}
				]}}}}`)
			}
		},
		run: func(ctx context.Context, c Client) (any, error) {
			return nil, c.MoveIssue(ctx, "iss-1", "Nonexistent")
		},
		wantErrContains: `no state named "Nonexistent" on its team`,
	})

	cases = append(cases, contractCase{
		name: "completed and canceled issues wait on nobody",
		seedFake: func(f *Fake) {
			ctx := context.Background()
			f.AddIssue("LERP", Issue{ID: "iss-1", Identifier: "LERP-1", Status: "Todo"})
			f.AddIssue("LERP", Issue{ID: "iss-2", Identifier: "LERP-2", Status: "Done"})
			f.AddIssue("LERP", Issue{ID: "iss-3", Identifier: "LERP-3", Status: "Canceled"})
			f.AddIssue("LERP", Issue{ID: "iss-4", Identifier: "LERP-4", Status: "Todo"})
			_ = f.AssignIssue(ctx, "iss-1", "user-9")
			_ = f.AssignIssue(ctx, "iss-3", "user-9")
		},
		handler: func(t *testing.T) http.HandlerFunc {
			t.Helper()
			return func(w http.ResponseWriter, r *http.Request) {
				req := decodeRequest(t, r)
				switch {
				// LERP-3, assigned but canceled, and LERP-2, unassigned but
				// done, are both absent: the real query excludes them
				// server-side, so a fixture that included them would not be
				// testing what the client actually receives.
				case strings.Contains(req.Query, "ListAssignedIssues"):
					writeData(t, w, `{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
						{"id":"iss-1","identifier":"LERP-1","title":"","state":{"name":"Todo","type":"unstarted"},
						 "assignee":{"id":"user-9"},"inverseRelations":{"nodes":[]}}
					]}}`)
				case strings.Contains(req.Query, "ListUnassignedIssues"):
					writeData(t, w, `{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
						{"id":"iss-4","identifier":"LERP-4","title":"","state":{"name":"Todo","type":"unstarted"},
						 "assignee":null,"inverseRelations":{"nodes":[]}}
					]}}`)
				default:
					t.Fatalf("unexpected query: %s", req.Query)
				}
			}
		},
		run: func(ctx context.Context, c Client) (any, error) {
			assigned, err := c.ListAssignedIssues(ctx, "LERP", "user-9")
			if err != nil {
				return nil, err
			}
			unassigned, err := c.ListUnassignedIssues(ctx, "LERP")
			if err != nil {
				return nil, err
			}
			return [2][]string{identifiers(assigned), identifiers(unassigned)}, nil
		},
		want: [2][]string{{"LERP-1"}, {"LERP-4"}},
	})

	cases = append(cases, contractCase{
		name: "blocking derivation and release when the blocker finishes",
		seedFake: func(f *Fake) {
			f.AddIssue("LERP", Issue{ID: "iss-a", Identifier: "LERP-1", Status: "Todo"})
			f.AddIssue("LERP", Issue{ID: "iss-b", Identifier: "LERP-2", Status: "In Progress"})
			f.AddIssue("LERP", Issue{ID: "iss-c", Identifier: "LERP-3", Status: "Done"})
			f.Block("iss-a", "iss-b")
			f.Block("iss-a", "iss-c")
		},
		handler: blockingHandler,
		run: func(ctx context.Context, c Client) (any, error) {
			a1, err := c.GetIssue(ctx, "iss-a")
			if err != nil {
				return nil, err
			}
			b, err := c.GetIssue(ctx, "iss-b")
			if err != nil {
				return nil, err
			}
			if err := c.MoveIssue(ctx, "iss-b", "Done"); err != nil {
				return nil, err
			}
			a2, err := c.GetIssue(ctx, "iss-a")
			if err != nil {
				return nil, err
			}
			return blockingSnapshot{
				aBlockedBefore:   a1.Blocked,
				aBlockedByBefore: a1.BlockedBy,
				bBlocks:          b.Blocks,
				aBlockedAfter:    a2.Blocked,
			}, nil
		},
		want: blockingSnapshot{
			aBlockedBefore:   true,
			aBlockedByBefore: []string{"LERP-2"},
			bBlocks:          []string{"LERP-1"},
			aBlockedAfter:    false,
		},
	})

	return cases
}
