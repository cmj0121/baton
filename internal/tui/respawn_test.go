package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// TestRespawnExitedPanel checks that r on a focused exited (dead-slot) panel sends
// a panel.respawn for it.
func TestRespawnExitedPanel(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = sampleFleet()

	// a5 (the exited panel) is the last dashboard item.
	items := m.dashItems()
	m.cursor = len(items) - 1
	want := items[m.cursor]

	m = press(m, "r")
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.respawn" })
	if got.ID != want.panel.ID {
		t.Fatalf("respawn should target the focused exited panel %s, got %q", want.panel.ID, got.ID)
	}
}

// TestRespawnRefusesLivePanel checks that r on a live panel sends nothing and only
// sets a status hint.
func TestRespawnRefusesLivePanel(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = sampleFleet()
	m.cursor = 0 // a1, attention (live)

	m = press(m, "r")
	if m.status != "panel is still running" {
		t.Fatalf("expected a still-running hint, got %q", m.status)
	}
	select {
	case got := <-cmds:
		t.Fatalf("a live panel should not be respawned, but sent %+v", got)
	default:
	}
}
