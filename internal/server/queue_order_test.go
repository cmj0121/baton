package server

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/task"
)

// TestReprioritizeOrdersScheduler checks that promoting/demoting queued tasks
// changes the order the scheduler drains them: with three tasks waiting and one
// idle agent, the promoted task is the one assigned first.
func TestReprioritizeOrdersScheduler(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Idle})

	id1, _ := s.enqueueTask("first", "", nil)
	_, _ = s.enqueueTask("second", "", nil)
	id3, _ := s.enqueueTask("third", "", nil)

	if err := s.reprioritizeTask(id3, true); err != nil { // third → head
		t.Fatalf("promote: %v", err)
	}
	if err := s.reprioritizeTask(id1, false); err != nil { // first → tail
		t.Fatalf("demote: %v", err)
	}

	s.mu.Lock()
	deliver, _ := s.scheduleLocked()
	s.mu.Unlock()

	if len(deliver) != 1 {
		t.Fatalf("one idle agent should take exactly one task, got %d", len(deliver))
	}
	if got := s.tasks[id3]; got.Panel != "a1" || got.Status != task.Dispatched {
		t.Fatalf("the promoted task should be scheduled first, got %+v", got)
	}
}

// TestReprioritizeRefusesInFlight checks that only a waiting task can be reordered:
// a task already dispatched onto a panel is refused, like cancel.
func TestReprioritizeRefusesInFlight(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Idle})
	if err := s.dispatchPanel("a1", "work", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	tid := taskFor(s, "a1").ID
	if err := s.reprioritizeTask(tid, true); err == nil {
		t.Fatal("reordering an in-flight task should be refused")
	}
}
