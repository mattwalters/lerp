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
	"sync"
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
	// StatusType is Linear's own category for that state — one of the
	// Category constants below. Requested only by the reads behind the
	// attention pass, and empty everywhere else: it is what separates a
	// ticket that has not entered the pipeline yet from one that fell out
	// of it, and nothing else asks.
	StatusType string
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
	// UpdatedAt is when Linear last changed the issue. Requested by the two
	// inbox listings and by ListTeamIssuesUpdatedSince, and zero everywhere
	// else — including the per-queue ListIssues, which has no cursor to
	// seed. Its only reader is the delta cursor, which advances to the
	// newest UpdatedAt it has seen so the next read asks for what changed
	// after it.
	UpdatedAt time.Time
}

// Linear's own workflow-state categories, as its API spells them. The first
// three are the ones a ticket sits in before any work starts on it — nothing
// has routed it anywhere — which is a different thing from a ticket resting
// in a status something moved it to. The two finished ones are named because
// a delta read is not filtered by state (see ListTeamIssuesUpdatedSince), so
// its reader has to recognise a ticket that has finished. The remaining
// category, started, is not named here because nothing reads it.
const (
	CategoryTriage    = "triage"
	CategoryBacklog   = "backlog"
	CategoryUnstarted = "unstarted"
	CategoryCompleted = "completed"
	CategoryCanceled  = "canceled"
)

// GitAutomation is one of a team's Linear git automations: a rule that moves
// the ticket linked to a pull request when a git event fires. Linear
// configures these per team, under the team's workflow settings.
type GitAutomation struct {
	// Event is the git event that fires the rule, as Linear's own enum
	// spells it — one of the GitEvent constants, or a name lerp has never
	// heard of if Linear adds one.
	Event string
	// Status is the workflow state the rule moves the ticket into. Empty
	// when the rule is set to take no action, which is Linear's way of
	// switching one off.
	Status string
	// Branch is the target-branch pattern the rule is scoped to, empty for
	// the team-wide rule. A branch-scoped rule overrides the team-wide one
	// for pull requests targeting a matching branch, so the two are reported
	// separately: an operator looking for the rule lerp warned about needs
	// to know which of the two to open.
	Branch string
}

// Linear's git automation events, as its API spells them. The first four fire
// while a pull request is open — which, for a ticket lerp is running, is
// mid-stage; the last fires on merge, after the pipeline is done with the
// ticket (SCOPE names that one the benign case).
const (
	GitEventDraft     = "draft"     // a draft pull request was opened
	GitEventStart     = "start"     // a pull request was opened
	GitEventReview    = "review"    // a review was requested, or reviewed
	GitEventMergeable = "mergeable" // it became ready for merge
	GitEventMerge     = "merge"     // it merged
)

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
	// ListTeamIssuesUpdatedSince returns the team's issues Linear touched at
	// or after since — the read that keeps the inbox current without listing
	// the whole backlog every pass. The caller holds the previous listing and
	// applies what comes back to it.
	//
	// Filtered by team and updatedAt and by nothing else, deliberately: an
	// issue that finishes, or that a colleague claims, has left the inbox,
	// and the only way its reader learns that is by the issue arriving here.
	// Filtering those out server-side — the obvious economy — is exactly how
	// a ticket would linger in the inbox until something else dislodged it.
	//
	// Ask with gte rather than gt: two issues can share the boundary
	// millisecond, and re-reading one is free where missing one is not.
	ListTeamIssuesUpdatedSince(ctx context.Context, teamKey string, since time.Time) ([]Issue, error)
	// TeamStates reports the names of the team's workflow states, in board
	// order — the one read behind the startup verification that every
	// configured status exists on its team (loop.Verify). The loop's
	// regular passes never call it.
	TeamStates(ctx context.Context, teamKey string) ([]string, error)
	// TeamGitAutomations reports the team's configured git automations —
	// the rules by which Linear itself moves a ticket when a pull request
	// linked to it changes state. Read once at startup beside TeamStates,
	// for the verification that warns when one of them would move a ticket
	// out from under a live run (loop.Verify). The loop's regular passes
	// never call it.
	TeamGitAutomations(ctx context.Context, teamKey string) ([]GitAutomation, error)
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

	auth Auth
	hc   *http.Client

	// The viewer id is immutable per API key, and half a dozen call sites
	// want it — every claim, every release, every attention pass. It is
	// memoized here rather than in any of them so they all get it with no
	// signature to thread it through. Cached on success only: a failed read
	// must not pin an empty id for the process's life.
	viewerMu sync.Mutex
	viewerID string
}

var _ Client = (*HTTP)(nil)

// Auth supplies the Authorization header value for one request. A plain
// func, not an interface: a personal API key is a closure over a string and
// there is no second method anything wants.
//
// Asked per request rather than held as a string because the value need not
// be constant — an OAuth access token expires and is renewed underneath the
// client, which never learns that happened. Who resolves it, and how, is
// internal/credentials' business; this package stays purely GraphQL
// (SCOPE.md invariant 8).
type Auth func(ctx context.Context) (string, error)

// New returns a client whose requests auth signs. Personal API keys go in
// the Authorization header as-is; OAuth access tokens come back with their
// "Bearer " prefix already on them. A nil hc gets a default client with a
// 30s timeout.
func New(auth Auth, hc *http.Client) *HTTP {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTP{Endpoint: DefaultEndpoint, auth: auth, hc: hc}
}

// do posts one GraphQL request and decodes data into out (which may be
// nil when the caller only cares about errors).
func (c *HTTP) do(ctx context.Context, query string, vars map[string]any, out any) error {
	// First, before anything is built or sent: a credential that cannot be
	// resolved is not a request that should go out unsigned. Returned
	// unwrapped — these errors already name their own remedy ("run \"lerp
	// login\""), and a "linear:" prefix in front of that reads as noise.
	authHeader, err := c.auth(ctx)
	if err != nil {
		return err
	}

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
	req.Header.Set("Authorization", authHeader)

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
