package initui

import (
	"fmt"
	"strings"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/theme"
)

// Diagram renders a pipeline diagram for the given Stock choices.
// It is a pure function of config.Stock, redrawn on every toggle.
func Diagram(s config.Stock) string {
	plan := orStock(s.PlanStatus, config.StockPlanStatus)
	planRev := orStock(s.PlanReviewStatus, config.StockPlanReviewStatus)
	impl := orStock(s.ImplementStatus, config.StockImplementStatus)
	exit := orStock(s.ExitStatus, config.StockExitStatus)
	attn := orStock(s.AttentionStatus, config.StockAttentionStatus)

	var b strings.Builder

	if s.Plan {
		// Full or plan-only pipeline
		b.WriteString(theme.Faint.Render("  ┌─") + theme.Ticket.Render(fmt.Sprintf(" %s ", plan)) + theme.Faint.Render("─┐     ┌─") + theme.Ticket.Render(fmt.Sprintf(" %s ", planRev)) + theme.Faint.Render("─┐     ┌─") + theme.Ticket.Render(fmt.Sprintf(" %s ", impl)) + theme.Faint.Render("─┐     ┌─") + theme.Ticket.Render(fmt.Sprintf(" %s ", exit)) + theme.Faint.Render("─┐\n"))
		if s.Review {
			b.WriteString(theme.Faint.Render("  │  queue   │ ──> │   gate    │ ──> │ queue (review) │ ──> │   exit    │\n"))
		} else {
			b.WriteString(theme.Faint.Render("  │  queue   │ ──> │   gate    │ ──> │     queue      │ ──> │   exit    │\n"))
		}
		b.WriteString(theme.Faint.Render("  └──────────┘     └───────────┘     └───────┬────────┘     └───────────┘\n"))
		b.WriteString(theme.Faint.Render("                                             │ (failure)\n"))
		b.WriteString(theme.Faint.Render("                                             └──> ") + theme.Ticket.Render(attn) + theme.Faint.Render(" (exit)\n"))
	} else {
		// No planning stage
		b.WriteString(theme.Faint.Render("  ┌─") + theme.Ticket.Render(fmt.Sprintf(" %s ", impl)) + theme.Faint.Render("─┐     ┌─") + theme.Ticket.Render(fmt.Sprintf(" %s ", exit)) + theme.Faint.Render("─┐\n"))
		if s.Review {
			b.WriteString(theme.Faint.Render("  │ queue (review) │ ──> │   exit    │\n"))
		} else {
			b.WriteString(theme.Faint.Render("  │     queue      │ ──> │   exit    │\n"))
		}
		b.WriteString(theme.Faint.Render("  └───────┬────────┘     └───────────┘\n"))
		b.WriteString(theme.Faint.Render("          │ (failure)\n"))
		b.WriteString(theme.Faint.Render("          └──> ") + theme.Ticket.Render(attn) + theme.Faint.Render(" (exit)\n"))
	}

	return b.String()
}

func orStock(name, stock string) string {
	if name == "" {
		return stock
	}
	return name
}
