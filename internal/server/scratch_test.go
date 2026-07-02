package server_test

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// TestScratchSpawnsEphemeral checks the floating scratch pane's server path:
// panel.scratch spawns a transient PTY, replies "scratch" with its id, tracks it as
// an ephemeral panel (so it never reaches the fleet snapshot or the persisted
// state), and reaps it when the client closes the id. It runs "cat", which sits on
// its PTY reading stdin, so the panel stays alive for the assertions.
func TestScratchSpawnsEphemeral(t *testing.T) {
	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	if err := c.Send(proto.Command{Action: "panel.scratch", Path: "cat", Dir: t.TempDir()}); err != nil {
		t.Fatalf("panel.scratch: %v", err)
	}
	reply := recvEvent(t, c)
	if reply.Type != "scratch" || reply.ID == "" {
		t.Fatalf("expected a scratch reply with an id, got %+v", reply)
	}
	if got := srv.EphemeralCount(); got != 1 {
		t.Fatalf("the scratch pane should be one ephemeral panel, got %d", got)
	}

	// It stays off the fleet: a fresh list shows no panels — the whole point of the
	// ephemeral path is that the scratch never joins the dashboard or the snapshot.
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("panel.list: %v", err)
	}
	snap := recvEvent(t, c)
	if snap.Type != "panels" {
		t.Fatalf("expected a snapshot, got %+v", snap)
	}
	if len(snap.Panels) != 0 {
		t.Fatalf("the scratch must not appear in the fleet snapshot: %d panels", len(snap.Panels))
	}

	// Closing it by its ephemeral id reaps the PTY and empties the tracked set.
	if err := c.Send(proto.Command{Action: "panel.close", ID: reply.ID}); err != nil {
		t.Fatalf("panel.close: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for srv.EphemeralCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("closing the scratch left %d ephemeral panels", srv.EphemeralCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
