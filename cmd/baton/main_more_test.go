package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/paths"
)

// TestRunServerEnsureDirError covers runServer's early failure when the socket's
// parent directory cannot be created: a regular file stands where the runtime
// dir should be, so paths.EnsureDir fails before the listener is ever bound.
func TestRunServerEnsureDirError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)

	blocker := filepath.Join(home, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BATON_SOCK", filepath.Join(blocker, "baton.sock"))

	if err := runServer(); err == nil {
		t.Fatal("runServer should fail when the socket dir cannot be created")
	}
}

// TestRunServerStaleLiveSocket covers runServer's refusal to start when a live
// server already holds the socket: clearStaleSocket sees a reachable socket and
// returns an error rather than clobbering it.
func TestRunServerStaleLiveSocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	sock := filepath.Join(home, "baton.sock")
	t.Setenv("BATON_SOCK", sock)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if err := runServer(); err == nil {
		t.Fatal("runServer should refuse a live socket")
	}
}

// TestAttachForceStopError covers attach's force branch when the stop fails: a
// live daemon holds the socket but its PID file is unparseable, so stopDaemon
// returns an error and attach propagates it before starting a fresh daemon.
func TestAttachForceStopError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	sock := filepath.Join(home, "baton.sock")
	t.Setenv("BATON_SOCK", sock)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if err := os.WriteFile(paths.PidFile(sock), []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := attach(0, filepath.Join(home, "baton.log"), "", true); err == nil {
		t.Fatal("attach should fail when force-stop cannot signal the live daemon")
	}
}

// TestRunServerOnBadConfigFiles drives the server loop with malformed config,
// plugin, and TUI files under $HOME/.baton, exercising the warn-and-continue
// error branches of runServerOn/applyConfig (config.Load, plugin Load, and
// LoadTUI all fail) without stopping the server. The listener is closed to make
// Serve return on its own, as in TestRunServerOn.
func TestRunServerOnBadConfigFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)

	confDir := filepath.Join(home, ".baton")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Malformed YAML config and TUI files, and a Lua plugin with a syntax error,
	// so every load inside applyConfig fails and takes its warn branch.
	if err := os.WriteFile(filepath.Join(confDir, "config"), []byte("{ this is : not valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "TUI.yaml"), []byte("{ also : not : valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "plug-in.lua"), []byte("this is (((not lua"), 0o644); err != nil {
		t.Fatal(err)
	}

	sock := filepath.Join(t.TempDir(), "baton.sock")
	t.Setenv("BATON_SOCK", sock)
	// A fresh plugin path resolves under HOME; make sure no external override leaks.
	t.Setenv("BATON_PLUGIN", "")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- runServerOn(ln, sock) }()

	pidPath := paths.PidFile(sock)
	if !waitFor(func() bool { _, err := os.Stat(pidPath); return err == nil }, 100, 10*time.Millisecond) {
		t.Fatal("server did not write its pid file")
	}

	_ = ln.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServerOn returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runServerOn did not return after the listener closed")
	}
}
