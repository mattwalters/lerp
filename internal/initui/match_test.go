package initui

import (
	"reflect"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

func TestDefaultMapping(t *testing.T) {
	stock := config.Stock{Plan: true, Review: true}
	slots := PipelineSlots(&stock)

	tests := []struct {
		name     string
		slots    []Slot
		existing []linear.WorkflowState
		want     []string
	}{
		{
			name:  "Linear default board adopts In Progress and In Review and creates other three",
			slots: slots,
			existing: []linear.WorkflowState{
				{Name: "Backlog", Category: "backlog"},
				{Name: "Todo", Category: "unstarted"},
				{Name: "In Progress", Category: "started"},
				{Name: "In Review", Category: "started"},
				{Name: "Done", Category: "completed"},
			},
			want: []string{"", "", "In Progress", "In Review", ""},
		},
		{
			name:  "Board holding stock names adopts all five",
			slots: slots,
			existing: []linear.WorkflowState{
				{Name: "Planning", Category: "started"},
				{Name: "Plan Review", Category: "started"},
				{Name: "Implementing", Category: "started"},
				{Name: "In Review", Category: "started"},
				{Name: "Needs Attention", Category: "started"},
			},
			want: []string{"Planning", "Plan Review", "Implementing", "In Review", "Needs Attention"},
		},
		{
			name:     "Empty board creates all five",
			slots:    slots,
			existing: nil,
			want:     []string{"", "", "", "", ""},
		},
		{
			name:  "Case and spacing folding matches and preserves actual names",
			slots: slots,
			existing: []linear.WorkflowState{
				{Name: "  in progress  ", Category: "started"},
				{Name: "In   Review", Category: "started"},
			},
			want: []string{"", "", "  in progress  ", "In   Review", ""},
		},
		{
			name: "Greedy uniqueness gives shared candidate to earlier slot and sends later to next alias or create",
			slots: []Slot{
				{Label: "implement runs in", Stock: "Implementing"},
				{Label: "implement runs in", Stock: "Implementing"},
			},
			existing: []linear.WorkflowState{
				{Name: "In Dev", Category: "started"},
				{Name: "Doing", Category: "started"},
			},
			want: []string{"In Dev", "Doing"},
		},
		{
			name: "Greedy uniqueness sends later slot to create when no other alias matches",
			slots: []Slot{
				{Label: "implement runs in", Stock: "Implementing"},
				{Label: "implement runs in", Stock: "Implementing"},
			},
			existing: []linear.WorkflowState{
				{Name: "In Dev", Category: "started"},
			},
			want: []string{"In Dev", ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := defaultMapping(tc.slots, tc.existing)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("defaultMapping() = %v, want %v", got, tc.want)
			}
		})
	}
}
