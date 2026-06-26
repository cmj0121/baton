package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	vt "github.com/charmbracelet/x/vt"
)

// Copy mode. Inside scroll mode v marks a selection anchor at the top of the
// view; scrolling then extends the span, and y copies those lines to the system
// clipboard. With no anchor, y copies the visible page. The text is delivered via
// OSC52, an escape the terminal itself handles — so it needs no helper binary and
// works over SSH, exactly like the bell.

// copyToggle marks or clears the selection anchor. The anchor sits on the line at
// the top of the current view; as you scroll, the span runs from it to the new
// top, so the selection grows under you and the visible part is highlighted.
func (m model) copyToggle() model {
	if m.copySelecting {
		m.copySelecting = false
		m.status = "selection cleared"
		return m
	}
	emu, _ := m.scrollTarget()
	if emu == nil {
		m.status = "nothing to select here"
		return m
	}
	m.copySelecting = true
	m.copyAnchor = m.topVisibleLine(emu)
	m.status = "selection started · scroll to extend · y copies · v clears"
	return m
}

// copyBlockToggle starts (or clears) a rectangular selection: rows run from the
// anchor to the current top like a line selection, but only columns [0, copyCol]
// are copied, so you can lift a narrow left column out of aligned output. h / l
// pull the right edge in and out. Pressing V again clears it.
func (m model) copyBlockToggle() model {
	if m.copySelecting && m.copyBlock {
		m.copySelecting, m.copyBlock = false, false
		m.status = "selection cleared"
		return m
	}
	emu, _ := m.scrollTarget()
	if emu == nil {
		m.status = "nothing to select here"
		return m
	}
	m.copySelecting = true
	m.copyBlock = true
	m.copyAnchor = m.topVisibleLine(emu)
	m.copyCol = max(0, m.scrollCols()-1) // start full width; h narrows it
	m.status = "block select · scroll rows · h/l columns · y copies · V clears"
	return m
}

// adjustCopyCol moves a block selection's right column edge by delta, clamped to
// the target's width. A no-op outside a block selection.
func (m model) adjustCopyCol(delta int) model {
	if !m.copySelecting || !m.copyBlock {
		return m
	}
	m.copyCol = max(0, min(m.copyCol+delta, m.scrollCols()-1))
	m.status = fmt.Sprintf("block · columns 0–%d", m.copyCol)
	return m
}

// scrollCols is the column width of the current scroll target: the full screen in a
// single zoom, or the focused tile's inner width in the group split.
func (m model) scrollCols() int {
	switch m.mode {
	case modeGroupZoom:
		c, _ := m.tileEmuSize(m.groupFocus)
		return c
	default:
		return m.width
	}
}

// topVisibleLine is the combined scrollback+screen index of the line at the top
// of the current scroll window.
func (m model) topVisibleLine(emu *vt.SafeEmulator) int {
	return windowStart(emu.ScrollbackLen(), m.scrollOff)
}

// windowStart maps a scroll offset to the combined scrollback+screen index of the
// line at the top of that window, clamping the offset to the buffer depth.
func windowStart(sbLen, off int) int {
	if off > sbLen {
		off = sbLen
	}
	if off < 0 {
		off = 0
	}
	return sbLen - off
}

// copyRange resolves the inclusive [lo, hi] line span y will copy: the selection
// from its anchor to the current top when one is marked, otherwise the visible
// page. It clamps to the available lines and reports false when there is nothing.
func (m model) copyRange(emu *vt.SafeEmulator, total, rows int) (lo, hi int, ok bool) {
	top := m.topVisibleLine(emu)
	if m.copySelecting {
		lo, hi = m.copyAnchor, top
	} else {
		lo, hi = top, top+rows-1 // the visible page
	}
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo < 0 {
		lo = 0
	}
	if hi >= total {
		hi = total - 1
	}
	if lo > hi || total == 0 {
		return 0, 0, false
	}
	return lo, hi, true
}

// yankSelection copies the selected lines (or the visible page) to the clipboard
// and leaves copy + scroll mode, the way a yank ends copy mode in tmux.
func (m model) yankSelection() (tea.Model, tea.Cmd) {
	emu, rows := m.scrollTarget()
	if emu == nil {
		return m.exitScroll(), nil
	}
	plain, _ := combinedPlain(emu)
	lo, hi, ok := m.copyRange(emu, len(plain), rows)
	if !ok {
		m.status = "nothing to copy"
		return m, nil
	}
	// Block selection clips each row to the chosen columns; a line selection takes
	// the whole row. Either way drop trailing blank rows (a page copy at the live
	// bottom picks up empty screen lines) and keep one closing newline.
	rowsOut := plain[lo : hi+1]
	if m.copyBlock {
		width := min(m.copyCol+1, m.scrollCols())
		clipped := make([]string, len(rowsOut))
		for i, r := range rowsOut {
			clipped[i] = strings.TrimRight(clipCells(r, width), " \t")
		}
		rowsOut = clipped
	}
	text := strings.TrimRight(strings.Join(rowsOut, "\n"), " \t\n") + "\n"
	n := hi - lo + 1
	m = m.exitScroll() // a yank ends copy mode and returns to the live bottom
	m.status = fmt.Sprintf("copied %d line(s) to the clipboard", n)
	return m, clipboardCmd(text)
}

// clipboardCmd writes text to the system clipboard with OSC52. Like the bell it
// goes to stderr — the same tty the terminal reads — so it never disturbs the
// alt-screen frame bubbletea draws on stdout.
func clipboardCmd(text string) tea.Cmd {
	enc := base64.StdEncoding.EncodeToString([]byte(text))
	seq := "\x1b]52;c;" + enc + "\a"
	return func() tea.Msg {
		_, _ = os.Stderr.WriteString(seq)
		return nil
	}
}

// selectWindow renders a scroll window with both the search highlight and the
// copy selection applied — selection takes visual priority, drawn as a
// reverse-video band over the lines that y would copy. Outside scroll mode it is
// just the search window (itself emuWindow when no search is active).
func (m model) selectWindow(emu *vt.SafeEmulator, cols, rows, off int) []string {
	lines := m.searchWindow(emu, cols, rows, off)
	if !m.copySelecting || emu == nil {
		return lines
	}
	start := windowStart(emu.ScrollbackLen(), off)
	plain, _ := combinedPlain(emu)
	lo, hi := m.copyAnchor, start
	if lo > hi {
		lo, hi = hi, lo
	}
	width := cols
	if m.copyBlock {
		width = max(0, min(m.copyCol+1, cols)) // the block's highlighted column span
	}
	for i := range lines {
		idx := start + i
		if idx >= lo && idx <= hi && idx >= 0 && idx < len(plain) {
			if m.copyBlock {
				lines[i] = selectCells(plain[idx], width, cols)
			} else {
				lines[i] = selectLine(plain[idx], cols)
			}
		}
	}
	return lines
}

// selectCells draws a plain line for a block selection: the first width columns in
// reverse video (padded so the band is solid), then the rest of the line plain up
// to cols. So a rectangular selection highlights only its columns, with the
// unselected remainder still legible.
func selectCells(s string, width, cols int) string {
	if width >= cols {
		return selectLine(s, cols) // full width — no distinct tail to draw
	}
	head := clipCells(s, width)
	if w := cellsWidth(head); w < width {
		head += strings.Repeat(" ", width-w)
	}
	tail := clipCells(dropCells(s, width), cols-width)
	return "\x1b[7m" + head + "\x1b[27m" + tail
}

// cellsWidth is the display width of a plain string in terminal cells.
func cellsWidth(s string) int {
	w := 0
	for _, r := range s {
		w += cellWidth(r)
	}
	return w
}

// dropCells returns the suffix of a plain string after the first n display columns,
// the complement of clipCells. A rune straddling the boundary is dropped whole.
func dropCells(s string, n int) string {
	vis := 0
	for i, r := range s {
		if vis >= n {
			return s[i:]
		}
		vis += cellWidth(r)
	}
	return ""
}

// selectLine draws a plain line as a full-width reverse-video band, so a selected
// row reads as highlighted across the whole tile rather than only under its text.
func selectLine(s string, cols int) string {
	s = clipCells(s, cols)
	w := 0
	for _, r := range s {
		w += cellWidth(r)
	}
	if w < cols {
		s += strings.Repeat(" ", cols-w)
	}
	return "\x1b[7m" + s + "\x1b[27m"
}
