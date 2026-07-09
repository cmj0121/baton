package main

import (
	"testing"

	"github.com/cmj0121/baton/internal/config"
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
	if !rc.allowNameConflict {
		t.Error("allowNameConflict should follow the config toggle")
	}
	if rc.replayBytes != 64*1024 {
		t.Errorf("replayBytes = %d, want %d", rc.replayBytes, 64*1024)
	}
	if rc.defaultDir != "/tmp/work" {
		t.Errorf("defaultDir = %q, want /tmp/work", rc.defaultDir)
	}
	if rc.queueMax != 200 {
		t.Errorf("queueMax = %d, want 200", rc.queueMax)
	}
	if rc.queueConcurrency != 3 {
		t.Errorf("queueConcurrency = %d, want 3", rc.queueConcurrency)
	}

	// The unset path: no toggle, no replay override → strict defaults.
	def := reloadableSettings(config.Config{})
	if def.allowNameConflict || def.replayBytes != 0 || def.queueMax != -1 {
		t.Errorf("unset config should keep strict defaults, got %+v", def)
	}
}
