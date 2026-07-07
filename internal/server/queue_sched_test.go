package server

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/task"
)

// TestScheduleAssignsToIdleAgent checks the scheduler drains an enqueued task onto
// a free idle agent on the next tick: the task is assigned and delivered, and the
// agent's card shows the brief.
func TestScheduleAssignsToIdleAgent(t *testing.T) {
	s, _, written := gateServer(panel.Panel{ID: "1", Kind: panel.Agent, State: panel.Idle, Group: "api"})

	id, err := s.enqueueTask("do it", "api", nil)
	if err != nil || id != "t1" {
		t.Fatalf("enqueue: id=%q err=%v", id, err)
	}
	if tk := s.tasks["t1"]; tk.Panel != "" || tk.Status != task.Queued {
		t.Fatalf("a fresh enqueue should be unassigned and queued, got %+v", tk)
	}

	s.monitorTick() // the scheduler drains the backlog
	tk := s.tasks["t1"]
	if tk.Panel != "1" || tk.Status != task.Dispatched {
		t.Fatalf("the task should be assigned and dispatched, got %+v", tk)
	}
	if len(*written) != 1 || (*written)[0] != "1:do it\n" {
		t.Fatalf("the prompt should be delivered to the agent, got %v", *written)
	}
	if s.panels[0].Task != "do it" {
		t.Fatalf("the agent's card should show the brief, got %q", s.panels[0].Task)
	}
}

// TestScheduleHonoursConcurrencyCap checks the per-group cap: with cap 1 and two
// idle agents, two queued tasks still only put one into flight at a time.
func TestScheduleHonoursConcurrencyCap(t *testing.T) {
	s, _, _ := gateServer(
		panel.Panel{ID: "1", Kind: panel.Agent, State: panel.Idle, Group: "api"},
		panel.Panel{ID: "2", Kind: panel.Agent, State: panel.Idle, Group: "api"},
	)
	s.queueConcurrency = 1
	if _, err := s.enqueueTask("a", "api", nil); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	if _, err := s.enqueueTask("b", "api", nil); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}

	s.monitorTick()
	assigned := 0
	for _, tk := range s.tasks {
		if tk.Panel != "" {
			assigned++
		}
	}
	if assigned != 1 {
		t.Fatalf("the concurrency cap should hold the group to 1 in flight, got %d", assigned)
	}
}

// TestScheduleRespectsGroup checks a grouped task is only handed to an agent in the
// same work item, never a free agent in another group.
func TestScheduleRespectsGroup(t *testing.T) {
	s, _, written := gateServer(panel.Panel{ID: "1", Kind: panel.Agent, State: panel.Idle, Group: "db"})
	if _, err := s.enqueueTask("api work", "api", nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	s.monitorTick()
	if len(*written) != 0 {
		t.Fatalf("a db agent must not take an api task, got %v", *written)
	}
	if tk := s.tasks["t1"]; tk.Panel != "" {
		t.Fatalf("the task should stay queued with no matching agent, got %+v", tk)
	}
}

// TestQueueMaxRejectsOverflow checks an enqueue past queueMax is refused, counting
// only the unassigned backlog.
func TestQueueMaxRejectsOverflow(t *testing.T) {
	s, _, _ := gateServer()
	s.queueMax = 1
	if _, err := s.enqueueTask("a", "", nil); err != nil {
		t.Fatalf("first enqueue should fit: %v", err)
	}
	if _, err := s.enqueueTask("b", "", nil); err == nil {
		t.Fatal("an enqueue past queue.max should be refused")
	}
}

// TestCancelAndDrain checks a queued task can be cancelled, an in-flight one cannot
// be cancelled via the queue, and drain clears the remaining backlog.
func TestCancelAndDrain(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "1", Kind: panel.Agent, State: panel.Idle})
	a, _ := s.enqueueTask("a", "", nil)
	b, _ := s.enqueueTask("b", "", nil)
	c, _ := s.enqueueTask("c", "", nil)

	if err := s.cancelTask(a); err != nil {
		t.Fatalf("cancel queued: %v", err)
	}
	if _, ok := s.tasks[a]; ok {
		t.Fatal("a cancelled task should be gone")
	}

	// Put b in flight, then it cannot be cancelled through the queue.
	s.monitorTick()
	if tk := s.tasks[b]; tk.Panel == "" {
		// b or c may have been the one scheduled; pick whichever got assigned.
		t.Logf("b assigned=%v", tk)
	}
	var assignedID string
	for id, tk := range s.tasks {
		if tk.Panel != "" {
			assignedID = id
		}
	}
	if assignedID != "" {
		if err := s.cancelTask(assignedID); err == nil {
			t.Fatal("an in-flight task must not be cancellable via the queue")
		}
	}

	if n := s.drainQueued(); n < 1 {
		t.Fatalf("drain should clear the remaining queued task(s), dropped %d", n)
	}
	for id, tk := range s.tasks {
		if tk.Panel == "" && tk.Status == task.Queued {
			t.Fatalf("drain left a queued task %s", id)
		}
	}
	_ = c
}
