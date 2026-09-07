package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// fullFleet is a fleet sitting exactly on maxConductorFleet, with a conductor at
// its head: the shape every capacity test needs, spelled once so the three that
// need it cannot drift from the constant or from each other.
func fullFleet() []panel.Panel {
	panels := make([]panel.Panel, 0, maxConductorFleet)
	panels = append(panels, panel.Panel{ID: "c1", Kind: panel.Agent, State: panel.Running, Conductor: true})
	for i := 1; i < maxConductorFleet; i++ {
		panels = append(panels, panel.Panel{ID: fmt.Sprintf("p%d", i), Kind: panel.Shell})
	}
	return panels
}

// TestSpawnGapThrottlesAcrossConnections is the OTHER half of the refine-cap
// bug, and it was there from the day minConductorSpawnGap was written.
//
// The gap existed precisely because an LLM will loop, and it kept its stamp on
// the clientConn — so it throttled a conductor holding one socket open and did
// nothing at all to `baton_spawn`, which goes through the same dial-per-tool-call
// MCP path the refine verbs do, or to `baton ctl spawn`, which is a process per
// command. It was cured by moving the stamp off the connection: the refine cap
// onto the conductor's PANEL, and this one — since #75 — onto the door it guards.
//
// TestConductorGuardrails covers the persistent-connection case and passed
// throughout, which is exactly why this one has to exist beside it.
func TestSpawnGapThrottlesAcrossConnections(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "c1", Kind: panel.Agent, State: panel.Running, Conductor: true})
	create := proto.Command{Action: "panel.create", Kind: "shell"}

	conductor := func() *clientConn {
		cc := conn("c1")
		cc.role = roleConductor
		return cc
	}
	if reason := s.guardConductor(conductor(), create); reason != "" {
		t.Fatalf("the first spawn was refused: %q", reason)
	}
	// Twenty fresh connections, each declaring the same conductor panel — the
	// shape of a looping agent. Every one of them must be refused.
	for i := range 20 {
		if reason := s.guardConductor(conductor(), create); !strings.Contains(reason, "too fast") {
			t.Fatalf("fresh connection %d was admitted (%q); the spawn cap must not be per-connection", i, reason)
		}
	}

	// The clock is the only thing holding it.
	rewind(s, &s.spawn, 2*minConductorSpawnGap)
	if reason := s.guardConductor(conductor(), create); reason != "" {
		t.Fatalf("a spawn past the gap was refused: %q", reason)
	}

	// A DIFFERENT identity IS throttled by the stamp the first one left, and that
	// is #75's ruling read from its narrowest angle. The cap fences the DOOR, not
	// the caller: three roads reach createPanel and gapStamp holds one stamp in
	// total, so keying it on who asked would let two askers alternate and refuse
	// neither. This is the assertion that fails the moment the key goes back to
	// being something a caller names.
	other := conn("c2")
	other.role = roleConductor
	if reason := s.guardConductor(other, create); !strings.Contains(reason, "too fast") {
		t.Fatalf("a second identity was admitted (%q) an instant after the first spawned: the spawn "+
			"cap is keyed on the caller again, and two callers alternating are never refused", reason)
	}
}

// TestFleetCapacityDoesNotSpendTheSpawnSlot: the capacity refusal is about the
// fleet, not about this caller, so being told the fleet is full must not also
// cost the caller its next quarter-second. The two refusals are ordered for that
// reason and the order is easy to swap back by accident.
func TestFleetCapacityDoesNotSpendTheSpawnSlot(t *testing.T) {
	s, _, _ := gateServer(fullFleet()...)
	cc := conn("c1")
	cc.role = roleConductor
	create := proto.Command{Action: "panel.create", Kind: "shell"}

	if reason := s.guardConductor(cc, create); !strings.Contains(reason, "capacity") {
		t.Fatalf("a full fleet answered %q, want the capacity refusal", reason)
	}
	// Room is made; the very next attempt is admitted rather than rate-refused.
	s.mu.Lock()
	s.panels = s.panels[:1]
	s.mu.Unlock()
	if reason := s.guardConductor(cc, create); reason != "" {
		t.Fatalf("the spawn after a capacity refusal answered %q, want it admitted", reason)
	}
}

// TestWorktreeAddPaysTheSpawnCaps is #67's ruling as an assertion: the two caps
// panel.create has always paid now reach panel.git worktree-add, which is a spawn
// wearing a git op's name. Before this, a conductor refused at the ceiling could
// keep spawning through the git verb indefinitely.
//
// BOTH forms are charged. The targetless one is what #66 fenced and #67 opens;
// the targeted one is the door beside it, and a ceiling that shut only the first
// would shut nothing — the conductor would fan its own agent onto branch after
// branch instead. That is the assertion the "targeted" case makes, and it fails
// against the shape where only cmd.ID == "" is charged.
//
// The wire reachability of all this — that the guard is actually consulted on the
// worktree path — is TestWTAddConductorReachesTheCap, on a real connection.
func TestWorktreeAddPaysTheSpawnCaps(t *testing.T) {
	wtAdd := func(id string) proto.Command {
		return proto.Command{Action: "panel.git", Git: "worktree-add", ID: id, Name: "feature/x",
			Dir: "/tmp/repo", Path: "/bin/sh", Args: []string{"-c", "sleep 30"}}
	}

	for _, tc := range []struct {
		name string
		cmd  proto.Command
	}{
		{"targetless", wtAdd("")},
		{"targeted", wtAdd("p1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := gateServer(fullFleet()...)
			cc := conn("c1")
			cc.role = roleConductor

			if reason := s.guardConductor(cc, tc.cmd); !strings.Contains(reason, "capacity") {
				t.Fatalf("a full fleet answered %q, want the capacity refusal", reason)
			}

			// Room made: admitted once, then rate-refused — the same two caps in the
			// same order panel.create pays them.
			s.mu.Lock()
			s.panels = s.panels[:1]
			s.mu.Unlock()
			if reason := s.guardConductor(cc, tc.cmd); reason != "" {
				t.Fatalf("a worktree spawn with room answered %q, want it admitted", reason)
			}
			if reason := s.guardConductor(cc, tc.cmd); !strings.Contains(reason, "too fast") {
				t.Fatalf("a second worktree spawn inside the gap answered %q, want the rate refusal", reason)
			}

			// THIS FENCE fences the conductor, not the verb: the operator's cockpit runs
			// the identical command against the identical full fleet and this guard has
			// nothing to say about it. That is unchanged by #86, which did not widen the
			// fence — it charged the cockpit's ceiling at createPanel instead, where the
			// road ends rather than where a conductor is stopped (see
			// TestEachRoadsCeilingAtTheDoor). So the operator is still unfenced HERE and
			// is still exempt from the rate cap everywhere.
			//
			// #90 then moved WHERE the operator's ceiling is read on this one road,
			// without moving the fence: worktreeSpawn reads it before git makes the tree,
			// because a refusal at the door is downstream of a side effect on disk. The
			// guard below is still the conductor's alone.
			cockpit, _, _ := gateServer(fullFleet()...)
			if reason := cockpit.guardConductor(conn("c1"), tc.cmd); reason != "" {
				t.Fatalf("the cockpit is not fenced by the conductor caps, got %q", reason)
			}
		})
	}
}

// TestWTAddConductorReachesTheCap is the reachability proof the guard-level tests
// above cannot give: a guard that returns the right reason on a path nothing
// routes through would satisfy every one of them. So this drives a real conductor
// connection through the real command loop and asserts the refusal comes back on
// the wire.
//
// Two things make it deterministic rather than a race against a 250ms gap. The
// gap is widened to an hour, so no amount of load can let it expire between the
// two commands — an earlier version of this test spent a real `git worktree add`
// inside the window and passed alone while failing under a loaded suite. And the
// repository named is NOT a repository, which turns the assertion into a
// discriminator: if the guard were not consulted for this verb the command would
// reach worktreeSpawn and answer "not a git repository", so "too fast" can only
// come from the cap.
func TestWTAddConductorReachesTheCap(t *testing.T) {
	s := newHostServer(t)
	s.spawn.gap = time.Hour
	send, until := conductorWire(t, s)

	// A plain spawn, admitted, spends the conductor's slot.
	send(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	if msg := until("panels", "error"); msg.Type != "panels" {
		t.Fatalf("the first spawn should be admitted, got %q", msg.Error)
	}

	// The worktree spawn now draws on a purse panel.create has emptied.
	notRepo := t.TempDir()
	send(proto.Command{
		Action: "panel.git", Git: "worktree-add",
		Dir: notRepo, Name: "feature/capped",
		Path: "/bin/sh", Args: []string{"-c", "sleep 30"},
	})
	msg := until("panels", "error")
	switch {
	case msg.Type != "error":
		t.Fatalf("a worktree spawn inside the gap should be refused, got a %q", msg.Type)
	case strings.Contains(msg.Error, "not a git repository"):
		t.Fatalf("the command reached worktreeSpawn, so the cap was never consulted: %q", msg.Error)
	case !strings.Contains(msg.Error, "too fast"):
		t.Fatalf("want the rate refusal, got %q", msg.Error)
	}
}

// TestWorktreeRemoveIsNotASpawn keeps the widening to the verb that spawns. The
// other worktree op destroys rather than creates, so charging it a spawn slot
// would be a refusal with nothing behind it — and would let a conductor exhaust
// its own budget tidying up.
func TestWorktreeRemoveIsNotASpawn(t *testing.T) {
	s, _, _ := gateServer(fullFleet()...)
	cc := conn("c1")
	cc.role = roleConductor

	rm := proto.Command{Action: "panel.git", Git: "worktree-remove", ID: "p1", Dir: "/tmp/tree"}
	if reason := s.guardConductor(cc, rm); reason != "" {
		t.Fatalf("worktree-remove on a full fleet answered %q, want it admitted", reason)
	}
	// And it did not quietly spend the slot on the way through.
	if reason := s.guardConductor(cc, proto.Command{Action: "panel.create", Kind: "shell"}); !strings.Contains(reason, "capacity") {
		t.Fatalf("worktree-remove must not touch the spawn budget, got %q", reason)
	}
}
