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

func TestWriteFileAtomicRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.dat")
	want := []byte("hello atomic\n")
	if err := WriteFileAtomic(path, want, 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// TestWriteFileAtomicNoLeftoverTmp confirms the happy path leaves no sibling
// ".tmp" file behind.
func TestWriteFileAtomicNoLeftoverTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.dat")
	if err := WriteFileAtomic(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// TestWriteFileAtomicTempOpenFails covers the create-temp failure: a directory
// where the ".tmp" file must go makes OpenFile fail regardless of privilege.
func TestWriteFileAtomicTempOpenFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.dat")
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("data"), 0o600); err == nil {
		t.Fatal("WriteFileAtomic should fail when the temp file cannot be created")
	}
}

// TestWriteFileAtomicRenameFails covers the write/sync/close success then a rename
// failure: a directory at the final path makes os.Rename fail, and the temp file
// must be cleaned up rather than left behind.
func TestWriteFileAtomicRenameFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.dat")
	if err := os.Mkdir(path, 0o700); err != nil { // final path is a directory
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("data"), 0o600); err == nil {
		t.Fatal("WriteFileAtomic should fail when the destination is a directory")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file should be removed after a failed rename")
	}
}

// TestHomeFallsBackToEnv checks home() resolves via $HOME when UserHomeDir relies
// on it, so baton's files anchor to an absolute path rather than a relative one.
func TestHomeFallsBackToEnv(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	if got := home(); got != "/home/tester" {
		t.Errorf("home() = %q, want /home/tester", got)
	}
}
