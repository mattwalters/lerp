package linear

import (
	"context"
	"fmt"
)

// issueNode is the shape shared by every query that reads an issue.
type issueNode struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	State      struct {
		Name string `json:"name"`
	} `json:"state"`
	Assignee *struct {
		ID string `json:"id"`
	} `json:"assignee"`
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
}

func (n issueNode) toIssue() Issue {
	is := Issue{
		ID:         n.ID,
		Identifier: n.Identifier,
		Title:      n.Title,
		Status:     n.State.Name,
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
      state { name }
      assignee { id }
      inverseRelations(first: 50) {
        nodes {
          type
          issue { identifier state { type } }
        }
      }
    }
  }
}`

// ListIssues returns every issue of the team with the given key sitting
// in the workflow state with the given name.
func (c *HTTP) ListIssues(ctx context.Context, teamKey, statusName string) ([]Issue, error) {
	var issues []Issue
	after := ""
	for {
		vars := map[string]any{"team": teamKey, "state": statusName}
		if after != "" {
			vars["after"] = after
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
		if err := c.do(ctx, listIssuesQuery, vars, &resp); err != nil {
			return nil, fmt.Errorf("list issues: %w", err)
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
    state { name }
    assignee { id }
    inverseRelations(first: 50) {
      nodes {
        type
        issue { identifier state { type } }
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
