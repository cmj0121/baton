package tui

import (
	"fmt"
	"strings"

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

// tileGeometry resolves the split's layout from the current model dimensions,
// reserving the footer bar (one row) and the header, honouring any column
// override the user has dialled in. It lays out only the live (capped) tiles —
// members past the cap are summarised in the header, not given a tile — so a
// huge group never shrinks the grid into unreadable slivers.
func (m model) tileGeometry() (cols, emuCols, emuRows int) {
	return tileGeometry(len(m.liveMembers()), m.width, m.height-1-groupHeaderRows, m.groupCols)
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

// liveMembers is the members that should hold a live tile emulator: the group's
// members capped at maxGroupTiles, in fleet order. The remainder render from
// their preview tail rather than a live stream.
func (m model) liveMembers() []panel.Panel {
	members := m.groupMembers()
	if len(members) > maxGroupTiles {
		members = members[:maxGroupTiles]
	}
	return members
}

// focusedMemberID is the id of the panel the split's focus currently rests on,
// read before a snapshot is applied so reconcileGroupTiles can keep the focus on
// the same panel even as the roster shifts. Empty when the focus is out of range.
func (m model) focusedMemberID() string {
	live := m.liveMembers()
	if m.groupFocus >= 0 && m.groupFocus < len(live) {
		return live[m.groupFocus].ID
	}
	return ""
}

// reconcileGroupTiles brings the split's live tiles in line with the latest
// fleet after a snapshot: it leaves an emptied group for the dashboard, attaches
// newly added members (up to the cap), tears down departed ones, and keeps the
// focus on the same panel (by id) when the roster shifts. A no-op without a
// client. focusID is the panel the focus rested on before the snapshot.
func (m *model) reconcileGroupTiles(focusID string) {
	members := m.groupMembers()
	if len(members) == 0 {
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
		live := m.liveMembers()
		want := make(map[string]bool, len(live))
		for _, p := range live {
			want[p.ID] = true
		}

		changed := false
		// Drop tiles whose panel left the group (or fell past the cap).
		for id, emu := range m.groupEmus {
			if !want[id] {
				m.sendf(proto.Command{Action: "panel.detach", ID: id})
				closeZoom(emu)
				delete(m.groupEmus, id)
				changed = true
			}
		}
		// Attach a tile for each newly added member, sized to the current grid.
		_, emuCols, emuRows := m.tileGeometry()
		for _, p := range live {
			if m.groupEmus[p.ID] == nil {
				m.attachTile(p, emuCols, emuRows)
				changed = true
			}
		}
		// A changed membership reflows the grid, so refit every existing tile too.
		if changed {
			m.resizeGroupTiles()
		}
	}
	// Keep the focus on the same panel by id; fall back to clamping into range
	// when that panel left the live tiles (removed, or pushed past the cap).
	live := m.liveMembers()
	if idx := indexOfMember(live, focusID); idx >= 0 {
		m.groupFocus = idx
	} else {
		m.groupFocus = max(0, min(m.groupFocus, len(live)-1))
		// The panel being typed into is gone: stop interacting so keys don't land
		// on whatever tile the focus fell onto.
		if m.groupInteract {
			m.groupInteract = false
			m.status = "interact ended · the panel left the group"
		}
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
	for _, p := range m.groupMembers() {
		if emu := m.groupEmus[p.ID]; emu != nil {
			emu.Resize(emuCols, emuRows)
			m.sendf(proto.Command{Action: "panel.resize", ID: p.ID, Rows: emuRows, Cols: emuCols})
		}
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
			}
		}
		if key == m.bindingKey(actDetach) { // C-t q detaches from the split too
			return m.runAction(actDetach)
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
	// Focus walks the live tiles only; members past the cap have no tile to land on.
	n := len(m.liveMembers())
	switch key {
	case "tab", "right", "l", "down", "j":
		if n > 0 {
			m.groupFocus = (m.groupFocus + 1) % n
		}
	case "shift+tab", "left", "h", "up", "k":
		if n > 0 {
			m.groupFocus = (m.groupFocus - 1 + n) % n
		}
	case "+", "=":
		return m.adjustGroupCols(1), nil
	case "-", "_":
		return m.adjustGroupCols(-1), nil
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
	m.feedFocused(k)
	return m, nil
}

// enterInteract hands the keyboard to the focused tile so it can be driven in
// place, without dropping into a full single-panel zoom. It needs a live tile to
// type into, so it is a no-op (with a hint) on a preview-only or out-of-range
// focus.
func (m model) enterInteract() model {
	members := m.liveMembers()
	if m.groupFocus < 0 || m.groupFocus >= len(members) {
		return m
	}
	p := members[m.groupFocus]
	if m.groupEmus[p.ID] == nil {
		m.status = "interact needs a live panel"
		return m
	}
	m.groupInteract = true
	m.groupArmed = false
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
	members := m.liveMembers()
	if m.groupFocus < 0 || m.groupFocus >= len(members) {
		return
	}
	if emu := m.groupEmus[members[m.groupFocus].ID]; emu != nil {
		feedKey(emu, k)
	}
}

// removeFocusedMember takes the focused tile's panel out of the group, returning
// it to the dashboard as a lone panel. The server broadcasts a fresh snapshot,
// and the split reconciles its tiles on the next applyEvent.
func (m model) removeFocusedMember() model {
	members := m.liveMembers()
	if m.groupFocus < 0 || m.groupFocus >= len(members) {
		return m
	}
	p := members[m.groupFocus]
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
	floorCols := max(1, (m.width+gtileGap)/(gtileFloorW+gtileGap))
	maxCols := min(len(m.liveMembers()), floorCols)
	m.groupCols = max(1, min(cols+delta, maxCols))
	m.resizeGroupTiles()
	m.status = fmt.Sprintf("group · %d column(s)", m.groupCols)
	return m
}

// zoomFocusedMember drops from the split into the focused panel's own live zoom,
// remembering the group so BIND-g returns to the split.
func (m model) zoomFocusedMember() (tea.Model, tea.Cmd) {
	members := m.liveMembers()
	if m.groupFocus < 0 || m.groupFocus >= len(members) {
		return m, nil
	}
	origin := m.groupName
	// Drop the split's streams before the single zoom takes over input + output.
	m.sendf(proto.Command{Action: "panel.detach"}) // detach all
	m.closeGroupEmus()
	m.groupInteract = false
	m = m.zoomInto(members[m.groupFocus])
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
	m.emu = nil
	m.zoomID, m.zoomTitle, m.zoomArmed, m.zoomExited, m.zoomGroupOrigin = "", "", false, false, ""
	m.attachGroupMembers() // re-subscribe every tile's live stream
	m.status = "group · " + m.groupName
	return m, nil
}

// groupZoomView renders the split: a header, a grid of member tiles with the
// focused one lit, and a footer of key hints pinned to the last line.
func (m model) groupZoomView() string {
	total := len(m.groupMembers())
	tilesFor := m.liveMembers() // only the capped, live tiles get a cell
	header := sectionStyle.Render(spaced("GROUP")) + "  " +
		lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render(m.groupName) +
		mutedStyle.Render(fmt.Sprintf("   %d panel(s)", total))
	if over := total - len(tilesFor); over > 0 {
		// Members past the live-tile cap are not drawn; say so rather than show a
		// shrunken or fabricated tile for them.
		header += lipgloss.NewStyle().Foreground(states[panel.Idle].color).
			Render(fmt.Sprintf("   · +%d more (showing first %d live)", over, len(tilesFor)))
	}

	cols, emuCols, emuRows := tileGeometry(len(tilesFor), m.width, m.height-1-groupHeaderRows, m.groupCols)
	tiles := make([]string, len(tilesFor))
	for i, p := range tilesFor {
		tiles[i] = m.renderTile(p, i == m.groupFocus, emuCols, emuRows)
	}
	grid := tileGrid(tiles, cols)

	body := lipgloss.JoinVertical(lipgloss.Left, header, "", grid)
	placed := lipgloss.Place(m.width, m.height-1, lipgloss.Left, lipgloss.Top, body)
	return placed + "\n" + m.groupZoomFooter()
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
	title := lipgloss.NewStyle().Foreground(titleColor).Bold(true).Render(truncate(p.Title, emuCols-2))
	head := led + " " + title
	if interacting {
		badge := lipgloss.NewStyle().Foreground(colDark).Background(colGreen).Bold(true).Render(" ⌨ ")
		head = badge + " " + led + " " + title
	}

	box := lipgloss.NewStyle().
		Width(emuCols+2). // inner content + padding; the border adds the last 2
		Padding(0, 1).
		MarginRight(gtileGap).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)

	return box.Render(lipgloss.JoinVertical(lipgloss.Left,
		head,
		lipgloss.JoinVertical(lipgloss.Left, m.tileBody(p, emuCols, emuRows, interacting)...),
	))
}

// tileBody is a tile's content rows, always exactly emuRows tall: the member's
// live screen when it is streaming, or its preview tail before output lands (and
// when there is no client, as in tests). When showCursor is set — the tile being
// interacted with — a reverse-video cell is drawn at the emulator's cursor, so
// you can see where your typing lands, exactly as the single zoom does.
func (m model) tileBody(p panel.Panel, emuCols, emuRows int, showCursor bool) []string {
	emu := m.groupEmus[p.ID]
	var src []string
	if emu != nil {
		src = strings.Split(emu.Render(), "\n")
	} else {
		for _, line := range previewLines(p) {
			src = append(src, mutedStyle.Render(truncate(line, emuCols)))
		}
	}
	rows := make([]string, emuRows) // pad/clip to a fixed tile height; copy stops at min
	copy(rows, src)
	if showCursor && emu != nil {
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
	mode := seg("▣ GROUP", colInk, colBlue)
	if m.groupInteract {
		mode = seg("⌨ INTERACT", colDark, colGreen) // typing into the focused tile
	}
	left := seg("◈ BATON", colDark, colBrand) +
		mode +
		seg(truncate(m.groupName, 24), colDark, colBrandHi)
	return m.statusBar(left, m.helpHint())
}
