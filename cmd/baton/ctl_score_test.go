package main

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/score"
	"github.com/cmj0121/baton/internal/server"
)

// ctlScoredServer is ctlTestServer with a live score store attached, so the
// `baton ctl score` handlers land on a store that answers.
func ctlScoredServer(t *testing.T) string {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	st, err := score.Open(t.TempDir(), score.Policy{})
	if err != nil {
		t.Fatalf("open score store: %v", err)
	}
	sock := filepath.Join(home, "baton.sock")
	t.Setenv("BATON_SOCK", sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// One option carries the store and the config knob together, so a stand-in
	// daemon cannot report a live store as a disabled subsystem.
	state := server.ScoreState{Store: st, Enabled: true}
	go func() { _ = server.New(ln, server.WithScore(state)).Serve() }()
	return sock
}

// TestCtlScoreRuns drives every `baton ctl score` handler against a live
// scored server, and the full entry point once, mirroring what the sibling
// queue handlers are covered for.
func TestCtlScoreRuns(t *testing.T) {
	sock := ctlScoredServer(t)
	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	runs := []interface {
		Run(*control.Client) error
	}{
		ctlScoreSubmit{Text: "operators drain the queue before lunch"},
		// The same observation again: the store folds it, and the handler takes
		// its other branch — the one that tells the operator their note was
		// counted into an entry rather than added as one.
		ctlScoreSubmit{Text: "Operators drain the queue before lunch."},
		ctlScoreList{},
		ctlScoreStatus{},
	}
	for _, r := range runs {
		if err := r.Run(c); err != nil {
			t.Fatalf("%T.Run: %v", r, err)
		}
	}

	// Naming a panel the fleet does not have is REFUSED, not answered with the
	// contextless listing under a different name — the whole reason the reply
	// echoes the context it ranked against.
	if err := (ctlScoreList{Panel: "nosuchpanel"}).Run(c); err == nil {
		t.Fatal("score list for an unknown panel should error, not fall back to a contextless ranking")
	}

	// The kong path takes the optional argument as well as omitting it.
	if code := ctlMain([]string{"score", "list"}); code != 0 {
		t.Fatalf("ctl score list exit = %d, want 0", code)
	}

	// The kong path parses and runs the nested subcommand end to end.
	if code := ctlMain([]string{"score", "status"}); code != 0 {
		t.Fatalf("ctl score status exit = %d, want 0", code)
	}
}

// TestCtlScoreClientErrors closes the client out from under each handler, so
// every Run method's error-return branch is exercised.
func TestCtlScoreClientErrors(t *testing.T) {
	sock := ctlScoredServer(t)
	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()

	runs := []interface {
		Run(*control.Client) error
	}{
		ctlScoreSubmit{Text: "x"},
		ctlScoreList{},
		ctlScoreStatus{},
	}
	for _, r := range runs {
		if err := r.Run(c); err == nil {
			t.Errorf("%T.Run on a closed client should error", r)
		}
	}
}
