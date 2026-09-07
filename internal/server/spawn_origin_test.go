package server

import (
	"os"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/task"
)

// The fleet spawn budget reaches createPanel down four roads, and it is TWO
// LIMITS rather than one: a ceiling, which is a statement about the host, and a
// rate gap, which is a statement about how fast something is asking. Each road
// answers each of them on its own (#86). These tests pin WHAT EACH ROAD PAYS ON
// EACH AXIS, in both directions: that the metered ones are still metered, and —
// the half a refusal test cannot give — that the exempt ones are still exempt.
//
// A test asserting only "a conductor spawning too fast is refused" passes just as
// happily against a change that quietly starts charging the operator's own hand
// as against one that does not. So the GAP is read off the DOOR'S STAMP rather
// than off a refusal: the budget's rate slot either was spent or was not, and
// that is the quantity the roads actually differ in. The CEILING has no stamp to
// read — it charges nothing and only refuses — so it is read the only way it can
// be, by packing the fleet and asking.

// doorSlotSpent reports whether the fleet spawn budget's rate slot has been spent
// — whether the gapStamp keyed on spawnDoor carries a stamp for it. A spend is
// the whole of what "this road paid" means, and it is invisible from the wire:
// admitting is silent, and the refusal it causes lands on whatever comes next.
func doorSlotSpent(s *Server) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawn.who == spawnDoor && !s.spawn.at.IsZero()
}

// packFleet fills the fleet to maxConductorFleet without spawning anything, so a
// test can ask what a road pays AT THE CEILING — the other half of the budget,
// and the half an exempt road walks through without noticing.
func packFleet(s *Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panels = fullFleet()
}

// TestTheOperatorsCreatePaysTheCeilingAndNotTheGap is #86's decision read from
// the wire, and it is one test rather than two because the point is the pair: the
// SAME hand, at the SAME door, is refused by one limit and waved through by the
// other. A test of either half alone reads as an accident of where a check sits.
//
// The ceiling is new. Until #86 guardConductor returned on its first line for a
// connection that was not a conductor, so the cockpit reached neither cap and
// could quietly take the fleet past 64 — and the conductor that hit the wall
// afterwards was refused for it. The gap is unchanged, and stays unchanged for a
// reason the ceiling does not share: it exists to stop something LOOPING, and a
// person at a keyboard does not loop.
func TestTheOperatorsCreatePaysTheCeilingAndNotTheGap(t *testing.T) {
	s := newHostServer(t)
	packFleet(s)
	send, until := cockpitWire(t, s)

	send(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	msg := until("panels", "error")
	if msg.Type != "error" || !strings.Contains(msg.Error, "capacity") {
		t.Fatalf("the operator's create onto a full fleet got a %q (%q), want the capacity "+
			"refusal: a ceiling the cockpit walks past is not a ceiling", msg.Type, msg.Error)
	}
	if doorSlotSpent(s) {
		t.Fatal("the capacity refusal spent the fleet's spawn slot: being told the fleet is " +
			"full is about the fleet, and must not also cost the next caller its quarter-second")
	}

	// Room made, and now the other axis on the identical road: two creates in the
	// same instant, both admitted, and the slot still unspent.
	s.mu.Lock()
	s.panels = nil
	s.mu.Unlock()
	for i := range 2 {
		send(proto.Command{Action: "panel.create", Kind: proto.KindShell})
		if msg := until("panels", "error"); msg.Type != "panels" {
			t.Fatalf("the operator's create #%d was refused with room to spare: %q", i, msg.Error)
		}
	}
	if doorSlotSpent(s) {
		t.Fatal("the cockpit spent the fleet's spawn slot: the operator's hand does not pay " +
			"the gap, and charging it there is a second policy change rather than this one")
	}
}

// TestTheConductorsCreateIsCharged is the same road driven by an agent, and the
// direction the exemption above is only meaningful against: the identical command
// on the identical server spends the slot and the next one is refused by it.
func TestTheConductorsCreateIsCharged(t *testing.T) {
	s := newHostServer(t)
	send, until := conductorWire(t, s)

	send(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	if msg := until("panels", "error"); msg.Type != "panels" {
		t.Fatalf("the conductor's first create was refused: %q", msg.Error)
	}
	if !doorSlotSpent(s) {
		t.Fatal("the conductor's create did not spend the fleet's spawn slot: the agent's road " +
			"is the metered one")
	}
	send(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	msg := until("panels", "error")
	if msg.Type != "error" || !strings.Contains(msg.Error, "too fast") {
		t.Fatalf("a second conductor create an instant later got a %q (%q), want the rate refusal",
			msg.Type, msg.Error)
	}
}

// TestThePluginPaysTheCeilingAndNotTheGap is the plugin road read on both axes
// through Server.Spawn itself — the method baton.spawn calls — rather than
// through createPanel, because which origin this road names is the whole of what
// it contributes and a test below it would not see the road at all.
//
// The ceiling half is new (#86) and is the operator's argument applied to a road
// the operator installed: a plugin spawning the 65th panel takes the slot from
// whatever asks next, and the ceiling is about the host either way.
func TestThePluginPaysTheCeilingAndNotTheGap(t *testing.T) {
	s := newHostServer(t)
	packFleet(s)
	dir := os.Getenv("BATON_TEST_DIR")

	_, err := s.Spawn(proto.KindShell, "", nil, dir, "")
	if err == nil {
		t.Fatal("baton.spawn spawned onto a full fleet: a plugin is unattended code, and the " +
			"ceiling it walks past is spent by whatever asks next")
	}
	if !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("the refusal says %q, want it to name the fleet's capacity", err)
	}
	if doorSlotSpent(s) {
		t.Fatal("the capacity refusal spent the fleet's spawn slot on the plugin road")
	}
}

// TestThePluginsBurstIsStillAdmitted is #86's MARKED HOLD in the only form a hold
// survives in: a test. baton.spawn spawns twice in the same instant and both go
// through, because the plugin host pays the ceiling and not the gap.
//
// The hold is not that a plugin SHOULD be exempt. The argument the other way is
// good and is written on originPlugin — a plugin is unattended code running a
// loop, which is exactly the failure the gap exists for, and the same API already
// meters baton.enqueue against queueMax, so "a plugin is the operator's hand"
// does not survive contact with the rest of the design. What #86 declined to do
// is take that decision on the operator's behalf, because charging the gap breaks
// a working plugin at a limit it has never hit, silently, on an upgrade.
//
// So this is the assertion that fails when someone makes the one-line change, and
// it is here so that making it is a decision rather than a drift. #51's
// authorAgent hold and TestAFanoutCountsNoUserSignal are the same pattern, and
// the reason for it: that hold was a comment for a while, and a comment is
// something a green suite lets you walk past.
func TestThePluginsBurstIsStillAdmitted(t *testing.T) {
	s := newHostServer(t)
	dir := os.Getenv("BATON_TEST_DIR")

	for i := range 2 {
		if _, err := s.Spawn(proto.KindShell, "", nil, dir, ""); err != nil {
			t.Fatalf("baton.spawn #%d was refused in a burst: %v — the gap exemption is the "+
				"hold #86 left standing, and closing it is the operator's call", i, err)
		}
	}
	if doorSlotSpent(s) {
		t.Fatal("baton.spawn spent the fleet's spawn slot: the plugin host is held exempt from " +
			"the rate gap, and the argument for charging it is on originPlugin rather than lost")
	}
}

// TestTheSchedulerIsChargedOnceAndNotTwice is the specific bug an origin
// parameter invites, as an assertion, and it is the GAP half of "exactly once" —
// the ceiling half is TestEachRoadsCeilingAtTheDoor's scheduler cell.
//
// The scheduler spends its slot when it DECIDES — inside scheduleLocked, holding
// s.mu — and creates the panel later, without it. A charge that also fired at
// creation would find the gap it had just stamped, refuse, and fail the task with
// `spawn failed: spawning too fast`. So the panel existing and the task being
// DISPATCHED rather than FAILED is exactly the double charge's absence, and it is
// why this asserts the task's fate and not only the fleet's size.
//
// BOTH DIRECTIONS ARE HERE, which is what makes it a charge rather than a hole:
// the slot must be spent (or the backlog is a door out of the rate gap) and it
// must be spent once (or the backlog is a door into a refusal it was granted).
func TestTheSchedulerIsChargedOnceAndNotTwice(t *testing.T) {
	s := newHostServer(t)
	id, err := s.enqueueTask(conn(""), "build it", "",
		&task.SpawnSpec{Command: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	before := s.PanelCount()
	s.monitorTick() // the scheduler's own tick: it decides, then provisions
	if got := s.PanelCount(); got != before+1 {
		t.Fatalf("the tick provisioned %d panels, want %d", got-before, 1)
	}
	if !doorSlotSpent(s) {
		t.Fatal("the scheduler provisioned an agent without spending the fleet's spawn slot: " +
			"the backlog is a door out of the rate gap")
	}
	got, ok := s.TaskByID(id)
	if !ok {
		t.Fatal("the queued task vanished")
	}
	if got.Status != task.Dispatched {
		t.Fatalf("the provisioned task is %v (%q), want dispatched: a second charge at creation "+
			"finds the gap the decision has just stamped and fails the task", got.Status, got.Result)
	}
}

// TestTheWorktreeRoadIsChargedAtTheFenceAndNotOnTheRoad pins WHERE the fourth
// road pays, which is not a detail: guardConductor charges it before any git
// runs, so a refused conductor is refused before a tree exists on disk (see
// TestWTAddConductorReachesTheCap). A charge moved onto the road would be paid
// after `git worktree add` had already built the tree.
//
// It drives the METERED half of the road — originConductor, the one a fence has
// already charged — because that is the half a second charge at the door would
// break, and the half that has to keep costing nothing here.
func TestTheWorktreeRoadIsChargedAtTheFenceAndNotOnTheRoad(t *testing.T) {
	repo := wtRepo(t)
	s, _ := wtServer(t)

	if err := s.worktreeSpawn(originConductor, repo, "feature/origin", idleAgent()); err != nil {
		t.Fatalf("worktreeSpawn: %v", err)
	}
	if doorSlotSpent(s) {
		t.Fatal("worktreeSpawn spent the fleet's spawn slot itself: the charge for this road is " +
			"the fence's, and it has to land before git builds the tree")
	}
}

// TestNoRoadSpendsTheGapAtCreatePanel is the GAP axis, and it is the axis on
// which the door still charges nobody — which #86 did not change and had no
// reason to.
//
// Two roads are exempt from it: the operator's hand, and the plugin's under a
// marked hold. The other two spend their slot upstream, each at the last point
// where a refusal can still land before that road's own side effects, so a road
// that paid at the fence AND here would pay twice — and the second charge would
// find the stamp the first just left and refuse the spawn it had just admitted.
// This is the assertion that fails the moment any arm starts to spend here.
func TestNoRoadSpendsTheGapAtCreatePanel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin panelOrigin
	}{
		{"operator", originOperator},
		{"conductor", originConductor},
		{"scheduler", originScheduler},
		{"plugin", originPlugin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHostServer(t)
			if _, err := s.createPanel(tc.origin, proto.KindShell, "", nil,
				os.Getenv("BATON_TEST_DIR"), "", false, false); err != nil {
				t.Fatalf("createPanel: %v", err)
			}
			if doorSlotSpent(s) {
				t.Fatal("createPanel spent the fleet's spawn slot for this road; the gap is " +
					"answered before any road gets here, and adding a charge at the door " +
					"charges the two upstream roads a second time")
			}
		})
	}
}

// TestEachRoadsCeilingAtTheDoor is the OTHER axis, read at the same door and read
// separately on purpose. The budget is two limits in one call — a ceiling that is
// a statement about the host, and a rate gap that is a statement about how fast
// something is asking — and a road pays each of them somewhere, or nowhere, on
// its own. One table per axis is what lets a cell move and be seen to move; the
// single-answer version above could only ever say "all four the same".
//
// The two hands that reach the ceiling for the first time are refused here (#86);
// the two that have already paid it upstream are admitted, and their two cells
// are the ones this table exists to hold still. The SCHEDULER'S is the sharpest
// of the four: it asked the ceiling at its decision and creates the panel later,
// so a fleet that filled up in between must not refuse it a second time — that is
// what "charged exactly once" means on an axis with no stamp to read.
func TestEachRoadsCeilingAtTheDoor(t *testing.T) {
	for _, tc := range []struct {
		name        string
		origin      panelOrigin
		wantRefused bool
	}{
		{"operator", originOperator, true},
		{"conductor", originConductor, false},
		{"scheduler", originScheduler, false},
		{"plugin", originPlugin, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHostServer(t)
			packFleet(s)
			before := s.PanelCount()

			_, err := s.createPanel(tc.origin, proto.KindShell, "", nil,
				os.Getenv("BATON_TEST_DIR"), "", false, false)
			switch {
			case tc.wantRefused && err == nil:
				t.Fatalf("this road spawned the %dst panel onto a full fleet: a ceiling every "+
					"road does not pay is not a ceiling", before+1)
			case tc.wantRefused && !strings.Contains(err.Error(), "capacity"):
				t.Fatalf("the refusal says %q, want it to name the fleet's capacity", err)
			case !tc.wantRefused && err != nil:
				t.Fatalf("this road was refused at the ceiling: %v — its charge is spent "+
					"upstream, and charging it here charges it twice", err)
			}

			want := before + 1
			if tc.wantRefused {
				want = before
			}
			if got := s.PanelCount(); got != want {
				t.Fatalf("the fleet is %d panels, want %d", got, want)
			}
		})
	}
}

// TestConnOriginReadsTheRoleAndNothingElse pins the discrimination the origin
// parameter exists to carry, and it is pinned on its own because nothing else
// can pin it: the four origins cost the same at the door today, so a connection
// handed the wrong one would spawn exactly the same panel.
//
// The third case is the one worth having. A remote cockpit declares a role, so
// "has a role" is not the test — being the SCOPED CONDUCTOR role is. A cockpit
// that reached the daemon over the ssh bridge is still the operator's own hand,
// and guardConductor fences it no more than it fences a local one.
func TestConnOriginReadsTheRoleAndNothingElse(t *testing.T) {
	local := conn("")
	remote := conn("")
	remote.role = roleRemote
	conductor := conn("c1")
	conductor.role = roleConductor

	for _, tc := range []struct {
		name string
		cc   *clientConn
		want panelOrigin
	}{
		{"cockpit", local, originOperator},
		{"remote cockpit", remote, originOperator},
		{"conductor", conductor, originConductor},
	} {
		if got := connOrigin(tc.cc); got != tc.want {
			t.Errorf("connOrigin(%s) = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestAFifthRoadMustNameItself is what the parameter buys, and it is the whole of
// #79's complaint answered: a road that reaches this door without an answer
// written for it now spawns nothing, where before it spawned freely and silently.
//
// The origin nobody has written a budget for is spelled here as a value outside
// the four, because that is what a fifth road is on the day it is added and
// before anyone has decided what it costs.
func TestAFifthRoadMustNameItself(t *testing.T) {
	s := newHostServer(t)
	before := s.PanelCount()

	id, err := s.createPanel(originPlugin+1, proto.KindShell, "", nil,
		os.Getenv("BATON_TEST_DIR"), "", false, false)
	if err == nil {
		t.Fatalf("a road with no spawn budget written for it spawned panel %q: the budget is "+
			"held by agreement again", id)
	}
	if !strings.Contains(err.Error(), "spawn budget") {
		t.Fatalf("the refusal says %q, want it to name the missing budget", err)
	}
	if got := s.PanelCount(); got != before {
		t.Fatalf("the fleet grew from %d to %d panels on a refused spawn", before, got)
	}
}
