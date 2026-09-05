package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteFileAtomicForcesPermOnReusedTemp demonstrates the mode-leak fix: the
// mode argument to os.OpenFile applies ONLY when the open creates the file, so a
// temp file that is already there keeps whatever mode it already had, and the
// rename carries that mode onto the target. Every caller of WriteFileAtomic in
// this tree asks for 0600 (config, state, queue, worktree, tui, the boot stamp),
// so a target that comes out group- or world-readable is the store contents of a
// fleet leaking to other users on the host.
//
// The temp path is derived from the target (path + ".tmp"), so it is PREDICTABLE
// rather than unguessable — a process killed mid-write leaves one behind under
// exactly the name the next write will reuse, and under a world-writable parent
// it is a name another user can create first.
func TestWriteFileAtomicForcesPermOnReusedTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	tmp := target + ".tmp"

	// The leftover: a temp file under the name WriteFileAtomic will reuse, carrying
	// a mode no store in this tree would ever ask for.
	if err := os.WriteFile(tmp, []byte("stale"), 0o666); err != nil {
		t.Fatalf("plant leftover temp: %v", err)
	}
	// chmod, not the WriteFile mode, because that one is subject to the caller's
	// umask; chmod is not. This keeps the test off process-global state.
	if err := os.Chmod(tmp, 0o666); err != nil {
		t.Fatalf("widen leftover temp: %v", err)
	}

	// Precondition: the leftover really is world-readable, or the assertion below
	// would pass for the wrong reason.
	if fi, err := os.Stat(tmp); err != nil {
		t.Fatalf("stat leftover: %v", err)
	} else if fi.Mode().Perm() != 0o666 {
		t.Fatalf("leftover temp is %04o, want 0666 for this test to mean anything", fi.Mode().Perm())
	}

	if err := WriteFileAtomic(target, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("target mode is %04o, want 0600: the reused temp file's mode "+
			"reached the target, so the store is readable by %04o", got, got&0o077)
	}
}
