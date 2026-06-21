package tui

import (
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// moveTarget computes the panel.move arguments that shift the unit at position
// sel one slot earlier (dir < 0) or later (dir > 0) within the fleet. units is
// the ordered list of cursor-addressable things — dashboard items or a group's
// members — where units[i] is the panel ids that unit covers, in fleet order. It
// returns the block of ids to move and the destination index among the panels
// that remain once the block is lifted out, plus ok. ok is false at the ends of
// the list (nothing to swap past) or when sel is out of range, so the caller can
// say "already first/last" rather than send a no-op.
//
// The destination is expressed relative to the neighbour being swapped past:
// moving earlier lands the block where the previous unit begins; moving later
// lands it just after the next unit ends. Working from the neighbour's own panels
// keeps the math correct whether units are single panels or whole groups, and
// regardless of how a group's members are interleaved in the fleet.
func moveTarget(fleet []panel.Panel, units [][]string, sel, dir int) (block []string, index int, ok bool) {
	if sel < 0 || sel >= len(units) || dir == 0 {
		return nil, 0, false
	}
	neighbor := sel + dir
	if neighbor < 0 || neighbor >= len(units) {
		return nil, 0, false // already at the corresponding end
	}
	block = units[sel]
	if len(block) == 0 {
		return nil, 0, false
	}

	inBlock := make(map[string]bool, len(block))
	for _, id := range block {
		inBlock[id] = true
	}
	// Index every panel that is not part of the moved block by its position in the
	// remaining (rest) list — the coordinate space panel.move's index lives in.
	restIndex := make(map[string]int, len(fleet))
	n := 0
	for _, p := range fleet {
		if inBlock[p.ID] {
			continue
		}
		restIndex[p.ID] = n
		n++
	}

	nb := units[neighbor]
	if len(nb) == 0 {
		return nil, 0, false
	}
	if dir < 0 {
		index, ok = restIndex[nb[0]] // land where the previous unit begins
	} else {
		index, ok = restIndex[nb[len(nb)-1]]
		index++ // land just past where the next unit ends
	}
	if !ok {
		return nil, 0, false
	}
	return block, index, true
}

// reorderEdgeStatus is the nudge shown when a reorder cannot go further.
func reorderEdgeStatus(dir int) string {
	if dir < 0 {
		return "already first"
	}
	return "already last"
}

// reorderSelection asks the server to move the unit at sel one slot in dir among
// units, naming it title in the status. It is the shared tail of the dashboard
// and group reorder actions: order is server-owned, so it sends panel.move and
// lets the broadcast refold the view, where the cursor/focus follows the moved
// item by identity.
func (m model) reorderSelection(units [][]string, sel, dir int, title string) model {
	block, index, ok := moveTarget(m.fleet, units, sel, dir)
	if !ok {
		m.status = reorderEdgeStatus(dir)
		return m
	}
	m.sendf(proto.Command{Action: "panel.move", IDs: block, Index: index})
	m.status = "moved " + title
	return m
}

// reorderDashItem moves the selected dashboard item one slot earlier (dir < 0) or
// later (dir > 0) among the dashboard's items.
func (m model) reorderDashItem(dir int) model {
	items := m.dashItems()
	if m.cursor < 0 || m.cursor >= len(items) {
		return m
	}
	units := make([][]string, len(items))
	for i, it := range items {
		units[i] = it.ids()
	}
	return m.reorderSelection(units, m.cursor, dir, items[m.cursor].title())
}

// reorderGroupMember moves the focused member one slot earlier (dir < 0) or later
// (dir > 0) within the group being split-viewed. It reorders by the group's fleet
// order — the order the tiles fill in — so the broadcast's reconcile keeps the
// focus on the same panel as the roster shifts.
func (m model) reorderGroupMember(dir int) model {
	p, ok := m.focusedMember()
	if !ok {
		return m
	}
	members := m.groupMembers()
	sel := indexOfMember(members, p.ID)
	if sel < 0 {
		return m
	}
	units := make([][]string, len(members))
	for i, mm := range members {
		units[i] = []string{mm.ID}
	}
	return m.reorderSelection(units, sel, dir, p.Title)
}
