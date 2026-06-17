package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/paths"
)

func TestSetupLogger(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "logs", "baton.log")
	for _, v := range []int{0, 1, 2} {
		if err := setupLogger(v, logPath); err != nil {
			t.Fatalf("setupLogger(%d): %v", v, err)
		}
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log file not created: %v", err)
	}

	// A log path under a regular file cannot have its directory created.
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setupLogger(0, filepath.Join(bad, "nested.log")); err == nil {
		t.Fatal("setupLogger should fail when the log dir cannot be created")
	}
}

func TestAlive(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "baton.sock")
	if alive(sock) {
		t.Fatal("a missing socket should not be alive")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if !alive(sock) {
		t.Fatal("a listening socket should be alive")
	}
}

func TestClearStaleSocket(t *testing.T) {
	dir := t.TempDir()

	// Missing socket: nothing to do.
	if err := clearStaleSocket(filepath.Join(dir, "missing.sock")); err != nil {
		t.Fatalf("missing socket: %v", err)
	}

	// A leftover (dead) socket file is removed, along with its orphaned PID file
	// (a SIGKILLed daemon leaves both behind).
	stale := filepath.Join(dir, "stale.sock")
	stalePid := paths.PidFile(stale)
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stalePid, []byte("99999"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearStaleSocket(stale); err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale socket should have been removed")
	}
	if _, err := os.Stat(stalePid); !os.IsNotExist(err) {
		t.Fatal("orphaned PID file should have been removed")
	}

	// A live socket is refused.
	live := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err := clearStaleSocket(live); err == nil {
		t.Fatal("clearStaleSocket should refuse a live socket")
	}
}

func TestWaitFor(t *testing.T) {
	if !waitFor(func() bool { return true }, 3, time.Millisecond) {
		t.Fatal("waitFor should succeed when the condition holds")
	}
	calls := 0
	if waitFor(func() bool { calls++; return false }, 3, time.Millisecond) {
		t.Fatal("waitFor should fail when the condition never holds")
	}
	if calls < 3 {
		t.Fatalf("expected at least 3 polls, got %d", calls)
	}
}

func TestStopDaemon(t *testing.T) {
	dir := t.TempDir()

	// No server alive: a no-op (and tidies a stale socket).
	if err := stopDaemon(filepath.Join(dir, "none.sock")); err != nil {
		t.Fatalf("stopDaemon with no server: %v", err)
	}

	// Alive but no PID file: cannot locate the daemon.
	sock := filepath.Join(dir, "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err := stopDaemon(sock); err == nil {
		t.Fatal("stopDaemon should fail without a PID file")
	}

	// Alive with a non-numeric PID file: parse error.
	if err := os.WriteFile(paths.PidFile(sock), []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stopDaemon(sock); err == nil {
		t.Fatal("stopDaemon should fail on an unparseable PID")
	}

	// Alive with a PID that cannot be signalled (already-reaped): signal error.
	if err := os.WriteFile(paths.PidFile(sock), []byte(strconv.Itoa(1<<30)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stopDaemon(sock); err == nil {
		t.Fatal("stopDaemon should fail signalling a non-existent PID")
	}
}
