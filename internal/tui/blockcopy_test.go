package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	vt "github.com/charmbracelet/x/vt"
)

// TestDropCells: the complement of clipCells drops the first n display columns.
func TestDropCells(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 0, "hello"},
		{"hello", 2, "llo"},
		{"hello", 5, ""},
		{"hello", 9, ""},
	} {
		if got := dropCells(tc.s, tc.n); got != tc.want {
			t.Errorf("dropCells(%q,%d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

// TestSelectCellsHighlightsColumns: a block highlight reverse-videos the chosen
// columns and leaves the remainder plain and legible.
func TestSelectCellsHighlightsColumns(t *testing.T) {
	out := selectCells("abcdef", 3, 6)
	if !strings.HasPrefix(out, "\x1b[7mabc\x1b[27m") {
		t.Errorf("first 3 columns should be reverse video: %q", out)
	}
	if !strings.HasSuffix(out, "def") {
		t.Errorf("the remainder should stay plain: %q", out)
	}
}

// TestBlockToggleAndColumnAdjust: V starts a block selection, h/l move the column
// edge clamped to the width, and V again clears it.
func TestBlockToggleAndColumnAdjust(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 5)
	writeEmu(emu, []byte("hello world"))
	m := baseModel()
	m.mode = modeZoom
	m.emu = emu
	m.zoomID = "z"
	m.scrolling = true

	m = m.copyBlockToggle()
	if !m.copySelecting || !m.copyBlock {
		t.Fatal("V should start a block selection")
	}
	if m.copyCol != m.width-1 {
		t.Fatalf("block should start full width: copyCol=%d width=%d", m.copyCol, m.width)
	}
	m = m.adjustCopyCol(-5)
	if m.copyCol != m.width-1-5 {
		t.Fatalf("h should narrow the column edge, copyCol=%d", m.copyCol)
	}
	// Clamp at zero.
	m = m.adjustCopyCol(-9999)
	if m.copyCol != 0 {
		t.Fatalf("column edge should clamp at 0, got %d", m.copyCol)
	}
	m = m.copyBlockToggle()
	if m.copySelecting || m.copyBlock {
		t.Fatal("V again should clear the block selection")
	}
}

// TestClickFocusesTile: a left click in the group split focuses the tile under the
// pointer; a click on the header focuses nothing.
func TestClickFocusesTile(t *testing.T) {
	m := baseModel()
	m.mode = modeGroupZoom
	m.fleet = groupedFleet()
	m.groupName = "api" // 3 members, tiled even grid
	m.mouseEnabled = true

	rects := m.tileHitRects()
	if len(rects) < 2 {
		t.Fatalf("expected at least 2 tiles, got %d", len(rects))
	}
	// Click inside the second tile's box (offset into the grid by the header).
	r := rects[1]
	click := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: r.x + 1, Y: r.y + groupHeaderRows + 1}
	next, _ := m.handleMouse(click)
	if got := next.(model).groupFocus; got != 1 {
		t.Fatalf("click in tile 1 should focus it, groupFocus=%d", got)
	}

	// A click on the header row resolves to no tile and leaves the focus put.
	m.groupFocus = 2
	header := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 0}
	next, _ = m.handleMouse(header)
	if got := next.(model).groupFocus; got != 2 {
		t.Fatalf("a header click should not change the focus, groupFocus=%d", got)
	}
}
