package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

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

// relativeTimeLabel formats an exact age from right ("now", "-30s", "-1m", "-5m")
// based on the horizon rung.
func relativeTimeLabel(age, horizon time.Duration) string {
	if age <= 0 {
		return "now"
	}
	if horizon <= time.Minute {
		secs := int(math.Round(age.Seconds()))
		return fmt.Sprintf("-%ds", secs)
	}
	mins := int(math.Round(age.Minutes()))
	return fmt.Sprintf("-%dm", mins)
}

// laneChartXLabelRow constructs the bottom X label row for laneChart.
// Geometry comes from the chart's origin and graph width. "now" is right-aligned
// to the graph's last column, and earlier ticks step back by the rung step.
// Ticks that would collide with or touch a label to their right are dropped.
func laneChartXLabelRow(width, originX, graphWidth int, left, right time.Time, horizon, step time.Duration) string {
	if width <= 0 {
		return ""
	}
	row := make([]rune, width)
	for i := range row {
		row[i] = ' '
	}

	// If the plot is narrower than "now" itself, the row stays blank.
	if graphWidth < len("now") {
		return padTo(styleFaint.Render(string(row)), width)
	}

	plotStart := originX + 1
	plotEnd := originX + graphWidth

	nowStr := "now"
	nowStart := plotEnd - len(nowStr) + 1
	copy(row[nowStart:], []rune(nowStr))
	leftmostOccupied := nowStart

	for t := right.Add(-step); !t.Before(left); t = t.Add(-step) {
		age := right.Sub(t)
		label := relativeTimeLabel(age, horizon)
		labelRunes := []rune(label)
		labelLen := len(labelRunes)

		tOffset := t.Sub(left)
		colOffset := int(math.Round(float64(tOffset) / float64(horizon) * float64(graphWidth-1)))
		tickCol := plotStart + colOffset

		if tickCol < plotStart {
			continue
		}
		if tickCol+labelLen >= leftmostOccupied {
			continue
		}

		copy(row[tickCol:tickCol+labelLen], labelRunes)
		leftmostOccupied = tickCol
	}

	return padTo(styleFaint.Render(string(row)), width)
}

// laneChart renders a braille time series line chart of recent activity
// across the lane pane's width at the given height, with a relative time X axis,
// a numbered events/min Y axis, and a legend row under it.
//
// It is drawn from the dated buckets in r.chart. The horizon snaps up a ladder
// (1m, 5m, 15m) to the smallest rung holding the ring's history.
func laneChart(r workRow, width, height int) []string {
	if width <= 0 || height <= 0 || len(r.chart) == 0 {
		return nil
	}

	n := len(r.chart)
	right := r.chart[n-1].at
	span := time.Duration(n-1) * pulseBucket

	var horizon time.Duration
	var step time.Duration
	switch {
	case span <= time.Minute:
		horizon = time.Minute
		step = 30 * time.Second
	case span <= 5*time.Minute:
		horizon = 5 * time.Minute
		step = time.Minute
	default:
		horizon = 15 * time.Minute
		step = 5 * time.Minute
	}
	left := right.Add(-horizon)

	maxVal := 0.0
	for _, b := range r.chart {
		if b.at.Before(left) || b.at.After(right) {
			continue
		}
		v := float64(b.count) * 60.0 / pulseBucket.Seconds()
		if v > maxVal {
			maxVal = v
		}
	}
	hi := yCeiling(maxVal)

	xFormatter := func(i int, v float64) string {
		return ""
	}
	yFormatter := func(i int, v float64) string {
		return strconv.Itoa(int(math.Round(v)))
	}

	chart := timeserieslinechart.New(
		width,
		height,
		timeserieslinechart.WithTimeRange(left, right),
		timeserieslinechart.WithYRange(0, hi),
		timeserieslinechart.WithXYSteps(1, 2),
		timeserieslinechart.WithXLabelFormatter(xFormatter),
		timeserieslinechart.WithYLabelFormatter(yFormatter),
		timeserieslinechart.WithAxesStyles(styleFaint, styleFaint),
		timeserieslinechart.WithDataSetStyle("main", styleRunning),
	)
	chart.SetViewTimeAndYRange(left, right, 0, hi)

	for _, b := range r.chart {
		if b.at.Before(left) || b.at.After(right) {
			continue
		}
		val := float64(b.count) * 60.0 / pulseBucket.Seconds()
		chart.PushDataSet("main", timeserieslinechart.TimePoint{
			Time:  b.at,
			Value: val,
		})
	}

	chart.DrawBrailleAll()

	lines := strings.Split(chart.View(), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	if len(lines) == height {
		lines[height-1] = laneChartXLabelRow(width, chart.Origin().X, chart.GraphWidth(), left, right, horizon, step)
	}

	// Legend row: events/min · ▇ main
	series := []struct {
		name  string
		style lipgloss.Style
	}{
		{"main", styleRunning},
	}
	var legendParts []string
	legendParts = append(legendParts, styleFaint.Render("events/min"))
	for _, s := range series {
		legendParts = append(legendParts, s.style.Render("▇")+" "+styleFaint.Render(s.name))
	}
	legend := strings.Join(legendParts, styleFaint.Render(" · "))

	lines = append(lines, padTo(legend, width))
	return lines
}
