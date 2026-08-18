package tui

import (
	"fmt"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// Grab-and-move: pick a row up, carry it through the tree, put it down.
//
// Reorganising a fleet used to be a modal round trip. Moving one panel from
// `backend` to `frontend` meant entering backend's split, finding the panel,
// removing it from the group, coming back to the dashboard, finding it again in
// the flat list, marking it, moving the cursor to frontend and adding it — seven
// steps across two views. And `a` only ever filed into a TOP-LEVEL group, so
// there was no way to put anything into `backend/api` at all.
//
// One gesture replaces that, and it replaces two others with it. Reordering and
// re-parenting were separate verbs (shift+arrows moved a row within its level,
// mark-and-group changed which level it was on); carrying a row through the tree
// is both at once, and you can see where it will land before you let go.
//
// It also makes NESTING discoverable for the first time. Nested work items have
// existed, with tests, since before the dashboard could draw them — but the only
// way to create one was to know that a group name is a slash-delimited path and
// type `backend/db` into the rename box. Now you carry `db` into `backend` and
// drop it.

// grabState is the row being carried. It is held by IDENTITY rather than by row
// number for the reason the cursor is: the list reflows under a grab — a snapshot
// lands, a group folds — and an index would then be carrying something else.
type grabState struct {
	kind dashKind
	id   string // itemPanel: the panel id
	path string // itemGroup: the full group path
	name string // what the status line calls it
}

// grabbing reports whether a row is currently being carried.
func (m model) grabbing() bool { return m.grab != nil }

// toggleGrab picks the selected row up, or puts it down where the cursor is.
func (m model) toggleGrab() model {
	if m.grabbing() {
		return m.dropGrab()
	}
	return m.startGrab()
}

// startGrab picks up the selected row.
//
// A quiet fold row cannot be picked up: it stands for panels without carrying
// them — deliberately, so that no bulk verb can reach them through it — and a row
// that answered "these twelve" to a drop would be exactly the surprise it is not
// allowed to be. Expand it and carry a real row instead.
func (m model) startGrab() model {
	it, ok := m.selectedItem()
	if !ok {
		m.status = "nothing to move"
		return m
	}
	switch it.kind {
	case itemFold:
		m.status = "expand the quiet rows first — the fold stands for panels, it does not hold them"
		return m
	case itemGroup:
		m.grab = &grabState{kind: itemGroup, path: it.name, name: it.label()}
	default:
		m.grab = &grabState{kind: itemPanel, id: it.panel.ID, name: it.panel.Title}
	}
	m.status = fmt.Sprintf("moving %s · ↑↓ to place it, enter to drop, esc to cancel", m.grab.name)
	return m
}

// cancelGrab puts the row back where it was, which is where it still is: nothing
// has been sent until the drop.
func (m model) cancelGrab() model {
	if !m.grabbing() {
		return m
	}
	m.status = "left " + m.grab.name + " where it was"
	m.grab = nil
	return m
}

// dropGrab commits the move: the carried row becomes a sibling of whatever the
// cursor is on, placed just after it.
//
// The drop target is the LEVEL of the row under the cursor, never the row itself.
// That is what makes it unambiguous — "into this group" and "after this group"
// would otherwise be the same keystroke on the same row — and it composes with
// collapsing: a shut work item is one row, so you move past it, and to put
// something inside it you open it and land on one of its children. Which is the
// rule the whole feature keeps: expansion is a view state, and a view state may
// decide what you can REACH but never what a verb means.
func (m model) dropGrab() model {
	g := m.grab
	if g == nil {
		return m
	}
	target, ok := m.selectedItem()
	m.grab = nil
	if !ok {
		m.status = "nowhere to drop " + g.name
		return m
	}
	if target.kind == itemFold {
		m.status = "a quiet fold is not a place to drop " + g.name
		return m
	}

	parent := target.parent
	if g.kind == itemGroup {
		return m.reparentGroup(*g, parent)
	}
	return m.refilePanel(*g, parent, target)
}

// reparentGroup moves a whole work item under a new parent, carrying its
// sub-structure with it. The server does the work: a rename to a path rewrites
// the prefix across every descendant in one move.
func (m model) reparentGroup(g grabState, parent string) model {
	if panel.GroupParent(g.path) == parent {
		m.status = g.name + " is already there"
		return m
	}
	if panel.GroupIsUnder(g.path, parent) {
		// Dropping a work item inside itself would ask the server to rewrite a path
		// prefix onto its own descendants, which has no meaning and no way back.
		m.status = "cannot move " + g.name + " inside itself"
		return m
	}
	dest := panel.GroupJoin(parent, panel.GroupLeaf(g.path))
	if m.nameConflict(dest, "", g.path) {
		m.status = fmt.Sprintf("%q is already taken — rename it first", dest)
		return m
	}
	m.sendf(proto.Command{Action: "panel.rename", Group: g.path, Name: dest})
	if parent == "" {
		m.status = "moved " + g.name + " to the top level"
	} else {
		m.status = "moved " + g.name + " into " + parent
	}
	return m
}

// refilePanel moves one panel to a new level: into a work item, or out to the top.
//
// Dropping at the top level UNGROUPS the panel, which is how the gesture absorbs
// the ungroup verb for a single panel — carrying a row out past every work item is
// the same thing as saying it belongs to none of them.
func (m model) refilePanel(g grabState, parent string, target dashItem) model {
	cur := ""
	for _, p := range m.fleet {
		if p.ID == g.id {
			cur = p.Group
		}
	}
	if cur == parent {
		// Same level: this is a reorder rather than a re-file, so place it after the
		// row it was dropped on and leave its group alone.
		return m.moveAfter(g, target)
	}
	if parent == "" {
		m.sendf(proto.Command{Action: "panel.ungroup", IDs: []string{g.id}})
		m.status = "moved " + g.name + " out to the top level"
		return m
	}
	m.sendf(proto.Command{Action: "panel.group", IDs: []string{g.id}, Group: parent})
	m.status = "moved " + g.name + " into " + parent
	return m
}

// moveAfter reorders a panel to sit just after target within the fleet, the
// same coordinate space panel.move already works in.
func (m model) moveAfter(g grabState, target dashItem) model {
	if target.kind == itemPanel && target.panel.ID == g.id {
		m.status = g.name + " is already there"
		return m
	}
	index := -1
	n := 0
	for _, p := range m.fleet {
		if p.ID == g.id {
			continue // the moved panel is lifted out of the coordinate space first
		}
		n++
		if inTarget(target, p.ID) {
			index = n // just after the row it was dropped on
		}
	}
	if index < 0 {
		m.status = "nowhere to drop " + g.name
		return m
	}
	m.sendf(proto.Command{Action: "panel.move", IDs: []string{g.id}, Index: index})
	m.status = "moved " + g.name
	return m
}

// inTarget reports whether a panel id is the row a drop landed on — the panel
// itself, or the last panel of a group the drop landed after.
func inTarget(target dashItem, id string) bool {
	if target.kind == itemPanel {
		return target.panel.ID == id
	}
	if len(target.members) == 0 {
		return false
	}
	return target.members[len(target.members)-1].ID == id
}

// grabbedRow reports whether a row is the one being carried, so the renderer can
// mark it. The carried row stays in place until the drop — nothing is sent before
// then, so showing it anywhere else would be showing a move that has not happened.
func (m model) grabbedRow(it dashItem) bool {
	if !m.grabbing() {
		return false
	}
	switch it.kind {
	case itemGroup:
		return m.grab.kind == itemGroup && m.grab.path == it.name
	case itemPanel:
		return m.grab.kind == itemPanel && m.grab.id == it.panel.ID
	}
	return false
}

// grabHint is the footer line while a row is being carried, so the keys are on
// screen for a gesture nobody has used before.
func (m model) grabHint() string {
	if !m.grabbing() {
		return ""
	}
	return fmt.Sprintf("moving %s  ·  ↑↓ place  ·  enter drop  ·  esc cancel", truncate(m.grab.name, 24))
}
