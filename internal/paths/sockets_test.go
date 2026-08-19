package paths

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSocketsListsTheRuntimeDirNewestFirst pins the order the remote bridge
// depends on: it picks the first socket that answers, so "newest first" is what
// makes that the session still in use rather than one left over from last week.
func TestSocketsListsTheRuntimeDirNewestFirst(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	base := filepath.Join(dir, "baton")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	old := filepath.Join(base, "baton-1.sock")
	recent := filepath.Join(base, "baton-2.sock")
	for _, p := range []string{old, recent} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Files that are not control sockets are not offered to the bridge.
	for _, p := range []string{"baton-1.pid", "baton-1.state.json", "baton.log"} {
		if err := os.WriteFile(filepath.Join(base, p), nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	got := Sockets()
	if len(got) != 2 {
		t.Fatalf("Sockets() = %v, want the two sockets only", got)
	}
	if got[0] != recent || got[1] != old {
		t.Fatalf("Sockets() = %v, want the most recently touched first", got)
	}
}

func TestSocketsIsEmptyWithoutARuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "nothing-here"))
	if got := Sockets(); got != nil {
		t.Fatalf("Sockets() = %v, want nil when there is nothing to list", got)
	}
}

// TestSocketsSkipsAnEntryThatVanished covers the stat race: a daemon may unlink
// its socket between the glob and the stat, and a listing is not the place to
// fail over it.
func TestSocketsSkipsAnEntryThatVanished(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	base := filepath.Join(dir, "baton")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A dangling symlink globs but cannot be stat'd.
	if err := os.Symlink(filepath.Join(base, "gone"), filepath.Join(base, "baton-9.sock")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := Sockets(); len(got) != 0 {
		t.Fatalf("Sockets() = %v, want the unstattable entry skipped", got)
	}
}
