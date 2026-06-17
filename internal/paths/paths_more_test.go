package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketUsesEnvOverride(t *testing.T) {
	t.Setenv("BATON_SOCK", "/custom/baton.sock")
	if got := Socket(); got != "/custom/baton.sock" {
		t.Fatalf("Socket() = %q, want the BATON_SOCK override", got)
	}
}

func TestSocketUsesXDGRuntimeDir(t *testing.T) {
	t.Setenv("BATON_SOCK", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/42")
	got := Socket()
	if !strings.HasPrefix(got, "/run/user/42/baton/baton-") || !strings.HasSuffix(got, ".sock") {
		t.Fatalf("Socket() = %q, want it under XDG_RUNTIME_DIR/baton", got)
	}
}

func TestSocketFallsBackToHome(t *testing.T) {
	t.Setenv("BATON_SOCK", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", "/home/tester")
	got := Socket()
	if !strings.HasPrefix(got, "/home/tester/.baton/baton-") {
		t.Fatalf("Socket() = %q, want it under $HOME/.baton", got)
	}
}

func TestLogAndConfigPaths(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	if LogFile() != "/home/tester/.baton/baton.log" {
		t.Errorf("LogFile() = %q", LogFile())
	}
	if ConfigFile() != "/home/tester/.baton/config" {
		t.Errorf("ConfigFile() = %q", ConfigFile())
	}
}

func TestEnsureDirCreatesParent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "nested", "deep", "thing.sock")
	if err := EnsureDir(file); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	info, err := os.Stat(filepath.Dir(file))
	if err != nil || !info.IsDir() {
		t.Fatalf("parent dir not created: %v", err)
	}
}
