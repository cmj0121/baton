package main

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
)

// TestCtlSpawnCommandKind is the issue's own reproduction, fixed: `ctl spawn
// --command time` produces a COMMAND panel. Typed as `--agent time` it produced
// an agent panel, and the fleet then offered it queued work.
func TestCtlSpawnCommandKind(t *testing.T) {
	sock := ctlTestServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	cmd := ctlSpawn{Command: "/bin/sh", Arg: []string{"-c", "sleep 30"}, Dir: t.TempDir()}
	if err := cmd.Run(c); err != nil {
		t.Fatalf("ctl spawn --command: %v", err)
	}

	panels, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(panels) != 1 {
		t.Fatalf("want one panel, got %d", len(panels))
	}
	if panels[0].Kind != proto.KindCommand {
		t.Fatalf("kind = %q, want %q", panels[0].Kind, proto.KindCommand)
	}
}

// TestCtlSpawnCommandRefusals covers what ctl.go itself decides. Both flags name
// the process to run, so honouring either would silently drop the other — and the
// panel would come up looking right with the wrong standing, which is the whole
// defect. --worktree has no command form at all: it spawns an agent in the tree
// it makes.
//
// The client is closed first, so a refusal naming an I/O failure instead would
// mean the command had travelled.
func TestCtlSpawnCommandRefusals(t *testing.T) {
	sock := ctlTestServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()

	cases := []struct {
		name string
		cmd  ctlSpawn
		want string
	}{
		{"agent and command", ctlSpawn{Agent: "/bin/sh", Command: "/bin/sh"}, "pick one"},
		{"command with worktree", ctlSpawn{Command: "/bin/sh", Dir: "/tmp/repo", Branch: "feat/x", Worktree: true}, "no worktree form"},
	}
	for _, tc := range cases {
		err := tc.cmd.Run(c)
		if err == nil {
			t.Errorf("%s: should be refused, got none", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want a refusal containing %q, got %v", tc.name, tc.want, err)
		}
	}
}
