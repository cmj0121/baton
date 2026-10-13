package server

import (
	"context"
	"testing"

	"github.com/cmj0121/baton/internal/classifier/moldtest"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// classifierStatusOf runs classifier.status over cc and decodes the reply.
func classifierStatusOf(t *testing.T, s *Server, cc *clientConn) proto.ClassifierStatus {
	t.Helper()
	s.onCommand(cc, proto.Command{Action: "classifier.status"})
	msg := reply(t, cc)
	if msg.Type != "classifier" || msg.Classifier == nil {
		t.Fatalf("classifier.status must answer a classifier object, got %+v", msg)
	}
	return *msg.Classifier
}

// TestClassifierStatusOffWithoutAClient: no block configured is "off", and the
// reply says nothing else about a socket it was never given.
func TestClassifierStatusOffWithoutAClient(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	got := classifierStatusOf(t, s, ctl(""))
	if got.Enabled || got.Socket != "" || got.Mold != nil || got.Error != "" {
		t.Fatalf("no client should read as off and nothing else, got %+v", got)
	}
}

// TestClassifierStatusReportsADeadSocket: a configured socket nobody serves is
// "enabled, unreachable": the probe's error rides verbatim, and because a
// failed probe is a failure like any other, the latch is set afterwards.
func TestClassifierStatusReportsADeadSocket(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	path := moldtest.Sock(t)
	withClassifier(t, s, path)
	s.classifierRecord = true

	got := classifierStatusOf(t, s, ctl(""))
	if !got.Enabled || got.Socket != path || got.Scope != "baton-attention" || got.MinConfidence != 0.5 || !got.Record {
		t.Fatalf("the config in force should ride on the reply, got %+v", got)
	}
	if got.Error == "" || got.Mold != nil {
		t.Fatalf("a dead socket should answer with the dial error and no mold, got %+v", got)
	}
	if got.Absent {
		t.Fatalf("Absent is the latch BEFORE the probe, and nothing had tripped it yet: %+v", got)
	}
	if !s.classifier.Absent() {
		t.Fatal("a failed probe should leave the latch set")
	}
}

// TestClassifierStatusProbeClearsTheLatch is the re-arm: a client that had
// written the socket off is asked again by the probe, the fake answers, the
// reply carries mold's identity, and Absent says what the latch read BEFORE —
// which is what tells the operator the fleet had been on the regex alone.
func TestClassifierStatusProbeClearsTheLatch(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	path := moldtest.Sock(t)
	client := withClassifier(t, s, path)

	// Trip the latch while nothing listens, then bring the fake up.
	if _, err := client.Ask(context.Background(), "x"); err == nil {
		t.Fatal("an ask at a dead socket should fail")
	}
	if !client.Absent() {
		t.Fatal("the latch should be set before the probe")
	}
	mold := moldtest.Serve(t, path, func(map[string]any) []byte {
		return moldtest.StatusReply(4772, "0.3.1", "/b", 12, "cpu")
	})

	got := classifierStatusOf(t, s, ctl(""))
	if got.Error != "" || got.Mold == nil {
		t.Fatalf("a live socket should answer with mold's status, got %+v", got)
	}
	want := proto.MoldStatus{PID: 4772, Version: "0.3.1", Bundle: "/b", Keys: 12, Device: "cpu"}
	if *got.Mold != want {
		t.Fatalf("mold = %+v, want %+v", *got.Mold, want)
	}
	if !got.Absent {
		t.Fatalf("Absent should report the latch as it stood before the probe, got %+v", got)
	}
	if client.Absent() {
		t.Fatal("a successful probe should clear the latch")
	}
	if mold.Dials() != 1 {
		t.Fatalf("the probe should be the one dial, got %d", mold.Dials())
	}
}

// TestClassifierStatusIsOpenToAConductor: read-only like worktree.list, so a
// conductor connection gets the answer rather than a role refusal.
func TestClassifierStatusIsOpenToAConductor(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running})
	path := moldtest.Sock(t)
	moldtest.Serve(t, path, func(map[string]any) []byte {
		return moldtest.StatusReply(4772, "0.3.1", "/b", 12, "cpu")
	})
	withClassifier(t, s, path)

	cc := ctl("p1")
	cc.role = roleConductor
	got := classifierStatusOf(t, s, cc)
	if !got.Enabled || got.Mold == nil {
		t.Fatalf("a conductor should get the same answer as the cockpit, got %+v", got)
	}
}
