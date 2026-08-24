package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is Linear's GraphQL endpoint.
const DefaultEndpoint = "https://api.linear.app/graphql"

// Issue is the slice of a Linear issue that lerp cares about.
type Issue struct {
	ID         string // Linear's internal UUID
	Identifier string // human identifier, e.g. LERP-7
	Title      string
	Status     string // workflow state name, e.g. "In Progress"
	AssigneeID string // empty when unassigned
	URL        string // Linear's own web URL for the issue
	Blocked    bool   // true when any incomplete issue blocks this one
	BlockedBy  []string
	// Blocks names the unfinished issues this one blocks — the other half
	// of the same relation, read forward. It is what makes leverage
	// ("promoting this frees three others") visible without walking the
	// board.
	Blocks []string
	// Priority is Linear's own scale: 0 none, 1 urgent, 2 high, 3 medium,
	// 4 low. Lerp never acts on it; it is carried so a human choosing what
	// to route can see it.
	Priority int
	// Project is the name of Linear's own project for the issue, empty when
	// it has none. Carried for the same reason as Priority and acted on as
	// little: it is the inbox table's project column, and the one field
	// its project filter matches.
	Project string
}

// IssueDetail is one ticket as the inbox pane reads it: the body the
// operator has to judge, and the comments on it — which, by SCOPE.md
// invariant 7, are lerp's own stage-boundary artifacts (the plan, the
// review verdict, the escalation note that parked the ticket).
type IssueDetail struct {
	Body     string
	Comments []Comment // oldest first
}

// Comment is one comment on an issue. Author is a display name, never an
// identity lerp acts on.
type Comment struct {
	Author    string
	Body      string
	CreatedAt time.Time
}

// Client is the operation surface lerp needs from Linear — exactly these,
// nothing more (SCOPE.md invariant 8). The loop is written against this
// interface and tested against Fake.
type Client interface {
	ListIssues(ctx context.Context, teamKey, statusName string) ([]Issue, error)
	// ListAssignedIssues is half of the inbox view's read: the
	// operator's own claimed tickets, in any workflow state, of which the
	// view keeps the ones resting outside every queue status. Completed and
	// canceled issues are excluded; they wait on nobody.
	ListAssignedIssues(ctx context.Context, teamKey, assigneeID string) ([]Issue, error)
	// ListUnassignedIssues is the other half: unclaimed tickets, in any
	// workflow state. Completed and canceled issues are excluded, same as
	// ListAssignedIssues.
	ListUnassignedIssues(ctx context.Context, teamKey string) ([]Issue, error)
	// TeamStates reports the names of the team's workflow states, in board
	// order — the one read behind the startup verification that every
	// configured status exists on its team (loop.VerifyStatuses). The loop's
	// regular passes never call it.
	TeamStates(ctx context.Context, teamKey string) ([]string, error)
	GetIssue(ctx context.Context, issueID string) (Issue, error)
	// GetIssueDetail reads one issue's body and its comments — the read
	// SCOPE.md's "not a Linear client" bullet licenses for the inbox
	// pane. Read-only, and only for the ticket the operator selected; no
	// pass calls it, the TUI issues it on selection.
	GetIssueDetail(ctx context.Context, issueID string) (IssueDetail, error)
	MoveIssue(ctx context.Context, issueID, statusName string) error
	AssignIssue(ctx context.Context, issueID, userID string) error
	UnassignIssue(ctx context.Context, issueID string) error
	CommentOnIssue(ctx context.Context, issueID, bodyMarkdown string) error
	Viewer(ctx context.Context) (string, error)
}

// Sentinel errors, matchable with errors.Is.
var (
	// ErrAuth means Linear rejected the API key (HTTP 401).
	ErrAuth = errors.New("linear: authentication failed")
	// ErrNotFound means the referenced entity does not exist.
	ErrNotFound = errors.New("linear: not found")
	// ErrRateLimited means Linear returned HTTP 429. The error in the
	// chain is a *RateLimitError carrying the Retry-After delay.
	ErrRateLimited = errors.New("linear: rate limited")
)

// RateLimitError is returned on HTTP 429. It matches ErrRateLimited via
// errors.Is; use errors.As to read RetryAfter (zero when the server sent
// no usable Retry-After header).
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("linear: rate limited (retry after %s)", e.RetryAfter)
	}
	return "linear: rate limited"
}

func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// GraphQLError is an HTTP 200 response whose errors array was non-empty.
type GraphQLError struct {
	Messages []string
}

func (e *GraphQLError) Error() string {
	return "linear: graphql: " + strings.Join(e.Messages, "; ")
}

// HTTP is the real Client, speaking GraphQL over net/http.
type HTTP struct {
	// Endpoint is the GraphQL URL, DefaultEndpoint unless overridden
	// (tests point it at an httptest.Server).
	Endpoint string

	apiKey string
	hc     *http.Client
}

var _ Client = (*HTTP)(nil)

// New returns a client authenticating with apiKey (conventionally read
// from the LINEAR_API_KEY environment variable by the caller). Personal
// API keys go in the Authorization header as-is. A nil hc gets a default
// client with a 30s timeout.
func New(apiKey string, hc *http.Client) *HTTP {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTP{Endpoint: DefaultEndpoint, apiKey: apiKey, hc: hc}
}

// do posts one GraphQL request and decodes data into out (which may be
// nil when the caller only cares about errors).
func (c *HTTP) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables,omitempty"`
	}{query, vars})
	if err != nil {
		return fmt.Errorf("linear: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("linear: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrAuth
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode != http.StatusOK:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("linear: unexpected status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("linear: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			msgs[i] = e.Message
			if strings.Contains(strings.ToLower(e.Message), "entity not found") {
				return fmt.Errorf("linear: %s: %w", e.Message, ErrNotFound)
			}
		}
		return &GraphQLError{Messages: msgs}
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("linear: decode data: %w", err)
		}
	}
	return nil
}

// retryAfter parses a Retry-After header: delta-seconds or an HTTP date.
func retryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
