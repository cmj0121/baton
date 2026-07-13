package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestQueueDirPairsWithSocket mirrors StateFile/PidFile: the backlog dir is
// derived from the socket path by swapping the ".sock" suffix.
func TestQueueDirPairsWithSocket(t *testing.T) {
	cases := map[string]string{
		"/run/baton/baton-42.sock": "/run/baton/baton-42.queue",
		"/tmp/x.sock":              "/tmp/x.queue",
		"/tmp/nosuffix":            "/tmp/nosuffix.queue",
	}
	for sock, want := range cases {
		if got := QueueDir(sock); got != want {
			t.Errorf("QueueDir(%q) = %q, want %q", sock, got, want)
		}
	}
}

// TestTUIConfigFile resolves the cockpit appearance file under $HOME/.baton.
func TestTUIConfigFile(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	if got := TUIConfigFile(); got != "/home/tester/.baton/TUI.yaml" {
		t.Fatalf("TUIConfigFile() = %q, want $HOME/.baton/TUI.yaml", got)
	}
}

// TestSecureSocketReturnsRealChmodError covers the SecureSocket branch where
// os.Chmod fails with an error that is NOT "not exist": a path whose parent is a
// regular file yields ENOTDIR, which must be surfaced rather than swallowed.
func TestSecureSocketReturnsRealChmodError(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "afile")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// afile is a regular file, so treating it as a directory component fails with
	// ENOTDIR — not a not-exist error — and SecureSocket must return it.
	bad := filepath.Join(notADir, "s.sock")
	if err := SecureSocket(bad); err == nil {
		t.Fatal("SecureSocket should return the underlying chmod error (ENOTDIR)")
	} else if os.IsNotExist(err) {
		t.Fatalf("SecureSocket returned a not-exist error %v; wanted a real error", err)
	}
}

// TestNewConductorWorkspaceMkdirFails covers the MkdirAll failure branch: when
// the runtime base cannot be created (its parent is a regular file) the helper
// returns an error and an empty path rather than a workspace.
func TestNewConductorWorkspaceMkdirFails(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "afile")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// runtimeDir() becomes <notADir>/baton; MkdirAll under a regular file fails.
	t.Setenv("XDG_RUNTIME_DIR", notADir)

	ws, err := NewConductorWorkspace()
	if err == nil {
		t.Fatalf("NewConductorWorkspace should fail when the base cannot be created; got %q", ws)
	}
	if ws != "" {
		t.Errorf("workspace path = %q, want empty string on failure", ws)
	}
}

// TestWriteFileAtomicWriteFails covers the f.Write failure branch and the
// stale-temp cleanup that follows it. WriteFileAtomic opens "path+.tmp"; by
// pre-creating that temp name as a symlink to /dev/full, the O_WRONLY open
// succeeds but every write fails with ENOSPC. This device exists only on Linux
// (which is where codecov's CI runs), so the test skips elsewhere.
func TestWriteFileAtomicWriteFails(t *testing.T) {
	const devFull = "/dev/full"
	if _, err := os.Stat(devFull); err != nil {
		t.Skipf("no %s on this platform; write-failure branch not exercised", devFull)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "x.dat")
	// The helper opens path+".tmp"; route that name at /dev/full via a symlink so
	// the write — not the open — is what fails.
	if err := os.Symlink(devFull, path+".tmp"); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("data"), 0o600); err == nil {
		t.Fatal("WriteFileAtomic should fail when writes to the temp file fail (ENOSPC)")
	}
}

// TestWriteFileAtomicParentSyncTolerated confirms the durability fsync of the
// parent directory is best-effort: a normal successful write returns nil and the
// data is readable, exercising the dir.Open success branch.
func TestWriteFileAtomicParentSyncTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "x.dat")
	if err := EnsureDir(path); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("durable"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "durable" {
		t.Errorf("content = %q, want %q", got, "durable")
	}
}

// TestSessionIDPositive pins that sessionID resolves to a positive integer via
// the getsid path (the common case), so Socket carries a stable per-session id.
func TestSessionIDPositive(t *testing.T) {
	if id := sessionID(); id <= 0 {
		t.Fatalf("sessionID() = %d, want a positive session/ppid", id)
	}
}

// TestSocketCarriesSessionID ties Socket's default path to sessionID so the
// socket name is unique per login session.
func TestSocketCarriesSessionID(t *testing.T) {
	t.Setenv("BATON_SOCK", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/7")
	got := Socket()
	if !strings.Contains(got, "/baton/baton-") || !strings.HasSuffix(got, ".sock") {
		t.Fatalf("Socket() = %q, want a /baton/baton-<id>.sock name", got)
	}
}
