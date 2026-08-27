package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The backgrounds the palette is measured against. Nothing here can read the
// operator's actual terminal, so these stand in for it: pure black and the
// two dark greys editors and terminals ship as defaults, then white, Solarized
// Light's paper and a light grey. A colour that clears the floor on all three
// of its side clears it on the terminals in between.
var (
	darkBackgrounds  = []string{"#000000", "#1E1E1E", "#282C34"}
	lightBackgrounds = []string{"#FFFFFF", "#FDF6E3", "#EEEEEE"}
)

// Colour marks state and never carries it alone, which theme.go already gets
// right — but a mark nobody can read is still a mark nobody can read. This is
// the other half: every palette entry stays legible on the background it will
// actually be drawn on. It is arithmetic over a handful of constants, so the
// floor can be an invariant rather than a one-time retune.
//
// Measured on the colours as declared, which is what a 24-bit terminal draws.
// A terminal with fewer colours is drawn the nearest one termenv has, and the
// nearest one can sit under the floor — faint's dark variant quantizes to
// #8787AF, 4.08:1 on #282C34. Meeting the floor after quantization too is a
// different change (the ticket keeps low-colour degradation separate), so
// what this pins is the palette, not the downsample.
func TestPaletteClearsContrastFloor(t *testing.T) {
	for _, c := range palette {
		for _, tc := range []struct {
			variant     string
			fg          string
			backgrounds []string
		}{
			{"light", c.color.Light, lightBackgrounds},
			{"dark", c.color.Dark, darkBackgrounds},
		} {
			for _, bg := range tc.backgrounds {
				t.Run(c.name+"/"+tc.variant+"/"+bg, func(t *testing.T) {
					got := contrastRatio(t, tc.fg, bg)
					if got < contrastFloor {
						t.Errorf("%s %s (%s) on %s is %.2f:1, want at least %.1f:1",
							c.name, tc.variant, tc.fg, bg, got, contrastFloor)
					}
				})
			}
		}
	}
}

// lipgloss picks a variant from a background it detects by asking the
// terminal, and a terminal that stays silent is read as black — so a light
// terminal behind tmux or ssh gets the dark variants on white. The override
// is the way out, and it is one env var read once.
func TestUseBackgroundOverridesDetection(t *testing.T) {
	for _, tc := range []struct {
		in       string
		wantDark bool
	}{
		{"light", false},
		{"dark", true},
		{"Dark", true},
		{" light ", false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			restoreBackground(t)
			// From the other side, always: detection under `go test` reads
			// dark, so a "dark" case that started dark would pass on a
			// branch that does nothing at all.
			lipgloss.SetHasDarkBackground(!tc.wantDark)
			if err := useBackground(tc.in); err != nil {
				t.Fatalf("useBackground(%q) = %v, want no error", tc.in, err)
			}
			if got := lipgloss.HasDarkBackground(); got != tc.wantDark {
				t.Errorf("after %s=%q, HasDarkBackground() = %v, want %v",
					backgroundEnv, tc.in, got, tc.wantDark)
			}
		})
	}
}

// A value that is neither is a refusal, not a shrug: an operator who spelled
// it "White" asked for something, and quietly ignoring them leaves them on
// the guess the override exists to escape. Unset is not a value at all.
//
// Both cases are checked for what they left behind as well as what they
// returned: writing to a global is the whole of what this function does, so
// a branch that wrote the wrong thing would otherwise pass. From both seeds,
// because one of them is whatever detection already returns here — a branch
// that pinned dark would sit still against that seed and look untouched.
func TestUseBackgroundLeavesDetectionAloneOtherwise(t *testing.T) {
	restoreBackground(t)
	for _, seed := range []bool{true, false} {
		lipgloss.SetHasDarkBackground(seed)
		if err := useBackground(""); err != nil {
			t.Fatalf("useBackground(\"\") = %v, want no error", err)
		}
		if lipgloss.HasDarkBackground() != seed {
			t.Errorf("unset flipped the background to %v — it is meant to leave detection alone",
				!seed)
		}
		for _, in := range []string{"White", "0", "auto", "no"} {
			err := useBackground(in)
			if err == nil {
				t.Errorf("useBackground(%q) = nil, want an error", in)
				continue
			}
			if !strings.Contains(err.Error(), backgroundEnv) || !strings.Contains(err.Error(), in) {
				t.Errorf("useBackground(%q) = %q, want it to name both %s and the value",
					in, err, backgroundEnv)
			}
			if lipgloss.HasDarkBackground() != seed {
				t.Errorf("useBackground(%q) changed the background before refusing it", in)
			}
		}
	}
}

// The floor is only a floor if it covers everything, and palette is a list
// somebody has to remember to add to. So the list is checked against the
// declarations themselves — every adaptive colour this package declares,
// wherever in it they land, since a new colour is as likely to be written
// beside its user as in theme.go. A colour missing from the list fails
// here rather than shipping unmeasured under a green suite.
//
// It reads the package's own source, so it wants the package directory as
// the working directory — which is where `go test` runs it.
func TestPaletteListsEveryColor(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	declared := map[string]bool{}
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), src, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", src, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, v := range spec.Values {
				lit, ok := v.(*ast.CompositeLit)
				if !ok {
					continue
				}
				if sel, ok := lit.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "AdaptiveColor" {
					declared[spec.Names[i].Name] = true
				}
			}
			return true
		})
	}
	listed := map[string]bool{}
	for _, c := range palette {
		listed[c.name] = true
	}
	for name := range declared {
		if !listed[name] && !decorativeColors[name] {
			t.Errorf("%s is declared but not in palette — an unlisted colour is an unmeasured one", name)
		}
	}
	for name := range listed {
		if !declared[name] {
			t.Errorf("palette lists %s, which this package does not declare", name)
		}
	}
}

// The carve-out LERP-145's rule 2 asks for: colorWordmark is decoration, so
// WCAG exempts it from the contrast rules this floor otherwise enforces on
// every colour in the package, and the exemption is pinned rather than
// merely granted — a rebalance that quietly bought it more contrast would
// leave the decoration carve-out wider than it needs to be, and a change
// that took it under a 1:1 ratio would leave decoration nobody can even
// squint at. Scoped to this one name (see decorativeColors in theme.go):
// nothing else gets to cite this test for its own exemption, and the floor
// above stays exactly as strict for everything informational.
func TestWordmarkIsExemptDecoration(t *testing.T) {
	for _, tc := range []struct {
		variant     string
		fg          string
		backgrounds []string
	}{
		{"light", colorWordmark.Light, lightBackgrounds},
		{"dark", colorWordmark.Dark, darkBackgrounds},
	} {
		for _, bg := range tc.backgrounds {
			t.Run(tc.variant+"/"+bg, func(t *testing.T) {
				got := contrastRatio(t, tc.fg, bg)
				if got >= contrastFloor {
					t.Errorf("colorWordmark %s (%s) on %s is %.2f:1, want under the %.1f:1 floor — it is decoration, not text",
						tc.variant, tc.fg, bg, got, contrastFloor)
				}
				if got <= 1.0 {
					t.Errorf("colorWordmark %s (%s) on %s is %.2f:1 — invisible, not merely dim",
						tc.variant, tc.fg, bg, got)
				}
			})
		}
	}
}

// NO_COLOR works for free — termenv honours it and every style here goes
// through termenv — which is exactly the kind of claim that rots into a lie
// nobody checks. So it is checked: with NO_COLOR set, a styled string is the
// string.
func TestNoColorLeavesTheTextBare(t *testing.T) {
	render := func(env fakeEnviron) string {
		r := lipgloss.NewRenderer(io.Discard, termenv.WithEnvironment(env), termenv.WithTTY(true))
		// Explicit, so the renderer never asks the terminal it does not have.
		r.SetHasDarkBackground(true)
		return styleRunning.Renderer(r).Render("running")
	}
	color := render(fakeEnviron{"TERM": "xterm-256color"})
	if !strings.Contains(color, "\x1b[") {
		t.Fatalf("without NO_COLOR the render came back bare (%q) — the control case is broken, not the claim", color)
	}
	bare := render(fakeEnviron{"TERM": "xterm-256color", "NO_COLOR": "1"})
	if bare != "running" {
		t.Errorf("with NO_COLOR set, render = %q, want %q", bare, "running")
	}
}

// fakeEnviron is the environment a renderer under test reads, so a test can
// set NO_COLOR without setting it for the process.
type fakeEnviron map[string]string

func (e fakeEnviron) Getenv(k string) string { return e[k] }

func (e fakeEnviron) Environ() []string {
	out := make([]string, 0, len(e))
	for k, v := range e {
		out = append(out, k+"="+v)
	}
	return out
}

// restoreBackground puts the default renderer's background back when the test
// ends. useBackground writes to global lipgloss state, and a test that left
// it flipped would be deciding what the next test renders.
func restoreBackground(t *testing.T) {
	t.Helper()
	was := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(was) })
}

// contrastRatio is WCAG 2.x's own formula: (L1+0.05)/(L2+0.05) over the two
// relative luminances, lighter first.
func contrastRatio(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// luminance is WCAG's relative luminance of a #rrggbb colour.
func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	r, g, b := channels(t, hex)
	return 0.2126*linearize(r) + 0.7152*linearize(g) + 0.0722*linearize(b)
}

// linearize undoes sRGB's transfer function for one 0..1 channel.
func linearize(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// channels splits #rrggbb into three 0..1 channels. A palette entry lipgloss
// could not parse would render as no colour at all, so a malformed one fails
// the test rather than passing it with a ratio computed from zeroes.
func channels(t *testing.T, hex string) (r, g, b float64) {
	t.Helper()
	s, ok := strings.CutPrefix(hex, "#")
	if !ok || len(s) != 6 {
		t.Fatalf("colour %q is not #rrggbb", hex)
	}
	var out [3]float64
	for i := range out {
		v, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			t.Fatalf("colour %q: %v", hex, err)
		}
		out[i] = float64(v) / 255
	}
	return out[0], out[1], out[2]
}
