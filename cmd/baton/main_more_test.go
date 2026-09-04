package main

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/config"
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
	go func() { done <- runServerOn(ln, sock, loadServerBoot(sock)) }()

	waitServing(t, sock)

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

// TestABootConfigNobodyCouldReadCarriesOnlyScore is #48's rule at the one seam
// applyConfig cannot reach.
//
// A failed load returns a struct beside its error, and it is not the zero one:
// the decoder fills what it read before it gave up. applyConfig's NEVER-HAD-ONE
// branch throws that away and comes up on the defaults — but usageOption and
// limitsOption are spent when the server is BUILT, off serverBoot.cfg, and
// srv.Reload never revisits them. So a file that failed AFTER its usage section
// booted the daemon polling an endpoint on a cadence against thresholds nobody
// could read, for the life of the process.
//
// The file below is exactly that shape: the whole usage section and both score
// keys decode, and then a number that is not one takes the strict pass down. The
// assertion is the whole struct rather than the usage keys, because the next
// construction-time option read off this config would be the same defect again,
// and this catches it without being rewritten.
//
// Score is asserted PRESENT in the same breath, because that is the deliberate
// exception and a fix that took it out would be a worse bug than the one being
// fixed: the store is already open on score.dir, and a WithScore that disagreed
// with it would leave the daemon reporting a memory it is not using.
func TestABootConfigNobodyCouldReadCarriesOnlyScore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)

	confDir := filepath.Join(home, ".baton")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scoreDir := filepath.Join(home, "memory")
	conf := "usage:\n" +
		"  source: api\n" +
		"  interval: 900\n" +
		"  limits: oauth\n" +
		"  window: 168h\n" +
		"  warn-at: 0.1\n" +
		"  alarm-at: 0.2\n" +
		"score:\n" +
		"  dir: " + scoreDir + "\n" +
		"  enabled: true\n" +
		"queue:\n" +
		"  max: not-a-number\n"
	if err := os.WriteFile(filepath.Join(confDir, "config"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}

	// The file must fail to parse AND have decoded the usage section, or this
	// test is asserting over a case that cannot arise.
	raw, err := config.Load()
	if err == nil {
		t.Fatal("the config parsed; this test needs one that does not")
	}
	if raw.Usage.Source == "" {
		t.Fatalf("the decoder kept nothing from the usage section (%+v); the residual this test is about is gone", raw.Usage)
	}

	sock := filepath.Join(t.TempDir(), "baton.sock")
	t.Setenv("BATON_SOCK", sock)
	boot := loadServerBoot(sock)
	defer boot.release()

	if boot.cfg.Score.Dir != scoreDir || !boot.cfg.Score.IsEnabled() {
		t.Fatalf("boot score = %+v, want the two keys #48 takes from a half-parsed file", boot.cfg.Score)
	}
	rest := boot.cfg
	rest.Score = config.ScoreConfig{}
	if !reflect.DeepEqual(rest, config.Config{}) {
		t.Fatalf("the boot config carries %+v from a file nobody could read, want only score", rest)
	}
}
