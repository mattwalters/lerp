package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/NimbleMarkets/ntcharts/canvas/runes"
	"github.com/NimbleMarkets/ntcharts/linechart/timeserieslinechart"
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
	r.chart = []timedBucket{{at: now, count: 1}}
	if got := laneChart(r, 0, 12); len(got) != 0 {
		t.Fatalf("laneChart with width 0 returned %d lines, want 0", len(got))
	}
	if got := laneChart(r, 80, 0); len(got) != 0 {
		t.Fatalf("laneChart with height 0 returned %d lines, want 0", len(got))
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
	r := workRow{lane: 1, chart: chart}
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
	r := workRow{lane: 1, chart: chart}
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
	r := workRow{lane: 1, chart: chart}
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

// Build a chart with the empty formatter and assert the last line of View() is
// all spaces — the assumption the rewrite rests on.
func TestLaneChartLibraryXLabelRowEmpty(t *testing.T) {
	now := time.Now()
	chart := timeserieslinechart.New(
		80,
		12,
		timeserieslinechart.WithTimeRange(now.Add(-time.Minute), now),
		timeserieslinechart.WithYRange(0, 10),
		timeserieslinechart.WithXYSteps(1, 2),
		timeserieslinechart.WithXLabelFormatter(func(i int, v float64) string { return "" }),
		timeserieslinechart.WithYLabelFormatter(func(i int, v float64) string { return "0" }),
	)
	chart.DrawBrailleAll()
	lines := strings.Split(chart.View(), "\n")
	if len(lines) < 12 {
		t.Fatalf("chart.View() had %d lines, want >= 12", len(lines))
	}
	lastLine := lines[11]
	if strings.TrimSpace(ansi.Strip(lastLine)) != "" {
		t.Fatalf("expected last line of library chart to be all spaces, got %q", lastLine)
	}
}

// At widths 40, 60, 80 and 120 and at each rung, the stripped label row ends
// with "now" and no trailing space.
func TestLaneChartNowRightAligned(t *testing.T) {
	now := time.Now()
	rungs := []struct {
		name    string
		buckets int
	}{
		{"1m", 15},
		{"5m", 80},
		{"15m", 300},
	}
	widths := []int{40, 60, 80, 120}

	for _, rung := range rungs {
		chart := make([]timedBucket, rung.buckets)
		for i := range chart {
			chart[i] = timedBucket{
				at:    now.Add(-time.Duration(rung.buckets-1-i) * pulseBucket),
				count: 1,
			}
		}
		r := workRow{lane: 1, chart: chart}
		for _, w := range widths {
			lines := laneChart(r, w, 12)
			if len(lines) != 13 {
				t.Fatalf("rung %s width %d: laneChart returned %d lines, want 13", rung.name, w, len(lines))
			}
			xRow := ansi.Strip(lines[11])
			if !strings.HasSuffix(xRow, "now") {
				t.Errorf("rung %s width %d: X label row %q does not end with 'now'", rung.name, w, xRow)
			}
			if lipgloss.Width(xRow) != w {
				t.Errorf("rung %s width %d: X label row width = %d, want %d", rung.name, w, lipgloss.Width(xRow), w)
			}
		}
	}
}

// Scan the row for label runs; assert at least one space between adjacent
// labels and that no label runs past width.
func TestLaneChartNoOverlap(t *testing.T) {
	now := time.Now()
	bucketCounts := []int{1, 5, 20, 50, 100, 200, 300}
	widths := []int{20, 25, 30, 40, 50, 60, 80, 120}

	for _, n := range bucketCounts {
		chart := make([]timedBucket, n)
		for i := range chart {
			chart[i] = timedBucket{
				at:    now.Add(-time.Duration(n-1-i) * pulseBucket),
				count: 1,
			}
		}
		r := workRow{lane: 1, chart: chart}
		for _, w := range widths {
			lines := laneChart(r, w, 12)
			if len(lines) == 0 {
				continue
			}
			xRow := ansi.Strip(lines[11])
			if lipgloss.Width(xRow) > w {
				t.Errorf("n=%d w=%d: row width %d > %d", n, w, lipgloss.Width(xRow), w)
			}

			// Scan for label tokens and their index ranges
			type token struct {
				text  string
				start int
				end   int
			}
			var tokens []token
			inToken := false
			curStart := 0
			for i, r := range []rune(xRow) {
				if r != ' ' {
					if !inToken {
						inToken = true
						curStart = i
					}
				} else {
					if inToken {
						tokens = append(tokens, token{text: string([]rune(xRow)[curStart:i]), start: curStart, end: i})
						inToken = false
					}
				}
			}
			if inToken {
				tokens = append(tokens, token{text: string([]rune(xRow)[curStart:]), start: curStart, end: len([]rune(xRow))})
			}

			for i := 0; i < len(tokens)-1; i++ {
				gap := tokens[i+1].start - tokens[i].end
				if gap < 1 {
					t.Errorf("n=%d w=%d: tokens %q (end %d) and %q (start %d) overlap or touch (gap %d)",
						n, w, tokens[i].text, tokens[i].end, tokens[i+1].text, tokens[i+1].start, gap)
				}
			}
		}
	}
}

// Windows of 1, 20, 100 and 300 buckets give 1m, 1m, 5m and 15m — asserted
// through the labels the row carries (-30s at 1m, -1m-family at 5m, -5m-family at 15m).
func TestLaneChartRungLadder(t *testing.T) {
	now := time.Now()
	tests := []struct {
		buckets int
		want    string // expected label family
		unwant  []string
	}{
		{
			buckets: 1,
			want:    "-30s",
			unwant:  []string{"-1m", "-5m", "-10m", "-15m"},
		},
		{
			buckets: 20,
			want:    "-30s",
			unwant:  []string{"-1m", "-5m", "-10m", "-15m"},
		},
		{
			buckets: 100,
			want:    "-1m",
			unwant:  []string{"-30s", "-10m", "-15m"},
		},
		{
			buckets: 300,
			want:    "-5m",
			unwant:  []string{"-30s", "-1m"},
		},
	}

	for _, tc := range tests {
		chart := make([]timedBucket, tc.buckets)
		for i := range chart {
			chart[i] = timedBucket{
				at:    now.Add(-time.Duration(tc.buckets-1-i) * pulseBucket),
				count: 1,
			}
		}
		r := workRow{lane: 1, chart: chart}
		lines := laneChart(r, 100, 12)
		if len(lines) != 13 {
			t.Fatalf("buckets %d: laneChart returned %d lines, want 13", tc.buckets, len(lines))
		}
		xRow := ansi.Strip(lines[11])
		if !strings.Contains(xRow, "now") {
			t.Errorf("buckets %d: X label row = %q, want it to contain 'now'", tc.buckets, xRow)
		}
		if !strings.Contains(xRow, tc.want) {
			t.Errorf("buckets %d: X label row = %q, want it to contain %q", tc.buckets, xRow, tc.want)
		}
		for _, uw := range tc.unwant {
			if strings.Contains(xRow, uw) {
				t.Errorf("buckets %d: X label row = %q, want it NOT to contain %q", tc.buckets, xRow, uw)
			}
		}
	}
}

// A chart whose oldest bucket is twelve minutes old but whose rest fits five
// minutes renders the same row as the same chart with that bucket removed, and
// the same Y labels — one assertion for the view range, one for the ceiling.
func TestLaneChartOutOfWindowBucketIgnored(t *testing.T) {
	now := time.Now()
	// 100 buckets spanning ~5 minutes
	baseChart := make([]timedBucket, 100)
	for i := range baseChart {
		baseChart[i] = timedBucket{
			at:    now.Add(-time.Duration(100-1-i) * pulseBucket),
			count: 2,
		}
	}

	// Chart with an out-of-window bucket 12 minutes ago with a huge count
	extraChart := append([]timedBucket{
		{
			at:    now.Add(-12 * time.Minute),
			count: 10000,
		},
	}, baseChart...)

	rBase := workRow{lane: 1, chart: baseChart}
	rExtra := workRow{lane: 1, chart: extraChart}

	linesBase := laneChart(rBase, 80, 12)
	linesExtra := laneChart(rExtra, 80, 12)

	if len(linesBase) != len(linesExtra) {
		t.Fatalf("line count mismatch: base=%d, extra=%d", len(linesBase), len(linesExtra))
	}

	for i := range linesBase {
		if linesBase[i] != linesExtra[i] {
			t.Errorf("line %d differs:\nbase:  %q\nextra: %q", i, linesBase[i], linesExtra[i])
		}
	}
}
