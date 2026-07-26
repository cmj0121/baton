package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// runningGlobalShell is a one-panel fleet holding a live global shell.
func runningGlobalShell() []panel.Panel {
	return []panel.Panel{{ID: "g1", Kind: panel.Shell, GlobalShell: true, State: panel.Running, Title: "shell · g1"}}
}

// TestGlobalShellSpawn checks that H with no global shell in the fleet spawns one,
// flagged GlobalShell as a plain shell, and arms the spawn-then-zoom.
func TestGlobalShellSpawn(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = sampleFleet() // no global shell

	m = press(m, "H")
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.create" })
	if !got.GlobalShell {
		t.Fatalf("H should spawn a global shell, got %+v", got)
	}
	if got.Conductor || got.Kind == proto.KindAgent {
		t.Fatalf("the global shell must be a plain shell, not an agent/conductor: %+v", got)
	}
	if !m.pendingGlobalShell {
		t.Fatal("spawning the global shell should arm the pending zoom")
	}
}

// TestGlobalShellPendingZoomOnSnapshot checks the spawned global shell is zoomed
// the moment it arrives in a snapshot — it has no card to select, so H is the way in.
func TestGlobalShellPendingZoomOnSnapshot(t *testing.T) {
	m := baseModel()
	m.pendingGlobalShell = true
	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: []proto.Panel{
		{ID: "g1", Kind: "shell", State: "running", GlobalShell: true, Title: "shell · g1"},
	}})
	if m.mode != modeZoom || m.zoomID != "g1" {
		t.Fatalf("the global shell should auto-zoom on arrival, got mode=%v id=%q", m.mode, m.zoomID)
	}
	if m.pendingGlobalShell {
		t.Fatal("the pending-zoom flag should clear once consumed")
	}
}

// TestGlobalShellRespawn checks that H on an exited global shell re-runs it and
// zooms the restart live (not a read-only result view).
func TestGlobalShellRespawn(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{{ID: "g1", Kind: panel.Shell, GlobalShell: true, State: panel.Exited, Title: "shell · g1"}}

	m = press(m, "H")
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.respawn" })
	if got.ID != "g1" {
		t.Fatalf("H on an exited global shell should respawn it, got %q", got.ID)
	}
	if m.mode != modeZoom || m.zoomID != "g1" || m.zoomExited {
		t.Fatalf("re-running should zoom the global shell live, got mode=%v id=%q exited=%v", m.mode, m.zoomID, m.zoomExited)
	}
}

// TestGlobalShellRunningZooms checks that H on a running global shell zooms it,
// attaching and sending no create/respawn.
func TestGlobalShellRunningZooms(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = runningGlobalShell()

	m = press(m, "H")
	if m.mode != modeZoom || m.zoomID != "g1" {
		t.Fatalf("H on a running global shell should zoom it, got mode=%v id=%q", m.mode, m.zoomID)
	}
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.attach" })
	if got.ID != "g1" {
		t.Fatalf("zoom should attach the global shell g1, got %q", got.ID)
	}
}

// TestGlobalShellHiddenFromDashboard checks the global shell is off the dashboard
// roster and its counts, surfacing only as the FLEET-heading mark — beside the
// conductor's, never as a card.
func TestGlobalShellHiddenFromDashboard(t *testing.T) {
	m := baseModel()
	m.fleet = append(sampleFleet(), panel.Panel{ID: "g1", Kind: panel.Shell, GlobalShell: true, State: panel.Running, Title: "shell · g1"})

	for _, it := range m.dashItems() {
		if it.kind == itemPanel && it.panel.GlobalShell {
			t.Fatal("the global shell must not appear as a dashboard card")
		}
	}
	if got := len(m.visibleFleet()); got != len(sampleFleet()) {
		t.Fatalf("visibleFleet should drop the global shell: %d, want %d", got, len(sampleFleet()))
	}
	if m.globalShellMark() == "" {
		t.Fatal("a running global shell should show a FLEET-heading mark")
	}
}
