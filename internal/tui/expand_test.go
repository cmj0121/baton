package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// expandFleet nests a sub-group under a work item, with enough loose panels that
// the dashboard is a tree on its own.
func expandFleet() []panel.Panel {
	fleet := []panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "backend d", State: panel.Running, Group: "backend"},
		{ID: "2", Kind: panel.Agent, Title: "api a", State: panel.Running, Group: "backend/api"},
		{ID: "3", Kind: panel.Agent, Title: "api b", State: panel.Running, Group: "backend/api"},
	}
	return append(fleet, loosePanels(6)...)
}

// TestSpaceOpensAndShutsAWorkItem: space is the disclosure key, and unlike ← and →
// it does not move the cursor — pressing it twice leaves the dashboard exactly as
// it was found.
func TestSpaceOpensAndShutsAWorkItem(t *testing.T) {
	m := baseModel()
	m.mode, m.fleet = modeDashboard, expandFleet()
	m.cursorOnGroup(t, "backend")
	at := m.cursor

	m = press(m, " ")
	if it, _ := m.selectedItem(); it.expanded {
		t.Fatalf("space should shut the open work item, got %+v", it)
	}
	if m.cursor != at {
		t.Fatalf("space moved the cursor %d -> %d", at, m.cursor)
	}

	m = press(m, " ")
	if it, _ := m.selectedItem(); !it.expanded {
		t.Fatal("space should open it again")
	}
	if m.cursor != at {
		t.Fatalf("space moved the cursor %d -> %d", at, m.cursor)
	}
}

// TestSpaceNestsAllTheWayDown: every level keeps its own state, so a sub-group
// inside an open work item toggles on its own — the nesting goes as deep as the
// fleet does.
func TestSpaceNestsAllTheWayDown(t *testing.T) {
	m := baseModel()
	m.mode, m.fleet = modeDashboard, expandFleet()
	m.cursorOnGroup(t, "backend/api")

	m = press(m, " ")
	if m.collapsed["backend"] {
		t.Fatal("shutting the sub-group shut its parent too")
	}
	for _, it := range m.dashItems() {
		if it.parent == "backend/api" {
			t.Fatalf("the sub-group's panels should be hidden, still saw %+v", it)
		}
	}
	if it, _ := m.selectedItem(); it.kind != itemGroup || it.name != "backend/api" {
		t.Fatalf("the cursor should stay on the sub-group, got %+v", it)
	}
}

// TestSpaceOnAQuietFoldOpensIt: the fold row is the other row with something
// inside it, so the disclosure key has no exception to remember.
func TestSpaceOnAQuietFoldOpensIt(t *testing.T) {
	fleet := make([]panel.Panel, 0, 10)
	for i := 0; i < 10; i++ {
		fleet = append(fleet, panel.Panel{ID: string(rune('a' + i)), Title: "quiet", State: panel.Idle})
	}
	m := baseModel()
	m.mode, m.fleet, m.foldQuiet = modeDashboard, fleet, 8

	rows := m.dashItems()
	if len(rows) != 1 || rows[0].kind != itemFold {
		t.Fatalf("ten quiet panels should fold to one row, got %d", len(rows))
	}
	m.cursor = 0
	m = press(m, " ")
	if len(m.dashItems()) < 10 {
		t.Fatalf("space should open the fold, got %d rows", len(m.dashItems()))
	}
}

// TestSpaceOnAPanelSaysSo: a leaf answers rather than doing nothing quietly — the
// point of a disclosure key is that pressing it tells you whether there is
// anything under the row.
func TestSpaceOnAPanelSaysSo(t *testing.T) {
	m := baseModel()
	m.mode, m.fleet = modeDashboard, expandFleet()
	m.cursorOnPanel(t, "2")

	m = press(m, " ")
	if m.status == "" {
		t.Fatal("space on a panel should say there is nothing nested there")
	}
	if m.grabbing() {
		t.Fatal("space must not still pick rows up — that is the move binding now")
	}
}

// TestSpaceIsRebindable: it goes through the same lookup as every other action, so
// the key map can move it.
func TestSpaceIsRebindable(t *testing.T) {
	m := baseModel()
	m.mode, m.fleet = modeDashboard, expandFleet()
	for i := range m.binds {
		if m.binds[i].act == actExpand {
			m.binds[i].key = "M"
		}
	}
	m.cursorOnGroup(t, "backend")

	m = press(m, "M")
	if it, _ := m.selectedItem(); it.expanded {
		t.Fatalf("the rebound key should shut the work item, got %+v", it)
	}
	m = press(m, " ")
	if it, _ := m.selectedItem(); it.expanded {
		t.Fatal("space should no longer open it once it has been rebound")
	}
}

// TestSpaceOnACardShowsTheTree: the grid draws a work item whole, so the
// disclosure key means the same thing there as everywhere else — show me what is
// inside — and the only layout that can answer is the tree.
func TestSpaceOnACardShowsTheTree(t *testing.T) {
	m := baseModel()
	m.mode = modeDashboard
	m.fleet = []panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "backend d", State: panel.Running, Group: "backend"},
		{ID: "2", Kind: panel.Agent, Title: "api a", State: panel.Running, Group: "backend/api"},
	}
	if !m.gridDash() {
		t.Fatal("one work item is one card")
	}

	m.cursor = 0
	m = press(m, " ")
	if !m.treeView() {
		t.Fatalf("space on a work item card should show the tree: %s", m.status)
	}
	if it, _ := m.selectedItem(); it.name != "backend" {
		t.Fatalf("the cursor should stay on the work item, got %+v", it)
	}
}
