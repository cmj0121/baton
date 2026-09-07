package server

import (
	"os"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/task"
)

// The fleet spawn budget reaches createPanel down four roads and charges them
// three different ways. These tests pin WHAT EACH ROAD PAYS, in both directions:
// that the metered ones are still metered, and — the half a refusal test cannot
// give — that the exempt ones are still exempt.
//
// A test asserting only "a conductor spawning too fast is refused" passes just as
// happily against a change that quietly starts charging the operator's own hand
// as against one that does not. So each road here is read off the DOOR'S STAMP
// rather than off a refusal: the budget's rate slot either was spent or was not,
// and that is the quantity the roads actually differ in.

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

// TestTheOperatorsCreatePaysNothing is the exemption nobody wrote down, read from
// the wire: guardConductor returns on its first line for a connection that is not
// a conductor, so the cockpit's panel.create reaches neither cap. It spawns on a
// fleet already at the ceiling, twice in the same instant, and both go through.
//
// This is the assertion that fails the moment the charge is moved somewhere that
// cannot tell the operator's hand from an agent's.
func TestTheOperatorsCreatePaysNothing(t *testing.T) {
	s := newHostServer(t)
	packFleet(s)
	send, until := cockpitWire(t, s)

	for i := range 2 {
		send(proto.Command{Action: "panel.create", Kind: proto.KindShell})
		if msg := until("panels", "error"); msg.Type != "panels" {
			t.Fatalf("the operator's create #%d was refused on a full fleet: %q", i, msg.Error)
		}
	}
	if doorSlotSpent(s) {
		t.Fatal("the cockpit spent the fleet's spawn slot: the operator's own hand is unmetered " +
			"today, and metering it is a policy change rather than a move")
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

// TestThePluginsSpawnIsExempt pins the argued exemption rather than merely
// leaving it unasserted. baton.spawn is a plugin the OPERATOR installed acting as
// the operator's hand, so it is charged neither cap — it spawns twice in the same
// instant onto a fleet already at the ceiling.
//
// Whether that exemption deserves to survive is a separate question. What this
// asserts is only that a refactor of where the charge lives does not answer it by
// accident.
func TestThePluginsSpawnIsExempt(t *testing.T) {
	s := newHostServer(t)
	packFleet(s)
	dir := os.Getenv("BATON_TEST_DIR")

	for i := range 2 {
		if _, err := s.Spawn(proto.KindShell, "", nil, dir, ""); err != nil {
			t.Fatalf("baton.spawn #%d was refused on a full fleet: %v", i, err)
		}
	}
	if doorSlotSpent(s) {
		t.Fatal("baton.spawn spent the fleet's spawn slot: the plugin host is deliberately exempt")
	}
}

// TestTheSchedulerIsChargedOnceAndNotTwice is the specific bug an origin
// parameter invites, as an assertion.
//
// The scheduler spends its slot when it DECIDES — inside scheduleLocked, holding
// s.mu — and creates the panel later, without it. A charge that also fired at
// creation would find the gap it had just stamped, refuse, and fail the task with
// `spawn failed: spawning too fast`. So the panel existing and the task being
// DISPATCHED rather than FAILED is exactly the double charge's absence, and it is
// why this asserts the task's fate and not only the fleet's size.
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

// TestNoRoadIsChargedAtCreatePanel is #79's refactor read as the thing it had to
// be: a move of WHERE each road's answer is written, and of nothing else.
//
// Every road's answer now lives on its panelOrigin constant, and for all four of
// them the answer is that createPanel takes nothing — two are exempt, and the
// other two spend their slot upstream, each at the last point where a refusal can
// still land before that road's own side effects. So the door itself charges
// none of them, and this is the assertion that fails the moment an arm starts to.
// A road that paid at the fence AND at the door would pay twice.
func TestNoRoadIsChargedAtCreatePanel(t *testing.T) {
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
				t.Fatal("createPanel spent the fleet's spawn slot for this road; today every " +
					"road's charge is answered before it gets here, and adding one at the door " +
					"charges the two metered roads a second time")
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
// Every road is admitted onto a fleet already sitting on maxConductorFleet today.
// Two of those four are the cells #86 changes; the other two are the ones it must
// not, and they are here so a change to them cannot pass unnoticed.
func TestEachRoadsCeilingAtTheDoor(t *testing.T) {
	for _, tc := range []struct {
		name        string
		origin      panelOrigin
		wantRefused bool
	}{
		{"operator", originOperator, false},
		{"conductor", originConductor, false},
		{"scheduler", originScheduler, false},
		{"plugin", originPlugin, false},
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
