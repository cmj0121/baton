package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// grabFleet: two work items, one nested, and a loose panel.
func grabFleet() []panel.Panel {
	return []panel.Panel{
		{ID: "1", Title: "api a", State: panel.Running, Group: "backend/api"},
		{ID: "2", Title: "backend d", State: panel.Running, Group: "backend"},
		{ID: "3", Title: "web a", State: panel.Running, Group: "frontend"},
		{ID: "4", Title: "lone", State: panel.Idle},
	}
}

func grabModel(t *testing.T) (model, <-chan proto.Command) {
	t.Helper()
	c, cmds := recordingServer(t)
	m := baseModel()
	m.mode = modeDashboard
	m.client = c
	m.fleet = grabFleet()
	m.showTree = true // carrying a row INTO a work item is a tree gesture
	return m, cmds
}

// TestGrabCarriesAPanelIntoAWorkItem is the gesture's whole point: what used to
// be seven steps across two views is pick up, move, drop.
func TestGrabCarriesAPanelIntoAWorkItem(t *testing.T) {
	m, cmds := grabModel(t)
	m.cursorOnPanel(t, "4") // the loose panel
	m = m.startGrab()
	if !m.grabbing() {
		t.Fatal("space should pick the row up")
	}

	m.cursorOnPanel(t, "1") // a panel inside backend/api — the level to drop into
	m = m.dropGrab()

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.group" })
	if got.Group != "backend/api" || len(got.IDs) != 1 || got.IDs[0] != "4" {
		t.Fatalf("the drop should file #4 into backend/api, got %+v", got)
	}
	if m.grabbing() {
		t.Fatal("the drop should end the grab")
	}
}

// TestGrabDropsToTheTopLevelUngroups: carrying a row out past every work item is
// the same thing as saying it belongs to none of them, so the gesture absorbs the
// ungroup verb for a single panel.
func TestGrabDropsToTheTopLevelUngroups(t *testing.T) {
	m, cmds := grabModel(t)
	m.cursorOnPanel(t, "1")
	m = m.startGrab()
	m.cursorOnPanel(t, "4") // a top-level row
	m = m.dropGrab()

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.ungroup" })
	if len(got.IDs) != 1 || got.IDs[0] != "1" {
		t.Fatalf("dropping at the top level should ungroup #1, got %+v", got)
	}
}

// TestGrabNestsAWorkItem: carrying a group into another nests it, sub-structure
// and all. Before this the only way to make a nested work item was to know that a
// group name is a slash-delimited path and type one into the rename box.
func TestGrabNestsAWorkItem(t *testing.T) {
	m, cmds := grabModel(t)
	m.cursorOnGroup(t, "frontend")
	m = m.startGrab()
	m.cursorOnPanel(t, "2") // a panel filed directly in backend
	m = m.dropGrab()

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.rename" })
	if got.Group != "frontend" || got.Name != "backend/frontend" {
		t.Fatalf("the drop should re-parent frontend under backend, got group=%q name=%q", got.Group, got.Name)
	}
}

// TestGrabPromotesAWorkItemToTheTop is the same move in reverse.
func TestGrabPromotesAWorkItemToTheTop(t *testing.T) {
	m, cmds := grabModel(t)
	m.cursorOnGroup(t, "backend/api")
	m = m.startGrab()
	m.cursorOnPanel(t, "4") // a top-level row
	m = m.dropGrab()

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.rename" })
	if got.Group != "backend/api" || got.Name != "api" {
		t.Fatalf("the drop should promote api to the top, got group=%q name=%q", got.Group, got.Name)
	}
}

// TestGrabRefusesToNestAGroupInItself: rewriting a path prefix onto its own
// descendants has no meaning and no way back.
func TestGrabRefusesToNestAGroupInItself(t *testing.T) {
	m, _ := grabModel(t)
	m.cursorOnGroup(t, "backend")
	m = m.startGrab()
	m.cursorOnPanel(t, "1") // inside backend/api — a descendant of what is being carried
	m = m.dropGrab()

	if !strings.Contains(m.status, "inside itself") {
		t.Fatalf("the drop should be refused with a reason, got %q", m.status)
	}
}

// TestGrabOnTheSameLevelReorders: dropping where it already lives is a reorder,
// not a re-file, so the group is left alone.
func TestGrabOnTheSameLevelReorders(t *testing.T) {
	m, cmds := grabModel(t)
	m.cursorOnPanel(t, "4")
	m = m.startGrab()
	m.cursorOnGroup(t, "backend") // a top-level row, where #4 already sits
	m = m.dropGrab()

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.move" })
	if len(got.IDs) != 1 || got.IDs[0] != "4" {
		t.Fatalf("a same-level drop should reorder, got %+v", got)
	}
}

// TestGrabRefusesTheQuietFold: the fold stands for panels without carrying them,
// so it can be neither picked up nor dropped on.
func TestGrabRefusesTheQuietFold(t *testing.T) {
	m := foldedModel(t)
	if it, _ := m.selectedItem(); it.kind != itemFold {
		t.Fatal("setup: the cursor should rest on the fold row")
	}
	m = m.startGrab()
	if m.grabbing() {
		t.Fatal("a fold row must not be picked up")
	}
	if !strings.Contains(m.status, "expand") {
		t.Fatalf("the refusal should say what to do instead, got %q", m.status)
	}
}

// TestGrabCancelSendsNothing: nothing travels until the drop, so esc is a true
// undo rather than a second move.
func TestGrabCancelSendsNothing(t *testing.T) {
	m, cmds := grabModel(t)
	m.cursorOnPanel(t, "4")
	m = m.startGrab()
	m.cursorOnPanel(t, "1")
	m = m.cancelGrab()

	if m.grabbing() {
		t.Fatal("esc should end the grab")
	}
	select {
	case c := <-cmds:
		t.Fatalf("a cancelled grab must send nothing, got %+v", c)
	default:
	}
}

// TestGrabIsHeldByIdentity: the list reflows under a grab — a snapshot lands, a
// level folds — and a row number would then be carrying something else.
func TestGrabIsHeldByIdentity(t *testing.T) {
	m, _ := grabModel(t)
	m.cursorOnPanel(t, "4")
	m = m.startGrab()

	// A new panel arrives at the head of the fleet, pushing every row down.
	nf := append([]panel.Panel{{ID: "9", Title: "new", State: panel.Running}}, grabFleet()...)
	m.applyEvent(snapshot(nf))

	if !m.grabbing() || m.grab.id != "4" {
		t.Fatalf("the grab should still be carrying #4, got %+v", m.grab)
	}
	// …and the mark is drawn on the row it still sits on.
	marked := 0
	for _, it := range m.dashItems() {
		if m.grabbedRow(it) {
			marked++
			if it.panel.ID != "4" {
				t.Fatalf("the wrong row is marked: %+v", it)
			}
		}
	}
	if marked != 1 {
		t.Fatalf("exactly one row should be marked as carried, got %d", marked)
	}
}

// TestGrabKeysDriveIt checks the gesture is bound, not only implemented.
func TestGrabKeysDriveIt(t *testing.T) {
	m, cmds := grabModel(t)
	m.cursorOnPanel(t, "4")

	m = press(m, " ")
	if !m.grabbing() {
		t.Fatal("space should pick the row up")
	}
	m.cursorOnPanel(t, "1")
	m = press(m, "enter")
	if m.grabbing() {
		t.Fatal("enter should drop it")
	}
	if got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.group" }); got.Group != "backend/api" {
		t.Fatalf("enter should have committed the move, got %+v", got)
	}

	// esc cancels rather than folding or clearing a filter.
	m.cursorOnPanel(t, "4")
	m = press(m, " ")
	m = press(m, "esc")
	if m.grabbing() {
		t.Fatal("esc should cancel the grab")
	}
}
