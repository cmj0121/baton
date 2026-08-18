package config

import (
	"os"
	"path/filepath"
	"strings"
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

// TestLoadNormalizesNegativeReplayKB checks a hand-edited negative replay buffer
// is clamped to zero on Load rather than passed through — zero is what every
// consumer reads as "use the built-in default".
func TestLoadNormalizesNegativeReplayKB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile(), []byte("panel:\n  replay-kb: -5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Panel.ReplayKB != 0 {
		t.Fatalf("negative replay-kb should clamp to 0, got %d", got.Panel.ReplayKB)
	}
}

// TestSaveAtomicNoLeftoverTmp confirms the atomic write leaves no sibling .tmp
// file behind on the happy path.
func TestSaveAtomicNoLeftoverTmp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := (Config{Keys: map[string]string{"close": "w"}}).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(paths.ConfigFile()))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after atomic save: %s", e.Name())
		}
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

// TestFoldSimilarSetting checks the group summary's fold switch reaches the file
// under the name the docs give it, and that leaving it out stays nil — a tri-state
// the cockpit reads as "on", so the default can change without a rewritten config
// masking it.
func TestFoldSimilarSetting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	off := false
	if err := (Config{Settings: Settings{FoldSimilar: &off}}).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(paths.ConfigFile())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "fold-similar: false") {
		t.Fatalf("the setting should be written as fold-similar, got:\n%s", raw)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Settings.FoldSimilar == nil || *got.Settings.FoldSimilar {
		t.Fatalf("fold-similar should round-trip as false, got %+v", got.Settings.FoldSimilar)
	}
	if err := (Config{}).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got, _ := Load(); got.Settings.FoldSimilar != nil {
		t.Fatal("an unset fold-similar should load as nil so the default applies")
	}
}

// TestFoldQuietSetting: the dashboard's quiet threshold reaches the file under the
// name the docs give it, and an EXPLICIT 0 survives the round trip.
//
// That last part is why the field is a pointer rather than a plain int, and it is
// not pedantry: 0 is the value that means "never fold". A bare int with omitempty
// would drop the line on the next rewrite of the file — which the cockpit does
// every time you toggle a setting — and the user who switched folding off would
// find it back on, with nothing in the config to explain why.
func TestFoldQuietSetting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	never := 0
	if err := (Config{Settings: Settings{FoldQuiet: &never}}).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(paths.ConfigFile())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "fold-quiet: 0") {
		t.Fatalf("an explicit 0 must be written out, got:\n%s", raw)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Settings.FoldQuiet == nil || *got.Settings.FoldQuiet != 0 {
		t.Fatalf("fold-quiet should round-trip as 0, got %+v", got.Settings.FoldQuiet)
	}
	if err := (Config{}).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got, _ := Load(); got.Settings.FoldQuiet != nil {
		t.Fatal("an unset fold-quiet should load as nil so the default applies")
	}
}
