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

// TestCondSpawn checks that C with no conductor in the fleet
// spawns one, flagged Conductor.
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
}

// TestCondRespawn checks that C on an exited conductor re-runs it.
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
}

// TestCondRestart checks that C on a running conductor confirms first,
// then restarts it: close the running one, spawn a fresh one (so it re-reads its
// brief).
func TestCondRestart(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = runningConductor()

	// C arms a restart but sends nothing until confirmed.
	m = press(m, "C")
	if m.pendingConductor != "c1" {
		t.Fatalf("C on a running conductor should arm a restart, got pending %q", m.pendingConductor)
	}
	select {
	case got := <-cmds:
		t.Fatalf("restart must wait for confirmation, but sent %+v", got)
	default:
	}

	// y confirms: close the running conductor, then spawn a fresh one.
	m = press(m, "y")
	if got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.close" }); got.ID != "c1" {
		t.Fatalf("restart should close the running conductor c1, got %q", got.ID)
	}
	if got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.create" }); !got.Conductor {
		t.Fatalf("restart should spawn a fresh conductor, got %+v", got)
	}
}

// TestCondRestartCancel checks that any non-yes answer cancels the restart
// and sends nothing.
func TestCondRestartCancel(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = runningConductor()

	m = press(m, "C")
	m = press(m, "n")
	if m.pendingConductor != "" {
		t.Fatalf("a cancelled restart should clear the pending state, got %q", m.pendingConductor)
	}
	select {
	case got := <-cmds:
		t.Fatalf("a cancelled restart should send nothing, but sent %+v", got)
	default:
	}
}
