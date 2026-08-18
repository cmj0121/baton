package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// walkFleet nests two work items under one, with a loose panel alongside.
func walkFleet() []panel.Panel {
	return []panel.Panel{
		{ID: "1", Title: "backend d", State: panel.Running, Group: "backend"},
		{ID: "2", Title: "api a", State: panel.Running, Group: "backend/api"},
		{ID: "3", Title: "lone", State: panel.Idle},
	}
}

func walkModel() model {
	m := baseModel()
	m.mode = modeDashboard
	m.fleet = walkFleet()
	return m
}

// TestRightOpensThenDescends: → has nothing left to open on an already-open work
// item, so it steps into it — which is how every other tree is walked.
func TestRightOpensThenDescends(t *testing.T) {
	m := walkModel()
	m.collapsed = map[string]bool{"backend": true}
	m.cursorOnGroup(t, "backend")

	m = m.expandSelected()
	if it, _ := m.selectedItem(); it.kind != itemGroup || it.name != "backend" || !it.expanded {
		t.Fatalf("→ should open the group and stay on it, got %+v", it)
	}

	m = m.expandSelected()
	if it, _ := m.selectedItem(); it.parent != "backend" {
		t.Fatalf("→ on an open group should step inside it, got %+v", it)
	}
}

// TestRightOnAPanelDoesNothing: a panel is a leaf. A key that silently walked off
// it would make → mean "down" on most of the rows in a fleet.
func TestRightOnAPanelDoesNothing(t *testing.T) {
	m := walkModel()
	m.cursorOnPanel(t, "3")
	at := m.cursor

	m = m.expandSelected()
	if m.cursor != at {
		t.Fatalf("→ on a panel moved the cursor %d -> %d", at, m.cursor)
	}
}

// TestLeftShutsThenStepsOut: ← shuts an open work item, and from anything else
// jumps to the row that contains it — so ← walks back up the way → walked down.
func TestLeftShutsThenStepsOut(t *testing.T) {
	m := walkModel()
	m.cursorOnGroup(t, "backend")

	m = m.collapseSelected()
	if it, _ := m.selectedItem(); it.name != "backend" || it.expanded {
		t.Fatalf("← should shut the group and stay on it, got %+v", it)
	}
	if !m.collapsed["backend"] {
		t.Fatal("the collapse should be recorded by path")
	}

	// From a nested panel, ← goes to its parent rather than doing nothing.
	m.collapsed = nil
	m.cursorOnPanel(t, "2") // inside backend/api
	m = m.collapseSelected()
	if it, _ := m.selectedItem(); it.kind != itemGroup || it.name != "backend/api" {
		t.Fatalf("← from a nested panel should land on its parent, got %+v", it)
	}
}

// TestLeftAtTheTopStaysPut: a top-level row has no parent to step out to.
func TestLeftAtTheTopStaysPut(t *testing.T) {
	m := walkModel()
	m.cursorOnPanel(t, "3")
	at := m.cursor

	m = m.collapseSelected()
	if m.cursor != at {
		t.Fatalf("← at the top moved the cursor %d -> %d", at, m.cursor)
	}
}

// TestCollapseKeepsTheCursorOnTheGroup is the case an index would get wrong:
// shutting a work item removes every row beneath it, so a cursor holding a number
// would point at something else or past the end.
func TestCollapseKeepsTheCursorOnTheGroup(t *testing.T) {
	m := walkModel()
	m.cursorOnGroup(t, "backend/api")
	m = m.collapseSelected() // shut it — the rows under it go

	if it, ok := m.selectedItem(); !ok || it.kind != itemGroup || it.name != "backend/api" {
		t.Fatalf("the cursor should stay on the group it just shut, got %+v ok=%v", it, ok)
	}
}

// TestArrowsDriveTheTree checks the keys are actually bound to it, not only the
// methods behind them.
func TestArrowsDriveTheTree(t *testing.T) {
	m := walkModel()
	m.cursorOnGroup(t, "backend")

	m = press(m, "left")
	if !m.collapsed["backend"] {
		t.Fatal("← should collapse the selected work item")
	}
	m = press(m, "right")
	if m.collapsed["backend"] {
		t.Fatal("→ should expand it again")
	}
	// h and l are the vim-side spelling of the same pair.
	m = press(m, "h")
	if !m.collapsed["backend"] {
		t.Fatal("h should collapse like ←")
	}
	m = press(m, "l")
	if m.collapsed["backend"] {
		t.Fatal("l should expand like →")
	}
}

// TestRightStillOpensTheQuietFold: the fold row was the only row with something
// inside it before the tree existed, and → has opened it since.
func TestRightStillOpensTheQuietFold(t *testing.T) {
	m := foldedModel(t)
	if it, _ := m.selectedItem(); it.kind != itemFold {
		t.Fatal("setup: the cursor should rest on the fold row")
	}
	m = m.expandSelected()
	if !m.foldOpen[""] {
		t.Fatal("→ should open the quiet fold")
	}
}

// TestCollapseStatusNamesTheGroup: the status line says which work item moved, by
// path, so a collapse in a deep tree is not a silent change of shape.
func TestCollapseStatusNamesTheGroup(t *testing.T) {
	m := walkModel()
	m.cursorOnGroup(t, "backend/api")
	if m = m.collapseSelected(); !strings.Contains(m.status, "backend/api") {
		t.Fatalf("status should name the group, got %q", m.status)
	}
}
