package initui

import (
	"strings"

	"github.com/mattwalters/lerp/internal/linear"
)

var acceptedSlotNames = map[string][]string{
	"plan runs in": {
		"Planning", "Plan", "Design", "Scoping",
	},
	"plans wait for approval in": {
		"Plan Review", "Plan Approval", "Design Review", "Ready for Dev",
	},
	"implement runs in": {
		"Implementing", "In Progress", "In Development", "In Dev", "Doing", "WIP",
	},
	"finished work exits to": {
		"In Review", "Code Review", "Review", "PR Review", "Ready for Review",
	},
	"failures exit to": {
		"Needs Attention", "Blocked", "Needs Help", "Stuck", "On Hold",
	},
}

func foldName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func defaultMapping(slots []Slot, existing []linear.WorkflowState) []string {
	result := make([]string, len(slots))
	taken := make(map[int]bool)

	for i, sl := range slots {
		candidates := acceptedSlotNames[sl.Label]
		if len(candidates) == 0 && sl.Stock != "" {
			candidates = []string{sl.Stock}
		}

		matchedName := ""
		for _, cand := range candidates {
			foldedCand := foldName(cand)
			foundIdx := -1
			for eIdx, st := range existing {
				if taken[eIdx] {
					continue
				}
				if foldName(st.Name) == foldedCand {
					foundIdx = eIdx
					break
				}
			}
			if foundIdx >= 0 {
				taken[foundIdx] = true
				matchedName = existing[foundIdx].Name
				break
			}
		}

		result[i] = matchedName
	}

	return result
}
