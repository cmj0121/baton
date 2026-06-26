package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// TestDispatchKeyOpensOverlayAndSends checks the interactive dispatch flow on the
// dashboard: T on an agent opens the task overlay remembering the panel, and enter
// commits the typed brief.
func TestDispatchKeyOpensOverlayAndSends(t *testing.T) {
	m := baseModel()
	m.fleet = sampleFleet() // index 0 is an agent panel
	m.cursor = 0

	m = press(m, keyDispatch)
	if m.input != inputDispatch || m.dispatchID != "a1" {
		t.Fatalf("T should open the dispatch overlay for the agent, got input=%v id=%q", m.input, m.dispatchID)
	}

	m.inputBuf = "ship the login fix"
	m = press(m, "enter")
	if m.input != inputNone {
		t.Fatalf("enter should close the overlay, got input=%v", m.input)
	}
	if !strings.Contains(m.status, "dispatched") {
		t.Fatalf("commit should report the dispatch, got %q", m.status)
	}
	if m.dispatchID != "" {
		t.Fatalf("dispatch target should be cleared after commit, got %q", m.dispatchID)
	}
}

// TestDispatchKeyAgentOnly checks the UX gate: T on a shell panel hints to pick an
// agent instead of opening the overlay.
func TestDispatchKeyAgentOnly(t *testing.T) {
	m := baseModel()
	m.fleet = sampleFleet()
	m.cursor = 3 // a shell panel

	m = press(m, keyDispatch)
	if m.input != inputNone {
		t.Fatalf("T on a shell should not open an overlay, got input=%v", m.input)
	}
	if !strings.Contains(m.status, "agent panel") {
		t.Fatalf("expected an agent-only hint, got %q", m.status)
	}
}

// TestDispatchReassignSeedsBrief checks that dispatching a panel that already has a
// brief seeds the overlay with it, so the action edits the existing task.
func TestDispatchReassignSeedsBrief(t *testing.T) {
	m := baseModel()
	got := m.startDispatch(panel.Panel{ID: "9", Kind: panel.Agent, Task: "old brief"})
	if got.input != inputDispatch || got.inputBuf != "old brief" {
		t.Fatalf("re-assign should seed the current brief, got input=%v buf=%q", got.input, got.inputBuf)
	}
}

// TestDispatchGroupFromDashboard checks that the dispatch key on a selected work
// item opens the overlay for the whole group and commits a group dispatch.
func TestDispatchGroupFromDashboard(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet() // item 0 is the "api" group
	m.cursor = 0

	m = press(m, keyDispatch)
	if m.input != inputDispatch || m.dispatchGroup != "api" || m.dispatchID != "" {
		t.Fatalf("T on a group should target the group, got input=%v group=%q id=%q", m.input, m.dispatchGroup, m.dispatchID)
	}
	m.inputBuf = "refactor the api"
	m = press(m, "enter")
	if m.input != inputNone || !strings.Contains(m.status, "group") {
		t.Fatalf("group dispatch should commit, got input=%v status=%q", m.input, m.status)
	}
	if m.dispatchGroup != "" {
		t.Fatalf("group target should clear after commit, got %q", m.dispatchGroup)
	}
}

// TestDispatchGuards covers the commit guards: an empty brief is refused, and a
// commit with no remembered target is a no-op hint.
func TestDispatchGuards(t *testing.T) {
	m := baseModel()
	m.dispatchID = "1"
	if got := m.commitDispatch(""); !strings.Contains(got.status, "empty") || got.dispatchID != "" {
		t.Fatalf("empty brief should be refused and the target cleared, got status=%q id=%q", got.status, got.dispatchID)
	}
	empty := baseModel() // no remembered target
	if got := empty.commitDispatch("x"); !strings.Contains(got.status, "nothing") {
		t.Fatalf("commit with no target should hint, got %q", got.status)
	}
}
