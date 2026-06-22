package state

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// TestSaveParentMissing covers the OpenFile error branch: the temp file's parent
// directory does not exist, so Save cannot create the temp file and must return
// an error.
func TestSaveParentMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "x.state.json")
	if err := sample().Save(path); err == nil {
		t.Fatal("Save into a missing parent dir returned nil, want an error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Save left a file at %q despite the error", path)
	}
}

// TestSaveUnwritableDirPreservesGoodFile covers the failed-write path on a
// read-only directory and asserts it does not corrupt the existing good file:
// the prior snapshot must still load intact.
func TestSaveUnwritableDirPreservesGoodFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions do not gate writes on this platform")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory write permissions")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "x.state.json")

	// Write a known-good snapshot first.
	good := sample()
	if err := good.Save(path); err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	// Make the directory read-only so the temp-file create fails, and restore
	// perms in cleanup so t.TempDir can remove the tree.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// A second Save must fail (cannot create the sibling temp file).
	updated := sample()
	updated.Seq = 999
	if err := updated.Save(path); err == nil {
		t.Fatal("Save into a read-only dir returned nil, want an error")
	}

	// Restore perms now so we can read back and confirm the good file survived.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restore chmod: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load after failed Save: %v", err)
	}
	if !reflect.DeepEqual(got, good) {
		t.Fatalf("failed Save corrupted the existing file:\n got: %+v\nwant: %+v", got, good)
	}
}

// TestSaveOverExistingTmp exercises the temp+rename path when a stale .tmp
// sibling already exists: O_TRUNC must let Save reuse it and still produce a
// correct final file with no leftover temp.
func TestSaveOverExistingTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.state.json")
	if err := os.WriteFile(path+".tmp", []byte("stale leftover"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := sample().Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, sample()) {
		t.Fatalf("round-trip mismatch after reusing stale tmp:\n got: %+v\nwant: %+v", got, sample())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("leftover .tmp after successful Save")
	}
}

// TestSaveRenameFailsWhenPathIsDir covers the rename error branch: the temp
// file is written successfully, but os.Rename cannot replace a non-empty
// directory at the destination, so Save returns an error and cleans up the temp.
func TestSaveRenameFailsWhenPathIsDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.state.json")

	// Make the destination a non-empty directory so rename-over-it fails.
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := sample().Save(path); err == nil {
		t.Fatal("Save returned nil when destination is a non-empty dir, want an error")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("Save left its temp file behind after the rename failure")
	}
}

// TestLoadIOErrorNotMissing covers Load's non-IsNotExist read-error branch:
// reading a path that is a directory fails with an error that is not
// "file missing", so Load returns an empty State and that error.
func TestLoadIOErrorNotMissing(t *testing.T) {
	dir := t.TempDir()
	got, err := Load(dir) // a directory, not a regular file
	if err == nil {
		t.Fatal("Load of a directory returned nil error, want a read error")
	}
	if !reflect.DeepEqual(got, State{Schema: Schema}) {
		t.Fatalf("Load I/O error: got %+v, want empty State", got)
	}
}

// TestSaveZeroValueDurablePath drives Save with a zero State so both the Schema
// and SavedAt defaulting branches run, and confirms the parent-dir fsync best-
// effort path completes (a real, openable directory) without error.
func TestSaveZeroValueDurablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zero.state.json")
	var s State
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Schema != Schema {
		t.Errorf("Schema = %d, want %d", got.Schema, Schema)
	}
	if got.SavedAt.IsZero() {
		t.Error("SavedAt is zero, want it defaulted by Save")
	}
}
