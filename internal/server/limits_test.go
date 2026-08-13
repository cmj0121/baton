package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cmj0121/baton/internal/limits"
	"github.com/cmj0121/baton/internal/proto"
)

// limitsServer is a server seeded with a two-layer policy — a fleet-wide cap and
// one profile that raises the memory half of it — plus the temp dir its socket
// lives in, which doubles as a workdir for the panels the tests spawn.
func limitsServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := newHostServer(t, WithLimits(
		limits.Limits{CPUs: "2", Memory: "4Gi", Pids: "512"},
		map[string]limits.Limits{"heavy": {Memory: "16Gi"}},
	))
	t.Cleanup(func() { s.Shutdown() })
	return s, os.Getenv("BATON_TEST_DIR")
}

// TestEffectiveLimitsLayersProfile is the resolution rule as the server applies
// it on a real spawn: a panel from a profile gets the fleet-wide caps with that
// profile's own layered over, and a panel with no profile gets the fleet's alone.
func TestEffectiveLimitsLayersProfile(t *testing.T) {
	s, dir := limitsServer(t)

	heavy, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "heavy", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	plain, err := s.createPanel(proto.KindShell, "", nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}

	if got := s.EffectiveLimits(heavy); got != (limits.Limits{CPUs: "2", Memory: "16Gi", Pids: "512"}) {
		t.Errorf("the profile should raise memory and inherit the rest, got %+v", got)
	}
	if got := s.EffectiveLimits(plain); got != (limits.Limits{CPUs: "2", Memory: "4Gi", Pids: "512"}) {
		t.Errorf("a profile-less panel should get the fleet-wide caps, got %+v", got)
	}
	if got := s.EffectiveLimits("no-such-panel"); got != (limits.Limits{CPUs: "2", Memory: "4Gi", Pids: "512"}) {
		t.Errorf("an unknown panel should resolve to the fleet-wide caps, got %+v", got)
	}
}

// TestReloadSwapsLimitsUnderLiveFleet is the hot-reload path: a panel spawned
// under one policy resolves the next one after a reload, with no respawn and no
// per-panel migration — because the panel records its profile name, not the caps
// that name resolved to.
func TestReloadSwapsLimitsUnderLiveFleet(t *testing.T) {
	s, dir := limitsServer(t)

	id, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "heavy", false, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before := s.EffectiveLimits(id)
	if before.Memory != "16Gi" {
		t.Fatalf("precondition: the panel should start on the seeded policy, got %+v", before)
	}

	s.Reload(Settings{
		Limits:      limits.Limits{CPUs: "8", Memory: "4Gi"},
		AgentLimits: map[string]limits.Limits{"heavy": {Memory: "32Gi", Pids: limits.Unlimited}},
	})

	want := limits.Limits{CPUs: "8", Memory: "32Gi", Pids: limits.Unlimited}
	if got := s.EffectiveLimits(id); got != want {
		t.Fatalf("the live panel should resolve the reloaded policy: got %+v, want %+v", got, want)
	}

	// A profile dropped from the config falls back to the fleet-wide caps rather
	// than keeping the ones it had — the policy is the config, not a cache.
	s.Reload(Settings{Limits: limits.Limits{CPUs: "1"}})
	if got := s.EffectiveLimits(id); got != (limits.Limits{CPUs: "1"}) {
		t.Fatalf("dropping the profile should fall back to the fleet-wide caps, got %+v", got)
	}
}

// TestReloadKeepsTheRestOfTheSettings guards the signature change: Reload now
// takes one value, so every knob it used to carry positionally must still land.
func TestReloadKeepsTheRestOfTheSettings(t *testing.T) {
	s, _ := limitsServer(t)

	s.Reload(Settings{
		AllowNameConflict: true,
		DefaultDir:        "/work",
		DiffCommand:       "delta",
		Editor:            "vim",
		WorktreeDir:       "/wt",
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.allowNameConflict || s.defaultDir != "/work" || s.diffCommand != "delta" || s.editor != "vim" || s.worktreeDir != "/wt" {
		t.Fatalf("Reload dropped a setting: names=%v dir=%q diff=%q editor=%q worktree=%q",
			s.allowNameConflict, s.defaultDir, s.diffCommand, s.editor, s.worktreeDir)
	}
}

// TestRestoredPanelKeepsItsProfile checks the profile survives a daemon restart:
// the snapshot carries it, so a restored panel resolves its caps through the same
// profile the live one did instead of silently dropping to the fleet-wide ones.
func TestRestoredPanelKeepsItsProfile(t *testing.T) {
	s, dir := limitsServer(t)
	stateF := filepath.Join(dir, "state.json")
	s.stateF = stateF

	id, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "heavy", false, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.snapshotState().Save(stateF); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(stateF); err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}

	// A fresh daemon on the same snapshot and the same policy.
	next, _ := limitsServer(t)
	next.stateF = stateF
	next.Restore()

	if got := next.EffectiveLimits(id); got.Memory != "16Gi" {
		t.Fatalf("the restored panel lost its profile, got %+v", got)
	}
}

// TestWorktreeAgentInheritsTheProfile covers the one derived spawn that must
// carry the profile across: the git worktree agent is a copy of its source, so it
// has to land under the same caps rather than dropping to the fleet-wide ones.
func TestWorktreeAgentInheritsTheProfile(t *testing.T) {
	s, dir := limitsServer(t)

	id, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "heavy", false, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	spec, err := s.agentTargetSpec(id, "git")
	if err != nil {
		t.Fatalf("agentTargetSpec: %v", err)
	}
	if spec.Profile != "heavy" {
		t.Fatalf("the target spec should carry the profile, got %q", spec.Profile)
	}
	if spec.Command != "/bin/sh" || spec.Dir != dir {
		t.Fatalf("the target spec should still carry the process spec, got %+v", spec.Spec)
	}
}
