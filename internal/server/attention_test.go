package server

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cmj0121/baton/internal/attn"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// ctl is a stand-in control connection: the reply channel a verb sends its error
// to, and the self id an agent's own `baton ctl attention` addresses through.
func ctl(self string) *clientConn {
	return &clientConn{out: make(chan proto.ServerMsg, 8), attached: map[string]bool{}, self: self}
}

// replyErr drains what a verb sent back, returning the error text or "" when it
// answered with nothing (which is what a success looks like: the fleet snapshot
// goes to the registered clients, not to the caller).
func replyErr(cc *clientConn) string {
	for {
		select {
		case msg := <-cc.out:
			if msg.Type == "error" {
				return msg.Error
			}
		default:
			return ""
		}
	}
}

// stateOf is the panel's live lifecycle state, by id.
func stateOf(s *Server, id string) panel.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.panels[s.indexLocked(id)].State
}

// reasonOf is the panel's live declared reason, by id.
func reasonOf(s *Server, id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.panels[s.indexLocked(id)].Reason
}

// TestDeclarationOutranksEveryGuess is priority 1 of the issue's detection table,
// end to end through the server: an agent says it needs a human, the panel is in
// attention before the call returns, and the reason it gave reaches the wire.
// Neither the quiet timer below it nor a resumed byte of output takes that back.
func TestDeclarationOutranksEveryGuess(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running})
	cc := ctl("")

	s.declareAttention(cc, proto.Command{Action: "panel.attention", ID: "a1", Reason: "which migration do I run first?"})
	if err := replyErr(cc); err != "" {
		t.Fatalf("declare: %v", err)
	}
	if got := stateOf(s, "a1"); got != panel.Attention {
		t.Fatalf("a declaration should raise attention at once, got %v", got)
	}

	wire := s.panelsMsg().Panels[0]
	if wire.Reason != "which migration do I run first?" {
		t.Errorf("the agent's own words should reach the wire, got %q", wire.Reason)
	}
	if wire.State != "attention" {
		t.Errorf("wire state = %q, want attention", wire.State)
	}

	// The quiet ladder runs underneath and must not reclaim the panel: silence is
	// a guess, and the panel already said what the silence means.
	clk.add(time.Hour)
	s.monitorTick()
	if got := stateOf(s, "a1"); got != panel.Attention {
		t.Fatalf("a timer must not outrank a declaration, got %v", got)
	}

	// Nor may resumed output. An agent that prints a spinner while it waits on you
	// would otherwise lose its raised hand on the very next byte.
	s.routeOutput("a1", []byte("thinking...\n"))
	if got := stateOf(s, "a1"); got != panel.Attention {
		t.Fatalf("output must not withdraw a declaration, got %v", got)
	}
}

// TestDeclarationClosesTheSchedulerWindow is the reason declareAttention
// re-derives the state itself instead of leaving it to the next tick.
//
// freeForWork and dispatchReady read nothing but panel.State. If a declaration
// only landed on the following tick, there would be up to a second in which the
// scheduler could hand queued backlog work to a panel that has already said it is
// blocked on a person — and the task would sit undelivered behind a human. The
// assertion is deliberately made with NO tick between the declaration and the
// scheduling pass, because that is the whole of the window.
func TestDeclarationClosesTheSchedulerWindow(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Idle})
	if _, ok := freeIdleAgent(s, ""); !ok {
		t.Fatal("an idle agent should start out in the scheduler's free pool")
	}

	s.declareAttention(ctl(""), proto.Command{Action: "panel.attention", ID: "a1", Reason: "which branch?"})

	if id, ok := freeIdleAgent(s, ""); ok {
		t.Fatalf("a panel that raised its hand is not free for work, got %q", id)
	}
	if _, err := s.enqueueTask("audit the auth module", "", nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if deliver, spawns := schedule(s); len(deliver) != 0 || len(spawns) != 0 {
		t.Fatalf("the backlog must not drain onto a declared panel, got %d deliveries", len(deliver))
	}
	if got, _ := s.TaskByPanel("a1"); got.Panel != "" {
		t.Fatalf("no task should have been assigned, got %+v", got)
	}
}

// freeIdleAgent runs one free-pool lookup under the lock.
func freeIdleAgent(s *Server, group string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.freeIdleAgentLocked(group)
}

// TestDeclarationNeedsAReason checks the one thing a declaration must carry. It
// outranks the timer and the heuristic because it can say why; a bare flag would
// displace both of them while saying no more than either, so it is refused —
// including when the text only LOOKED like a reason before it was scrubbed.
func TestDeclarationNeedsAReason(t *testing.T) {
	for _, reason := range []string{"", "   ", "\n\t", "\x1b\x07"} {
		s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running})
		cc := ctl("")
		s.declareAttention(cc, proto.Command{Action: "panel.attention", ID: "a1", Reason: reason})
		if err := replyErr(cc); !strings.Contains(err, "reason is required") {
			t.Errorf("reason %q: want a reason-required error, got %q", reason, err)
		}
		if got := stateOf(s, "a1"); got != panel.Running {
			t.Errorf("reason %q: a refused declaration must not move the panel, got %v", reason, got)
		}
	}
}

// TestDeclarationRefusedOnAPanelThatCannotAsk covers the two panels that have no
// standing to raise a hand: one that does not exist, and one whose process is
// gone. A dead process is not asking for anything.
func TestDeclarationRefusedOnAPanelThatCannotAsk(t *testing.T) {
	s, _, _ := gateServer(
		panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running},
		panel.Panel{ID: "dead", Kind: panel.Agent, State: panel.Exited},
	)

	cc := ctl("")
	s.declareAttention(cc, proto.Command{Action: "panel.attention", ID: "nope", Reason: "hello?"})
	if err := replyErr(cc); !strings.Contains(err, "no panel with id") {
		t.Errorf("an unknown panel should be refused by name, got %q", err)
	}

	s.declareAttention(cc, proto.Command{Action: "panel.attention", ID: "dead", Reason: "hello?"})
	if err := replyErr(cc); !strings.Contains(err, "has exited") {
		t.Errorf("an exited panel should be refused, got %q", err)
	}
	if got := stateOf(s, "dead"); got != panel.Exited {
		t.Errorf("exited is terminal, got %v", got)
	}
}

// TestDeclarationTargetsTheConnectionsOwnPanel covers the form an agent actually
// uses: `baton ctl attention --why "…"` with no id at all, inside a panel baton
// identified. The identity is already on the connection, so the agent never has
// to learn its own id — and a connection that declared none is told so rather
// than silently addressing nothing.
func TestDeclarationTargetsTheConnectionsOwnPanel(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running})

	own := ctl("a1")
	s.declareAttention(own, proto.Command{Action: "panel.attention", Reason: "approve the plan?"})
	if err := replyErr(own); err != "" {
		t.Fatalf("an id-less declaration should target the connection's own panel: %v", err)
	}
	if got := stateOf(s, "a1"); got != panel.Attention {
		t.Fatalf("state = %v, want attention", got)
	}

	anon := ctl("")
	s.resolveAttention(anon, proto.Command{Action: "panel.resolve"})
	if err := replyErr(anon); !strings.Contains(err, "no panel id") {
		t.Errorf("a connection with no self and no id should be told so, got %q", err)
	}
	if got := stateOf(s, "a1"); got != panel.Attention {
		t.Errorf("an unaddressed resolve must not withdraw someone else's hand, got %v", got)
	}
}

// TestConductorMayRaiseItsOwnHand pins a deliberate deviation from DESIGN §11.1,
// which lists panel.attention and panel.resolve alongside panel.ack as verbs a
// conductor connection is refused. §12 — where the decision is actually reasoned
// — fences only the INBOX verbs, and the fence itself exists to stop an agent
// acting destructively on its own panel: closing it, signalling it, feeding it
// input. Saying "I need a decision" is the opposite of destructive, and the
// conductor is the one agent in the fleet that is always identified to the
// server. Refusing it would make the issue's second gap unfixed for the very
// agent the control surface was built for.
func TestConductorMayRaiseItsOwnHand(t *testing.T) {
	s, _, _ := gateServer(
		panel.Panel{ID: "c1", Kind: panel.Agent, State: panel.Running, Conductor: true},
		panel.Panel{ID: "w1", Kind: panel.Agent, State: panel.Idle},
	)
	cc := ctl("c1")
	cc.role = roleConductor

	if reason := s.guardConductor(cc, proto.Command{Action: "panel.attention", ID: "c1"}); reason != "" {
		t.Fatalf("the fence should not reach a declaration about itself, got %q", reason)
	}
	if reason := s.guardConductor(cc, proto.Command{Action: "panel.attention"}); reason != "" {
		t.Fatalf("an id-less declaration already means its own panel, got %q", reason)
	}
	s.declareAttention(cc, proto.Command{Action: "panel.attention", Reason: "the brief is ambiguous"})
	if err := replyErr(cc); err != "" {
		t.Fatalf("a conductor must be able to say it needs a decision: %v", err)
	}
	if got := stateOf(s, "c1"); got != panel.Attention {
		t.Fatalf("state = %v, want attention", got)
	}

	// The other half of the same argument. The justification above holds only for
	// self-targeting: a declaration takes its panel out of freeForWork until
	// something withdraws it, so a conductor raising hands across the fleet could
	// starve the scheduler of every worker it has.
	for _, action := range []string{"panel.attention", "panel.resolve"} {
		if reason := s.guardConductor(cc, proto.Command{Action: action, ID: "w1"}); reason == "" {
			t.Errorf("%s: a conductor must not raise or lower another panel's hand", action)
		}
	}
	if got := stateOf(s, "w1"); got != panel.Idle {
		t.Errorf("the worker should be untouched and still free for work, got %v", got)
	}
}

// TestResolveDerivesTheStateAgain checks that withdrawing a declaration hands the
// panel back to the ladder rather than to a state this code picked. A quiet agent
// settles to idle and then climbs the same quiet clock it was already on — the
// declaration was a detour, not a reset.
func TestResolveDerivesTheStateAgain(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running})
	cc := ctl("")

	clk.add(idleAfter)
	s.declareAttention(cc, proto.Command{Action: "panel.attention", ID: "a1", Reason: "which migration?"})
	if got := stateOf(s, "a1"); got != panel.Attention {
		t.Fatalf("state = %v, want attention", got)
	}

	s.resolveAttention(cc, proto.Command{Action: "panel.resolve", ID: "a1"})
	if err := replyErr(cc); err != "" {
		t.Fatalf("resolve: %v", err)
	}
	if got := stateOf(s, "a1"); got != panel.Idle {
		t.Fatalf("a quiet panel should settle back to idle, got %v", got)
	}
	if got := reasonOf(s, "a1"); got != "" {
		t.Errorf("the reason should go with the declaration, got %q", got)
	}
	if s.declaredLocked("a1") {
		t.Error("no declaration should stand after a resolve")
	}

	// The quiet clock was never reset, so the ladder picks up where it left off.
	clk.add(attn.DefaultDoneAfter)
	s.monitorTick()
	if got := stateOf(s, "a1"); got != panel.Done {
		t.Fatalf("the ladder should keep climbing after a resolve, got %v", got)
	}
}

// TestResolveWithNothingStandingIsANoOp checks the forgiving half of the verb. An
// agent tidying up after itself should not have to know whether its hand is still
// up — a human may have dealt with it, or the panel may never have raised one.
func TestResolveWithNothingStandingIsANoOp(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running})
	cc := ctl("")

	s.resolveAttention(cc, proto.Command{Action: "panel.resolve", ID: "a1"})
	if err := replyErr(cc); err != "" {
		t.Errorf("resolving nothing is not an error, got %q", err)
	}
	s.resolveAttention(cc, proto.Command{Action: "panel.resolve", ID: "ghost"})
	if err := replyErr(cc); err != "" {
		t.Errorf("resolving an unknown panel is not an error, got %q", err)
	}
	if got := stateOf(s, "a1"); got != panel.Running {
		t.Errorf("a no-op resolve must not move the panel, got %v", got)
	}
}

// TestResolveSuppressesTheTailUntilNewOutput is the contract the suppression
// exists for, exercised against a REAL retained tail rather than a stubbed one.
//
// The agent's last words were a question, so the bytes that make the heuristic
// say "this panel is waiting on you" are still sitting in the ring after the
// resolve — the panel has not spoken since, and it has no reason to. Without the
// suppression the ladder reads that same unchanged tail on the next quiet tick,
// reaches the same conclusion, and puts the panel straight back into attention:
// `ctl resolve` would be a verb that undoes itself.
//
// One byte of new output ends it, because a new tail is a new claim.
func TestResolveSuppressesTheTailUntilNewOutput(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running})
	s.pty = askingPty(t, "a1")

	s.declareAttention(ctl(""), proto.Command{Action: "panel.attention", ID: "a1", Reason: "which migration?"})
	if got := stateOf(s, "a1"); got != panel.Attention {
		t.Fatalf("state = %v, want attention", got)
	}
	s.resolveAttention(ctl(""), proto.Command{Action: "panel.resolve", ID: "a1"})
	if got := stateOf(s, "a1"); got != panel.Running {
		t.Fatalf("a panel that is not yet quiet re-enters the ladder at running, got %v", got)
	}

	// The panel falls quiet with the question still its last word. This is the
	// tick the suppression is for.
	clk.add(idleAfter)
	s.monitorTick()
	if got := stateOf(s, "a1"); got != panel.Idle {
		t.Fatalf("the same unchanged tail must not undo the resolve, got %v", got)
	}
	for i := 0; i < 3; i++ {
		clk.add(idleAfter)
		s.monitorTick()
		if got := stateOf(s, "a1"); got == panel.Attention {
			t.Fatalf("tick %d: the resolve should still hold", i)
		}
	}

	// One byte of output, and the panel is speaking again: whatever its tail says
	// now deserves to be read on its own terms.
	clk.add(time.Second)
	s.routeOutput("a1", []byte("x"))
	if got := stateOf(s, "a1"); got != panel.Running {
		t.Fatalf("output should wake an undeclared panel, got %v", got)
	}
	clk.add(idleAfter)
	s.monitorTick()
	if got := stateOf(s, "a1"); got != panel.Attention {
		t.Fatalf("new output should end the suppression, got %v", got)
	}
}

// TestResolveWithoutADeclarationLeavesTheHeuristicAlone is the other side of the
// same boundary: the suppression is installed by a resolve that actually withdrew
// something, so a stray resolve on a panel the heuristic flagged does not mute it
// forever.
func TestResolveWithoutADeclarationLeavesTheHeuristicAlone(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running})
	s.pty = askingPty(t, "a1")

	s.resolveAttention(ctl(""), proto.Command{Action: "panel.resolve", ID: "a1"})
	clk.add(idleAfter)
	s.monitorTick()
	if got := stateOf(s, "a1"); got != panel.Attention {
		t.Fatalf("a resolve that withdrew nothing must not mute the tail, got %v", got)
	}
}

// TestDeclarationIsDroppedWhenThePanelDies checks the raised hand goes with the
// process, including the suppression the entry would otherwise keep alive: a
// panel that comes back is not born muted.
func TestDeclarationIsDroppedWhenThePanelDies(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running})
	s.declareAttention(ctl(""), proto.Command{Action: "panel.attention", ID: "a1", Reason: "which migration?"})

	s.onPanelExit("a1", 1)

	if s.declaredLocked("a1") {
		t.Error("a dead process is not asking for anything")
	}
	if got := reasonOf(s, "a1"); got != "" {
		t.Errorf("the reason should die with the process, got %q", got)
	}
	s.mu.Lock()
	suppressed := s.suppressedLocked("a1")
	s.mu.Unlock()
	if suppressed {
		t.Error("a panel that exits must not leave the heuristic muted behind it")
	}
}

// TestSanitizeReason pins where the scrubbing of agent-supplied text happens:
// on the way IN, once, so every reader downstream — the inbox, `baton ctl`, an
// MCP result, a plugin handler — is already holding safe text and none of them
// needs to escape it a second time.
func TestSanitizeReason(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ordinary text is untouched", "which migration do I run first?", "which migration do I run first?"},
		{"non-ascii is kept", "要先跑哪一個 migration？", "要先跑哪一個 migration？"},
		{"a newline folds to one space", "line one\nline two", "line one line two"},
		{"a run of whitespace folds to one space", "a \t\n  b", "a b"},
		{"leading and trailing space is dropped", "  hemmed in  ", "hemmed in"},
		{"a control byte is dropped", "bell\x07here", "bellhere"},
		{"an OSC 52 clipboard write cannot reach the terminal", "\x1b]52;c;aGk=\x07done", "]52;c;aGk=done"},
		{"an escape loses its ESC and stays visible", "\x1b[1;31mred", "[1;31mred"},
		{"the replacement character is dropped", "bad�byte", "badbyte"},
		{"a bidi override cannot reverse the row", "safe\u202egnp.exe", "safegnp.exe"},
		{"bidi isolates go with it", "\u2066mixed\u2069 text", "mixed text"},
		{"a zero-width space cannot hide a word break", "one\u200btwo", "onetwo"},
		{"nothing but noise scrubs to nothing", "\x1b\x07\n", ""},
	}
	for _, tc := range cases {
		if got := sanitizeReason(tc.in); got != tc.want {
			t.Errorf("%s: sanitizeReason(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}

	// A reason rides every fleet snapshot to every client, so an agent does not get
	// to make the whole fleet carry a payload. The cap counts RUNES, so a
	// non-ASCII reason is not silently cut to a third of the length an ASCII one
	// gets — nor ever cut mid-rune.
	for _, in := range []string{strings.Repeat("a", 5000), strings.Repeat("要", 5000)} {
		got := sanitizeReason(in)
		if n := len([]rune(got)); n != maxReasonRunes {
			t.Errorf("an over-long reason should cap at %d runes, got %d", maxReasonRunes, n)
		}
		if !utf8.ValidString(got) {
			t.Errorf("the cap must not cut a rune in half, got %q", got)
		}
	}
	if got := sanitizeReason(strings.Repeat("a", maxReasonRunes)); len(got) != maxReasonRunes {
		t.Errorf("a reason exactly at the cap should survive whole, got %d", len(got))
	}
}

// askingPty returns a PTY manager holding one panel whose retained output ends in
// a question, so the tail heuristic has something real to read. The process runs
// and exits immediately; ptymgr keeps the ring after the process is gone, which is
// exactly the state a panel waiting at a prompt is in from the Monitor's side.
func askingPty(t *testing.T, id string) *ptymgr.Manager {
	t.Helper()
	pm := ptymgr.New()
	t.Cleanup(func() { pm.Stop(id) })
	spec := ptymgr.Spec{Command: "/bin/sh", Args: []string{"-c", `printf 'Apply this refactor? [y/N] '`}}
	if err := pm.StartCmd(id, spec); err != nil {
		t.Fatalf("start the asking panel: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(strings.ToLower(string(pm.Tail(id, attnTailBytes))), "[y/n]") {
		if time.Now().After(deadline) {
			t.Fatalf("the asking panel never produced its prompt, tail=%q", pm.Tail(id, attnTailBytes))
		}
		time.Sleep(10 * time.Millisecond)
	}
	return pm
}
