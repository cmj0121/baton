package tui

import "testing"

// TestPinPersistAndAutoZoom proves pins survive leaving the split and that a lone
// pinned member auto-zooms when the group is reopened.
func TestPinPersistAndAutoZoom(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet() // api = panels 1,3,6

	// Pin two members: reopening the group shows them as tiles, no auto-zoom.
	m.pinned = map[string]bool{"1": true, "6": true}
	m = m.zoomGroup(m.dashItems()[0])
	if m.mode != modeGroupZoom {
		t.Fatalf("two pins should open the split, got mode=%v", m.mode)
	}
	if !m.groupPinned["1"] || !m.groupPinned["6"] {
		t.Fatalf("the split should reopen with the persisted pins shown, got %v", m.groupPinned)
	}

	// Now a single pin: reopening drops straight into that panel's zoom.
	m2 := baseModel()
	m2.fleet = groupedFleet()
	m2.pinned = map[string]bool{"3": true}
	m2 = m2.zoomGroup(m2.dashItems()[0])
	if m2.mode != modeZoom || m2.zoomID != "3" {
		t.Fatalf("a lone pin should auto-zoom into that panel, got mode=%v id=%q", m2.mode, m2.zoomID)
	}
	if m2.zoomGroupOrigin != "api" {
		t.Fatalf("the auto-zoom should remember the group for prefix-g, got %q", m2.zoomGroupOrigin)
	}
}

// TestTogglePinPersists checks a pin toggle mirrors into the persistent set so it
// outlives the view.
func TestTogglePinPersists(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0]) // api split, 3 members, focus on 1
	focus, _ := m.focusedMember()
	m = m.togglePin()
	if !m.pinned[focus.ID] {
		t.Fatalf("pinning should record %s in the persistent set, got %v", focus.ID, m.pinned)
	}
	m = m.togglePin()
	if m.pinned[focus.ID] {
		t.Fatalf("unpinning should clear %s from the persistent set, got %v", focus.ID, m.pinned)
	}
}
