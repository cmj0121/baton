package client

import (
	"encoding/json"
	"net"
	"os"
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

func TestOutputRoutedSeparately(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "o.sock")
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
		_ = json.NewDecoder(conn).Decode(new(proto.Command)) // hello
		enc := json.NewEncoder(conn)
		_ = enc.Encode(proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion})
		_ = enc.Encode(proto.ServerMsg{Type: "output", ID: "1", Data: []byte("hi")})
		time.Sleep(50 * time.Millisecond)
	}()

	c, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	if got := <-c.Events; got.Type != "welcome" {
		t.Fatalf("welcome should arrive on Events, got %+v", got)
	}
	select {
	case got := <-c.Output:
		if got.Type != "output" || string(got.Data) != "hi" {
			t.Fatalf("unexpected output message: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("output never arrived on the Output channel")
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

// TestPingIsIgnored confirms a "ping" keepalive never reaches Events: the server
// sends welcome, then a ping, then a notice. The client must surface welcome and
// notice on Events but silently swallow the ping in between.
func TestPingIsIgnored(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "ping.sock")
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
		_ = json.NewDecoder(conn).Decode(new(proto.Command)) // hello
		enc := json.NewEncoder(conn)
		_ = enc.Encode(proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion})
		_ = enc.Encode(proto.ServerMsg{Type: "ping"})
		_ = enc.Encode(proto.ServerMsg{Type: "notice", Notice: "after-ping"})
		time.Sleep(50 * time.Millisecond)
	}()

	c, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	if got := <-c.Events; got.Type != "welcome" {
		t.Fatalf("first event should be welcome, got %+v", got)
	}
	// The very next Events message must be the notice — never the ping. If the ping
	// leaked onto Events, this would receive a "ping" and fail.
	select {
	case got := <-c.Events:
		if got.Type != "notice" || got.Notice != "after-ping" {
			t.Fatalf("ping leaked onto Events; got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notice never arrived after the ping")
	}
}

// TestReadTimeoutClosesChannels confirms readLoop's idle read deadline fires when
// the server says hello and then goes silent: with a millisecond read timeout, no
// heartbeat ever arrives, so the Decode times out and every channel closes.
func TestReadTimeoutClosesChannels(t *testing.T) {
	restore := SetReadTimeout(30 * time.Millisecond)
	defer restore()

	// A short socket path: the per-test temp dir embeds this test's long name, which
	// would push a Unix socket path past the ~104-char limit, so use os.MkdirTemp's
	// shorter root directly.
	dir, err := os.MkdirTemp("", "rt")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// Accept and hold the conn open but never send a thing after the welcome — a
	// stalled server with no heartbeat. The client's read deadline must trip.
	hold := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = json.NewDecoder(conn).Decode(new(proto.Command)) // hello
		_ = json.NewEncoder(conn).Encode(proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion})
		<-hold // keep the conn open so closure is the client's timeout, not the server hanging up
	}()
	defer close(hold)

	c, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Drain until the read deadline fires and Events closes. The welcome arrives
	// first; then silence trips the timeout well within the margin.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-c.Events:
			if !ok {
				return // closed by the read timeout, as expected
			}
		case <-deadline:
			t.Fatal("read timeout never closed the channels")
		}
	}
}
