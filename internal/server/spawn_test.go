package server

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/task"
)

// schedule runs one scheduling pass under the lock, returning the deliveries and
// spawn requests, so a test can assert what the scheduler decided without a tick.
func schedule(s *Server) ([]readyDispatch, []spawnRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scheduleLocked()
}

// TestSpawnOnDemandRequestsPanel checks that a spawn-on-demand task with no free
// agent yields exactly one spawn request, marks itself in flight, and is not
// requested again on the next pass (so a slow spawn is not double-provisioned).
func TestSpawnOnDemandRequestsPanel(t *testing.T) {
	s, _, _ := gateServer() // no agents in the fleet
	id, err := s.enqueueTask("build it", "", &task.SpawnSpec{Command: "claude"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deliver, spawns := schedule(s)
	if len(deliver) != 0 {
		t.Fatalf("no standing agent should mean no direct delivery, got %d", len(deliver))
	}
	if len(spawns) != 1 || spawns[0].taskID != id || spawns[0].spec.Command != "claude" {
		t.Fatalf("a spawn task with no free agent should request one panel, got %+v", spawns)
	}
	if !s.spawning[id] {
		t.Fatal("the task should be marked in flight while its panel is provisioned")
	}

	if _, again := schedule(s); len(again) != 0 {
		t.Fatalf("a task already being provisioned must not be spawned again, got %d", len(again))
	}
}

// TestSpawnOnDemandPrefersIdleAgent checks that a spawn task rides a free standing
// agent when one exists, rather than provisioning a new panel.
func TestSpawnOnDemandPrefersIdleAgent(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Idle})
	id, err := s.enqueueTask("build it", "", &task.SpawnSpec{Command: "claude"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deliver, spawns := schedule(s)
	if len(spawns) != 0 {
		t.Fatalf("an idle agent should be used instead of spawning, got %d spawns", len(spawns))
	}
	if len(deliver) != 1 {
		t.Fatalf("the task should be delivered to the idle agent, got %d", len(deliver))
	}
	if got := s.tasks[id]; got.Panel != "a1" || got.Status != task.Dispatched {
		t.Fatalf("the task should be assigned to the idle agent, got %+v", got)
	}
}

// TestSpawnOnDemandProvisionsAndAssigns drives the whole spawn path: with no free
// agent, applyScheduledSpawns creates a real panel for the task and assigns it,
// holding the dispatch for delivery once the panel settles.
func TestSpawnOnDemandProvisionsAndAssigns(t *testing.T) {
	s, _, _ := gateServer() // no agents
	id, err := s.enqueueTask("hi", "", &task.SpawnSpec{Command: "cat"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	_, spawns := schedule(s)
	if len(spawns) != 1 {
		t.Fatalf("expected one spawn request, got %d", len(spawns))
	}
	if !s.applyScheduledSpawns(spawns) {
		t.Fatal("provisioning a panel should report a fleet change")
	}

	if len(s.panels) != 1 || s.panels[0].Kind != panel.Agent {
		t.Fatalf("a fresh agent panel should be provisioned, got %+v", s.panels)
	}
	pid := s.panels[0].ID
	tk := s.tasks[id]
	if tk.Panel != pid || tk.Status != task.Dispatched {
		t.Fatalf("the task should be assigned to the new panel, got %+v", tk)
	}
	if _, held := s.pendingDispatch[pid]; !held {
		t.Fatal("the prompt should be held for delivery once the new panel settles")
	}
	if s.spawning[id] {
		t.Fatal("the in-flight spawn mark should be cleared after provisioning")
	}
	_ = s.closePanel(pid) // reap the real process
}

// TestEnqueueSpawnNeedsCommand checks the guard: a spawn spec with no command is
// refused rather than queuing a task that can never provision anything.
func TestEnqueueSpawnNeedsCommand(t *testing.T) {
	s, _, _ := gateServer()
	if _, err := s.enqueueTask("x", "", &task.SpawnSpec{}); err == nil {
		t.Fatal("a spawn task without a command should be refused")
	}
}
