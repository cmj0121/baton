package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// favInFleet reports whether the panel with id in the fleet carries the Favourite
// flag, so a test can assert the optimistic local update.
func favInFleet(fleet []panel.Panel, id string) bool {
	for _, p := range fleet {
		if p.ID == id {
			return p.Favourite
		}
	}
	return false
}

// TestToggleFavouritePanelSendsCommand checks that starring a lone panel sends
// panel.favourite (and un-starring sends panel.unfavourite) and updates the local
// flag optimistically so the sort reflows before the snapshot lands.
func TestFavPanelSendsCmd(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = groupedFleet()
	m.cursor = 1 // the lone shell, panel id "2"
	if it, _ := m.selectedItem(); it.kind != itemPanel || it.panel.ID != "2" {
		t.Fatalf("setup: cursor should rest on the lone shell 2, got %+v", it)
	}

	m = m.toggleFavourite()
	if !favInFleet(m.fleet, "2") {
		t.Fatalf("favouriting should set panel 2 optimistically, got fleet %+v", m.fleet)
	}
	fav := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.favourite" })
	if fav.ID != "2" {
		t.Fatalf("favourite command should target panel 2, got %+v", fav)
	}
	// The cursor must follow panel 2 to its new favourites-first position (index 0),
	// so the highlight stays on the same card with no one-frame flicker.
	if m.cursor != 0 {
		t.Fatalf("cursor should follow panel 2 to the front (index 0), got %d", m.cursor)
	}
	if it, _ := m.selectedItem(); it.kind != itemPanel || it.panel.ID != "2" {
		t.Fatalf("selection should still be panel 2 after the reflow, got %+v", it)
	}

	m = m.toggleFavourite()
	if favInFleet(m.fleet, "2") {
		t.Fatalf("unfavouriting should clear panel 2 optimistically, got fleet %+v", m.fleet)
	}
	unfav := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.unfavourite" })
	if unfav.ID != "2" {
		t.Fatalf("unfavourite command should target panel 2, got %+v", unfav)
	}
	// The cursor follows panel 2 back to its home position (index 1) too.
	if it, _ := m.selectedItem(); it.kind != itemPanel || it.panel.ID != "2" {
		t.Fatalf("selection should still be panel 2 after un-favouriting, got %+v", it)
	}
}

// TestToggleFavouriteGroupSendsCommand checks that starring a group sends
// group.favourite (and un-starring sends group.unfavourite) and updates the local
// favGroups set optimistically.
func TestFavGroupSendsCmd(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = groupedFleet()
	m.cursor = 2 // the db group, third card — it will move to the front once favourited
	if it, _ := m.selectedItem(); it.kind != itemGroup || it.name != "db" {
		t.Fatalf("setup: cursor should rest on the db group, got %+v", it)
	}

	m = m.toggleFavourite()
	if !m.favGroups["db"] {
		t.Fatalf("favouriting should set group db optimistically, got %v", m.favGroups)
	}
	fav := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "group.favourite" })
	if fav.Group != "db" {
		t.Fatalf("favourite command should target group db, got %+v", fav)
	}
	// The cursor must follow the db group as it reflows from index 2 to the front
	// (index 0), so the highlight stays on the same card with no one-frame flicker.
	if m.cursor != 0 {
		t.Fatalf("cursor should follow the db group to the front (index 0), got %d", m.cursor)
	}
	if it, _ := m.selectedItem(); it.kind != itemGroup || it.name != "db" {
		t.Fatalf("selection should still be the db group after the reflow, got %+v", it)
	}

	m = m.toggleFavourite()
	if m.favGroups["db"] {
		t.Fatalf("unfavouriting should clear group db optimistically, got %v", m.favGroups)
	}
	unfav := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "group.unfavourite" })
	if unfav.Group != "db" {
		t.Fatalf("unfavourite command should target group db, got %+v", unfav)
	}
	// The cursor follows db back to its home position (index 2) too.
	if it, _ := m.selectedItem(); it.kind != itemGroup || it.name != "db" {
		t.Fatalf("selection should still be the db group after un-favouriting, got %+v", it)
	}
}

// TestDashItemsSortsFavouritesFirst checks favourited items float to the front of
// the dashboard while the relative order within each partition is preserved (a
// stable sort), so both the grid and the tree view show favourites first.
func TestDashItemsSortsFavouritesFirst(t *testing.T) {
	m := baseModel()
	fleet := groupedFleet()
	for i := range fleet {
		if fleet[i].ID == "5" { // the lone panel "lone two"
			fleet[i].Favourite = true
		}
	}
	m.fleet = fleet
	m.favGroups = map[string]bool{"db": true}

	items := m.dashItems()
	// Original order is [api(g), shell2(p), db(g), lone5(p)]; the favourites are the
	// db group and lone panel 5, floated to the front in their original relative order.
	want := []string{"db", "5", "api", "2"}
	got := make([]string, len(items))
	for i, it := range items {
		if it.kind == itemGroup {
			got[i] = it.name
		} else {
			got[i] = it.panel.ID
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("favourites should float to the front stably: want %v, got %v", want, got)
	}
}

// TestFavouriteCardRendersMarker checks the ⊙ marker shows on a favourited lone
// panel card and a favourited group card, and is absent otherwise.
func TestFavouriteCardRendersMarker(t *testing.T) {
	m := baseModel()

	plain := m.renderCard(panel.Panel{ID: "1", Kind: panel.Agent, Title: "worker", State: panel.Running}, false)
	if strings.Contains(plain, "⊙") {
		t.Fatalf("a non-favourite panel card should not show the ⊙ marker")
	}
	starred := m.renderCard(panel.Panel{ID: "1", Kind: panel.Agent, Title: "worker", State: panel.Running, Favourite: true}, false)
	if !strings.Contains(starred, "⊙") {
		t.Fatalf("a favourited panel card should show the ⊙ marker")
	}

	gog := dashItem{kind: itemGroup, name: "api", members: groupedFleet()}
	if strings.Contains(m.renderGroupCard(gog, false), "⊙") {
		t.Fatalf("a non-favourite group card should not show the ⊙ marker")
	}
	m.favGroups = map[string]bool{"api": true}
	if !strings.Contains(m.renderGroupCard(gog, false), "⊙") {
		t.Fatalf("a favourited group card should show the ⊙ marker")
	}
}

// TestFavouriteGroupCardHeightMatchesPanel guards the dashboard invariant that a
// favourited group-of-group card — whose ⊙ prefix and sub-group count both share
// the head — is still exactly the same size as a plain panel card.
func TestFavouriteGroupCardHeightMatchesPanel(t *testing.T) {
	m := baseModel()
	panelCard := m.renderCard(panel.Panel{ID: "9", Kind: panel.Agent, Title: "worker", State: panel.Running, Task: "do the thing"}, false)
	wantH, wantW := lipgloss.Height(panelCard), lipgloss.Width(panelCard)

	gog := dashItem{kind: itemGroup, name: "backend", members: []panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "backend-with-a-fairly-long-name", State: panel.Running, Group: "backend"},
		{ID: "2", Kind: panel.Shell, Title: "s", State: panel.Idle, Group: "backend"},
		{ID: "3", Kind: panel.Agent, Title: "a", State: panel.Running, Group: "backend/api"},
		{ID: "4", Kind: panel.Agent, Title: "b", State: panel.Idle, Group: "backend/db"},
	}}
	m.favGroups = map[string]bool{"backend": true}
	for _, sel := range []bool{false, true} {
		for _, marking := range []bool{false, true} {
			if marking {
				m.marked = map[string]bool{"zzz": true} // force the select-mark column on
			} else {
				m.marked = nil
			}
			card := m.renderGroupCard(gog, sel)
			if h, w := lipgloss.Height(card), lipgloss.Width(card); h != wantH || w != wantW {
				t.Fatalf("favourited group-of-group card sel=%v marking=%v is %dx%d, want %dx%d (a panel card)", sel, marking, w, h, wantW, wantH)
			}
		}
	}
	// A favourited lone-panel card keeps the panel-card size too.
	fp := m.renderCard(panel.Panel{ID: "9", Kind: panel.Agent, Title: "worker", State: panel.Running, Task: "do the thing", Favourite: true}, false)
	if h, w := lipgloss.Height(fp), lipgloss.Width(fp); h != wantH || w != wantW {
		t.Fatalf("favourited panel card is %dx%d, want %dx%d", w, h, wantW, wantH)
	}
}
