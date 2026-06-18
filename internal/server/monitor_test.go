package server

import (
	"testing"
	"time"

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

// TestNextState covers the pure transition: a live or just-spawned panel settles
// to idle (or attention) when quiet, holds otherwise, and resting states never
// move on their own.
func TestNextState(t *testing.T) {
	cases := []struct {
		name             string
		cur              panel.State
		quiet, attention bool
		want             panel.State
		moved            bool
	}{
		{"running stays while busy", panel.Running, false, false, panel.Running, false},
		{"running falls idle when quiet", panel.Running, true, false, panel.Idle, true},
		{"running needs you on a prompt", panel.Running, true, true, panel.Attention, true},
		{"spawning settles idle when quiet", panel.Spawning, true, false, panel.Idle, true},
		{"spawning holds while coming up", panel.Spawning, false, false, panel.Spawning, false},
		{"idle holds when still quiet", panel.Idle, true, false, panel.Idle, false},
		{"attention holds when still quiet", panel.Attention, true, false, panel.Attention, false},
		{"exited is terminal", panel.Exited, true, true, panel.Exited, false},
	}
	for _, tc := range cases {
		got, moved := nextState(tc.cur, tc.quiet, tc.attention)
		if got != tc.want || moved != tc.moved {
			t.Errorf("%s: nextState(%v,%v,%v) = %v,%v want %v,%v",
				tc.name, tc.cur, tc.quiet, tc.attention, got, moved, tc.want, tc.moved)
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
	mo.observed("p1")
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
// the new state and activity line. A second tick with nothing new reports no
// change.
func TestMonitorTickSettlesAndEmits(t *testing.T) {
	mo, clk := newTestMonitor()
	cc := &clientConn{out: make(chan proto.ServerMsg, 8), attached: map[string]bool{}}
	s := &Server{
		pty:     ptymgr.New(),
		clients: map[*clientConn]struct{}{cc: {}},
		mon:     mo,
		panels:  []panel.Panel{{ID: "p1", Kind: panel.Agent, Title: "claude", State: panel.Running}},
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
	if msg.Panels[0].State != "idle" || msg.Panels[0].Activity == "" {
		t.Fatalf("telemetry should carry state and activity, got %+v", msg.Panels[0])
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
