package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cmj0121/baton/internal/paths"
)

// TestScoreDefaults covers the accessors' defaulting: an absent (or partial)
// score section must land on "enabled, $HOME/.baton", an explicit false must
// stick, and a hand-written "~" path must expand to the test's fake home.
func TestScoreDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	off, on := false, true
	tests := []struct {
		name    string
		cfg     ScoreConfig
		enabled bool
		dir     string
	}{
		{"absent section", ScoreConfig{}, true, filepath.Join(home, ".baton")},
		{"explicit false", ScoreConfig{Enabled: &off}, false, filepath.Join(home, ".baton")},
		{"explicit true", ScoreConfig{Enabled: &on}, true, filepath.Join(home, ".baton")},
		{"tilde dir expands", ScoreConfig{Dir: "~/x"}, true, filepath.Join(home, "x")},
		{"absolute dir kept", ScoreConfig{Dir: "/var/lib/baton"}, true, "/var/lib/baton"},
		{"blank dir is unset", ScoreConfig{Dir: "   "}, true, filepath.Join(home, ".baton")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsEnabled(); got != tc.enabled {
				t.Errorf("IsEnabled() = %v; want %v", got, tc.enabled)
			}
			if got := tc.cfg.Directory(); got != tc.dir {
				t.Errorf("Directory() = %q; want %q", got, tc.dir)
			}
		})
	}
}

// TestScoreLoad parses a full score block through the real Load path, so the
// YAML keys and the section's spot on the Config root are both exercised.
func TestScoreLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	yaml := "score:\n  dir: ~/scores\n  enabled: false\n"
	if err := os.WriteFile(paths.ConfigFile(), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Score.IsEnabled() {
		t.Error("enabled: false should disable the subsystem")
	}
	if want := filepath.Join(home, "scores"); got.Score.Directory() != want {
		t.Errorf("Directory() = %q; want %q", got.Score.Directory(), want)
	}
}

// TestScoreRoundTrip saves a config carrying a score section and loads it back,
// proving the section survives the same Save/Load cycle every other section
// rides — an explicit false included, which omitempty must not eat.
func TestScoreRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	off := false
	want := Config{Score: ScoreConfig{Dir: "~/scores", Enabled: &off}}
	if err := want.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Score.Dir != "~/scores" {
		t.Errorf("dir should round-trip verbatim, got %q", got.Score.Dir)
	}
	if got.Score.Enabled == nil || *got.Score.Enabled {
		t.Errorf("enabled should round-trip as false, got %+v", got.Score.Enabled)
	}
}
