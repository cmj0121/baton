package server

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/attn"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// fakeClock is a manually advanced clock for the Monitor's time-driven logic.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestMonitor() (*monitor, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	return &monitor{now: clk.now, panels: map[string]*panelMon{}}, clk
}

// TestNextState walks every rung of the detection ladder, both kinds, and every
// transition the ladder deliberately excludes. nextState is pure, so the table
// is the cheapest evidence there is that the precedence says what §2.1 says.
func TestNextState(t *testing.T) {
	cases := []struct {
		name  string
		sig   stateSignals
		want  panel.State
		moved bool
	}{
		// Rung 0 — exited is terminal, and nothing below it applies.
		{"exited is terminal", stateSignals{cur: panel.Exited, quiet: true, looksAtt: true, stuckDue: true}, panel.Exited, false},

		// Rung 1 — a declaration outranks every guess, and survives resumed output.
		{"a declaration raises attention at once", stateSignals{cur: panel.Running, agent: true, declared: true}, panel.Attention, true},
		{"a declaration holds attention", stateSignals{cur: panel.Attention, agent: true, declared: true, quiet: true}, panel.Attention, false},
		{"a declaration outranks the stuck timer", stateSignals{cur: panel.Idle, agent: true, declared: true, quiet: true, stuckDue: true}, panel.Attention, true},

		// Rung 2a — the stuck timer, from every state it may escalate from.
		{"stuck from running", stateSignals{cur: panel.Running, agent: true, quiet: true, stuckDue: true}, panel.Stuck, true},
		{"stuck from spawning", stateSignals{cur: panel.Spawning, agent: true, quiet: true, stuckDue: true}, panel.Stuck, true},
		{"stuck from idle", stateSignals{cur: panel.Idle, agent: true, quiet: true, stuckDue: true}, panel.Stuck, true},
		{"stuck from done — the quiet clock keeps growing", stateSignals{cur: panel.Done, agent: true, quiet: true, stuckDue: true}, panel.Stuck, true},
		{"stuck holds", stateSignals{cur: panel.Stuck, agent: true, quiet: true, stuckDue: true}, panel.Stuck, false},
		{"never stuck from attention — the human is, not the panel", stateSignals{cur: panel.Attention, agent: true, quiet: true, stuckDue: true}, panel.Attention, false},
		{"the certain timer outranks the guessing tail", stateSignals{cur: panel.Running, agent: true, quiet: true, stuckDue: true, looksAtt: true}, panel.Stuck, true},

		// Rung 2b — the task event, which is the primary done signal.
		{"a finished task promotes an agent at once", stateSignals{cur: panel.Idle, agent: true, quiet: true, taskDone: true}, panel.Done, true},
		{"a finished task promotes from running too", stateSignals{cur: panel.Running, agent: true, taskDone: true}, panel.Done, true},
		{"a shell never reaches done by event", stateSignals{cur: panel.Idle, quiet: true, taskDone: true}, panel.Idle, false},
		{"a finished task never demotes an answerable panel", stateSignals{cur: panel.Attention, agent: true, quiet: true, taskDone: true}, panel.Attention, false},

		// Rung 3 — the tail heuristic, and the ordering that keeps a question above a review.
		{"a question raises attention from running", stateSignals{cur: panel.Running, quiet: true, looksAtt: true}, panel.Attention, true},
		{"a question raises attention from spawning", stateSignals{cur: panel.Spawning, quiet: true, looksAtt: true}, panel.Attention, true},
		{"a question now beats a review later", stateSignals{cur: panel.Running, agent: true, quiet: true, doneDue: true, looksAtt: true}, panel.Attention, true},

		// Rung 2c — the done timer, which only ever fires from idle.
		{"done from idle", stateSignals{cur: panel.Idle, agent: true, quiet: true, doneDue: true}, panel.Done, true},
		{"an agent passes through idle first", stateSignals{cur: panel.Running, agent: true, quiet: true, doneDue: true}, panel.Idle, true},
		{"done-on-quiet off leaves the agent idle", stateSignals{cur: panel.Idle, agent: true, quiet: true}, panel.Idle, false},
		{"a shell never reaches done by time", stateSignals{cur: panel.Idle, quiet: true}, panel.Idle, false},
		{"done holds when nothing above is due", stateSignals{cur: panel.Done, agent: true, quiet: true}, panel.Done, false},

		// Rung 4 — the original settle, unchanged.
		{"running stays while busy", stateSignals{cur: panel.Running}, panel.Running, false},
		{"running falls idle when quiet", stateSignals{cur: panel.Running, quiet: true}, panel.Idle, true},
		{"spawning settles idle when quiet", stateSignals{cur: panel.Spawning, quiet: true}, panel.Idle, true},
		{"spawning holds while coming up", stateSignals{cur: panel.Spawning}, panel.Spawning, false},
		{"attention holds when still quiet", stateSignals{cur: panel.Attention, quiet: true}, panel.Attention, false},
	}
	for _, tc := range cases {
		got, moved := nextState(tc.sig)
		if got != tc.want || moved != tc.moved {
			t.Errorf("%s: nextState(%+v) = %v,%v want %v,%v", tc.name, tc.sig, got, moved, tc.want, tc.moved)
		}
	}
}

// TestWantsTail checks the read the tail heuristic is gated behind: it is worth
// paying for only when rung 3 can still decide, which is what keeps fifty quiet
// panels from costing fifty kilobyte copies a second.
func TestWantsTail(t *testing.T) {
	cases := []struct {
		name string
		sig  stateSignals
		want bool
	}{
		{"a quiet running panel is the case it exists for", stateSignals{cur: panel.Running, quiet: true}, true},
		{"a quiet spawning panel too", stateSignals{cur: panel.Spawning, quiet: true}, true},
		{"a busy panel has not settled yet", stateSignals{cur: panel.Running}, false},
		{"a resting panel cannot reach rung 3", stateSignals{cur: panel.Idle, quiet: true}, false},
		{"an exited panel is decided at rung 0", stateSignals{cur: panel.Exited, quiet: true}, false},
		{"a declaration already decided it", stateSignals{cur: panel.Running, quiet: true, declared: true}, false},
		{"the stuck timer already decided it", stateSignals{cur: panel.Running, quiet: true, stuckDue: true}, false},
		{"a finished task already decided it", stateSignals{cur: panel.Running, quiet: true, agent: true, taskDone: true}, false},
		{"a shell's finished task decides nothing", stateSignals{cur: panel.Running, quiet: true, taskDone: true}, true},
	}
	for _, tc := range cases {
		if got := tc.sig.wantsTail(); got != tc.want {
			t.Errorf("%s: wantsTail() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestQuietAndObserve checks the quiet timer: a fresh panel is busy, output resets
// the clock, and crossing idleAfter without output reads as quiet. An unknown id
// reads quiet so a stray panel never animates.
func TestQuietAndObserve(t *testing.T) {
	mo, clk := newTestMonitor()
	mo.spawned("p1")

	if mo.quiet("p1") {
		t.Fatal("a just-spawned panel should not be quiet")
	}
	clk.add(idleAfter - time.Second)
	mo.observed("p1", 10)
	clk.add(idleAfter - time.Second)
	if mo.quiet("p1") {
		t.Fatal("output should have reset the quiet timer")
	}
	clk.add(time.Second)
	if !mo.quiet("p1") {
		t.Fatal("crossing idleAfter without output should read quiet")
	}
	if !mo.quiet("ghost") {
		t.Fatal("an unknown panel should read quiet")
	}
}

// TestRollSpark checks the sparkline window: each tick pushes the bytes seen onto
// the right and drops the oldest, and the bars scale to the busiest bucket.
func TestRollSpark(t *testing.T) {
	mo, _ := newTestMonitor()
	mo.spawned("p1")

	if got := mo.roll("p1"); got != "▁▁▁▁▁▁▁▁" {
		t.Fatalf("an empty window should be flat, got %q", got)
	}
	mo.observed("p1", 100)
	spark := mo.roll("p1")
	runes := []rune(spark)
	if len(runes) != sparkWidth {
		t.Fatalf("spark width = %d, want %d", len(runes), sparkWidth)
	}
	if runes[sparkWidth-1] != '█' {
		t.Fatalf("the busiest, newest bucket should peak, got %q", spark)
	}
	if runes[0] != '▁' {
		t.Fatalf("the oldest, empty bucket should be baseline, got %q", spark)
	}
	// The bucket resets after rolling, so a quiet tick shifts the spike one slot
	// left rather than re-counting it; it takes a full window to drain flat.
	if got := mo.roll("p1"); got != "▁▁▁▁▁▁█▁" {
		t.Fatalf("a quiet tick should shift the spike left, got %q", got)
	}
	for range sparkWidth {
		mo.roll("p1")
	}
	if got := mo.roll("p1"); got != "▁▁▁▁▁▁▁▁" {
		t.Fatalf("the spike should drain flat after a full window, got %q", got)
	}
}

func TestRenderSpark(t *testing.T) {
	if got := renderSpark([]int{0, 0, 0}); got != "▁▁▁" {
		t.Fatalf("all-zero = %q", got)
	}
	if got := renderSpark([]int{0, 4, 8}); got != "▁▄█" {
		t.Fatalf("scaled = %q", got)
	}
}

// TestLooksLikeAttention checks the prompt sniff: questions and confirmations on
// the last line flag attention, ordinary output and shell prompts do not, and
// colour codes are seen through.
func TestLooksLikeAttention(t *testing.T) {
	yes := []string{
		"Do you want to continue?",
		"building...\nProceed with the migration? (y/n)",
		"Overwrite the file [Y/n]",
		"\x1b[1;32mApply this change?\x1b[0m",
		"Press enter to continue",
	}
	for _, s := range yes {
		if !looksLikeAttention([]byte(s)) {
			t.Errorf("expected attention for %q", s)
		}
	}
	no := []string{
		"",
		"compiling main.go\nok  baton  0.3s",
		"user@host:~/baton$ ",
		"streaming tokens, still working",
	}
	for _, s := range no {
		if looksLikeAttention([]byte(s)) {
			t.Errorf("did not expect attention for %q", s)
		}
	}
}

// TestActivityText checks the live status line per state and the single-unit age.
func TestActivityText(t *testing.T) {
	cases := []struct {
		state panel.State
		since time.Duration
		want  string
	}{
		{panel.Spawning, 2 * time.Second, "spawning · 2s"},
		{panel.Running, 90 * time.Second, "running · 1m"},
		{panel.Idle, 3 * time.Minute, "idle · 3m"},
		{panel.Attention, 30 * time.Second, "needs you · 30s"},
		{panel.Done, 2 * time.Minute, "done · 2m"},
		{panel.Stuck, 12 * time.Minute, "stuck · 12m"},
		{panel.Exited, time.Hour, "exited"},
	}
	for _, tc := range cases {
		if got := activityText(tc.state, tc.since); got != tc.want {
			t.Errorf("activityText(%v,%v) = %q, want %q", tc.state, tc.since, got, tc.want)
		}
	}
}

// TestMonitorTickSettlesAndEmits drives the server-level tick: a panel that has
// gone quiet settles to idle and the tick emits a "telemetry" refresh carrying
// the new state, activity, and sparkline. A second tick with nothing new reports
// no change.
//
// The panel is a SHELL because the steady-state half of this test needs a panel
// that genuinely rests: only an agent climbs the quiet ladder past idle, and one
// left alone for a minute would legitimately move to done here (see
// TestMonitorTickClimbsTheLadder).
func TestMonitorTickSettlesAndEmits(t *testing.T) {
	mo, clk := newTestMonitor()
	cc := &clientConn{out: make(chan proto.ServerMsg, 8), attached: map[string]bool{}}
	s := &Server{
		pty:     ptymgr.New(),
		clients: map[*clientConn]struct{}{cc: {}},
		mon:     mo,
		panels:  []panel.Panel{{ID: "p1", Kind: panel.Shell, Title: "bash", State: panel.Running}},
	}
	mo.spawned("p1")

	clk.add(idleAfter) // output has gone quiet
	msg, ok := s.monitorTick()
	if !ok {
		t.Fatal("a settled panel should produce a telemetry refresh")
	}
	if msg.Type != "telemetry" || len(msg.Panels) != 1 {
		t.Fatalf("unexpected telemetry message %+v", msg)
	}
	if s.panels[0].State != panel.Idle {
		t.Fatalf("a quiet panel should settle to idle, got %v", s.panels[0].State)
	}
	if msg.Panels[0].State != "idle" || msg.Panels[0].Activity == "" || msg.Panels[0].Spark == "" {
		t.Fatalf("telemetry should carry state/activity/spark, got %+v", msg.Panels[0])
	}

	// The activity line carries a live age, so it keeps refreshing while the
	// seconds tick. Once the age rolls to minute granularity it holds steady, and a
	// panel with nothing moving — same state, flat spark, unchanged age text —
	// reports no change, so resting panels stop generating traffic.
	clk.add(time.Minute)
	if _, ok := s.monitorTick(); !ok {
		t.Fatal("the ticking age should still refresh")
	}
	clk.add(monitorInterval)
	if _, ok := s.monitorTick(); ok {
		t.Fatal("a steady idle panel at minute granularity should report no change")
	}
}

// TestRouteOutputWakes checks the output path wakes an idle panel back to running
// without waiting for a tick.
func TestRouteOutputWakes(t *testing.T) {
	mo, _ := newTestMonitor()
	s := &Server{
		pty:     ptymgr.New(),
		clients: map[*clientConn]struct{}{},
		mon:     mo,
		panels:  []panel.Panel{{ID: "p1", State: panel.Idle}},
	}
	mo.spawned("p1")

	s.routeOutput("p1", []byte("fresh output"))
	if s.panels[0].State != panel.Running {
		t.Fatalf("output should wake an idle panel to running, got %v", s.panels[0].State)
	}
	if mo.quiet("p1") {
		t.Fatal("output should reset the quiet timer")
	}

	// Every resting state wakes, including the two new rungs of the ladder.
	for _, from := range []panel.State{panel.Spawning, panel.Attention, panel.Done, panel.Stuck} {
		s.panels[0].State = from
		s.routeOutput("p1", []byte("more"))
		if s.panels[0].State != panel.Running {
			t.Fatalf("output should wake %v to running, got %v", from, s.panels[0].State)
		}
	}

	// A standing declaration survives resumed output: an agent that prints a
	// spinner while waiting on you must not lose its own raised hand.
	s.panels[0].State = panel.Attention
	s.declared = map[string]*declaration{"p1": {Reason: "which migration?"}}
	s.routeOutput("p1", []byte("⠋ waiting"))
	if s.panels[0].State != panel.Attention {
		t.Fatalf("a declared attention should survive output, got %v", s.panels[0].State)
	}
}

// TestMonitorTickClimbsTheLadder walks one agent up the whole quiet clock —
// running → idle → done → stuck — on the injected clock, and shows the same
// silence leaves a shell resting at idle. The thresholds come from the built-in
// policy, so this is also the check that a server with no attention config runs
// on the documented defaults.
func TestMonitorTickClimbsTheLadder(t *testing.T) {
	s, clk, _ := gateServer(
		panel.Panel{ID: "agent", Kind: panel.Agent, State: panel.Running},
		panel.Panel{ID: "shell", Kind: panel.Shell, State: panel.Running},
	)

	stateOf := func(id string) panel.State {
		i := s.indexLocked(id)
		return s.panels[i].State
	}

	clk.add(idleAfter)
	s.monitorTick()
	if stateOf("agent") != panel.Idle || stateOf("shell") != panel.Idle {
		t.Fatalf("both should settle to idle, got %v/%v", stateOf("agent"), stateOf("shell"))
	}

	clk.add(attn.DefaultDoneAfter)
	s.monitorTick()
	if stateOf("agent") != panel.Done {
		t.Fatalf("a quiet agent should reach done, got %v", stateOf("agent"))
	}
	if stateOf("shell") != panel.Idle {
		t.Fatalf("a quiet shell stays idle on purpose, got %v", stateOf("shell"))
	}

	// The stuck timer reads the QUIET clock, not the state clock: the agent has
	// been sitting in done, and nothing about entering that state reset the ladder.
	clk.add(attn.DefaultStuckAfter)
	s.monitorTick()
	if stateOf("agent") != panel.Stuck {
		t.Fatalf("done should escalate to stuck on the quiet clock, got %v", stateOf("agent"))
	}
	if stateOf("shell") != panel.Idle {
		t.Fatalf("a shell never climbs to stuck, got %v", stateOf("shell"))
	}

	// One byte of output takes the whole ladder back to the bottom.
	s.routeOutput("agent", []byte("back to work"))
	if stateOf("agent") != panel.Running {
		t.Fatalf("output should wake a stuck panel, got %v", stateOf("agent"))
	}
}

// TestMonitorTickDoneOnTaskEvent checks the primary done signal: a dispatched
// agent whose task the server SAW finish reaches done on the next tick, rather
// than waiting out done-after — and that the event is consumed, so waking the
// panel back up does not drag it to done again.
func TestMonitorTickDoneOnTaskEvent(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Idle})
	if err := s.dispatchPanel("p1", "work it", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	s.routeOutput("p1", []byte("thinking…"))

	clk.add(idleAfter) // the agent goes quiet: the task settles, the panel settles to idle
	s.monitorTick()
	if s.panels[0].State != panel.Idle {
		t.Fatalf("the settling tick should reach idle, got %v", s.panels[0].State)
	}

	s.monitorTick() // the next tick reads the task event, well before done-after
	if s.panels[0].State != panel.Done {
		t.Fatalf("a finished task should promote the panel to done, got %v", s.panels[0].State)
	}

	// The event was consumed: output wakes the panel and it stays awake.
	s.routeOutput("p1", []byte("more work"))
	s.monitorTick()
	if s.panels[0].State != panel.Running {
		t.Fatalf("a spent task event must not drag a woken panel back to done, got %v", s.panels[0].State)
	}
}

// TestOutputInvalidatesTheDoneEvent covers the one-tick window the other
// direction: a task settles and the panel produces output before the next tick.
// The panel is demonstrably working, so the pending event must not promote it —
// waking clears it, because a byte of output is the panel contradicting the claim
// that its turn is over.
func TestOutputInvalidatesTheDoneEvent(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Idle})
	s.taskSettled["p1"] = true

	s.routeOutput("p1", []byte("actually, one more thing"))
	s.monitorTick()
	if s.panels[0].State != panel.Running {
		t.Fatalf("output should invalidate the pending done event, got %v", s.panels[0].State)
	}
}

// TestSignalsLocked checks the assembly the ladder consumes: the per-profile
// thresholds are resolved per tick, a standing declaration is reported, and the
// expensive tail read is both gated and suppressible.
func TestSignalsLocked(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	s.specs["p1"] = spawnSpec{Profile: "claude"}
	s.agentAttention = map[string]attn.Policy{"claude": {StuckAfter: 30 * time.Minute}}

	clk.add(idleAfter)
	sig := s.signalsLocked(s.panels[0], false)
	if !sig.agent || !sig.quiet || sig.doneDue || sig.stuckDue {
		t.Fatalf("at 10s only the idle rung is due, got %+v", sig)
	}

	clk.add(attn.DefaultDoneAfter)
	if sig := s.signalsLocked(s.panels[0], false); !sig.doneDue || sig.stuckDue {
		t.Fatalf("at 70s the done rung is due and the profile's 30m stuck rung is not, got %+v", sig)
	}
	clk.add(30 * time.Minute)
	if sig := s.signalsLocked(s.panels[0], false); !sig.stuckDue {
		t.Fatalf("the profile's own stuck-after should be honoured, got %+v", sig)
	}

	// A declaration is reported, and it skips the tail read entirely: a rung the
	// agent itself decided leaves nothing for the heuristic to decide.
	s.declared["p1"] = &declaration{Reason: "which migration?"}
	if sig := s.signalsLocked(s.panels[0], false); !sig.declared || sig.looksAtt {
		t.Fatalf("a declaration should stand and skip the tail, got %+v", sig)
	}
}

// TestQuietFor checks the one clock every threshold reads, at the three
// thresholds the ladder uses, against an injected clock.
func TestQuietFor(t *testing.T) {
	mo, clk := newTestMonitor()
	mo.spawned("p1")

	clk.add(idleAfter)
	if !mo.quietFor("p1", idleAfter) || mo.quietFor("p1", attn.DefaultDoneAfter) {
		t.Fatal("at 10s the idle rung is due and the done rung is not")
	}
	clk.add(attn.DefaultDoneAfter)
	if !mo.quietFor("p1", attn.DefaultDoneAfter) || mo.quietFor("p1", attn.DefaultStuckAfter) {
		t.Fatal("at 70s the done rung is due and the stuck rung is not")
	}
	clk.add(attn.DefaultStuckAfter)
	if !mo.quietFor("p1", attn.DefaultStuckAfter) {
		t.Fatal("at 10m70s the stuck rung is due")
	}

	// Output resets the whole ladder, not just the rung that was due.
	mo.observed("p1", 1)
	if mo.quietFor("p1", idleAfter) {
		t.Fatal("one byte of output should reset the quiet clock")
	}
	if !mo.enteredAt("ghost").IsZero() {
		t.Fatal("an untracked panel reports a zero state clock")
	}
}

// TestSinceAndForget checks the state-duration clock and that forgetting a panel
// drops its bookkeeping.
func TestSinceAndForget(t *testing.T) {
	mo, clk := newTestMonitor()
	mo.spawned("p1")
	clk.add(5 * time.Second)
	if got := mo.since("p1"); got != 5*time.Second {
		t.Fatalf("since = %v, want 5s", got)
	}
	mo.entered("p1") // a state change restarts the duration
	if got := mo.since("p1"); got != 0 {
		t.Fatalf("entering a state should reset the duration, got %v", got)
	}
	mo.forget("p1")
	if _, ok := mo.panels["p1"]; ok {
		t.Fatal("forget should drop the panel")
	}
	if got := mo.since("ghost"); got != 0 {
		t.Fatalf("since of an unknown panel = %v, want 0", got)
	}
}
