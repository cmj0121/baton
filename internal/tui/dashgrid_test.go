package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// loosePanels is n ungrouped running panels — n top-level rows, whatever else the
// dashboard is doing.
func loosePanels(n int) []panel.Panel {
	out := make([]panel.Panel, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, panel.Panel{
			ID:    string(rune('a' + i)),
			Kind:  panel.Shell,
			Title: "shell #" + string(rune('1'+i)),
			State: panel.Running,
		})
	}
	return out
}

// TestDashboardPicksCardsOrTree: the layout is chosen off the TOP LEVEL of the
// tree, so a work item counts once however many panels it holds — and expanding a
// row can never flip the layout out from under the keystroke that expanded it.
func TestDashboardPicksCardsOrTree(t *testing.T) {
	grouped := func(n int) []panel.Panel {
		out := loosePanels(n)
		for i := range out {
			out[i].Group = "backend"
		}
		return out
	}

	for _, tc := range []struct {
		name  string
		fleet []panel.Panel
		width int
		lens  lens
		filt  string
		grid  bool
	}{
		{name: "two panels", fleet: loosePanels(2), width: 100, grid: true},
		{name: "five panels", fleet: loosePanels(5), width: 100, grid: true},
		{name: "six panels", fleet: loosePanels(6), width: 100, grid: false},
		{name: "a large fleet", fleet: sampleFleet(), width: 100, grid: false},
		{name: "five panels in one work item", fleet: grouped(5), width: 100, grid: true},
		{name: "a terminal too narrow for a card", fleet: loosePanels(2), width: 30, grid: false},
		{name: "a filter is showing", fleet: loosePanels(2), width: 100, filt: "shell", grid: false},
		{name: "a lens is on", fleet: loosePanels(2), width: 100, lens: lensState, grid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := model{mode: modeDashboard, fleet: tc.fleet, width: tc.width, height: 40, lens: tc.lens, filter: tc.filt}
			if got := m.gridDash(); got != tc.grid {
				t.Fatalf("gridDash() = %v, want %v", got, tc.grid)
			}
			if got := m.treeView(); got == tc.grid {
				t.Fatalf("treeView() = %v alongside gridDash() = %v", got, tc.grid)
			}
			if cols := m.cols(); (cols > 1) != tc.grid {
				t.Fatalf("cols() = %d in grid=%v", cols, tc.grid)
			}
			if m.View() == "" {
				t.Fatal("the dashboard should render")
			}
		})
	}
}

// TestGridAddressesTheTopLevelOnly: the cursor addresses what the grid DRAWS. A
// nested row the cards cannot show is a row the cursor cannot land on, so no verb
// can act on something invisible.
func TestGridAddressesTheTopLevelOnly(t *testing.T) {
	fleet := []panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "api · a", State: panel.Running, Group: "backend"},
		{ID: "2", Kind: panel.Agent, Title: "api · b", State: panel.Running, Group: "backend/api"},
		{ID: "3", Kind: panel.Shell, Title: "lone", State: panel.Running},
	}
	m := model{mode: modeDashboard, fleet: fleet, width: 100, height: 40}
	if !m.gridDash() {
		t.Fatalf("two top-level rows should be the card grid")
	}
	items := m.dashItems()
	if len(items) != 2 {
		t.Fatalf("the grid addresses %d rows, want the 2 top-level ones", len(items))
	}
	for _, it := range items {
		if it.depth != 0 {
			t.Fatalf("row %q sits at depth %d", it.title(), it.depth)
		}
	}
	// The group card still stands for its WHOLE subtree, open or shut — the
	// invariant every bulk verb rests on.
	if n := len(items[0].members); n != 2 {
		t.Fatalf("the work item card holds %d panels, want 2", n)
	}
}

// TestGridCursorReachesEveryCard: j/k walk the cards in reading order and the
// arrows walk whole rows, so the last card of a short final row is reachable.
func TestGridCursorReachesEveryCard(t *testing.T) {
	m := model{mode: modeDashboard, fleet: loosePanels(5), width: 100, height: 40}
	if cols := m.cols(); cols != 3 {
		t.Fatalf("a 100-column terminal fits %d cards a row, want 3", cols)
	}
	for i := 0; i < 4; i++ {
		m.move(m.step("j"))
	}
	if m.cursor != 4 {
		t.Fatalf("j four times lands on %d, want the last card (4)", m.cursor)
	}
	m.move(-m.step("up"))
	if m.cursor != 1 {
		t.Fatalf("↑ from the last card lands on %d, want 1 — one grid row up", m.cursor)
	}
}

// TestCardsCarryTheRowMarks: the cards draw the same marks the tree row does. The
// ⇅ is the one that matters here — it was added while the dashboard had no cards
// at all, so it is exactly the decoration a second, independent renderer would
// have quietly lacked.
func TestCardsCarryTheRowMarks(t *testing.T) {
	m := model{mode: modeDashboard, fleet: loosePanels(3), width: 100, height: 40}
	m = m.startGrab()
	if !m.grabbing() {
		t.Fatalf("the row should be carried: %s", m.status)
	}
	if got := stripANSI(m.View()); !strings.Contains(got, "⇅") {
		t.Fatal("a carried card is not marked where it sits")
	}

	m.fleet[0].Favourite = true
	if got := stripANSI(m.View()); !strings.Contains(got, "⊙") {
		t.Fatal("a favourited card lost its mark")
	}
}

// TestOpeningAWorkItemFromTheCardsShowsTheTree: the grid draws a work item whole,
// so "open this" can only be answered by the layout that draws what is inside —
// and ← out of the top level puts the cards back. Without the pair, the layout a
// person meets while building their first work items is the one layout that cannot
// show nesting.
func TestOpeningAWorkItemFromTheCardsShowsTheTree(t *testing.T) {
	fleet := []panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "api · a", State: panel.Running, Group: "backend"},
		{ID: "2", Kind: panel.Agent, Title: "api · b", State: panel.Running, Group: "backend/api"},
		{ID: "3", Kind: panel.Shell, Title: "lone", State: panel.Running},
	}
	m := model{mode: modeDashboard, fleet: fleet, width: 120, height: 40, binds: append([]binding(nil), bindings...)}
	if !m.gridDash() {
		t.Fatal("two top-level rows are cards")
	}

	m.cursor = 0 // the backend card
	m = m.expandSelected()
	if !m.treeView() {
		t.Fatalf("→ on a work item card should show the tree: %s", m.status)
	}
	if it, ok := m.selectedItem(); !ok || it.kind != itemGroup || it.name != "backend" {
		t.Fatalf("the cursor should stay on the work item it opened, got %+v", it)
	}
	if len(m.dashItems()) <= 2 {
		t.Fatal("the tree should now show the rows nested under backend")
	}

	// ← shuts the work item first (the tree's own meaning), and only then walks out
	// of the tree itself.
	m = m.collapseSelected()
	if !m.treeView() {
		t.Fatal("the first ← shuts the work item, it does not leave the tree")
	}
	m = m.collapseSelected()
	if !m.gridDash() {
		t.Fatalf("← at the top level should put the cards back: %s", m.status)
	}
	if it, ok := m.selectedItem(); !ok || it.name != "backend" {
		t.Fatalf("walking out should keep the cursor on the work item, got %+v", it)
	}
}

// TestTheHeadingCountsTheFleetsGroups: the FLEET line summarises the fleet, not
// the rows on screen. The cards draw a work item whole and a collapsed row hides
// its sub-groups, so counting rows made the dashboard's own heading disagree with
// its own tree depending on the layout and on what was open.
func TestTheHeadingCountsTheFleetsGroups(t *testing.T) {
	fleet := []panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "a", State: panel.Running, Group: "backend"},
		{ID: "2", Kind: panel.Agent, Title: "b", State: panel.Running, Group: "backend/api"},
	}
	if got := groupCount(fleet); got != 2 {
		t.Fatalf("backend and backend/api are 2 work items, got %d", got)
	}

	m := model{mode: modeDashboard, fleet: fleet, width: 120, height: 40}
	grid := stripANSI(m.View())
	m.showTree = true
	tree := stripANSI(m.View())
	for name, view := range map[string]string{"cards": grid, "tree": tree} {
		if !strings.Contains(view, "2 group") {
			t.Fatalf("the %s heading should count both work items:\n%s", name, view)
		}
	}
}
