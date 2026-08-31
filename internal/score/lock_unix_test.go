//go:build unix

package score

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSingleWriterPerDirectory is scope item 7 (#40, Page's note): two daemons
// on two sockets both default to $HOME/.baton, and an unenforced "run only one"
// would let their snapshots clobber each other silently. The claim is taken at
// Open and released at Close, so the second daemon is told plainly instead of
// corrupting the first one's view.
func TestSingleWriterPerDirectory(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	second, err := Open(dir)
	if err == nil {
		second.Close()
		t.Fatal("a second store opened the same directory; the claim is not enforced")
	}
	if !strings.Contains(err.Error(), "another baton daemon") {
		t.Errorf("error = %v, want it to name the conflict plainly", err)
	}

	// Close hands the directory over, which is what makes a restart work.
	first.Close()
	third, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after Close: %v", err)
	}
	third.Close()
	third.Close() // idempotent
}

// TestOpenReleasesTheClaimWhenItFails checks the failed-boot path: a store that
// could not read its own log must not leave the directory claimed, or the next
// start would be refused for a reason that no longer exists.
func TestOpenReleasesTheClaimWhenItFails(t *testing.T) {
	dir := t.TempDir()
	// The event log as a directory: replay's ReadFile fails with something other
	// than "not exist", so Open returns an error rather than an empty store.
	if err := os.Mkdir(filepath.Join(dir, scoreEvents), 0o700); err != nil {
		t.Fatalf("mkdir over the log: %v", err)
	}

	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatal("Open succeeded with an unreadable event log")
	}
	s, err := Open(dir)
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded on the second try, so the first failure was not the log")
	}
	if strings.Contains(err.Error(), "another baton daemon") {
		t.Fatal("a failed Open kept the directory claimed")
	}
}
