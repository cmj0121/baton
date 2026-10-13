package server

import (
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/classifier"
	"github.com/cmj0121/baton/internal/classifier/moldtest"
	"github.com/cmj0121/baton/internal/panel"
)

// testClient is the client every classifier test in this package asks with:
// the fake's socket, the scope it answers from, and a timeout short enough
// that a dead socket costs the suite nothing.
func testClient(path string) *classifier.Client {
	return classifier.New(classifier.Config{
		Socket: path, Scope: "baton-attention", MinConfidence: 0.5, Timeout: 500 * time.Millisecond,
	}, nil)
}

// classifierServer is gateServer with a live classifier behind a fake mold and
// the worker running, stopped with the test. The panel is a running agent whose
// tail the regex passes over, so only the classifier can raise it.
func classifierServer(t *testing.T, reply func(req map[string]any) []byte) (*Server, *fakeClock, *moldtest.Fake) {
	t.Helper()
	s, clk, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	path := moldtest.Sock(t)
	mold := moldtest.Serve(t, path, reply)
	withClassifier(t, s, path)
	return s, clk, mold
}

// withClassifier attaches a testClient at path to s, makes the ask queue, and
// runs the worker until the test ends — the wiring New and the Monitor do for a
// daemon. It returns the client so a test can trip or read its latch.
func withClassifier(t *testing.T, s *Server, path string) *classifier.Client {
	t.Helper()
	c := testClient(path)
	s.classifier = c
	s.classQ = make(chan classReq, classQueueCap)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go s.classifyLoop(stop)
	return c
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// asked reports whether the classifier holds an "already asked" entry for id,
// under the lock the worker forgets it under.
func asked(s *Server, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.classAskedLocked(id)
}

// TestClassifierRaisesAPlainTailFromIdle is the unit's acceptance row: a quiet
// agent whose tail has no question mark settles to idle on the first tick (the
// regex says no, and the ask goes out), and the classifier's "attention" lands
// on the next tick — from idle, which looksAtt could never do — with the
// snapshot naming the classifier as the source. Output takes the hand down and
// the source with it.
func TestClassifierRaisesAPlainTailFromIdle(t *testing.T) {
	s, clk, mold := classifierServer(t, func(map[string]any) []byte { return moldtest.AskReply("attention", 0.9, "baton-attention") })
	s.pty = scriptPty(t, "p1", "Your call, and then I continue", "I continue")

	settle(s, clk)
	if stateOf(s, "p1") != panel.Idle {
		t.Fatalf("the regex passes a plain tail; the first tick settles to idle, got %v", stateOf(s, "p1"))
	}
	waitFor(t, "the classifier's answer", func() bool { return hasHit(s, "p1") })
	if mold.Dials() != 1 {
		t.Fatalf("one tail, one ask, got %d dials", mold.Dials())
	}

	clk.add(monitorInterval)
	s.monitorTick()
	if stateOf(s, "p1") != panel.Attention {
		t.Fatalf("the second tick applies the answer, got %v", stateOf(s, "p1"))
	}
	if got := sourceOf(s, "p1"); got != "classifier" {
		t.Fatalf("the snapshot should blame the classifier, got %q", got)
	}
	if hasHit(s, "p1") {
		t.Fatal("an applied hit is consumed")
	}

	// The same screen is not asked twice, tick after tick.
	clk.add(monitorInterval)
	s.monitorTick()
	if mold.Dials() != 1 {
		t.Fatalf("an unchanged tail must not be re-asked, got %d dials", mold.Dials())
	}

	s.routeOutput("p1", []byte("ok, continuing"), 0)
	if stateOf(s, "p1") != panel.Running || sourceOf(s, "p1") != "" {
		t.Fatalf("output wakes the panel and drops the source, got %v %q", stateOf(s, "p1"), sourceOf(s, "p1"))
	}
}

// TestClassifierNoIsNotAnAnswer: a low score, another key, and another scope
// each leave the panel where the regex put it. The ask did go out — the regex
// said no, the question was worth asking — but nothing came back worth acting on.
func TestClassifierNoIsNotAnAnswer(t *testing.T) {
	cases := []struct {
		name  string
		reply []byte
	}{
		{"below the floor", moldtest.AskReply("attention", 0.2, "baton-attention")},
		{"another key", moldtest.AskReply("billing", 0.9, "baton-attention")},
		{"another scope", moldtest.AskReply("attention", 0.9, "other")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, clk, mold := classifierServer(t, func(map[string]any) []byte { return tc.reply })
			s.pty = scriptPty(t, "p1", "Your call, and then I continue", "I continue")

			settle(s, clk)
			waitFor(t, "the ask to go out", func() bool { return mold.Dials() == 1 })
			// Give a wrong-headed worker every chance to record a hit before asserting.
			time.Sleep(100 * time.Millisecond)
			for range 3 {
				clk.add(monitorInterval)
				s.monitorTick()
			}
			if stateOf(s, "p1") != panel.Idle {
				t.Fatalf("a no leaves the regex's idle in place, got %v", stateOf(s, "p1"))
			}
			if got := sourceOf(s, "p1"); got != "" {
				t.Fatalf("an idle panel has no source, got %q", got)
			}
		})
	}
}

// TestClassifierIgnoresAStaleAnswer is R3: the answer carries the hash of the
// tail it was asked about, and a panel whose screen changed in between keeps
// the state the ladder gave it — the hit is dropped, not applied to the new
// screen.
func TestClassifierIgnoresAStaleAnswer(t *testing.T) {
	s, clk, _ := classifierServer(t, func(req map[string]any) []byte {
		if strings.Contains(req["text"].(string), "Your call") {
			return moldtest.AskReply("attention", 0.9, "baton-attention")
		}
		return moldtest.AskReply("attention", 0.1, "baton-attention")
	})
	s.pty = scriptPty(t, "p1", "Your call, and then I continue", "I continue")

	settle(s, clk)
	waitFor(t, "the classifier's answer", func() bool { return hasHit(s, "p1") })

	// The screen moved on before the answer was applied.
	s.pty = scriptPty(t, "p1", "Compiling 12 of 40", "12 of 40")
	clk.add(monitorInterval)
	s.monitorTick()
	if stateOf(s, "p1") != panel.Idle {
		t.Fatalf("an answer about a tail no longer on screen must not raise the panel, got %v", stateOf(s, "p1"))
	}
	if hasHit(s, "p1") {
		t.Fatal("a stale hit is dropped, not kept for a screen that may come back")
	}
}

// TestNoClassifierAsksNothing: without the block nothing is enqueued and no
// hash is remembered — today's fleet pays nothing for the feature it did not
// turn on.
func TestNoClassifierAsksNothing(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	s.classQ = make(chan classReq, classQueueCap)
	s.pty = scriptPty(t, "p1", "Your call, and then I continue", "I continue")

	settle(s, clk)
	if stateOf(s, "p1") != panel.Idle {
		t.Fatalf("got %v, want idle", stateOf(s, "p1"))
	}
	if len(s.classQ) != 0 || len(s.classAsked) != 0 {
		t.Fatalf("nothing should be asked without a classifier: queue %d, asked %v", len(s.classQ), s.classAsked)
	}
}

// TestRaisedBySaysWhichGuess: the regex's own raise is blamed on the tail, a
// declaration over it reads as declared, and leaving attention forgets the entry.
func TestRaisedBySaysWhichGuess(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	s.pty = scriptPty(t, "p1", "Apply this refactor?", "refactor?")

	settle(s, clk)
	if stateOf(s, "p1") != panel.Attention || sourceOf(s, "p1") != "tail" {
		t.Fatalf("a regex raise is the tail's, got %v %q", stateOf(s, "p1"), sourceOf(s, "p1"))
	}

	s.declared["p1"] = &declaration{Reason: "which migration?"}
	if got := sourceOf(s, "p1"); got != "declared" {
		t.Fatalf("a standing declaration outranks the guess on the wire, got %q", got)
	}
	delete(s.declared, "p1")

	s.routeOutput("p1", []byte("done"), 0)
	if _, ok := s.raisedBy["p1"]; ok || sourceOf(s, "p1") != "" {
		t.Fatalf("leaving attention forgets who raised it, got %v", s.raisedBy)
	}
}

// TestAnAskThatFailedIsAskedAgainOnceMoldIsBack: the daemon came up before
// `mold serve`, a panel settled on a screen that never changes, and the one
// ask it got was a dial error. The status probe that re-arms the latch (D9)
// must also re-ask that screen, or the panel sits on `Password:` for the
// afternoon — the order the e2e did not run.
func TestAnAskThatFailedIsAskedAgainOnceMoldIsBack(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	path := moldtest.Sock(t) // nobody listens yet
	withClassifier(t, s, path)
	s.pty = scriptPty(t, "p1", "Password:", "Password:")

	settle(s, clk)
	waitFor(t, "the latch to trip on the dead socket", func() bool { return s.classifier.Absent() })
	waitFor(t, "the failed ask to be forgotten", func() bool { return !asked(s, "p1") })

	// mold comes up, answering attention; the operator runs `classifier status`.
	mold := moldtest.Serve(t, path, func(req map[string]any) []byte {
		if req["op"] == "status" {
			return moldtest.StatusReply(1, "0.1", "/b", 1, "cpu")
		}
		return moldtest.AskReply("attention", 0.9, "baton-attention")
	})
	cc := ctl("")
	s.classifierStatus(cc)
	if s.classifier.Absent() {
		t.Fatal("the probe should have re-armed the client")
	}

	// The unchanged screen is asked on the next tick, and raised on the one after.
	clk.add(monitorInterval)
	s.monitorTick()
	waitFor(t, "the re-ask to be answered", func() bool { return hasHit(s, "p1") })
	clk.add(monitorInterval)
	s.monitorTick()
	if stateOf(s, "p1") != panel.Attention || sourceOf(s, "p1") != "classifier" {
		t.Fatalf("the re-asked screen should be raised by the classifier, got %v %q", stateOf(s, "p1"), sourceOf(s, "p1"))
	}
	if mold.Dials() != 2 { // one status probe, one ask
		t.Fatalf("want one probe and one ask after mold came back, got %d dials", mold.Dials())
	}
}
