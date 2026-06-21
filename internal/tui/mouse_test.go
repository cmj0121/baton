package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/panel"
)

// wheel builds a press event for a wheel button.
func wheel(btn tea.MouseButton) tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: btn}
}

// TestMouseSetting covers the mouse toggle row: its label, its value tracking the
// model flag, and that it is a distinct row from the bell and confirm toggles.
func TestMouseSetting(t *testing.T) {
	if settingMouse == settingBell || settingMouse == settingConfirmClose {
		t.Fatal("the mouse toggle must be its own settings row")
	}
	if numSettings != 3 {
		t.Fatalf("expected three settings rows, got %d", numSettings)
	}
	on := model{mouseEnabled: true}
	off := model{mouseEnabled: false}
	if !on.settingValue(settingMouse) || off.settingValue(settingMouse) {
		t.Fatal("settingValue should track mouseEnabled")
	}
	if settingLabel(settingMouse) == settingLabel(settingBell) {
		t.Fatal("the mouse toggle needs its own label")
	}
}

// TestMouseWheelDashboard proves the wheel steps the dashboard selection like the
// arrow keys, clamped at both ends.
func TestMouseWheelDashboard(t *testing.T) {
	m := model{mode: modeDashboard, mouseEnabled: true, fleet: []panel.Panel{
		{ID: "a", Title: "a"}, {ID: "b", Title: "b"}, {ID: "c", Title: "c"},
	}}

	next, _ := m.handleMouse(wheel(tea.MouseButtonWheelDown))
	m = next.(model)
	if m.cursor != 1 {
		t.Fatalf("wheel down should advance the selection, cursor = %d", m.cursor)
	}
	next, _ = m.handleMouse(wheel(tea.MouseButtonWheelUp))
	m = next.(model)
	if m.cursor != 0 {
		t.Fatalf("wheel up should step back, cursor = %d", m.cursor)
	}
	// At the top, wheel up holds rather than wrapping.
	next, _ = m.handleMouse(wheel(tea.MouseButtonWheelUp))
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

	next, _ := m.handleMouse(wheel(tea.MouseButtonWheelUp))
	m = next.(model)
	if !m.scrolling {
		t.Fatal("wheel up should open scroll mode")
	}
	if m.scrollOff != mouseWheelLines {
		t.Fatalf("wheel up should scroll %d lines, off = %d", mouseWheelLines, m.scrollOff)
	}

	// Wheel back down past the bottom: clamps to 0 and leaves scroll mode.
	for i := 0; i < 3; i++ {
		next, _ = m.handleMouse(wheel(tea.MouseButtonWheelDown))
		m = next.(model)
	}
	if m.scrolling || m.scrollOff != 0 {
		t.Fatalf("wheeling back to the bottom should exit scroll mode, scrolling=%v off=%d", m.scrolling, m.scrollOff)
	}
}

// TestMouseNonWheelIgnored proves a click (non-wheel press) leaves the view be,
// so a stray button never disturbs the selection or the scroll.
func TestMouseNonWheelIgnored(t *testing.T) {
	m := model{mode: modeDashboard, mouseEnabled: true, fleet: []panel.Panel{{ID: "a"}, {ID: "b"}}}
	m.cursor = 1
	next, _ := m.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if next.(model).cursor != 1 {
		t.Fatal("a non-wheel click should not move the selection")
	}
}
