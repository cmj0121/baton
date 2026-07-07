package tui

import (
	"strings"
	"testing"
)

// TestEnqueueKeyFromGroup checks the cockpit enqueue flow on a selected work item:
// t opens the enqueue overlay restricted to that group, and enter queues the brief
// (a task.enqueue, not an immediate dispatch).
func TestEnqueueKeyFromGroup(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet() // item 0 is the "api" group
	m.cursor = 0

	m = press(m, keyEnqueue)
	if m.input != inputEnqueue || m.enqueueGroup != "api" {
		t.Fatalf("t on a group should open the enqueue overlay for it, got input=%v group=%q", m.input, m.enqueueGroup)
	}
	m.inputBuf = "sweep the api for nil checks"
	m = press(m, "enter")
	if m.input != inputNone {
		t.Fatalf("enter should close the overlay, got input=%v", m.input)
	}
	if !strings.Contains(m.status, "enqueued") {
		t.Fatalf("commit should report the enqueue, got %q", m.status)
	}
	if m.enqueueGroup != "" {
		t.Fatalf("enqueue group should clear after commit, got %q", m.enqueueGroup)
	}
}

// TestEnqueueKeyAnyAgent checks that enqueue with an ungrouped panel selected takes
// no group — the task is queued for any free agent.
func TestEnqueueKeyAnyAgent(t *testing.T) {
	m := baseModel()
	m.fleet = sampleFleet() // index 0 is an ungrouped agent panel
	m.cursor = 0

	m = press(m, keyEnqueue)
	if m.input != inputEnqueue || m.enqueueGroup != "" {
		t.Fatalf("t on an ungrouped panel should enqueue for any agent, got input=%v group=%q", m.input, m.enqueueGroup)
	}
}

// TestEnqueueGuardEmpty covers the commit guard: an empty brief is refused and the
// remembered group cleared, so a stray enter never queues a blank task.
func TestEnqueueGuardEmpty(t *testing.T) {
	m := baseModel()
	m.enqueueGroup = "api"
	got := m.commitEnqueue("")
	if !strings.Contains(got.status, "empty") || got.enqueueGroup != "" {
		t.Fatalf("empty brief should be refused and the group cleared, got status=%q group=%q", got.status, got.enqueueGroup)
	}
}
