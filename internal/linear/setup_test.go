package linear

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

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

func TestEnsureWorkflowStatesCreatesAbsentStates(t *testing.T) {
	var created []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		switch {
		case strings.Contains(req.Query, "TeamStates"):
			writeData(t, w, `{"teams":{"nodes":[{"id":"team-1","states":{"nodes":[{"name":"Planning"}]}}]}}`)
		case strings.Contains(req.Query, "WorkflowStateCreate"):
			if req.Variables["teamId"] != "team-1" {
				t.Errorf("teamId = %v", req.Variables["teamId"])
			}
			created = append(created, req.Variables["name"].(string))
			writeData(t, w, `{"workflowStateCreate":{"success":true}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
	})
	if err := c.EnsureWorkflowStates(context.Background(), "LERP", []string{"Review", "Planning", "Implementing"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Implementing", "Review"}; !reflect.DeepEqual(created, want) {
		t.Errorf("created = %v, want %v", created, want)
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
		writeData(t, w, `{"teams":{"nodes":[{"id":"team-1","states":{"nodes":[{"name":"Planning"},{"name":"Implementing"},{"name":"Review"}]}}]}}`)
	})
	if err := c.EnsureWorkflowStates(context.Background(), "LERP", []string{"Review", "Planning", "Implementing"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want only the states lookup", calls)
	}
}
