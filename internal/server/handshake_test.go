package server_test

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// TestNothingReachesAConnectionBeforeItsWelcome is #121, driven over a real
// socket and without a race to win.
//
// A connection joins the daemon's client set when it is ACCEPTED, not when its
// hello is handled, so there is a window in which a broadcast from any other
// goroutine can be queued onto it. Here the window is held open deliberately:
// the second connection is dialled and then says nothing, another client
// creates a panel — which broadcasts a fleet snapshot to everyone — and only
// afterwards does the silent connection greet.
//
// Every client drains the handshake POSITIONALLY: welcome first, then the
// panels snapshot. So the assertion is on the FIRST frame's type. One early
// broadcast pushes that by one and the connection reads a message behind for the
// rest of its life, which is why this cannot be written as "eventually a welcome
// arrives".
//
// The mutation that kills it: drop the greeted check from broadcast. The first
// frame becomes the panels snapshot the other client's spawn produced.
func TestNothingReachesAConnectionBeforeItsWelcome(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	serve(t, srv)

	// The silent connection: accepted and registered, but not greeted.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)

	// Something the whole fleet hears about, while it is still mid-handshake.
	other := dial(t, sock)
	if err := other.Send(proto.Command{Action: "panel.create"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := recv(t, other); got.Type != "panels" {
		t.Fatalf("the spawn should have broadcast a snapshot, got %q", got.Type)
	}

	// Now greet. The welcome must be the first thing this connection has been
	// handed, not the third.
	if err := json.NewEncoder(conn).Encode(proto.Command{Action: "hello"}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var first proto.ServerMsg
	if err := json.Unmarshal([]byte(line), &first); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	if first.Type != "welcome" {
		t.Errorf("the first frame was %q, not the welcome — this connection now reads a message behind forever", first.Type)
	}

	// And the handshake's second frame is still the snapshot it is supposed to be,
	// carrying the panel that was created while this connection was deaf. Nothing
	// is lost by waiting, and that is what makes dropping the early frame right
	// rather than merely quiet.
	line, err = br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var second proto.ServerMsg
	if err := json.Unmarshal([]byte(line), &second); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	if second.Type != "panels" {
		t.Fatalf("the second frame was %q, want the snapshot", second.Type)
	}
	if len(second.Panels) != 1 {
		t.Errorf("the handshake snapshot should carry the panel created meanwhile, got %d", len(second.Panels))
	}
}
