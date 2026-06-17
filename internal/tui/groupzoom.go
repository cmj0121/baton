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
	groupHeaderRows = 2  // header line + blank above the grid in groupZoomView
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
// override the user has dialled in.
func (m model) tileGeometry() (cols, emuCols, emuRows int) {
	return tileGeometry(len(m.groupMembers()), m.width, m.height-1-groupHeaderRows, m.groupCols)
}

// attachGroupMembers opens a live emulator for every member of the group and
// subscribes to its output. Each emulator is sized to its tile so the panel's PTY
// fills the space. Tiles are output-only — a per-emulator drain keeps a
// query-happy program from blocking, and no keystrokes are forwarded until you
// zoom one panel — so there is no input goroutine here. A no-op without a client.
func (m *model) attachGroupMembers() {
	if m.client == nil {
		return
	}
	_, emuCols, emuRows := m.tileGeometry()
	m.groupEmus = make(map[string]*vt.SafeEmulator)
	for _, p := range m.groupMembers() {
		emu := vt.NewSafeEmulator(emuCols, emuRows)
		m.groupEmus[p.ID] = emu
		go drainEmu(emu)
		m.sendf(proto.Command{Action: "panel.resize", ID: p.ID, Rows: emuRows, Cols: emuCols})
		m.sendf(proto.Command{Action: "panel.attach", ID: p.ID})
	}
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

// drainEmu discards a tile emulator's input side: query replies a live program
// generates are dropped rather than fed back, since split tiles are not driven.
func drainEmu(emu *vt.SafeEmulator) {
	buf := make([]byte, 4096)
	for {
		if _, err := emu.Read(buf); err != nil {
			return
		}
	}
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
	key := k.String()
	// The split is an output-only mosaic, so a bare key is safe here: the
	// dashboard key (d) — or esc — leaves straight for the dashboard.
	if key == m.bindingKey(actDashboard) || key == "esc" {
		return m.exitGroupZoom()
	}
	n := len(m.groupMembers())
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
	case "enter":
		return m.zoomFocusedMember()
	}
	return m, nil
}

// adjustGroupCols nudges the tile column count by delta (clamped to one column up
// to one per member), pins it as an explicit override, and refits the tiles to
// the new layout.
func (m model) adjustGroupCols(delta int) model {
	cols, _, _ := m.tileGeometry() // the current effective column count
	m.groupCols = max(1, min(cols+delta, len(m.groupMembers())))
	m.resizeGroupTiles()
	m.status = fmt.Sprintf("group · %d column(s)", m.groupCols)
	return m
}

// zoomFocusedMember drops from the split into the focused panel's own live zoom,
// remembering the group so BIND-g returns to the split.
func (m model) zoomFocusedMember() (tea.Model, tea.Cmd) {
	members := m.groupMembers()
	if m.groupFocus < 0 || m.groupFocus >= len(members) {
		return m, nil
	}
	origin := m.groupName
	// Drop the split's streams before the single zoom takes over input + output.
	m.sendf(proto.Command{Action: "panel.detach"}) // detach all
	m.closeGroupEmus()
	m = m.zoomInto(members[m.groupFocus])
	m.zoomGroupOrigin = origin
	return m, nil
}

// exitGroupZoom leaves the split for the dashboard: detach every tile, tear down
// its emulator, and ask the server for a fresh snapshot so the fleet is current.
func (m model) exitGroupZoom() (tea.Model, tea.Cmd) {
	m.sendf(proto.Command{Action: "panel.detach"}) // detach all
	m.closeGroupEmus()
	m.mode = modeDashboard
	m.groupName = ""
	m.groupFocus = 0
	m.status = "dashboard"
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
	m.emu = nil
	m.zoomID, m.zoomTitle, m.zoomArmed, m.zoomExited, m.zoomGroupOrigin = "", "", false, false, ""
	m.attachGroupMembers() // re-subscribe every tile's live stream
	m.status = "group · " + m.groupName
	return m, nil
}

// groupZoomView renders the split: a header, a grid of member tiles with the
// focused one lit, and a footer of key hints pinned to the last line.
func (m model) groupZoomView() string {
	members := m.groupMembers()
	header := sectionStyle.Render(spaced("GROUP")) + "  " +
		lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render(m.groupName) +
		mutedStyle.Render(fmt.Sprintf("   %d panel(s)", len(members)))

	cols, emuCols, emuRows := tileGeometry(len(members), m.width, m.height-1-groupHeaderRows, m.groupCols)
	tiles := make([]string, len(members))
	for i, p := range members {
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

	led := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render(info.led)
	title := lipgloss.NewStyle().Foreground(titleColor).Bold(true).Render(truncate(p.Title, emuCols-2))
	head := led + " " + title

	box := lipgloss.NewStyle().
		Width(emuCols+2). // inner content + padding; the border adds the last 2
		Padding(0, 1).
		MarginRight(gtileGap).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)

	return box.Render(lipgloss.JoinVertical(lipgloss.Left,
		head,
		lipgloss.JoinVertical(lipgloss.Left, m.tileBody(p, emuCols, emuRows)...),
	))
}

// tileBody is a tile's content rows, always exactly emuRows tall: the member's
// live screen when it is streaming, or its preview tail before output lands (and
// when there is no client, as in tests).
func (m model) tileBody(p panel.Panel, emuCols, emuRows int) []string {
	var src []string
	if emu := m.groupEmus[p.ID]; emu != nil {
		src = strings.Split(emu.Render(), "\n")
	} else {
		for _, line := range previewLines(p) {
			src = append(src, mutedStyle.Render(truncate(line, emuCols)))
		}
	}
	rows := make([]string, emuRows) // pad/clip to a fixed tile height; copy stops at min
	copy(rows, src)
	return rows
}

// groupZoomFooter is the split's status bar: a full-width strip of bright caps,
// matching the dashboard footer — a brand cap, a GROUP mode cap and the group
// name on the left, and the navigation keys as colour caps on the right, with a
// surface-filled middle so the whole row is solid colour.
func (m model) groupZoomFooter() string {
	left := seg("◈ BATON", colDark, colBrand) +
		seg("▣ GROUP", colInk, colBlue) +
		seg(truncate(m.groupName, 24), colDark, colBrandHi)

	next := seg("TAB next", colDark, colCyan)
	cols := seg("±  cols", colDark, colBrandHi)
	zoom := seg("⏎ zoom", colDark, colGreen)
	dash := seg(keyLabel(m.bindingKey(actDashboard))+" dashboard", colInk, colBlue)

	// Keep as many hints as fit, dropping the least essential first (cols, then
	// next), so the bar never spills past the edge on a narrow terminal.
	right := firstFit(m.width, left, [][]string{
		{next, cols, zoom, dash},
		{next, zoom, dash},
		{zoom, dash},
		{dash},
		{},
	})
	return fillBar(m.width, left, right)
}
