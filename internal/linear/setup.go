package linear

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const teamByKeyQuery = `
query TeamByKey($key: String!) { teams(filter: { key: { eq: $key } }, first: 1) { nodes { id } } }`

const teamCreateMutation = `
mutation TeamCreate($key: String!, $name: String!) {
  teamCreate(input: { key: $key, name: $name }) { success }
}`

// EnsureTeam verifies that key exists, creating it during setup when it does
// not. The loop never calls this method (SCOPE invariant 6).
func (c *HTTP) EnsureTeam(ctx context.Context, key, name string) error {
	var found struct {
		Teams struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := c.do(ctx, teamByKeyQuery, map[string]any{"key": key}, &found); err != nil {
		return err
	}
	if len(found.Teams.Nodes) != 0 {
		return nil
	}
	var created struct {
		TeamCreate struct {
			Success bool `json:"success"`
		} `json:"teamCreate"`
	}
	if err := c.do(ctx, teamCreateMutation, map[string]any{"key": key, "name": name}, &created); err != nil {
		return err
	}
	if !created.TeamCreate.Success {
		return errors.New("linear reported failure creating team")
	}
	return nil
}

const teamStatesByKeyQuery = `
query TeamStates($key: String!) {
  teams(filter: { key: { eq: $key } }, first: 1) { nodes { id states(first: 100) { nodes { name type } } } }
}`

// teamState is one workflow state as the states query reports it.
type teamState struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// teamStates reads a team's id and workflow states by team key.
func (c *HTTP) teamStates(ctx context.Context, teamKey string) (string, []teamState, error) {
	var found struct {
		Teams struct {
			Nodes []struct {
				ID     string `json:"id"`
				States struct {
					Nodes []teamState `json:"nodes"`
				} `json:"states"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := c.do(ctx, teamStatesByKeyQuery, map[string]any{"key": teamKey}, &found); err != nil {
		return "", nil, err
	}
	if len(found.Teams.Nodes) == 0 {
		return "", nil, fmt.Errorf("team %q: %w", teamKey, ErrNotFound)
	}
	team := found.Teams.Nodes[0]
	return team.ID, team.States.Nodes, nil
}

// TeamStates reports the names of every workflow state on the team, in
// Linear's listing order. Unlike this file's Ensure methods it is a pure
// read, safe outside setup: the loop's startup verification calls it once
// per configured team to check that every configured status names a real
// state (and to show the team's actual names next to a miss).
func (c *HTTP) TeamStates(ctx context.Context, teamKey string) ([]string, error) {
	_, states, err := c.teamStates(ctx, teamKey)
	if err != nil {
		return nil, fmt.Errorf("team states: %w", err)
	}
	names := make([]string, len(states))
	for i, s := range states {
		names[i] = s.Name
	}
	return names, nil
}

const viewerIdentityQuery = `
query ViewerIdentity {
  viewer { id name email }
}`

// Identity is the authenticated user, as `lerp login` reports it back to the
// operator: not merely the id the claim protocol uses (see Viewer), but the
// name and email that make "signed in as" mean something to a human.
type Identity struct {
	ID    string
	Name  string
	Email string
}

// ViewerIdentity reads the authenticated user's id, name and email. It sits
// beside the other setup-time reads: nothing in the loop calls it, only
// `lerp login` confirming who it just signed in as. Unlike Viewer, this is
// not memoized — login calls it exactly once, against the token it has just
// obtained, and never through Resolve, which would prefer LINEAR_API_KEY and
// report the wrong identity when one is set.
func (c *HTTP) ViewerIdentity(ctx context.Context) (Identity, error) {
	var resp struct {
		Viewer struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"viewer"`
	}
	if err := c.do(ctx, viewerIdentityQuery, nil, &resp); err != nil {
		return Identity{}, fmt.Errorf("viewer identity: %w", err)
	}
	return Identity{ID: resp.Viewer.ID, Name: resp.Viewer.Name, Email: resp.Viewer.Email}, nil
}

const workflowStateCreateMutation = `
mutation WorkflowStateCreate($teamId: String!, $name: String!, $type: String!, $color: String!) {
  workflowStateCreate(input: { teamId: $teamId, name: $name, type: $type, color: $color }) { success }
}`

// defaultStateColor is the color new states are created with. Linear requires
// one; which color a column wears is not lerp's business, and an operator can
// restyle it in Linear afterwards.
const defaultStateColor = "#6b7280"

// StateSpec is a workflow state to ensure: its name, and the Linear state
// category to create it in when it is absent.
//
// The category matters beyond cosmetics. Linear reports an issue as blocking
// its dependents until the blocker's state type is completed or canceled (see
// how Issue.Blocked is derived), so which category a status carries decides
// when dependent tickets become eligible. That judgment belongs to the
// caller; this method only carries it out.
type StateSpec struct {
	Name string
	Type string
}

// EnsureWorkflowStates adds any absent state in states, in its requested
// category, and reports the category of every state the team then has —
// existing states as Linear categorises them, created ones as requested.
// Existing states are left exactly as the operator has them.
func (c *HTTP) EnsureWorkflowStates(ctx context.Context, teamKey string, states []StateSpec) (map[string]string, error) {
	teamID, existing, err := c.teamStates(ctx, teamKey)
	if err != nil {
		return nil, err
	}
	categories := map[string]string{}
	for _, state := range existing {
		categories[state.Name] = state.Type
	}
	states = append([]StateSpec(nil), states...)
	slices.SortFunc(states, func(a, b StateSpec) int { return strings.Compare(a.Name, b.Name) })
	for _, state := range states {
		if _, ok := categories[state.Name]; ok {
			continue
		}
		if state.Type == "" {
			return nil, fmt.Errorf("workflow state %q: no state category requested", state.Name)
		}
		var created struct {
			WorkflowStateCreate struct {
				Success bool `json:"success"`
			} `json:"workflowStateCreate"`
		}
		vars := map[string]any{
			"teamId": teamID,
			"name":   state.Name,
			"type":   state.Type,
			"color":  defaultStateColor,
		}
		if err := c.do(ctx, workflowStateCreateMutation, vars, &created); err != nil {
			return nil, fmt.Errorf("create workflow state %q: %w", state.Name, err)
		}
		if !created.WorkflowStateCreate.Success {
			return nil, fmt.Errorf("linear reported failure creating workflow state %q", state.Name)
		}
		categories[state.Name] = state.Type
	}
	return categories, nil
}
