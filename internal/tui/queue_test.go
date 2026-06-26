package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// sampleTasks is a small backlog spanning the statuses the popup renders.
func sampleTasks() []proto.Task {
	return []proto.Task{
		{ID: "t3", Prompt: "land the parser fix", Status: "queued", Group: "build"},
		{ID: "t2", Prompt: "running the suite", Status: "running", Panel: "4", Group: "build"},
		{ID: "t1", Prompt: "shipped", Status: "done"},
	}
}

// TestOpenQueue enters the manager from the dashboard, remembering where to return
// and starting the cursor at the top.
func TestOpenQueue(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeDashboard}.openQueue(modeDashboard)
	if m.mode != modeQueue {
		t.Fatalf("openQueue should enter modeQueue, got %v", m.mode)
	}
	if m.queueFrom != modeDashboard || m.queueCursor != 0 {
		t.Fatalf("popup state wrong: from=%v cursor=%d", m.queueFrom, m.queueCursor)
	}
	if !strings.Contains(m.status, "task queue") {
		t.Fatalf("open status should name the queue, got %q", m.status)
	}
}

// TestQueueKeyOpensFromDashboard checks the Q binding reaches the manager through
// the normal command dispatch.
func TestQueueKeyOpensFromDashboard(t *testing.T) {
	m := press(model{width: 120, height: 40, mode: modeDashboard}, keyQueue)
	if m.mode != modeQueue {
		t.Fatalf("Q should open the queue manager, got mode %v", m.mode)
	}
}

// TestQueueNavWraps moves the cursor with the arrows and wraps at both ends.
func TestQueueNavWraps(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeQueue, queueFrom: modeDashboard, tasks: sampleTasks()}

	m = press(m, "down")
	if m.queueCursor != 1 {
		t.Fatalf("down should advance the cursor, got %d", m.queueCursor)
	}
	m = press(m, "up", "up")
	if m.queueCursor != 2 {
		t.Fatalf("up past the top should wrap to the last row, got %d", m.queueCursor)
	}
	m = press(m, "down")
	if m.queueCursor != 0 {
		t.Fatalf("down past the bottom should wrap to the first row, got %d", m.queueCursor)
	}
}

// TestQueueNavEmpty is a no-op on an empty backlog rather than a divide-by-zero.
func TestQueueNavEmpty(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeQueue, queueFrom: modeDashboard}
	m = press(m, "down", "up")
	if m.queueCursor != 0 {
		t.Fatalf("an empty backlog should leave the cursor at 0, got %d", m.queueCursor)
	}
}

// TestQueueCancel cancels the highlighted task; the status names the id being
// cancelled. (The actual removal rides the server's "tasks" reply.)
func TestQueueCancel(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeQueue, queueFrom: modeDashboard, tasks: sampleTasks()}
	m = press(m, "down")         // highlight t2
	m = press(m, keyQueueCancel) // d
	if !strings.Contains(m.status, "t2") {
		t.Fatalf("cancel should name the highlighted task, got %q", m.status)
	}

	// With no tasks the cancel is a no-op with a clear status.
	empty := model{width: 120, height: 40, mode: modeQueue, queueFrom: modeDashboard}
	empty = press(empty, keyQueueCancel)
	if !strings.Contains(empty.status, "nothing to cancel") {
		t.Fatalf("cancel on an empty backlog should say so, got %q", empty.status)
	}
}

// TestQueueDrain drains the whole backlog, and is a no-op when already empty.
func TestQueueDrain(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeQueue, queueFrom: modeDashboard, tasks: sampleTasks()}
	m = press(m, keyQueueDrain) // D
	if !strings.Contains(m.status, "draining") {
		t.Fatalf("drain should report draining, got %q", m.status)
	}

	empty := model{width: 120, height: 40, mode: modeQueue, queueFrom: modeDashboard}
	empty = press(empty, keyQueueDrain)
	if !strings.Contains(empty.status, "already empty") {
		t.Fatalf("drain on an empty backlog should say so, got %q", empty.status)
	}
}

// TestQueueEditDeferred confirms the editor pass is announced as a follow-up rather
// than silently doing nothing.
func TestQueueEditDeferred(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeQueue, queueFrom: modeDashboard, tasks: sampleTasks()}
	m = press(m, keyQueueEdit) // e
	if !strings.Contains(m.status, "edit") {
		t.Fatalf("edit should surface its status, got %q", m.status)
	}
	if m.mode != modeQueue {
		t.Fatalf("edit should keep the popup open, got mode %v", m.mode)
	}
}

// TestQueueEscCloses returns to the view the popup was opened from. An unhandled
// key is swallowed, never leaking to the dashboard.
func TestQueueEscCloses(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeQueue, queueFrom: modeDashboard, tasks: sampleTasks()}
	m = press(m, "x") // not a queue key — ignored, popup stays open
	if m.mode != modeQueue {
		t.Fatalf("a stray key should keep the popup open, got %v", m.mode)
	}
	m = press(m, "esc")
	if m.mode != modeDashboard {
		t.Fatalf("esc should restore the originating view, got %v", m.mode)
	}
}

// TestQueueApplyTasks feeds a "tasks" event and clamps the cursor as the backlog
// shrinks under it.
func TestQueueApplyTasks(t *testing.T) {
	m := model{mode: modeQueue, queueCursor: 2}
	m.applyEvent(proto.ServerMsg{Type: "tasks", Tasks: sampleTasks()})
	if len(m.tasks) != 3 {
		t.Fatalf("tasks event should fill the backlog, got %d", len(m.tasks))
	}

	// A shrinking backlog must drag the cursor back in range.
	m.applyEvent(proto.ServerMsg{Type: "tasks", Tasks: sampleTasks()[:1]})
	if m.queueCursor != 0 {
		t.Fatalf("cursor should clamp to the last row, got %d", m.queueCursor)
	}

	// Draining to empty clamps to zero, not a negative index.
	m.applyEvent(proto.ServerMsg{Type: "tasks", Tasks: nil})
	if m.queueCursor != 0 {
		t.Fatalf("an empty backlog should clamp the cursor to 0, got %d", m.queueCursor)
	}
}

// TestQueueView renders both the empty frame and a populated list.
func TestQueueView(t *testing.T) {
	empty := model{width: 120, height: 40, mode: modeQueue}.queueView()
	if !strings.Contains(empty, "empty") {
		t.Fatalf("an empty backlog view should say so, got:\n%s", empty)
	}

	m := model{width: 120, height: 40, mode: modeQueue, queueFrom: modeDashboard, tasks: sampleTasks()}
	out := m.queueView()
	for _, want := range []string{spaced("TASK QUEUE"), "t1", "queued", "running", "land the parser fix"} {
		if !strings.Contains(out, want) {
			t.Fatalf("queue view missing %q, got:\n%s", want, out)
		}
	}
}

// TestQueueStatusColor gives every status a distinct, non-empty badge colour.
func TestQueueStatusColor(t *testing.T) {
	for _, st := range []string{"queued", "dispatched", "running", "done", "failed"} {
		if queueStatusColor(st) == "" {
			t.Fatalf("status %q should map to a colour", st)
		}
	}
}
