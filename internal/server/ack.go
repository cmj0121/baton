package server

import (
	"fmt"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// Acknowledgement: the record that a human has already dealt with a panel from
// the inbox — dismissed it, snoozed it, or answered it.
//
// It is FLEET state rather than per-cockpit state, and that is the decision this
// file exists to hold. The queue's promise is "here is where the fleet needs a
// human"; a second cockpit re-offering work the first one just cleared is exactly
// the untrustworthy queue the feature was built to fix. `baton --remote` reattaches
// are routine enough that losing an afternoon's clearing work on a reconnect would
// be a daily cost, and a cockpit restart must not resurrect a queue somebody
// already emptied.
//
// The consequence is stated rather than hidden: two cockpits share one cleared
// queue, and so therefore do two people. baton is one daemon, one host, one
// uid-private socket, one session, so that is the right default — but it is a
// promise about WHOSE queue it is, and it is the most reversible thing in this
// design (one map, one wire bool).
//
// Nothing here is persisted. A daemon restart brings every panel back as an inert
// exited slot, so carrying an acknowledgement across it would suppress a row for a
// panel that no longer exists in the same sense.

// ackPanel handles panel.ack: a human dealt with this panel from the inbox.
//
// An empty Until means "until the panel next produces output" — the dismiss. A
// set Until is the snooze, and it is an absolute instant rather than a duration
// because the COCKPIT owns the policy: two cockpits configured with different
// settings.inbox-snooze each get what they configured, and the daemon holds no
// per-client preference at all.
//
// An unknown id is a silent no-op, for the same reason panel.tail answers one with
// empty bytes: a row can be reaped between the keystroke that cleared it and the
// command that carries it, and an error popup for a race the human did not cause
// is noise. An unparseable Until is NOT silent, because the failure mode there is
// the opposite — a snooze that quietly became a permanent dismiss is a row the
// human will never see again and never asked to lose.
func (s *Server) ackPanel(cc *clientConn, cmd proto.Command) {
	var until time.Time
	if cmd.Until != "" {
		t, err := time.Parse(time.RFC3339Nano, cmd.Until)
		if err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: fmt.Sprintf("panel.ack: bad until %q: %v", cmd.Until, err)})
			return
		}
		until = t
	}

	s.mu.Lock()
	if s.indexLocked(cmd.ID) < 0 {
		s.mu.Unlock()
		return
	}
	if s.acked == nil {
		s.acked = make(map[string]time.Time)
	}
	s.acked[cmd.ID] = until
	s.mu.Unlock()

	// A plain broadcast, not broadcastFleet: an acknowledgement is a live opinion
	// about a live process and is deliberately not persisted, so there is nothing
	// here to mark dirty.
	s.broadcast(s.panelsMsg())
}

// ackedLocked reports whether an acknowledgement currently stands for a panel.
// Caller holds s.mu.
//
// Expiry is evaluated HERE, where the value is read, rather than by a sweeper.
// A snooze is only ever asked about when a snapshot is built, so a background
// goroutine ticking over the map would do work nobody was waiting for — and the
// entry is dropped for real on the edges that already exist: the panel speaking,
// exiting, being closed, or being pruned.
func (s *Server) ackedLocked(id string) bool {
	until, ok := s.acked[id]
	switch {
	case !ok:
		return false
	case until.IsZero():
		return true // a dismiss: it stands until the panel speaks
	default:
		return s.mon.now().Before(until)
	}
}
