package control_test

import (
	"net"
	"os"
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

	// Dispatch records a brief and is accepted; an unknown panel surfaces the
	// server's error through the client.
	if err := c.Dispatch(agentID, "land the fix"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.Dispatch("999", "nope"); err == nil {
		t.Fatal("dispatch to an unknown panel should error")
	}

	// Group dispatch reaches a real work item, and errors on an unknown one.
	if err := c.Do(proto.Command{Action: "panel.group", Group: "team", IDs: []string{agentID}}); err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := c.DispatchGroup("team", "ship it"); err != nil {
		t.Fatalf("dispatch-group: %v", err)
	}
	if err := c.DispatchGroup("ghost", "nope"); err == nil {
		t.Fatal("dispatch-group to an unknown group should error")
	}

	// Enqueue a backlog task and read it back through the list.
	if err := c.Enqueue("queued work", "team"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	tasks, err := c.Tasks()
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	var queuedID string
	for _, tk := range tasks {
		if tk.Prompt == "queued work" {
			queuedID = tk.ID
		}
	}
	if queuedID == "" {
		t.Fatalf("the enqueued task should appear in the backlog, got %+v", tasks)
	}

	// The same backlog renders as indented JSON for the ctl/MCP surfaces.
	js, err = c.TasksJSON()
	if err != nil {
		t.Fatalf("tasks json: %v", err)
	}
	if !strings.Contains(js, queuedID) {
		t.Fatalf("TasksJSON should mention the queued task, got:\n%s", js)
	}

	// Cancel that one task by id, then enqueue another and drain the whole
	// backlog — both leave the queue empty.
	if err := c.CancelTask(queuedID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := c.Enqueue("more work", ""); err != nil {
		t.Fatalf("enqueue again: %v", err)
	}
	if err := c.DrainQueue(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if tasks, err = c.Tasks(); err != nil {
		t.Fatalf("tasks after drain: %v", err)
	}
	for _, tk := range tasks {
		// Drain clears only the unassigned backlog; tasks already bound to a
		// panel are left to finish.
		if tk.Panel == "" && tk.Status == "queued" {
			t.Fatalf("drain should clear the unassigned backlog, got %+v", tasks)
		}
	}
}

// TestControlDialErrors covers the connect-failure path: dialling a socket that
// no server is listening on surfaces a clear error rather than a nil client.
func TestControlDialErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.sock")
	if _, err := control.DialSocket(missing, "", ""); err == nil {
		t.Fatal("dialling an absent socket should fail")
	}

	// Cancelling an unknown task id is rejected by the server and surfaced.
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.CancelTask("t999"); err == nil {
		t.Fatal("cancelling an unknown task should error")
	}

	// Once the connection is closed, the write paths fail fast rather than block:
	// every wrapper surfaces the encode error.
	_ = c.Close()
	if err := c.Enqueue("after close", ""); err == nil {
		t.Fatal("enqueue on a closed client should error")
	}
	if _, err := c.Tasks(); err == nil {
		t.Fatal("tasks on a closed client should error")
	}
	if _, err := c.TasksJSON(); err == nil {
		t.Fatal("tasks-json on a closed client should error")
	}
}

// TestControlQueueOps exercises the backlog wrappers that reorder or spawn:
// EnqueueSpawn, PromoteTask, and DemoteTask, over a real server.
func TestControlQueueOps(t *testing.T) {
	sock := startServer(t)

	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// EnqueueSpawn records a spawn-on-demand backlog task that carries the
	// command shape and the close-on-done flag.
	if err := c.EnqueueSpawn("do the thing", "team", "/bin/cat", []string{"-u"}, t.TempDir(), true); err != nil {
		t.Fatalf("enqueue-spawn: %v", err)
	}
	// A second plain task so there are two entries to reorder.
	if err := c.Enqueue("second", "team"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	tasks, err := c.Tasks()
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	var spawnID, secondID string
	for _, tk := range tasks {
		switch tk.Prompt {
		case "do the thing":
			spawnID = tk.ID
		case "second":
			secondID = tk.ID
		}
	}
	if spawnID == "" || secondID == "" {
		t.Fatalf("both enqueued tasks should appear, got %+v", tasks)
	}

	// Promote the tail task to the head, then demote it back — both are accepted
	// by the server and reorder the visible backlog.
	if err := c.PromoteTask(secondID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if tasks, err = c.Tasks(); err != nil {
		t.Fatalf("tasks after promote: %v", err)
	}
	if len(tasks) < 2 || tasks[0].ID != secondID {
		t.Fatalf("promoted task should lead the backlog, got %+v", tasks)
	}
	if err := c.DemoteTask(secondID); err != nil {
		t.Fatalf("demote: %v", err)
	}
	if tasks, err = c.Tasks(); err != nil {
		t.Fatalf("tasks after demote: %v", err)
	}
	if len(tasks) < 2 || tasks[len(tasks)-1].ID != secondID {
		t.Fatalf("demoted task should trail the backlog, got %+v", tasks)
	}

	// Unknown ids are rejected by the server and surfaced through the wrappers.
	if err := c.PromoteTask("nope"); err == nil {
		t.Fatal("promoting an unknown task should error")
	}
	if err := c.DemoteTask("nope"); err == nil {
		t.Fatal("demoting an unknown task should error")
	}
}

// TestControlClosedClient covers the write-failure fast paths of the remaining
// wrappers: once the connection is closed every send-first method surfaces the
// encode error instead of blocking or panicking.
func TestControlClosedClient(t *testing.T) {
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()

	// Spawn calls List first, so its initial send fails on the closed conn.
	if _, err := c.Spawn(proto.Command{Action: "panel.create", Kind: proto.KindShell}); err == nil {
		t.Fatal("spawn on a closed client should error")
	}
	// ListJSON wraps List; the underlying send fails.
	if _, err := c.ListJSON(); err == nil {
		t.Fatal("list-json on a closed client should error")
	}
	if _, err := c.SpawnPanel("/bin/cat", nil, ""); err == nil {
		t.Fatal("spawn-panel on a closed client should error")
	}
	if err := c.EnqueueSpawn("x", "", "/bin/cat", nil, "", false); err == nil {
		t.Fatal("enqueue-spawn on a closed client should error")
	}
	if err := c.PromoteTask("x"); err == nil {
		t.Fatal("promote on a closed client should error")
	}
	if err := c.DemoteTask("x"); err == nil {
		t.Fatal("demote on a closed client should error")
	}
	if err := c.DrainQueue(); err == nil {
		t.Fatal("drain on a closed client should error")
	}
}

// TestControlDialHandshakeFails covers DialSocket's post-connect failure path:
// the dial succeeds but the peer closes without ever sending the panels
// snapshot, so readUntilPanels drains to EOF and Dial returns an error.
func TestControlDialHandshakeFails(t *testing.T) {
	// A short temp dir keeps the socket path under the unix-domain length limit.
	dir, err := os.MkdirTemp("", "bs")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// Accept one connection and hang up immediately, never sending a snapshot.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}()

	if _, err := control.DialSocket(sock, "", ""); err == nil {
		t.Fatal("dial against a peer that never sends panels should fail")
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
