package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wantLogPerm is spelled as a LITERAL rather than taken from logrotate.go's
// logPerm on purpose: a test that asserts against the constant it is guarding
// passes for any value that constant is given, which is the one mutation this
// file exists to catch.
const wantLogPerm os.FileMode = 0o600

// TestDaemonLogIsOwnerOnly pins the mode of the file EVERY baton process appends
// to. It is the sibling of TestWritePidFilePerms, and the log needs the guard far
// more than the pid file does: a pid is a number, while this file carries content
// the daemon is handed. Two lines put it beyond doubt —
//
//   - internal/server/score.go logs Str("entry", f.Text), the verbatim text of a
//     fleet-memory entry, at INFO, which is the level setupLogger selects when no
//     -v is given. Every fold of the score store writes one.
//   - internal/server/server.go logs Str("prompt", d.prompt), the verbatim text of
//     a task brief, whenever a task.pre hook vetoes a delivery.
//
// Both are 0600 where they live (score.md is written through createAtomic at
// 0600), so 0644 here republished at a wider mode exactly the content the store
// keeps private. The 0700 parent directory gates the default path, but --log=FILE
// puts this file wherever an operator points it, and the house posture is that
// the inner layer holds too — see paths.SecureSocket, which clamps a socket
// already sitting behind that same 0700 directory.
//
// It exercises openLogRotator rather than setupLogger deliberately. setupLogger
// replaces the PROCESS-GLOBAL zerolog logger, which the score and reload tests in
// this package capture; calling it here stole their output and raced them. The
// rotator is the creation site the mode actually lives on, so nothing is lost.
func TestDaemonLogIsOwnerOnly(t *testing.T) {
	// No umask is set here, and none is needed: umask only ever CLEARS bits, so a
	// correct 0600 is the same under every umask, and the 0644 this guards against
	// survives every umask that would leave the bits exposed in the first place.
	// Forcing one would mean mutating process-global state shared with every other
	// test in this binary.
	path := filepath.Join(t.TempDir(), "baton.log")
	sink, err := openLogRotator(path, logRotateAtBytes)
	if err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != wantLogPerm {
		t.Errorf("fresh daemon log is %04o, want %04o: readable by group/other (%04o)",
			perm, wantLogPerm, perm&0o077)
	}

	// The rotation opens a NEW file, so it is a second creation site with a mode of
	// its own; a fix applied only to the open above would leave the log wide again
	// the first time it filled up.
	if _, err := sink.Write([]byte(strings.Repeat("x", 64))); err != nil {
		t.Fatalf("write: %v", err)
	}
	sink.max = 1 // the next write crosses the cap and rolls
	if _, err := sink.Write([]byte("roll\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, p := range []string{path, path + rotatedLogSuffix} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s after rotation: %v", filepath.Base(p), err)
		}
		if perm := fi.Mode().Perm(); perm != wantLogPerm {
			t.Errorf("%s after rotation is %04o, want %04o", filepath.Base(p), perm, wantLogPerm)
		}
	}
}

// TestRotationLockIsOwnerOnly pins the one mode in logrotate.go that was already
// right, so the fix above does not make the file look uniform by accident.
func TestRotationLockIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baton.log")
	release, err := lockRotation(path + rotateLockSuffix)
	if err != nil {
		t.Fatalf("lockRotation: %v", err)
	}
	defer release()

	fi, err := os.Stat(path + rotateLockSuffix)
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != wantLogPerm {
		t.Errorf("rotation lock is %04o, want %04o", perm, wantLogPerm)
	}
}
