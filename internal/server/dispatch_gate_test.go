package server

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/task"
)

// gateServer builds an in-process server with an injected clock and a recording
// input writer, so the dispatch-gating state machine is exercised without a real
// PTY or wall-clock sleeps.
func gateServer(panels ...panel.Panel) (*Server, *fakeClock, *[]string) {
	mo, clk := newTestMonitor()
	written := &[]string{}
	s := &Server{
		pty:             ptymgr.New(),
		clients:         map[*clientConn]struct{}{},
		mon:             mo,
		panels:          panels,
		pendingDispatch: map[string][]byte{},
		tasks:           map[string]*task.Task{},
		panelTask:       map[string]string{},
		spawning:        map[string]bool{},
		specs:           map[string]ptymgr.Spec{},
	}
	s.writeInput = func(id string, data []byte) { *written = append(*written, id+":"+string(data)) }
	for _, p := range panels {
		mo.spawned(p.ID)
	}
	return s, clk, written
}

// TestDispatchHeldUntilSettle checks the ready-gate: a dispatch to a panel that is
// still spawning records the brief immediately but holds the bytes, then delivers
// them once the monitor sees the panel settle to idle.
func TestDispatchHeldUntilSettle(t *testing.T) {
	s, clk, written := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Spawning})

	if err := s.dispatchPanel("p1", "do it", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(*written) != 0 {
		t.Fatalf("a dispatch to a spawning panel must be held, got %v", *written)
	}
	if s.panels[0].Task != "do it" {
		t.Fatalf("the brief should record immediately, got %q", s.panels[0].Task)
	}
	if len(s.pendingDispatch) != 1 {
		t.Fatalf("the dispatch should be queued, pending=%d", len(s.pendingDispatch))
	}

	clk.add(idleAfter) // the panel goes quiet
	s.monitorTick()
	if len(*written) != 1 || (*written)[0] != "p1:do it\n" {
		t.Fatalf("settling should deliver the held dispatch, got %v", *written)
	}
	if len(s.pendingDispatch) != 0 {
		t.Fatalf("pending should drain after delivery, pending=%d", len(s.pendingDispatch))
	}
}

// TestDispatchImmediateWhenReady checks that a dispatch to an already-settled
// panel is written at once, with no pending entry.
func TestDispatchImmediateWhenReady(t *testing.T) {
	s, _, written := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Idle})

	if err := s.dispatchPanel("p1", "go", "\r"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(*written) != 1 || (*written)[0] != "p1:go\r" {
		t.Fatalf("a settled panel should deliver immediately with the given submit, got %v", *written)
	}
	if len(s.pendingDispatch) != 0 {
		t.Fatalf("no dispatch should be held, pending=%d", len(s.pendingDispatch))
	}
}

// TestDispatchSupersedesHeld checks that a fresh immediate dispatch replaces a
// dispatch still held for the same panel, so only the latest brief is delivered.
func TestDispatchSupersedesHeld(t *testing.T) {
	s, clk, written := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Spawning})

	if err := s.dispatchPanel("p1", "first", ""); err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	// The panel settles, then a new dispatch lands while it is idle: delivered now,
	// and the held "first" is dropped rather than delivered later.
	clk.add(idleAfter)
	s.monitorTick()
	if err := s.dispatchPanel("p1", "second", ""); err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	if len(s.pendingDispatch) != 0 {
		t.Fatalf("an immediate dispatch should leave nothing held, pending=%d", len(s.pendingDispatch))
	}
	want := []string{"p1:first\n", "p1:second\n"}
	if len(*written) != 2 || (*written)[0] != want[0] || (*written)[1] != want[1] {
		t.Fatalf("delivered %v, want %v", *written, want)
	}
}

// TestDispatchGroupFansAndSkipsConductor checks that a group dispatch reaches every
// member of the work item but never the conductor, and counts what it reached.
func TestDispatchGroupFansAndSkipsConductor(t *testing.T) {
	s, _, written := gateServer(
		panel.Panel{ID: "1", Kind: panel.Agent, State: panel.Idle, Group: "api"},
		panel.Panel{ID: "2", Kind: panel.Agent, State: panel.Idle, Group: "api", Conductor: true},
		panel.Panel{ID: "3", Kind: panel.Agent, State: panel.Idle, Group: "api"},
		panel.Panel{ID: "4", Kind: panel.Agent, State: panel.Idle, Group: "db"},
	)

	n, err := s.dispatchGroup("api", "refactor", "")
	if err != nil {
		t.Fatalf("dispatch-group: %v", err)
	}
	if n != 2 {
		t.Fatalf("group api has 2 non-conductor members, reached %d", n)
	}
	if len(*written) != 2 {
		t.Fatalf("expected 2 deliveries (conductor skipped), got %v", *written)
	}
	for _, w := range *written {
		if w == "2:refactor\n" {
			t.Fatalf("the conductor must never be a group-dispatch target, got %v", *written)
		}
	}
	// Both api agents recorded the brief; the db agent did not.
	if s.panels[0].Task != "refactor" || s.panels[2].Task != "refactor" {
		t.Fatalf("api members should carry the brief, got %q / %q", s.panels[0].Task, s.panels[2].Task)
	}
	if s.panels[3].Task != "" {
		t.Fatalf("a member of another group must be untouched, got %q", s.panels[3].Task)
	}
}

// TestDispatchGroupErrors checks the guard rails: an empty group, an empty prompt,
// and a group with no member each error.
func TestDispatchGroupErrors(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "1", Kind: panel.Agent, State: panel.Idle, Group: "api"})
	for _, tc := range []struct{ group, prompt string }{
		{"", "x"},
		{"api", ""},
		{"ghost", "x"},
	} {
		if _, err := s.dispatchGroup(tc.group, tc.prompt, ""); err == nil {
			t.Fatalf("dispatchGroup(%q,%q) should error", tc.group, tc.prompt)
		}
	}
}

// TestDispatchExitDropsHeld checks that a held dispatch is discarded when the panel
// exits, so it never fires at a dead process.
func TestDispatchExitDropsHeld(t *testing.T) {
	s, _, written := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Spawning})
	if err := s.dispatchPanel("p1", "do it", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(s.pendingDispatch) != 1 {
		t.Fatalf("dispatch should be held, pending=%d", len(s.pendingDispatch))
	}

	s.onPanelExit("p1", 0)
	if len(s.pendingDispatch) != 0 {
		t.Fatalf("exit should drop the held dispatch, pending=%d", len(s.pendingDispatch))
	}
	if len(*written) != 0 {
		t.Fatalf("nothing should be delivered to an exited panel, got %v", *written)
	}
}
