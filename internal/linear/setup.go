package linear

import (
	"context"
	"fmt"
	"slices"
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
		return fmt.Errorf("Linear reported failure creating team")
	}
	return nil
}

const teamStatesByKeyQuery = `
query TeamStates($key: String!) {
  teams(filter: { key: { eq: $key } }, first: 1) { nodes { id states(first: 100) { nodes { name } } } }
}`

const workflowStateCreateMutation = `
mutation WorkflowStateCreate($teamId: String!, $name: String!) {
  workflowStateCreate(input: { teamId: $teamId, name: $name, type: started }) { success }
}`

// EnsureWorkflowStates adds absent state names as active ("started") states.
// Their category is intentionally not inferred from queue topology: the
// config names statuses but does not encode a workflow language.
func (c *HTTP) EnsureWorkflowStates(ctx context.Context, teamKey string, names []string) error {
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
	names = append([]string(nil), names...)
	slices.Sort(names)
	for _, name := range names {
		if existing[name] {
			continue
		}
		var created struct {
			WorkflowStateCreate struct {
				Success bool `json:"success"`
			} `json:"workflowStateCreate"`
		}
		if err := c.do(ctx, workflowStateCreateMutation, map[string]any{"teamId": team.ID, "name": name}, &created); err != nil {
			return fmt.Errorf("create workflow state %q: %w", name, err)
		}
		if !created.WorkflowStateCreate.Success {
			return fmt.Errorf("Linear reported failure creating workflow state %q", name)
		}
	}
	return nil
}
