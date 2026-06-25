package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/server"
)

// ctlTestServer starts an in-process server on a private socket and points the
// control client at it via BATON_SOCK.
func ctlTestServer(t *testing.T) string {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	sock := filepath.Join(home, "baton.sock")
	t.Setenv("BATON_SOCK", sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = server.New(ln).Serve() }()
	return sock
}

// TestCtlMain drives the `baton ctl` entry point for a couple of subcommands.
func TestCtlMain(t *testing.T) {
	ctlTestServer(t)
	if code := ctlMain([]string{"spawn"}); code != 0 {
		t.Fatalf("ctl spawn exit = %d, want 0", code)
	}
	if code := ctlMain([]string{"list"}); code != 0 {
		t.Fatalf("ctl list exit = %d, want 0", code)
	}
}

// TestCtlRuns exercises every ctl subcommand handler against a live server.
func TestCtlRuns(t *testing.T) {
	sock := ctlTestServer(t)
	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	id, err := c.SpawnPanel("", nil, "")
	if err != nil {
		t.Fatalf("seed spawn: %v", err)
	}

	runs := []interface {
		Run(*control.Client) error
	}{
		ctlList{},
		ctlSpawn{Agent: "/bin/cat", Dir: t.TempDir()},
		ctlGroup{Name: "g", IDs: []string{id}},
		ctlRename{ID: id, Name: "renamed"},
		ctlPin{IDs: []string{id}},
		ctlUnpin{IDs: []string{id}},
		ctlSend{ID: id, Text: "hi"},
		ctlSend{ID: id, Text: "x", NoEnter: true},
		ctlSignal{Signal: "SIGTERM", IDs: []string{id}},
		ctlClose{IDs: []string{id}},
	}
	for _, r := range runs {
		if err := r.Run(c); err != nil {
			t.Fatalf("%T.Run: %v", r, err)
		}
	}
}

// TestMcpMain runs `baton mcp` with an immediately-closed stdin, so the server
// loop reads EOF and returns cleanly.
func TestMcpMain(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	_ = w.Close() // EOF on the first read
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; _ = r.Close() }()

	if code := mcpMain(); code != 0 {
		t.Fatalf("mcpMain exit = %d, want 0", code)
	}
}
