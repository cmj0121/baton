package main

import (
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestADaemonThatReturnedNoLongerReloadsOnSIGHUP is the teardown half of the
// signal wiring: what runServerOn registers, it has to give back when its loop
// ends.
//
// One process runs one daemon, so in production the registration and the
// process end together and nothing here is visible. The test binary is the other
// case: it drives runServerOn directly, a dozen times over, and a handler that
// outlives its loop leaves every past daemon subscribed to the next SIGHUP. They
// all reload — on a server whose listener is closed and a store nobody is
// dispatching against — and each writes its own `config reloaded on SIGHUP` into
// the one log the tests capture. That is not merely noise: a test that sends a
// HUP and waits for that line can be answered by a daemon that returned long
// before its config was even written, which is how a green suite turned red on
// CI alone (run 34946636857) with nothing in the tree changed.
//
// The assertion is the absence of a line, so the signal has to be proven
// delivered rather than assumed: the test subscribes a channel of its own,
// waits to receive on it, and only then holds the log to account. That channel
// is also what keeps the test alive — with no handler registered at all, a
// SIGHUP's default disposition is to kill the process.
func TestADaemonThatReturnedNoLongerReloadsOnSIGHUP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("BATON_PLUGIN", "")
	writeScoreConfig(t, home, filepath.Join(home, "memory"))

	mine := make(chan os.Signal, 1)
	signal.Notify(mine, syscall.SIGHUP)
	t.Cleanup(func() { signal.Stop(mine) })

	sock := filepath.Join(shortDir(t), "b.sock")
	t.Setenv("BATON_SOCK", sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	logged := captureBootLog(t)
	done := make(chan error, 1)
	go func() { done <- runServerOn(ln, sock, loadServerBoot(sock)) }()
	waitServing(t, sock)

	// The daemon ends the way every other in-process one does: its listener is
	// closed and the loop returns. Whatever it still holds after this line, it
	// holds having been shut down.
	_ = ln.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServerOn returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runServerOn did not return after the listener closed")
	}

	mark := len(logged())
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	select {
	case <-mine:
	case <-time.After(3 * time.Second):
		t.Fatal("the SIGHUP was never delivered, so this test proved nothing about the handler")
	}

	after := func() string { return logged()[mark:] }
	if waitFor(func() bool { return strings.Contains(after(), "config reloaded on SIGHUP") },
		50, 10*time.Millisecond) {
		t.Errorf("a daemon whose loop had already returned reloaded on a later SIGHUP:\n%s", after())
	}
}
