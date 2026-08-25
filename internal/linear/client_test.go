package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// gqlRequest is the decoded body of one GraphQL POST.
type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// testClient starts an httptest.Server on h and returns a client
// pointed at it.
func testClient(t *testing.T, h http.HandlerFunc) *HTTP {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("test-key", srv.Client())
	c.Endpoint = srv.URL
	return c
}

func decodeRequest(t *testing.T, r *http.Request) gqlRequest {
	t.Helper()
	var req gqlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req
}

func writeData(t *testing.T, w http.ResponseWriter, data string) {
	t.Helper()
	fmt.Fprintf(w, `{"data":%s}`, data)
}

func TestViewer(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "test-key" {
			t.Errorf("Authorization = %q, want %q", got, "test-key")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		req := decodeRequest(t, r)
		if !strings.Contains(req.Query, "viewer") {
			t.Errorf("query does not mention viewer: %q", req.Query)
		}
		writeData(t, w, `{"viewer":{"id":"user-1"}}`)
	})
	id, err := c.Viewer(context.Background())
	if err != nil {
		t.Fatalf("Viewer: %v", err)
	}
	if id != "user-1" {
		t.Errorf("id = %q, want user-1", id)
	}
}

func TestListIssues(t *testing.T) {
	page := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Variables["team"] != "LERP" || req.Variables["state"] != "Todo" {
			t.Errorf("variables = %v", req.Variables)
		}
		page++
		switch page {
		case 1:
			if _, ok := req.Variables["after"]; ok {
				t.Errorf("first page sent after = %v", req.Variables["after"])
			}
			writeData(t, w, `{"issues":{
				"pageInfo":{"hasNextPage":true,"endCursor":"cur-1"},
				"nodes":[{
					"id":"iss-1","identifier":"LERP-1","title":"First","state":{"name":"Todo"},
					"assignee":{"id":"user-9"},"priority":2,
					"inverseRelations":{"nodes":[
						{"type":"blocks","issue":{"identifier":"LERP-5","state":{"type":"started"}}},
						{"type":"blocks","issue":{"identifier":"LERP-6","state":{"type":"completed"}}},
						{"type":"related","issue":{"identifier":"LERP-7","state":{"type":"started"}}}
					]},
					"relations":{"nodes":[
						{"type":"blocks","relatedIssue":{"identifier":"LERP-8","state":{"type":"unstarted"}}},
						{"type":"blocks","relatedIssue":{"identifier":"LERP-9","state":{"type":"canceled"}}},
						{"type":"related","relatedIssue":{"identifier":"LERP-10","state":{"type":"started"}}}
					]}
				}]
			}}`)
		case 2:
			if req.Variables["after"] != "cur-1" {
				t.Errorf("second page after = %v, want cur-1", req.Variables["after"])
			}
			writeData(t, w, `{"issues":{
				"pageInfo":{"hasNextPage":false,"endCursor":""},
				"nodes":[{
					"id":"iss-2","identifier":"LERP-2","title":"Second","state":{"name":"Todo"},
					"assignee":null,"priority":0,
					"inverseRelations":{"nodes":[]},
					"relations":{"nodes":[]}
				}]
			}}`)
		default:
			t.Errorf("unexpected page %d", page)
		}
	})

	issues, err := c.ListIssues(context.Background(), "LERP", "Todo")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	want := []Issue{
		{ID: "iss-1", Identifier: "LERP-1", Title: "First", Status: "Todo",
			AssigneeID: "user-9", Blocked: true, BlockedBy: []string{"LERP-5"},
			Blocks: []string{"LERP-8"}, Priority: 2},
		{ID: "iss-2", Identifier: "LERP-2", Title: "Second", Status: "Todo"},
	}
	if !reflect.DeepEqual(issues, want) {
		t.Errorf("issues = %+v, want %+v", issues, want)
	}
}

func TestListAssignedIssues(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Variables["team"] != "LERP" || req.Variables["assignee"] != "user-9" {
			t.Errorf("variables = %v", req.Variables)
		}
		// Finished issues wait on nobody; the query must exclude them
		// server-side rather than page them all back.
		if !strings.Contains(req.Query, `nin: ["completed", "canceled"]`) {
			t.Errorf("query does not exclude finished states: %q", req.Query)
		}
		// The inbox table has a project column, so this is one of the
		// two queries that asks for one.
		if !strings.Contains(req.Query, "project { name }") {
			t.Errorf("query does not read the project: %q", req.Query)
		}
		writeData(t, w, `{"issues":{
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[{
				"id":"iss-1","identifier":"LERP-1","title":"First","state":{"name":"Needs Help"},
				"url":"https://linear.app/acme/issue/LERP-1/first",
				"assignee":{"id":"user-9"},
				"project":{"name":"Open-source readiness"},
				"inverseRelations":{"nodes":[]}
			}]
		}}`)
	})

	issues, err := c.ListAssignedIssues(context.Background(), "LERP", "user-9")
	if err != nil {
		t.Fatalf("ListAssignedIssues: %v", err)
	}
	want := []Issue{
		{ID: "iss-1", Identifier: "LERP-1", Title: "First", Status: "Needs Help",
			AssigneeID: "user-9", URL: "https://linear.app/acme/issue/LERP-1/first",
			Project: "Open-source readiness"},
	}
	if !reflect.DeepEqual(issues, want) {
		t.Errorf("issues = %+v, want %+v", issues, want)
	}
}

func TestListUnassignedIssues(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Variables["team"] != "LERP" {
			t.Errorf("variables = %v", req.Variables)
		}
		if !strings.Contains(req.Query, "assignee: { null: true }") {
			t.Errorf("query does not filter for no assignee: %q", req.Query)
		}
		if !strings.Contains(req.Query, `nin: ["completed", "canceled"]`) {
			t.Errorf("query does not exclude finished states: %q", req.Query)
		}
		if !strings.Contains(req.Query, "project { name }") {
			t.Errorf("query does not read the project: %q", req.Query)
		}
		// A ticket in no project decodes as a null, which is the empty
		// name the table draws as a dash.
		writeData(t, w, `{"issues":{
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[{
				"id":"iss-1","identifier":"LERP-1","title":"First","state":{"name":"Backlog"},
				"url":"https://linear.app/acme/issue/LERP-1/first",
				"assignee":null,
				"project":null,
				"inverseRelations":{"nodes":[]}
			}]
		}}`)
	})

	issues, err := c.ListUnassignedIssues(context.Background(), "LERP")
	if err != nil {
		t.Fatalf("ListUnassignedIssues: %v", err)
	}
	want := []Issue{
		{ID: "iss-1", Identifier: "LERP-1", Title: "First", Status: "Backlog",
			URL: "https://linear.app/acme/issue/LERP-1/first"},
	}
	if !reflect.DeepEqual(issues, want) {
		t.Errorf("issues = %+v, want %+v", issues, want)
	}
}

func TestGetIssue(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Variables["id"] != "iss-1" {
			t.Errorf("id = %v, want iss-1", req.Variables["id"])
		}
		writeData(t, w, `{"issue":{
			"id":"iss-1","identifier":"LERP-1","title":"First","state":{"name":"In Progress"},
			"assignee":{"id":"user-9"},
			"inverseRelations":{"nodes":[]}
		}}`)
	})
	is, err := c.GetIssue(context.Background(), "iss-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	want := Issue{ID: "iss-1", Identifier: "LERP-1", Title: "First",
		Status: "In Progress", AssigneeID: "user-9"}
	if !reflect.DeepEqual(is, want) {
		t.Errorf("issue = %+v, want %+v", is, want)
	}
}

func TestGetIssueNotFound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"errors":[{"message":"Entity not found: Issue - Could not find referenced Issue."}]}`)
	})
	_, err := c.GetIssue(context.Background(), "iss-nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestGetIssueNullIssue(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(t, w, `{"issue":null}`)
	})
	_, err := c.GetIssue(context.Background(), "iss-nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// The inbox pane's read: the body the operator judges from, and the
// stage-boundary artifacts lerp itself wrote on the ticket.
func TestGetIssueDetail(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Variables["id"] != "iss-1" {
			t.Errorf("id = %v, want iss-1", req.Variables["id"])
		}
		if strings.Contains(req.Query, "inverseRelations") {
			t.Errorf("detail query carries the list query's relations: %q", req.Query)
		}
		writeData(t, w, `{"issue":{
			"description":"the body",
			"comments":{"nodes":[
				{"body":"verdict","createdAt":"2026-08-24T12:00:00.000Z","user":{"displayName":"Matt"}},
				{"body":"plan","createdAt":"2026-08-24T09:00:00.000Z","user":null,"botActor":{"name":"lerp"}}
			]}
		}}`)
	})
	detail, err := c.GetIssueDetail(context.Background(), "iss-1")
	if err != nil {
		t.Fatalf("GetIssueDetail: %v", err)
	}
	if detail.Body != "the body" {
		t.Errorf("body = %q, want %q", detail.Body, "the body")
	}
	// Oldest first, whatever order the connection came back in — and an
	// agent posting as a bot actor still has a name on its comment.
	want := []Comment{
		{Author: "lerp", Body: "plan", CreatedAt: time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)},
		{Author: "Matt", Body: "verdict", CreatedAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)},
	}
	if !reflect.DeepEqual(detail.Comments, want) {
		t.Errorf("comments = %+v, want %+v", detail.Comments, want)
	}
}

// A comment with neither a user nor a bot actor still renders: the pane
// needs a byline, not an identity.
func TestGetIssueDetailUnknownAuthor(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(t, w, `{"issue":{"description":"","comments":{"nodes":[{"body":"orphan"}]}}}`)
	})
	detail, err := c.GetIssueDetail(context.Background(), "iss-1")
	if err != nil {
		t.Fatalf("GetIssueDetail: %v", err)
	}
	if len(detail.Comments) != 1 || detail.Comments[0].Author != "unknown" {
		t.Errorf("comments = %+v, want one authored \"unknown\"", detail.Comments)
	}
}

func TestGetIssueDetailNullIssue(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(t, w, `{"issue":null}`)
	})
	_, err := c.GetIssueDetail(context.Background(), "iss-nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMoveIssue(t *testing.T) {
	var moved gqlRequest
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		switch {
		case strings.Contains(req.Query, "IssueStates"):
			if req.Variables["id"] != "iss-1" {
				t.Errorf("states lookup id = %v", req.Variables["id"])
			}
			writeData(t, w, `{"issue":{"team":{"states":{"nodes":[
				{"id":"st-1","name":"Todo"},
				{"id":"st-2","name":"In Progress"}
			]}}}}`)
		case strings.Contains(req.Query, "MoveIssue"):
			moved = req
			writeData(t, w, `{"issueUpdate":{"success":true}}`)
		default:
			t.Errorf("unexpected query %q", req.Query)
		}
	})
	if err := c.MoveIssue(context.Background(), "iss-1", "In Progress"); err != nil {
		t.Fatalf("MoveIssue: %v", err)
	}
	if moved.Variables["id"] != "iss-1" || moved.Variables["stateId"] != "st-2" {
		t.Errorf("mutation variables = %v, want id iss-1 stateId st-2", moved.Variables)
	}
}

func TestMoveIssueUnknownStatus(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(t, w, `{"issue":{"team":{"states":{"nodes":[{"id":"st-1","name":"Todo"}]}}}}`)
	})
	err := c.MoveIssue(context.Background(), "iss-1", "Nonexistent")
	if err == nil || !strings.Contains(err.Error(), `no state named "Nonexistent"`) {
		t.Errorf("err = %v, want unknown-state error", err)
	}
}

func TestAssignIssue(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Variables["id"] != "iss-1" || req.Variables["assigneeId"] != "user-9" {
			t.Errorf("variables = %v", req.Variables)
		}
		writeData(t, w, `{"issueUpdate":{"success":true}}`)
	})
	if err := c.AssignIssue(context.Background(), "iss-1", "user-9"); err != nil {
		t.Fatalf("AssignIssue: %v", err)
	}
}

func TestUnassignIssue(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if !strings.Contains(req.Query, "assigneeId: null") {
			t.Errorf("query does not null the assignee: %q", req.Query)
		}
		if req.Variables["id"] != "iss-1" {
			t.Errorf("variables = %v", req.Variables)
		}
		writeData(t, w, `{"issueUpdate":{"success":true}}`)
	})
	if err := c.UnassignIssue(context.Background(), "iss-1"); err != nil {
		t.Fatalf("UnassignIssue: %v", err)
	}
}

func TestCommentOnIssue(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Variables["issueId"] != "iss-1" || req.Variables["body"] != "## Plan\n\ndo it" {
			t.Errorf("variables = %v", req.Variables)
		}
		writeData(t, w, `{"commentCreate":{"success":true}}`)
	})
	if err := c.CommentOnIssue(context.Background(), "iss-1", "## Plan\n\ndo it"); err != nil {
		t.Fatalf("CommentOnIssue: %v", err)
	}
}

func TestUpdateReportsFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(t, w, `{"issueUpdate":{"success":false}}`)
	})
	err := c.AssignIssue(context.Background(), "iss-1", "user-9")
	if err == nil || !strings.Contains(err.Error(), "reported failure") {
		t.Errorf("err = %v, want reported-failure error", err)
	}
}

func TestErrorStatuses(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		check   func(t *testing.T, err error)
	}{
		{
			name: "auth 401",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrAuth) {
					t.Errorf("err = %v, want ErrAuth", err)
				}
			},
		},
		{
			name: "rate limit 429 with Retry-After",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrRateLimited) {
					t.Fatalf("err = %v, want ErrRateLimited", err)
				}
				var rl *RateLimitError
				if !errors.As(err, &rl) {
					t.Fatalf("err = %v, want *RateLimitError", err)
				}
				if rl.RetryAfter != 30*time.Second {
					t.Errorf("RetryAfter = %v, want 30s", rl.RetryAfter)
				}
			},
		},
		{
			name: "rate limit 429 without Retry-After",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
			},
			check: func(t *testing.T, err error) {
				var rl *RateLimitError
				if !errors.As(err, &rl) {
					t.Fatalf("err = %v, want *RateLimitError", err)
				}
				if rl.RetryAfter != 0 {
					t.Errorf("RetryAfter = %v, want 0", rl.RetryAfter)
				}
			},
		},
		{
			name: "graphql errors array",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"errors":[{"message":"Argument Validation Error"},{"message":"second"}]}`)
			},
			check: func(t *testing.T, err error) {
				var ge *GraphQLError
				if !errors.As(err, &ge) {
					t.Fatalf("err = %v, want *GraphQLError", err)
				}
				want := []string{"Argument Validation Error", "second"}
				if !reflect.DeepEqual(ge.Messages, want) {
					t.Errorf("Messages = %v, want %v", ge.Messages, want)
				}
			},
		},
		{
			name: "unexpected 500",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
			check: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
					t.Errorf("err = %v, want wrapped 500 with body", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, tt.handler)
			_, err := c.Viewer(context.Background())
			if err == nil {
				t.Fatal("Viewer succeeded, want error")
			}
			tt.check(t, err)
		})
	}
}

func TestRetryAfter(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"30", 30 * time.Second},
		{"0", 0},
		{"garbage", 0},
		{"-5", 0},
	}
	for _, tt := range tests {
		if got := retryAfter(tt.in); got != tt.want {
			t.Errorf("retryAfter(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
	// HTTP-date form: a date in the future yields a positive duration.
	future := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	if got := retryAfter(future); got <= 0 || got > 90*time.Second {
		t.Errorf("retryAfter(%q) = %v, want in (0, 90s]", future, got)
	}
}
