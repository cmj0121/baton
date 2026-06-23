package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
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

// TestRespawnGroupExited checks that r on a focused group card
// re-runs every exited member and leaves the live one alone.
func TestRespawnGroupExited(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{
		{ID: "d1", Kind: panel.Agent, Title: "db · a", State: panel.Exited, Group: "db"},
		{ID: "d2", Kind: panel.Agent, Title: "db · b", State: panel.Running, Group: "db"},
		{ID: "d3", Kind: panel.Agent, Title: "db · c", State: panel.Exited, Group: "db"},
	}
	m.cursor = 0 // the whole fleet folds into the one "db" group card

	m = press(m, "r")

	// Exactly the two exited members are respawned; the running one is not.
	got := map[string]bool{}
	got[waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.respawn" }).ID] = true
	got[waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.respawn" }).ID] = true
	if !got["d1"] || !got["d3"] {
		t.Fatalf("group respawn should target d1 and d3, got %v", got)
	}
	if got["d2"] {
		t.Fatal("the running member d2 should not be respawned")
	}
}

// TestRespawnGroupNoneExited checks that r on a group with no exited members sends
// nothing and reports it by name.
func TestRespawnGroupNoneExited(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{
		{ID: "a1", Kind: panel.Agent, Title: "api · a", State: panel.Running, Group: "api"},
		{ID: "a2", Kind: panel.Agent, Title: "api · b", State: panel.Idle, Group: "api"},
	}
	m.cursor = 0

	m = press(m, "r")
	if m.status != "no exited panel in api" {
		t.Fatalf("expected a no-exited hint naming the group, got %q", m.status)
	}
	select {
	case got := <-cmds:
		t.Fatalf("an all-live group should respawn nothing, but sent %+v", got)
	default:
	}
}
