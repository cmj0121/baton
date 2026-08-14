package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/cmj0121/baton/internal/limits"
)

// TestLoadDropsUnreadableLimits checks the hand-edit guard: a typo in the config
// degrades that one field to "no cap" instead of travelling on unreadable, and
// the readable fields beside it survive.
func TestLoadDropsUnreadableLimits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".baton"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "panel:\n  limits:\n    cpus: \"2\"\n    memory: 4 gigs\n    pids: \"512\"\n"
	if err := os.WriteFile(filepath.Join(home, ".baton", "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Panel.Limits.Memory != "" {
		t.Errorf("the unreadable memory limit should be dropped, got %q", cfg.Panel.Limits.Memory)
	}
	if cfg.Panel.Limits.CPUs != "2" || cfg.Panel.Limits.Pids != "512" {
		t.Errorf("the readable limits should survive, got %+v", cfg.Panel.Limits)
	}
}

// TestLimitsYAMLRoundTrip pins the on-disk spelling — the keys a user hand-edits
// — and confirms an empty policy writes no limits section at all.
func TestLimitsYAMLRoundTrip(t *testing.T) {
	in := Config{Panel: PanelDefaults{Limits: limits.Limits{CPUs: "2", MemoryHigh: "3Gi", NOFile: "4096"}}}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"limits:", "cpus: \"2\"", "memory-high: 3Gi", "nofile: \"4096\""} {
		if !strings.Contains(string(data), want) {
			t.Errorf("marshalled config should contain %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "memory:") {
		t.Errorf("an unset field should be omitted:\n%s", data)
	}

	var out Config
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Panel.Limits != in.Panel.Limits {
		t.Fatalf("round trip changed the limits: %+v → %+v", in.Panel.Limits, out.Panel.Limits)
	}

	empty, err := yaml.Marshal(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "limits:") {
		t.Errorf("an empty config should write no limits section:\n%s", empty)
	}
}

// TestLoadDropsUnreadableProfileLimits extends the hand-edit guard to the
// per-agent limits: a typo in one profile must not travel on, and must not take
// the profile's command with it.
func TestLoadDropsUnreadableProfileLimits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".baton"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "panel:\n  agents:\n    claude:\n      command: claude\n      limits:\n        cpus: heaps\n        memory: 8Gi\n"
	if err := os.WriteFile(filepath.Join(home, ".baton", "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	prof := cfg.Panel.Agents["claude"]
	if prof.Command != "claude" {
		t.Errorf("the profile command should survive, got %q", prof.Command)
	}
	if prof.Limits.CPUs != "" {
		t.Errorf("the unreadable cpu limit should be dropped, got %q", prof.Limits.CPUs)
	}
	if prof.Limits.Memory != "8Gi" {
		t.Errorf("the readable memory limit should survive, got %q", prof.Limits.Memory)
	}
}
