package server_test

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// recv pulls the next server message or fails on timeout.
func recv(t *testing.T, c *client.Client) proto.ServerMsg {
	t.Helper()
	select {
	case msg, ok := <-c.Events:
		if !ok {
			t.Fatal("event channel closed unexpectedly")
		}
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server message")
		return proto.ServerMsg{}
	}
}

func TestAttachAndCreateShellPanel(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	srv := server.New(ln)
	go func() { _ = srv.Serve() }()

	// Attach: handshake yields a welcome and an (empty) panel snapshot.
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	const base = 0 // the server starts with no panels
	if got := recv(t, c); got.Type != "welcome" || got.Version != proto.ProtocolVersion {
		t.Fatalf("expected welcome %s, got %+v", proto.ProtocolVersion, got)
	}
	if got := recv(t, c); got.Type != "panels" || len(got.Panels) != base {
		t.Fatalf("expected an empty panels snapshot, got %+v", got)
	}

	// Create a shell panel; the server broadcasts the updated snapshot.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := recv(t, c)
	if got.Type != "panels" || len(got.Panels) != base+1 {
		t.Fatalf("expected %d panels after create, got %+v", base+1, got)
	}
	created := got.Panels[len(got.Panels)-1]
	if created.Kind != "shell" || created.ID == "" {
		t.Fatalf("unexpected created panel %+v", created)
	}

	// Create with an explicit command path; its basename names the panel.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell", Path: "/bin/sh"}); err != nil {
		t.Fatalf("send create with path: %v", err)
	}
	got = recv(t, c)
	withPath := got.Panels[len(got.Panels)-1]
	if !strings.HasPrefix(withPath.Title, "sh #") {
		t.Fatalf("explicit command should name the panel by basename, got %q", withPath.Title)
	}
	// Close it again to return to base+1 for the close assertion below.
	if err := c.Send(proto.Command{Action: "panel.close", ID: withPath.ID}); err != nil {
		t.Fatalf("send close: %v", err)
	}
	recv(t, c)

	// Close it again; the server broadcasts the panel gone.
	if err := c.Send(proto.Command{Action: "panel.close", ID: created.ID}); err != nil {
		t.Fatalf("send close: %v", err)
	}
	got = recv(t, c)
	if got.Type != "panels" || len(got.Panels) != base {
		t.Fatalf("expected %d panels after close, got %+v", base, got)
	}
	for _, p := range got.Panels {
		if p.ID == created.ID {
			t.Fatalf("closed panel %s still present", created.ID)
		}
	}

	// Re-attach a second client: it should see the persisted fleet immediately.
	c2, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("re-attach dial: %v", err)
	}
	defer func() { _ = c2.Close() }()
	if got := recv(t, c2); got.Type != "welcome" {
		t.Fatalf("expected welcome on re-attach, got %+v", got)
	}
	if got := recv(t, c2); got.Type != "panels" || len(got.Panels) != base {
		t.Fatalf("re-attached client should see %d panels, got %+v", base, got)
	}
}

func TestExitMarks(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	// Make the shell exit on its own; the server should mark the panel exited
	// and broadcast it.
	if err := c.Send(proto.Command{Action: "panel.input", ID: id, Data: []byte("exit\n")}); err != nil {
		t.Fatalf("input: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-c.Events:
			if msg.Type == "panels" {
				for _, p := range msg.Panels {
					if p.ID == id && p.State == "exited" {
						return // detected
					}
				}
			}
		case <-deadline:
			t.Fatal("panel was not marked exited after the shell exited")
		}
	}
}

func TestAttachIO(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	// Spawn a shell and grab its id.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	// Attach and type a command; its output streams back as "output" messages.
	if err := c.Send(proto.Command{Action: "panel.attach", ID: id}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := c.Send(proto.Command{Action: "panel.input", ID: id, Data: []byte("echo baton-xyz\n")}); err != nil {
		t.Fatalf("input: %v", err)
	}

	deadline := time.After(3 * time.Second)
	found := false
	for !found {
		select {
		case msg := <-c.Output: // the client routes "output" to its own channel
			if msg.ID == id && strings.Contains(string(msg.Data), "baton-xyz") {
				found = true
			}
		case <-deadline:
			t.Fatal("expected echoed output for the attached panel")
		}
	}

	// Detach stops the stream.
	if err := c.Send(proto.Command{Action: "panel.detach", ID: id}); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if err := c.Send(proto.Command{Action: "panel.close", ID: id}); err != nil {
		t.Fatalf("close: %v", err)
	}
}
