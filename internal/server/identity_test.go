package server

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
)

// identityServer is a server whose panels can be spawned for real, plus the temp
// dir they run in. XDG_RUNTIME_DIR is pinned under the test's own tree so a
// conductor's workspace never lands in the developer's runtime dir.
func identityServer(t *testing.T, opts ...Option) (*Server, string) {
	t.Helper()
	s := newHostServer(t, opts...)
	dir := os.Getenv("BATON_TEST_DIR")
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("HOME", dir)
	return s, dir
}

// restartDaemon persists the fleet, stops the server, and brings a second one up
// on the same state file — a real daemon restart, listening on its own socket.
// Nothing is faked: the returned server's panels come back exactly as Restore
// leaves them, which is the state the identity rebuild has to survive.
func restartDaemon(t *testing.T, s *Server, stateF string) *Server {
	t.Helper()
	s.SaveNow()
	s.Shutdown()
	next := newHostServer(t, WithStateFile(stateF))
	next.Restore()
	return next
}

// envValue returns the value of key in a spawn env, and whether it was present at
// all — the two questions the identity contract is made of.
func envValue(env []string, key string) (string, bool) {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// TestAgentPanelKnowsItself is the contract this unit exists for: every agent
// panel — not only the conductor — is told which panel it is. Its spawn env
// carries the control socket and its own id, and carries NO role, so the agent
// can name itself to the server without inheriting the conductor's fence.
func TestAgentPanelKnowsItself(t *testing.T) {
	s, dir := identityServer(t)

	id, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	env := s.PanelEnv(id)
	if got, ok := envValue(env, paths.EnvPanelID); !ok || got != id {
		t.Fatalf("agent env %v should carry %s=%s, got %q (present %v)", env, paths.EnvPanelID, id, got, ok)
	}
	if got, ok := envValue(env, paths.EnvSocket); !ok || got != s.socketPath() {
		t.Fatalf("agent env %v should carry the live socket %q, got %q (present %v)", env, s.socketPath(), got, ok)
	}
	// An ordinary agent is not a conductor: an injected role would hand it the
	// scoped policy (and its fence) on nothing but the fact that it is an agent.
	if got, ok := envValue(env, paths.EnvRole); ok {
		t.Fatalf("agent env must declare no role, got %s=%q", paths.EnvRole, got)
	}
}

// TestShellPanelCarriesNoIdentity pins the deliberate other half of the decision:
// a shell panel is a launcher for whatever a human types, so it is not marked
// with a panel id that every child process would then inherit. The global shell
// has always been held to this; a plain shell is held to the same.
func TestShellPanelCarriesNoIdentity(t *testing.T) {
	s, dir := identityServer(t)

	id, err := s.createPanel(proto.KindShell, "", nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if env := s.PanelEnv(id); len(env) != 0 {
		t.Fatalf("shell env should be empty, got %v", env)
	}
}

// TestConductorIdentityUnchanged guards the singleton against this unit: the
// conductor still gets socket, scoped role and own id — the server's role fence
// reads that role off the hello it produces — while a peer agent spawned beside
// it gets the same identity without the role.
func TestConductorIdentityUnchanged(t *testing.T) {
	s, dir := identityServer(t)

	cid, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "", true, false)
	if err != nil {
		t.Fatalf("create conductor: %v", err)
	}
	env := s.PanelEnv(cid)
	for _, want := range []string{
		paths.EnvSocket + "=" + s.socketPath(),
		paths.EnvRole + "=" + roleConductor,
		paths.EnvPanelID + "=" + cid,
	} {
		if !slices.Contains(env, want) {
			t.Fatalf("conductor env %v missing %q", env, want)
		}
	}
	// The workspace is the conductor's own; the peer below must not get one.
	if ws := s.PanelDir(cid); ws == dir {
		t.Fatalf("conductor should run in a managed workspace, not %q", dir)
	}
	t.Cleanup(func() { _ = os.RemoveAll(s.PanelDir(cid)) })

	pid, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create peer agent: %v", err)
	}
	if got, ok := envValue(s.PanelEnv(pid), paths.EnvRole); ok {
		t.Fatalf("a peer agent must not inherit the conductor role, got %s=%q", paths.EnvRole, got)
	}
}

// TestPanelIdentitiesAreDistinct checks the property the whole feature rests on:
// two agent panels are told two different ids, so "raise my hand" resolves to one
// panel rather than to whichever spoke last.
func TestPanelIdentitiesAreDistinct(t *testing.T) {
	s, dir := identityServer(t)

	first, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	a, _ := envValue(s.PanelEnv(first), paths.EnvPanelID)
	b, _ := envValue(s.PanelEnv(second), paths.EnvPanelID)
	if a == "" || b == "" {
		t.Fatalf("both agents should be told an id, got %q and %q", a, b)
	}
	if a == b {
		t.Fatalf("two panels were told the same id %q", a)
	}
}

// TestRespawnRebuildsSameIdentity drives the case the rebuild exists for, through
// the real path rather than a simulation of it: an agent panel is persisted, the
// daemon is replaced by a second one on a fresh socket, and the panel is re-run
// from what survived on disk. The restored spec is asserted to carry no
// environment at all — that is the premise, established rather than assumed — and
// the re-run panel then comes back as the same self, pointed at the socket that
// is actually listening now.
func TestRespawnRebuildsSameIdentity(t *testing.T) {
	stateF := filepath.Join(t.TempDir(), "state.json")
	first, dir := identityServer(t, WithStateFile(stateF))

	id, err := first.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	want, _ := envValue(first.PanelEnv(id), paths.EnvPanelID)
	oldSock := first.socketPath()

	s := restartDaemon(t, first, stateF)
	if s.socketPath() == oldSock {
		t.Fatalf("the second daemon should listen elsewhere; both are on %q", oldSock)
	}
	// The snapshot persists command, args, dir and profile — never the environment.
	// A re-run that replayed the frozen spec would hand the agent nothing.
	if env := s.PanelEnv(id); len(env) != 0 {
		t.Fatalf("a restored spec should carry no env, got %v", env)
	}

	if err := s.respawnPanel(id); err != nil {
		t.Fatalf("respawn: %v", err)
	}

	env := s.PanelEnv(id)
	if got, ok := envValue(env, paths.EnvPanelID); !ok || got != want {
		t.Fatalf("respawned env %v should carry the same id %q, got %q (present %v)", env, want, got, ok)
	}
	if got, ok := envValue(env, paths.EnvSocket); !ok || got != s.socketPath() {
		t.Fatalf("respawned env %v should carry the live socket %q, got %q (present %v)", env, s.socketPath(), got, ok)
	}
	if got, ok := envValue(env, paths.EnvRole); ok {
		t.Fatalf("a respawned agent must declare no role, got %s=%q", paths.EnvRole, got)
	}
}

// TestRespawnedShellStaysAnonymous is the respawn counterpart of the shell
// decision, over the same restart: a re-run shell is not quietly upgraded into a
// panel that names itself.
func TestRespawnedShellStaysAnonymous(t *testing.T) {
	stateF := filepath.Join(t.TempDir(), "state.json")
	first, dir := identityServer(t, WithStateFile(stateF))

	id, err := first.createPanel(proto.KindShell, "", nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}

	s := restartDaemon(t, first, stateF)
	if err := s.respawnPanel(id); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	if env := s.PanelEnv(id); len(env) != 0 {
		t.Fatalf("respawned shell env should stay empty, got %v", env)
	}
}

// TestConductorRespawnKeepsScopedRole checks the conductor's own re-run over the
// same real restart: it still rebuilds the full scoped identity — workspace, role
// and self — since the role fence is what stops a looping agent from closing
// itself, and a conductor that came back unfenced would be the regression that
// matters most here.
func TestConductorRespawnKeepsScopedRole(t *testing.T) {
	stateF := filepath.Join(t.TempDir(), "state.json")
	first, dir := identityServer(t, WithStateFile(stateF))

	cid, err := first.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "", true, false)
	if err != nil {
		t.Fatalf("create conductor: %v", err)
	}
	ws := first.PanelDir(cid)
	t.Cleanup(func() { _ = os.RemoveAll(ws) })

	s := restartDaemon(t, first, stateF)
	if err := s.respawnPanel(cid); err != nil {
		t.Fatalf("respawn conductor: %v", err)
	}
	env := s.PanelEnv(cid)
	if got, ok := envValue(env, paths.EnvRole); !ok || got != roleConductor {
		t.Fatalf("respawned conductor env %v should declare the scoped role, got %q (present %v)", env, got, ok)
	}
	if got, ok := envValue(env, paths.EnvPanelID); !ok || got != cid {
		t.Fatalf("respawned conductor env %v should carry its own id %q, got %q", env, cid, got)
	}
	if _, err := os.Stat(filepath.Join(s.PanelDir(cid), "BATON.md")); err != nil {
		t.Fatalf("respawned conductor workspace should be rewired: %v", err)
	}
}
