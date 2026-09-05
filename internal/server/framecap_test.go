package server_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// wireFrameCap is maxCommandBytes as the wire sees it. Restated here rather than
// exported: the tests below drive the socket the way a peer does, and a peer does
// not get to read the daemon's constants.
const wireFrameCap = 1 << 20

// rawDial opens a raw control connection and greets, returning the conn and a
// reader over the server's replies. Raw rather than client.Dial because these
// tests send frames no client type would build.
func rawDial(t *testing.T, sock string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := json.NewEncoder(conn).Encode(proto.Command{Action: "hello"}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	return conn, bufio.NewReader(conn)
}

// readUntil pulls server messages until one of type want arrives, and fails if
// the connection ends or the deadline passes first.
func readUntil(t *testing.T, r *bufio.Reader, conn net.Conn, want string) proto.ServerMsg {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("waiting for %q: %v", want, err)
		}
		var msg proto.ServerMsg
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("decode reply: %v", err)
		}
		if msg.Type == want {
			return msg
		}
	}
}

// TestOversizedCommandFrameIsRefused drives one command line past the cap and
// checks the daemon drops that connection unanswered while still serving the
// fleet's other clients.
//
// The input is the real one: 8 MiB of prompt in a single frame on a live socket.
// Unbounded, the same frame at 256 MiB took the daemon's heap to 449 MiB in under
// half a second and the connection stayed up — which is the harm this cap is
// about, one agent's connection being the whole fleet's memory.
//
// What is asserted is what a peer can rely on seeing: the flood is never answered
// and the connection ends. The daemon does queue a goodbye naming the limit, but
// it is best-effort by construction — see the command loop — because a peer still
// shovelling bytes at a socket the daemon has just closed has its queued inbound
// data discarded by the kernel. So the reason lands in the daemon's log, where it
// is the operator's to read, and the test does not assert a race.
func TestOversizedCommandFrameIsRefused(t *testing.T) {
	ln, sock, _ := listen(t)
	serve(t, server.New(ln))

	victim, vr := rawDial(t, sock)
	readUntil(t, vr, victim, "welcome")

	flood, fr := rawDial(t, sock)
	readUntil(t, fr, flood, "welcome")

	// Written in pieces so the test never holds the whole frame either.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = flood.Write([]byte(`{"action":"panel.dispatch","id":"flooded-frame","prompt":"`))
		chunk := []byte(strings.Repeat("A", 1<<16))
		for range (8 << 20) >> 16 {
			if _, err := flood.Write(chunk); err != nil {
				return
			}
		}
		_, _ = flood.Write([]byte("\"}\n"))
	}()

	// The flooding connection ends without its command ever being run.
	_ = flood.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		line, err := fr.ReadBytes('\n')
		if err != nil {
			break // the connection went, which is the refusal
		}
		var msg proto.ServerMsg
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("decode reply: %v", err)
		}
		if msg.Type == "error" && strings.Contains(msg.Error, "flooded-frame") {
			t.Fatal("the oversized frame reached the dispatch; it should never have been decoded")
		}
	}
	<-done

	// …and the daemon is still there for everyone else, which is the whole point.
	if err := json.NewEncoder(victim).Encode(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("second client send: %v", err)
	}
	readUntil(t, vr, victim, "panels")
}

// TestLargeButLegalCommandFrameIsAccepted is the other half: a frame just under
// the cap has to go through. A cap that fires in normal use is worse than none.
func TestLargeButLegalCommandFrameIsAccepted(t *testing.T) {
	ln, sock, _ := listen(t)
	serve(t, server.New(ln))

	conn, r := rawDial(t, sock)
	readUntil(t, r, conn, "welcome")

	// A dispatch at a prompt length that leaves the whole frame a few hundred
	// bytes short of the cap. It targets no panel, so the daemon answers "error" —
	// which is the proof the frame was DECODED rather than refused unread.
	cmd := proto.Command{Action: "panel.dispatch", ID: "no-such-panel", Prompt: strings.Repeat("A", wireFrameCap-1024)}
	if err := json.NewEncoder(conn).Encode(cmd); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := readUntil(t, r, conn, "error")
	if !strings.Contains(msg.Error, "no-such-panel") {
		t.Fatalf("expected the frame to reach the dispatch, got %q", msg.Error)
	}
}

// TestFrameCapIsPerFrameNotPerConnection sends far more than the cap in total,
// one ordinary command at a time, and checks the connection survives it.
//
// This is the test that kills a budget which is never restored: drop the
// limiter's reset and the connection dies partway through the run.
func TestFrameCapIsPerFrameNotPerConnection(t *testing.T) {
	ln, sock, _ := listen(t)
	serve(t, server.New(ln))

	conn, r := rawDial(t, sock)
	readUntil(t, r, conn, "welcome")

	enc := json.NewEncoder(conn)
	// 32 frames of a quarter-megabyte: 8 MiB in total, eight times the per-frame cap.
	const frames = 32
	for i := range frames {
		cmd := proto.Command{Action: "panel.dispatch", ID: fmt.Sprintf("gone-%d", i), Prompt: strings.Repeat("B", 1<<18)}
		if err := enc.Encode(cmd); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	for i := range frames {
		msg := readUntil(t, r, conn, "error")
		if !strings.Contains(msg.Error, fmt.Sprintf("gone-%d", i)) {
			t.Fatalf("frame %d answered out of order: %q", i, msg.Error)
		}
	}
}
