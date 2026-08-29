package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
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
