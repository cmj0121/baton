package server

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// TestFreeForWork checks which states the backlog scheduler may hand a task to.
// done and stuck have to count: all three describe an agent that has stopped
// producing output, and leaving them out would shrink the pool to nothing on a
// fleet left alone for a minute.
func TestFreeForWork(t *testing.T) {
	cases := map[panel.State]bool{
		panel.Idle:      true,
		panel.Done:      true,
		panel.Stuck:     true,
		panel.Attention: false, // waiting on a human is not waiting for work
		panel.Running:   false,
		panel.Spawning:  false,
		panel.Exited:    false,
	}
	for st, want := range cases {
		if got := freeForWork(st); got != want {
			t.Errorf("freeForWork(%v) = %v, want %v", st, got, want)
		}
	}
}

// TestDispatchReadyCoversTheRestingStates checks a dispatch is delivered rather
// than held for every settled state. done and stuck must be in the set: a held
// dispatch is only released by a transition, and neither moves again without new
// output, so omitting them would strand a prompt forever.
func TestDispatchReadyCoversTheRestingStates(t *testing.T) {
	cases := map[panel.State]bool{
		panel.Idle:      true,
		panel.Attention: true,
		panel.Done:      true,
		panel.Stuck:     true,
		panel.Running:   false,
		panel.Spawning:  false,
		panel.Exited:    false,
	}
	for st, want := range cases {
		if got := dispatchReady(st); got != want {
			t.Errorf("dispatchReady(%v) = %v, want %v", st, got, want)
		}
	}
}

// TestDispatchToADonePanelIsDelivered is the same claim end to end: a panel that
// has been quiet long enough to be called done still takes a prompt now.
func TestDispatchToADonePanelIsDelivered(t *testing.T) {
	s, _, written := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Done})
	if err := s.dispatchPanel("p1", "next task", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(*written) != 1 {
		t.Fatalf("a done panel should receive the prompt at once, got %v", *written)
	}
	if len(s.pendingDispatch) != 0 {
		t.Error("nothing should be held for a settled panel")
	}
}
