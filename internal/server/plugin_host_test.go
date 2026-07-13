package server

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// newHostServer builds a bare Server on a private unix socket for exercising the
// exported baton.* host methods directly (no Serve loop, no attached clients). It
// sets SHELL so a shell panel spawns a real, short-lived process whose spec is
// stashed for respawn. The socket lives in a short-named temp dir to stay under
// macOS's unix path cap.
func newHostServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return New(ln)
}

// TestHostSettersToggle covers the wiring setters that only store or push state:
// each mirrors a socket path but has no return to assert, so we drive them to prove
// they neither block nor panic and leave the server usable.
func TestHostSettersToggle(t *testing.T) {
	s := newHostServer(t)

	s.SetOutputEvents(true)
	s.SetOutputEvents(false)
	s.SetRunCommand(func(string) error { return nil })
	s.SetClientConfig([]byte(`{"prefix":"ctrl+a"}`))
	s.SetPluginCommands(nil)
	s.PushConfig()   // re-broadcasts config to (zero) attached clients
	s.SetFooter("x") // sets and broadcasts the footer segment
	s.SetFooter("")  // clearing is also valid
}

// TestHostMethodsExercise walks the exported baton.* fleet API — the plugin's whole
// mutation surface — hitting both the success path (which broadcasts) and the error
// path (which surfaces the wrapped core error) of each wrapper, so the thin skins in
// plugin.go are covered on both sides.
func TestHostMethodsExercise(t *testing.T) {
	s := newHostServer(t)
	dir := t.TempDir()

	// Spawn: grouped (covers the group branch) and ungrouped.
	id, err := s.Spawn("shell", "", nil, dir, "alpha")
	if err != nil {
		t.Fatalf("spawn grouped: %v", err)
	}
	id2, err := s.Spawn("shell", "", nil, dir, "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// Spawn error: an agent panel with no command is rejected before it starts.
	if _, err := s.Spawn("agent", "", nil, dir, ""); err == nil {
		t.Fatal("an agent spawn with no command should error")
	}

	// Dispatch: success (a spawning panel queues the brief) and the empty-id error.
	if err := s.Dispatch(id, "do a thing"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.Dispatch("", "x"); err == nil {
		t.Fatal("dispatch with no id should error")
	}

	// Enqueue: success and the empty-prompt error.
	if tid, err := s.Enqueue("queued brief", "alpha"); err != nil || tid == "" {
		t.Fatalf("enqueue: id=%q err=%v", tid, err)
	}
	if _, err := s.Enqueue("", ""); err == nil {
		t.Fatal("enqueue with no prompt should error")
	}

	// DispatchGroup: fans to the alpha members; an unknown group errors.
	if n, err := s.DispatchGroup("alpha", "hello group"); err != nil || n == 0 {
		t.Fatalf("dispatch group: n=%d err=%v", n, err)
	}
	if _, err := s.DispatchGroup("nope-nope", "x"); err == nil {
		t.Fatal("dispatch to an unknown group should error")
	}

	// Group / Rename / Move / SetPinned / GroupShow success paths.
	if err := s.Group([]string{id2}, "beta"); err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := s.Rename(id2, "", "renamed-panel"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := s.Move([]string{id2}, 0); err != nil {
		t.Fatalf("move: %v", err)
	}
	if err := s.SetPinned([]string{id2}, true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := s.SetPinned([]string{id2}, false); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if err := s.GroupShow("alpha", 3); err != nil {
		t.Fatalf("group show: %v", err)
	}

	// Error branches for the same wrappers (empty/invalid inputs).
	if err := s.Group(nil, ""); err == nil {
		t.Fatal("group with no name should error")
	}
	if err := s.Ungroup(nil, ""); err == nil {
		t.Fatal("ungroup with nothing to do should error")
	}
	if err := s.Rename("", "", ""); err == nil {
		t.Fatal("rename with no target should error")
	}
	if err := s.Move(nil, 0); err == nil {
		t.Fatal("move with no ids should error")
	}
	if err := s.SetPinned(nil, true); err == nil {
		t.Fatal("pin with no ids should error")
	}
	if err := s.GroupShow("", 1); err == nil {
		t.Fatal("group show with no group should error")
	}

	// Signal: a real signal to a live panel, then an unknown signal name.
	if err := s.Signal([]string{id}, "TERM"); err != nil {
		t.Fatalf("signal: %v", err)
	}
	if err := s.Signal([]string{id}, "NOTASIGNAL"); err == nil {
		t.Fatal("an unknown signal name should error")
	}

	// Read paths behind baton.panels / baton.groups.
	if got := s.PanelInfos(); len(got) == 0 {
		t.Fatal("PanelInfos should report the live fleet")
	}
	if got := s.GroupInfos(); len(got) == 0 {
		t.Fatalf("GroupInfos should report the alpha view set by GroupShow, got %+v", got)
	}

	// Ungroup a whole named group (dissolve beta).
	if err := s.Ungroup(nil, "beta"); err != nil {
		t.Fatalf("ungroup group: %v", err)
	}

	// Close: batch retire, then the empty-ids error.
	if err := s.Close([]string{id, id2}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Close(nil); err == nil {
		t.Fatal("close with no ids should error")
	}
}

// TestHostSpawnGroupingFailureKeepsPanel covers Spawn's salvage branch: the panel
// starts, but filing it under a name already taken by another panel's title fails —
// Spawn still returns the live id alongside the grouping error rather than stranding
// the process.
func TestHostSpawnGroupingFailureKeepsPanel(t *testing.T) {
	s := newHostServer(t)
	dir := t.TempDir()

	first, err := s.Spawn("shell", "", nil, dir, "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := s.Rename(first, "", "taken-name"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	id, err := s.Spawn("shell", "", nil, dir, "taken-name")
	if err == nil {
		t.Fatal("spawning into a name already taken should surface a grouping error")
	}
	if id == "" {
		t.Fatal("a grouping failure must still return the live panel's id")
	}
	if got := s.PanelInfos(); len(got) != 2 {
		t.Fatalf("the salvaged panel must stay in the fleet, got %d panels", len(got))
	}
}

// TestHostTitleHooks covers the panel.title wiring: SetTitleHook's early return when
// a hook is registered, and SetPanelTitle's clear path where a title equal to the
// panel's base drops the override rather than storing a redundant copy.
func TestHostTitleHooks(t *testing.T) {
	s := newHostServer(t)
	id, err := s.Spawn("shell", "", nil, t.TempDir(), "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// A registered hook: SetTitleHook(true) returns without touching overrides.
	s.SetTitleHook(true)

	// Read the base title and seed a display override, then set the override back to
	// the base — which must clear it (next == base -> next == "").
	s.mu.Lock()
	base := ""
	if i := s.indexLocked(id); i >= 0 {
		base = s.panels[i].Title
		s.panels[i].DisplayTitle = "temporary-override"
	}
	s.mu.Unlock()

	s.SetPanelTitle(id, base)

	s.mu.Lock()
	got := ""
	if i := s.indexLocked(id); i >= 0 {
		got = s.panels[i].DisplayTitle
	}
	s.mu.Unlock()
	if got != "" {
		t.Fatalf("DisplayTitle = %q, want it cleared when the title equals the base", got)
	}
}

// TestHostRespawnAndPurge covers Respawn (drive a live panel to Exited, re-run it
// from its frozen spec) and Purge (drop exited panels, and the no-op when none are
// exited), plus the error legs of each.
func TestHostRespawnAndPurge(t *testing.T) {
	s := newHostServer(t)

	// A fresh fleet has nothing exited, so Purge is a no-op returning zero.
	if n := s.Purge(); n != 0 {
		t.Fatalf("purge on an empty fleet = %d, want 0", n)
	}

	id, err := s.Spawn("shell", "", nil, t.TempDir(), "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Respawn refuses a running panel, and an unknown id.
	if err := s.Respawn(id); err == nil {
		t.Fatal("respawn of a running panel should error")
	}
	if err := s.Respawn("no-such-id"); err == nil {
		t.Fatal("respawn of an unknown id should error")
	}

	// Mark it exited under the lock, then respawn it from its stored spec.
	s.mu.Lock()
	if i := s.indexLocked(id); i >= 0 {
		s.panels[i].State = panel.Exited
	}
	s.mu.Unlock()
	if err := s.Respawn(id); err != nil {
		t.Fatalf("respawn of an exited panel: %v", err)
	}

	// Inject a dead slot and confirm Purge reaps it and reports the count.
	s.mu.Lock()
	s.panels = append(s.panels, panel.Panel{ID: "dead1", State: panel.Exited})
	s.mu.Unlock()
	if n := s.Purge(); n < 1 {
		t.Fatalf("purge should drop the exited panel, got %d", n)
	}
}
