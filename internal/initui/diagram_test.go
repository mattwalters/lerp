package initui

import (
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
)

func TestDiagramAllFourCombinations(t *testing.T) {
	cases := []struct {
		name      string
		stock     config.Stock
		wantIn    []string
		wantNotIn []string
	}{
		{
			name: "plan and review",
			stock: config.Stock{
				Plan:   true,
				Review: true,
			},
			wantIn: []string{
				"Planning",
				"Plan Review",
				"Implementing",
				"In Review",
				"Needs Attention",
				"queue (review)",
				"gate",
			},
			wantNotIn: nil,
		},
		{
			name: "plan and no review",
			stock: config.Stock{
				Plan:   true,
				Review: false,
			},
			wantIn: []string{
				"Planning",
				"Plan Review",
				"Implementing",
				"In Review",
				"Needs Attention",
				"gate",
			},
			wantNotIn: []string{
				"queue (review)",
			},
		},
		{
			name: "no plan and review",
			stock: config.Stock{
				Plan:   false,
				Review: true,
			},
			wantIn: []string{
				"Implementing",
				"In Review",
				"Needs Attention",
				"queue (review)",
			},
			wantNotIn: []string{
				"Planning",
				"Plan Review",
			},
		},
		{
			name: "no plan and no review",
			stock: config.Stock{
				Plan:   false,
				Review: false,
			},
			wantIn: []string{
				"Implementing",
				"In Review",
				"Needs Attention",
			},
			wantNotIn: []string{
				"Planning",
				"Plan Review",
				"queue (review)",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diagram(tc.stock)
			for _, w := range tc.wantIn {
				if !strings.Contains(got, w) {
					t.Errorf("Diagram missing expected %q in output:\n%s", w, got)
				}
			}
			for _, nw := range tc.wantNotIn {
				if strings.Contains(got, nw) {
					t.Errorf("Diagram contains unexpected %q in output:\n%s", nw, got)
				}
			}
		})
	}
}

func TestDiagramCustomStatusNames(t *testing.T) {
	s := config.Stock{
		Plan:             true,
		Review:           true,
		PlanStatus:       "Design",
		PlanReviewStatus: "Design Review",
		ImplementStatus:  "Coding",
		ExitStatus:       "Deployed",
		AttentionStatus:  "Blocked",
	}
	got := Diagram(s)
	for _, w := range []string{"Design", "Design Review", "Coding", "Deployed", "Blocked"} {
		if !strings.Contains(got, w) {
			t.Errorf("Diagram missing custom status %q:\n%s", w, got)
		}
	}
}
