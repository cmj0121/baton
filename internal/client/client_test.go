package client

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// echoServer accepts one connection, reads the client's hello, and replies with
// a welcome message. It returns the listener's socket path.
func echoServer(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		var cmd proto.Command
		if err := json.NewDecoder(conn).Decode(&cmd); err != nil || cmd.Action != "hello" {
			return
		}
		_ = json.NewEncoder(conn).Encode(proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion})
		time.Sleep(50 * time.Millisecond) // keep the conn open briefly
	}()
	return sock
}

func TestDialHandshakeAndEvents(t *testing.T) {
	c, err := Dial(echoServer(t))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.Endpoint() != "local" {
		t.Fatalf("Endpoint() = %q, want local", c.Endpoint())
	}

	select {
	case msg, ok := <-c.Events:
		if !ok {
			t.Fatal("events channel closed before welcome")
		}
		if msg.Type != "welcome" || msg.Version != proto.ProtocolVersion {
			t.Fatalf("unexpected first event: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for welcome")
	}
}

func TestSendAfterDial(t *testing.T) {
	c, err := Dial(echoServer(t))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestEventsCloseOnDisconnect(t *testing.T) {
	c, err := Dial(echoServer(t))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Drain until the server hangs up and the events channel closes.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-c.Events:
			if !ok {
				return // closed as expected
			}
		case <-deadline:
			t.Fatal("events channel never closed after server disconnect")
		}
	}
}

func TestDialUnknownSocketErrors(t *testing.T) {
	if _, err := Dial(filepath.Join(t.TempDir(), "nope.sock")); err == nil {
		t.Fatal("dialing a missing socket should error")
	}
}

func TestEndpointReportsRemoteHost(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		if conn, err := ln.Accept(); err == nil {
			_ = conn.Close()
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial tcp: %v", err)
	}
	defer func() { _ = conn.Close() }()

	c := &Client{conn: conn}
	if got := c.Endpoint(); got != "127.0.0.1" {
		t.Fatalf("Endpoint() = %q, want 127.0.0.1", got)
	}
}
