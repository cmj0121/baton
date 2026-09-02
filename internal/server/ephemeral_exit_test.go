package server_test

import (
	"os/exec"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// TestEphemeralSelfExitReaped proves a transient panel that exits on its own is
// reaped from the ephemeral set, not left counting against the per-connection cap
// until the client closes it. It drives the diff pop-up because that is the
// shortest route to a transient PTY whose command is the test's to choose: an
// explicit diff-command takes panel.diff off its structured path, and `true`
// exits 0 at once.
func TestEphemeralSelfExitReaped(t *testing.T) {
	requireGitDiff(t)
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no `true` binary to run a self-exiting panel")
	}
	repo := gitRepoWithChange(t)

	srv, sock := startDiffServer(t, server.WithDiffCommand(truePath))
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	reply := recvEvent(t, c)
	if reply.Type != "ephemeral" {
		t.Fatalf("expected an ephemeral reply, got %+v", reply)
	}

	deadline := time.Now().Add(10 * time.Second)
	for srv.EphemeralCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("a self-exited panel left %d orphan ephemeral panels", srv.EphemeralCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
