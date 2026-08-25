package main

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/isolate"
	"github.com/cmj0121/baton/internal/restart"
)

// TestUsageOptionIntervalBranches exercises the interval resolution in
// usageOption: a zero interval falls back to the per-source default, a positive
// but too-small interval is clamped to the 10s floor, and a sane interval passes
// through. Each just needs to build a valid option without panicking.
func TestUsageOptionIntervalBranches(t *testing.T) {
	t.Setenv("BATON_ANTHROPIC_ADMIN_KEY", "") // force the local source, no network
	for _, secs := range []int{0, 5, 45} {
		cfg := config.Config{}
		cfg.Usage.Source = "local"
		cfg.Usage.Interval = secs
		if usageOption(cfg) == nil {
			t.Fatalf("usageOption(interval=%d) returned a nil option", secs)
		}
	}
}

// TestReloadableSettingsBranches covers the value-present branches of
// reloadableSettings: an explicit name-conflict toggle, a positive replay buffer,
// and a positive queue cap all flow onto the reloadable struct.
func TestReloadableSettingsBranches(t *testing.T) {
	yes := true
	cfg := config.Config{}
	cfg.Settings.AllowNameConflict = &yes
	cfg.Panel.ReplayKB = 64
	cfg.Panel.Workdir = "/tmp/work"
	cfg.Queue.Max = 200
	cfg.Queue.Concurrency = 3

	rc := reloadableSettings(cfg)
	if !rc.settings.AllowNameConflict {
		t.Error("allowNameConflict should follow the config toggle")
	}
	if rc.settings.ReplayBytes != 64*1024 {
		t.Errorf("replayBytes = %d, want %d", rc.settings.ReplayBytes, 64*1024)
	}
	if rc.settings.DefaultDir != "/tmp/work" {
		t.Errorf("defaultDir = %q, want /tmp/work", rc.settings.DefaultDir)
	}
	if rc.settings.QueueMax != 200 {
		t.Errorf("queueMax = %d, want 200", rc.settings.QueueMax)
	}
	if rc.settings.QueueConcurrency != 3 {
		t.Errorf("queueConcurrency = %d, want 3", rc.settings.QueueConcurrency)
	}

	// The unset path: no toggle, no replay override → strict defaults.
	def := reloadableSettings(config.Config{})
	if def.settings.AllowNameConflict || def.settings.ReplayBytes != 0 || def.settings.QueueMax != -1 {
		t.Errorf("unset config should keep strict defaults, got %+v", def)
	}
}

// TestRestartPoliciesProjectsConfig: the fleet policy and the per-profile ones
// reach the server the way the resource caps do — the fleet's as a floor, a
// profile's layered over it, and a profile with nothing to say left out.
func TestRestartPoliciesProjectsConfig(t *testing.T) {
	cfg := config.Config{Panel: config.PanelDefaults{
		Restart: config.RestartConfig{Restart: "on-failure", RestartMax: 4},
		Agents: map[string]config.AgentProfile{
			"claude": {Command: "claude", Restart: config.RestartConfig{Restart: "never"}},
			"codex":  {Command: "codex"},
		},
	}}

	fleet, perAgent := restartPolicies(cfg)
	if fleet.Mode != restart.OnFailure || fleet.Max != 4 {
		t.Fatalf("fleet = %+v", fleet)
	}
	if perAgent["claude"].Mode != restart.Never {
		t.Errorf("claude = %+v, want the never override", perAgent["claude"])
	}
	if _, listed := perAgent["codex"]; listed {
		t.Error("a profile with no policy of its own should not be listed")
	}
}

// TestRestartPoliciesDropsAMalformedPolicy: a policy baton half-understood would
// start processes on a schedule the user did not write. It is dropped and
// reported instead, leaving a fleet that restarts nothing.
func TestRestartPoliciesDropsAMalformedPolicy(t *testing.T) {
	cfg := config.Config{Panel: config.PanelDefaults{
		Restart: config.RestartConfig{Restart: "always"},
		Agents: map[string]config.AgentProfile{
			"claude": {Command: "claude", Restart: config.RestartConfig{RestartBackoff: "soon"}},
		},
	}}

	fleet, perAgent := restartPolicies(cfg)
	if !fleet.IsZero() {
		t.Errorf("a refused fleet policy should restart nothing, got %+v", fleet)
	}
	if _, listed := perAgent["claude"]; listed {
		t.Error("a malformed profile policy should be dropped, not half-applied")
	}
}

// TestIsolationPoliciesProjectsConfig covers the seam between the config file and
// the daemon: a profile that asks to isolate reaches the server as a policy, and
// one that does not is absent rather than stored empty.
func TestIsolationPoliciesProjectsConfig(t *testing.T) {
	cfg := config.Config{Panel: config.PanelDefaults{Agents: map[string]config.AgentProfile{
		"walled": {Command: "claude", Isolate: "docker", Image: "example/agent:1", Network: "bridge"},
		"plain":  {Command: "claude"},
	}}}

	got := isolationPolicies(cfg)
	if len(got) != 1 {
		t.Fatalf("only the profile that asked for it belongs in the table, got %v", got)
	}
	p := got["walled"]
	if p.Mode != isolate.ModeDocker || p.Image != "example/agent:1" || p.Network != isolate.NetworkBridge {
		t.Fatalf("the policy did not survive the projection: %+v", p)
	}
	if _, ok := got["plain"]; ok {
		t.Error("a profile with no isolation must not be carried at all")
	}
}

// TestIsolationPoliciesKeepsABrokenPolicy is the property that makes this
// projection different from every other one here: a policy baton cannot read is
// kept and poisoned, because dropping it would spawn the panel on the host.
func TestIsolationPoliciesKeepsABrokenPolicy(t *testing.T) {
	cfg := config.Config{Panel: config.PanelDefaults{Agents: map[string]config.AgentProfile{
		"walled": {Command: "claude", Isolate: "dockerr", Image: "example/agent:1"},
	}}}

	p, ok := isolationPolicies(cfg)["walled"]
	if !ok {
		t.Fatal("a broken policy must reach the daemon, or the panel spawns unconfined")
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "dockerr") {
		t.Fatalf("it must carry the reason to fail the spawn with, got %v", err)
	}
}

// TestReloadableSettingsCarriesIsolation checks the table is actually attached to
// the settings the daemon reloads from, not merely computable.
func TestReloadableSettingsCarriesIsolation(t *testing.T) {
	cfg := config.Config{Panel: config.PanelDefaults{Agents: map[string]config.AgentProfile{
		"walled": {Command: "claude", Isolate: "docker", Image: "example/agent:1"},
	}}}
	if got := reloadableSettings(cfg).settings.AgentIsolate; len(got) != 1 {
		t.Fatalf("AgentIsolate = %v, want the walled profile", got)
	}
}
