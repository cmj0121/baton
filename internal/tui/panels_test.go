package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// TestStatesCoverEveryLifecycleState checks the presentation map has an entry
// for every state the domain model can be in — a missing one renders as a blank
// LED with an empty label, which reads as "nothing here" rather than as a bug.
func TestStatesCoverEveryLifecycleState(t *testing.T) {
	for _, st := range []panel.State{
		panel.Spawning, panel.Running, panel.Idle, panel.Attention, panel.Exited, panel.Done, panel.Stuck,
	} {
		info, ok := states[st]
		if !ok || info.led == "" || info.label == "" || info.color == "" {
			t.Errorf("%v has no presentation: %+v", st, info)
		}
		if info.label != st.String() {
			t.Errorf("%v label = %q, want the wire name %q", st, info.label, st.String())
		}
	}
}

// TestStateInfoFor covers the one override the states map cannot express: an
// exited panel that exited badly renders as failed, while a clean exit stays an
// ordinary exit. Every other state ignores the exit code, which is meaningless
// on a live panel.
func TestStateInfoFor(t *testing.T) {
	cases := []struct {
		name  string
		p     panel.Panel
		label string
	}{
		{"a clean exit is just exited", panel.Panel{State: panel.Exited}, "exited"},
		{"a non-zero exit is failed", panel.Panel{State: panel.Exited, ExitCode: 1}, "failed"},
		{"a signal death is failed too", panel.Panel{State: panel.Exited, ExitCode: -1}, "failed"},
		{"a live panel ignores a stale code", panel.Panel{State: panel.Running, ExitCode: 2}, "running"},
		{"done renders as done", panel.Panel{State: panel.Done}, "done"},
		{"stuck renders as stuck", panel.Panel{State: panel.Stuck}, "stuck"},
	}
	for _, tc := range cases {
		if got := stateInfoFor(tc.p).label; got != tc.label {
			t.Errorf("%s: label = %q, want %q", tc.name, got, tc.label)
		}
	}
	if got := stateInfoFor(panel.Panel{State: panel.Exited, ExitCode: 1}); got.color != colFailed || got.led == states[panel.Exited].led {
		t.Errorf("failed should have its own colour and glyph, got %+v", got)
	}
}

// TestStateOrderAndRank pins the two orderings the dashboard reads: the summary
// strip's chips run loudest first, and a group rolls up to its most urgent
// member — with done and stuck above running, since both want a human and
// running does not.
func TestStateOrderAndRank(t *testing.T) {
	want := []panel.State{
		panel.Attention, panel.Stuck, panel.Running, panel.Done, panel.Idle, panel.Spawning, panel.Exited,
	}
	if len(stateOrder) != len(want) {
		t.Fatalf("stateOrder = %v, want %v", stateOrder, want)
	}
	for i, st := range want {
		if stateOrder[i] != st {
			t.Errorf("stateOrder[%d] = %v, want %v", i, stateOrder[i], st)
		}
	}

	ranked := []panel.State{
		panel.Attention, panel.Stuck, panel.Done, panel.Running, panel.Spawning, panel.Idle, panel.Exited,
	}
	for i := 1; i < len(ranked); i++ {
		if stateRank[ranked[i-1]] <= stateRank[ranked[i]] {
			t.Errorf("%v should outrank %v", ranked[i-1], ranked[i])
		}
	}

	// The rollup follows the rank, so one wedged member speaks for the card.
	members := []panel.Panel{{State: panel.Running}, {State: panel.Stuck}, {State: panel.Idle}}
	if got := groupState(members); got != panel.Stuck {
		t.Errorf("groupState = %v, want stuck", got)
	}
	if got := groupState([]panel.Panel{{State: panel.Done}, {State: panel.Running}}); got != panel.Done {
		t.Errorf("groupState = %v, want done", got)
	}
}

// TestActiveState checks which states animate. done and stuck deliberately do
// not: nothing is happening in either, and a breathing card would say otherwise.
func TestActiveState(t *testing.T) {
	for st, want := range map[panel.State]bool{
		panel.Running:   true,
		panel.Attention: true,
		panel.Spawning:  true,
		panel.Idle:      false,
		panel.Done:      false,
		panel.Stuck:     false,
		panel.Exited:    false,
	} {
		if got := activeState(st); got != want {
			t.Errorf("activeState(%v) = %v, want %v", st, got, want)
		}
	}
}
