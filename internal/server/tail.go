package server

import (
	"github.com/cmj0121/baton/internal/proto"
)

// The pull side of the inbox's detail pane.
//
// The queue's whole promise is that a human can see WHY a panel wants them
// without opening it, and the only honest answer to "why" is the bytes the
// Monitor read when it raised the flag. Shipping those bytes on every snapshot
// would put fifty tails on a broadcast that already goes out on every state
// change, for a pane that shows exactly one of them — so the cockpit asks for
// the one row it is looking at, and nothing else travels.
//
// There is deliberately no second implementation of the read. sendTail calls
// ptymgr.Manager.Tail with the Monitor's own default byte count, so "what the
// inbox shows" and "what raised the flag" cannot drift apart by a refactor.

// maxTailBytes caps what one panel.tail may pull.
//
// This is a per-row, per-keystroke door: walking a thirty-row queue opens it
// thirty times, and a cockpit is free to ask for any Count it likes. fleet.search
// is the deliberately wide read in this protocol, bounded by a query; this one is
// bounded by a number, and without a cap it would quietly become a way to drag
// fifty whole replay rings through the inbox one arrow key at a time.
const maxTailBytes = 8192

// sendTail replies with the last Count bytes of a panel's retained output.
//
// Count is clamped to [1, maxTailBytes], and zero — which is what an older
// client, or a cockpit with no opinion, sends — means attnTailBytes: the exact
// window looksLikeAttention read on the tick that decided this panel needed a
// human.
//
// An unknown id replies with empty Data rather than an error, and that is the
// point rather than laziness. A row can be reaped between the keystroke that
// selected it and the read that serves it — a panel closed from another cockpit,
// an exited slot pruned — and an error popup for a race the human did not cause
// is noise. The empty pane says the same thing more quietly, and the next
// snapshot takes the row away.
func (s *Server) sendTail(cc *clientConn, id string, count int) {
	n := count
	if n <= 0 {
		n = attnTailBytes
	}
	n = min(n, maxTailBytes)
	send(cc, proto.ServerMsg{Type: "tail", ID: id, Data: s.pty.Tail(id, n)})
}
