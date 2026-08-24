package paths

import (
	"os"
	"path/filepath"
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

// TestConductorWorkspaceMkdirFails covers the MkdirAll failure branch: when the
// temporary base cannot be created (its parent is a regular file) the helper
// returns an error and an empty path rather than a workspace.
func TestConductorWorkspaceMkdirFails(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "afile")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// conductorBase() becomes <notADir>/baton; MkdirAll under a regular file fails.
	t.Setenv("XDG_RUNTIME_DIR", notADir)

	ws, err := ConductorWorkspace("/run/baton.sock")
	if err == nil {
		t.Fatalf("ConductorWorkspace should fail when the base cannot be created; got %q", ws)
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

// TestSocketIsOneFixedPathPerUser pins the whole point of the socket name: it
// carries no session id, so `baton` launched from any terminal resolves the same
// path and attaches to the one backend instead of starting a second one.
func TestSocketIsOneFixedPathPerUser(t *testing.T) {
	t.Setenv("BATON_SOCK", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/7")
	if got, want := Socket(), "/run/user/7/baton/baton.sock"; got != want {
		t.Fatalf("Socket() = %q, want %q", got, want)
	}
}
