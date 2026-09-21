package server

import (
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// attachonce_test.go covers the attach half of #133: the replay a zoom opens on
// must say what size it was painted at, and a chunk that races the attach must
// reach the screen once.
//
// The race cannot be won on purpose by timing. A pump appends a chunk to the
// ring under the manager lock and only THEN calls into the server, where it
// queues on s.mu behind an attach that is snapshotting that very ring — so the
// chunk is in the replay and, a moment later, live output too. The test builds
// that order by hand: the pump has no sink, the chunk is certainly in the ring,
// and its late delivery is made after the attach.

// sinkless is a server whose PTY output lands in the ring and nowhere else: the
// pump never reaches fanOutput, so each test delivers "live" chunks by hand.
func sinkless() *Server {
	s := New(nil)
	s.pty.OnOutput(func(string, []byte, int64) {})
	return s
}

// holdOutput starts a panel on a sinkless server and waits until mark is in its
// ring. It returns the offset the ring ends at.
func holdOutput(t *testing.T, s *Server, id, mark string) int64 {
	t.Helper()
	if err := s.pty.StartCmd(id, ptymgr.Spec{Command: "/bin/sh", Args: []string{"-c", "printf " + mark + "; sleep 5"}}); err != nil {
		t.Fatalf("StartCmd: %v", err)
	}
	t.Cleanup(func() { s.pty.Stop(id) })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r := s.pty.SnapshotAt(id); strings.Contains(string(r.Data), mark) {
			return r.End
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%q never reached the ring", mark)
	return 0
}

// queued returns every message queued to cc.
func queued(cc *clientConn) []proto.ServerMsg {
	var got []proto.ServerMsg
	for {
		select {
		case msg := <-cc.out:
			got = append(got, msg)
		default:
			return got
		}
	}
}

// TestAChunkRacingTheAttachIsDeliveredOnce is cause 2 of #133. Painted twice, a
// relative repaint (cursor-up, erase, redraw) lands a second time from wherever
// the first left the cursor, and the screen keeps the difference as stale text.
func TestAChunkRacingTheAttachIsDeliveredOnce(t *testing.T) {
	s := sinkless()
	end := holdOutput(t, s, "p", "RACED")

	cc := &clientConn{out: make(chan proto.ServerMsg, 8), attached: map[string]bool{}}
	s.clients[cc] = struct{}{}
	s.attach(cc, "p")
	s.fanOutput("p", []byte("RACED"), end)          // the chunk the replay already holds, arriving late
	s.fanOutput("p", []byte("AFTER"), end+int64(5)) // the first chunk the replay does not hold

	var all strings.Builder
	for _, msg := range queued(cc) {
		all.Write(msg.Data)
	}
	if n := strings.Count(all.String(), "RACED"); n != 1 {
		t.Errorf("the racing chunk reached the client %d times, want once: %q", n, all.String())
	}
	if !strings.Contains(all.String(), "AFTER") {
		t.Errorf("output past the replay must still be delivered: %q", all.String())
	}
}

// TestTheReplayCarriesItsPaintedSize pins the tag the cockpit replays at, and
// that the tag rides only the replay: live output is painted at whatever size
// the client itself is, so a size on it would be a lie.
func TestTheReplayCarriesItsPaintedSize(t *testing.T) {
	s := sinkless()
	end := holdOutput(t, s, "p", "SIZED")

	cc := &clientConn{out: make(chan proto.ServerMsg, 8), attached: map[string]bool{}}
	s.clients[cc] = struct{}{}
	s.attach(cc, "p")
	s.fanOutput("p", []byte("live"), end+4)

	got := queued(cc)
	if len(got) != 2 {
		t.Fatalf("want the replay and one live chunk, got %d messages", len(got))
	}
	if got[0].Rows != 24 || got[0].Cols != 80 {
		t.Errorf("the replay of a never-sized panel is tagged %dx%d, want its birth size 24x80", got[0].Rows, got[0].Cols)
	}
	if got[1].Rows != 0 || got[1].Cols != 0 {
		t.Errorf("live output is tagged %dx%d; only the replay carries a size", got[1].Rows, got[1].Cols)
	}
}

// TestDetachForgetsTheReplayOffset keeps the per-client record from outliving
// the stream it describes — for one panel and for the detach-all a zoom sends.
func TestDetachForgetsTheReplayOffset(t *testing.T) {
	s := sinkless()
	holdOutput(t, s, "p", "ONE")
	holdOutput(t, s, "q", "TWO")

	cc := &clientConn{out: make(chan proto.ServerMsg, 8), attached: map[string]bool{}}
	s.attach(cc, "p")
	s.attach(cc, "q")
	s.detach(cc, "p")
	if _, ok := cc.replayed["p"]; ok {
		t.Error("detaching a panel left its replay offset behind")
	}
	s.detach(cc, "")
	if len(cc.replayed) != 0 {
		t.Errorf("detach-all left %d replay offsets behind", len(cc.replayed))
	}
}

// TestADroppedOutputIsCountedAndSaid is cause 3: a full client queue used to
// swallow output without a trace, leaving a screen that no longer matched the
// program and nothing anywhere saying so.
func TestADroppedOutputIsCountedAndSaid(t *testing.T) {
	s := New(nil)
	logged := captureLog(t)

	cc := &clientConn{out: make(chan proto.ServerMsg, 1), attached: map[string]bool{"p": true}}
	s.clients[cc] = struct{}{}
	for i := range 5 {
		s.fanOutput("p", []byte("x"), int64(i+1))
	}
	if cc.dropped != 4 {
		t.Errorf("counted %d dropped outputs, want 4 (one fit the queue)", cc.dropped)
	}
	if line := logged(); strings.Count(line, "output dropped") != 1 {
		t.Errorf("want exactly one paced line for a burst of drops, got:\n%s", line)
	}
}
