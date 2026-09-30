package tui

import (
	"testing"

	vt "github.com/charmbracelet/x/vt"
)

// TestZoomCursorFollowsProgram proves the terminal cursor the zoom reports is the
// zoomed program's own, cell for cell — including after a wide glyph, where a cursor
// counted in runes would land a column short (#140).
func TestZoomCursorFollowsProgram(t *testing.T) {
	emu := vt.NewSafeEmulator(80, 23)
	hidden := false
	m := model{emu: emu, mode: modeZoom, width: 80, height: 24, cursorHidden: &hidden}
	_, _ = emu.Write([]byte("\x1b[3;1H❯ hello 中文"))
	cur := m.View().Cursor
	if cur == nil {
		t.Fatal("the zoom should report a cursor")
	}
	if cur.X != 12 || cur.Y != 2 {
		t.Fatalf("cursor = (%d,%d), want (12,2): two wide glyphs take four cells", cur.X, cur.Y)
	}
}

// TestZoomCursorWithheld lists the states in which the zoom must not report a
// cursor: history (scrolled back), a copy selection, a text-input overlay that
// holds the keyboard, the dashboard, a cockpit too small to draw a zoom at all.
func TestZoomCursorWithheld(t *testing.T) {
	emu := vt.NewSafeEmulator(80, 23)
	hidden := false
	base := model{emu: emu, mode: modeZoom, width: 80, height: 24, cursorHidden: &hidden}
	cases := map[string]func(m model) model{
		"scrolled back":   func(m model) model { m.scrollOff = 3; return m },
		"copy selecting":  func(m model) model { m.copySelecting = true; return m },
		"typing a search": func(m model) model { m.input = inputSearch; return m },
		"not zoomed":      func(m model) model { m.mode = modeDashboard; return m },
		"too small":       func(m model) model { m.width, m.height = 2, 2; return m },
		"no emulator yet": func(m model) model { m.emu = nil; return m },
	}
	if base.View().Cursor == nil {
		t.Fatal("the base zoom should report a cursor")
	}
	for name, tweak := range cases {
		if tweak(base).View().Cursor != nil {
			t.Errorf("%s: the zoom should withhold the cursor", name)
		}
	}
}
