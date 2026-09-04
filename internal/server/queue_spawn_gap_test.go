package server

import (
	"encoding/json"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/task"
)

// TestARefusedConductorCannotSpawnThroughTheBacklog is #75's acceptance, and it
// is deliberately the assertion that can fail rather than a reading of a guard
// function: a real conductor connection, a real refusal, a real scheduler tick,
// and the fleet counted before and after.
//
// task.enqueue is the third road to createPanel. It paid the fleet ceiling —
// spelled at scheduleLocked rather than shared with the conductor's — and no rate
// gap at all, so a conductor told `spawning too fast, slow down` could turn round
// and have the scheduler make it a panel in the same instant. The gap it had just
// been refused by was the one thing the other road did not have.
//
// The enqueue itself is still admitted, and that is the design rather than an
// omission: the conductor is not spawning when it queues, and a rate cap there
// would meter an actor that spawned nothing. What must not happen is the PANEL,
// and that is what this counts.
func TestARefusedConductorCannotSpawnThroughTheBacklog(t *testing.T) {
	s := newHostServer(t)
	send, until := conductorWire(t, s)

	// One spawn on the synchronous road, admitted: it spends the door's slot.
	send(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	if msg := until("panels", "error"); msg.Type != "panels" {
		t.Fatalf("the first spawn should be admitted, got %q", msg.Error)
	}
	// And the next one, an instant later, is refused by the gap.
	send(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	if msg := until("panels", "error"); msg.Type != "error" || !strings.Contains(msg.Error, "too fast") {
		t.Fatalf("the second spawn should be rate-refused, got a %q (%q)", msg.Type, msg.Error)
	}

	// The other road, in the same instant. The queueing is allowed through.
	send(proto.Command{
		Action: "task.enqueue", Prompt: "build it",
		Path: "/bin/sh", Args: []string{"-c", "sleep 30"},
	})
	if msg := until("panels", "error"); msg.Type == "error" {
		t.Fatalf("queueing work is not spawning and must not be refused: %q", msg.Error)
	}

	before := s.PanelCount()
	s.monitorTick() // the scheduler's own tick, which is what actually spawns
	if got := s.PanelCount(); got != before {
		t.Fatalf("the fleet grew from %d to %d panels on the tick after a conductor was told to "+
			"slow down: the backlog is a door out of the rate gap", before, got)
	}

	// Past the gap, the queued task provisions its agent as it always did.
	// Nothing was refused here — only deferred, which is what a backlog is for.
	rewind(s, &s.spawn, 2*minConductorSpawnGap)
	s.monitorTick()
	if got := s.PanelCount(); got != before+1 {
		t.Fatalf("the fleet is %d panels a full gap later, want %d: the backlog is not draining "+
			"at all, which is a refusal wearing a deferral's name", got, before+1)
	}
}

// TestASpawningSchedulerRefusesTheConductorsNextCreate is the same purse read
// from the other end, and it is the half that a second gapStamp keyed on the
// scheduler would pass while failing the test above.
//
// gapStamp holds ONE stamp in total. Give the scheduler its own key on it and the
// two identities alternate: each takes the slot from the other and NEITHER is
// ever refused — the failure the type's own comment names. Both roads are stamped
// under the door for that reason, so a panel the scheduler has just provisioned
// is a panel the conductor's next panel.create waits behind.
func TestASpawningSchedulerRefusesTheConductorsNextCreate(t *testing.T) {
	s := newHostServer(t)
	send, until := conductorWire(t, s)

	send(proto.Command{
		Action: "task.enqueue", Prompt: "build it",
		Path: "/bin/sh", Args: []string{"-c", "sleep 30"},
	})
	if msg := until("panels", "error"); msg.Type == "error" {
		t.Fatalf("enqueue was refused: %q", msg.Error)
	}
	s.monitorTick()
	if got := s.PanelCount(); got != 1 {
		t.Fatalf("the backlog provisioned %d panels, want 1", got)
	}

	send(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	msg := until("panels", "error")
	if msg.Type != "error" || !strings.Contains(msg.Error, "too fast") {
		t.Fatalf("a conductor spawn an instant after the scheduler provisioned an agent got a %q "+
			"(%q), want the rate refusal: the two roads are drawing on separate purses",
			msg.Type, msg.Error)
	}
}

// TestTheBacklogProvisionsOneAgentPerGap is the fork bomb the gap closes, at the
// scheduler rather than at the conductor.
//
// One pass used to hand back a spawn request for every spawn-on-demand task the
// ceiling would admit, and applyScheduledSpawns creates them back to back — so a
// conductor with an unmetered task.enqueue got sixty-odd panels in the time
// createPanel takes, sixty times over. The gap makes that one per quarter second,
// and the rest of the backlog waits rather than being turned away.
func TestTheBacklogProvisionsOneAgentPerGap(t *testing.T) {
	s, _, _ := gateServer() // no standing agents, so every task provisions its own
	var ids []string
	for range 5 {
		id, err := s.enqueueTask("build it", "", &task.SpawnSpec{Command: "claude"})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		ids = append(ids, id)
	}

	_, spawns := schedule(s)
	if len(spawns) != 1 {
		t.Fatalf("one pass over five spawn-on-demand tasks asked for %d panels, want 1: with no gap "+
			"here the whole backlog is provisioned at machine speed", len(spawns))
	}
	// The four it did not take are still QUEUED, not failed and not dropped. A gap
	// that refused work rather than deferring it would be a budget, which is the
	// thing this deliberately is not.
	if _, again := schedule(s); len(again) != 0 {
		t.Fatalf("a second pass in the same instant asked for %d more panels, want 0", len(again))
	}
	for _, id := range ids[1:] {
		if got := s.tasks[id]; got == nil || got.Status != task.Queued {
			t.Fatalf("task %s is %+v, want it still queued and waiting its turn", id, got)
		}
	}

	// A gap later, the next one goes — so five tasks take five gaps rather than
	// never draining.
	rewind(s, &s.spawn, 2*minConductorSpawnGap)
	if _, third := schedule(s); len(third) != 1 {
		t.Fatalf("a full gap later the backlog asked for %d panels, want 1", len(third))
	}
}

// TestTheFleetCeilingIsTheSameRuleOnBothRoads is what the ONE SPELLING buys, as
// behaviour rather than as a claim about where the code lives.
//
// The ceiling was written twice — once in guardConductor's budget, once at
// scheduleLocked — and #67 had just added a third caller to the first of them.
// The two had already drifted in an order nobody chose: the scheduler's refusal
// came before its gap did not exist, and the conductor's is ordered so that being
// told the fleet is full does not also cost the caller its next quarter second.
// Sharing one helper makes that property true on both roads at once, and this is
// the assertion that notices if they are ever pulled apart again.
func TestTheFleetCeilingIsTheSameRuleOnBothRoads(t *testing.T) {
	s, _, _ := gateServer(fullFleet()...)
	if _, err := s.enqueueTask("build it", "", &task.SpawnSpec{Command: "claude"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, spawns := schedule(s); len(spawns) != 0 {
		t.Fatalf("a fleet at capacity provisioned %d more panels, want 0", len(spawns))
	}

	// Room is made, and the very next pass provisions — in the same instant, with
	// no gap crossed. Being told the fleet is full must not spend the slot.
	s.mu.Lock()
	s.panels = s.panels[:1]
	s.mu.Unlock()
	if _, spawns := schedule(s); len(spawns) != 1 {
		t.Fatalf("the pass after a capacity refusal asked for %d panels, want 1: the ceiling is "+
			"spending the rate slot it declines to use", len(spawns))
	}
}

// conductorWire dials a conductor connection over a pipe and greets as one,
// answering a sender and a reader that skips the snapshots and pings. It is the
// shape TestWTAddConductorReachesTheCap spelled inline, hoisted because #75's
// acceptance needs the same wire and two tests sharing a hand-rolled codec is one
// drift away from testing different things.
//
// It does not reuse remote_test.go's rawConn, which is the same codec with a read
// deadline on top: that file is package server_test, and everything here reaches
// into the unexported server — handle, monitorTick, roleConductor — so the two
// cannot see each other. The duplication is the package boundary's, not a
// choice.
func conductorWire(t *testing.T, s *Server) (func(proto.Command), func(...string) proto.ServerMsg) {
	t.Helper()
	srvEnd, cliEnd := net.Pipe()
	go s.handle(srvEnd)
	t.Cleanup(func() { _ = cliEnd.Close() })

	enc, dec := json.NewEncoder(cliEnd), json.NewDecoder(cliEnd)
	send := func(cmd proto.Command) {
		t.Helper()
		if err := enc.Encode(cmd); err != nil {
			t.Fatalf("send %s: %v", cmd.Action, err)
		}
	}
	until := func(want ...string) proto.ServerMsg {
		t.Helper()
		for range 40 {
			var msg proto.ServerMsg
			if err := dec.Decode(&msg); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if slices.Contains(want, msg.Type) {
				return msg
			}
		}
		t.Fatalf("never saw any of %v", want)
		return proto.ServerMsg{}
	}
	send(proto.Command{Action: "hello", Role: roleConductor, Self: "c1"})
	until("panels")
	return send, until
}
