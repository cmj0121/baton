package control_test

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

func startServer(t *testing.T) string {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = server.New(ln).Serve() }()
	return sock
}

// TestControlRoundtrips drives the fleet through the control client: spawn,
// list, group, send input, and close all resolve synchronously.
func TestControlRoundtrips(t *testing.T) {
	sock := startServer(t)

	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	id, err := c.Spawn(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if id == "" {
		t.Fatal("spawn returned an empty id")
	}

	panels, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(panels) != 1 || panels[0].ID != id {
		t.Fatalf("list should show the spawned panel, got %+v", panels)
	}

	if err := c.Do(proto.Command{Action: "panel.group", Group: "work", IDs: []string{id}}); err != nil {
		t.Fatalf("group: %v", err)
	}
	if panels, _ = c.List(); panels[0].Group != "work" {
		t.Fatalf("panel should be grouped, got %+v", panels[0])
	}

	// Prompt injection into the panel resolves without error.
	if err := c.Do(proto.Command{Action: "panel.input", ID: id, Data: []byte("echo hi\n")}); err != nil {
		t.Fatalf("send input: %v", err)
	}

	if err := c.Do(proto.Command{Action: "panel.close", IDs: []string{id}}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if panels, _ = c.List(); len(panels) != 0 {
		t.Fatalf("fleet should be empty after close, got %+v", panels)
	}
}

// TestControlHelpers exercises the semantic wrappers shared with the MCP tools:
// SpawnPanel (agent and shell), ListJSON, and SendText (submit on/off).
func TestControlHelpers(t *testing.T) {
	sock := startServer(t)

	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Agent spawn (with args) and a plain shell spawn.
	agentID, err := c.SpawnPanel("/bin/cat", []string{"-u"}, t.TempDir())
	if err != nil {
		t.Fatalf("spawn agent: %v", err)
	}
	shellID, err := c.SpawnPanel("", nil, "")
	if err != nil {
		t.Fatalf("spawn shell: %v", err)
	}

	js, err := c.ListJSON()
	if err != nil {
		t.Fatalf("list json: %v", err)
	}
	if !strings.Contains(js, agentID) || !strings.Contains(js, shellID) {
		t.Fatalf("ListJSON should mention both panels, got:\n%s", js)
	}

	if err := c.SendText(agentID, "submitted", true); err != nil {
		t.Fatalf("send submit: %v", err)
	}
	if err := c.SendText(agentID, "no newline", false); err != nil {
		t.Fatalf("send no-submit: %v", err)
	}
}

// TestControlConductorFenced confirms the env-driven conductor identity reaches
// the server: a control client that inherits BATON_ROLE/BATON_PANEL_ID is fenced
// off from acting on its own panel.
func TestControlConductorFenced(t *testing.T) {
	sock := startServer(t)

	admin, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial admin: %v", err)
	}
	defer func() { _ = admin.Close() }()
	selfID, err := admin.Spawn(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	if err != nil {
		t.Fatalf("spawn self: %v", err)
	}

	t.Setenv(paths.EnvSocket, sock)
	t.Setenv(paths.EnvRole, "conductor")
	t.Setenv(paths.EnvPanelID, selfID)

	cond, err := control.Dial()
	if err != nil {
		t.Fatalf("dial conductor: %v", err)
	}
	defer func() { _ = cond.Close() }()

	err = cond.Do(proto.Command{Action: "panel.close", IDs: []string{selfID}})
	if err == nil || !strings.Contains(err.Error(), "own panel") {
		t.Fatalf("conductor self-close should be refused, got %v", err)
	}
}
