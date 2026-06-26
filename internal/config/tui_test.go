package config

import (
	"os"
	"testing"

	"github.com/cmj0121/baton/internal/paths"
)

// TestLoadTUIMissing: no TUI.yaml yields a zero config and no error, so the
// built-in theme and preset layouts apply on a first run.
func TestLoadTUIMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := LoadTUI()
	if err != nil {
		t.Fatalf("LoadTUI on a missing file: %v", err)
	}
	if got.Theme != (Theme{}) || len(got.Layouts) != 0 || got.DefaultLayout != "" {
		t.Fatalf("missing TUI.yaml should be the zero TUIConfig, got %+v", got)
	}
}

// TestLoadTUIParses: a populated TUI.yaml round-trips the theme tokens, the
// default layout, and a custom areas grid.
func TestLoadTUIParses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.TUIConfigFile()); err != nil {
		t.Fatal(err)
	}
	yaml := `
theme:
  brand: "33"
  attention: "#ff0000"
default-layout: review
layouts:
  - name: review
    rows: 2
    areas:
      - [diff, diff, log]
      - [diff, diff, sh]
`
	if err := os.WriteFile(paths.TUIConfigFile(), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadTUI()
	if err != nil {
		t.Fatalf("LoadTUI: %v", err)
	}
	if got.Theme.Brand != "33" || got.Theme.Attention != "#ff0000" {
		t.Fatalf("theme tokens not parsed: %+v", got.Theme)
	}
	if got.DefaultLayout != "review" {
		t.Fatalf("default-layout = %q, want review", got.DefaultLayout)
	}
	if len(got.Layouts) != 1 || got.Layouts[0].Name != "review" || got.Layouts[0].Rows != 2 {
		t.Fatalf("layout not parsed: %+v", got.Layouts)
	}
	if got.Layouts[0].Areas[0][0] != "diff" || got.Layouts[0].Areas[1][2] != "sh" {
		t.Fatalf("areas grid not parsed: %+v", got.Layouts[0].Areas)
	}
}

// TestLoadTUIMalformed: a syntactically broken file is a surfaced error, not a
// silent empty config.
func TestLoadTUIMalformed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.TUIConfigFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.TUIConfigFile(), []byte("theme: [unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTUI(); err == nil {
		t.Fatal("malformed TUI.yaml should error")
	}
}
