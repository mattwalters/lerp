package linear

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// issueNode is the shape shared by every query that reads an issue.
type issueNode struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	State      struct {
		Name string `json:"name"`
	} `json:"state"`
	Assignee *struct {
		ID string `json:"id"`
	} `json:"assignee"`
	// Linear types priority as a Float, so it decodes as one.
	Priority         float64 `json:"priority"`
	InverseRelations struct {
		Nodes []struct {
			Type  string `json:"type"`
			Issue struct {
				Identifier string `json:"identifier"`
				State      struct {
					Type string `json:"type"`
				} `json:"state"`
			} `json:"issue"`
		} `json:"nodes"`
	} `json:"inverseRelations"`
	Relations struct {
		Nodes []struct {
			Type         string `json:"type"`
			RelatedIssue struct {
				Identifier string `json:"identifier"`
				State      struct {
					Type string `json:"type"`
				} `json:"state"`
			} `json:"relatedIssue"`
		} `json:"nodes"`
	} `json:"relations"`
}

func (n issueNode) toIssue() Issue {
	is := Issue{
		ID:         n.ID,
		Identifier: n.Identifier,
		Title:      n.Title,
		URL:        n.URL,
		Status:     n.State.Name,
		Priority:   int(n.Priority),
	}
	if n.Assignee != nil {
		is.AssigneeID = n.Assignee.ID
	}
	// An issue blocked by A shows a relation of type "blocks" in its
	// inverseRelations, whose issue field is A (the blocker). A blocker
	// counts only while incomplete: its state type is neither completed
	// nor canceled.
	for _, r := range n.InverseRelations.Nodes {
		if r.Type != "blocks" {
			continue
		}
		if t := r.Issue.State.Type; t == "completed" || t == "canceled" {
			continue
		}
		is.BlockedBy = append(is.BlockedBy, r.Issue.Identifier)
	}
	is.Blocked = len(is.BlockedBy) > 0
	// The same relation read forward: this issue's own "blocks" relations
	// name the issues it holds up. A finished issue is no longer held up by
	// anything, so it does not count.
	for _, r := range n.Relations.Nodes {
		if r.Type != "blocks" {
			continue
		}
		if t := r.RelatedIssue.State.Type; t == "completed" || t == "canceled" {
			continue
		}
		is.Blocks = append(is.Blocks, r.RelatedIssue.Identifier)
	}
	return is
}

// updateResult is the shape of issueUpdate mutations.
type updateResult struct {
	IssueUpdate struct {
		Success bool `json:"success"`
	} `json:"issueUpdate"`
}

const listIssuesQuery = `
query ListIssues($team: String!, $state: String!, $after: String) {
  issues(
    first: 50
    after: $after
    filter: { team: { key: { eq: $team } }, state: { name: { eq: $state } } }
  ) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      identifier
      title
      url
      state { name }
      assignee { id }
      priority
      inverseRelations(first: 50) {
        nodes {
          type
          issue { identifier state { type } }
        }
      }
      relations(first: 50) {
        nodes {
          type
          relatedIssue { identifier state { type } }
        }
      }
    }
  }
}`

// ListIssues returns every issue of the team with the given key sitting
// in the workflow state with the given name.
func (c *HTTP) ListIssues(ctx context.Context, teamKey, statusName string) ([]Issue, error) {
	issues, err := c.listIssues(ctx, listIssuesQuery, map[string]any{"team": teamKey, "state": statusName})
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	return issues, nil
}

const listAssignedIssuesQuery = `
query ListAssignedIssues($team: String!, $assignee: ID!, $after: String) {
  issues(
    first: 50
    after: $after
    filter: {
      team: { key: { eq: $team } }
      assignee: { id: { eq: $assignee } }
      state: { type: { nin: ["completed", "canceled"] } }
    }
  ) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      identifier
      title
      url
      state { name }
      assignee { id }
      priority
      inverseRelations(first: 50) {
        nodes {
          type
          issue { identifier state { type } }
        }
      }
      relations(first: 50) {
        nodes {
          type
          relatedIssue { identifier state { type } }
        }
      }
    }
  }
}`

// ListAssignedIssues returns the team's unfinished issues assigned to the
// user, in any workflow state — the read behind the attention view (see
// Client). Completed and canceled issues are filtered out server-side.
func (c *HTTP) ListAssignedIssues(ctx context.Context, teamKey, assigneeID string) ([]Issue, error) {
	issues, err := c.listIssues(ctx, listAssignedIssuesQuery, map[string]any{"team": teamKey, "assignee": assigneeID})
	if err != nil {
		return nil, fmt.Errorf("list assigned issues: %w", err)
	}
	return issues, nil
}

const listUnassignedIssuesQuery = `
query ListUnassignedIssues($team: String!, $after: String) {
  issues(
    first: 50
    after: $after
    filter: {
      team: { key: { eq: $team } }
      assignee: { null: true }
      state: { type: { nin: ["completed", "canceled"] } }
    }
  ) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      identifier
      title
      url
      state { name }
      assignee { id }
      priority
      inverseRelations(first: 50) {
        nodes {
          type
          issue { identifier state { type } }
        }
      }
      relations(first: 50) {
        nodes {
          type
          relatedIssue { identifier state { type } }
        }
      }
    }
  }
}`

// ListUnassignedIssues returns the team's unclaimed issues, in any workflow
// state — the read behind needs-you's "to route" group (see Client).
// Completed and canceled issues are filtered out server-side.
func (c *HTTP) ListUnassignedIssues(ctx context.Context, teamKey string) ([]Issue, error) {
	issues, err := c.listIssues(ctx, listUnassignedIssuesQuery, map[string]any{"team": teamKey})
	if err != nil {
		return nil, fmt.Errorf("list unassigned issues: %w", err)
	}
	return issues, nil
}

// listIssues runs one of the issue-listing queries to exhaustion, following
// pageInfo cursors. vars holds the query's own variables; the cursor is
// added here.
func (c *HTTP) listIssues(ctx context.Context, query string, vars map[string]any) ([]Issue, error) {
	var issues []Issue
	after := ""
	for {
		page := make(map[string]any, len(vars)+1)
		for k, v := range vars {
			page[k] = v
		}
		if after != "" {
			page["after"] = after
		}
		var resp struct {
			Issues struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []issueNode `json:"nodes"`
			} `json:"issues"`
		}
		if err := c.do(ctx, query, page, &resp); err != nil {
			return nil, err
		}
		for _, n := range resp.Issues.Nodes {
			issues = append(issues, n.toIssue())
		}
		if !resp.Issues.PageInfo.HasNextPage {
			return issues, nil
		}
		after = resp.Issues.PageInfo.EndCursor
	}
}

const getIssueQuery = `
query GetIssue($id: String!) {
  issue(id: $id) {
    id
    identifier
    title
    url
    state { name }
    assignee { id }
    priority
    inverseRelations(first: 50) {
      nodes {
        type
        issue { identifier state { type } }
      }
    }
    relations(first: 50) {
      nodes {
        type
        relatedIssue { identifier state { type } }
      }
    }
  }
}`

// GetIssue reads one issue fresh — the read-back of the claim protocol
// (SCOPE.md invariant 4).
func (c *HTTP) GetIssue(ctx context.Context, issueID string) (Issue, error) {
	var resp struct {
		Issue *issueNode `json:"issue"`
	}
	if err := c.do(ctx, getIssueQuery, map[string]any{"id": issueID}, &resp); err != nil {
		return Issue{}, fmt.Errorf("get issue: %w", err)
	}
	if resp.Issue == nil {
		return Issue{}, fmt.Errorf("get issue %s: %w", issueID, ErrNotFound)
	}
	return resp.Issue.toIssue(), nil
}

const issueDetailQuery = `
query IssueDetail($id: String!) {
  issue(id: $id) {
    description
    comments(first: 50) {
      nodes {
        body
        createdAt
        user { displayName }
        botActor { name }
      }
    }
  }
}`

// GetIssueDetail reads one issue's body and comments — the needs-you pane's
// read (see Client). It is deliberately its own query rather than fields on
// issueNode: that struct is shared by the three list queries every pass
// runs, and hanging a description or a comment connection off it would grow
// the payload of the passes this read is meant to stay out of. Fifty
// comments is past the point where a pane is the right way to read a
// thread; `o` is the answer to a longer one.
func (c *HTTP) GetIssueDetail(ctx context.Context, issueID string) (IssueDetail, error) {
	var resp struct {
		Issue *struct {
			Description string `json:"description"`
			Comments    struct {
				Nodes []commentNode `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}
	if err := c.do(ctx, issueDetailQuery, map[string]any{"id": issueID}, &resp); err != nil {
		return IssueDetail{}, fmt.Errorf("get issue detail: %w", err)
	}
	if resp.Issue == nil {
		return IssueDetail{}, fmt.Errorf("get issue detail %s: %w", issueID, ErrNotFound)
	}
	detail := IssueDetail{Body: resp.Issue.Description}
	for _, n := range resp.Issue.Comments.Nodes {
		detail.Comments = append(detail.Comments, n.toComment())
	}
	// Oldest first, whatever order the connection came back in: the pane
	// reads chronologically, so the verdict written last is the last thing
	// on screen.
	sort.SliceStable(detail.Comments, func(i, j int) bool {
		return detail.Comments[i].CreatedAt.Before(detail.Comments[j].CreatedAt)
	})
	return detail, nil
}

// commentNode is one node of the comments connection.
type commentNode struct {
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	User      *struct {
		DisplayName string `json:"displayName"`
	} `json:"user"`
	BotActor *struct {
		Name string `json:"name"`
	} `json:"botActor"`
}

func (n commentNode) toComment() Comment {
	// Lerp's own artifacts arrive under the API key's user; agents working
	// Linear-side post as bot actors, with no user at all.
	author := "unknown"
	switch {
	case n.User != nil && n.User.DisplayName != "":
		author = n.User.DisplayName
	case n.BotActor != nil && n.BotActor.Name != "":
		author = n.BotActor.Name
	}
	return Comment{Author: author, Body: n.Body, CreatedAt: n.CreatedAt}
}

const issueStatesQuery = `
query IssueStates($id: String!) {
  issue(id: $id) {
    team {
      states(first: 100) {
        nodes { id name }
      }
    }
  }
}`

const moveIssueMutation = `
mutation MoveIssue($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: { stateId: $stateId }) { success }
}`

// MoveIssue moves an issue to the workflow state named statusName,
// resolved within the issue's own team.
func (c *HTTP) MoveIssue(ctx context.Context, issueID, statusName string) error {
	var resp struct {
		Issue *struct {
			Team struct {
				States struct {
					Nodes []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"states"`
			} `json:"team"`
		} `json:"issue"`
	}
	if err := c.do(ctx, issueStatesQuery, map[string]any{"id": issueID}, &resp); err != nil {
		return fmt.Errorf("move issue: %w", err)
	}
	if resp.Issue == nil {
		return fmt.Errorf("move issue %s: %w", issueID, ErrNotFound)
	}
	stateID := ""
	for _, s := range resp.Issue.Team.States.Nodes {
		if s.Name == statusName {
			stateID = s.ID
			break
		}
	}
	if stateID == "" {
		return fmt.Errorf("move issue %s: no state named %q on its team", issueID, statusName)
	}
	var upd updateResult
	if err := c.do(ctx, moveIssueMutation, map[string]any{"id": issueID, "stateId": stateID}, &upd); err != nil {
		return fmt.Errorf("move issue: %w", err)
	}
	if !upd.IssueUpdate.Success {
		return fmt.Errorf("move issue %s: linear reported failure", issueID)
	}
	return nil
}

const assignIssueMutation = `
mutation AssignIssue($id: String!, $assigneeId: String!) {
  issueUpdate(id: $id, input: { assigneeId: $assigneeId }) { success }
}`

// AssignIssue assigns the issue to the user — the claim of the claim
// protocol.
func (c *HTTP) AssignIssue(ctx context.Context, issueID, userID string) error {
	var upd updateResult
	if err := c.do(ctx, assignIssueMutation, map[string]any{"id": issueID, "assigneeId": userID}, &upd); err != nil {
		return fmt.Errorf("assign issue: %w", err)
	}
	if !upd.IssueUpdate.Success {
		return fmt.Errorf("assign issue %s: linear reported failure", issueID)
	}
	return nil
}

const unassignIssueMutation = `
mutation UnassignIssue($id: String!) {
  issueUpdate(id: $id, input: { assigneeId: null }) { success }
}`

// UnassignIssue clears the issue's assignee.
func (c *HTTP) UnassignIssue(ctx context.Context, issueID string) error {
	var upd updateResult
	if err := c.do(ctx, unassignIssueMutation, map[string]any{"id": issueID}, &upd); err != nil {
		return fmt.Errorf("unassign issue: %w", err)
	}
	if !upd.IssueUpdate.Success {
		return fmt.Errorf("unassign issue %s: linear reported failure", issueID)
	}
	return nil
}

const commentCreateMutation = `
mutation CommentOnIssue($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) { success }
}`

// CommentOnIssue posts a markdown comment — how stage-boundary artifacts
// reach Linear (SCOPE.md invariant 7).
func (c *HTTP) CommentOnIssue(ctx context.Context, issueID, bodyMarkdown string) error {
	var resp struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	if err := c.do(ctx, commentCreateMutation, map[string]any{"issueId": issueID, "body": bodyMarkdown}, &resp); err != nil {
		return fmt.Errorf("comment on issue: %w", err)
	}
	if !resp.CommentCreate.Success {
		return fmt.Errorf("comment on issue %s: linear reported failure", issueID)
	}
	return nil
}

const viewerQuery = `
query Viewer {
  viewer { id }
}`

// Viewer returns the authenticated user's id — the self of the claim
// protocol.
func (c *HTTP) Viewer(ctx context.Context) (string, error) {
	var resp struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
	}
	if err := c.do(ctx, viewerQuery, nil, &resp); err != nil {
		return "", fmt.Errorf("viewer: %w", err)
	}
	return resp.Viewer.ID, nil
}
