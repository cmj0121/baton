package server

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
)

// envProbe is an agent command that prints the variables these tests ask about
// between a mark's brackets, then exits.
func envProbe(mark string) []string {
	return []string{"-c", `printf '` + mark + `<%s|%s>' "$GROK_THEME" "$` + paths.EnvPanelID + `"`}
}

// ringHas waits until the panel's output ring holds want.
func ringHas(t *testing.T, s *Server, id, want string) {
	t.Helper()
	waitFor(t, "output "+want, func() bool {
		return strings.Contains(string(s.pty.SnapshotAt(id).Data), want)
	})
}

// TestProfileEnvReachesTheProcess is the feature: a profile's env is in the
// spawned process's environment, and the identity baton injects still wins a key
// the profile also names, so a profile cannot make a panel claim another id.
func TestProfileEnvReachesTheProcess(t *testing.T) {
	s, dir := identityServer(t)
	s.Reload(Settings{AgentEnv: map[string][]string{
		"grok": {"GROK_THEME=terminal", paths.EnvPanelID + "=forged"},
	}})

	id, err := s.createPanel(originOperator, proto.KindAgent, "/bin/sh", envProbe("A"), dir, "grok", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	ringHas(t, s, id, "A<terminal|"+id+">")

	// The retained spec is what a re-run starts from; the profile env must not be
	// baked into it, or a reload could never take it back out.
	if v, ok := envValue(s.PanelEnv(id), "GROK_THEME"); ok {
		t.Fatalf("retained spec must not carry the profile env, got GROK_THEME=%q", v)
	}
}

// TestProfileEnvIsPerProfile pins that the table is keyed by profile: a panel of
// another profile gets none of it.
func TestProfileEnvIsPerProfile(t *testing.T) {
	s, dir := identityServer(t)
	t.Setenv("GROK_THEME", "") // whatever the developer's shell exports must not leak in
	s.Reload(Settings{AgentEnv: map[string][]string{"grok": {"GROK_THEME=terminal"}}})

	id, err := s.createPanel(originOperator, proto.KindAgent, "/bin/sh", envProbe("B"), dir, "claude", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	ringHas(t, s, id, "B<|"+id+">")
}

// TestRespawnReadsTheReloadedProfileEnv checks the env is resolved at every start
// rather than frozen at the first: after a reload changes it, a re-run of the same
// exited panel starts with the new value.
func TestRespawnReadsTheReloadedProfileEnv(t *testing.T) {
	s, dir := identityServer(t)
	s.Reload(Settings{AgentEnv: map[string][]string{"grok": {"GROK_THEME=dark"}}})

	id, err := s.createPanel(originOperator, proto.KindAgent, "/bin/sh", envProbe("C"), dir, "grok", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	ringHas(t, s, id, "C<dark|"+id+">")
	waitFor(t, "the probe to exit", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		idx := s.indexLocked(id)
		return idx >= 0 && s.panels[idx].State == panel.Exited
	})

	s.Reload(Settings{AgentEnv: map[string][]string{"grok": {"GROK_THEME=terminal"}}})
	if err := s.respawnPanel(id); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	ringHas(t, s, id, "C<terminal|"+id+">")
}

// TestReloadSwapsAgentSubmit pins that the submit table is the config, not a
// cache: a reload replaces it whole, so a profile dropped from the file falls
// back to the default instead of keeping the sequence it had.
func TestReloadSwapsAgentSubmit(t *testing.T) {
	s := newHostServer(t)
	submitOf := func(profile string) (string, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		v, ok := s.agentSubmit[profile]
		return v, ok
	}

	s.Reload(Settings{AgentSubmit: map[string]string{"grok": "\r"}})
	if v, ok := submitOf("grok"); !ok || v != "\r" {
		t.Fatalf("after reload grok submit = %q, %v; want CR", v, ok)
	}

	s.Reload(Settings{AgentSubmit: map[string]string{"codex": "\r\n"}})
	if v, ok := submitOf("grok"); ok {
		t.Fatalf("a profile dropped from the config must not keep its submit, got %q", v)
	}
	if v, _ := submitOf("codex"); v != "\r\n" {
		t.Fatalf("codex submit = %q, want CRLF", v)
	}
}
