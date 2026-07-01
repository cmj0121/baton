package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/config"
)

// The group split's layout engine. The default — "tiled" — is the historical even
// grid rendered by tileGeometry/tileGrid and is handled on that path untouched.
// Every other layout (the built-in presets and the user's custom TUI.yaml grids)
// resolves to a set of tileRects here: variable-span boxes the compositor stamps
// onto a screen buffer. Presets and custom grids share one model — a grid of unit
// cells, each tile spanning a rectangle of them — so the same rect math serves all.

// Built-in layout names. "tiled" is the default even grid (not resolved here).
const (
	layoutTiled          = "tiled"
	layoutMainVertical   = "main-vertical"
	layoutMainHorizontal = "main-horizontal"
	layoutStack          = "stack"
)

// presetLayouts is the order layouts cycle in before any custom ones.
var presetLayouts = []string{layoutTiled, layoutMainVertical, layoutMainHorizontal, layoutStack}

// tileRect is one tile's placement: its top-left corner (x,y) and outer size (w,h)
// in terminal cells, plus the inner emulator size (emuCols,emuRows) derived from
// them. The compositor blits each rendered box at (x,y); attach/resize size the
// panel's emulator to (emuCols,emuRows).
type tileRect struct {
	x, y, w, h       int
	emuCols, emuRows int
}

// cellSpan is a tile's span over the unit-cell grid: rows [r0,r1) and cols [c0,c1).
type cellSpan struct {
	r0, c0, r1, c1 int
}

// resolveLayout lays n tiles into a w×h area under the named layout, returning one
// tileRect per tile (in tile order) and ok=true. It returns ok=false when the name
// is "tiled" or unknown, or when the grid does not fit (a tile would be too small to
// render) — the caller falls back to the even-grid path. customs are the user's
// TUI.yaml layouts, consulted before the built-in presets so a custom may override a
// preset name. Optional per-column and per-row weights skew the even track division
// (see tracksAxis), so a tile can be grown or shrunk from the split's resize mode;
// nil weights (the common case) give the plain even layout, and weights of the wrong
// length are padded with 1 / truncated to the resolved grid, so a stale set from
// another layout degrades gracefully rather than being rejected.
func resolveLayout(name string, customs []config.Layout, n, w, h int, colWeights, rowWeights []float64) ([]tileRect, bool) {
	if n < 1 || w < 1 || h < 1 || name == "" || name == layoutTiled {
		return nil, false
	}
	rows, cols, spans, ok := layoutSpans(name, customs, n)
	if !ok || len(spans) == 0 {
		return nil, false
	}
	return spansToRects(rows, cols, spans, w, h, colWeights, rowWeights)
}

// layoutSpans resolves a layout name and tile count to a unit-cell grid: its row
// and column count and one cellSpan per tile (capped at and padded to n). A custom
// layout (matched by name) wins over a preset of the same name.
func layoutSpans(name string, customs []config.Layout, n int) (rows, cols int, spans []cellSpan, ok bool) {
	for _, l := range customs {
		if l.Name == name {
			return customSpans(l, n)
		}
	}
	switch name {
	case layoutStack:
		return stackSpans(n)
	case layoutMainVertical:
		return mainVerticalSpans(n)
	case layoutMainHorizontal:
		return mainHorizontalSpans(n)
	}
	return 0, 0, nil, false
}

// stackSpans stacks every tile in a single full-width column.
func stackSpans(n int) (int, int, []cellSpan, bool) {
	spans := make([]cellSpan, n)
	for i := 0; i < n; i++ {
		spans[i] = cellSpan{i, 0, i + 1, 1}
	}
	return n, 1, spans, true
}

// mainVerticalSpans gives the first tile the full-height left column and stacks the
// rest down a right column. With one tile it degenerates to a single full cell.
func mainVerticalSpans(n int) (int, int, []cellSpan, bool) {
	if n == 1 {
		return 1, 1, []cellSpan{{0, 0, 1, 1}}, true
	}
	rows := n - 1 // the right column holds the n-1 secondary tiles
	spans := make([]cellSpan, n)
	spans[0] = cellSpan{0, 0, rows, 1} // main spans the whole left column
	for i := 1; i < n; i++ {
		spans[i] = cellSpan{i - 1, 1, i, 2}
	}
	return rows, 2, spans, true
}

// mainHorizontalSpans gives the first tile the full-width top row and lays the rest
// along a bottom row. With one tile it degenerates to a single full cell.
func mainHorizontalSpans(n int) (int, int, []cellSpan, bool) {
	if n == 1 {
		return 1, 1, []cellSpan{{0, 0, 1, 1}}, true
	}
	cols := n - 1 // the bottom row holds the n-1 secondary tiles
	spans := make([]cellSpan, n)
	spans[0] = cellSpan{0, 0, 1, cols} // main spans the whole top row
	for i := 1; i < n; i++ {
		spans[i] = cellSpan{1, i - 1, 2, i}
	}
	return 2, cols, spans, true
}

// customSpans resolves a user's areas grid to spans. Each distinct region name maps
// to the bounding rectangle of the cells that carry it; the region must be a solid
// rectangle (no gaps, no second disjoint block) or the layout is rejected. Regions
// are ordered by first appearance in row-major order, and the first n fill the n
// tiles — so members map to regions in reading order, and a layout with more
// regions than tiles simply leaves the trailing regions empty.
func customSpans(l config.Layout, n int) (int, int, []cellSpan, bool) {
	if len(l.Areas) == 0 {
		return 0, 0, nil, false
	}
	rows := len(l.Areas)
	cols := 0
	for _, row := range l.Areas {
		cols = max(cols, len(row))
	}
	if cols == 0 {
		return 0, 0, nil, false
	}

	// Collect each region's bounding box and cell count, in first-appearance order.
	type region struct {
		name                   string
		r0, c0, r1, c1, filled int
	}
	idx := map[string]int{}
	var regions []*region
	for r, row := range l.Areas {
		for c, nameCell := range row {
			if nameCell == "" || nameCell == "." { // "." is an explicit empty cell
				continue
			}
			i, ok := idx[nameCell]
			if !ok {
				i = len(regions)
				idx[nameCell] = i
				regions = append(regions, &region{name: nameCell, r0: r, c0: c, r1: r + 1, c1: c + 1})
			}
			rg := regions[i]
			rg.r0, rg.c0 = min(rg.r0, r), min(rg.c0, c)
			rg.r1, rg.c1 = max(rg.r1, r+1), max(rg.c1, c+1)
			rg.filled++
		}
	}
	if len(regions) == 0 {
		return 0, 0, nil, false
	}
	// Every region must be a solid rectangle — its cell count equals its area — so
	// the compositor never has to deal with L-shapes or holes.
	spans := make([]cellSpan, 0, len(regions))
	for _, rg := range regions {
		area := (rg.r1 - rg.r0) * (rg.c1 - rg.c0)
		if rg.filled != area {
			return 0, 0, nil, false
		}
		spans = append(spans, cellSpan{rg.r0, rg.c0, rg.r1, rg.c1})
	}
	if len(spans) > n {
		spans = spans[:n] // more regions than tiles: the extra regions stay empty
	}
	return rows, cols, spans, true
}

// spansToRects converts unit-cell spans into pixel tileRects over a w×h area. The
// columns and rows tile the area exactly (gaps of gtileGap sit between columns; no
// vertical gap, matching the even grid), with any rounding remainder spread across
// the tracks. Optional per-column / per-row weights skew the otherwise-even track
// sizes (nil ⇒ even) — the seam manual resize adjusts. A tile too small to render
// (its inner emulator would vanish) fails the whole layout, so the caller falls
// back to the even grid and an over-aggressive resize is rejected rather than drawn
// broken.
func spansToRects(rows, cols int, spans []cellSpan, w, h int, colWeights, rowWeights []float64) ([]tileRect, bool) {
	if rows < 1 || cols < 1 {
		return nil, false
	}
	colX, colW := tracksAxis(w, cols, gtileGap, colWeights)
	rowY, rowH := tracksAxis(h, rows, 0, rowWeights)

	rects := make([]tileRect, len(spans))
	for i, s := range spans {
		if s.r0 < 0 || s.c0 < 0 || s.r1 > rows || s.c1 > cols || s.r1 <= s.r0 || s.c1 <= s.c0 {
			return nil, false
		}
		x := colX[s.c0]
		y := rowY[s.r0]
		// The width spans every column the tile covers, plus the gtileGap gutters
		// between them (which become tile content, not blank space).
		tw := colX[s.c1-1] + colW[s.c1-1] - x
		th := rowY[s.r1-1] + rowH[s.r1-1] - y
		emuCols := tw - 4 // border (2) + padding (2)
		emuRows := th - 3 // border (2) + head line (1)
		if emuCols < 1 || emuRows < 1 {
			return nil, false
		}
		rects[i] = tileRect{x: x, y: y, w: tw, h: th, emuCols: emuCols, emuRows: emuRows}
	}
	return rects, true
}

// tracks divides total cells into n tracks separated by gap-cell gutters, returning
// each track's start offset and size. The usable space (total minus the gutters) is
// split as evenly as possible, with the remainder handed to the first tracks so the
// tracks fill the whole span.
func tracks(total, n, gap int) (starts, sizes []int) {
	starts = make([]int, n)
	sizes = make([]int, n)
	usable := total - (n-1)*gap
	if usable < n {
		usable = n // floor each track at 1; spansToRects rejects if still too small
	}
	base := usable / n
	extra := usable % n
	x := 0
	for i := 0; i < n; i++ {
		sz := base
		if i < extra {
			sz++
		}
		starts[i] = x
		sizes[i] = sz
		x += sz + gap
	}
	return starts, sizes
}

// tracksAxis sizes n tracks like tracks, but skewed by per-track weights when any
// are given: it is the seam manual resize hooks into. With no weights it is exactly
// tracks (so the even grid is byte-for-byte unchanged), keeping the weighted path
// off every default render.
func tracksAxis(total, n, gap int, weights []float64) (starts, sizes []int) {
	if len(weights) == 0 {
		return tracks(total, n, gap)
	}
	return tracksWeighted(total, n, gap, weights)
}

// tracksWeighted divides total (minus the gutters) across n tracks in proportion
// to weights, rather than evenly. It uses largest-remainder apportionment — floor
// each track's exact share, then hand the leftover cells one at a time to the
// tracks with the largest fractional part — so the sizes always sum to the usable
// span exactly, with no drift. Weights shorter than n are padded with 1 and
// non-positive weights are floored to 1, so a partial or stale weight set still
// yields a sane layout. A weight small enough to zero a track is left for
// spansToRects to reject (it fails the layout), which is how the resize hook clamps.
func tracksWeighted(total, n, gap int, weights []float64) (starts, sizes []int) {
	starts = make([]int, n)
	sizes = make([]int, n)
	usable := total - (n-1)*gap
	if usable < n {
		usable = n
	}
	sum := 0.0
	w := make([]float64, n)
	for i := 0; i < n; i++ {
		wi := 1.0
		if i < len(weights) && weights[i] > 0 {
			wi = weights[i]
		}
		w[i] = wi
		sum += wi
	}
	type rem struct {
		i    int
		frac float64
	}
	rems := make([]rem, n)
	used := 0
	for i := 0; i < n; i++ {
		exact := float64(usable) * w[i] / sum
		sizes[i] = int(exact)
		rems[i] = rem{i, exact - float64(sizes[i])}
		used += sizes[i]
	}
	sort.Slice(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for k := 0; used < usable; k, used = k+1, used+1 {
		sizes[rems[k%n].i]++
	}
	x := 0
	for i := 0; i < n; i++ {
		starts[i] = x
		x += sizes[i] + gap
	}
	return starts, sizes
}

// composeTiles stamps each rendered tile box onto a w×h screen buffer at its rect,
// returning the joined frame. It works row by row: for each screen line it finds
// the tiles spanning that line (their x-ranges are disjoint in a valid layout),
// orders them left to right, and concatenates their slice of that row with blank
// fill in the gaps — so a tall tile and a short one side by side compose like
// JoinHorizontal, and ANSI styling is never split mid-escape.
func composeTiles(rects []tileRect, rendered []string, w, h int) string {
	blocks := make([][]string, len(rendered))
	for i, r := range rendered {
		blocks[i] = strings.Split(r, "\n")
	}
	lines := make([]string, h)
	for y := 0; y < h; y++ {
		var cover []int
		for i, r := range rects {
			if y >= r.y && y < r.y+r.h {
				cover = append(cover, i)
			}
		}
		sort.Slice(cover, func(a, b int) bool { return rects[cover[a]].x < rects[cover[b]].x })

		var line strings.Builder
		cursor := 0
		for _, i := range cover {
			r := rects[i]
			if r.x > cursor {
				line.WriteString(strings.Repeat(" ", r.x-cursor))
			}
			cell := ""
			if row := y - r.y; row >= 0 && row < len(blocks[i]) {
				cell = blocks[i][row]
			}
			line.WriteString(cell)
			vw := lipgloss.Width(cell)
			if vw < r.w { // pad a short row out to the tile's full width
				line.WriteString(strings.Repeat(" ", r.w-vw))
				vw = r.w
			}
			cursor = r.x + vw
		}
		lines[y] = line.String()
	}
	return strings.Join(lines, "\n")
}
