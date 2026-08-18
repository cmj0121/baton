package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestClaimSessionIsExclusive: one backend per session. The second claim is told
// it lost rather than handed an error, because losing is the ordinary outcome
// when several cockpits start at once.
func TestClaimSessionIsExclusive(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "baton.lock")

	release, held, err := claimSession(lock)
	if err != nil || !held {
		t.Fatalf("first claim = held %v, err %v; want held", held, err)
	}

	if _, held, err := claimSession(lock); err != nil || held {
		t.Fatalf("second claim = held %v, err %v; want lost without an error", held, err)
	}

	release()
	release2, held, err := claimSession(lock)
	if err != nil || !held {
		t.Fatalf("claim after release = held %v, err %v; want held", held, err)
	}
	release2()
}

// TestClaimSessionUnwritablePath: a lock baton cannot even open is an error, not
// a silent "you won" — starting a second backend is worse than not starting one.
func TestClaimSessionUnwritablePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if _, held, err := claimSession(filepath.Join(dir, "baton.lock")); err == nil || held {
		t.Fatalf("claim in a missing dir = held %v, err %v; want an error", held, err)
	}
}

// TestClaimSessionLeavesTheLockFile: the lock file is a lock and nothing else.
// Removing it would let the next daemon lock a fresh inode and believe it won,
// which is exactly the race the lock exists to close.
func TestClaimSessionLeavesTheLockFile(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "baton.lock")
	release, _, err := claimSession(lock)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("the lock file should outlive the claim: %v", err)
	}
}
