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

// TestResizeFocusedWidensMainTile: widening the focused main tile grows its width
// and stores the group's view-local ratios.
func TestResizeFocusedWidensMainTile(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.groupName = "api" // 3 members
	m.groupLayout = map[string]string{"api": layoutMainVertical}
	m.groupFocus = 0 // the main tile

	before, _ := m.layoutRects()
	m = m.resizeFocused(1, 0) // widen
	after, ok := m.layoutRects()
	if !ok {
		t.Fatal("layout must still resolve after a resize")
	}
	if after[0].w <= before[0].w {
		t.Errorf("widening the main tile should grow its width: before=%d after=%d", before[0].w, after[0].w)
	}
	if _, stored := m.groupRatios["api"]; !stored {
		t.Error("resize should store the group's ratios")
	}
}

// TestEnterResizeNeedsSplitLayout: resize does not arm on the even "tiled" grid,
// which has no per-track sizing to skew; the status points at the layout key.
func TestEnterResizeNeedsSplitLayout(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.groupName = "api" // default tiled
	m = m.enterResize()
	if m.groupResize {
		t.Error("resize must not arm on the even grid")
	}
	if !strings.Contains(m.status, "split layout") {
		t.Errorf("status should guide to a layout, got %q", m.status)
	}
}

// TestResizeRefusesCollapse: shrinking a tile repeatedly stops at the weight floor
// rather than collapsing it or dropping the layout back to the even grid.
func TestResizeRefusesCollapse(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.groupName = "api"
	m.groupLayout = map[string]string{"api": layoutMainVertical}
	m.groupFocus = 0
	for i := 0; i < 20; i++ {
		m = m.resizeFocused(-1, 0) // hammer the shrink
	}
	if _, ok := m.layoutRects(); !ok {
		t.Fatal("layout must stay resolved after aggressive shrinking")
	}
	if r := m.groupRatios["api"]; len(r.cols) == 0 || r.cols[0] < resizeMinWeight {
		t.Errorf("main column weight should hold at or above the floor %.2f: %v", resizeMinWeight, r.cols)
	}
}

// TestSplitGridRendersSpannedLayout: a non-tiled layout composes a frame the size
// of the split area (full width, full height net of the footer + header).
func TestSplitGridRendersSpannedLayout(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.groupName = "api" // 3 members
	m.groupLayout = map[string]string{"api": layoutMainVertical}

	grid := m.renderSplitGrid(m.splitSlots())
	lines := strings.Split(grid, "\n")
	wantH := m.height - 1 - groupHeaderRows
	if len(lines) != wantH {
		t.Fatalf("spanned grid height = %d, want %d", len(lines), wantH)
	}
}
