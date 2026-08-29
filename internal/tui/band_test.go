package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/NimbleMarkets/ntcharts/canvas/runes"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattwalters/lerp/internal/config"
)

// A command runner has no vendor, model or effort: only its name appears,
// with no parens and no trailing separators.
func TestLaneBandCommandRunner(t *testing.T) {
	r := workRow{
		lane:   1,
		runner: config.RunnerIdentity{Name: "agent"},
		since:  time.Now(),
	}
	lines := laneBand(r, 80)
	if len(lines) != 1 {
		t.Fatalf("laneBand returned %d lines, want 1", len(lines))
	}
	got := lines[0]
	if !strings.Contains(got, "lane 1 · agent") {
		t.Errorf("laneBand = %q, want it to contain %q", got, "lane 1 · agent")
	}
	if strings.Contains(got, "(") || strings.Contains(got, ")") {
		t.Errorf("laneBand = %q, want no vendor parens", got)
	}
	if strings.Contains(got, "··") || strings.HasSuffix(strings.TrimSpace(got), "·") {
		t.Errorf("laneBand = %q has stray separators", got)
	}
}

// The log's model (r.model) beats config's (r.runner.Model): config is what
// was asked for; the log is what the runner actually ran.
func TestLaneBandLogModelBeatsConfig(t *testing.T) {
	r := workRow{
		lane:   2,
		runner: config.RunnerIdentity{Name: "agent", Vendor: "claude", Model: "claude-sonnet-4", Effort: "high"},
		model:  "claude-opus-5",
		since:  time.Now(),
	}
	lines := laneBand(r, 100)
	if len(lines) != 1 {
		t.Fatalf("laneBand returned %d lines, want 1", len(lines))
	}
	got := lines[0]
	if !strings.Contains(got, "claude-opus-5") {
		t.Errorf("laneBand = %q, want log model claude-opus-5", got)
	}
	if strings.Contains(got, "claude-sonnet-4") {
		t.Errorf("laneBand = %q still contains config model claude-sonnet-4", got)
	}
	if !strings.Contains(got, "lane 2 · agent (claude) · claude-opus-5 · high") {
		t.Errorf("laneBand = %q, want full identity", got)
	}
}

// A reading with no configured window prints the raw ctx reading and no
// percentage, matching the work row's posture.
func TestLaneBandContextWithoutWindow(t *testing.T) {
	r := workRow{
		lane:    1,
		runner:  config.RunnerIdentity{Name: "claude", Vendor: "claude"},
		context: 82100,
		window:  0,
		since:   time.Now(),
	}
	lines := laneBand(r, 100)
	if len(lines) != 1 {
		t.Fatalf("laneBand returned %d lines, want 1", len(lines))
	}
	got := lines[0]
	if !strings.Contains(got, "ctx 82k") {
		t.Errorf("laneBand = %q, want ctx 82k", got)
	}
	if strings.Contains(got, "%") {
		t.Errorf("laneBand = %q, want no percentage when window is unset", got)
	}

	// With window configured, percentage is appended.
	r.window = 200000
	lines = laneBand(r, 100)
	got = lines[0]
	if !strings.Contains(got, "ctx 82k · 41%") {
		t.Errorf("laneBand = %q, want ctx 82k · 41%%", got)
	}
}

// A run with no log yet (a provisioning lane) prints identity and the clock
// and nothing else — no tokens, no spend, no context.
func TestLaneBandProvisioningLane(t *testing.T) {
	r := workRow{
		lane:   3,
		runner: config.RunnerIdentity{Name: "claude", Vendor: "claude", Model: "claude-opus-5", Effort: "high"},
		since:  time.Now(),
	}
	lines := laneBand(r, 100)
	if len(lines) != 1 {
		t.Fatalf("laneBand returned %d lines, want 1", len(lines))
	}
	got := lines[0]
	if !strings.Contains(got, "lane 3 · claude (claude) · claude-opus-5 · high") {
		t.Errorf("laneBand = %q, want identity", got)
	}
	if strings.Contains(got, "tok") || strings.Contains(got, "$") || strings.Contains(got, "ctx") {
		t.Errorf("laneBand = %q, want no figures other than clock", got)
	}
}

// Narrowing the width drops figures from the right (context, then cost, then
// tokens) before identity truncates against the clock.
func TestLaneBandNarrowDropsFiguresFromRight(t *testing.T) {
	r := workRow{
		lane:    2,
		runner:  config.RunnerIdentity{Name: "agent", Vendor: "claude", Model: "claude-opus-5", Effort: "high"},
		since:   time.Now().Add(-4*time.Minute - 12*time.Second),
		tokens:  84200,
		cost:    1.24,
		context: 82100,
		window:  200000,
	}

	// Full width: identity + all figures (clock, tokens, cost, context)
	full := laneBand(r, 120)[0]
	if !strings.Contains(full, "lane 2 · agent (claude) · claude-opus-5 · high") {
		t.Errorf("full = %q missing identity", full)
	}
	if !strings.Contains(full, "84k tok") || !strings.Contains(full, "$1.24") || !strings.Contains(full, "ctx 82k · 41%") {
		t.Errorf("full = %q missing figures", full)
	}

	// Squeeze until context drops, but cost and tokens remain intact and identity is untruncated
	wContextDropped := 80
	s1 := laneBand(r, wContextDropped)[0]
	if !strings.Contains(s1, "lane 2 · agent (claude) · claude-opus-5 · high") {
		t.Errorf("s1 (%d) = %q truncated identity early", wContextDropped, s1)
	}
	if strings.Contains(s1, "ctx") {
		t.Errorf("s1 (%d) = %q still contains context", wContextDropped, s1)
	}
	if !strings.Contains(s1, "$1.24") || !strings.Contains(s1, "84k tok") {
		t.Errorf("s1 (%d) = %q missing cost or tokens", wContextDropped, s1)
	}

	// Squeeze until cost drops too, tokens remain intact and identity untruncated
	wCostDropped := 65
	s2 := laneBand(r, wCostDropped)[0]
	if !strings.Contains(s2, "lane 2 · agent (claude) · claude-opus-5 · high") {
		t.Errorf("s2 (%d) = %q truncated identity early", wCostDropped, s2)
	}
	if strings.Contains(s2, "$") || strings.Contains(s2, "ctx") {
		t.Errorf("s2 (%d) = %q still contains cost or context", wCostDropped, s2)
	}
	if !strings.Contains(s2, "84k tok") {
		t.Errorf("s2 (%d) = %q missing tokens", wCostDropped, s2)
	}

	// Squeeze until tokens drop too, clock remains intact and identity untruncated
	wTokensDropped := 55
	s3 := laneBand(r, wTokensDropped)[0]
	if !strings.Contains(s3, "lane 2 · agent (claude) · claude-opus-5 · high") {
		t.Errorf("s3 (%d) = %q truncated identity early", wTokensDropped, s3)
	}
	if strings.Contains(s3, "tok") || strings.Contains(s3, "$") || strings.Contains(s3, "ctx") {
		t.Errorf("s3 (%d) = %q still contains dropped figures", wTokensDropped, s3)
	}

	// Squeeze below identity + clock: identity truncates with ellipsis, clock remains
	wTruncated := 35
	s4 := laneBand(r, wTruncated)[0]
	if !strings.Contains(s4, "…") {
		t.Errorf("s4 (%d) = %q expected identity to truncate with ellipsis", wTruncated, s4)
	}
	if !strings.Contains(s4, "4m12s") {
		t.Errorf("s4 (%d) = %q clock dropped", wTruncated, s4)
	}
	if lipgloss.Width(s4) > wTruncated {
		t.Errorf("s4 width %d > %d", lipgloss.Width(s4), wTruncated)
	}
}

// laneChart returns nil if width <= 0, height <= 0, or chart is empty.
func TestLaneChartEmptyOrZeroWidth(t *testing.T) {
	r := workRow{lane: 1}
	if got := laneChart(r, 80, 12); len(got) != 0 {
		t.Fatalf("laneChart with empty chart returned %d lines, want 0", len(got))
	}
	now := time.Now()
	r.chart = []chartSeries{{key: "", name: "main", buckets: []timedBucket{{at: now, count: 1}}}}
	if got := laneChart(r, 0, 12); len(got) != 0 {
		t.Fatalf("laneChart with width 0 returned %d lines, want 0", len(got))
	}
	if got := laneChart(r, 80, 0); len(got) != 0 {
		t.Fatalf("laneChart with height 0 returned %d lines, want 0", len(got))
	}
	r.chart = []chartSeries{{key: "", name: "main", buckets: nil}}
	if got := laneChart(r, 80, 12); len(got) != 0 {
		t.Fatalf("laneChart with empty buckets returned %d lines, want 0", len(got))
	}
}

func TestLaneChartRendersBrailleLines(t *testing.T) {
	now := time.Now()
	// A 15-minute window (300 buckets of 3s)
	chart := make([]timedBucket, 300)
	for i := range chart {
		chart[i] = timedBucket{
			at:    now.Add(-time.Duration(300-1-i) * pulseBucket),
			count: (i % 5),
		}
	}
	r := workRow{lane: 1, chart: []chartSeries{{key: "", name: "main", buckets: chart}}}
	width := 80
	height := 12
	lines := laneChart(r, width, height)
	if len(lines) != height+1 {
		t.Fatalf("laneChart returned %d lines, want %d (12 chart + 1 legend)", len(lines), height+1)
	}
	for i, l := range lines {
		stripped := ansi.Strip(l)
		if lipgloss.Width(stripped) != width {
			t.Errorf("line %d width = %d, want %d", i, lipgloss.Width(stripped), width)
		}
	}

	// Bottom chart rows contain braille
	chartArea := strings.Join(lines[:height], "\n")
	if !strings.ContainsFunc(chartArea, runes.IsBraillePattern) {
		t.Fatalf("chart area has no braille runes:\n%s", chartArea)
	}

	// X label row is line height-1 (bottom row of linechart)
	xRow := ansi.Strip(lines[height-1])
	if !strings.Contains(xRow, "now") {
		t.Errorf("X label row = %q, want it to contain %q", xRow, "now")
	}
	if !strings.Contains(xRow, "-15m") && !strings.Contains(xRow, "-10m") && !strings.Contains(xRow, "-5m") {
		t.Errorf("X label row = %q, want it to contain relative -Nm label", xRow)
	}

	// Y label column carries ascending integers on the left
	y0 := ansi.Strip(lines[height-2]) // bottom line of Y axis ticks (above X axis)
	if !strings.Contains(y0, "0") {
		t.Errorf("bottom Y tick line = %q, want 0", y0)
	}

	// Legend row is lines[height]
	legend := ansi.Strip(lines[height])
	if !strings.Contains(legend, "events/min") {
		t.Errorf("legend row = %q, want events/min", legend)
	}
	if strings.Count(legend, "events/min") != 1 {
		t.Errorf("legend row = %q, want events/min once", legend)
	}
	if !strings.Contains(legend, "main") {
		t.Errorf("legend row = %q, want main", legend)
	}
}

func TestLaneChartYoungRunShortHorizon(t *testing.T) {
	now := time.Now()
	// A run with 5 buckets (12-15 seconds old)
	chart := make([]timedBucket, 5)
	for i := range chart {
		chart[i] = timedBucket{
			at:    now.Add(-time.Duration(5-1-i) * pulseBucket),
			count: i + 1,
		}
	}
	r := workRow{lane: 1, chart: []chartSeries{{key: "", name: "main", buckets: chart}}}
	width := 60
	height := 8
	lines := laneChart(r, width, height)
	if len(lines) != height+1 {
		t.Fatalf("laneChart returned %d lines, want %d", len(lines), height+1)
	}
	for i, l := range lines {
		stripped := ansi.Strip(l)
		if lipgloss.Width(stripped) != width {
			t.Errorf("line %d width = %d, want %d", i, lipgloss.Width(stripped), width)
		}
	}

	// X label row should not contain 15m or 10m (short horizon)
	xRow := ansi.Strip(lines[height-1])
	if strings.Contains(xRow, "-15m") || strings.Contains(xRow, "-10m") {
		t.Errorf("young run X label row = %q contained long horizon labels", xRow)
	}
	if !strings.Contains(xRow, "now") {
		t.Errorf("young run X label row = %q missing now", xRow)
	}
}

func TestLaneChartAllZerosReadsFlat(t *testing.T) {
	now := time.Now()
	chart := make([]timedBucket, 20)
	for i := range chart {
		chart[i] = timedBucket{
			at:    now.Add(-time.Duration(20-1-i) * pulseBucket),
			count: 0,
		}
	}
	r := workRow{lane: 1, chart: []chartSeries{{key: "", name: "main", buckets: chart}}}
	width := 40
	height := 6
	lines := laneChart(r, width, height)
	if len(lines) != height+1 {
		t.Fatalf("laneChart returned %d lines, want %d", len(lines), height+1)
	}
	// Chart area should contain braille and not panic
	chartArea := strings.Join(lines[:height], "\n")
	if !strings.ContainsFunc(chartArea, runes.IsBraillePattern) {
		t.Fatalf("all-zero chart has no braille runes:\n%s", chartArea)
	}
}

// Four series draw four data sets and a one-row legend naming all four.
func TestLaneChartMultiSeriesFourAgentsAndLegend(t *testing.T) {
	now := time.Now()
	makeBuckets := func(count int) []timedBucket {
		b := make([]timedBucket, 10)
		for i := range b {
			b[i] = timedBucket{
				at:    now.Add(-time.Duration(10-1-i) * pulseBucket),
				count: count,
			}
		}
		return b
	}

	r := workRow{
		lane: 1,
		chart: []chartSeries{
			{key: "", name: "main", buckets: makeBuckets(2)},
			{key: "toolu_sub1", name: "Explore", buckets: makeBuckets(4)},
			{key: "toolu_sub2", name: "Plan", buckets: makeBuckets(1)},
			{key: "toolu_sub3", name: "Review", buckets: makeBuckets(3)},
		},
	}

	width := 100
	height := 10
	lines := laneChart(r, width, height)
	if len(lines) != height+1 {
		t.Fatalf("laneChart returned %d lines, want %d", len(lines), height+1)
	}

	legend := ansi.Strip(lines[height])
	for _, want := range []string{"events/min", "main", "Explore", "Plan", "Review"} {
		if !strings.Contains(legend, want) {
			t.Errorf("legend row = %q, want it to contain %q", legend, want)
		}
	}
}

// Two series with the same name get an ordinal appended (Explore, Explore 2).
func TestLaneChartSameNamedSeriesGetOrdinals(t *testing.T) {
	now := time.Now()
	makeBuckets := func() []timedBucket {
		b := make([]timedBucket, 5)
		for i := range b {
			b[i] = timedBucket{
				at:    now.Add(-time.Duration(5-1-i) * pulseBucket),
				count: 2,
			}
		}
		return b
	}

	r := workRow{
		lane: 1,
		chart: []chartSeries{
			{key: "", name: "main", buckets: makeBuckets()},
			{key: "toolu_sub1", name: "Explore", buckets: makeBuckets()},
			{key: "toolu_sub2", name: "Explore", buckets: makeBuckets()},
		},
	}

	width := 100
	height := 8
	lines := laneChart(r, width, height)
	legend := ansi.Strip(lines[height])
	if !strings.Contains(legend, "Explore") {
		t.Errorf("legend row = %q, want Explore", legend)
	}
	if !strings.Contains(legend, "Explore 2") {
		t.Errorf("legend row = %q, want Explore 2", legend)
	}
}

// A legend too wide for the pane drops series from the right while keeping
// events/min and main, and still fits width exactly.
func TestLaneChartLegendTooWideDropsFromRight(t *testing.T) {
	now := time.Now()
	makeBuckets := func() []timedBucket {
		b := make([]timedBucket, 5)
		for i := range b {
			b[i] = timedBucket{
				at:    now.Add(-time.Duration(5-1-i) * pulseBucket),
				count: 1,
			}
		}
		return b
	}

	r := workRow{
		lane: 1,
		chart: []chartSeries{
			{key: "", name: "main", buckets: makeBuckets()},
			{key: "toolu_sub1", name: "ExploreLongNameHere", buckets: makeBuckets()},
			{key: "toolu_sub2", name: "PlanAnotherLongName", buckets: makeBuckets()},
			{key: "toolu_sub3", name: "ReviewThirdLongName", buckets: makeBuckets()},
		},
	}

	// Squeeze width so all 4 don't fit, but 2 or 1 fit
	width := 45
	height := 8
	lines := laneChart(r, width, height)
	legend := lines[height]
	stripped := ansi.Strip(legend)
	if lipgloss.Width(stripped) != width {
		t.Errorf("legend width = %d, want %d: %q", lipgloss.Width(stripped), width, stripped)
	}
	if !strings.Contains(stripped, "events/min") || !strings.Contains(stripped, "main") {
		t.Errorf("legend = %q, must always keep events/min and main", stripped)
	}
	// The rightmost long name should have been dropped
	if strings.Contains(stripped, "ReviewThirdLongName") {
		t.Errorf("legend = %q still contains rightmost series ReviewThirdLongName", stripped)
	}
}
