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
  teams(filter: { key: { eq: $key } }, first: 1) { nodes { id states(first: 100) { nodes { name } } } }
}`

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
// how Issue.Blocked is derived), so a status that ends work must be created as
// a completed category. Created as "started", it would leave every dependent
// ticket permanently ineligible.
type StateSpec struct {
	Name string
	Type string
}

// EnsureWorkflowStates adds any absent state in states, in its requested
// category. Existing states are left exactly as the operator has them.
func (c *HTTP) EnsureWorkflowStates(ctx context.Context, teamKey string, states []StateSpec) error {
	var found struct {
		Teams struct {
			Nodes []struct {
				ID     string `json:"id"`
				States struct {
					Nodes []struct {
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"states"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := c.do(ctx, teamStatesByKeyQuery, map[string]any{"key": teamKey}, &found); err != nil {
		return err
	}
	if len(found.Teams.Nodes) == 0 {
		return fmt.Errorf("team %q: %w", teamKey, ErrNotFound)
	}
	team := found.Teams.Nodes[0]
	existing := map[string]bool{}
	for _, state := range team.States.Nodes {
		existing[state.Name] = true
	}
	states = append([]StateSpec(nil), states...)
	slices.SortFunc(states, func(a, b StateSpec) int { return strings.Compare(a.Name, b.Name) })
	for _, state := range states {
		if existing[state.Name] {
			continue
		}
		if state.Type == "" {
			return fmt.Errorf("workflow state %q: no state category requested", state.Name)
		}
		var created struct {
			WorkflowStateCreate struct {
				Success bool `json:"success"`
			} `json:"workflowStateCreate"`
		}
		vars := map[string]any{
			"teamId": team.ID,
			"name":   state.Name,
			"type":   state.Type,
			"color":  defaultStateColor,
		}
		if err := c.do(ctx, workflowStateCreateMutation, vars, &created); err != nil {
			return fmt.Errorf("create workflow state %q: %w", state.Name, err)
		}
		if !created.WorkflowStateCreate.Success {
			return fmt.Errorf("linear reported failure creating workflow state %q", state.Name)
		}
	}
	return nil
}
