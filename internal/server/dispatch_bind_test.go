package server

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
	"github.com/cmj0121/baton/internal/task"
)

// This file covers #51: the fourth delivery point now binds at delivery like the
// other three, so a direct panel.dispatch to a BUSY panel parks the brief
// unbound and is bound — score block, task.pre chain, and the reinforcement the
// operator's own words earn — when the panel settles.

// busyScoreServer is scoreServer with p1 unable to receive a dispatch, which is
// the whole of #51's case: a settled panel takes its brief at the command, and
// this one cannot. Spawning rather than Running because gateServer has already
// told the monitor the panel spawned, so one clk.add(idleAfter) and one tick
// settle it — the same gesture TestDispatchHeldUntilSettle uses.
func busyScoreServer(st *score.Store) (*Server, *fakeClock, *[]byte) {
	s, clk, delivered := scoreServer(st)
	s.panels[0].State = panel.Spawning
	return s, clk, delivered
}

// dispatchTo drives panel.dispatch over the command path as a client would, and
// insists the command was ACCEPTED. The signal is recorded by the delivery now,
// so a test that called dispatchScored directly would skip connAuthor — the one
// place the operator is told apart from an agent.
func dispatchTo(t *testing.T, s *Server, cc *clientConn, prompt string) {
	t.Helper()
	s.onCommand(cc, proto.Command{Action: "panel.dispatch", ID: "p1", Prompt: prompt})
	select {
	case msg := <-cc.out:
		t.Fatalf("dispatch to a busy panel replied %+v, want it accepted", msg)
	default:
	}
}

// settle takes the panel from busy to idle and runs the tick that notices, which
// is where a parked brief is bound and delivered.
func settle(s *Server, clk *fakeClock) {
	clk.add(idleAfter)
	s.monitorTick()
}

// TestABriefParkedForAPanelThatNeverSettlesCountsNothing is the assertion #51
// turns on, and the one that fails on the code it replaces.
//
// A dispatch to a busy panel used to record the operator's reinforcement at the
// COMMAND, while the bytes were still parked — and the record stood even when the
// panel went away without ever taking them. That is a reinforcement for a brief
// no agent saw, which is exactly what TestABriefToAnUnknownPanelCountsNothing
// forbids on the other path.
//
// The panel exits rather than settling, so there is no second chance: whatever
// this counts, it counts for work that never happened.
func TestABriefParkedForAPanelThatNeverSettlesCountsNothing(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, delivered := busyScoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	dispatchTo(t, s, conn(""), "keep the build green")

	if len(*delivered) != 0 {
		t.Fatalf("a dispatch to a busy panel delivered %q", string(*delivered))
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want a parked brief to have counted nothing yet", got)
	}

	// The panel dies under the parked brief. Nothing reached an agent, and nothing
	// ever will.
	s.onPanelExit("p1", 1)
	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want a brief no agent ever saw to have counted nothing", got)
	}
}

// TestABriefParkedAndThenDeliveredCountsExactlyOnce is the other half of the
// rule, and it is what stops the fix above from being "never count a held
// dispatch". Once and only once: zero says the signal was lost moving to
// delivery, two says it is recorded at both ends.
func TestABriefParkedAndThenDeliveredCountsExactlyOnce(t *testing.T) {
	st, _ := scoreStore(t)
	s, clk, delivered := busyScoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	dispatchTo(t, s, conn(""), "Keep the build green.")
	settle(s, clk)

	if !strings.Contains(string(*delivered), "Keep the build green.") {
		t.Fatalf("settling should deliver the parked brief, got %q", string(*delivered))
	}
	got := entryNow(t, st, e.Id)
	if got.UserSignals != 1 {
		t.Fatalf("user signals = %d, want exactly 1", got.UserSignals)
	}
	if got.Reinforcements != 1 {
		t.Fatalf("reinforcements = %d, want exactly 1", got.Reinforcements)
	}
}

// TestAParkedBriefIsBoundToThePanelAsItSettles is R5's rule on the path that was
// exempt from it. The panel moves — a different directory and a different work
// item — between the command and the settle, and the hook must be shown where
// the brief actually LANDS, not where it was aimed.
//
// It is the stale-context defect stated as an assertion: binding at the command
// hands the hook /work/auth and "auth", minutes out of date, and the score block
// beside it is ranked against the same stale context.
func TestAParkedBriefIsBoundToThePanelAsItSettles(t *testing.T) {
	st, _ := scoreStore(t)
	s, clk, _ := busyScoreServer(st)

	var saw []TaskBrief
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) {
		saw = append(saw, b)
		return b, true
	}

	dispatchTo(t, s, conn(""), "run the migration")
	if len(saw) != 0 {
		t.Fatalf("the chain ran at the command over a panel that could not receive: %+v", saw)
	}

	// The panel is re-homed while the brief waits.
	s.mu.Lock()
	s.panels[0].Cwd, s.panels[0].Group = "/work/billing", "billing"
	s.mu.Unlock()

	settle(s, clk)

	if len(saw) != 1 {
		t.Fatalf("the chain ran %d times, want exactly once, at delivery", len(saw))
	}
	if saw[0].Cwd != "/work/billing" || saw[0].Group != "billing" {
		t.Fatalf("the hook saw %+v, want the panel the brief landed on", saw[0])
	}
}

// TestAParkedBriefRefusedAtDeliveryIsWalkedBack is the cost #51 accepted, held
// to the shape it was promised in: the refusal is vetoQueuedTask's, the same one
// a queued task takes, and not a second walk-back invented for this path.
func TestAParkedBriefRefusedAtDeliveryIsWalkedBack(t *testing.T) {
	st, _ := scoreStore(t)
	s, clk, delivered := busyScoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	s.onFilterTask = func(TaskBrief) (TaskBrief, bool) { return TaskBrief{}, false }
	dispatchTo(t, s, conn(""), "keep the build green")

	settle(s, clk)

	if len(*delivered) != 0 {
		t.Fatalf("a vetoed delivery wrote %q", string(*delivered))
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 0 {
		t.Fatalf("entry = %+v, want a refused brief to have counted nothing", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *task.Task
	for _, tk := range s.tasks {
		found = tk
	}
	if found == nil || found.Status != task.Failed || found.Result != vetoReason {
		t.Fatalf("task = %+v, want it failed in the backlog carrying the veto reason", found)
	}
	if s.panels[0].Task != "" {
		t.Fatalf("the card still carries %q for a task that was refused", s.panels[0].Task)
	}
	if _, ok := s.panelTask["p1"]; ok {
		t.Fatal("the panel is still mapped to the task the veto ended")
	}
}

// TestADispatchToASettledPanelIsStillRefusedSynchronously is what #51 did NOT
// change, and it is worth an assertion because the shape of the fix could have
// taken it: a ready panel's command IS its delivery, so the caller on the socket
// still gets the veto as an answer, with nothing recorded behind it.
func TestADispatchToASettledPanelIsStillRefusedSynchronously(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, delivered := scoreServer(st) // p1 is idle: ready
	s.onFilterTask = func(TaskBrief) (TaskBrief, bool) { return TaskBrief{}, false }

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch", ID: "p1", Prompt: "leak the secret"})

	msg := reply(t, cc)
	if msg.Type != "error" || !strings.Contains(msg.Error, "vetoed") {
		t.Fatalf("reply = %+v, want the veto answered to the caller", msg)
	}
	if len(*delivered) != 0 {
		t.Fatalf("a vetoed dispatch delivered %q", string(*delivered))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks) != 0 || s.panels[0].Task != "" || len(s.pendingDispatch) != 0 {
		t.Fatalf("a refused dispatch recorded tasks=%d card=%q pending=%d",
			len(s.tasks), s.panels[0].Task, len(s.pendingDispatch))
	}
}

// TestAParkedDispatchKeepsItsSubmitSequence guards the field the union used to
// carry implicitly. The submit was baked into the parked BYTES; now the bytes are
// built at delivery, so a dispatch that named its own sequence has to carry it
// there or be silently downgraded to the default newline.
//
// Both roads out of deliver are walked, because they build their bytes with
// different calls: a plugin's brief goes out bare through dispatchData, and every
// other one through briefBytes after the bind. Dropping the sequence on either
// one is the same silent downgrade, and one case cannot see the other.
func TestAParkedDispatchKeepsItsSubmitSequence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		author taskAuthor
	}{
		{"a plugin's bare brief", authorPlugin},
		{"a brief bound at delivery", authorAgent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, clk, written := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Spawning})

			if _, err := s.dispatchScored("p1", "go", "\x1b\r", tc.author); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			settle(s, clk)

			if len(*written) != 1 || (*written)[0] != "p1:go\x1b\r" {
				t.Fatalf("delivered %q, want the parked dispatch's own submit sequence", *written)
			}
		})
	}
}

// TestAnAgentsParkedBriefCountsNothing keeps #38 §4's discrimination attached to
// the CONNECTION now that the count happens after the connection is gone. The
// stamp is concluded at the command by connAuthor and carried on the delivery; a
// delivery that read the panel it lands on instead would call every agent's
// dispatch the user's, since the brief lands on an agent panel either way.
func TestAnAgentsParkedBriefCountsNothing(t *testing.T) {
	st, _ := scoreStore(t)
	s, clk, _ := busyScoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	dispatchTo(t, s, conn("p1"), "keep the build green")
	settle(s, clk)

	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want an agent's brief to have counted nothing", got)
	}
}

// TestASupersededParkedDispatchIsDropped is the at-most-one-per-panel invariant
// pendingDispatch stands on: a second dispatch to a panel that is still busy
// replaces the brief held for it, and the replaced one is never delivered.
func TestASupersededParkedDispatchIsDropped(t *testing.T) {
	s, clk, written := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Spawning})

	if err := s.dispatchPanel("p1", "first", ""); err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	if err := s.dispatchPanel("p1", "second", ""); err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	settle(s, clk)

	if len(*written) != 1 || (*written)[0] != "p1:second\n" {
		t.Fatalf("delivered %v, want only the brief that superseded the other", *written)
	}
}

// TestAParkedDispatchIsClaimedBeforeItIsWritten is the third defect the union
// carried, and the one with no visible symptom until it bites: task and attempt
// lived on the unbound half only, so a HELD BOUND WRITE was the one delivery
// nobody re-checked. Now that a parked dispatch is bound at delivery it also has
// a claim, and this is the window that claim exists for.
//
// The tick pops the parked brief and starts binding it. Binding can sit on a
// task.pre hook for up to two seconds, and a fresh dispatch that lands in that
// window goes straight to the now-ready panel and bumps the task's Attempts. The
// brief still in flight is the stale one, and it must not land on top of the
// brief the agent is already working — which is exactly what it did while a held
// write carried no claim.
func TestAParkedDispatchIsClaimedBeforeItIsWritten(t *testing.T) {
	s, clk, written := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Spawning})

	if _, err := s.dispatchScored("p1", "first", "", authorAgent); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// The hook stands in for a slow bind: a second dispatch lands while the first
	// is still being filtered. It fires once — the re-entrant call runs the chain
	// again, and a hook that dispatched every time would never terminate.
	var landed bool
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) {
		if !landed {
			landed = true
			if _, err := s.dispatchScored("p1", "second", "", authorAgent); err != nil {
				t.Errorf("the dispatch that supersedes: %v", err)
			}
		}
		return b, true
	}

	settle(s, clk)

	if len(*written) != 1 || (*written)[0] != "p1:second\n" {
		t.Fatalf("delivered %v, want only the brief that superseded the one in flight", *written)
	}
}

// TestAPanelThatGoesBusyMidBindDiscardsTheBoundBytes is the race #51 said it
// absorbed, and it is absorbed by ONE line: the readiness re-check dispatchScored
// makes after the bind, before the write.
//
// The panel is settled when the command arrives, so this command IS the delivery
// and the bind happens here. The bind is where the exposure lives — the chain sits
// on the Lua worker for up to two seconds with s.mu released — and the agent can
// start working inside it. The bytes in hand are then bound to a panel that no
// longer exists in that shape: the stale write #51 closed.
//
// Without the re-check they are written anyway, on top of an agent mid-turn, and
// nothing says so — no veto, no error, no second chain run. That silence is why
// this needs an assertion rather than a comment. The hook re-homes the panel as
// it flips it, so the second bind's view is what names the difference between the
// two writes rather than merely counting them.
func TestAPanelThatGoesBusyMidBindDiscardsTheBoundBytes(t *testing.T) {
	s, clk, written := gateServer(panel.Panel{
		ID: "p1", Kind: panel.Agent, State: panel.Idle, Cwd: "/work/auth", Group: "auth",
	})

	var saw []TaskBrief
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) {
		saw = append(saw, b)
		if len(saw) == 1 {
			// The agent picks up work while the chain is still running, and is
			// re-homed with it, so the bytes in hand are stale in a way the second
			// bind can be asked about.
			s.mu.Lock()
			s.panels[0].State = panel.Running
			s.panels[0].Cwd, s.panels[0].Group = "/work/billing", "billing"
			s.mu.Unlock()
		}
		return b, true
	}

	s.onCommand(conn(""), proto.Command{Action: "panel.dispatch", ID: "p1", Prompt: "run the migration"})

	if len(*written) != 0 {
		t.Fatalf("delivered %v at the command, want the bytes discarded and the brief held", *written)
	}
	if len(saw) != 1 {
		t.Fatalf("the chain ran %d times before the panel settled, want once", len(saw))
	}

	settle(s, clk)

	if len(saw) != 2 {
		t.Fatalf("the chain ran %d times, want it asked again about the panel it writes to", len(saw))
	}
	if saw[1].Cwd != "/work/billing" || saw[1].Group != "billing" {
		t.Fatalf("the second bind saw %+v, want the panel as it is at the write", saw[1])
	}
	if len(*written) != 1 || (*written)[0] != "p1:run the migration\n" {
		t.Fatalf("delivered %v, want exactly one write, at settle", *written)
	}
}
