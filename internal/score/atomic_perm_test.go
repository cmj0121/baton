package score

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateAtomicForcesPermOnReusedTemp is the score store's copy of
// paths.TestWriteFileAtomicForcesPermOnReusedTemp, and it is a separate test
// because createAtomic is a separate implementation: the steps are spelled again
// here to keep this package off internal/paths (see the type's comment), so a fix
// to one does not reach the other.
//
// What it pins: the perm argument to os.OpenFile applies only when the open
// CREATES the file. score.md holds the fleet's memory — the text of everything
// the fleet has been told to remember — and is written at 0600 for that reason,
// so a temp file that was already there handing its own mode to the rename is the
// store going readable. The fixed ".tmp" name is safe against baton's OWN writers
// (the directory lock orders them) but says nothing about a file already on disk.
func TestCreateAtomicForcesPermOnReusedTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "score.md")
	tmp := target + tempSuffix

	if err := os.WriteFile(tmp, []byte("stale"), 0o666); err != nil {
		t.Fatalf("plant leftover temp: %v", err)
	}
	// chmod, not the WriteFile mode, because that one is subject to the caller's
	// umask; chmod is not. This keeps the test off process-global state.
	if err := os.Chmod(tmp, 0o666); err != nil {
		t.Fatalf("widen leftover temp: %v", err)
	}
	if fi, err := os.Stat(tmp); err != nil {
		t.Fatalf("stat leftover: %v", err)
	} else if fi.Mode().Perm() != 0o666 {
		t.Fatalf("leftover is %04o, want 0666 for this test to mean anything", fi.Mode().Perm())
	}

	if err := writeFileAtomic(target, []byte("a remembered fact\n"), 0o600); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("score.md is %04o, want 0600: the reused temp's mode reached the "+
			"store, so the fleet's memory is readable by %04o", got, got&0o077)
	}
}
