package linear

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestTeamsReportsAllTeamsInKeyOrder(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if !strings.Contains(req.Query, "Teams") {
			t.Errorf("unexpected query: %s", req.Query)
		}
		if !strings.Contains(req.Query, "teams(first: 250)") {
			t.Errorf("query does not request 250 teams: %s", req.Query)
		}
		writeData(t, w, `{"teams":{"nodes":[
			{"key":"LERP","name":"Lerp"},
			{"key":"ACEM","name":"Acme Marketing"},
			{"key":"ENG","name":"Engineering"}
		]}}`)
	})
	teams, err := c.Teams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []TeamRef{
		{Key: "ACEM", Name: "Acme Marketing"},
		{Key: "ENG", Name: "Engineering"},
		{Key: "LERP", Name: "Lerp"},
	}
	if !reflect.DeepEqual(teams, want) {
		t.Errorf("teams = %+v, want %+v", teams, want)
	}
}

func TestEnsureTeamCreatesOnlyWhenMissing(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		req := decodeRequest(t, r)
		switch {
		case strings.Contains(req.Query, "TeamByKey"):
			if req.Variables["key"] != "LERP" {
				t.Errorf("key = %v", req.Variables["key"])
			}
			writeData(t, w, `{"teams":{"nodes":[]}}`)
		case strings.Contains(req.Query, "TeamCreate"):
			if req.Variables["key"] != "LERP" || req.Variables["name"] != "Lerp" {
				t.Errorf("variables = %v", req.Variables)
			}
			writeData(t, w, `{"teamCreate":{"success":true}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
	})
	if err := c.EnsureTeam(context.Background(), "LERP", "Lerp"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestEnsureTeamDoesNotCreateExistingTeam(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		req := decodeRequest(t, r)
		if !strings.Contains(req.Query, "TeamByKey") {
			t.Errorf("unexpected query: %s", req.Query)
		}
		writeData(t, w, `{"teams":{"nodes":[{"id":"team-1"}]}}`)
	})
	if err := c.EnsureTeam(context.Background(), "LERP", "Lerp"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want only the team lookup", calls)
	}
}

func TestTeamStatesReportsNamesInBoardOrder(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if !strings.Contains(req.Query, "TeamStates") {
			t.Errorf("unexpected query: %s", req.Query)
		}
		if !strings.Contains(req.Query, "position") {
			t.Errorf("query does not request position: %s", req.Query)
		}
		if req.Variables["key"] != "LERP" {
			t.Errorf("key = %v", req.Variables["key"])
		}
		// States delivered in creation order (out of board order) across multiple categories and positions.
		writeData(t, w, `{"teams":{"nodes":[{"id":"team-1","states":{"nodes":[
			{"name":"Done","type":"completed","position":10},
			{"name":"Plan Review","type":"unstarted","position":20},
			{"name":"Backlog","type":"backlog","position":5},
			{"name":"Implementing","type":"started","position":10},
			{"name":"Planning","type":"unstarted","position":10},
			{"name":"In Review","type":"started","position":20},
			{"name":"Canceled","type":"canceled","position":10},
			{"name":"Triage","type":"triage","position":1}
		]}}]}}`)
	})
	names, err := c.TeamStates(context.Background(), "LERP")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Triage",
		"Backlog",
		"Planning",
		"Plan Review",
		"Implementing",
		"In Review",
		"Done",
		"Canceled",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestTeamStatesPreservesOrderOnEqualPositionAndSortsUnknownTypesLast(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(t, w, `{"teams":{"nodes":[{"id":"team-1","states":{"nodes":[
			{"name":"Custom","type":"custom_type","position":0},
			{"name":"Alpha","type":"started","position":10},
			{"name":"Beta","type":"started","position":10}
		]}}]}}`)
	})
	names, err := c.TeamStates(context.Background(), "LERP")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Alpha", "Beta", "Custom"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestTeamWorkflowStatesReportsStatesInBoardOrder(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if !strings.Contains(req.Query, "TeamStates") {
			t.Errorf("unexpected query: %s", req.Query)
		}
		if !strings.Contains(req.Query, "position") {
			t.Errorf("query does not request position: %s", req.Query)
		}
		if req.Variables["key"] != "LERP" {
			t.Errorf("key = %v", req.Variables["key"])
		}
		// States delivered in creation order (out of board order) across multiple categories and positions.
		writeData(t, w, `{"teams":{"nodes":[{"id":"team-1","states":{"nodes":[
			{"name":"Done","type":"completed","position":10},
			{"name":"Plan Review","type":"unstarted","position":20},
			{"name":"Backlog","type":"backlog","position":5},
			{"name":"Implementing","type":"started","position":10},
			{"name":"Planning","type":"unstarted","position":10},
			{"name":"In Review","type":"started","position":20},
			{"name":"Canceled","type":"canceled","position":10},
			{"name":"Triage","type":"triage","position":1}
		]}}]}}`)
	})
	states, err := c.TeamWorkflowStates(context.Background(), "LERP")
	if err != nil {
		t.Fatal(err)
	}
	want := []WorkflowState{
		{Name: "Triage", Category: "triage", Position: 1},
		{Name: "Backlog", Category: "backlog", Position: 5},
		{Name: "Planning", Category: "unstarted", Position: 10},
		{Name: "Plan Review", Category: "unstarted", Position: 20},
		{Name: "Implementing", Category: "started", Position: 10},
		{Name: "In Review", Category: "started", Position: 20},
		{Name: "Done", Category: "completed", Position: 10},
		{Name: "Canceled", Category: "canceled", Position: 10},
	}
	if !reflect.DeepEqual(states, want) {
		t.Errorf("states = %+v, want %+v", states, want)
	}
}

func TestTeamWorkflowStatesReportsUnknownTeam(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(t, w, `{"teams":{"nodes":[]}}`)
	})
	_, err := c.TeamWorkflowStates(context.Background(), "NOPE")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestTeamStatesReportsUnknownTeam(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(t, w, `{"teams":{"nodes":[]}}`)
	})
	_, err := c.TeamStates(context.Background(), "NOPE")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestEnsureWorkflowStatesCreatesAbsentStates(t *testing.T) {
	var created []string
	types := map[string]any{}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		switch {
		case strings.Contains(req.Query, "TeamStates"):
			writeData(t, w, `{"teams":{"nodes":[{"id":"team-1","states":{"nodes":[{"name":"Planning","type":"unstarted"}]}}]}}`)
		case strings.Contains(req.Query, "WorkflowStateCreate"):
			if req.Variables["teamId"] != "team-1" {
				t.Errorf("teamId = %v", req.Variables["teamId"])
			}
			// Linear requires a color, and takes the category as a string;
			// sending neither is a schema error the server would reject.
			if req.Variables["color"] == nil || req.Variables["color"] == "" {
				t.Errorf("color = %v, want a color", req.Variables["color"])
			}
			if _, ok := req.Variables["type"].(string); !ok {
				t.Errorf("type = %v, want a string", req.Variables["type"])
			}
			name := req.Variables["name"].(string)
			created = append(created, name)
			types[name] = req.Variables["type"]
			writeData(t, w, `{"workflowStateCreate":{"success":true}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
	})
	states := []StateSpec{
		{Name: "Review", Type: "completed"},
		{Name: "Planning", Type: "started"},
		{Name: "Implementing", Type: "started"},
	}
	categories, err := c.EnsureWorkflowStates(context.Background(), "LERP", states)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Implementing", "Review"}; !reflect.DeepEqual(created, want) {
		t.Errorf("created = %v, want %v", created, want)
	}
	if want := map[string]any{"Implementing": "started", "Review": "completed"}; !reflect.DeepEqual(types, want) {
		t.Errorf("types = %v, want %v", types, want)
	}
	// The report carries the existing state's category as Linear has it, and
	// created states in their requested category.
	want := map[string]string{"Planning": "unstarted", "Implementing": "started", "Review": "completed"}
	if !reflect.DeepEqual(categories, want) {
		t.Errorf("categories = %v, want %v", categories, want)
	}
}

func TestEnsureWorkflowStatesRejectsStateWithoutCategory(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if !strings.Contains(req.Query, "TeamStates") {
			t.Errorf("state was created despite a missing category: %s", req.Query)
		}
		writeData(t, w, `{"teams":{"nodes":[{"id":"team-1","states":{"nodes":[]}}]}}`)
	})
	_, err := c.EnsureWorkflowStates(context.Background(), "LERP", []StateSpec{{Name: "Shipped"}})
	if err == nil || !strings.Contains(err.Error(), "no state category") {
		t.Fatalf("error = %v, want a missing-category error", err)
	}
}

// ViewerIdentity decodes id, name and email, and leaves Viewer's memoization
// alone — a second Viewer call must not be answered from this one's cache.
func TestViewerIdentityDecodesNameAndEmail(t *testing.T) {
	var viewerCalls, identityCalls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		switch {
		case strings.Contains(req.Query, "ViewerIdentity"):
			identityCalls++
			writeData(t, w, `{"viewer":{"id":"user-1","name":"Ada Lovelace","email":"ada@example.com"}}`)
		case strings.Contains(req.Query, "Viewer"):
			viewerCalls++
			writeData(t, w, `{"viewer":{"id":"user-1"}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
	})
	got, err := c.ViewerIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{ID: "user-1", Name: "Ada Lovelace", Email: "ada@example.com"}
	if got != want {
		t.Errorf("identity = %+v, want %+v", got, want)
	}
	if identityCalls != 1 {
		t.Errorf("identityCalls = %d, want 1", identityCalls)
	}

	if _, err := c.Viewer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if viewerCalls != 1 {
		t.Errorf("viewerCalls = %d, want 1 — ViewerIdentity must not have satisfied Viewer's cache", viewerCalls)
	}
}

func TestEnsureWorkflowStatesDoesNotCreateExistingStates(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		req := decodeRequest(t, r)
		if !strings.Contains(req.Query, "TeamStates") {
			t.Errorf("unexpected query: %s", req.Query)
		}
		writeData(t, w, `{"teams":{"nodes":[{"id":"team-1","states":{"nodes":[{"name":"Planning","type":"started"},{"name":"Implementing","type":"started"},{"name":"Review","type":"unstarted"}]}}]}}`)
	})
	states := []StateSpec{
		{Name: "Review", Type: "completed"},
		{Name: "Planning", Type: "started"},
		{Name: "Implementing", Type: "started"},
	}
	categories, err := c.EnsureWorkflowStates(context.Background(), "LERP", states)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want only the states lookup", calls)
	}
	// Existing states keep the category the operator gave them — the report
	// says what Linear has, not what the spec asked for.
	want := map[string]string{"Planning": "started", "Implementing": "started", "Review": "unstarted"}
	if !reflect.DeepEqual(categories, want) {
		t.Errorf("categories = %v, want %v", categories, want)
	}
}
