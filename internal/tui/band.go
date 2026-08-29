package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/linechart"
	"github.com/NimbleMarkets/ntcharts/linechart/timeserieslinechart"
	"github.com/charmbracelet/lipgloss"
)

const (
	// laneChartHeight is the maximum height of the lane pane activity chart:
	// twelve rows of braille give about 48 distinct vertical levels of
	// resolution, a relative time X axis, and a numbered events/min Y axis,
	// with a legend row under it.
	//
	// laneChartMinHeight is where a Y axis stops being able to say anything:
	// below six rows the chart and legend drop together.
	//
	// laneLogMinHeight is the minimum height the log tail gets when a chart is
	// shown beside it.
	laneChartHeight    = 12
	laneChartMinHeight = 6
	laneLogMinHeight   = 8
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

// yCeiling rounds maxVal up to a readable ceiling (1, 2 or 5 times a power of
// ten), with a floor of 1 so an all-quiet window has a non-zero range.
func yCeiling(maxVal float64) float64 {
	if maxVal <= 1 {
		return 1
	}
	p10 := math.Pow(10, math.Floor(math.Log10(maxVal)))
	norm := maxVal / p10
	if norm <= 1.0 {
		return 1.0 * p10
	} else if norm <= 2.0 {
		return 2.0 * p10
	} else if norm <= 5.0 {
		return 5.0 * p10
	}
	return 10.0 * p10
}

// relativeTimeFormatter formats unix seconds as relative time from right
// ("now", "-5m", "-90s"), rounding to the nearest tick step based on the
// horizon.
func relativeTimeFormatter(right time.Time, horizon time.Duration) linechart.LabelFormatter {
	rightSec := float64(right.Unix())
	var step float64
	var formatMins bool
	if horizon >= 2*time.Minute {
		formatMins = true
		if horizon >= 10*time.Minute {
			step = (5 * time.Minute).Seconds()
		} else {
			step = (1 * time.Minute).Seconds()
		}
	} else {
		formatMins = false
		if horizon <= 45*time.Second {
			step = 10
		} else {
			step = 30
		}
	}

	return func(i int, v float64) string {
		ageSec := rightSec - v
		if ageSec < 0 {
			ageSec = 0
		}
		roundedAge := math.Round(ageSec/step) * step
		if roundedAge <= 0 {
			return "now"
		}
		if formatMins {
			mins := int(math.Round(roundedAge / 60))
			return fmt.Sprintf("-%dm", mins)
		}
		secs := int(math.Round(roundedAge))
		return fmt.Sprintf("-%ds", secs)
	}
}

// laneChart renders a braille time series line chart of recent activity
// across the lane pane's width at the given height, with a relative time X axis,
// a numbered events/min Y axis, and a legend row under it.
//
// It is drawn from the dated buckets in r.chart. The horizon is the duration
// the window actually holds, so a run younger than fifteen minutes draws only
// the history it has.
func laneChart(r workRow, width, height int) []string {
	if width <= 0 || height <= 0 || len(r.chart) == 0 {
		return nil
	}

	n := len(r.chart[0].buckets)
	if n == 0 {
		return nil
	}
	right := r.chart[0].buckets[n-1].at
	left := right.Add(-time.Duration(n-1) * pulseBucket)
	if left.Equal(right) {
		left = right.Add(-pulseBucket)
	}

	maxVal := 0.0
	for _, s := range r.chart {
		for _, b := range s.buckets {
			v := float64(b.count) * 60.0 / pulseBucket.Seconds()
			if v > maxVal {
				maxVal = v
			}
		}
	}
	hi := yCeiling(maxVal)

	horizon := right.Sub(left)
	xFormatter := relativeTimeFormatter(right, horizon)
	yFormatter := func(i int, v float64) string {
		return strconv.Itoa(int(math.Round(v)))
	}

	seriesStyles := []lipgloss.Style{
		styleRunning,
		styleFocus,
		styleProvisioning,
		styleAttention,
	}

	opts := []timeserieslinechart.Option{
		timeserieslinechart.WithTimeRange(left, right),
		timeserieslinechart.WithYRange(0, hi),
		timeserieslinechart.WithXYSteps(1, 2),
		timeserieslinechart.WithXLabelFormatter(xFormatter),
		timeserieslinechart.WithYLabelFormatter(yFormatter),
		timeserieslinechart.WithAxesStyles(styleFaint, styleFaint),
	}
	for i, s := range r.chart {
		st := seriesStyles[i%len(seriesStyles)]
		opts = append(opts, timeserieslinechart.WithDataSetStyle(s.key, st))
	}

	chart := timeserieslinechart.New(width, height, opts...)
	chart.SetViewTimeAndYRange(left, right, 0, hi)

	for _, s := range r.chart {
		for _, b := range s.buckets {
			val := float64(b.count) * 60.0 / pulseBucket.Seconds()
			chart.PushDataSet(s.key, timeserieslinechart.TimePoint{
				Time:  b.at,
				Value: val,
			})
		}
	}

	chart.DrawBrailleAll()

	lines := strings.Split(chart.View(), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}

	// Legend row: events/min · ▇ main [· ▇ subagent ...]
	nameCounts := make(map[string]int, len(r.chart))
	for _, s := range r.chart {
		nameCounts[s.name]++
	}
	nameSeen := make(map[string]int, len(r.chart))
	displayNames := make([]string, len(r.chart))
	for i, s := range r.chart {
		nameSeen[s.name]++
		if nameCounts[s.name] > 1 && nameSeen[s.name] > 1 {
			displayNames[i] = fmt.Sprintf("%s %d", s.name, nameSeen[s.name])
		} else {
			displayNames[i] = s.name
		}
	}

	type legendItem struct {
		rendered string
	}
	items := make([]legendItem, len(r.chart))
	for i := range r.chart {
		st := seriesStyles[i%len(seriesStyles)]
		items[i] = legendItem{
			rendered: st.Render("▇") + " " + styleFaint.Render(displayNames[i]),
		}
	}

	var legend string
	sep := styleFaint.Render(" · ")
	prefix := styleFaint.Render("events/min")
	for k := len(items); k >= 1; k-- {
		parts := make([]string, 1+k)
		parts[0] = prefix
		for i := 0; i < k; i++ {
			parts[1+i] = items[i].rendered
		}
		cand := strings.Join(parts, sep)
		if lipgloss.Width(cand) <= width || k == 1 {
			legend = cand
			break
		}
	}

	lines = append(lines, padTo(legend, width))
	return lines
}
