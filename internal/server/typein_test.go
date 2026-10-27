package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
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
		panel.Panel{ID: "grok", Kind: panel.Agent, Profile: "grok"},
		panel.Panel{ID: "plain", Kind: panel.Agent, Profile: "claude"},
		panel.Panel{ID: "sh", Kind: panel.Shell},
	)
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
