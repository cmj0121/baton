package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/panel"
)

// filterFleet is a mixed fleet: two lone panels and a two-member group.
func filterFleet() []panel.Panel {
	return []panel.Panel{
		{ID: "1", Title: "api server"},
		{ID: "2", Title: "web ui"},
		{ID: "3", Title: "db migrate", Group: "infra"},
		{ID: "4", Title: "redis", Group: "infra"},
	}
}

// TestFilterNarrows proves the filter keeps only items whose panel title, group
// name, or any member title matches — case-insensitively.
func TestFilterNarrows(t *testing.T) {
	m := model{mode: modeDashboard, fleet: filterFleet()}

	m.filter = "API"
	if items := m.dashItems(); len(items) != 1 || items[0].title() != "api server" {
		t.Fatalf("case-insensitive title match failed, got %v", titles(m.dashItems()))
	}

	// The group name matches.
	m.filter = "infra"
	if items := m.dashItems(); len(items) != 1 || items[0].kind != itemGroup {
		t.Fatalf("group-name match should surface the group, got %v", titles(m.dashItems()))
	}

	// A member title surfaces the whole group.
	m.filter = "redis"
	items := m.dashItems()
	if len(items) != 1 || items[0].kind != itemGroup || len(items[0].members) != 2 {
		t.Fatalf("member match should surface the whole group intact, got %v", titles(items))
	}

	// No match yields an empty list (the dashboard renders the empty-state note).
	m.filter = "nonesuch"
	if items := m.dashItems(); len(items) != 0 {
		t.Fatalf("a non-matching filter should hide everything, got %v", titles(items))
	}

	// An empty filter shows the whole fleet.
	m.filter = ""
	if items := m.dashItems(); len(items) != 3 {
		t.Fatalf("an empty filter should show every item, got %v", titles(items))
	}
}

// TestFilterLiveTyping proves the overlay narrows the dashboard as you type and
// esc clears it back to the whole fleet.
func TestFilterLiveTyping(t *testing.T) {
	m := model{mode: modeDashboard, fleet: filterFleet()}
	m = m.openFilter()
	if m.input != inputFilter {
		t.Fatal("openFilter should arm the filter overlay")
	}

	type_ := func(s string) {
		next, _ := m.handleInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
		m = next.(model)
	}
	type_("w")
	if m.filter != "w" || len(m.dashItems()) != 1 {
		t.Fatalf("typing should filter live, filter=%q items=%v", m.filter, titles(m.dashItems()))
	}

	// esc clears the filter and closes the overlay.
	next, _ := m.handleInput(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.filter != "" || m.input != inputNone {
		t.Fatalf("esc should clear the filter and close the overlay, filter=%q input=%v", m.filter, m.input)
	}
	if len(m.dashItems()) != 3 {
		t.Fatalf("clearing the filter should restore the fleet, got %v", titles(m.dashItems()))
	}
}

// TestFilterCommitKeeps proves enter applies the filter and keeps it after the
// overlay closes, so the narrowed dashboard stays put.
func TestFilterCommitKeeps(t *testing.T) {
	m := model{mode: modeDashboard, fleet: filterFleet()}
	m = m.openFilter()
	m.inputBuf = "ui"
	next, _ := m.handleInput(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.input != inputNone {
		t.Fatal("enter should close the overlay")
	}
	if m.filter != "ui" || len(m.dashItems()) != 1 {
		t.Fatalf("enter should keep the applied filter, filter=%q items=%v", m.filter, titles(m.dashItems()))
	}
}

// titles is a small helper for failure messages.
func titles(items []dashItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.title()
	}
	return out
}
