package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
)

// TestResolveLayoutTiledIsEvenGrid: the default name resolves to ok=false so the
// caller keeps the unchanged even-grid path.
func TestResolveLayoutTiledIsEvenGrid(t *testing.T) {
	if _, ok := resolveLayout(layoutTiled, nil, 4, 120, 40, nil, nil); ok {
		t.Fatal(`"tiled" must not resolve to rects — it uses the even-grid path`)
	}
	if _, ok := resolveLayout("", nil, 4, 120, 40, nil, nil); ok {
		t.Fatal("empty layout name must fall back to the even grid")
	}
	if _, ok := resolveLayout("no-such-layout", nil, 4, 120, 40, nil, nil); ok {
		t.Fatal("an unknown layout must fall back to the even grid")
	}
}

// TestStackLayout: every tile is full width and stacked top to bottom.
func TestStackLayout(t *testing.T) {
	rects, ok := resolveLayout(layoutStack, nil, 3, 100, 30, nil, nil)
	if !ok || len(rects) != 3 {
		t.Fatalf("stack: ok=%v rects=%d", ok, len(rects))
	}
	for i, r := range rects {
		if r.x != 0 || r.w != rects[0].w {
			t.Errorf("tile %d not full width: x=%d w=%d", i, r.x, r.w)
		}
		if i > 0 && r.y <= rects[i-1].y {
			t.Errorf("tile %d does not stack below tile %d", i, i-1)
		}
	}
}

// TestMainVerticalLayout: the main tile takes the tall left column and the rest
// stack down a narrower right column.
func TestMainVerticalLayout(t *testing.T) {
	rects, ok := resolveLayout(layoutMainVertical, nil, 3, 120, 40, nil, nil)
	if !ok || len(rects) != 3 {
		t.Fatalf("main-vertical: ok=%v rects=%d", ok, len(rects))
	}
	main, a, b := rects[0], rects[1], rects[2]
	if main.x != 0 || a.x <= main.x || b.x != a.x {
		t.Errorf("secondary tiles should sit in a right column: %+v %+v %+v", main, a, b)
	}
	if main.h <= a.h {
		t.Errorf("main tile should be taller than a secondary: main.h=%d a.h=%d", main.h, a.h)
	}
	if a.y >= b.y {
		t.Errorf("secondary tiles should stack: a.y=%d b.y=%d", a.y, b.y)
	}
}

// TestMainHorizontalLayout: the main tile takes the wide top row, the rest line up
// along the bottom row.
func TestMainHorizontalLayout(t *testing.T) {
	rects, ok := resolveLayout(layoutMainHorizontal, nil, 3, 120, 40, nil, nil)
	if !ok || len(rects) != 3 {
		t.Fatalf("main-horizontal: ok=%v rects=%d", ok, len(rects))
	}
	main, a, b := rects[0], rects[1], rects[2]
	if main.y != 0 || a.y <= main.y || b.y != a.y {
		t.Errorf("secondary tiles should sit in a bottom row: %+v %+v %+v", main, a, b)
	}
	if main.w <= a.w {
		t.Errorf("main tile should be wider than a secondary: main.w=%d a.w=%d", main.w, a.w)
	}
	if a.x >= b.x {
		t.Errorf("secondary tiles should sit side by side: a.x=%d b.x=%d", a.x, b.x)
	}
}

// TestCustomLayoutSpans: a custom areas grid maps regions (in reading order) to
// spanned rects; the spanning region is bigger than the single-cell ones.
func TestCustomLayoutSpans(t *testing.T) {
	custom := []config.Layout{{
		Name: "review",
		Areas: [][]string{
			{"diff", "diff", "log"},
			{"diff", "diff", "sh"},
		},
	}}
	rects, ok := resolveLayout("review", custom, 3, 120, 40, nil, nil)
	if !ok || len(rects) != 3 {
		t.Fatalf("custom: ok=%v rects=%d", ok, len(rects))
	}
	diff, log, sh := rects[0], rects[1], rects[2]
	if diff.w <= log.w || diff.h <= log.h {
		t.Errorf("diff should span 2x2 and dwarf the single cells: %+v %+v", diff, log)
	}
	if log.x != sh.x || log.y >= sh.y {
		t.Errorf("log and sh share the right column, log above sh: %+v %+v", log, sh)
	}
}

// TestCustomLayoutRejectsNonRectangular: an L-shaped region (not a solid rectangle)
// is rejected so the compositor never sees a hole.
func TestCustomLayoutRejectsNonRectangular(t *testing.T) {
	custom := []config.Layout{{
		Name: "bad",
		Areas: [][]string{
			{"a", "a"},
			{"a", "b"},
		},
	}}
	if _, ok := resolveLayout("bad", custom, 2, 100, 30, nil, nil); ok {
		t.Fatal("a non-rectangular region must reject the layout (fail-open to the even grid)")
	}
}

// TestLayoutFailsOpenWhenTooSmall: a terminal too small for the grid yields ok=false
// rather than zero-sized tiles.
func TestLayoutFailsOpenWhenTooSmall(t *testing.T) {
	if _, ok := resolveLayout(layoutStack, nil, 20, 10, 6, nil, nil); ok {
		t.Fatal("20 stacked tiles in 6 rows cannot fit — must fail open")
	}
}

// TestTracksFillSpan: the column tracks plus their gutters exactly cover the span.
func TestTracksFillSpan(t *testing.T) {
	for _, tc := range []struct{ total, n, gap int }{{100, 3, 1}, {120, 4, 1}, {37, 2, 1}, {50, 1, 0}} {
		starts, sizes := tracks(tc.total, tc.n, tc.gap)
		end := starts[tc.n-1] + sizes[tc.n-1]
		want := tc.total
		// With gaps, the last track ends at total (no trailing gutter).
		if end != want {
			t.Errorf("tracks(%d,%d,%d) end=%d, want %d", tc.total, tc.n, tc.gap, end, want)
		}
	}
}

// TestTracksWeightedSumsExactly: however the weights are skewed, the weighted
// tracks plus their gutters still cover the span exactly — no rounding drift.
func TestTracksWeightedSumsExactly(t *testing.T) {
	cases := []struct {
		total, n, gap int
		weights       []float64
	}{
		{100, 3, 1, []float64{2, 1, 1}},
		{120, 4, 1, []float64{1, 3, 1, 2}},
		{37, 2, 1, []float64{1, 0.4}},
		{80, 2, 1, []float64{5, 1}},
		{60, 3, 0, []float64{1}}, // short weight slice pads with 1.0
	}
	for _, tc := range cases {
		starts, sizes := tracksWeighted(tc.total, tc.n, tc.gap, tc.weights)
		sum := 0
		for _, s := range sizes {
			sum += s
		}
		if want := tc.total - (tc.n-1)*tc.gap; sum != want {
			t.Errorf("tracksWeighted(%d,%d,%d,%v) sizes sum=%d, want usable %d", tc.total, tc.n, tc.gap, tc.weights, sum, want)
		}
		if end := starts[tc.n-1] + sizes[tc.n-1]; end != tc.total {
			t.Errorf("tracksWeighted(%d,%d,%d,%v) end=%d, want %d", tc.total, tc.n, tc.gap, tc.weights, end, tc.total)
		}
	}
}

// TestTracksWeightedSkews: a heavier weight yields a strictly larger track, and
// equal weights match the plain even split (so the default render is unchanged).
func TestTracksWeightedSkews(t *testing.T) {
	_, sizes := tracksWeighted(100, 2, 0, []float64{3, 1})
	if sizes[0] <= sizes[1] {
		t.Errorf("weight 3:1 should make track 0 larger: %v", sizes)
	}
	_, wEqual := tracksWeighted(100, 4, 1, []float64{1, 1, 1, 1})
	_, even := tracks(100, 4, 1)
	for i := range even {
		if wEqual[i] != even[i] {
			t.Errorf("equal weights must match tracks(): weighted=%v even=%v", wEqual, even)
			break
		}
	}
}

// TestResolveLayoutWeightedWidensMain: a column weight in favour of the main tile
// makes main-vertical's main column wider than its even-split width.
func TestResolveLayoutWeightedWidensMain(t *testing.T) {
	even, ok := resolveLayout(layoutMainVertical, nil, 3, 120, 40, nil, nil)
	if !ok {
		t.Fatal("even main-vertical must resolve")
	}
	wide, ok := resolveLayout(layoutMainVertical, nil, 3, 120, 40, []float64{2, 1}, nil)
	if !ok {
		t.Fatal("weighted main-vertical must resolve")
	}
	if wide[0].w <= even[0].w {
		t.Errorf("column weight 2:1 should widen the main tile: even=%d weighted=%d", even[0].w, wide[0].w)
	}
}

// TestComposeTiles: two side-by-side tiles of different heights compose into a
// full-width, full-height frame with the shorter one's gap blank-filled.
func TestComposeTiles(t *testing.T) {
	rects := []tileRect{
		{x: 0, y: 0, w: 3, h: 3},
		{x: 3, y: 0, w: 2, h: 1},
	}
	rendered := []string{
		strings.Join([]string{"aaa", "aaa", "aaa"}, "\n"),
		"bb",
	}
	out := composeTiles(rects, rendered, 5, 3)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	if lines[0] != "aaabb" {
		t.Errorf("row 0 = %q, want %q", lines[0], "aaabb")
	}
	// Row 1: tile b is only 1 tall, so it contributes nothing here; the trailing
	// gap past the last covering tile is left unpadded, like JoinHorizontal.
	if lines[1] != "aaa" {
		t.Errorf("row 1 = %q, want %q", lines[1], "aaa")
	}
	// A leading gap before a tile that starts mid-row IS blank-filled.
	gap := composeTiles([]tileRect{{x: 2, y: 0, w: 2, h: 1}}, []string{"cc"}, 4, 1)
	if gap != "  cc" {
		t.Errorf("leading gap = %q, want %q", gap, "  cc")
	}
}
