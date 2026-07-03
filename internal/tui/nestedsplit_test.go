package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// TestSplitShowsChildGroupTiles: opening a group scopes the split to its direct
// panels plus one tile per immediate sub-group, so the focus walks all of them.
func TestSplitShowsChildGroupTiles(t *testing.T) {
	m := baseModel()
	m.fleet = nestedFleet() // backend: 1 direct panel + sub-groups api, db
	m.groupName = "backend"

	cgs := m.childGroups()
	if len(cgs) != 2 || cgs[0].path != "backend/api" || cgs[1].path != "backend/db" {
		t.Fatalf("childGroups should be [backend/api, backend/db], got %+v", cgs)
	}
	if tiles := m.tileMembers(); len(tiles) != 1 || tiles[0].ID != "1" {
		t.Fatalf("direct panels should be just panel 1, got %+v", tiles)
	}
	if n := m.focusCount(); n != 3 { // 1 panel + 2 sub-groups, no overflow
		t.Fatalf("focusCount should be 3 (panel + 2 sub-groups), got %d", n)
	}
}

// TestFocusedChildGroup: the sub-group tiles occupy the focus slots after the panel
// tiles, and a panel-tile focus is not a sub-group.
func TestFocusedChildGroup(t *testing.T) {
	m := baseModel()
	m.fleet = nestedFleet()
	m.groupName = "backend"

	m.groupFocus = 0 // the direct panel tile
	if _, ok := m.focusedChildGroup(); ok {
		t.Fatal("focus 0 is a panel tile, not a sub-group")
	}
	m.groupFocus = 1 // first sub-group tile
	if cg, ok := m.focusedChildGroup(); !ok || cg.path != "backend/api" {
		t.Fatalf("focus 1 should be backend/api, got %+v ok=%v", cg, ok)
	}
	m.groupFocus = 2
	if cg, ok := m.focusedChildGroup(); !ok || cg.path != "backend/db" {
		t.Fatalf("focus 2 should be backend/db, got %+v ok=%v", cg, ok)
	}
}

// TestDescendRescopesToChild: descending into a sub-group re-points the split at the
// child path and shows the child's own direct panels.
func TestDescendRescopesToChild(t *testing.T) {
	m := baseModel()
	m.fleet = nestedFleet()
	m.groupName = "backend"
	m.groupFocus = 1 // backend/api

	cg, ok := m.focusedChildGroup()
	if !ok {
		t.Fatal("expected a sub-group under the focus")
	}
	m = m.rescopeGroup(cg.path)
	if m.groupName != "backend/api" || m.groupFocus != 0 {
		t.Fatalf("descend should scope to backend/api at focus 0, got %q/%d", m.groupName, m.groupFocus)
	}
	if tiles := m.tileMembers(); len(tiles) != 2 {
		t.Fatalf("backend/api has 2 direct panels, got %d", len(tiles))
	}
}

// TestPopAscendsThenExits: back pops one level to the parent group, and from the top
// level it leaves the split for the dashboard.
func TestPopAscendsThenExits(t *testing.T) {
	m := baseModel()
	m.fleet = nestedFleet()
	m.mode = modeGroupZoom
	m.groupName = "backend/api"

	tm, _ := m.popGroupLevel()
	if got := tm.(model); got.groupName != "backend" || got.mode != modeGroupZoom {
		t.Fatalf("pop from backend/api should ascend to backend, got %q mode=%v", got.groupName, got.mode)
	}

	top := baseModel()
	top.fleet = nestedFleet()
	top.mode = modeGroupZoom
	top.groupName = "backend"
	tm2, _ := top.popGroupLevel()
	if got := tm2.(model); got.mode != modeDashboard {
		t.Fatalf("pop from the top level should return to the dashboard, got mode %v", got.mode)
	}
}

// TestContainerGroupDoesNotBail: a group with only sub-groups (no direct panels)
// still renders — it must not reset to the dashboard as an "emptied" group.
func TestContainerGroupDoesNotBail(t *testing.T) {
	m := baseModel()
	m.fleet = []panel.Panel{
		{ID: "2", Kind: panel.Agent, Title: "api · a", State: panel.Running, Group: "backend/api"},
		{ID: "3", Kind: panel.Agent, Title: "db · a", State: panel.Idle, Group: "backend/db"},
	}
	m.mode = modeGroupZoom
	m.groupName = "backend" // no panel is directly in "backend"

	(&m).reconcileGroupTiles("")
	if m.mode != modeGroupZoom {
		t.Fatalf("a container group with only sub-groups must not bail to the dashboard")
	}
	if len(m.childGroups()) != 2 {
		t.Fatalf("expected the two sub-groups to remain, got %d", len(m.childGroups()))
	}
}
