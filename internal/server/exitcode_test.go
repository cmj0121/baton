package server

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestExitCodeReachesTheWire checks the fact the cockpit draws `failed` from: a
// panel's exit status is recorded when its process ends and travels on the
// snapshot. There is no failed state — the daemon reports the code and the
// frontend concludes — so this is the whole of the server's side of it.
func TestExitCodeReachesTheWire(t *testing.T) {
	s, _, _ := gateServer(
		panel.Panel{ID: "bad", Kind: panel.Agent, State: panel.Running, Reason: "which migration?"},
		panel.Panel{ID: "good", Kind: panel.Shell, State: panel.Running},
	)

	s.onPanelExit("bad", 3)
	s.onPanelExit("good", 0)

	wire := map[string]int{}
	for _, p := range s.panelsMsg().Panels {
		wire[p.ID] = p.ExitCode
	}
	if wire["bad"] != 3 {
		t.Errorf("a non-zero exit should reach the wire, got %d", wire["bad"])
	}
	if wire["good"] != 0 {
		t.Errorf("a clean exit carries no code, got %d", wire["good"])
	}
	if i := s.indexLocked("bad"); s.panels[i].Reason != "" {
		t.Errorf("a dead process is not asking for anything, got %q", s.panels[i].Reason)
	}
}

// TestWirePanelJoinsTheSameFieldsEverywhere is the regression guard for the two
// snapshot builders drifting: the "panels" snapshot used to join the pid while
// the "telemetry" frame did not. Both now go through wirePanel, so both carry
// the joined state clock, and neither can gain a field the other lacks without
// this failing.
func TestWirePanelJoinsTheSameFieldsEverywhere(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	s.clients[&clientConn{out: make(chan proto.ServerMsg, 8), attached: map[string]bool{}}] = struct{}{}

	clk.add(idleAfter)
	tick, ok := s.monitorTick()
	if !ok || tick.Type != "telemetry" || len(tick.Panels) != 1 {
		t.Fatalf("expected a telemetry frame, got %+v", tick)
	}
	snap := s.panelsMsg()

	if tick.Panels[0].Since == "" {
		t.Error("telemetry should carry the state clock")
	}
	if snap.Panels[0].Since == "" {
		t.Error("the snapshot should carry the state clock")
	}
	if tick.Panels[0] != snap.Panels[0] {
		t.Errorf("the two builders disagree:\n telemetry %+v\n snapshot  %+v", tick.Panels[0], snap.Panels[0])
	}

	// The instant is the one the Monitor recorded, not the one the message was
	// built at, so a queue sorting on it sorts by when the panel settled.
	if got, err := time.Parse(time.RFC3339Nano, snap.Panels[0].Since); err != nil {
		t.Fatalf("Since is not an RFC 3339 instant: %v", err)
	} else if !got.Equal(s.mon.enteredAt("p1")) {
		t.Errorf("Since = %v, want the state-entry instant %v", got, s.mon.enteredAt("p1"))
	}
}

// TestExitedPanelStillCarriesAClock is the other half of the exit code reaching
// the wire: a queue that lists failures has to order them, and the Monitor
// forgets a panel the moment it dies. The exit instant stands in, so a dead
// panel is never the one row with nothing to sort on.
func TestExitedPanelStillCarriesAClock(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	s.onPanelExit("p1", 1)

	wire := s.panelsMsg().Panels[0]
	if wire.Since == "" {
		t.Fatal("an exited panel should still carry a state clock")
	}
	if _, err := time.Parse(time.RFC3339Nano, wire.Since); err != nil {
		t.Fatalf("Since is not an RFC 3339 instant: %v", err)
	}
	if !s.mon.enteredAt("p1").IsZero() {
		t.Fatal("the Monitor should have forgotten the dead panel, so this is the fallback path")
	}
}
