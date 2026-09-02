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

// sessionClaimed is one question asked through a fresh probe — the shape
// stopUnboundDaemon's opening check has, and the one a test that changes the
// state between questions needs. The daemon's WAIT holds a single probe across
// all of its questions instead; see sessionProbe.
func sessionClaimed(path string) bool {
	p := openSessionProbe(path)
	defer p.close()
	return p.claimed()
}

// TestSessionClaimedTracksTheHolder pins the probe stopUnboundDaemon trusts a
// pid on, in both directions: it must answer true for exactly as long as a
// daemon holds the session, and false the moment one does not.
//
// The false direction is the one that matters. A PID file outlives the process
// it names, and pids get reused, so a check that only asked "is this number a
// live process" would hand a SIGTERM to whatever unrelated program inherited the
// number. This is the check that stops that, and the released case below is the
// same on-disk state as a daemon that crashed: the lock file is still there, and
// nothing holds it.
func TestSessionClaimedTracksTheHolder(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "baton.lock")

	if sessionClaimed(lock) {
		t.Fatal("a session whose lock file does not exist cannot be claimed")
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("the probe must not create the lock file it asks about: %v", err)
	}

	release, held, err := claimSession(lock)
	if err != nil || !held {
		t.Fatalf("claim = held %v, err %v; want held", held, err)
	}
	if !sessionClaimed(lock) {
		t.Fatal("a session a live daemon holds should read as claimed")
	}

	release()
	if sessionClaimed(lock) {
		t.Fatal("a released session should read as free, with the lock file still on disk")
	}
}
