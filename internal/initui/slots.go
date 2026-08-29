package initui

import "github.com/mattwalters/lerp/internal/config"

// Slot is one status referenced by the chosen pipeline.
type Slot struct {
	Label string
	Stock string
	Get   func(s *config.Stock) string
	Set   func(s *config.Stock, val string)
}

// PipelineSlots lists the statuses s's stages reference, in pipeline order.
func PipelineSlots(s *config.Stock) []Slot {
	var slots []Slot
	if s.Plan {
		slots = append(slots,
			Slot{
				Label: "plan runs in",
				Stock: config.StockPlanStatus,
				Get:   func(s *config.Stock) string { return s.PlanStatus },
				Set:   func(s *config.Stock, val string) { s.PlanStatus = val },
			},
			Slot{
				Label: "plans wait for approval in",
				Stock: config.StockPlanReviewStatus,
				Get:   func(s *config.Stock) string { return s.PlanReviewStatus },
				Set:   func(s *config.Stock, val string) { s.PlanReviewStatus = val },
			},
		)
	}
	slots = append(slots,
		Slot{
			Label: "implement runs in",
			Stock: config.StockImplementStatus,
			Get:   func(s *config.Stock) string { return s.ImplementStatus },
			Set:   func(s *config.Stock, val string) { s.ImplementStatus = val },
		},
		Slot{
			Label: "finished work exits to",
			Stock: config.StockExitStatus,
			Get:   func(s *config.Stock) string { return s.ExitStatus },
			Set:   func(s *config.Stock, val string) { s.ExitStatus = val },
		},
		Slot{
			Label: "failures exit to",
			Stock: config.StockAttentionStatus,
			Get:   func(s *config.Stock) string { return s.AttentionStatus },
			Set:   func(s *config.Stock, val string) { s.AttentionStatus = val },
		},
	)
	return slots
}
