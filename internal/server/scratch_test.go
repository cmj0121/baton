package server_test

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// TestScratchActionIsRefused holds the daemon to the compatibility claim in
// proto.ProtocolVersion's comment: panel.scratch was removed WITHOUT a version
// bump, on the grounds that an old cockpit still sending it gets a clean refusal
// rather than a misread. So the refusal has to be the `unknown action` error, it
// has to name the action, and — the half that matters — it must spawn nothing:
// a daemon that quietly opened a PTY for an action it no longer admits to would
// pass a test that only read the error.
func TestScratchActionIsRefused(t *testing.T) {
	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	if err := c.Send(proto.Command{Action: "panel.scratch", Path: "cat", Dir: t.TempDir()}); err != nil {
		t.Fatalf("panel.scratch: %v", err)
	}
	reply := recvEvent(t, c)
	if reply.Type != "error" {
		t.Fatalf("panel.scratch should be refused, got %+v", reply)
	}
	if reply.Error != `unknown action "panel.scratch"` {
		t.Fatalf("unexpected refusal: %q", reply.Error)
	}
	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("a refused action must spawn no PTY, but %d ephemeral panels exist", got)
	}
}
