package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// runningConductor is a one-panel fleet holding a live conductor.
func runningConductor() []panel.Panel {
	return []panel.Panel{{ID: "c1", Kind: panel.Agent, Conductor: true, State: panel.Running, Title: "conductor · c1"}}
}

// TestCondSpawn checks that C with no conductor in the fleet spawns one, flagged
// Conductor, and arms the spawn-then-zoom so it opens when it lands.
func TestCondSpawn(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = sampleFleet() // no conductor

	m = press(m, "C")
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.create" })
	if !got.Conductor {
		t.Fatalf("C should spawn a conductor panel, got %+v", got)
	}
	if !m.pendingConductor {
		t.Fatal("spawning the conductor should arm the pending zoom")
	}
}

// TestCondPendingZoomOnSnapshot checks the spawned conductor is zoomed the moment it
// arrives in a snapshot — it has no card to select, so C is the only way in.
func TestCondPendingZoomOnSnapshot(t *testing.T) {
	m := baseModel()
	m.pendingConductor = true
	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: []proto.Panel{
		{ID: "c1", Kind: "agent", State: "running", Conductor: true, Title: "conductor · c1"},
	}})
	if m.mode != modeZoom || m.zoomID != "c1" {
		t.Fatalf("the conductor should auto-zoom on arrival, got mode=%v id=%q", m.mode, m.zoomID)
	}
	if m.pendingConductor {
		t.Fatal("the pending-zoom flag should clear once consumed")
	}
}

// TestCondRespawn checks that C on an exited conductor re-runs it and zooms the
// restart as a live panel (not a read-only result view).
func TestCondRespawn(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{{ID: "c1", Kind: panel.Agent, Conductor: true, State: panel.Exited, Title: "conductor · c1"}}

	m = press(m, "C")
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.respawn" })
	if got.ID != "c1" {
		t.Fatalf("C on an exited conductor should respawn it, got %q", got.ID)
	}
	if m.mode != modeZoom || m.zoomID != "c1" || m.zoomExited {
		t.Fatalf("re-running should zoom the conductor live, got mode=%v id=%q exited=%v", m.mode, m.zoomID, m.zoomExited)
	}
}

// TestCondRunningZooms checks that C on a running conductor zooms it (to watch its
// work) — attaching, and sending no create/close, since restart is gone.
func TestCondRunningZooms(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = runningConductor()

	m = press(m, "C")
	if m.mode != modeZoom || m.zoomID != "c1" {
		t.Fatalf("C on a running conductor should zoom it, got mode=%v id=%q", m.mode, m.zoomID)
	}
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.attach" })
	if got.ID != "c1" {
		t.Fatalf("zoom should attach the conductor c1, got %q", got.ID)
	}
}

// TestConductorHiddenFromDashboard checks the conductor is off the dashboard roster
// and its counts, surfacing only as the FLEET-heading mark.
func TestConductorHiddenFromDashboard(t *testing.T) {
	m := baseModel()
	m.fleet = append(sampleFleet(), panel.Panel{ID: "c1", Kind: panel.Agent, Conductor: true, State: panel.Running, Title: "conductor · c1"})

	for _, it := range m.dashItems() {
		if it.kind == itemPanel && it.panel.Conductor {
			t.Fatal("the conductor must not appear as a dashboard card")
		}
	}
	if got := len(m.visibleFleet()); got != len(sampleFleet()) {
		t.Fatalf("visibleFleet should drop the conductor: %d, want %d", got, len(sampleFleet()))
	}
	if m.conductorMark() == "" {
		t.Fatal("a running conductor should show a FLEET-heading mark")
	}
}
