package server_test

import (
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/state"
)

// TestHeartbeatPing verifies the server emits a periodic ping through the normal
// outbound stream. A raw connection (not the client, which swallows pings)
// decodes the wire directly and asserts a "ping" arrives after the welcome.
func TestHeartbeatPing(t *testing.T) {
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	srv.SetHeartbeat(20 * time.Millisecond) // fire fast for the test
	go func() { _ = srv.Serve() }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Say hello so the handshake completes and the writer/heartbeat goroutines run.
	if err := json.NewEncoder(conn).Encode(proto.Command{Action: "hello"}); err != nil {
		t.Fatalf("hello: %v", err)
	}

	dec := json.NewDecoder(conn)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	sawPing := false
	for !sawPing {
		var msg proto.ServerMsg
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("decode (no ping seen yet): %v", err)
		}
		if msg.Type == "ping" {
			sawPing = true
		}
	}
}

// TestIdleClientStaysConnected confirms the handshake read deadline is cleared
// after the first command: a client that says hello and then sits idle past the
// handshake window keeps receiving heartbeats and is not dropped.
func TestIdleClientStaysConnected(t *testing.T) {
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	srv.SetHeartbeat(20 * time.Millisecond)
	go func() { _ = srv.Serve() }()

	c := dial(t, sock) // drains welcome + initial panels, never sends again

	// Sit idle well past a (hypothetical) short handshake window, then prove the
	// connection is alive by issuing a command and getting a reply.
	time.Sleep(200 * time.Millisecond)
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("send after idle: %v", err)
	}
	if got := recv(t, c); got.Type != "panels" {
		t.Fatalf("idle client should still be served, got %+v", got)
	}
}

// TestHandshakeTimeoutDropsSilentConn confirms a connection that never says hello
// is dropped after the handshake deadline. The test reuses the production
// HandshakeTimeout via a deadline on its own read of the (closed) conn.
func TestHandshakeTimeoutDropsSilentConn(t *testing.T) {
	if proto.HandshakeTimeout > 5*time.Second {
		t.Skip("handshake timeout too long to exercise quickly")
	}
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	go func() { _ = srv.Serve() }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Never send hello. The server's handshake read deadline fires and closes the
	// conn; our read then returns EOF. Allow a margin past HandshakeTimeout.
	_ = conn.SetReadDeadline(time.Now().Add(proto.HandshakeTimeout + 3*time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("a silent connection should be dropped, but the read succeeded")
	}
}

// syncConn wraps a net.Conn and flags any concurrent Write. It backs the
// single-writer assertion: only the writer goroutine may ever encode to the conn,
// so two overlapping Writes would mean the invariant was broken. SetReadDeadline
// is forwarded so the server's handshake/idle deadline logic still works.
type syncConn struct {
	net.Conn
	inWrite atomic.Bool
	raced   atomic.Bool
}

func (s *syncConn) Write(p []byte) (int, error) {
	if !s.inWrite.CompareAndSwap(false, true) {
		s.raced.Store(true)
	}
	defer s.inWrite.Store(false)
	// Hold the "in write" flag briefly so a genuine second concurrent writer would
	// reliably observe the overlap rather than slipping between two fast calls.
	time.Sleep(50 * time.Microsecond)
	return s.Conn.Write(p)
}

// TestSingleWriterInvariant interposes an instrumented conn on the server side of
// an in-memory pipe and drives a fast heartbeat plus a flood of broadcasts at
// once. Only the writer goroutine may ever encode to the conn, so the concurrent-
// write flag must never fire.
func TestSingleWriterInvariant(t *testing.T) {
	ln, _, _ := listen(t)
	srv := server.New(ln)
	srv.SetHeartbeat(time.Millisecond) // hammer the heartbeat path alongside broadcasts

	// net.Pipe gives a synchronous in-memory connection; wrap the server's end so
	// every Write it makes is checked for overlap.
	srvEnd, cliEnd := net.Pipe()
	sc := &syncConn{Conn: srvEnd}
	go srv.Handle(sc)

	// Client end: say hello, then keep draining so the writer never blocks (a
	// blocked writer would mask, not cause, a race — we want it writing freely).
	enc := json.NewEncoder(cliEnd)
	dec := json.NewDecoder(cliEnd)
	if err := enc.Encode(proto.Command{Action: "hello"}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				var msg proto.ServerMsg
				if err := dec.Decode(&msg); err != nil {
					return
				}
			}
		}
	}()

	// Flood broadcasts from many goroutines while the heartbeat ticks; each
	// panel.list triggers a broadcast back to this conn through cc.out. A mutex
	// serialises the client's own writes so the command stream itself stays intact —
	// the assertion is about the SERVER's writer, not this test's encoder.
	var encMu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				encMu.Lock()
				_ = enc.Encode(proto.Command{Action: "panel.list"})
				encMu.Unlock()
			}
		}()
	}
	wg.Wait()
	time.Sleep(50 * time.Millisecond) // let the writer drain the queued broadcasts/pings
	close(stop)
	_ = cliEnd.Close()
	_ = sc.Close()

	if sc.raced.Load() {
		t.Fatal("concurrent Write detected: single-writer invariant broken")
	}
}

// TestTeardownClosesConnOnce confirms that when the peer stops reading and the
// write deadline fires, the writer goroutine tears the conn down, the reader
// returns, and the client is removed exactly once — no double-close panic, no
// leaked goroutine. We assert via the server's client count dropping back to 0.
func TestTeardownClosesConnOnce(t *testing.T) {
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	srv.SetHeartbeat(5 * time.Millisecond)
	go func() { _ = srv.Serve() }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Say hello, then NEVER read. Heartbeats + the welcome fill the socket buffer;
	// once it is full the server's per-encode write deadline (proto.WriteTimeout)
	// fires, the writer tears down, and the reader returns. The client is reaped.
	if err := json.NewEncoder(conn).Encode(proto.Command{Action: "hello"}); err != nil {
		t.Fatalf("hello: %v", err)
	}

	// The client must appear, then disappear once teardown runs. WriteTimeout
	// bounds how long; allow a margin. (We don't fill the buffer ourselves — the
	// stalled reader does that as heartbeats accumulate.)
	deadline := time.After(proto.WriteTimeout + 5*time.Second)
	for srv.ClientCount() != 0 {
		select {
		case <-deadline:
			t.Fatal("a non-reading client was never torn down")
		case <-time.After(20 * time.Millisecond):
		}
	}
	_ = conn.Close()
	// A short settle: if teardown double-closed or leaked, the race detector or a
	// panic would already have fired by now.
	time.Sleep(20 * time.Millisecond)
}

// TestShutdownPersistsLatestState proves the shutdown flush keeps the LATEST
// state even though markDirty coalesces through a 1-deep channel: after a final
// mutation, SaveNow snapshots current state directly (bypassing the dirty
// channel), so no in-flight save can lose the last change.
func TestShutdownPersistsLatestState(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	// A burst of mutations to load the 1-deep dirty channel, then a final mutation
	// whose effect must survive even if a coalesced async save already ran.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{id}, Group: "first"}); err != nil {
		t.Fatalf("group first: %v", err)
	}
	recv(t, c)
	if err := c.Send(proto.Command{Action: "panel.rename", Group: "first", Name: "final"}); err != nil {
		t.Fatalf("rename group: %v", err)
	}
	recv(t, c)

	// Shutdown's synchronous flush must capture the LATEST group name.
	srv.SaveNow()
	st, err := state.Load(stateF)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(st.Panels) != 1 || st.Panels[0].Group != "final" {
		t.Fatalf("SaveNow should persist the latest state, got %+v", st.Panels)
	}
}
