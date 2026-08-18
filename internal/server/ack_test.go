package server

import (
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// ackedOnWire is what a cockpit would actually see: the flag as it is joined onto
// the fleet snapshot, not the map behind it. Every assertion here goes through the
// wire because that is the only thing the inbox reads.
func ackedOnWire(s *Server, id string) bool {
	for _, p := range s.panelsMsg().Panels {
		if p.ID == id {
			return p.Acked
		}
	}
	return false
}

// TestAckStandsAndReachesTheWire. The record is fleet state, so it has to be
// visible to every cockpit, not just to the one that made it — that is the whole
// reason it lives on the daemon rather than in a model.
func TestAckStandsAndReachesTheWire(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Attention})
	cc := ctl("")

	if ackedOnWire(s, "a1") {
		t.Fatal("a fresh panel has not been dealt with")
	}
	s.onCommand(cc, proto.Command{Action: "panel.ack", ID: "a1"})
	if err := replyErr(cc); err != "" {
		t.Fatalf("ack: %v", err)
	}
	if !ackedOnWire(s, "a1") {
		t.Fatal("an acknowledgement should reach the wire")
	}
}

// TestDismissSurvivesAStateChangeButNotOutput is the contract dismiss exists for,
// and the reason "until its state changes" was rejected as a definition.
//
// A dismissed `done` that came back as `stuck` ten minutes later — on a timer the
// human did nothing to cause — is a resurrection, and a queue that resurrects rows
// is one people stop clearing. Output is the panel doing something; a timer
// expiring is not.
func TestDismissSurvivesAStateChangeButNotOutput(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Idle})

	clk.add(90 * time.Second)
	s.monitorTick()
	if got := stateOf(s, "a1"); got != panel.Done {
		t.Fatalf("the quiet ladder should reach done, got %v", got)
	}
	s.onCommand(ctl(""), proto.Command{Action: "panel.ack", ID: "a1"})

	// Ten more minutes of silence escalate it to stuck. The state changed; the
	// human did nothing; the row must stay cleared.
	clk.add(11 * time.Minute)
	s.monitorTick()
	if got := stateOf(s, "a1"); got != panel.Stuck {
		t.Fatalf("the ladder should escalate to stuck, got %v", got)
	}
	if !ackedOnWire(s, "a1") {
		t.Fatal("a timer-driven state change must not resurrect a dismissed row")
	}

	// One byte of output, though, is the panel speaking — a new claim on a human's
	// attention, and the acknowledgement stops standing.
	s.routeOutput("a1", []byte("resuming\n"))
	if ackedOnWire(s, "a1") {
		t.Fatal("output should end the acknowledgement")
	}
}

// TestAckSurvivesOutputUnderADeclaration is the case the wake edge alone would get
// wrong. An agent with a standing panel.attention never takes the quiet→noisy wake
// branch (its declaration outranks the output), so an acknowledgement dropped only
// on that edge would suppress its row for as long as the hand stayed up. The rule
// is "until the panel next produces output", and output is exactly what this is.
func TestAckSurvivesOutputUnderADeclaration(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running})
	s.declareAttention(ctl(""), proto.Command{Action: "panel.attention", ID: "a1", Reason: "which branch?"})
	s.onCommand(ctl(""), proto.Command{Action: "panel.ack", ID: "a1"})
	if !ackedOnWire(s, "a1") {
		t.Fatal("the ack should stand to begin with")
	}

	s.routeOutput("a1", []byte("still waiting…\n"))

	if got := stateOf(s, "a1"); got != panel.Attention {
		t.Fatalf("a declaration survives output, got %v", got)
	}
	if ackedOnWire(s, "a1") {
		t.Fatal("the panel spoke, so the acknowledgement should have fallen away")
	}
}

// TestSnoozeExpiresAgainstTheClock. `-` sends an absolute instant computed by the
// cockpit from its own settings.inbox-snooze; the daemon holds no policy of its
// own and simply stops honouring the record once that instant has passed.
func TestSnoozeExpiresAgainstTheClock(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Stuck})
	until := s.mon.now().Add(10 * time.Minute)

	s.onCommand(ctl(""), proto.Command{Action: "panel.ack", ID: "a1", Until: until.Format(time.RFC3339Nano)})
	if !ackedOnWire(s, "a1") {
		t.Fatal("a snooze should stand at once")
	}

	clk.add(9 * time.Minute)
	if !ackedOnWire(s, "a1") {
		t.Fatal("a snooze should still stand a minute before it expires")
	}
	clk.add(2 * time.Minute)
	if ackedOnWire(s, "a1") {
		t.Fatal("an expired snooze should put the row back in the queue")
	}
}

// TestAckRejectsAnUnparseableUntil. Silence is right for a race the human did not
// cause; it is wrong here. A snooze that quietly became a permanent dismiss is a
// row they will never see again and never asked to lose.
func TestAckRejectsAnUnparseableUntil(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Done})
	cc := ctl("")

	s.onCommand(cc, proto.Command{Action: "panel.ack", ID: "a1", Until: "in a bit"})

	if err := replyErr(cc); !strings.Contains(err, "bad until") {
		t.Errorf("a broken until should be named, got %q", err)
	}
	if ackedOnWire(s, "a1") {
		t.Error("a refused ack must not half-apply")
	}
}

// TestAckOnAnUnknownPanelIsSilent. A row can be reaped between the keystroke that
// cleared it and the command that carries it; an error popup for that race is
// noise, exactly as it is for panel.tail.
func TestAckOnAnUnknownPanelIsSilent(t *testing.T) {
	s, _, _ := gateServer()
	cc := ctl("")

	s.onCommand(cc, proto.Command{Action: "panel.ack", ID: "gone"})

	if err := replyErr(cc); err != "" {
		t.Errorf("an unknown id should be a quiet no-op, got %q", err)
	}
}

// TestAckIsDroppedWithThePanel. An acknowledgement is about a live process; when
// the process is gone, or the slot is, so is the record. Nothing here is
// persisted either — a daemon restart brings panels back as inert exited slots,
// and suppressing a row for one of those would be suppressing a row for a panel
// that no longer exists in the same sense.
func TestAckIsDroppedWithThePanel(t *testing.T) {
	// …when the process ends.
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Attention})
	s.onCommand(ctl(""), proto.Command{Action: "panel.ack", ID: "a1"})
	s.onPanelExit("a1", 0)
	if _, held := s.acked["a1"]; held {
		t.Error("an exit should drop the acknowledgement")
	}

	// …when the panel is closed.
	s2, _, _ := gateServer(panel.Panel{ID: "b1", Kind: panel.Agent, State: panel.Done})
	s2.onCommand(ctl(""), proto.Command{Action: "panel.ack", ID: "b1"})
	if err := s2.closePanel("b1"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, held := s2.acked["b1"]; held {
		t.Error("a close should drop the acknowledgement")
	}

	// …and when an exited slot is pruned to keep the fleet bounded.
	var dead []panel.Panel
	for i := range maxExitedPanels + 2 {
		dead = append(dead, panel.Panel{ID: string(rune('a' + i)), Kind: panel.Agent, State: panel.Exited, ExitCode: 1})
	}
	s3, _, _ := gateServer(dead...)
	s3.onCommand(ctl(""), proto.Command{Action: "panel.ack", ID: "a"})
	s3.mu.Lock()
	s3.pruneExitedLocked()
	s3.mu.Unlock()
	if _, held := s3.acked["a"]; held {
		t.Error("a prune should drop the acknowledgement with the slot")
	}
}

// TestConductorCannotAcknowledge is the other half of DESIGN §12's "no conductor
// inbox": the queue is an operator surface, and an agent clearing the fleet's
// attention queue on a human's behalf is a design this round deliberately did not
// build.
func TestConductorCannotAcknowledge(t *testing.T) {
	s, _, _ := gateServer(
		panel.Panel{ID: "c1", Kind: panel.Agent, State: panel.Running, Conductor: true},
		panel.Panel{ID: "w1", Kind: panel.Agent, State: panel.Attention},
	)
	cc := ctl("c1")
	cc.role = roleConductor

	s.onCommand(cc, proto.Command{Action: "panel.ack", ID: "w1"})

	msg := <-cc.out
	if msg.Type != "error" || !strings.Contains(msg.Error, "operator surface") {
		t.Fatalf("a conductor must be refused panel.ack, got %+v", msg)
	}
	if ackedOnWire(s, "w1") {
		t.Error("the refusal must not half-apply")
	}
}
