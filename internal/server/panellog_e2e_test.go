package server_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// TestPanelLoggingEndToEnd drives the whole feature over the real socket with a
// real PTY: spawn a panel, make it produce output, switch logging on, and check
// that the file holds what the panel had ALREADY printed (the replay flush) as
// well as what it printed after — in plain text, on the daemon's disk.
//
// It is deliberately an end-to-end test rather than another unit one. Every
// interesting thing about logging is a join between parts that are unit-tested
// separately: the ring the daemon snapshots, the strip, the file, and the wire
// field the badge reads. A test that stubs any of those cannot tell you the
// feature works.
func TestPanelLoggingEndToEnd(t *testing.T) {
	logs := t.TempDir()
	sock := filepath.Join(shortDir(t), "b.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	srv := server.New(ln, server.WithLogging(logs, nil, nil, 0))
	serve(t, srv)

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // the empty fleet

	// A panel that prints a line and then waits, so there is something in the
	// replay ring before logging is switched on. It is created as an agent kind
	// because that is the kind that carries arguments (a shell panel is launched
	// bare, by design).
	if err := c.Send(proto.Command{Action: "panel.create", Kind: proto.KindAgent, Path: "/bin/sh", Args: []string{"-c", "printf 'before the keypress\\n'; cat"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	snap := recv(t, c)
	if len(snap.Panels) != 1 {
		t.Fatalf("fleet has %d panels; want 1", len(snap.Panels))
	}
	id := snap.Panels[0].ID

	// Give the shell a moment to actually print, so the ring is not empty.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.Send(proto.Command{Action: "panel.tail", ID: id}); err != nil {
			t.Fatalf("tail: %v", err)
		}
		if msg := waitFor(t, c, "tail"); strings.Contains(string(msg.Data), "before the keypress") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := c.Send(proto.Command{Action: "panel.log", ID: id}); err != nil {
		t.Fatalf("panel.log: %v", err)
	}
	logging := waitForLogging(t, c, id, true)
	if logging.LogPath == "" {
		t.Fatalf("the snapshot should name the file: %+v", logging)
	}
	if !strings.HasPrefix(logging.LogPath, logs) {
		t.Errorf("log path %q is not under the configured dir %q", logging.LogPath, logs)
	}

	// Live output after the switch must land in the same file. The panel is a
	// `cat`, so anything written to it comes straight back out.
	if err := c.Send(proto.Command{Action: "panel.input", ID: id, Data: []byte("after the keypress\r")}); err != nil {
		t.Fatalf("input: %v", err)
	}
	body := waitForFile(t, logging.LogPath, "after the keypress")
	for _, want := range []string{"=== baton log ·", "logging started", "replay buffer:", "before the keypress", "live output follows", "after the keypress"} {
		if !strings.Contains(body, want) {
			t.Errorf("log is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "\x1b") {
		t.Errorf("escape sequences reached the file:\n%q", body)
	}

	// The second press stops it, and the file says so.
	if err := c.Send(proto.Command{Action: "panel.log", ID: id}); err != nil {
		t.Fatalf("panel.log off: %v", err)
	}
	waitForLogging(t, c, id, false)
	if got := readFile(t, logging.LogPath); !strings.Contains(got, "logging stopped") {
		t.Errorf("the log should close with a marker saying why:\n%s", got)
	}
}

// TestPanelLoggingRefusedWithoutADir checks the off-by-default shape over the
// wire: with no destination configured the key is answered with an error naming
// the config key, and nothing is written anywhere.
func TestPanelLoggingRefusedWithoutADir(t *testing.T) {
	sock := filepath.Join(shortDir(t), "b.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	srv := server.New(ln) // no WithLogging: the default
	serve(t, srv)

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c)
	recv(t, c)

	if err := c.Send(proto.Command{Action: "panel.create", Kind: proto.KindAgent, Path: "/bin/sh", Args: []string{"-c", "sleep 30"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	snap := recv(t, c)
	id := snap.Panels[0].ID

	if err := c.Send(proto.Command{Action: "panel.log", ID: id}); err != nil {
		t.Fatalf("panel.log: %v", err)
	}
	msg := waitFor(t, c, "error")
	if !strings.Contains(msg.Error, "log-dir") {
		t.Errorf("the refusal should name the key to set, got %q", msg.Error)
	}
}

// waitFor drains server messages until one of the given type arrives.
func waitFor(t *testing.T, c *client.Client, typ string) proto.ServerMsg {
	t.Helper()
	for i := 0; i < 200; i++ {
		msg := recv(t, c)
		if msg.Type == typ {
			return msg
		}
	}
	t.Fatalf("no %q message arrived", typ)
	return proto.ServerMsg{}
}

// waitForLogging drains snapshots until the named panel reports the wanted
// logging state, and returns it.
func waitForLogging(t *testing.T, c *client.Client, id string, want bool) proto.Panel {
	t.Helper()
	for i := 0; i < 200; i++ {
		msg := recv(t, c)
		if msg.Type == "error" {
			t.Fatalf("server error while waiting for the logging flag: %s", msg.Error)
		}
		if msg.Type != "panels" {
			continue
		}
		for _, p := range msg.Panels {
			if p.ID == id && p.Logging == want {
				return p
			}
		}
	}
	t.Fatalf("panel %s never reported logging=%v", id, want)
	return proto.Panel{}
}

// waitForFile polls a log until it holds want, so the test does not race the
// daemon's write. It returns the contents.
func waitForFile(t *testing.T, path, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		body := readFile(t, path)
		if strings.Contains(body, want) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never held %q:\n%s", path, want, body)
			return body
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// shortDir is a temp root with a SHORT name: macOS caps a unix socket path near
// 104 bytes and t.TempDir() embeds the test's own name, so a descriptively named
// test would otherwise fail to bind.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
