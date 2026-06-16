package server_test

import (
	"net"
	"path/filepath"
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

	if got := recv(t, c); got.Type != "welcome" || got.Version != proto.ProtocolVersion {
		t.Fatalf("expected welcome %s, got %+v", proto.ProtocolVersion, got)
	}
	if got := recv(t, c); got.Type != "panels" || len(got.Panels) != 0 {
		t.Fatalf("expected empty panels snapshot, got %+v", got)
	}

	// Create a shell panel; the server broadcasts the updated snapshot.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := recv(t, c)
	if got.Type != "panels" || len(got.Panels) != 1 {
		t.Fatalf("expected one panel after create, got %+v", got)
	}
	if p := got.Panels[0]; p.Kind != "shell" || p.ID == "" {
		t.Fatalf("unexpected panel %+v", p)
	}

	// Re-attach a second client: it should see the existing panel immediately.
	c2, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("re-attach dial: %v", err)
	}
	defer func() { _ = c2.Close() }()
	if got := recv(t, c2); got.Type != "welcome" {
		t.Fatalf("expected welcome on re-attach, got %+v", got)
	}
	if got := recv(t, c2); got.Type != "panels" || len(got.Panels) != 1 {
		t.Fatalf("re-attached client should see 1 panel, got %+v", got)
	}
}
