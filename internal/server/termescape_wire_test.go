package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// The same argument as termescape_test.go, driven over the wire instead: this is
// the error a cockpit and `baton ctl` actually receive, so the assertion is on
// proto.ServerMsg.Error rather than on a Go error the daemon kept to itself.
const escWireDir = "\x1b[2J\x1b[Hfake"

func TestDiffErrQuotesDir(t *testing.T) {
	requireGitDiff(t)
	plain := filepath.Join(t.TempDir(), escWireDir) // a real dir, and not a git work tree
	if err := os.Mkdir(plain, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, sock := startDiffServer(t)
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, plain)
	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	msg := recvEvent(t, c)
	if msg.Type != "error" {
		t.Fatalf("a diff outside a git repo should error, got %+v", msg)
	}
	if strings.ContainsRune(msg.Error, 0x1b) {
		t.Errorf("a raw ESC reached the client: %q", msg.Error)
	}
	if !strings.Contains(msg.Error, `\x1b`) {
		t.Errorf("the path is not in the message, so the check above proves nothing: %q", msg.Error)
	}
}
