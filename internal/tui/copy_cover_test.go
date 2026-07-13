package tui

import (
	"strings"
	"testing"

	vt "github.com/charmbracelet/x/vt"
)

// TestWindowStartClamps: the offset is clamped to [0, sbLen] before mapping to an
// absolute line index.
func TestWindowStartClamps(t *testing.T) {
	if got := windowStart(5, 10); got != 0 {
		t.Fatalf("an over-deep offset should clamp to the buffer top (0), got %d", got)
	}
	if got := windowStart(5, -1); got != 5 {
		t.Fatalf("a negative offset should clamp to the live bottom (sbLen), got %d", got)
	}
	if got := windowStart(5, 2); got != 3 {
		t.Fatalf("an in-range offset should map to sbLen-off, got %d", got)
	}
}

// TestCopyRangeClampsAndSwaps: an anchor below the top is swapped, and the span is
// clamped to the available lines.
func TestCopyRangeClampsAndSwaps(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	fillLines(emu, 30)
	m := model{emu: emu, mode: modeZoom, width: 20, height: 8}
	total := mustLen(emu)

	// Anchor far below the current top forces the lo/hi swap and the hi clamp.
	m.copySelecting = true
	m.copyAnchor = total + 50
	lo, hi, ok := m.copyRange(emu, total, 4)
	if !ok {
		t.Fatal("a valid selection should resolve")
	}
	if lo < 0 || hi != total-1 {
		t.Fatalf("the span should clamp to [0, total-1], got [%d,%d]", lo, hi)
	}
}

// TestCopyRangeEmpty: with no lines at all there is nothing to copy.
func TestCopyRangeEmpty(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	m := model{emu: emu, mode: modeZoom, width: 20, height: 8}
	if _, _, ok := m.copyRange(emu, 0, 4); ok {
		t.Fatal("a zero-line buffer should report nothing to copy")
	}
}

// TestClipboardCmdWritesTty: invoking the returned command runs its OSC52 side
// effect and yields a nil message.
func TestClipboardCmdWritesTty(t *testing.T) {
	if msg := clipboardCmd("payload")(); msg != nil {
		t.Fatalf("the clipboard command should yield a nil message, got %v", msg)
	}
}

// TestYankNilTargetExitsScroll: yanking with no scroll target just leaves scroll
// mode without emitting a clipboard command.
func TestYankNilTargetExitsScroll(t *testing.T) {
	m := model{mode: modeDashboard, scrolling: true}
	next, cmd := m.yankSelection()
	if cmd != nil {
		t.Fatal("a nil target should not emit a clipboard command")
	}
	if next.(model).scrolling {
		t.Fatal("yanking with no target should leave scroll mode")
	}
}

// TestYankBlockSelection copies a rectangular selection: only the chosen columns
// of each row reach the clipboard.
func TestYankBlockSelection(t *testing.T) {
	m := copyModel(t, 20)
	m.scrollOff = 4
	m = m.copyBlockToggle()
	if !m.copyBlock {
		t.Fatal("V should start a block selection")
	}
	m = m.adjustCopyCol(-2) // pull the right edge in
	next, cmd := m.yankSelection()
	m = next.(model)
	if cmd == nil {
		t.Fatal("y should emit a clipboard command for a block selection")
	}
	if m.copySelecting || m.scrolling {
		t.Fatal("y should leave copy + scroll mode")
	}
}

// TestAdjustCopyColOutsideBlock: nudging the column edge is a no-op outside a block
// selection.
func TestAdjustCopyColOutsideBlock(t *testing.T) {
	m := copyModel(t, 10)
	before := m.copyCol
	m = m.adjustCopyCol(3)
	if m.copyCol != before {
		t.Fatalf("adjustCopyCol should be a no-op outside a block, col %d -> %d", before, m.copyCol)
	}
}

// TestCopyToggleNothingToSelect: with no scroll target there is nothing to anchor.
func TestCopyToggleNothingToSelect(t *testing.T) {
	m := model{mode: modeDashboard}
	m = m.copyToggle()
	if m.copySelecting {
		t.Fatal("copyToggle with no target should not start a selection")
	}
	if !strings.Contains(m.status, "nothing to select") {
		t.Fatalf("expected a nothing-to-select status, got %q", m.status)
	}
}

// TestCopyBlockToggleNothingToSelect: likewise for a block selection.
func TestCopyBlockToggleNothingToSelect(t *testing.T) {
	m := model{mode: modeDashboard}
	m = m.copyBlockToggle()
	if m.copySelecting {
		t.Fatal("copyBlockToggle with no target should not start a selection")
	}
}

// TestCopyBlockToggleClears: a second V clears an active block selection.
func TestCopyBlockToggleClears(t *testing.T) {
	m := copyModel(t, 10)
	m = m.copyBlockToggle()
	m = m.copyBlockToggle()
	if m.copySelecting || m.copyBlock {
		t.Fatal("a second V should clear the block selection")
	}
}

// TestSelectWindowLineBand: an active line selection paints a reverse-video band
// over the selected rows of the window.
func TestSelectWindowLineBand(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	fillLines(emu, 30)
	m := model{emu: emu, mode: modeZoom, width: 20, height: 8}
	m.copySelecting = true
	m.copyAnchor = 0 // whole buffer up to the window top is selected

	lines := m.selectWindow(emu, 20, 4, 30)
	banded := false
	for _, l := range lines {
		if strings.Contains(l, "\x1b[7m") {
			banded = true
		}
	}
	if !banded {
		t.Fatal("a line selection should band at least one row in reverse video")
	}
}

// TestSelectWindowBlockBand: a block selection bands only its chosen columns.
func TestSelectWindowBlockBand(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	fillLines(emu, 30)
	m := model{emu: emu, mode: modeZoom, width: 20, height: 8}
	m.copySelecting, m.copyBlock = true, true
	m.copyAnchor = 0
	m.copyCol = 2

	lines := m.selectWindow(emu, 20, 4, 30)
	banded := false
	for _, l := range lines {
		if strings.Contains(l, "\x1b[7m") {
			banded = true
		}
	}
	if !banded {
		t.Fatal("a block selection should band its columns in reverse video")
	}
}

// TestSelectWindowNoSelection: without an active selection the window is untouched.
func TestSelectWindowNoSelection(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	fillLines(emu, 30)
	m := model{emu: emu, mode: modeZoom, width: 20, height: 8}
	lines := m.selectWindow(emu, 20, 4, 2)
	for _, l := range lines {
		if strings.Contains(l, "\x1b[7m") {
			t.Fatal("no selection should leave the window unbanded")
		}
	}
}

// TestSelectCellsVariants: full-width falls back to a line band; a narrow band pads
// its head and keeps the tail plain.
func TestSelectCellsVariants(t *testing.T) {
	full := selectCells("abc", 6, 6) // width >= cols → whole-line band
	if !strings.HasPrefix(full, "\x1b[7m") || !strings.HasSuffix(full, "\x1b[27m") {
		t.Fatalf("a full-width block should render a whole-line band, got %q", full)
	}

	narrow := selectCells("abcdefgh", 3, 8) // banded head, plain tail
	if !strings.Contains(narrow, "\x1b[7m") || !strings.Contains(narrow, "\x1b[27m") {
		t.Fatalf("a narrow block should band its head, got %q", narrow)
	}

	padded := selectCells("ab", 4, 8) // head shorter than the band → padded
	if !strings.Contains(padded, "ab  ") {
		t.Fatalf("a short head should pad to the band width, got %q", padded)
	}
}

// TestScrollColsGroupZoom exercises the split-view column width branch.
func TestScrollColsGroupZoom(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0])
	if m.mode != modeGroupZoom {
		t.Fatalf("expected the split, got mode=%v", m.mode)
	}
	if got := m.scrollCols(); got <= 0 {
		t.Fatalf("the split's focused-tile width should be positive, got %d", got)
	}
}
