package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// laneBand renders the pinned header band over a running ticket's log:
// identity on the left (lane, runner, model, effort) and figures on the right
// (clock, tokens, spend, context load).
//
// Precedence: the log's model (r.model) beats config's (r.runner.Model)
// because config is what we asked for and the log is what the runner did. A
// command runner has no vendor, model or effort and prints no placeholder.
//
// On a narrow pane, figures drop from the right in fixed order (context, then
// cost, then tokens; the clock never drops) to keep identity in view; identity
// truncates only once the figures are down to the clock alone.
func laneBand(r workRow, width int) []string {
	if width <= 0 {
		return nil
	}

	// Identity on the left: lane number, runner name/vendor, model, effort.
	var idParts []string
	if r.lane > 0 {
		idParts = append(idParts, "lane "+strconv.Itoa(r.lane))
	}
	var runnerStr string
	if r.runner.Name != "" && r.runner.Vendor != "" {
		runnerStr = fmt.Sprintf("%s (%s)", r.runner.Name, r.runner.Vendor)
	} else if r.runner.Name != "" {
		runnerStr = r.runner.Name
	} else if r.runner.Vendor != "" {
		runnerStr = r.runner.Vendor
	}
	if runnerStr != "" {
		idParts = append(idParts, runnerStr)
	}
	model := r.model
	if model == "" {
		model = r.runner.Model
	}
	if model != "" {
		idParts = append(idParts, model)
	}
	if r.runner.Effort != "" {
		idParts = append(idParts, r.runner.Effort)
	}
	left := styleFaint.Render(strings.Join(idParts, " · "))

	// Figures on the right: clock, tokens, cost, context load.
	// Clock is always present and never dropped.
	clock := styleFaint.Render(elapsed(r.since))

	type figItem struct {
		rendered string
	}
	var optFigs []figItem
	if r.tokens > 0 {
		optFigs = append(optFigs, figItem{rendered: styleFaint.Render(tokenCount(r.tokens))})
	}
	if r.cost >= minCost {
		optFigs = append(optFigs, figItem{rendered: styleFaint.Render(costLabel(r.cost))})
	}
	if r.context > 0 {
		rawCtx := "ctx " + strings.TrimSuffix(tokenCount(r.context), " tok")
		if load, ok := contextLoad(r.context, r.window); ok {
			optFigs = append(optFigs, figItem{rendered: styleFaint.Render(rawCtx+" · ") + load})
		} else {
			optFigs = append(optFigs, figItem{rendered: styleFaint.Render(rawCtx)})
		}
	}

	joinFigures := func(count int) string {
		parts := []string{clock}
		for i := 0; i < count; i++ {
			parts = append(parts, optFigs[i].rendered)
		}
		return strings.Join(parts, styleFaint.Render(" · "))
	}

	leftW := lipgloss.Width(left)
	for count := len(optFigs); count >= 0; count-- {
		right := joinFigures(count)
		rightW := lipgloss.Width(right)
		if count > 0 {
			if leftW+1+rightW <= width {
				leftMax := width - rightW - 1
				return []string{padTo(left, leftMax) + " " + right}
			}
		} else {
			if leftW+1+rightW <= width {
				leftMax := width - rightW - 1
				return []string{padTo(left, leftMax) + " " + right}
			}
			return []string{splitRow(left, right, width)}
		}
	}

	return []string{splitRow(left, clock, width)}
}
