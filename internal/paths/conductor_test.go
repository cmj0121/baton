package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConductorFile checks the operator-brief path resolves under the home dir.
func TestConductorFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".baton", "CONDUCTOR.md")
	if got := ConductorFile(); got != want {
		t.Fatalf("ConductorFile() = %q, want %q", got, want)
	}
}

// TestNewConductorWorkspace checks it creates a fresh private directory under the
// runtime base.
func TestNewConductorWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", home) // runtimeDir becomes <home>/baton

	ws, err := NewConductorWorkspace()
	if err != nil {
		t.Fatalf("NewConductorWorkspace: %v", err)
	}
	fi, err := os.Stat(ws)
	if err != nil || !fi.IsDir() {
		t.Fatalf("workspace %q is not a directory (err %v)", ws, err)
	}
	if base := filepath.Join(home, "baton"); !strings.HasPrefix(ws, base) {
		t.Fatalf("workspace %q should sit under the runtime base %q", ws, base)
	}

	// A second call yields a distinct directory (MkdirTemp), so two conductors
	// never share a workspace.
	ws2, err := NewConductorWorkspace()
	if err != nil {
		t.Fatalf("second NewConductorWorkspace: %v", err)
	}
	if ws2 == ws {
		t.Fatalf("two workspaces collided: %q", ws)
	}
}
