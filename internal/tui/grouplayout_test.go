package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
)

// TestGroupLayoutNameDefaults: with nothing set the group opens tiled; a configured
// default applies; a per-group server choice wins over the default.
func TestGroupLayoutNameDefaults(t *testing.T) {
	m := baseModel()
	m.groupName = "api"
	if got := m.groupLayoutName(); got != layoutTiled {
		t.Fatalf("default should be tiled, got %q", got)
	}
	m.tuiCfg = config.TUIConfig{DefaultLayout: layoutStack}
	if got := m.groupLayoutName(); got != layoutStack {
		t.Fatalf("configured default should apply, got %q", got)
	}
	m.groupLayout = map[string]string{"api": layoutMainVertical}
	if got := m.groupLayoutName(); got != layoutMainVertical {
		t.Fatalf("per-group choice should win, got %q", got)
	}
}

// TestAvailableLayoutsIncludesCustom: the cycle order is the presets plus any
// custom layouts that do not shadow a preset name.
func TestAvailableLayoutsIncludesCustom(t *testing.T) {
	m := baseModel()
	m.tuiCfg = config.TUIConfig{Layouts: []config.Layout{
		{Name: "review"},
		{Name: layoutStack}, // shadows a preset — must not duplicate
	}}
	got := m.availableLayouts()
	if len(got) != len(presetLayouts)+1 || got[len(got)-1] != "review" {
		t.Fatalf("availableLayouts = %v, want presets + review", got)
	}
}

// TestCycleGroupLayoutAdvancesAndSends: L advances the layout and sends group.layout.
func TestCycleGroupLayoutAdvancesAndSends(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.groupName = "api"
	m = m.cycleGroupLayout(1)
	if got := m.groupLayoutName(); got != layoutMainVertical {
		t.Fatalf("cycle from tiled should land on main-vertical, got %q", got)
	}
	if !strings.Contains(m.status, "main-vertical") {
		t.Fatalf("status should name the new layout, got %q", m.status)
	}
}

// TestSplitGridRendersSpannedLayout: a non-tiled layout composes a frame the size
// of the split area (full width, full height net of the footer + header).
func TestSplitGridRendersSpannedLayout(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.groupName = "api" // 3 members
	m.groupLayout = map[string]string{"api": layoutMainVertical}

	tiles, collapsed := m.splitMembers()
	grid := m.renderSplitGrid(tiles, collapsed)
	lines := strings.Split(grid, "\n")
	wantH := m.height - 1 - groupHeaderRows
	if len(lines) != wantH {
		t.Fatalf("spanned grid height = %d, want %d", len(lines), wantH)
	}
}
