package server

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// drained is what a client's queue holds, without blocking on an empty one.
func drained(cc *clientConn) []proto.ServerMsg {
	var out []proto.ServerMsg
	for {
		select {
		case msg := <-cc.out:
			out = append(out, msg)
		default:
			return out
		}
	}
}

// TestTheFanOutsWaitForTheHandshake pins the rule at the unit, for each fan-out
// that has to follow it.
//
// broadcast and pushRemote are the daemon's two UNSOLICITED fan-outs: nothing
// the receiving connection asked for, sent to everyone. The output and
// ephemeral-exit loops beside them are gated on cc.attached, which only a
// command from that connection can set, so they cannot reach a client that has
// said nothing.
//
// pushRemote is the one that made this visible rather than theoretical: addClient
// calls it, so accepting a connection pushed a frame to every client including
// the one being accepted — arriving before its own welcome, every time, with no
// race needed at all.
func TestTheFanOutsWaitForTheHandshake(t *testing.T) {
	for _, tc := range []struct {
		name string
		fan  func(*Server)
	}{
		{"broadcast", func(s *Server) { s.broadcast(proto.ServerMsg{Type: "panels"}) }},
		{"pushRemote", func(s *Server) { s.pushRemote() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quiet := &clientConn{out: make(chan proto.ServerMsg, 8)}
			ready := &clientConn{out: make(chan proto.ServerMsg, 8), greeted: true}
			s := &Server{clients: map[*clientConn]struct{}{quiet: {}, ready: {}}}

			tc.fan(s)

			if got := drained(quiet); len(got) != 0 {
				t.Errorf("a connection mid-handshake was sent %d frame(s) before its welcome: %+v", len(got), got)
			}
			if got := drained(ready); len(got) != 1 {
				t.Errorf("a greeted connection should still hear it, got %d frame(s)", len(got))
			}

			// …and it hears everything from the moment it has been greeted, so the
			// gate is a wait rather than a mute.
			quiet.greeted = true
			tc.fan(s)
			if got := drained(quiet); len(got) != 1 {
				t.Errorf("after the handshake it should hear the fan-out, got %d frame(s)", len(got))
			}
		})
	}
}
