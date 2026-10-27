package server

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// writeLog records every writeInput call as its own entry, so a test can tell a
// line written in one piece from the same bytes split across two writes — the
// distinction a concatenating fake erases, and the one issue #154 turns on.
type writeLog struct {
	mu     sync.Mutex
	writes []string
}

func (w *writeLog) write(id string, data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, id+":"+string(data))
}

func (w *writeLog) got() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.writes...)
}

// typeinServer builds a Server through New (so the construction defaults are
// the real ones) over the given panels, with a recording writer and a pid map
// the test controls. The gap is left at whatever the test sets after.
func typeinServer(t *testing.T, panels ...panel.Panel) (*Server, *writeLog, *sync.Map) {
	t.Helper()
	s := New(nil)
	s.panels = panels
	w := &writeLog{}
	s.writeInput = w.write
	pids := &sync.Map{}
	for _, p := range panels {
		pids.Store(p.ID, p.Pid)
	}
	s.pidOf = func(id string) int {
		v, ok := pids.Load(id)
		if !ok {
			return 0
		}
		return v.(int)
	}
	return s, w, pids
}

func equalWrites(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("writes = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("writes = %q, want %q", got, want)
		}
	}
}

func TestNewSetsDefaultSubmitGap(t *testing.T) {
	if s := New(nil); s.submitGap != defaultSubmitGap {
		t.Fatalf("submitGap = %v, want %v", s.submitGap, defaultSubmitGap)
	}
}

func TestTypeLineNonAgentOneWrite(t *testing.T) {
	s, w, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Shell, Pid: 10})
	s.submitGap = 0

	if !s.typeLine("1", false, []byte("ls"), "\n") {
		t.Fatal("a non-agent line must report submitted")
	}
	equalWrites(t, w.got(), []string{"1:ls\n"})
}

func TestTypeLineAgentSplitsTextAndSubmit(t *testing.T) {
	s, w, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: 10})
	s.submitGap = 0

	if !s.typeLine("1", true, []byte("do it"), "\r") {
		t.Fatal("an agent whose process held still must report submitted")
	}
	equalWrites(t, w.got(), []string{"1:do it", "1:\r"})
}

// A pid of 0 before the text means the server cannot see the process, not that
// it changed; the submit is still written (see typeLine).
func TestTypeLineAgentUnknownPidStillSubmits(t *testing.T) {
	s, w, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent})
	s.submitGap = 0

	if !s.typeLine("1", true, []byte("x"), "\n") {
		t.Fatal("a pid the server cannot see must not drop the submit")
	}
	equalWrites(t, w.got(), []string{"1:x", "1:\n"})
}

func TestTypeLinePidChangeDropsSubmit(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after int
	}{
		{"restarted", 10, 11},
		{"exited", 10, 0},
		// An exited panel (pid 0) restarted inside the gap: the fresh agent never
		// saw the text, so the Enter must not reach it.
		{"restarted from exited", 0, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, pids := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: tc.before})
			s.submitGap = time.Millisecond
			// Change the pid between the text and the submit: the writer sees the
			// text go out, then the process moves before the gap ends.
			s.writeInput = func(id string, data []byte) {
				w.write(id, data)
				pids.Store(id, tc.after)
			}

			if s.typeLine("1", true, []byte("x"), "\n") {
				t.Fatal("a submit to a process that changed during the gap must report dropped")
			}
			equalWrites(t, w.got(), []string{"1:x"})
		})
	}
}

// Two senders typing into one panel at once must land as whole lines. Without
// the lane the second text is written during the first line's gap and the order
// becomes text1 text2 submit1 submit2.
func TestTypeLineConcurrentSendsDoNotInterleave(t *testing.T) {
	s, w, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: 10})
	s.submitGap = 5 * time.Millisecond

	firstTyped := make(chan struct{})
	var once sync.Once
	s.writeInput = func(id string, data []byte) {
		w.write(id, data)
		once.Do(func() { close(firstTyped) })
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.typeLine("1", true, []byte("a"), "\n") }()
	// Start the second only once the first is inside its gap, so the order of the
	// two lines is fixed and the lane is what keeps them apart.
	<-firstTyped
	go func() { defer wg.Done(); s.typeLine("1", true, []byte("b"), "\n") }()
	wg.Wait()

	equalWrites(t, w.got(), []string{"1:a", "1:\n", "1:b", "1:\n"})
}

func TestTypeLineThenAsyncWithGap(t *testing.T) {
	s, w, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: 10})
	s.submitGap = 5 * time.Millisecond

	// Hold the writer shut: an inline typeLineThen would block here, and a
	// handed-off one returns at once with done still pending.
	gate := make(chan struct{})
	s.writeInput = func(id string, data []byte) { <-gate; w.write(id, data) }

	var ok, called atomic.Bool
	returned := make(chan struct{})
	go func() {
		s.typeLineThen("1", true, []byte("x"), "\n", func(b bool) { ok.Store(b); called.Store(true) })
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("typeLineThen blocked on the write; the tick must not wait out the line")
	}
	if called.Load() {
		t.Fatal("done ran before the line was written; it was not handed off")
	}
	close(gate)

	s.typing.Wait()
	if !called.Load() || !ok.Load() {
		t.Fatalf("after typing.Wait: called=%v ok=%v, want both true", called.Load(), ok.Load())
	}
	equalWrites(t, w.got(), []string{"1:x", "1:\n"})
}

func TestTypeLineThenInlineWithoutGap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent bool
		gap   time.Duration
	}{
		{"agent zero gap", true, 0},
		{"non-agent with gap", false, 5 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: 10})
			s.submitGap = tc.gap
			called := false
			s.typeLineThen("1", tc.agent, []byte("x"), "\n", func(bool) { called = true })
			if !called {
				t.Fatal("with nothing to wait out, done must run before typeLineThen returns")
			}
		})
	}
}

// Shutdown must let a handed-off line finish before it kills the fleet, so the
// text is never left on screen without its Enter.
func TestShutdownWaitsForTyping(t *testing.T) {
	s, w, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: 10})
	s.submitGap = 5 * time.Millisecond

	s.typeLineThen("1", true, []byte("x"), "\n", func(bool) {})
	s.Shutdown()
	equalWrites(t, w.got(), []string{"1:x", "1:\n"})
}

func TestInputLanesReleasedAfterUse(t *testing.T) {
	s, _, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: 10})
	s.submitGap = time.Millisecond

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); s.typeLine("1", true, []byte("x"), "\n") }()
	}
	wg.Wait()
	s.typeLine("1", false, []byte("y"), "\n")

	s.lanes.mu.Lock()
	defer s.lanes.mu.Unlock()
	if n := len(s.lanes.lanes); n != 0 {
		t.Fatalf("%d lanes left after every holder released; the map leaks", n)
	}
}

func TestSubmitFor(t *testing.T) {
	s, _, _ := typeinServer(t,
		panel.Panel{ID: "grok", Kind: panel.Agent},
		panel.Panel{ID: "plain", Kind: panel.Agent},
		panel.Panel{ID: "sh", Kind: panel.Shell},
	)
	// The profile lives on the spawn spec, as createPanel leaves it — never on
	// the panel record.
	s.specs["grok"] = spawnSpec{Profile: "grok"}
	s.specs["plain"] = spawnSpec{Profile: "claude"}
	s.agentSubmit = map[string]string{"grok": "\r"}

	for _, tc := range []struct {
		name, id, explicit string
		submit             string
		agent              bool
	}{
		{"explicit wins over profile", "grok", "\r\n", "\r\n", true},
		{"profile", "grok", "", "\r", true},
		{"profile without a submit", "plain", "", defaultSubmit, true},
		{"unknown panel", "gone", "", defaultSubmit, false},
		{"non-agent", "sh", "", defaultSubmit, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submit, agent := s.submitFor(tc.id, tc.explicit)
			if submit != tc.submit || agent != tc.agent {
				t.Fatalf("submitFor(%q, %q) = (%q, %v), want (%q, %v)",
					tc.id, tc.explicit, submit, agent, tc.submit, tc.agent)
			}
		})
	}
}

// The built-in default is keyed on the spawn spec's command: a grok with no
// profile still submits with "\r", a profile's configured submit still beats it,
// and a CLI baton knows nothing about keeps defaultSubmit.
func TestSubmitForCommandDefault(t *testing.T) {
	s, _, _ := typeinServer(t,
		panel.Panel{ID: "bare", Kind: panel.Agent},
		panel.Panel{ID: "pinned", Kind: panel.Agent},
		panel.Panel{ID: "claude", Kind: panel.Agent},
	)
	s.specs["bare"] = spawnSpec{Spec: ptymgr.Spec{Command: "/usr/local/bin/grok"}}
	s.specs["pinned"] = spawnSpec{Spec: ptymgr.Spec{Command: "grok"}, Profile: "mygrok"}
	s.specs["claude"] = spawnSpec{Spec: ptymgr.Spec{Command: "claude"}}
	s.agentSubmit = map[string]string{"mygrok": "\n"}

	for _, tc := range []struct {
		name, id, explicit, submit string
	}{
		{"grok without a profile", "bare", "", "\r"},
		{"explicit wins over the command default", "bare", "\r\n", "\r\n"},
		{"profile's configured submit wins", "pinned", "", "\n"},
		{"unknown command", "claude", "", defaultSubmit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if submit, _ := s.submitFor(tc.id, tc.explicit); submit != tc.submit {
				t.Fatalf("submitFor(%q, %q) = %q, want %q", tc.id, tc.explicit, submit, tc.submit)
			}
		})
	}
}

// A profile-less agent spawned through createPanel from a command named grok
// submits with "\r": the default comes from the command the spec records.
func TestSubmitForBareGrok(t *testing.T) {
	s, dir := identityServer(t)
	grok := filepath.Join(t.TempDir(), "grok")
	if err := os.Symlink("/bin/cat", grok); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	id, err := s.createPanel(originOperator, proto.KindAgent, grok, nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if submit, agent := s.submitFor(id, ""); submit != "\r" || !agent {
		t.Fatalf("submitFor(%q, \"\") = (%q, %v), want (%q, true)", id, submit, agent, "\r")
	}
}

// A panel spawned through createPanel resolves its profile's submit: the
// profile is recorded on the spawn spec only, so a submitFor that read the
// panel record would fall back to defaultSubmit here.
func TestSubmitForSpawnedAgentUsesItsProfile(t *testing.T) {
	s, dir := identityServer(t)
	s.agentSubmit = map[string]string{"grok": "\r"}

	id, err := s.createPanel(originOperator, proto.KindAgent, "/bin/cat", nil, dir, "grok", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if submit, agent := s.submitFor(id, ""); submit != "\r" || !agent {
		t.Fatalf("submitFor(%q, \"\") = (%q, %v), want (%q, true)", id, submit, agent, "\r")
	}
}

// A line offered after Shutdown has begun is refused rather than registered on
// s.typing: Shutdown is already waiting (or past it), so an Add now would race
// that Wait, and the line would be cut by the kill that follows.
func TestTypeLineThenRefusedWhileShuttingDown(t *testing.T) {
	s, w, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: 10})
	s.submitGap = 5 * time.Millisecond
	s.mu.Lock()
	s.shuttingDown = true
	s.mu.Unlock()

	var ok, called atomic.Bool
	s.typeLineThen("1", true, []byte("x"), "\n", func(b bool) { ok.Store(b); called.Store(true) })
	s.typing.Wait()
	if !called.Load() || ok.Load() {
		t.Fatalf("called=%v ok=%v, want done(false) at once", called.Load(), ok.Load())
	}
	equalWrites(t, w.got(), nil)
}

// A handed-off line whose write is stuck on a panel that stopped reading must
// not hold Shutdown forever: the kill that would unblock it comes after the wait.
func TestShutdownTypingWaitIsBounded(t *testing.T) {
	s, _, _ := typeinServer(t, panel.Panel{ID: "1", Kind: panel.Agent, Pid: 10})
	s.submitGap = time.Millisecond
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	s.writeInput = func(string, []byte) { <-stuck }

	s.typeLineThen("1", true, []byte("x"), "\n", func(bool) {})
	returned := make(chan struct{})
	go func() { s.Shutdown(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown hung on a typed line whose write never returns")
	}
}

// submitSiteServer is gateServer over one idle grok agent ("g", profile grok,
// whose profile submits with "\r"), one agent with no profile ("c"), and a
// shell ("sh"), all in one peers-on group — the three ways a line can be
// submitted, behind every write site U4 routed through typeLine.
func submitSiteServer(state panel.State) (*Server, *fakeClock, *[]string) {
	s, clk, written := gateServer(
		panel.Panel{ID: "g", Kind: panel.Agent, Title: "grok", Group: "work", State: state},
		panel.Panel{ID: "c", Kind: panel.Agent, Title: "plain", Group: "work", State: panel.Running},
		panel.Panel{ID: "sh", Kind: panel.Shell, Title: "term", Group: "work", State: panel.Running},
	)
	s.specs["g"] = spawnSpec{Profile: "grok"}
	s.agentSubmit = map[string]string{"grok": "\r"}
	s.groupPeers = map[string]bool{"work": true}
	return s, clk, written
}

// Every write site ends an agent's line with its PROFILE's submit, as a write of
// its own after the text (#154). Each case would fail on the pre-split code
// twice over: one joined write, and "\n" where the profile says "\r".
func TestEveryWriteSiteSubmitsWithTheProfileSequence(t *testing.T) {
	want := []string{"g:go", "g:\r"}

	t.Run("peer.send", func(t *testing.T) {
		s, _, written := submitSiteServer(panel.Idle)
		if msg := peerDo(t, s, "c", proto.Command{Action: "peer.send", ID: "g", Data: []byte("go")}); msg.Type != "notice" {
			t.Fatalf("the send should be admitted, got %+v", msg)
		}
		if w := []string{"g:[from plain (c)] go", "g:\r"}; !slices.Equal(*written, w) {
			t.Fatalf("typed %q, want %q", *written, w)
		}
	})
	t.Run("a direct dispatch with no submit", func(t *testing.T) {
		s, _, written := submitSiteServer(panel.Idle)
		if err := s.dispatchPanel("g", "go", ""); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if !slices.Equal(*written, want) {
			t.Fatalf("typed %q, want %q", *written, want)
		}
	})
	t.Run("a dispatch held for a busy panel", func(t *testing.T) {
		s, clk, written := submitSiteServer(panel.Running)
		if err := s.dispatchPanel("g", "go", ""); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if len(*written) != 0 {
			t.Fatalf("a busy panel's dispatch must be held, got %q", *written)
		}
		settle(s, clk)
		if !slices.Equal(*written, want) {
			t.Fatalf("typed %q, want %q", *written, want)
		}
	})
	t.Run("a queued task", func(t *testing.T) {
		s, _, written := submitSiteServer(panel.Idle)
		if _, err := s.enqueueTask(conn(""), "go", "", nil); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		s.monitorTick()
		if !slices.Equal(*written, want) {
			t.Fatalf("typed %q, want %q", *written, want)
		}
	})
}

// A dispatch that names its own submit overrides the profile's; precedence is
// submitFor's, and the dispatch path must hand it the command's sequence.
func TestADispatchSubmitWinsOverTheProfile(t *testing.T) {
	s, _, written := submitSiteServer(panel.Idle)
	if err := s.dispatchPanel("g", "go", "\r\n"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if want := []string{"g:go", "g:\r\n"}; !slices.Equal(*written, want) {
		t.Fatalf("typed %q, want %q", *written, want)
	}
}

// An agent with no profile — a restored panel, or one whose profile was removed
// — still gets the split, with the default submit.
func TestAProfilelessAgentGetsTheDefaultSplit(t *testing.T) {
	s, _, written := submitSiteServer(panel.Idle)
	if msg := peerDo(t, s, "g", proto.Command{Action: "peer.send", ID: "c", Data: []byte("hi")}); msg.Type != "notice" {
		t.Fatalf("the send should be admitted, got %+v", msg)
	}
	s.panels[1].State = panel.Idle
	if err := s.dispatchPanel("c", "go", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	want := []string{"c:[from grok (g)] hi", "c:\n", "c:go", "c:\n"}
	if !slices.Equal(*written, want) {
		t.Fatalf("typed %q, want %q", *written, want)
	}
}

// A shell is typed into exactly as before the split: ONE write, text and submit
// together, byte for byte. A sender still cannot choose the submit.
func TestAShellLineStaysOneWrite(t *testing.T) {
	s, _, written := submitSiteServer(panel.Idle)
	if msg := peerDo(t, s, "g", proto.Command{Action: "peer.send", ID: "sh", Data: []byte("make test")}); msg.Type != "notice" {
		t.Fatalf("the send should be admitted, got %+v", msg)
	}
	if want := []string{"sh:make test\n"}; !slices.Equal(*written, want) {
		t.Fatalf("typed %q, want %q", *written, want)
	}
	msg := peerDo(t, s, "g", proto.Command{Action: "peer.send", ID: "sh", Data: []byte("x"), Submit: "\r"})
	if msg.Type != "error" {
		t.Fatalf("a sender-chosen submit must be refused, got %+v", msg)
	}
	if len(*written) != 1 {
		t.Fatalf("a refused send typed %q", *written)
	}
}

// A line whose Enter was dropped — the panel's process changed between the text
// and the submit — reached no agent as a brief, so it must not count as the
// operator's reinforcement. Both delivery moments are walked: the direct write
// (dispatchScored) and the held one the tick carries out (deliver).
func TestADroppedSubmitCountsNoSignal(t *testing.T) {
	for _, tc := range []struct {
		name string
		busy bool
	}{
		{"a direct dispatch", false},
		{"a held dispatch delivered on the tick", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := scoreStore(t)
			s, clk, _ := scoreServer(st)
			if tc.busy {
				s.panels[0].State = panel.Spawning
			}
			e := seedEntry(t, st, "keep the build green")
			// The process is replaced as soon as the text lands, so the pid read
			// after the gap differs from the one read before it.
			var pid atomic.Int32
			pid.Store(10)
			s.pidOf = func(string) int { return int(pid.Load()) }
			w := &writeLog{}
			s.writeInput = func(id string, data []byte) { w.write(id, data); pid.Store(11) }

			dispatchTo(t, s, conn(""), "keep the build green")
			if tc.busy {
				settle(s, clk)
			}
			if got := w.got(); len(got) != 1 {
				t.Fatalf("writes = %q, want the text alone, its submit dropped", got)
			}
			if got := entryNow(t, st, e.Id); got.UserSignals != 0 {
				t.Fatalf("user signals = %d, want a dropped submit to count nothing", got.UserSignals)
			}
		})
	}
}

// With a real gap the tick hands the line off rather than sleeping, and the
// reinforcement lands when the line is submitted, not when the tick returns.
func TestAHeldDeliveryWithAGapSignalsAfterTheSubmit(t *testing.T) {
	st, _ := scoreStore(t)
	s, clk, _ := busyScoreServer(st)
	s.submitGap = 5 * time.Millisecond
	w := &writeLog{}
	s.writeInput = w.write
	e := seedEntry(t, st, "keep the build green")

	dispatchTo(t, s, conn(""), "keep the build green")
	settle(s, clk)
	s.typing.Wait()

	if got := w.got(); len(got) != 2 || got[1] != "p1:"+defaultSubmit {
		t.Fatalf("writes = %q, want the text then its submit", got)
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 1 {
		t.Fatalf("user signals = %d, want the submitted line counted once", got.UserSignals)
	}
}

// A direct dispatch must not wait out the gap: dispatchScored is reached from
// the plugin's baton.dispatch and dispatch_group on the ONE Lua worker, and a
// worker held 50ms per agent member makes every concurrent task.pre hook fail
// open past its timeout — vetoes silently skipped. So the line is handed off,
// and the operator's reinforcement is counted when the submit lands.
func TestADirectDispatchDoesNotWaitOutTheGap(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	s.submitGap = 5 * time.Millisecond
	e := seedEntry(t, st, "keep the build green")

	// Hold the writer shut: a dispatch that typed inline would block on it.
	gate := make(chan struct{})
	w := &writeLog{}
	s.writeInput = func(id string, data []byte) { <-gate; w.write(id, data) }

	returned := make(chan struct{})
	go func() {
		dispatchTo(t, s, conn(""), "keep the build green")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		close(gate)
		t.Fatal("dispatchScored blocked on the line; a plugin's dispatch would hold the Lua worker")
	}
	if got := w.got(); len(got) != 0 {
		t.Fatalf("writes = %q before the gate opened", got)
	}
	close(gate)

	s.typing.Wait()
	if got := w.got(); len(got) != 2 || got[1] != "p1:"+defaultSubmit {
		t.Fatalf("writes = %q, want the text then its submit", got)
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 1 {
		t.Fatalf("user signals = %d, want the submitted line counted once", got.UserSignals)
	}
}
