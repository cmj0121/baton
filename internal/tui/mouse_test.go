package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/panel"
)

// wheel builds the mouse event a wheel turn carries. Which turn it is lives in
// the button; that it is a turn at all is the message type Update matched, and
// by here that is already decided.
func wheel(btn tea.MouseButton) tea.Mouse {
	return tea.Mouse{Button: btn}
}

// TestMouseSetting covers the mouse toggle row: its label, its value tracking the
// model flag, and that it is a distinct row from the bell and confirm toggles.
func TestMouseSetting(t *testing.T) {
	if settingMouse == settingBell || settingMouse == settingConfirmClose {
		t.Fatal("the mouse toggle must be its own settings row")
	}
	if numSettings != 4 {
		t.Fatalf("expected four settings rows, got %d", numSettings)
	}
	on := model{mouseEnabled: true}
	off := model{mouseEnabled: false}
	if !on.settingValue(settingMouse) || off.settingValue(settingMouse) {
		t.Fatal("settingValue should track mouseEnabled")
	}
	if on.settingLabel(settingMouse) == on.settingLabel(settingBell) {
		t.Fatal("the mouse toggle needs its own label")
	}
}

// TestMouseWheelDashboard proves the wheel steps the dashboard selection like the
// arrow keys, clamped at both ends.
func TestMouseWheelDashboard(t *testing.T) {
	m := model{mode: modeDashboard, mouseEnabled: true, fleet: []panel.Panel{
		{ID: "a", Title: "a"}, {ID: "b", Title: "b"}, {ID: "c", Title: "c"},
	}}

	next, _ := m.handleMouse(wheel(tea.MouseWheelDown))
	m = next.(model)
	if m.cursor != 1 {
		t.Fatalf("wheel down should advance the selection, cursor = %d", m.cursor)
	}
	next, _ = m.handleMouse(wheel(tea.MouseWheelUp))
	m = next.(model)
	if m.cursor != 0 {
		t.Fatalf("wheel up should step back, cursor = %d", m.cursor)
	}
	// At the top, wheel up holds rather than wrapping.
	next, _ = m.handleMouse(wheel(tea.MouseWheelUp))
	m = next.(model)
	if m.cursor != 0 {
		t.Fatalf("wheel up at the top should clamp, cursor = %d", m.cursor)
	}
}

// TestMouseWheelZoomScroll proves the wheel enters scroll mode and walks the
// scrollback in a zoom, then drops out once back at the live bottom.
func TestMouseWheelZoomScroll(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	fillLines(emu, 30)
	m := model{emu: emu, mode: modeZoom, zoomID: "1", width: 20, height: 5, mouseEnabled: true}

	next, _ := m.handleMouse(wheel(tea.MouseWheelUp))
	m = next.(model)
	if !m.scrolling {
		t.Fatal("wheel up should open scroll mode")
	}
	if m.scrollOff != mouseWheelLines {
		t.Fatalf("wheel up should scroll %d lines, off = %d", mouseWheelLines, m.scrollOff)
	}

	// Wheel back down past the bottom: clamps to 0 and leaves scroll mode.
	for i := 0; i < 3; i++ {
		next, _ = m.handleMouse(wheel(tea.MouseWheelDown))
		m = next.(model)
	}
	if m.scrolling || m.scrollOff != 0 {
		t.Fatalf("wheeling back to the bottom should exit scroll mode, scrolling=%v off=%d", m.scrolling, m.scrollOff)
	}
}

// TestMouseWheelInZoomNoFallthrough proves a wheel in a zoom with nothing to
// scroll (no emulator yet) never reaches back to move the hidden dashboard.
func TestMouseWheelInZoomNoFallthrough(t *testing.T) {
	m := model{mode: modeZoom, mouseEnabled: true, emu: nil, fleet: []panel.Panel{{ID: "a"}, {ID: "b"}}}
	m.cursor = 0
	for _, b := range []tea.MouseButton{tea.MouseWheelDown, tea.MouseWheelUp} {
		next, _ := m.handleMouse(wheel(b))
		if next.(model).cursor != 0 {
			t.Fatal("a wheel in a zoom must not move the dashboard cursor")
		}
	}
}

// TestMouseIgnoredWithOverlay proves the wheel is inert while an input overlay
// (filter, search, rename…) is open, so it never scrolls behind a prompt.
func TestMouseIgnoredWithOverlay(t *testing.T) {
	m := model{mode: modeDashboard, mouseEnabled: true, input: inputFilter,
		fleet: []panel.Panel{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	m.cursor = 1
	next, _ := m.handleMouse(wheel(tea.MouseWheelDown))
	if next.(model).cursor != 1 {
		t.Fatal("the wheel should be ignored while an input overlay is open")
	}
}

// TestMouseNonWheelIgnored proves a click (non-wheel press) leaves the view be,
// so a stray button never disturbs the selection or the scroll.
func TestMouseNonWheelIgnored(t *testing.T) {
	m := model{mode: modeDashboard, mouseEnabled: true, fleet: []panel.Panel{{ID: "a"}, {ID: "b"}}}
	m.cursor = 1
	next, _ := m.handleMouse(tea.Mouse{Button: tea.MouseLeft})
	if next.(model).cursor != 1 {
		t.Fatal("a non-wheel click should not move the selection")
	}
}
