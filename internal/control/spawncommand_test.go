package control_test

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
)

// TestSpawnCommandKind is the acceptance for #54 at the layer both front ends
// share: the panel that comes back is a COMMAND panel on the wire, not an agent.
// Before the third kind existed this same call — a non-empty binary — could only
// produce Kind "agent", and everything downstream that asks "is this an agent"
// said yes.
func TestSpawnCommandKind(t *testing.T) {
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	id, err := c.SpawnCommand("/bin/sh", []string{"-c", "sleep 30"}, t.TempDir())
	if err != nil {
		t.Fatalf("SpawnCommand: %v", err)
	}
	if id == "" {
		t.Fatal("SpawnCommand returned an empty id")
	}

	panels, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, p := range panels {
		if p.ID != id {
			continue
		}
		found = true
		if p.Kind != proto.KindCommand {
			t.Fatalf("kind = %q, want %q", p.Kind, proto.KindCommand)
		}
	}
	if !found {
		t.Fatalf("panel %q is not in the fleet", id)
	}
}

// TestSpawnPanelStillMeansAgent holds the other half, and it is the half that
// makes the one above mean something: SpawnPanel was NOT re-pointed. A caller
// that names a binary there still gets an agent panel, because that is what every
// existing caller asks it for — the fix is a second verb, not a changed one.
func TestSpawnPanelStillMeansAgent(t *testing.T) {
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	id, err := c.SpawnPanel("/bin/sh", []string{"-c", "sleep 30"}, t.TempDir())
	if err != nil {
		t.Fatalf("SpawnPanel: %v", err)
	}
	panels, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range panels {
		if p.ID == id && p.Kind != proto.KindAgent {
			t.Fatalf("SpawnPanel kind = %q, want %q", p.Kind, proto.KindAgent)
		}
	}
}

// TestSpawnCommandNeedsACommand: refused in the client, before the socket is
// dialled. The connection is closed first, so an error naming an I/O failure
// instead would mean the command had travelled.
func TestSpawnCommandNeedsACommand(t *testing.T) {
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()

	if _, err := c.SpawnCommand("", nil, t.TempDir()); err == nil {
		t.Fatal("an empty command should be refused")
	} else if !strings.Contains(err.Error(), "needs a command") {
		t.Fatalf("want the local refusal, got %v", err)
	}
}
