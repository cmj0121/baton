package control_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
)

// TestASpawnResolvesARelativeDirBeforeItTravels is the client half of the rule
// the daemon enforces. A relative path means something only in the process that
// typed it — `ctl`'s operator shell, or the panel an MCP tool runs in — so it is
// resolved HERE, and what reaches the socket is absolute.
//
// The raw send below is the control: the same relative directory, put on the wire
// unresolved, is refused. Without it this test would pass against a client that
// changed nothing and a server that guarded nothing.
func TestASpawnResolvesARelativeDirBeforeItTravels(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)

	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	if _, err := c.ResolveSpawn(control.SpawnRequest{
		Run: "/bin/sh", Args: []string{"-c", "sleep 30"}, Dir: "work",
	}); err != nil {
		t.Fatalf("ResolveSpawn with a relative dir = %v, want it resolved against the caller's cwd", err)
	}

	_, err = c.Spawn(proto.Command{
		Action: "panel.create", Kind: proto.KindCommand,
		Path: "/bin/sh", Args: []string{"-c", "sleep 30"}, Dir: "work",
	})
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("an unresolved relative dir on the wire = %v, want the daemon's refusal", err)
	}
}

// TestASpawnLeavesAnEmptyDirEmpty keeps the fleet default reachable through the
// same seam: "" is not a path the caller wrote, it is the caller declining to
// write one, and resolving it against the cwd would silently pin every spawn to
// wherever `ctl` happened to be run.
func TestASpawnLeavesAnEmptyDirEmpty(t *testing.T) {
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	id, err := c.ResolveSpawn(control.SpawnRequest{Run: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	if err != nil {
		t.Fatalf("ResolveSpawn with no dir = %v, want the fleet default used", err)
	}
	if id == "" {
		t.Fatal("ResolveSpawn returned an empty id")
	}
}
