package server_test

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
)

// TestRespawnForgetsTheAcknowledgement. An acknowledgement is about a RUN, not
// about a slot: it records that a human dealt with what this process was doing.
// Re-running the panel starts a new process with a clean state clock and a
// cleared exit code, and a record left over from the run before would suppress
// the new one's first call for help.
//
// Nothing user-visible depends on it today — a re-run panel takes a while to
// climb back into the queue — but every other per-panel map is dropped on this
// path, and it is the asymmetry that invites the bug later.
func TestRespawnForgetsTheAcknowledgement(t *testing.T) {
	c := startServer(t)

	// /bin/cat sits on its stdin forever, so the panel is live until it is
	// signalled and live again once it is re-run. That is what makes the assertion
	// below about the RESPAWN rather than about an exit that happened to follow it.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell", Path: "/bin/cat"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	if err := c.Send(proto.Command{Action: "panel.signal", IDs: []string{id}, Signal: "SIGKILL"}); err != nil {
		t.Fatalf("signal: %v", err)
	}
	waitPanel(t, c, id, func(p proto.Panel) bool { return p.State == "exited" })

	if err := c.Send(proto.Command{Action: "panel.ack", ID: id}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	waitPanel(t, c, id, func(p proto.Panel) bool { return p.Acked })

	if err := c.Send(proto.Command{Action: "panel.respawn", ID: id}); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	live := waitPanel(t, c, id, func(p proto.Panel) bool { return p.State != "exited" })
	if live.Acked {
		t.Fatalf("a re-run panel carries no acknowledgement from the run before it, got %+v", live)
	}
}

// waitPanel pulls fleet snapshots until one carries the named panel in a state
// want accepts, and returns it. recv fails the test on timeout, so a queue that
// never produces the frame ends the test rather than hanging it.
func waitPanel(t *testing.T, c *client.Client, id string, want func(proto.Panel) bool) proto.Panel {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msg := recv(t, c)
		if msg.Type != "panels" && msg.Type != "telemetry" {
			continue
		}
		for _, p := range msg.Panels {
			if p.ID == id && want(p) {
				return p
			}
		}
	}
	t.Fatalf("no snapshot ever showed panel %q in the state the test was waiting for", id)
	return proto.Panel{}
}
