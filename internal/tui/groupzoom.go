package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// The group split: zooming a work item lays its panels out as live tiles you
// navigate as a unit. tab cycles the focus, enter drops into the focused panel's
// own zoom, and the dashboard key (d) — or esc — leaves for the dashboard. From a
// zoomed member, back (C-t b) pops back to the split.

const (
	gtileGap        = 1  // space reserved to the right of each tile
	gtileMinW       = 32 // preferred minimum tile width, deciding the column count
	groupHeaderRows = 2  // header line + blank above the grid in groupZoomView

	// maxGroupTiles caps how many members stream live at once, bounding the PTYs,
	// emulators, and drain goroutines a single huge group can spin up. It is both
	// the hard ceiling on the visible count N and the default N before the user
	// dials it. Members past the cap fold into the summary tile.
	maxGroupTiles = 16
)

const (
	// resizeStep is how much weight one arrow press shifts across a tile boundary
	// in resize mode — a track carrying weight 1.0 by default, so ~0.18 is a brisk
	// but controllable nudge. resizeMinWeight floors a track's weight so no tile
	// can be shrunk to nothing (the render-time reject in spansToRects is the final
	// backstop; this keeps the common case from ever reaching it).
	resizeStep      = 0.18
	resizeMinWeight = 0.35
)

// splitRatios holds a group's manual resize weights for one layout: a multiplier
// per column and per row of that layout's unit-cell grid, applied on top of the
// even track division. All-1.0 is the even split. They are view-local — never sent
// to the server — and keyed to the layout they were made under, so cycling the
// layout (which changes the track counts) starts fresh rather than misapplying a
// stale set.
type splitRatios struct {
	layout string
	cols   []float64
	rows   []float64
}

// tileGeometry lays n tiles into a w×h area (h already net of the footer bar and
// the header), returning the column count and the inner emulator size of each
// tile so the grid fills the screen rather than sitting in fixed little boxes.
// A want > 0 forces that many columns (clamped to [1, n]); want == 0 auto-fits.
func tileGeometry(n, w, h, want int) (cols, emuCols, emuRows int) {
	if n < 1 || w < 1 || h < 1 {
		return 1, 1, 1
	}
	cols = want
	if cols < 1 {
		cols = (w + gtileGap) / (gtileMinW + gtileGap) // auto: as many as fit
	}
	cols = max(1, min(cols, n))
	rows := (n + cols - 1) / cols
	tileW := (w - cols*gtileGap) / cols // each tile reserves a right-margin gap
	tileH := h / rows
	emuCols = max(1, tileW-4) // border (2) + padding (2)
	emuRows = max(1, tileH-3) // border (2) + head line (1)
	return cols, emuCols, emuRows
}

// tileGeometry resolves the split's layout from the current model dimensions,
// reserving the footer bar (one row) and the header. Columns always auto-fit the
// full width (want == 0). The cell count is the live tiles plus the summary tile
// when there are collapsed members, so the even grid sizes every cell — summary
// included — alike.
func (m model) tileGeometry() (cols, emuCols, emuRows int) {
	return tileGeometry(m.gridCells(), m.width, m.height-1-groupHeaderRows, 0)
}

// gridCells is how many cells the even grid lays out: one per focus slot (the
// live tiles plus the summary tile when any member is collapsed), floored at one
// so geometry never divides by zero on an empty split.
func (m model) gridCells() int {
	return max(1, m.focusCount())
}

// attachGroupMembers opens a live emulator for every member of the group and
// subscribes to its output. Each emulator is sized to its tile so the panel's PTY
// fills the space. Tiles stay passive while you navigate — no keystrokes are fed
// to them — but each one's input side is forwarded so interact mode (i) can drive
// the focused tile in place without a zoom. A no-op without a client.
func (m *model) attachGroupMembers() {
	if m.client == nil {
		return
	}
	sizes := m.tileEmuSizes()
	m.groupEmus = make(map[string]*vt.SafeEmulator)
	for _, p := range m.tileMembers() {
		s := sizes[p.ID]
		m.attachTile(p, s[0], s[1])
	}
}

// attachTile opens a live, tile-sized emulator for one member and subscribes to
// its output. A reader forwards the emulator's input side to the panel's PTY, so
// the focused tile can be typed into during interact mode; outside it no keys are
// fed, so the tile only ever relays the query replies a live program emits. The
// shared per-member step of building the split and of reconciling it.
func (m *model) attachTile(p panel.Panel, emuCols, emuRows int) {
	m.groupEmus[p.ID] = m.attachEmu(p.ID, emuCols, emuRows)
}

// attachEmu opens a live, correctly-sized emulator for a panel id: it streams the
// panel's output and forwards the emulator's input side to the PTY (zoomReader), so
// keys fed to it reach the program. This is the shared attach handshake behind the
// split tiles and the floating scratch pane; the caller owns where the returned
// emulator is stored.
func (m model) attachEmu(id string, emuCols, emuRows int) *vt.SafeEmulator {
	emu := vt.NewSafeEmulator(emuCols, emuRows)
	go zoomReader(emu, m.client, id)
	m.sendf(proto.Command{Action: "panel.resize", ID: id, Rows: emuRows, Cols: emuCols})
	m.sendf(proto.Command{Action: "panel.attach", ID: id})
	return emu
}

// groupShownN is the group's visible-tile count N: how many members stream as
// live tiles before the rest fold into the summary tile. It comes from the
// snapshot's per-group Shown (carried in m.groupShown), defaulting to
// maxGroupTiles when the server has not set one, and is clamped to [1,
// maxGroupTiles] so it can never ask for more tiles than the hard cap allows.
func (m model) groupShownN() int {
	n, ok := m.groupShown[m.groupName]
	if !ok {
		n = maxGroupTiles
	}
	return max(1, min(n, maxGroupTiles))
}

// groupLayoutName is the split arrangement the current group opens with: the
// server-owned per-group choice (m.groupLayout), else the user's configured
// default (tuiCfg.DefaultLayout), else the built-in "tiled" even grid.
func (m model) groupLayoutName() string {
	if n, ok := m.groupLayout[m.groupName]; ok && n != "" {
		return n
	}
	if d := m.tuiCfg.DefaultLayout; d != "" {
		return d
	}
	return layoutTiled
}

// availableLayouts is the cycle order for the layout key: the built-in presets
// followed by any custom TUI.yaml layouts not shadowing a preset name, so L walks
// every arrangement the user can pick.
func (m model) availableLayouts() []string {
	out := append([]string(nil), presetLayouts...)
	seen := map[string]bool{}
	for _, n := range out {
		seen[n] = true
	}
	for _, l := range m.tuiCfg.Layouts {
		if l.Name != "" && !seen[l.Name] {
			out = append(out, l.Name)
			seen[l.Name] = true
		}
	}
	return out
}

// layoutRects resolves the current group's layout to one tileRect per cell (the
// live tiles plus the summary slot when any member is collapsed), or ok=false for
// the even-grid default. The render, attach, and resize paths share it so a tile's
// emulator is always sized to the box it is drawn in.
func (m model) layoutRects() ([]tileRect, bool) {
	name := m.groupLayoutName()
	var colW, rowW []float64
	if r, ok := m.groupRatios[m.groupName]; ok && r.layout == name {
		colW, rowW = r.cols, r.rows // manual resize weights apply only to their own layout
	}
	return resolveLayout(name, m.tuiCfg.Layouts, m.gridCells(), m.width, m.height-1-groupHeaderRows, colW, rowW)
}

// tileHitRects returns one rect per cell for hit-testing — the layout's rects for a
// resolved layout, or the even grid's rects reconstructed from its geometry. Unlike
// layoutRects it always returns a set, so a mouse click resolves under any layout.
func (m model) tileHitRects() []tileRect {
	if rects, ok := m.layoutRects(); ok {
		return rects
	}
	cols, emuCols, emuRows := m.tileGeometry()
	tileW, tileH := emuCols+4, emuRows+3 // border (2) + padding (2); border (2) + head (1)
	n := m.gridCells()
	rects := make([]tileRect, n)
	for i := 0; i < n; i++ {
		r, c := i/cols, i%cols
		rects[i] = tileRect{x: c * (tileW + gtileGap), y: r * tileH, w: tileW, h: tileH, emuCols: emuCols, emuRows: emuRows}
	}
	return rects
}

// tileAtPoint maps a screen point to the focus index of the tile under it, or false
// when it falls on the header, a gap, or past the grid. The grid sits below the
// split header, so the point is shifted into the grid's own coordinates first.
func (m model) tileAtPoint(x, y int) (int, bool) {
	gy := y - groupHeaderRows
	if gy < 0 {
		return 0, false
	}
	for i, r := range m.tileHitRects() {
		if x >= r.x && x < r.x+r.w && gy >= r.y && gy < r.y+r.h {
			return i, true
		}
	}
	return 0, false
}

// tileEmuSize is the emulator size for the live tile at focus-order index i under
// the current layout — the rect's inner size for a resolved layout, else the
// uniform even-grid size. The shared source for attaching and resizing each tile.
func (m model) tileEmuSize(i int) (cols, rows int) {
	if rects, ok := m.layoutRects(); ok && i >= 0 && i < len(rects) {
		return rects[i].emuCols, rects[i].emuRows
	}
	_, ec, er := m.tileGeometry()
	return ec, er
}

// tileEmuSizes maps each live tile's panel id to its emulator size under the
// current layout, so attach/resize size every tile to the box it occupies without
// re-deriving its index.
func (m model) tileEmuSizes() map[string][2]int {
	tiles := m.tileMembers()
	out := make(map[string][2]int, len(tiles))
	for i, p := range tiles {
		c, r := m.tileEmuSize(i)
		out[p.ID] = [2]int{c, r}
	}
	return out
}

// cycleGroupLayout advances the group's layout by delta through availableLayouts,
// optimistically setting it (so the split reflows at once) and sending group.layout
// so the server owns and persists the choice. The tiles resize to the new boxes
// immediately; the next snapshot reconciles the local guess. A no-op in the summary
// sub-view, which shows a fixed scoped grid, not a chosen layout.
func (m model) cycleGroupLayout(delta int) model {
	if m.summaryScope {
		return m
	}
	avail := m.availableLayouts()
	if len(avail) == 0 {
		return m
	}
	cur := m.groupLayoutName()
	idx := 0
	for i, n := range avail {
		if n == cur {
			idx = i
			break
		}
	}
	next := avail[wrapIndex(idx, delta, len(avail))]
	if m.groupLayout == nil {
		m.groupLayout = map[string]string{}
	}
	m.groupLayout[m.groupName] = next
	m.sendf(proto.Command{Action: "group.layout", Group: m.groupName, Layout: next})
	m.resizeGroupTiles() // re-fit every tile's emulator to the new layout's boxes
	m.status = "layout · " + next
	return m
}

// enterResize arms resize mode, in which the arrow keys grow and shrink the
// focused tile. It needs a resolved (non-"tiled") layout to skew — the even grid
// has no per-track sizing to adjust — so it points the user at the layout key when
// the group is on the default grid, and no-ops in the summary sub-view.
func (m model) enterResize() model {
	if m.summaryScope {
		m.status = "resize is not available in the summary"
		return m
	}
	if _, ok := m.layoutRects(); !ok {
		m.status = "resize needs a split layout — press " + keyLabel(keyLayout) + " to pick one"
		return m
	}
	m.groupResize = true
	m.groupArmed = false
	m.status = fmt.Sprintf("resize · %s · arrows grow/shrink · %s or esc to finish", m.groupName, keyLabel(keyResize))
	return m
}

// exitResize returns the split to passive navigation, leaving the ratios in place
// so the sizing sticks until the layout is cycled (or the group is left).
func (m model) exitResize() model {
	m.groupResize = false
	m.status = "group · " + m.groupName
	return m
}

// resizeFocused grows or shrinks the focused tile by shifting weight across one of
// its boundaries: dCol ±1 widens/narrows it (borrowing from the column to its
// right, or the left when it is already flush right), dRow ±1 does the same
// vertically. The candidate weights are validated by re-resolving the layout — a
// nudge that would make any tile too small to render is refused rather than
// committed — so resize can never drop the split back to the even grid. A no-op on
// the summary slot (not a real tile) or an unresolved layout.
func (m model) resizeFocused(dCol, dRow int) model {
	name := m.groupLayoutName()
	rows, cols, spans, ok := layoutSpans(name, m.tuiCfg.Layouts, m.gridCells())
	if !ok || m.groupFocus < 0 || m.groupFocus >= len(spans) {
		return m
	}
	sp := spans[m.groupFocus]
	r := m.ratiosFor(name, rows, cols) // a fresh copy sized to this layout, all-1.0 default

	if dCol != 0 && cols > 1 {
		edge, nb := sp.c1-1, sp.c1 // grow the tile's right column, shrink its right neighbour
		if nb >= cols {            // already flush right: borrow from the left instead
			edge, nb = sp.c0, sp.c0-1
		}
		shiftWeight(r.cols, edge, nb, float64(dCol)*resizeStep)
	}
	if dRow != 0 && rows > 1 {
		edge, nb := sp.r1-1, sp.r1
		if nb >= rows {
			edge, nb = sp.r0, sp.r0-1
		}
		shiftWeight(r.rows, edge, nb, float64(dRow)*resizeStep)
	}

	// Validate with the spans already resolved above rather than re-resolving: a
	// nudge that would make any tile too small to render is refused, so resize can
	// never drop the split back to the even grid.
	if _, valid := spansToRects(rows, cols, spans, m.width, m.height-1-groupHeaderRows, r.cols, r.rows); !valid {
		m.status = "can't resize any further"
		return m
	}
	if m.groupRatios == nil {
		m.groupRatios = map[string]splitRatios{}
	}
	m.groupRatios[m.groupName] = r
	m.resizeGroupTiles() // refit every tile's emulator + PTY to its new box
	m.status = "resize · " + m.groupName
	return m
}

// ratiosFor returns a fresh splitRatios for the current group sized to a layout's
// rows×cols, seeded from any stored set for the same layout (else all-1.0). The
// copy means an adjustment mutates the returned slices, not the stored ones, so a
// rejected nudge leaves the committed sizing untouched.
func (m model) ratiosFor(layout string, rows, cols int) splitRatios {
	r, ok := m.groupRatios[m.groupName]
	if !ok || r.layout != layout {
		r = splitRatios{layout: layout}
	}
	return splitRatios{layout: layout, cols: fitWeights(r.cols, cols), rows: fitWeights(r.rows, rows)}
}

// fitWeights returns a length-n weight slice seeded from w — padded with 1.0 where
// w is short or non-positive, truncated where it is long — so a weight set carried
// over from a different track count is coerced to the current one.
func fitWeights(w []float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		if i < len(w) && w[i] > 0 {
			out[i] = w[i]
		} else {
			out[i] = 1
		}
	}
	return out
}

// shiftWeight moves amt of weight from track b into track a, in place, refusing the
// move if it would push either below resizeMinWeight — so a tile never inverts or
// collapses. Out-of-range indices are ignored (e.g. a tile already at the grid
// edge has no neighbour to borrow from).
func shiftWeight(w []float64, a, b int, amt float64) {
	if a < 0 || a >= len(w) || b < 0 || b >= len(w) {
		return
	}
	na, nb := w[a]+amt, w[b]-amt
	if na < resizeMinWeight || nb < resizeMinWeight {
		return
	}
	w[a], w[b] = na, nb
}

// handleGroupResizeKey drives resize mode: the arrows (or hjkl) grow and shrink the
// focused tile, tab moves the focus to a different tile to resize, and z / esc
// leave. The prefix is not consumed here — a leader press just falls through to the
// caller after resize exits — keeping the mode a thin, self-contained loop.
func (m model) handleGroupResizeKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case keyResize, "esc", "enter":
		return m.exitResize(), nil
	case "right", "l":
		return m.resizeFocused(1, 0), nil
	case "left", "h":
		return m.resizeFocused(-1, 0), nil
	case "down", "j":
		return m.resizeFocused(0, 1), nil
	case "up", "k":
		return m.resizeFocused(0, -1), nil
	case "tab":
		m.groupFocus = wrapIndex(m.groupFocus, 1, m.focusCount())
	case "shift+tab":
		m.groupFocus = wrapIndex(m.groupFocus, -1, m.focusCount())
	}
	return m, nil
}

// splitMembers partitions the group into the live tiles and the collapsed
// members (folded into the summary tile), in fleet order, from a single fleet
// scan. N is the group's visible count (groupShownN):
//
//   - len(all) <= N → everything is a tile, nothing collapsed; pins are moot.
//   - over N with any pins → the pinned panels are the tiles and everyone else
//     collapses, so you curate a few to watch live and summarise the rest.
//   - over N with no pins → the first N are tiles and the rest collapse, the
//     sensible default before any curation.
//
// In the summary sub-view (summaryScope) groupMembers already returns just the
// collapsed set, so every member shows as a tile (capped at maxGroupTiles) and
// nothing collapses again — no nested summary.
func (m model) splitMembers() (tiles, collapsed []panel.Panel) {
	if m.summaryScope {
		// The sub-view shows the collapsed set (groupMembers is already scoped to it)
		// as tiles, capped at the hard ceiling; any remainder past the cap is noted
		// in the status and never re-summarised — no nested summary.
		all := m.groupMembers()
		if len(all) > maxGroupTiles {
			return all[:maxGroupTiles], nil
		}
		return all, nil
	}
	return m.partitionGroup()
}

// tileMembers is the group's live tiles in focus order — the panels that hold a
// live emulator. The summary tile, when present, is not a panel: it occupies one
// extra focus slot AFTER these (see focusCount / focusedIsSummary), so the cursor
// walks tiles[0..n) then the summary.
func (m model) tileMembers() []panel.Panel {
	tiles, _ := m.splitMembers()
	return tiles
}

// childGroups is the immediate sub-groups of the current scope (m.groupName), in
// fleet order of first appearance, each carrying its whole subtree for the rollup.
// None in the summary sub-view, which shows only the overflow of direct panels.
func (m model) childGroups() []childGroup {
	if m.summaryScope {
		return nil
	}
	return childGroupsOf(m.fleet, m.groupName)
}

// tileKind classifies a split focus slot: a live member panel, an immediate
// sub-group (descendable), or the overflow summary of direct panels past N.
type tileKind int

const (
	tilePanel tileKind = iota
	tileGroup
	tileSummary
)

// splitTile is one focus slot of the split, in display order; only the field for its
// kind is set. Reifying the slots into one ordered list keeps the slot order in a
// single place — focusCount, the focus classification, and the render all index this
// list rather than each re-deriving boundaries from len(tiles)/len(children).
type splitTile struct {
	kind      tileKind
	panel     panel.Panel   // tilePanel
	child     childGroup    // tileGroup
	collapsed []panel.Panel // tileSummary
}

// splitSlots builds the ordered focus slots for the current scope: the live panel
// tiles, then one tile per immediate sub-group, then the overflow summary when direct
// panels spill past N.
func (m model) splitSlots() []splitTile {
	tiles, collapsed := m.splitMembers()
	children := m.childGroups()
	slots := make([]splitTile, 0, len(tiles)+len(children)+1)
	for _, p := range tiles {
		slots = append(slots, splitTile{kind: tilePanel, panel: p})
	}
	for _, cg := range children {
		slots = append(slots, splitTile{kind: tileGroup, child: cg})
	}
	if len(collapsed) > 0 {
		slots = append(slots, splitTile{kind: tileSummary, collapsed: collapsed})
	}
	return slots
}

// focusedSlot resolves the focus to its slot, false when out of range.
func (m model) focusedSlot() (splitTile, bool) {
	slots := m.splitSlots()
	if m.groupFocus < 0 || m.groupFocus >= len(slots) {
		return splitTile{}, false
	}
	return slots[m.groupFocus], true
}

// focusCount is how many slots the focus walks: the live panel tiles, the sub-group
// tiles, and the summary tile when direct panels overflow. tab / shift-tab wrap over
// this.
func (m model) focusCount() int {
	return len(m.splitSlots())
}

// focusedChildGroup resolves the focus to a sub-group tile, reporting false
// elsewhere. enter descends into it.
func (m model) focusedChildGroup() (childGroup, bool) {
	if s, ok := m.focusedSlot(); ok && s.kind == tileGroup {
		return s.child, true
	}
	return childGroup{}, false
}

// clampGroupFocus keeps the focus within the current slot count (live tiles plus
// the summary slot, if any). reconcileGroupTiles already clamps on every fleet
// update; this is a cheap guard at the render entry so no path that changes the
// member set can leave the focus pointing past the end.
func (m *model) clampGroupFocus() {
	if n := m.focusCount(); n > 0 {
		m.groupFocus = max(0, min(m.groupFocus, n-1))
	} else {
		m.groupFocus = 0
	}
}

// focusedIsSummary reports whether the focus rests on the summary tile — the
// extra slot past the last live tile, present only when some member is collapsed.
// The pin / interact / signal / remove actions no-op on it, and enter zooms it.
func (m model) focusedIsSummary() bool {
	s, ok := m.focusedSlot()
	return ok && s.kind == tileSummary
}

// pinnedCount is how many of the parent group's members are pinned to a live
// tile. It counts the full group (fleetGroup), not the scoped view, so the pin cap
// holds the same in the summary sub-view as in the group view.
func (m model) pinnedCount() int {
	n := 0
	for _, p := range m.fleetGroup() {
		if m.groupPinned[p.ID] {
			n++
		}
	}
	return n
}

// focusedMember resolves the focus to its tile's panel, reporting false when the
// focus is out of range OR rests on the summary slot (which is not a panel). The
// single bounds check the pin, interact, remove, signal, and zoom actions share.
func (m model) focusedMember() (panel.Panel, bool) {
	if s, ok := m.focusedSlot(); ok && s.kind == tilePanel {
		return s.panel, true
	}
	return panel.Panel{}, false
}

// focusedMemberID is the id of the panel the focus rests on, read before a
// snapshot so reconcileGroupTiles can keep the focus on the same panel as the
// roster shifts. Empty when the focus is out of range.
func (m model) focusedMemberID() string {
	if p, ok := m.focusedMember(); ok {
		return p.ID
	}
	return ""
}

// reconcileGroupTiles brings the split's live tiles in line with the current
// membership and pin set — after a snapshot, or after a pin toggle: it leaves an
// emptied group for the dashboard, attaches newly-live members, tears down those
// that left the tile set (removed from the group, or demoted to the tree), and
// keeps the focus on the same panel (by id) across both regions. A no-op without
// a client. focusID is the panel the focus rested on before the change.
func (m *model) reconcileGroupTiles(focusID string) {
	tiles, collapsed := m.splitMembers()
	if len(tiles)+len(collapsed) == 0 && len(m.childGroups()) == 0 {
		// The scope lost its last panel AND has no sub-groups left: leave for the
		// dashboard. A container group with only sub-groups still renders (its
		// sub-group tiles), so it does not bail.
		m.resetToDashboard("group emptied · dashboard")
		return
	}

	// Live tiles only exist with a client attached; without one the split shows
	// previews, so there is nothing to attach or tear down.
	if m.client != nil {
		if m.groupEmus == nil {
			m.groupEmus = make(map[string]*vt.SafeEmulator)
		}
		want := make(map[string]bool, len(tiles))
		for _, p := range tiles {
			want[p.ID] = true
		}

		changed := false
		// Drop tiles whose panel left the tile set (removed, or demoted to the tree).
		for id, emu := range m.groupEmus {
			if !want[id] {
				m.sendf(proto.Command{Action: "panel.detach", ID: id})
				closeZoom(emu)
				delete(m.groupEmus, id)
				changed = true
			}
		}
		// Attach a tile for each newly-live member, sized to its box in the layout.
		sizes := m.tileEmuSizes()
		for _, p := range tiles {
			if m.groupEmus[p.ID] == nil {
				s := sizes[p.ID]
				m.attachTile(p, s[0], s[1])
				changed = true
			}
		}
		// A changed tile set reflows the grid, so refit every existing tile too.
		if changed {
			m.resizeGroupTiles()
		}
	}
	// Keep the focus on the same tile by id; fall back to clamping into range
	// (tiles plus the summary slot) when that panel left the tile set — it may have
	// been removed, or folded into the summary, in which case the focus lands on the
	// nearest remaining slot rather than off the end.
	if idx := indexOfMember(tiles, focusID); idx >= 0 {
		m.groupFocus = idx
	} else {
		m.groupFocus = max(0, min(m.groupFocus, m.focusCount()-1))
	}
	// Interact needs a live tile: stop if the panel being typed into (focusID) is
	// no longer one — removed, or folded into the summary — so keys never land on
	// whatever panel the focus clamped onto instead.
	if m.groupInteract && indexOfMember(tiles, focusID) < 0 {
		m.groupInteract = false
		m.status = "interact ended · panel is no longer a live tile"
	}
}

// indexOfMember is the position of the panel with id in members, or -1.
func indexOfMember(members []panel.Panel, id string) int {
	if id == "" {
		return -1
	}
	for i, p := range members {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// resizeGroupTiles re-sizes every tile emulator and its panel's PTY to the
// current geometry, so the split reflows when the window — and thus the space
// above the footer bar — changes.
func (m *model) resizeGroupTiles() {
	sizes := m.tileEmuSizes()
	// groupEmus already holds exactly the live tiles, keyed by id, so resize them
	// directly rather than re-scanning the fleet for members. Each tile is sized to
	// its own box, which may differ under a spanned layout.
	for id, emu := range m.groupEmus {
		s, ok := sizes[id]
		if !ok {
			continue
		}
		emu.Resize(s[0], s[1])
		m.sendf(proto.Command{Action: "panel.resize", ID: id, Rows: s[1], Cols: s[0]})
	}
}

// closeGroupEmus tears down every tile emulator, which stops its drain goroutine
// (Read returns EOF once the pipe closes).
func (m *model) closeGroupEmus() {
	for _, emu := range m.groupEmus {
		closeZoom(emu)
	}
	m.groupEmus = nil
}

// groupMembers is the panels the split currently navigates, in fleet order. In
// the normal view it is every member of m.groupName; in the summary sub-view
// (summaryScope) it is just the parent group's collapsed set, so the same tile
// machinery lays the summarised members out as their own grid.
func (m model) groupMembers() []panel.Panel {
	if m.summaryScope {
		_, collapsed := m.partitionGroup()
		return collapsed
	}
	return m.fleetGroup()
}

// fleetGroup is the raw panels of m.groupName in fleet order — the parent group's
// full membership, before any tile/summary partition.
func (m model) fleetGroup() []panel.Panel {
	var out []panel.Panel
	for _, p := range m.fleet {
		if p.Group == m.groupName {
			out = append(out, p)
		}
	}
	return out
}

// subtreeMembers is every panel in the current scope's subtree — its direct panels
// and all nested descendants — the set the group-wide signal (S) recurses over, to
// match dispatch-group and the documented subtree semantics.
func (m model) subtreeMembers() []panel.Panel {
	var out []panel.Panel
	for _, p := range m.fleet {
		if panel.GroupIsUnder(m.groupName, p.Group) {
			out = append(out, p)
		}
	}
	return out
}

// partitionGroup splits the parent group's full membership into the live tiles
// and the collapsed (summarised) set, by the same N/pins rules splitMembers
// applies — but always against the raw fleetGroup, never the scoped view. It is
// the seam the summary sub-view reuses: entering the summary scopes the view to
// the collapsed half this returns, without splitMembers recursing on itself.
func (m model) partitionGroup() (tiles, collapsed []panel.Panel) {
	all := m.fleetGroup()
	n := m.groupShownN()
	if len(all) <= n {
		return all, nil
	}
	pins := 0
	for _, p := range all {
		if m.groupPinned[p.ID] {
			pins++
		}
	}
	for i, p := range all {
		switch {
		case pins > 0:
			if m.groupPinned[p.ID] {
				tiles = append(tiles, p)
			} else {
				collapsed = append(collapsed, p)
			}
		case i < n:
			tiles = append(tiles, p)
		default:
			collapsed = append(collapsed, p)
		}
	}
	return tiles, collapsed
}

// handleGroupZoomKey drives the split: cycle the focused tile, zoom into it, or
// leave. Movement wraps so tab walks the whole group.
func (m model) handleGroupZoomKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Interact mode hands the keyboard to the focused tile; the prefix is still the
	// only way back out, exactly as in a zoom.
	if m.groupInteract {
		return m.handleGroupInteractKey(k)
	}
	// Resize mode captures the arrows to grow/shrink the focused tile until z / esc.
	if m.groupResize {
		return m.handleGroupResizeKey(k)
	}
	key := k.String()
	// The split is command-mode, so the prefix is only needed for the universal
	// escapes — C-t d leaves for the dashboard; bare b (back) does the same.
	if m.groupArmed {
		m.groupArmed = false
		if b, ok := m.lookupEscape(key); ok {
			switch b.act {
			case actDashboard:
				return m.exitGroupZoom()
			case actEditMap:
				return m.openEditMap(modeGroupZoom), nil
			case actScroll: // C-t [ → scroll the focused tile's history
				return m.enterScroll(), nil
			case actScratch: // C-t ~ → float the scratch pane over the split
				return m.toggleScratch()
			}
		}
		if key == m.bindingKey(actDetach) { // C-t q detaches from the split too
			return m.runAction(actDetach)
		}
		if b, ok := m.lookupCmd(key); ok && b.act == actReload { // C-t R → reload config
			return m.runAction(actReload)
		}
		if b, ok := m.lookupCmd(key); ok && b.act == actSearch { // C-t f → search the focused tile's scrollback
			return m.openSearch(), nil
		}
		return m, nil
	}
	if key == m.effPrefix() {
		m.groupArmed = true
		return m, nil
	}
	// The dashboard key (d) jumps straight out of the split, skipping any levels;
	// esc goes back just one level (summary → group, sub-group → parent, top → out).
	if key == m.bindingKey(actDashboard) {
		return m.exitGroupZoom()
	}
	if key == "esc" {
		return m.popGroupLevel()
	}
	// Focus walks the live tiles, then the sub-group tiles, then the summary slot, so
	// a large group's overflow and its sub-groups are all reachable, not stranded.
	n := m.focusCount()
	// The per-member actions need a real panel under the focus; on the summary or a
	// sub-group tile (neither is a panel) they no-op with a hint rather than acting on
	// nothing.
	if m.focusedIsSummary() {
		switch key {
		case keyPin, keySignal, keyRemove, keyInteract, keyDiff, keyRespawn:
			m.status = "not available on the summary"
			return m, nil
		}
	}
	if _, onGroup := m.focusedChildGroup(); onGroup {
		switch key {
		case keyPin, keySignal, keyRemove, keyInteract, keyDiff, keyRespawn:
			m.status = "not available on a sub-group — enter to descend"
			return m, nil
		}
	}
	switch key {
	case "tab", "right", "l", "down", "j":
		m.groupFocus = wrapIndex(m.groupFocus, 1, n)
		m.scrollOff = 0 // scrollback follows the focus; a new tile starts at its bottom
	case "shift+tab", "left", "h", "up", "k":
		m.groupFocus = wrapIndex(m.groupFocus, -1, n)
		m.scrollOff = 0
	case "shift+left":
		return m.reorderGroupMember(-1), nil
	case "shift+right":
		return m.reorderGroupMember(1), nil
	case "+", "=":
		return m.adjustGroupShown(1), nil
	case "-", "_":
		return m.adjustGroupShown(-1), nil
	case keyLayout:
		// Cycle the split arrangement: tiled → main-vertical → main-horizontal →
		// stack → any custom TUI.yaml layouts → back to tiled.
		return m.cycleGroupLayout(1), nil
	case keyResize:
		// Enter resize mode: the arrows then grow/shrink the focused tile, skewing the
		// current split layout's tracks (a no-op on the even "tiled" grid).
		return m.enterResize(), nil
	case keyPin:
		return m.togglePin(), nil
	case keySignal:
		// Bare s signals the focused member, like the split's other keys (x, i,
		// enter) act on the focus; S signals the whole group.
		p, ok := m.focusedMember()
		if !ok {
			return m, nil
		}
		if p.State == panel.Exited {
			m.status = p.Title + " has exited — nothing to signal"
			return m, nil
		}
		return m.openSignalPicker(modeGroupZoom, []string{p.ID}, p.Title), nil
	case keySignalAll:
		// S signals the whole subtree of the current scope — nested sub-groups
		// included, matching dispatch-group and the docs — not just this level's
		// direct panels; the summary sub-view signals the set it shows.
		members := m.subtreeMembers()
		if m.summaryScope {
			members = m.groupMembers()
		}
		ids := liveIDs(members)
		scope := fmt.Sprintf("%s (%d panels)", groupBreadcrumb(m.groupName), len(ids))
		return m.openSignalPicker(modeGroupZoom, ids, scope), nil
	case keyDiff:
		// Bare D pops up the work-tree diff of the focused member, like s signals it.
		return m.runAction(actDiff)
	case keyRespawn:
		// Bare r re-runs the focused member if it has exited — the split's per-tile
		// counterpart to r on a dashboard panel.
		return m.runAction(actRespawn)
	case keyBack:
		// Bare b goes back one level — the summary to its group, a sub-group to its
		// parent, the top group to the dashboard — the same pop esc performs.
		return m.popGroupLevel()
	case keyRemove:
		return m.removeFocusedMember(), nil
	case keyInteract:
		return m.enterInteract(), nil
	case keyHelp:
		return m.openHelp(modeGroupZoom), nil
	case keyCtrlC, keyCtrlE:
		// Captured like on the dashboard: the split exits only via detach.
		m.status = m.exitHint()
	case "enter":
		if cg, ok := m.focusedChildGroup(); ok {
			return m.rescopeGroup(cg.path), nil // descend into the sub-group
		}
		if m.focusedIsSummary() {
			return m.enterSummaryScope(), nil
		}
		return m.zoomFocusedMember()
	}
	return m, nil
}

// handleGroupInteractKey drives the focused tile while interact mode is on: every
// bare key is fed to that panel's program, and the prefix is the only escape —
// C-t i stops interacting and returns to navigation, C-t d leaves for the
// dashboard, C-t q detaches, and C-t C-t sends a literal prefix. This mirrors a
// zoom's input model, but on one tile of the split rather than a full screen.
func (m model) handleGroupInteractKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.groupArmed {
		m.groupArmed = false
		if key == m.effPrefix() {
			m.feedFocused(k) // prefix+prefix → a literal prefix to the program
			return m, nil
		}
		if key == keyInteract { // C-t i toggles interact back off
			return m.exitInteract(), nil
		}
		if b, ok := m.lookupEscape(key); ok {
			switch b.act {
			case actDashboard:
				return m.exitGroupZoom()
			case actEditMap:
				return m.openEditMap(modeGroupZoom), nil
			case actScroll: // C-t [ → scroll the focused tile's history
				return m.enterScroll(), nil
			case actScratch: // C-t ~ → float the scratch pane over the split
				return m.toggleScratch()
			}
			return m, nil
		}
		if key == m.bindingKey(actDetach) { // C-t q detaches from interact too
			return m.runAction(actDetach)
		}
		return m, nil
	}
	if key == m.effPrefix() {
		m.groupArmed = true
		return m, nil
	}
	m.scrollOff = 0 // driving the program returns the tile to its live bottom
	m.feedFocused(k)
	return m, nil
}

// enterInteract hands the keyboard to the focused tile so it can be driven in
// place, without dropping into a full single-panel zoom. It needs a live tile to
// type into, so it is a no-op on a preview-only (no client) or out-of-range focus
// (the caller already guards the summary slot, which is not a panel).
func (m model) enterInteract() model {
	p, ok := m.focusedMember()
	if !ok {
		return m
	}
	if m.groupEmus[p.ID] == nil {
		m.status = "interact needs a live panel"
		return m
	}
	m.groupInteract = true
	m.groupArmed = false
	m.scrollOff = 0 // typing happens at the live bottom
	m.status = fmt.Sprintf("interact · %s · %s %s to stop", p.Title, keyLabel(m.effPrefix()), keyInteract)
	return m
}

// exitInteract returns the split to passive navigation.
func (m model) exitInteract() model {
	m.groupInteract = false
	m.groupArmed = false
	m.status = "group · " + m.groupName
	return m
}

// feedFocused routes a keystroke to the focused tile's emulator, which encodes it
// in the program's mode; the tile's reader forwards the bytes to the PTY. A no-op
// when the focus has no live emulator.
func (m model) feedFocused(k tea.KeyMsg) {
	p, ok := m.focusedMember()
	if !ok {
		return
	}
	if emu := m.groupEmus[p.ID]; emu != nil {
		feedKey(emu, k)
	}
}

// togglePin pins or unpins the focused member. Pinning promotes a tree-listed
// panel to a live streaming tile; unpinning demotes a tile back to the list when
// the group is over the tile budget. The pin set is capped at maxGroupTiles, so
// pinning beyond it is refused. The server owns the pin flag, so each toggle is
// sent on to it (and broadcast to every client); the local set is updated
// optimistically so the tile reflows at once, then the next snapshot reconciles
// it against the authoritative flags. Reconciling attaches or tears down the
// affected tile and keeps the focus on the same panel.
func (m model) togglePin() model {
	p, ok := m.focusedMember()
	if !ok {
		return m
	}
	if m.groupPinned == nil {
		m.groupPinned = map[string]bool{}
	}
	if m.groupPinned[p.ID] {
		delete(m.groupPinned, p.ID)
		m.sendf(proto.Command{Action: "panel.unpin", IDs: []string{p.ID}})
		m.status = "unpinned " + p.Title
	} else {
		if m.pinnedCount() >= maxGroupTiles {
			m.status = fmt.Sprintf("at most %d panels can be pinned — unpin one first", maxGroupTiles)
			return m
		}
		m.groupPinned[p.ID] = true
		m.sendf(proto.Command{Action: "panel.pin", IDs: []string{p.ID}})
		m.status = "pinned " + p.Title
	}
	m.reconcileGroupTiles(p.ID) // attach/detach the affected tile, keep focus on p
	return m
}

// removeFocusedMember takes the focused tile's panel out of the group, returning
// it to the dashboard as a lone panel. The server broadcasts a fresh snapshot,
// and the split reconciles its tiles on the next applyEvent.
func (m model) removeFocusedMember() model {
	p, ok := m.focusedMember()
	if !ok {
		return m
	}
	m.sendf(proto.Command{Action: "panel.ungroup", IDs: []string{p.ID}})
	m.status = "removed " + p.Title + " from the group"
	return m
}

// adjustGroupShown nudges the group's visible-tile count N by delta, clamped to
// [1, maxGroupTiles]. The new N is set optimistically in m.groupShown for instant
// feedback (the grid reflows at once, summarising the spillover), then sent to the
// server as group.show so it owns the count and broadcasts it to every client; the
// next snapshot reconciles the local guess against the authoritative value. A
// no-op in the summary sub-view, which shows a fixed scoped set, not a dialled one.
func (m model) adjustGroupShown(delta int) model {
	if m.summaryScope {
		return m // the sub-view's tile set is the parent's collapsed half, not dialled
	}
	newN := max(1, min(m.groupShownN()+delta, maxGroupTiles))
	if m.groupShown == nil {
		m.groupShown = map[string]int{}
	}
	m.groupShown[m.groupName] = newN
	delete(m.groupRatios, m.groupName) // a new N reshapes the grid; manual weights reset
	m.sendf(proto.Command{Action: "group.show", Group: m.groupName, Count: newN})
	m.status = fmt.Sprintf("group · %d shown", newN)
	return m
}

// enterSummaryScope opens the collapsed (summarised) members as their own even
// grid: it detaches the current tiles, scopes the view to the parent group's
// collapsed half (summaryScope), resets the focus, and re-attaches a tile per
// summarised member. The parent stays in m.groupName so esc / the dashboard key
// pop back to it. A no-op without a collapsed set to scope into.
func (m model) enterSummaryScope() model {
	_, collapsed := m.splitMembers()
	if len(collapsed) == 0 {
		return m
	}
	parent := m.groupName
	m = m.retile(func(m *model) { m.summaryScope = true }) // scope to the parent's collapsed half
	shown := m.tileMembers()
	status := fmt.Sprintf("summary · %s (%d panels)", groupBreadcrumb(parent), len(collapsed))
	if len(collapsed) > len(shown) {
		status += fmt.Sprintf(" · showing first %d", len(shown))
	}
	m.status = status
	return m
}

// exitSummaryScope returns from the summary sub-view to the parent group view: it
// re-tiles the parent group's own tiles.
func (m model) exitSummaryScope() model {
	m = m.retile(func(m *model) { m.summaryScope = false })
	m.status = "group · " + groupBreadcrumb(m.groupName)
	return m
}

// rescopeGroup re-points the split at a different path in the tree — a descend into
// a sub-group tile, or an ascend to the parent — re-tiling the new scope's direct
// panels (its sub-groups render as their own tiles). summaryScope is always cleared;
// the pin set is rebuilt for the new scope.
func (m model) rescopeGroup(path string) model {
	m = m.retile(func(m *model) {
		m.summaryScope = false
		m.groupName = path
		m.groupPinned = pinsForMembers(m.fleetGroup()) // pins are per-scope, over the new direct panels
	})
	m.status = "group · " + groupBreadcrumb(path)
	return m
}

// retile re-tiles the split at a new scope — descend, ascend, or enter/leave the
// summary sub-view. It drops the current tiles' streams, applies the caller's scope
// mutation (change groupName / toggle summaryScope / rebuild pins), and re-attaches.
// The transient per-scope modes (interact, resize) always reset, so no re-scope path
// can strand one, and the focus returns to the top.
func (m model) retile(mutate func(*model)) model {
	m.sendf(proto.Command{Action: "panel.detach"}) // drop the current scope's tiles
	m.closeGroupEmus()
	m.groupInteract = false
	m.groupResize = false
	m.groupFocus = 0
	m.scrollOff = 0
	mutate(&m)
	m.attachGroupMembers()
	return m
}

// popGroupLevel goes back one level: out of the summary sub-view to its group, up to
// the parent group when nested, or off the split to the dashboard at the top level.
// This is what esc and back (b) do; the dashboard key jumps straight out instead.
func (m model) popGroupLevel() (tea.Model, tea.Cmd) {
	if m.summaryScope {
		return m.exitSummaryScope(), nil
	}
	if parent := panel.GroupParent(m.groupName); parent != "" {
		return m.rescopeGroup(parent), nil
	}
	return m.exitGroupZoom()
}

// groupBreadcrumb renders a group path as a breadcrumb trail — "backend/api" reads
// as "backend › api" — so the header/footer show where in the tree the split sits.
func groupBreadcrumb(path string) string {
	return strings.ReplaceAll(path, panel.GroupSep, " › ")
}

// childPinnedDefault returns a sub-group's lone pinned *direct* panel — the one
// descending would auto-zoom into — when exactly one of its direct panels carries
// the server-owned Pinned flag, matching zoomGroup's single-pin default at the top
// level. ok=false on no pin, several pins, or a pin that sits deeper in the subtree.
func childPinnedDefault(cg childGroup) (panel.Panel, bool) {
	var only panel.Panel
	n := 0
	for _, p := range cg.members {
		if p.Group == cg.path && p.Pinned { // a direct panel of the sub-group, pinned
			only, n = p, n+1
		}
	}
	return only, n == 1
}

// zoomFocusedMember drops from the split into the focused panel's own live zoom,
// remembering the group so back (C-t b) returns to the split.
func (m model) zoomFocusedMember() (tea.Model, tea.Cmd) {
	p, ok := m.focusedMember()
	if !ok {
		return m, nil
	}
	origin := m.groupName
	// Drop the split's streams before the single zoom takes over input + output.
	m.sendf(proto.Command{Action: "panel.detach"}) // detach all
	m.closeGroupEmus()
	m.groupInteract = false
	m = m.zoomInto(p)
	m.zoomGroupOrigin = origin
	return m, nil
}

// resetToDashboard detaches every tile, tears down the split's emulators, and
// returns the model to a clean dashboard with the given status — the shared core
// of leaving the group view, whether by key or because the group emptied.
func (m *model) resetToDashboard(status string) {
	m.sendf(proto.Command{Action: "panel.detach"}) // detach all
	m.closeGroupEmus()
	m.mode = modeDashboard
	m.groupName = ""
	m.groupFocus = 0
	m.groupArmed = false
	m.groupInteract = false
	m.groupResize = false
	m.summaryScope = false
	m.groupPinned = nil
	m.scrollOff = 0
	m.scrolling = false
	m.copySelecting = false
	*m = m.clearSearch()
	m.status = status
}

// exitGroupZoom leaves the split for the dashboard and asks the server for a
// fresh snapshot so the fleet is current.
func (m model) exitGroupZoom() (tea.Model, tea.Cmd) {
	m.resetToDashboard("dashboard")
	if m.client != nil {
		return m, func() tea.Msg { _ = m.client.Send(proto.Command{Action: "panel.list"}); return nil }
	}
	return m, nil
}

// backToGroup returns from a single-panel zoom to the split it was launched from.
// It detaches the panel and tears down its emulator, just like a detach to the
// dashboard, but lands back on the group.
func (m model) backToGroup() (tea.Model, tea.Cmd) {
	m.sendf(proto.Command{Action: "panel.detach", ID: m.zoomID})
	closeZoom(m.emu)
	m.mode = modeGroupZoom
	m.groupName = m.zoomGroupOrigin
	m.groupArmed = false
	m.groupInteract = false
	m.summaryScope = false // back lands on the parent group, never the summary sub-view
	m.groupFocus = 0
	m.scrollOff = 0
	m.scrolling = false
	m.cursorHidden = nil
	m.emu = nil
	m.zoomID, m.zoomTitle, m.zoomArmed, m.zoomExited, m.zoomGroupOrigin = "", "", false, false, ""
	m.attachGroupMembers() // re-subscribe every tile's live stream
	m.status = "group · " + m.groupName
	return m, nil
}

// groupZoomView renders the split: a header, an even grid of live member tiles —
// with one extra summary tile as the last cell when some members are collapsed —
// and a footer pinned to the last line. In the summary sub-view the header names
// the parent and there is no summary tile (the scoped set shows in full).
func (m model) groupZoomView() string {
	slots := m.splitSlots() // the ordered focus slots, computed once for the whole view
	// Clamp the focus into range here (reconcile already clamps on fleet updates),
	// against the slots we just built rather than recomputing them.
	if n := len(slots); n > 0 {
		m.groupFocus = max(0, min(m.groupFocus, n-1))
	} else {
		m.groupFocus = 0
	}
	// The header counts derive from the slots — the shown direct panels, the sub-group
	// tiles, and the overflow set — so nothing is re-scanned.
	var tiles, collapsed []panel.Panel
	ng := 0
	for _, s := range slots {
		switch s.kind {
		case tilePanel:
			tiles = append(tiles, s.panel)
		case tileGroup:
			ng++
		case tileSummary:
			collapsed = s.collapsed
		}
	}

	caption := "GROUP"
	if m.summaryScope {
		caption = "SUMMARY"
	}
	header := sectionStyle.Render(spaced(caption)) + "  " +
		lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render(groupBreadcrumb(m.groupName)) +
		mutedStyle.Render(fmt.Sprintf("   %d panel(s)  ", len(tiles))) + kindBreakdown(tiles)
	if ng > 0 {
		header += lipgloss.NewStyle().Foreground(colBrand).Render(fmt.Sprintf("   ▣ %d sub-group(s)", ng))
	}
	if len(collapsed) > 0 {
		header += lipgloss.NewStyle().Foreground(states[panel.Idle].color).
			Render(fmt.Sprintf("   · %d live · %d summarised", len(tiles), len(collapsed)))
	}

	grid := m.renderSplitGrid(slots)
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", grid)
	placed := lipgloss.Place(m.width, m.height-1, lipgloss.Left, lipgloss.Top, body)
	return placed + "\n" + m.groupZoomFooter()
}

// renderSplitGrid lays the live tiles (plus the summary tile when members are
// collapsed) out under the group's chosen layout. The default "tiled" layout uses
// the even-grid path — uniform geometry joined by tileGrid — unchanged. Any other
// layout resolves to per-tile rects and is composited; an unknown or non-fitting
// layout falls back to the even grid, so a layout that only exists in another
// frontend's config never breaks the split here.
func (m model) renderSplitGrid(slots []splitTile) string {
	// renderSlot draws focus slot i by its kind — a panel tile, a sub-group tile, or
	// the overflow summary — sized to the box it occupies.
	renderSlot := func(i, emuCols, emuRows, margin int) string {
		s := slots[i]
		focused := i == m.groupFocus
		switch s.kind {
		case tilePanel:
			return m.renderTile(s.panel, focused, emuCols, emuRows, margin)
		case tileGroup:
			return m.renderGroupTile(s.child, focused, emuCols, emuRows, margin)
		default:
			return m.renderSummaryTile(s.collapsed, focused, emuCols, emuRows, margin)
		}
	}

	if rects, ok := m.layoutRects(); ok {
		rendered := make([]string, 0, len(rects))
		for i := 0; i < len(slots) && i < len(rects); i++ {
			rendered = append(rendered, renderSlot(i, rects[i].emuCols, rects[i].emuRows, 0))
		}
		return composeTiles(rects[:len(rendered)], rendered, m.width, m.height-1-groupHeaderRows)
	}

	// The even-grid path (the "tiled" default).
	cols, emuCols, emuRows := m.tileGeometry()
	rendered := make([]string, 0, len(slots))
	for i := range slots {
		rendered = append(rendered, renderSlot(i, emuCols, emuRows, gtileGap))
	}
	return tileGrid(rendered, cols)
}

// rollupRow is one roster line of a rollup tile: a member (or an immediate
// sub-sub-group) name and its already-styled LED — a state LED for a panel, a ▣
// for a nested group — so renderRollupTile lays the roster out without re-styling.
type rollupRow struct {
	led  string // already styled
	name string
}

// renderRollupTile draws a non-panel tile — a sub-group or the overflow summary — as
// a glyph-and-title head over a fixed-height body: the state chips, a roster of the
// members it rolls up, and a centred descend hint pinned to the bottom. The box
// matches renderTile (size, padding, brand glow when focused) so it sits flush in the
// grid, and the body is always exactly emuRows tall so every cell is the same height.
// pinned prefixes a ⊙ before the glyph, exactly as renderTile marks a pinned panel.
func (m model) renderRollupTile(glyph string, glyphColor lipgloss.Color, pinned bool, title, countRight, chips string, roster []rollupRow, footer string, focused bool, emuCols, emuRows, marginRight int) string {
	border, titleColor := colFaint, colInk
	if focused {
		border, titleColor = colBrand, colBrandHi
	}
	// The head's leading markers first — an optional pin glyph, then the tile glyph —
	// then the title in whatever width is left of the right-aligned count, so a long
	// name never wraps the head onto a second row.
	prefix := lipgloss.NewStyle().Foreground(glyphColor).Bold(true).Render(glyph) + " "
	if pinned {
		prefix = lipgloss.NewStyle().Foreground(colBrandHi).Render("⊙") + " " + prefix
	}
	titleStyle := lipgloss.NewStyle().Foreground(titleColor).Bold(true)
	// Right-align the count only when the prefix, count, and at least one title cell
	// all fit; otherwise drop the count so a wide count (plus a ⊙ prefix) can never
	// force the head past emuCols and wrap it onto a second row.
	var head string
	if avail := emuCols - lipgloss.Width(prefix) - lipgloss.Width(countRight) - 1; countRight != "" && avail >= 1 {
		head = padEnds(prefix+titleStyle.Render(truncate(title, avail)), countRight, emuCols)
	} else {
		head = prefix + titleStyle.Render(truncate(title, max(1, emuCols-lipgloss.Width(prefix))))
	}
	head = clampWidth(head, emuCols) // final guard: a tiny tile's prefix alone must not wrap
	body := rollupBody(chips, roster, footer, emuCols, emuRows)
	return paneBox(emuCols, marginRight, border).Render(lipgloss.JoinVertical(lipgloss.Left, head,
		lipgloss.JoinVertical(lipgloss.Left, body...)))
}

// rollupBody composes a rollup tile's body into exactly emuRows lines: the chips on
// line 0, a blank line, then the roster, with the footer centred on the last line and
// the gap between filled with blanks. It degrades gracefully as emuRows shrinks —
// dropping the roster first, then the footer, always keeping the chips — and never
// emits more than emuRows lines.
func rollupBody(chips string, roster []rollupRow, footer string, emuCols, emuRows int) []string {
	body := make([]string, emuRows)
	if emuRows < 1 {
		return body
	}
	body[0] = truncate(chips, emuCols) // chips carry ANSI, but truncate only ever shortens — safe against a wrap
	// The footer sits on the last line once there is room for it below the chips and
	// the blank spacer (emuRows >= 3); below that it is dropped and only the chips show.
	footerRow := -1
	if footer != "" && emuRows >= 3 {
		footerRow = emuRows - 1
		body[footerRow] = centerText(mutedStyle.Render(truncate(footer, emuCols)), emuCols)
	}
	// The roster fills line 2 up to (but not including) the footer, leaving line 1 blank.
	end := emuRows
	if footerRow >= 0 {
		end = footerRow
	}
	writeRoster(body, roster, 2, end, emuCols)
	return body
}

// writeRoster lays roster rows into body[start:end], one per line. When there are
// more rows than fit, the last shown line becomes a muted "+K more" so the overflow
// is never silently dropped.
func writeRoster(body []string, roster []rollupRow, start, end, emuCols int) {
	capacity := end - start
	if capacity < 1 || len(roster) == 0 {
		return
	}
	if len(roster) <= capacity {
		for i, r := range roster {
			body[start+i] = rollupLine(r, emuCols)
		}
		return
	}
	// More rows than fit: show capacity-1 and fold the rest into a "+K more" line.
	shown := capacity - 1
	for i := 0; i < shown; i++ {
		body[start+i] = rollupLine(roster[i], emuCols)
	}
	body[start+shown] = mutedStyle.Render(truncate(fmt.Sprintf("+%d more", len(roster)-shown), emuCols))
}

// padEnds lays left flush-left and right flush-right within width columns, with at
// least one space between — the visible width of each is measured so styled (ANSI)
// segments still align.
func padEnds(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// centerText left-pads s so it sits centred within width columns; the box's own
// width fills the trailing space, so no right pad is needed.
func centerText(s string, width int) string {
	if pad := (width - lipgloss.Width(s)) / 2; pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}

// rollupLine renders one roster row: a "· " bullet and the leaf name flush-left, its
// LED flush-right, clamped to emuCols so the forced minimum gap can't overflow a very
// narrow tile (emuCols <= 4) and wrap the row.
func rollupLine(r rollupRow, emuCols int) string {
	name := truncate(r.name, max(1, emuCols-3-lipgloss.Width(r.led)))
	return clampWidth(padEnds("· "+name, r.led, emuCols), emuCols)
}

// clampWidth truncates a possibly-styled string to at most width visible cells,
// preserving its ANSI styling — the final guard that a composed head or roster line
// can never exceed the tile's inner width and wrap onto a second row.
func clampWidth(s string, width int) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "")
}

// renderGroupTile draws an immediate sub-group as one descendable tile: a ▣ glyph in
// the sub-group's rolled-up state colour, its leaf name and a compact count in the
// head, its state chips, a roster of its immediate children, and a ↵ descend hint. A
// ⊙ marks a sub-group holding a lone pinned default (enter drops straight into it).
func (m model) renderGroupTile(cg childGroup, focused bool, emuCols, emuRows, marginRight int) string {
	count := fmt.Sprintf("%d", len(cg.members))
	if n := subGroupCount(cg.members, cg.path); n > 0 {
		count += fmt.Sprintf(" · ▣%d", n)
	}
	// The roster is the sub-group's immediate children: its direct panels first (leaf =
	// title, state LED), then its immediate sub-sub-groups (leaf = group name, a ▣ LED).
	var roster []rollupRow
	for _, p := range cg.members {
		if p.Group == cg.path {
			info := states[p.State]
			led := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render(info.led)
			roster = append(roster, rollupRow{led: led, name: p.Title})
		}
	}
	for _, sub := range childGroupsOf(cg.members, cg.path) {
		led := lipgloss.NewStyle().Foreground(states[groupState(sub.members)].color).Bold(true).Render("▣")
		roster = append(roster, rollupRow{led: led, name: panel.GroupLeaf(sub.path)})
	}
	_, pinned := childPinnedDefault(cg)
	return m.renderRollupTile("▣", states[groupState(cg.members)].color, pinned, panel.GroupLeaf(cg.path),
		mutedStyle.Render(count), groupCountChips(cg.members), roster, "↵ descend", focused, emuCols, emuRows, marginRight)
}

// renderSummaryTile draws the rollup of the collapsed members as one tile: a "+N
// more" head, the per-state chips, a roster of the summarised panels, and a ↵ open
// hint — so the spillover is legible at a glance and one enter away.
func (m model) renderSummaryTile(collapsed []panel.Panel, focused bool, emuCols, emuRows, marginRight int) string {
	roster := make([]rollupRow, 0, len(collapsed))
	for _, p := range collapsed {
		info := states[p.State]
		led := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render(info.led)
		roster = append(roster, rollupRow{led: led, name: p.Title})
	}
	return m.renderRollupTile("▦", colBrandHi, false, fmt.Sprintf("+%d more", len(collapsed)),
		"", groupCountChips(collapsed), roster, "↵ open", focused, emuCols, emuRows, marginRight)
}

// tileGrid arranges rendered tiles into rows of at most cols columns.
func tileGrid(tiles []string, cols int) string {
	if cols < 1 {
		cols = 1
	}
	rows := make([]string, 0, (len(tiles)+cols-1)/cols)
	for i := 0; i < len(tiles); i += cols {
		end := min(i+cols, len(tiles))
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, tiles[i:end]...))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// renderTile draws one member as a bordered box that fills its share of the
// screen: a status LED and title above the panel's live screen (emuCols×emuRows).
// The focused tile glows in the brand colour, mirroring the dashboard's selected
// card.
func (m model) renderTile(p panel.Panel, focused bool, emuCols, emuRows, marginRight int) string {
	info := states[p.State]
	border := colFaint
	titleColor := colInk
	if focused {
		border = colBrand
		titleColor = colBrandHi
	}
	// The tile being typed into glows green and wears a keyboard badge, so it is
	// obvious where keystrokes are going while interact mode is on.
	interacting := focused && m.groupInteract
	if interacting {
		border = colGreen
	}

	led := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render(info.led)
	// Build the head's leading markers first — an interact badge, else a pin glyph —
	// then fit the title in whatever width is left, so a long title never wraps the
	// head onto a second row.
	prefix := led + " "
	switch {
	case interacting:
		badge := lipgloss.NewStyle().Foreground(colDark).Background(colGreen).Bold(true).Render(" ⌨ ")
		prefix = badge + " " + led + " "
	case m.groupPinned[p.ID]:
		pin := lipgloss.NewStyle().Foreground(colBrandHi).Render("⊙")
		prefix = pin + " " + led + " "
	}
	title := lipgloss.NewStyle().Foreground(titleColor).Bold(true).
		Render(truncate(p.Title, max(1, emuCols-lipgloss.Width(prefix))))
	head := prefix + title

	return paneBox(emuCols, marginRight, border).Render(lipgloss.JoinVertical(lipgloss.Left,
		head,
		lipgloss.JoinVertical(lipgloss.Left, m.tileBody(p, emuCols, emuRows, focused, interacting)...),
	))
}

// paneBox is the shared tile/pane chrome: a rounded, padded box emuCols+2 wide
// (inner content + padding; the border adds the last 2), with the given border
// colour and right margin. renderTile, renderSummaryTile, and the floating scratch
// pane all wrap their head + body in it.
func paneBox(emuCols, marginRight int, border lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(emuCols+2).
		Padding(0, 1).
		MarginRight(marginRight).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)
}

// tileBody is a tile's content rows, always exactly emuRows tall: the member's
// live screen when it is streaming, or a one-line activity note before output
// lands (and when there is no client, as in tests). The focused tile honours the
// scrollback offset, so its history scrolls in place while the other tiles stay
// at their live bottom. When showCursor is set — the tile being interacted with,
// and not scrolled back — a reverse-video cell is drawn at the emulator's cursor,
// so you can see where your typing lands, exactly as the single zoom does.
func (m model) tileBody(p panel.Panel, emuCols, emuRows int, focused, showCursor bool) []string {
	emu := m.groupEmus[p.ID]
	if emu == nil {
		rows := make([]string, emuRows) // pad to a fixed tile height
		if p.Activity != "" && len(rows) > 0 {
			rows[0] = mutedStyle.Render(truncate(p.Activity, emuCols))
		}
		return rows
	}
	// Only the focused tile owns the scroll offset and the search highlight; the hit
	// indices are computed against its emulator, so searchWindow applies there and
	// the other tiles render their plain live bottom.
	rows := emuWindow(emu, emuCols, emuRows, 0)
	if focused {
		rows = m.selectWindow(emu, emuCols, emuRows, m.scrollOff)
	}
	if showCursor && m.scrollOff == 0 {
		cur := emu.CursorPosition()
		if cur.Y >= 0 && cur.Y < len(rows) {
			rows[cur.Y] = overlayCursor(rows[cur.Y], cur.X)
		}
	}
	return rows
}

// groupZoomFooter is the split's status bar: the brand and GROUP caps with the
// work-item name on the left, the ? help hint in the middle, and the shared
// host stats, clock, and connection status on the right.
func (m model) groupZoomFooter() string {
	if m.input == inputSearch { // typing a find term over the focused tile
		return m.searchPromptFooter()
	}
	mode := seg("▣ GROUP", colInk, colBlue)
	if m.summaryScope {
		mode = seg("▦ SUMMARY", colDark, colBrandHi) // scoped to the parent's summarised members
	}
	switch {
	case m.copySelecting:
		mode = seg("✄ SELECT", colDark, colCyan)
	case m.searchActive():
		mode = m.searchSeg()
	case m.scrolling:
		mode = seg("↕ SCROLL", colDark, colScroll)
	case m.groupResize:
		mode = seg("⤢ RESIZE", colDark, colCyan) // arrows grow/shrink the focused tile
	case m.groupInteract:
		mode = seg("⌨ INTERACT", colDark, colGreen) // typing into the focused tile
	}
	left := seg("◈ BATON", colDark, colBrand) +
		mode +
		seg(truncate(groupBreadcrumb(m.groupName), 32), colDark, colBrandHi) +
		scrollSeg(m.scrollOff)
	return m.statusBar(left, m.helpHint())
}
