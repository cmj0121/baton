package server

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/task"
)

// taskFor reads a panel's current task directly (in-package), for asserting the
// status the panel lifecycle drove it to.
func taskFor(s *Server, panelID string) *task.Task {
	tid, ok := s.panelTask[panelID]
	if !ok {
		return nil
	}
	return s.tasks[tid]
}

// TestTaskLifecycle walks a dispatched task through the whole lifecycle as the
// panel it runs on settles, wakes, and settles again: queued (held) → dispatched
// (delivered) → running (output) → done (settled).
func TestTaskLifecycle(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Spawning})

	if err := s.dispatchPanel("p1", "work it", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if tk := taskFor(s, "p1"); tk == nil || tk.Status != task.Queued || tk.Attempts != 1 || tk.ID != "t1" {
		t.Fatalf("a held dispatch should create a queued task t1, got %+v", tk)
	}

	clk.add(idleAfter) // the panel settles: the held dispatch is delivered
	s.monitorTick()
	if tk := taskFor(s, "p1"); tk.Status != task.Dispatched {
		t.Fatalf("delivery should move the task to dispatched, got %q", tk.Status)
	}

	s.routeOutput("p1", []byte("thinking…")) // the agent produces output
	if tk := taskFor(s, "p1"); tk.Status != task.Running {
		t.Fatalf("output should move the task to running, got %q", tk.Status)
	}

	clk.add(idleAfter) // the agent goes quiet again: its turn is done
	s.monitorTick()
	if tk := taskFor(s, "p1"); tk.Status != task.Done {
		t.Fatalf("settling after running should finish the task, got %q", tk.Status)
	}
}

// TestTaskReDispatchUpdatesSameTask checks that re-dispatching a panel whose task
// is still live updates that one task — bumping Attempts — rather than spawning a
// second; a dispatch after the task is terminal starts a fresh one.
func TestTaskReDispatchUpdatesSameTask(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Idle})

	if err := s.dispatchPanel("p1", "first", ""); err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	if err := s.dispatchPanel("p1", "second", ""); err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	tk := taskFor(s, "p1")
	if tk == nil || tk.ID != "t1" || tk.Attempts != 2 || tk.Prompt != "second" {
		t.Fatalf("re-dispatch should update the same task with attempts=2, got %+v", tk)
	}
	if s.TaskCount() != 1 {
		t.Fatalf("re-dispatch should not create a second task, count=%d", s.TaskCount())
	}

	// Drive the task terminal, then a fresh dispatch starts a new task id.
	s.advanceTaskLocked("p1", task.Running)
	s.advanceTaskLocked("p1", task.Done)
	if err := s.dispatchPanel("p1", "third", ""); err != nil {
		t.Fatalf("dispatch 3: %v", err)
	}
	if tk := taskFor(s, "p1"); tk == nil || tk.ID != "t2" || tk.Attempts != 1 {
		t.Fatalf("a dispatch after the task finished should start a new task t2, got %+v", tk)
	}
}

// TestTaskFailsOnExit checks that a task in flight is marked failed when its panel
// exits under it.
func TestTaskFailsOnExit(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Idle})
	if err := s.dispatchPanel("p1", "work", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	s.advanceTaskLocked("p1", task.Running)

	s.onPanelExit("p1", 1)
	if tk := taskFor(s, "p1"); tk == nil || tk.Status != task.Failed {
		t.Fatalf("a task should fail when its panel exits, got %+v", tk)
	}
}

// TestTaskTransitionGuards locks down the transition table: terminal is sticky and
// the lifecycle only moves forward (a settle does not finish a task that never ran).
func TestTaskTransitionGuards(t *testing.T) {
	if task.CanAdvance(task.Done, task.Running) {
		t.Fatal("a done task must not advance")
	}
	if task.CanAdvance(task.Dispatched, task.Done) {
		t.Fatal("a task must run before it can be done")
	}
	if !task.CanAdvance(task.Queued, task.Failed) {
		t.Fatal("any non-terminal task can fail")
	}
	if !task.CanAdvance(task.Running, task.Done) {
		t.Fatal("a running task can finish")
	}
}
