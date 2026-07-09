package server_test

import (
	"os/exec"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// TestScratchSelfExitReaped proves an ephemeral panel that exits on its own is
// reaped from the ephemeral set, not left counting against the per-connection cap
// until the client closes it. A scratch shell running a command that exits at once
// should leave the set empty once its process ends.
func TestScratchSelfExitReaped(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no `true` binary to run a self-exiting scratch panel")
	}
	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	// panel.scratch runs Path directly (no args) — `true` exits 0 immediately.
	if err := c.Send(proto.Command{Action: "panel.scratch", Path: truePath}); err != nil {
		t.Fatalf("panel.scratch: %v", err)
	}
	reply := recvEvent(t, c)
	if reply.Type != "scratch" {
		t.Fatalf("expected a scratch reply, got %+v", reply)
	}

	deadline := time.Now().Add(2 * time.Second)
	for srv.EphemeralCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("a self-exited scratch panel left %d orphan ephemeral panels", srv.EphemeralCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
