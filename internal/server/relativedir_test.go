package server_test

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// relativeDirs are the shapes a relative directory arrives in. Each one resolves
// against the DAEMON's working directory — the terminal baton was started in —
// and that is what makes them refusals rather than curiosities: "." is the
// operator's launch directory and ".." is its parent, neither of which the caller
// could have meant, since the caller is not the daemon.
var relativeDirs = []string{".", "..", "sub", "../sibling", "./x"}

// TestARelativeDirIsRefusedOnEveryVerbThatCarriesOne pins the guard where it
// lives: one check ahead of the switch, so a verb added later cannot slip a
// relative directory past it by forgetting to ask.
func TestARelativeDirIsRefusedOnEveryVerbThatCarriesOne(t *testing.T) {
	c := startServer(t)

	verbs := []proto.Command{
		{Action: "panel.create", Kind: proto.KindShell},
		{Action: "panel.create", Kind: proto.KindAgent, Path: "/bin/cat"},
		{Action: "task.enqueue", Prompt: "do it", Path: "/bin/cat"},
		{Action: "panel.git", Git: "worktree-add", Name: "feature/x", Path: "/bin/cat"},
	}
	for _, base := range verbs {
		for _, dir := range relativeDirs {
			cmd := base
			cmd.Dir = dir
			if err := c.Send(cmd); err != nil {
				t.Fatalf("send %s: %v", cmd.Action, err)
			}
			msg := recv(t, c)
			if msg.Type != "error" || !strings.Contains(msg.Error, "absolute path") {
				t.Fatalf("%s with dir %q = %+v, want a refusal naming the absolute-path rule", cmd.Action, dir, msg)
			}
		}
	}
}

// TestAnAbsoluteDirStillSpawns is the other half. The guard must refuse the
// ambiguous spelling and nothing else — an operator running a panel in /tmp, or a
// worktree pointed at a sibling checkout, is ordinary use.
func TestAnAbsoluteDirStillSpawns(t *testing.T) {
	c := startServer(t)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: proto.KindShell, Dir: t.TempDir()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg := recv(t, c); msg.Type == "error" {
		t.Fatalf("panel.create with an absolute dir = error %q, want a spawn", msg.Error)
	}
}

// TestAnEmptyDirIsNotRelative keeps the fleet default reachable: an unset
// directory is "wherever the server says", which createPanel resolves, and it
// must not be swept up by a check on the spelling of a path that is not there.
func TestAnEmptyDirIsNotRelative(t *testing.T) {
	c := startServer(t)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: proto.KindShell}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg := recv(t, c); msg.Type == "error" {
		t.Fatalf("panel.create with no dir = error %q, want the fleet default used", msg.Error)
	}
}
