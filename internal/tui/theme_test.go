package tui

import (
	"strings"
	"testing"
)

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

// TestScrollThumbFitsOutright is the shorter-than-viewport edge: a document
// that never needs to scroll gets no thumb, rather than one that always
// reads full and says nothing.
func TestScrollThumbFitsOutright(t *testing.T) {
	for _, tc := range []struct {
		name                string
		total, height, yOff int
	}{
		{"shorter than the track", 10, 20, 0},
		{"exactly the track", 20, 20, 0},
		{"zero height", 10, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if sb, ok := scrollThumb(tc.total, tc.height, tc.yOff); ok {
				t.Fatalf("scrollThumb(%d, %d, %d) = %+v, true — want no thumb", tc.total, tc.height, tc.yOff, sb)
			}
		})
	}
}

// TestScrollThumbPinsTheEdges is the proportion math LERP-146 asks tests to
// pin: at yOffset 0 the thumb's top row is 0, and at the viewport's own
// maximum offset the thumb's last row is the track's last row — both exactly,
// with no off-by-one from the division, whatever the document's length.
func TestScrollThumbPinsTheEdges(t *testing.T) {
	for _, tc := range []struct {
		name          string
		total, height int
	}{
		{"a long document", 240, 36},
		{"barely over the track", 37, 36},
		{"a track much taller than one row", 1000, 50},
		{"a document not a clean multiple of the track", 241, 36},
		{"a document long enough the proportion floors to zero", 100000, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			maxOffset := tc.total - tc.height
			top, ok := scrollThumb(tc.total, tc.height, 0)
			if !ok {
				t.Fatalf("scrollThumb(%d, %d, 0) reported no thumb for a document taller than the track", tc.total, tc.height)
			}
			if top.top != 0 {
				t.Errorf("at yOffset 0, thumb top = %d, want 0", top.top)
			}
			bottom, ok := scrollThumb(tc.total, tc.height, maxOffset)
			if !ok {
				t.Fatalf("scrollThumb(%d, %d, %d) reported no thumb", tc.total, tc.height, maxOffset)
			}
			if last := bottom.top + bottom.len; last != tc.height {
				t.Errorf("at yOffset %d (the viewport's max), thumb covers rows [%d,%d), want it flush with the last row %d",
					maxOffset, bottom.top, last, tc.height)
			}
			// The thumb never claims a row the track does not have, at either
			// edge — the invariant both the top and bottom checks above rely on.
			for _, sb := range []scrollbar{top, bottom} {
				if sb.top < 0 || sb.top+sb.len > tc.height {
					t.Errorf("thumb %+v falls outside the %d-row track", sb, tc.height)
				}
			}
		})
	}
}

// TestScrollThumbNeverLosesTheThumbToRounding pins the floor: a document so
// long that a proportional thumb would round down to zero rows still gets
// one. Every other case in TestScrollThumbPinsTheEdges keeps
// height*height/total at 1 or more, so this is the one case that would miss
// a dropped max(1, …) — the thumb would vanish for exactly the longest
// documents the indicator exists for, with the rest of the suite still
// green.
func TestScrollThumbNeverLosesTheThumbToRounding(t *testing.T) {
	const total, height = 100000, 20
	if height*height >= total {
		t.Fatalf("test setup: height*height (%d) must be less than total (%d) to floor to zero", height*height, total)
	}
	sb, ok := scrollThumb(total, height, 0)
	if !ok {
		t.Fatalf("scrollThumb(%d, %d, 0) reported no thumb", total, height)
	}
	if sb.len != 1 {
		t.Errorf("thumb length = %d, want 1 — a document this long floors to a zero-length thumb without the max(1, …) floor", sb.len)
	}
}

// TestScrollThumbMovesBetweenTheEdges is the calm rule for what sits between
// top and bottom: a thumb further down the document never sits above one
// further up it, so scrolling reads as motion in the right direction and
// never flickers backward.
func TestScrollThumbMovesBetweenTheEdges(t *testing.T) {
	const total, height = 240, 36
	prev := -1
	for y := 0; y <= total-height; y++ {
		sb, ok := scrollThumb(total, height, y)
		if !ok {
			t.Fatalf("scrollThumb(%d, %d, %d) reported no thumb", total, height, y)
		}
		if sb.top < prev {
			t.Fatalf("at yOffset %d, thumb top %d moved back above %d", y, sb.top, prev)
		}
		prev = sb.top
	}
}

// TestPanelBoxDrawsTheThumb checks the rendering, not just the math: a
// scrollbar handed to panelBox replaces the right border with the thumb
// glyph on exactly the rows it covers, and leaves every other row's edge
// alone.
func TestPanelBoxDrawsTheThumb(t *testing.T) {
	sb := &scrollbar{top: 1, len: 2}
	rows := []string{"a", "b", "c", "d"}
	lines := strings.Split(panelBox("t", false, 10, 6, rows, padMain, sb), "\n")
	// lines[0] is the top border, lines[1..4] are the four body rows,
	// lines[5] is the bottom border.
	for i, want := range []bool{false, true, true, false} {
		line := lines[1+i]
		if got := strings.Contains(line, scrollThumbGlyph); got != want {
			t.Errorf("body row %d = %q, thumb glyph present = %v, want %v", i, line, got, want)
		}
	}
	if plain := panelBox("t", false, 10, 6, rows, padMain, nil); strings.Contains(plain, scrollThumbGlyph) {
		t.Errorf("a nil scrollbar drew a thumb anyway:\n%s", plain)
	}
}

func TestMoreMarker(t *testing.T) {
	got := moreMarker(3)
	want := styleFaint.Render("⋯ 3 more")
	if got != want {
		t.Errorf("moreMarker(3) = %q, want %q", got, want)
	}

	rows := []string{"row 1", "row 2", "row 3", "row 4"}
	fitted := fitRows(rows, 3)
	if len(fitted) != 3 {
		t.Fatalf("fitRows returned %d rows, want 3", len(fitted))
	}
	if fitted[2] != moreMarker(2) {
		t.Errorf("fitRows last row = %q, want %q", fitted[2], moreMarker(2))
	}
}
