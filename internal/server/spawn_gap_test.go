package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestSpawnGapThrottlesAcrossConnections is the OTHER half of the refine-cap
// bug, and it was there from the day minConductorSpawnGap was written.
//
// The gap existed precisely because an LLM will loop, and it kept its stamp on
// the clientConn — so it throttled a conductor holding one socket open and did
// nothing at all to `baton_spawn`, which goes through the same dial-per-tool-call
// MCP path the refine verbs do, or to `baton ctl spawn`, which is a process per
// command. It was cured by the same change: both caps are keyed on the
// conductor's PANEL now, through Server.gapStamp.
//
// TestConductorGuardrails covers the persistent-connection case and passed
// throughout, which is exactly why this one has to exist beside it.
func TestSpawnGapThrottlesAcrossConnections(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "c1", Kind: panel.Agent, State: panel.Running, Conductor: true})
	create := proto.Command{Action: "panel.create", Kind: "shell"}

	conductor := func() *clientConn {
		cc := conn("c1")
		cc.role = roleConductor
		return cc
	}
	if reason := s.guardConductor(conductor(), create); reason != "" {
		t.Fatalf("the first spawn was refused: %q", reason)
	}
	// Twenty fresh connections, each declaring the same conductor panel — the
	// shape of a looping agent. Every one of them must be refused.
	for i := range 20 {
		if reason := s.guardConductor(conductor(), create); !strings.Contains(reason, "too fast") {
			t.Fatalf("fresh connection %d was admitted (%q); the spawn cap must not be per-connection", i, reason)
		}
	}

	// The clock is the only thing holding it.
	rewind(s, &s.spawn, 2*minConductorSpawnGap)
	if reason := s.guardConductor(conductor(), create); reason != "" {
		t.Fatalf("a spawn past the gap was refused: %q", reason)
	}

	// A DIFFERENT conductor identity is not throttled by this one's stamp. The
	// singleton makes that hypothetical today, and the assertion is what says the
	// cap fences a caller rather than the verb.
	other := conn("c2")
	other.role = roleConductor
	if reason := s.guardConductor(other, create); reason != "" {
		t.Fatalf("a different panel was throttled by another's stamp: %q", reason)
	}
}

// TestFleetCapacityDoesNotSpendTheSpawnSlot: the capacity refusal is about the
// fleet, not about this caller, so being told the fleet is full must not also
// cost the caller its next quarter-second. The two refusals are ordered for that
// reason and the order is easy to swap back by accident.
func TestFleetCapacityDoesNotSpendTheSpawnSlot(t *testing.T) {
	panels := make([]panel.Panel, 0, maxConductorFleet)
	panels = append(panels, panel.Panel{ID: "c1", Kind: panel.Agent, State: panel.Running, Conductor: true})
	for i := 1; i < maxConductorFleet; i++ {
		panels = append(panels, panel.Panel{ID: fmt.Sprintf("p%d", i), Kind: panel.Shell})
	}
	s, _, _ := gateServer(panels...)
	cc := conn("c1")
	cc.role = roleConductor
	create := proto.Command{Action: "panel.create", Kind: "shell"}

	if reason := s.guardConductor(cc, create); !strings.Contains(reason, "capacity") {
		t.Fatalf("a full fleet answered %q, want the capacity refusal", reason)
	}
	// Room is made; the very next attempt is admitted rather than rate-refused.
	s.mu.Lock()
	s.panels = s.panels[:1]
	s.mu.Unlock()
	if reason := s.guardConductor(cc, create); reason != "" {
		t.Fatalf("the spawn after a capacity refusal answered %q, want it admitted", reason)
	}
}
