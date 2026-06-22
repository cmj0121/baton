package config

import (
	"os"
	"testing"

	"github.com/cmj0121/baton/internal/paths"
)

func TestLoadDirectoryErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A directory where the config file should be makes ReadFile fail with a
	// non-not-exist error.
	if err := os.MkdirAll(paths.ConfigFile(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("reading a directory as the config should error")
	}
}

func TestSaveDirPrepFails(t *testing.T) {
	// Point HOME at a regular file so creating $HOME/.baton fails.
	notADir := t.TempDir() + "/file"
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", notADir)
	if err := (Config{Keys: map[string]string{"a": "b"}}).Save(); err == nil {
		t.Fatal("Save should fail when its directory cannot be created")
	}
}

func TestLoadMalformedYAMLErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile(), []byte("keys: [not-a-map\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("malformed YAML should return an error")
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatalf("loading a missing config should not error: %v", err)
	}
	if len(c.Keys) != 0 {
		t.Fatalf("missing config should be empty, got %+v", c.Keys)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	off := false
	want := Config{
		Prefix:   "ctrl+a",
		Keys:     map[string]string{"new-panel": "x", "close": "W"},
		Settings: Settings{ConfirmClose: &off},
		Panel:    PanelDefaults{Shell: "/bin/zsh", Workdir: "/work", ReplayKB: 512, DiffCommand: "delta"},
	}
	if err := want.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Keys["new-panel"] != "x" || got.Keys["close"] != "W" {
		t.Fatalf("keys round-trip mismatch: %+v", got.Keys)
	}
	if got.Settings.ConfirmClose == nil || *got.Settings.ConfirmClose {
		t.Fatalf("confirm-close should round-trip as false, got %+v", got.Settings.ConfirmClose)
	}
	if got.Prefix != "ctrl+a" || got.Panel.Shell != "/bin/zsh" {
		t.Fatalf("prefix/panel round-trip mismatch: %q %q", got.Prefix, got.Panel.Shell)
	}
	if got.Panel.ReplayKB != 512 {
		t.Fatalf("replay-kb should round-trip, got %d", got.Panel.ReplayKB)
	}
	if got.Panel.Workdir != "/work" {
		t.Fatalf("workdir should round-trip, got %q", got.Panel.Workdir)
	}
	if got.Panel.DiffCommand != "delta" {
		t.Fatalf("diff-command should round-trip, got %q", got.Panel.DiffCommand)
	}
}

func TestLoadMissingSettingIsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := (Config{Keys: map[string]string{"close": "w"}}).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Settings.ConfirmClose != nil {
		t.Fatal("an unset confirm-close should load as nil so the default applies")
	}
}
