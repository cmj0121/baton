package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// parkingShim writes an executable with the given basename that parks, so two
// profiles can differ by the command they resolve to and nothing else.
func parkingShim(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil { //nolint:gosec // a test shim must be executable
		t.Fatal(err)
	}
	return path
}

// TestRelaunchTakesTheCommandLineTheConfigNowNames is the verb's whole reason to
// exist. A panel is spawned from a profile, the profile is re-pointed (the
// SIGHUP an operator does after editing panel.agents), and the dead slot is
// brought back.
//
// The contrast is the assertion: `r` on that same slot replays the old command
// line — that is what `r` is FOR — so a test that only checked the new one would
// pass against a relaunch that was a spelling of respawn. Both are asked here,
// in that order, on the same panel.
func TestRelaunchTakesTheCommandLineTheConfigNowNames(t *testing.T) {
	s, dir := identityServer(t)
	before, after := parkingShim(t, "claude"), parkingShim(t, "claude")

	s.SetAgents([]proto.AgentBackend{{Name: "worker", Command: before, Args: []string{"--old"}}})
	id, err := s.createPanel(originOperator, proto.KindAgent, before, []string{"--old"}, dir, "worker", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	s.stopAndWaitExited(t, id)

	// r first: the frozen spec, replayed. This is the behaviour relaunch must not
	// quietly become.
	s.SetAgents([]proto.AgentBackend{{Name: "worker", Command: after, Args: []string{"--new"}}})
	if err := s.respawnPanel(id); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	if got := s.specOf(id); got.Command != before || strings.Join(got.Args, " ") != "--old" {
		t.Fatalf("r should replay the launched command line, got %s %v", got.Command, got.Args)
	}
	s.stopAndWaitExited(t, id)

	if err := s.relaunchPanel(id); err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	got := s.specOf(id)
	if got.Command != after || strings.Join(got.Args, " ") != "--new" {
		t.Errorf("n r should take the config in force, got %s %v", got.Command, got.Args)
	}
	// Stored back, so the NEXT plain r replays what was actually launched rather
	// than undoing the re-launch.
	if got.Dir != dir {
		t.Errorf("the panel's directory is its own and must not move, got %q want %q", got.Dir, dir)
	}
	if i := s.indexLocked(id); i < 0 {
		t.Error("a re-launch must keep the slot, not make a new one")
	}
}

// TestRelaunchKeepsEverythingHangingOffTheID: a re-launch re-resolves the SPEC,
// not the panel's identity. The work item, the pin and the favourite are what an
// operator would silently lose if this were purge-and-spawn — along with every
// reference held by ctl, the MCP tools and their own memory of "panel 63".
func TestRelaunchKeepsEverythingHangingOffTheID(t *testing.T) {
	s, dir := identityServer(t)
	cmd := parkingShim(t, "claude")
	s.SetAgents([]proto.AgentBackend{{Name: "worker", Command: cmd}})

	id, err := s.createPanel(originOperator, proto.KindAgent, cmd, nil, dir, "worker", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	s.mu.Lock()
	i := s.indexLocked(id)
	s.panels[i].Group, s.panels[i].Pinned, s.panels[i].Favourite = "catnip", true, true
	s.panels[i].Task = "ship the thing"
	s.mu.Unlock()
	s.stopAndWaitExited(t, id)

	if err := s.relaunchPanel(id); err != nil {
		t.Fatalf("relaunch: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	i = s.indexLocked(id)
	if i < 0 {
		t.Fatal("the slot is gone")
	}
	p := s.panels[i]
	if p.ID != id || p.Group != "catnip" || !p.Pinned || !p.Favourite || p.Task != "ship the thing" {
		t.Errorf("a re-launch dropped what hangs off the slot: %+v", p)
	}
}

// TestRelaunchRefusesWhatItCannotResolve. Each refusal names what to do instead,
// because the alternative — quietly doing what r does — would make two keys mean
// one thing on some panels and two on others, which is the worst of both.
func TestRelaunchRefusesWhatItCannotResolve(t *testing.T) {
	s, dir := identityServer(t)
	cmd := parkingShim(t, "claude")

	// A shell has no profile to re-resolve.
	shell, err := s.createPanel(originOperator, proto.KindShell, "", nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	s.stopAndWaitExited(t, shell)
	if err := s.relaunchPanel(shell); err == nil || !strings.Contains(err.Error(), "r re-runs") {
		t.Errorf("a profile-less panel should be refused with the remedy, got %v", err)
	}

	// A profile the config in force no longer names.
	s.SetAgents([]proto.AgentBackend{{Name: "worker", Command: cmd}})
	id, err := s.createPanel(originOperator, proto.KindAgent, cmd, nil, dir, "gone", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	s.stopAndWaitExited(t, id)
	if err := s.relaunchPanel(id); err == nil || !strings.Contains(err.Error(), `"gone"`) {
		t.Errorf("an unknown profile should be refused by name, got %v", err)
	}

	// A panel that is still running.
	live, err := s.createPanel(originOperator, proto.KindAgent, cmd, nil, dir, "worker", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.relaunchPanel(live); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("a live panel should be refused, got %v", err)
	}
	if err := s.relaunchPanel("nope"); err == nil {
		t.Error("an unknown id should be refused")
	}
}

// specOf is the spawn spec the server has retained for a panel.
func (s *Server) specOf(id string) ptySpecView {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := s.specs[id].Spec
	return ptySpecView{Command: spec.Command, Args: spec.Args, Dir: spec.Dir}
}

type ptySpecView struct {
	Command string
	Args    []string
	Dir     string
}

// stopAndWaitExited kills a panel's process and waits for the fleet to see it go,
// which is the precondition both re-run verbs share.
func (s *Server) stopAndWaitExited(t *testing.T, id string) {
	t.Helper()
	s.pty.Stop(id)
	waitState(t, s, id, panel.Exited)
}
