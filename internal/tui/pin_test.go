package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// pinFleet returns the grouped fleet (api = panels 1,3,6) with the named ids
// pinned, mirroring what the server-owned Pinned flag delivers in a snapshot.
func pinFleet(pinned ...string) []panel.Panel {
	want := map[string]bool{}
	for _, id := range pinned {
		want[id] = true
	}
	fleet := groupedFleet()
	for i := range fleet {
		fleet[i].Pinned = want[fleet[i].ID]
	}
	return fleet
}

// TestPinPersistAndAutoZoom proves the split reopens with the server's pins shown
// and that a lone pinned member auto-zooms when the group is entered.
func TestPinPersistAndAutoZoom(t *testing.T) {
	m := baseModel()
	m.fleet = pinFleet("1", "6") // two pins: the split opens, no auto-zoom

	m = m.zoomGroup(m.dashItems()[0])
	if m.mode != modeGroupZoom {
		t.Fatalf("two pins should open the split, got mode=%v", m.mode)
	}
	if !m.groupPinned["1"] || !m.groupPinned["6"] {
		t.Fatalf("the split should reopen with the server's pins shown, got %v", m.groupPinned)
	}

	// Now a single pin: entering drops straight into that panel's zoom.
	m2 := baseModel()
	m2.fleet = pinFleet("3")
	m2 = m2.zoomGroup(m2.dashItems()[0])
	if m2.mode != modeZoom || m2.zoomID != "3" {
		t.Fatalf("a lone pin should auto-zoom into that panel, got mode=%v id=%q", m2.mode, m2.zoomID)
	}
	if m2.zoomGroupOrigin != "api" {
		t.Fatalf("the auto-zoom should remember the group for prefix-g, got %q", m2.zoomGroupOrigin)
	}
}

// TestTogglePinSendsCommand checks a pin toggle is sent on to the server (which
// owns the flag) and is reflected optimistically in the local set.
func TestTogglePinSendsCommand(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0]) // api split, 3 members, focus on 1
	focus, _ := m.focusedMember()

	m = m.togglePin()
	if !m.groupPinned[focus.ID] {
		t.Fatalf("pinning should set %s optimistically, got %v", focus.ID, m.groupPinned)
	}
	pin := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.pin" })
	if len(pin.IDs) != 1 || pin.IDs[0] != focus.ID {
		t.Fatalf("pin command should target %s, got %+v", focus.ID, pin)
	}

	m = m.togglePin()
	if m.groupPinned[focus.ID] {
		t.Fatalf("unpinning should clear %s optimistically, got %v", focus.ID, m.groupPinned)
	}
	unpin := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.unpin" })
	if len(unpin.IDs) != 1 || unpin.IDs[0] != focus.ID {
		t.Fatalf("unpin command should target %s, got %+v", focus.ID, unpin)
	}
}
