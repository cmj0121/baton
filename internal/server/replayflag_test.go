package server

import (
	"path/filepath"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// wireReplay is the replay flag the snapshot carries for one panel.
func wireReplay(t *testing.T, s *Server, id string) bool {
	t.Helper()
	msg := s.panelsMsg()
	for _, p := range msg.Panels {
		if p.ID == id {
			return p.Replay
		}
	}
	t.Fatalf("panel %q is not in the snapshot", id)
	return false
}

// TestRestoredPanelReportsNoReplay is the fact the cockpit could not work out for
// itself, and the reason it now travels: a panel the daemon rebuilt from its
// persisted fleet and a panel that died under this daemon both report State
// "exited", and only one of them has a screen to show.
//
// The same panel is asked on both sides of a real daemon restart, so the two
// answers cannot be two differently-built fixtures agreeing with themselves.
//
// The mutation that kills this: report Replay from anything other than the pty
// manager — a State check, a non-empty Activity, a non-zero Since — and the
// restored slot claims a terminal it does not have, which is the black screen the
// cockpit's guard exists to refuse.
func TestRestoredPanelReportsNoReplay(t *testing.T) {
	stateF := filepath.Join(t.TempDir(), "state.json")
	first, dir := identityServer(t, WithStateFile(stateF))

	id, err := first.createPanel(originOperator, proto.KindShell, "", nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if !wireReplay(t, first, id) {
		t.Fatal("a live panel has a terminal behind it and must say so")
	}

	s := restartDaemon(t, first, stateF)

	if wireReplay(t, s, id) {
		t.Error("a restored dead slot claimed a terminal record the daemon does not hold")
	}
}

// TestAnExitedPanelKeepsItsReplay: the other half, and the one a guard keyed on
// the lifecycle state would have got wrong. The ring outlives the process — it is
// removed only when the panel is closed or purged — because a dead panel's last
// screen is the whole reason the fleet holds a dead slot at all.
func TestAnExitedPanelKeepsItsReplay(t *testing.T) {
	s, dir := identityServer(t)

	id, err := s.createPanel(originOperator, proto.KindCommand, "/bin/sh", []string{"-c", "exit 0"}, dir, "", false, false)
	if err != nil {
		t.Fatalf("create command: %v", err)
	}
	waitState(t, s, id, panel.Exited)

	if !wireReplay(t, s, id) {
		t.Error("an exited panel's last screen is still there, and the snapshot said otherwise")
	}
}
