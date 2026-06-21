package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// The group split: zooming a work item lays its panels out as live tiles you
// navigate as a unit. tab cycles the focus, enter drops into the focused panel's
// own zoom, and the dashboard key (d) — or esc — leaves for the dashboard. From a
// zoomed member, BIND-g pops back to the split.

const (
	gtileGap        = 1  // space reserved to the right of each tile
	gtileMinW       = 32 // preferred minimum tile width, deciding the column count
	gtileFloorW     = 16 // hard floor on a tile's width when the user dials columns up
	groupHeaderRows = 2  // header line + blank above the grid in groupZoomView
	groupTreeWidth  = 26 // outer width of the tree pane listing the unpinned members

	// maxGroupTiles caps how many members stream live at once, bounding the PTYs,
	// emulators, and drain goroutines a single huge group can spin up. Members
	// past the cap still render from their preview tail.
	maxGroupTiles = 16
)

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

// gridWidth is the width left for the tile grid, after reserving the tree pane
// when the group has any unpinned members listed there.
func (m model) gridWidth() int {
	_, tree := m.splitMembers()
	if len(tree) > 0 {
		return max(1, m.width-groupTreeWidth-1)
	}
	return m.width
}

// tileGeometry resolves the split's layout from the current model dimensions,
// reserving the footer bar (one row), the header, and the tree pane, honouring
// any column override the user has dialled in. It lays out only the live tiles,
// so a huge group never shrinks the grid into unreadable slivers — its overflow
// lives in the tree list instead.
func (m model) tileGeometry() (cols, emuCols, emuRows int) {
	return tileGeometry(len(m.tileMembers()), m.gridWidth(), m.height-1-groupHeaderRows, m.groupCols)
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
	_, emuCols, emuRows := m.tileGeometry()
	m.groupEmus = make(map[string]*vt.SafeEmulator)
	for _, p := range m.liveMembers() {
		m.attachTile(p, emuCols, emuRows)
	}
}

// attachTile opens a live, tile-sized emulator for one member and subscribes to
// its output. A reader forwards the emulator's input side to the panel's PTY, so
// the focused tile can be typed into during interact mode; outside it no keys are
// fed, so the tile only ever relays the query replies a live program emits. The
// shared per-member step of building the split and of reconciling it.
func (m *model) attachTile(p panel.Panel, emuCols, emuRows int) {
	emu := vt.NewSafeEmulator(emuCols, emuRows)
	m.groupEmus[p.ID] = emu
	go zoomReader(emu, m.client, p.ID)
	m.sendf(proto.Command{Action: "panel.resize", ID: p.ID, Rows: emuRows, Cols: emuCols})
	m.sendf(proto.Command{Action: "panel.attach", ID: p.ID})
}

// splitMembers partitions the group into the live tiles and the tree list, in
// fleet order, from a single fleet scan:
//
//   - A group that fits the cap is all tiles, no list — pins do not matter.
//   - Over the cap, if the user has pinned any panels, those pinned panels are
//     the tiles and everyone else is the list, so you curate a few to watch live.
//   - Over the cap with no pins, the first maxGroupTiles are tiles and the rest
//     fall into the list — a sensible default before any curation.
func (m model) splitMembers() (tiles, tree []panel.Panel) {
	all := m.groupMembers()
	if len(all) <= maxGroupTiles {
		return all, nil // everything fits as tiles; pins are moot
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
				tree = append(tree, p)
			}
		case i < maxGroupTiles:
			tiles = append(tiles, p)
		default:
			tree = append(tree, p)
		}
	}
	return tiles, tree
}

// tileMembers is the subset of the group that holds a live tile emulator.
func (m model) tileMembers() []panel.Panel {
	tiles, _ := m.splitMembers()
	return tiles
}

// liveMembers is an alias for tileMembers — the panels with a live emulator.
func (m model) liveMembers() []panel.Panel { return m.tileMembers() }

// displayedMembers is every member in the order the focus walks them: the live
// tiles first, then the tree list. The cursor indexes into this.
func (m model) displayedMembers() []panel.Panel {
	tiles, tree := m.splitMembers()
	out := make([]panel.Panel, 0, len(tiles)+len(tree))
	out = append(out, tiles...)
	return append(out, tree...)
}

// pinnedCount is how many of the group's members are pinned to a live tile.
func (m model) pinnedCount() int {
	n := 0
	for _, p := range m.groupMembers() {
		if m.groupPinned[p.ID] {
			n++
		}
	}
	return n
}

// focusedMember resolves the focus to its member — a tile or a tree row —
// reporting false when out of range. The single bounds check the pin, interact,
// remove, and zoom actions share.
func (m model) focusedMember() (panel.Panel, bool) {
	disp := m.displayedMembers()
	if m.groupFocus < 0 || m.groupFocus >= len(disp) {
		return panel.Panel{}, false
	}
	return disp[m.groupFocus], true
}

// focusedIsTile reports whether the focus currently rests on a live tile (rather
// than a tree row) — the gate for interact, which needs a streaming emulator.
func (m model) focusedIsTile() bool {
	return m.groupFocus >= 0 && m.groupFocus < len(m.tileMembers())
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
	tiles, tree := m.splitMembers()
	if len(tiles)+len(tree) == 0 {
		// The group dissolved or lost its last panel: leave for the dashboard.
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
		// Attach a tile for each newly-live member, sized to the current grid.
		_, emuCols, emuRows := m.tileGeometry()
		for _, p := range tiles {
			if m.groupEmus[p.ID] == nil {
				m.attachTile(p, emuCols, emuRows)
				changed = true
			}
		}
		// A changed tile set reflows the grid, so refit every existing tile too.
		if changed {
			m.resizeGroupTiles()
		}
	}
	// Keep the focus on the same panel by id across tiles and tree; fall back to
	// clamping into range when that panel left the group entirely.
	disp := m.displayedMembers()
	if idx := indexOfMember(disp, focusID); idx >= 0 {
		m.groupFocus = idx
	} else {
		m.groupFocus = max(0, min(m.groupFocus, len(disp)-1))
	}
	// Interact needs a live tile: stop if the panel being typed into (focusID) is
	// no longer one — removed, or demoted to the tree — so keys never land on
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
	_, emuCols, emuRows := m.tileGeometry()
	// groupEmus already holds exactly the live tiles, keyed by id, so resize them
	// directly rather than re-scanning the fleet for members.
	for id, emu := range m.groupEmus {
		emu.Resize(emuCols, emuRows)
		m.sendf(proto.Command{Action: "panel.resize", ID: id, Rows: emuRows, Cols: emuCols})
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

// groupMembers is the panels of the group currently being split-viewed, in fleet
// order.
func (m model) groupMembers() []panel.Panel {
	var out []panel.Panel
	for _, p := range m.fleet {
		if p.Group == m.groupName {
			out = append(out, p)
		}
	}
	return out
}

// handleGroupZoomKey drives the split: cycle the focused tile, zoom into it, or
// leave. Movement wraps so tab walks the whole group.
func (m model) handleGroupZoomKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Interact mode hands the keyboard to the focused tile; the prefix is still the
	// only way back out, exactly as in a zoom.
	if m.groupInteract {
		return m.handleGroupInteractKey(k)
	}
	key := k.String()
	// The split is command-mode, so the prefix is only needed for the universal
	// escapes — C-t d leaves, C-t g is a no-op (already in the group view).
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
	// Bare single keys drive the split. The dashboard key (d) and esc leave.
	if key == m.bindingKey(actDashboard) || key == "esc" {
		return m.exitGroupZoom()
	}
	// Focus walks every member — the live tiles first, then the tree list — so a
	// large group's overflow is reachable, not stranded.
	n := len(m.displayedMembers())
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
		return m.adjustGroupCols(1), nil
	case "-", "_":
		return m.adjustGroupCols(-1), nil
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
		ids := liveIDs(m.groupMembers())
		scope := fmt.Sprintf("%s (%d panels)", m.groupName, len(ids))
		return m.openSignalPicker(modeGroupZoom, ids, scope), nil
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
		return m.zoomFocusedMember()
	}
	return m, nil
}

// handleGroupInteractKey drives the focused tile while interact mode is on: every
// bare key is fed to that panel's program, and the prefix is the only escape —
// C-t i (or C-t g) stops interacting and returns to navigation, C-t d leaves for
// the dashboard, C-t q detaches, and C-t C-t sends a literal prefix. This mirrors
// a zoom's input model, but on one tile of the split rather than a full screen.
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
			case actGroupView: // C-t g → back to the split's navigation
				return m.exitInteract(), nil
			case actEditMap:
				return m.openEditMap(modeGroupZoom), nil
			case actScroll: // C-t [ → scroll the focused tile's history
				return m.enterScroll(), nil
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
// type into, so it hints to pin a tree-listed panel first, and is a no-op on a
// preview-only (no client) or out-of-range focus.
func (m model) enterInteract() model {
	p, ok := m.focusedMember()
	if !ok {
		return m
	}
	if !m.focusedIsTile() {
		m.status = fmt.Sprintf("%s is in the list — press %s to pin it first", p.Title, keyPin)
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

// adjustGroupCols nudges the tile column count by delta (clamped to one column up
// to one per member), pins it as an explicit override, and refits the tiles to
// the new layout.
func (m model) adjustGroupCols(delta int) model {
	cols, _, _ := m.tileGeometry() // the current effective column count
	// Cap columns at one per live tile and at whatever keeps a tile above the
	// width floor, so dialling up never collapses the grid into unreadable slivers.
	// Use the grid's own width, which the tree pane narrows when it is shown.
	floorCols := max(1, (m.gridWidth()+gtileGap)/(gtileFloorW+gtileGap))
	maxCols := min(len(m.liveMembers()), floorCols)
	m.groupCols = max(1, min(cols+delta, maxCols))
	m.resizeGroupTiles()
	m.status = fmt.Sprintf("group · %d column(s)", m.groupCols)
	return m
}

// zoomFocusedMember drops from the split into the focused panel's own live zoom,
// remembering the group so BIND-g returns to the split.
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
	m.scrollOff = 0
	m.scrolling = false
	m.cursorHidden = nil
	m.emu = nil
	m.zoomID, m.zoomTitle, m.zoomArmed, m.zoomExited, m.zoomGroupOrigin = "", "", false, false, ""
	m.attachGroupMembers() // re-subscribe every tile's live stream
	m.status = "group · " + m.groupName
	return m, nil
}

// groupZoomView renders the split: a header, the grid of live member tiles, the
// tree pane listing the unpinned overflow when there is any, and a footer pinned
// to the last line.
func (m model) groupZoomView() string {
	tiles, tree := m.splitMembers()
	members := append(append([]panel.Panel{}, tiles...), tree...)
	header := sectionStyle.Render(spaced("GROUP")) + "  " +
		lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render(m.groupName) +
		mutedStyle.Render(fmt.Sprintf("   %d panel(s)  ", len(members))) + kindBreakdown(members)
	if len(tree) > 0 {
		header += lipgloss.NewStyle().Foreground(states[panel.Idle].color).
			Render(fmt.Sprintf("   · %d live · %d in list", len(tiles), len(tree)))
	}

	cols, emuCols, emuRows := m.tileGeometry()
	rendered := make([]string, len(tiles))
	for i, p := range tiles {
		rendered[i] = m.renderTile(p, i == m.groupFocus, emuCols, emuRows)
	}
	grid := tileGrid(rendered, cols)

	if len(tree) > 0 {
		// The focus index within the tree (after the tiles), or < 0 on a tile.
		pane := m.renderGroupTree(tree, m.groupFocus-len(tiles), lipgloss.Height(grid))
		grid = lipgloss.JoinHorizontal(lipgloss.Top, grid, " ", pane)
	}

	body := lipgloss.JoinVertical(lipgloss.Left, header, "", grid)
	placed := lipgloss.Place(m.width, m.height-1, lipgloss.Left, lipgloss.Top, body)
	return placed + "\n" + m.groupZoomFooter()
}

// renderGroupTree draws the right-hand pane listing the group's unpinned members
// — the overflow without a live tile — as a compact, scrollable list with the
// focused row lit. focusIdx is the focused row within the tree, or < 0 when the
// focus rests on a tile. The pane is sized to the grid's height so the two align.
func (m model) renderGroupTree(tree []panel.Panel, focusIdx, height int) string {
	inner := groupTreeWidth - 4 // border (2) + padding (2)
	head := sectionStyle.Render(spaced("LIST")) + mutedStyle.Render(fmt.Sprintf(" %d", len(tree)))

	// Reserve the header, a blank, a blank, and the hint; scroll the rest.
	visible := max(1, height-6)
	start, end := scrollWindow(max(0, focusIdx), len(tree), visible)

	rows := []string{head, ""}
	for i := start; i < end; i++ {
		p := tree[i]
		info := states[p.State]
		led := lipgloss.NewStyle().Foreground(info.color).Render(info.led)
		name := truncate(p.Title, inner-2)
		style := lipgloss.NewStyle().Width(inner)
		if i == focusIdx {
			style = style.Foreground(colDark).Background(colBrand).Bold(true)
		} else {
			style = style.Foreground(colInk)
		}
		rows = append(rows, style.Render(led+" "+name))
	}
	rows = append(rows, "", mutedStyle.Render(fmt.Sprintf("%s pin · enter zoom", keyPin)))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colFaint).
		Padding(0, 1).
		Width(groupTreeWidth - 2).
		Height(max(1, height-2)). // the border adds the 2 rows back, matching the grid
		Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
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
func (m model) renderTile(p panel.Panel, focused bool, emuCols, emuRows int) string {
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

	box := lipgloss.NewStyle().
		Width(emuCols+2). // inner content + padding; the border adds the last 2
		Padding(0, 1).
		MarginRight(gtileGap).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)

	return box.Render(lipgloss.JoinVertical(lipgloss.Left,
		head,
		lipgloss.JoinVertical(lipgloss.Left, m.tileBody(p, emuCols, emuRows, focused, interacting)...),
	))
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
	switch {
	case m.copySelecting:
		mode = seg("✄ SELECT", colDark, colCyan)
	case m.searchActive():
		mode = m.searchSeg()
	case m.scrolling:
		mode = seg("↕ SCROLL", colDark, colCyan)
	case m.groupInteract:
		mode = seg("⌨ INTERACT", colDark, colGreen) // typing into the focused tile
	}
	left := seg("◈ BATON", colDark, colBrand) +
		mode +
		seg(truncate(m.groupName, 24), colDark, colBrandHi) +
		scrollSeg(m.scrollOff)
	return m.statusBar(left, m.helpHint())
}
