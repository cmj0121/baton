package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// TestReloadAction checks the reload binding tells the daemon to
// re-read its config (the fleet keeps running) and refreshes the cockpit's own
// prefs in place — no detach, no restart.
func TestReloadAction(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = sampleFleet()
	m.mode = modeDashboard

	// Bare R on the dashboard fires the reload action.
	m = press(m, "R")

	if m.mode != modeDashboard {
		t.Fatalf("reload should not change the view, got mode=%v", m.mode)
	}
	if m.quitting {
		t.Fatal("reload must not quit the cockpit")
	}
	waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "server.reload" })
}
