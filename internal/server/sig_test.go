package server

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// TestLineShape checks the reduction the whole fold rests on: two panels are the
// same when their last line has the same SHAPE, and a counter, a clock, a colour
// escape, or trailing whitespace is not a shape.
func TestLineShape(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want string
	}{
		{"the last line is the one that describes the panel", "starting\nbuilding\nlinking", "linking"},
		{"digits collapse, so a counter is not a difference", "[3/12] building", "[#/#] building"},
		{"trailing whitespace is not a difference", "waiting   \t", "waiting"},
		{"colour is not a difference", "\x1b[32mready\x1b[0m", "ready"},
		{"a per-panel window title is not a difference", "\x1b]0;bash: /srv/node-7\x07$ ", "$"},
		{"an ST-terminated title too", "\x1b]2;zsh\x1b\\$ ", "$"},
		{"a title holding a CSI-looking body is still just a title", "\x1b]0;a[1mb\x07ok", "ok"},
		{"blank trailing lines are skipped", "answer? [y/N]\n\n   \n", "answer? [y/N]"},
		{"a redrawn progress bar is only its last frame", "downloading 10%\rdownloading 90%", "downloading #%"},
		{"nothing but blanks has no shape", "\n\n  \n", ""},
		{"no output at all has no shape", "", ""},
	}
	for _, tc := range cases {
		if got := lineShape([]byte(tc.tail)); got != tc.want {
			t.Errorf("%s: lineShape(%q) = %q, want %q", tc.name, tc.tail, got, tc.want)
		}
	}
}

// TestPanelSig checks the signature says what the fold needs it to say: eight hex
// characters, equal when only the digits moved, different when the shape or the
// state moved. The digit case is the whole reason the signature is a shape rather
// than the line — without it a broadcast to fifty shells produces fifty different
// progress counters and folds nothing.
func TestPanelSig(t *testing.T) {
	base := panelSig(panel.Running, []byte("[3/12] building"))
	if len(base) != 8 || strings.TrimLeft(base, "0123456789abcdef") != "" {
		t.Fatalf("a signature should be 8 hex characters, got %q", base)
	}
	if got := panelSig(panel.Running, []byte("[7/12] building")); got != base {
		t.Errorf("only the counter moved, so the signature should hold: %q vs %q", got, base)
	}
	if got := panelSig(panel.Running, []byte("[3/12] linking")); got == base {
		t.Error("a different last line should change the signature")
	}
	if got := panelSig(panel.Stuck, []byte("[3/12] building")); got == base {
		t.Error("the same line in a different state is a different event, and should hash differently")
	}
	// A state name must not be able to bleed into the line and forge a match.
	if panelSig(panel.Idle, []byte("x")) == panelSig(panel.Running, []byte("")) {
		t.Error("the state and the line shape must stay separable in the hash")
	}

	// A spinner is NOT collapsed: only digit runs are, and a glyph is not a digit.
	// Two agents on different frames therefore read as different, which is what
	// leaves a spinning group with no majority and safely on the positional fold —
	// deliberate, and documented on partitionSimilar. Guessing which glyphs mean
	// "the same animation" would fold panels genuinely doing different things.
	if panelSig(panel.Running, []byte("⠋ thinking")) == panelSig(panel.Running, []byte("⠙ thinking")) {
		t.Error("two spinner frames are not known to be the same thing, and must not hash alike")
	}
}

// TestRefreshSigRecomputesOnlyWhenItCanHaveChanged is the cost guard: a panel
// that has neither spoken nor moved is not re-hashed, because at fifty panels a
// tick that re-reads every tail every second is exactly the thing this feature is
// supposed to make affordable. Both wake conditions are checked by planting a
// sentinel and seeing whether the tick overwrites it.
func TestRefreshSigRecomputesOnlyWhenItCanHaveChanged(t *testing.T) {
	mo, _ := newTestMonitor()
	s := &Server{pty: ptymgr.New(), mon: mo,
		panels: []panel.Panel{{ID: "p1", Kind: panel.Shell, State: panel.Running}}}
	mo.spawned("p1")

	// A panel with no signature yet always gets one, so the very first snapshot
	// after a spawn can already fold.
	if !s.refreshSigLocked("p1", panel.Running) {
		t.Fatal("the first signature is a change, and the tick has to broadcast it")
	}
	first := mo.sig("p1")
	if first == "" {
		t.Fatal("a tracked panel should always end a tick with a signature")
	}

	mo.panels["p1"].sig = "sentinel"
	if s.refreshSigLocked("p1", panel.Running) {
		t.Error("a silent, unmoved panel should report no change")
	}
	if got := mo.sig("p1"); got != "sentinel" {
		t.Errorf("a silent, unmoved panel should not be re-hashed, got %q", got)
	}

	mo.observed("p1", 4) // it said something: the shape may have changed
	if !s.refreshSigLocked("p1", panel.Running) {
		t.Error("re-hashing away a stale value is a change")
	}
	if got := mo.sig("p1"); got != first {
		t.Errorf("new output should re-hash the panel, got %q want %q", got, first)
	}

	// Re-hashing to the SAME value is not a change: a panel repeating itself must
	// not keep the fleet broadcasting.
	mo.observed("p1", 4)
	if s.refreshSigLocked("p1", panel.Running) {
		t.Error("an unchanged signature should report no change, however often it is recomputed")
	}

	mo.panels["p1"].sig = "sentinel"
	if !s.refreshSigLocked("p1", panel.Idle) { // it moved: the state is half the signature
		t.Error("a state change should report a changed signature")
	}
	if got := mo.sig("p1"); got == "sentinel" {
		t.Error("a state change should re-hash the panel")
	}

	// An untracked id is a no-op rather than a panic — the Monitor forgets a panel
	// the moment it exits, and a tick can still name it.
	if s.refreshSigLocked("gone", panel.Exited) {
		t.Error("an untracked panel cannot have changed")
	}
	if got := mo.sig("gone"); got != "" {
		t.Errorf("an untracked panel has no signature, got %q", got)
	}
}

// TestSignatureReachesTheWire checks the fold's input actually leaves the daemon:
// the tick computes it and every outbound panel — snapshot and telemetry alike —
// carries it, since wirePanel is the one builder both go through.
func TestSignatureReachesTheWire(t *testing.T) {
	mo, clk := newTestMonitor()
	cc := &clientConn{out: make(chan proto.ServerMsg, 8), attached: map[string]bool{}}
	s := &Server{
		pty:     ptymgr.New(),
		clients: map[*clientConn]struct{}{cc: {}},
		mon:     mo,
		panels:  []panel.Panel{{ID: "p1", Kind: panel.Shell, Title: "bash", State: panel.Running}},
	}
	mo.spawned("p1")

	clk.add(idleAfter) // settle, so the tick has a reason to broadcast
	msg, ok := s.monitorTick()
	if !ok {
		t.Fatal("a settled panel should produce a telemetry refresh")
	}
	if msg.Panels[0].Sig == "" {
		t.Fatalf("telemetry should carry the similarity signature, got %+v", msg.Panels[0])
	}
	if want := panelSig(panel.Idle, nil); msg.Panels[0].Sig != want {
		t.Errorf("the wire signature should be the settled panel's, got %q want %q", msg.Panels[0].Sig, want)
	}
	if got := s.panelsMsg().Panels[0].Sig; got != msg.Panels[0].Sig {
		t.Errorf("the snapshot and the telemetry frame must agree, got %q vs %q", got, msg.Panels[0].Sig)
	}
}
