package server_test

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// recv pulls the next server message or fails on timeout.
func recv(t *testing.T, c *client.Client) proto.ServerMsg {
	t.Helper()
	select {
	case msg, ok := <-c.Events:
		if !ok {
			t.Fatal("event channel closed unexpectedly")
		}
		return msg
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for server message")
		return proto.ServerMsg{}
	}
}

func TestAttachAndCreateShellPanel(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	srv := server.New(ln)
	go func() { _ = srv.Serve() }()

	// Attach: handshake yields a welcome and an (empty) panel snapshot.
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	const base = 0 // the server starts with no panels
	if got := recv(t, c); got.Type != "welcome" || got.Version != proto.ProtocolVersion {
		t.Fatalf("expected welcome %s, got %+v", proto.ProtocolVersion, got)
	}
	if got := recv(t, c); got.Type != "panels" || len(got.Panels) != base {
		t.Fatalf("expected an empty panels snapshot, got %+v", got)
	}

	// Create a shell panel; the server broadcasts the updated snapshot.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := recv(t, c)
	if got.Type != "panels" || len(got.Panels) != base+1 {
		t.Fatalf("expected %d panels after create, got %+v", base+1, got)
	}
	created := got.Panels[len(got.Panels)-1]
	if created.Kind != "shell" || created.ID == "" {
		t.Fatalf("unexpected created panel %+v", created)
	}

	// Create with an explicit command path; its basename names the panel.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell", Path: "/bin/sh"}); err != nil {
		t.Fatalf("send create with path: %v", err)
	}
	got = recv(t, c)
	withPath := got.Panels[len(got.Panels)-1]
	if !strings.HasPrefix(withPath.Title, "sh #") {
		t.Fatalf("explicit command should name the panel by basename, got %q", withPath.Title)
	}
	// Close it again to return to base+1 for the close assertion below.
	if err := c.Send(proto.Command{Action: "panel.close", ID: withPath.ID}); err != nil {
		t.Fatalf("send close: %v", err)
	}
	recv(t, c)

	// Close it again; the server broadcasts the panel gone.
	if err := c.Send(proto.Command{Action: "panel.close", ID: created.ID}); err != nil {
		t.Fatalf("send close: %v", err)
	}
	got = recv(t, c)
	if got.Type != "panels" || len(got.Panels) != base {
		t.Fatalf("expected %d panels after close, got %+v", base, got)
	}
	for _, p := range got.Panels {
		if p.ID == created.ID {
			t.Fatalf("closed panel %s still present", created.ID)
		}
	}

	// Re-attach a second client: it should see the persisted fleet immediately.
	c2, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("re-attach dial: %v", err)
	}
	defer func() { _ = c2.Close() }()
	if got := recv(t, c2); got.Type != "welcome" {
		t.Fatalf("expected welcome on re-attach, got %+v", got)
	}
	if got := recv(t, c2); got.Type != "panels" || len(got.Panels) != base {
		t.Fatalf("re-attached client should see %d panels, got %+v", base, got)
	}
}

func TestExitMarks(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	// Make the shell exit on its own; the server should mark the panel exited
	// and broadcast it.
	if err := c.Send(proto.Command{Action: "panel.input", ID: id, Data: []byte("exit\n")}); err != nil {
		t.Fatalf("input: %v", err)
	}

	deadline := time.After(15 * time.Second)
	for {
		select {
		case msg := <-c.Events:
			if msg.Type == "panels" {
				for _, p := range msg.Panels {
					if p.ID == id && p.State == "exited" {
						return // detected
					}
				}
			}
		case <-deadline:
			t.Fatal("panel was not marked exited after the shell exited")
		}
	}
}

func TestStatsOnAttach(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // panels

	// The server seeds a stats sample right after the handshake on the dedicated
	// Stats channel; it is measured on the server (the backend), so total memory
	// must be real.
	select {
	case got := <-c.Stats:
		if got.Type != "stats" {
			t.Fatalf("expected a stats message, got %+v", got)
		}
		if got.MemTotal == 0 || got.MemUsed == 0 || got.MemUsed > got.MemTotal {
			t.Fatalf("stats out of range: used=%d total=%d", got.MemUsed, got.MemTotal)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no stats sample arrived after attach")
	}
}

func TestPurgeExited(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	// One panel that will exit, and one that stays alive.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create dying: %v", err)
	}
	dying := recv(t, c).Panels[0].ID
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create live: %v", err)
	}
	live := recv(t, c).Panels[1].ID

	// Make the first one exit and wait for it to be marked exited.
	if err := c.Send(proto.Command{Action: "panel.input", ID: dying, Data: []byte("exit\n")}); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitExited := func() {
		deadline := time.After(15 * time.Second)
		for {
			select {
			case msg := <-c.Events:
				if msg.Type != "panels" {
					continue
				}
				for _, p := range msg.Panels {
					if p.ID == dying && p.State == "exited" {
						return
					}
				}
			case <-deadline:
				t.Fatal("dying panel never reached exited state")
			}
		}
	}
	waitExited()

	// Purge: the exited panel goes, the live one stays.
	if err := c.Send(proto.Command{Action: "panel.purge"}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	got := recv(t, c)
	if got.Type != "panels" || len(got.Panels) != 1 || got.Panels[0].ID != live {
		t.Fatalf("after purge expected only the live panel, got %+v", got.Panels)
	}

	// Purging again with nothing exited is a silent no-op (no broadcast): the
	// next message we force is a fresh list with the live panel still present.
	if err := c.Send(proto.Command{Action: "panel.purge"}); err != nil {
		t.Fatalf("purge again: %v", err)
	}
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := recv(t, c); len(got.Panels) != 1 || got.Panels[0].ID != live {
		t.Fatalf("live panel should remain after a no-op purge, got %+v", got.Panels)
	}
}

func TestAttachIO(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	// Spawn a shell and grab its id.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	// Attach and type a command; its output streams back as "output" messages.
	if err := c.Send(proto.Command{Action: "panel.attach", ID: id}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := c.Send(proto.Command{Action: "panel.input", ID: id, Data: []byte("echo baton-xyz\n")}); err != nil {
		t.Fatalf("input: %v", err)
	}

	deadline := time.After(15 * time.Second)
	found := false
	for !found {
		select {
		case msg := <-c.Output: // the client routes "output" to its own channel
			if msg.ID == id && strings.Contains(string(msg.Data), "baton-xyz") {
				found = true
			}
		case <-deadline:
			t.Fatal("expected echoed output for the attached panel")
		}
	}

	// Detach stops the stream.
	if err := c.Send(proto.Command{Action: "panel.detach", ID: id}); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if err := c.Send(proto.Command{Action: "panel.close", ID: id}); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestGroupAndRename(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	// Two panels to file under one work item.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create a: %v", err)
	}
	a := recv(t, c).Panels[0].ID
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create b: %v", err)
	}
	b := recv(t, c).Panels[1].ID

	// Group both under "api". The broadcast carries Group on each member.
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a, b}, Group: "api"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	got := recv(t, c)
	for _, p := range got.Panels {
		if p.Group != "api" {
			t.Fatalf("panel %s should be in group api, got %q", p.ID, p.Group)
		}
	}

	// Ungroup dissolves the work item: both members go back to no group.
	if err := c.Send(proto.Command{Action: "panel.ungroup", Group: "api"}); err != nil {
		t.Fatalf("ungroup: %v", err)
	}
	got = recv(t, c)
	for _, p := range got.Panels {
		if p.Group != "" {
			t.Fatalf("panel %s should be ungrouped, got %q", p.ID, p.Group)
		}
	}
	// Ungrouping a group that does not exist errors.
	if err := c.Send(proto.Command{Action: "panel.ungroup", Group: "ghost"}); err != nil {
		t.Fatalf("send ungroup ghost: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("ungrouping a missing group should error, got %+v", msg)
	}
	// Re-group for the rest of the test.
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a, b}, Group: "api"}); err != nil {
		t.Fatalf("regroup: %v", err)
	}
	recv(t, c)

	// Remove just one member by id: a leaves the group, b stays.
	if err := c.Send(proto.Command{Action: "panel.ungroup", IDs: []string{a}}); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	got = recv(t, c)
	if g := panelByID(got.Panels, a).Group; g != "" {
		t.Fatalf("panel a should have left the group, got %q", g)
	}
	if g := panelByID(got.Panels, b).Group; g != "api" {
		t.Fatalf("panel b should remain in api, got %q", g)
	}
	// Removing an id that is not in any group errors.
	if err := c.Send(proto.Command{Action: "panel.ungroup", IDs: []string{a}}); err != nil {
		t.Fatalf("send remove ungrouped: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("removing an ungrouped panel should error, got %+v", msg)
	}
	// Put a back for the rest of the test.
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a}, Group: "api"}); err != nil {
		t.Fatalf("regroup a: %v", err)
	}
	recv(t, c)

	// Rename one panel's title.
	if err := c.Send(proto.Command{Action: "panel.rename", ID: a, Name: "worker"}); err != nil {
		t.Fatalf("rename panel: %v", err)
	}
	got = recv(t, c)
	if title := panelByID(got.Panels, a).Title; title != "worker" {
		t.Fatalf("panel a should be titled worker, got %q", title)
	}

	// Rename the group; every member follows.
	if err := c.Send(proto.Command{Action: "panel.rename", Group: "api", Name: "backend"}); err != nil {
		t.Fatalf("rename group: %v", err)
	}
	got = recv(t, c)
	for _, p := range got.Panels {
		if p.Group != "backend" {
			t.Fatalf("panel %s should follow the group rename, got %q", p.ID, p.Group)
		}
	}

	// Under the default strict policy, renaming one group onto another's name is
	// a conflict: the rename is rejected and the group keeps its own name.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create d: %v", err)
	}
	d := recv(t, c).Panels[2].ID
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{d}, Group: "infra"}); err != nil {
		t.Fatalf("group d: %v", err)
	}
	recv(t, c)
	if err := c.Send(proto.Command{Action: "panel.rename", Group: "infra", Name: "backend"}); err != nil {
		t.Fatalf("merge group: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("merging onto an existing group should be rejected, got %+v", msg)
	}
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if g := panelByID(recv(t, c).Panels, d).Group; g != "infra" {
		t.Fatalf("the rejected member should stay in infra, got %q", g)
	}

	// Error paths reply with an error and do not broadcast a snapshot.
	for _, bad := range []proto.Command{
		{Action: "panel.group", IDs: []string{a}, Group: ""},         // empty name
		{Action: "panel.group", IDs: nil, Group: "x"},                // no panels
		{Action: "panel.group", IDs: []string{"nope"}, Group: "x"},   // no match
		{Action: "panel.rename", ID: a, Group: "backend", Name: "z"}, // ambiguous target
		{Action: "panel.rename", Name: "z"},                          // no target
		{Action: "panel.rename", ID: "nope", Name: "z"},              // unknown panel
		{Action: "panel.rename", Group: "ghost", Name: "z"},          // unknown group
	} {
		if err := c.Send(bad); err != nil {
			t.Fatalf("send %v: %v", bad, err)
		}
		if msg := recv(t, c); msg.Type != "error" || msg.Error == "" {
			t.Fatalf("command %+v should error, got %+v", bad, msg)
		}
	}
}

// panelByID finds a panel in a snapshot by id, or returns the zero value.
func panelByID(panels []proto.Panel, id string) proto.Panel {
	for _, p := range panels {
		if p.ID == id {
			return p
		}
	}
	return proto.Panel{}
}

// TestNameConflictPolicy covers the uniqueness rule on panel titles and group
// names: rejected by default, bypassed for "add to existing group", and lifted
// entirely when the server is built to allow conflicts.
func TestNameConflictPolicy(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")

	// setup spins a server with the given options and returns a client with two
	// shell panels (ids a, b), past the welcome + snapshot handshake. A short
	// socket dir keeps the path under the macOS sun_path limit.
	setup := func(t *testing.T, opts ...server.Option) (*client.Client, string, string) {
		t.Helper()
		dir, err := os.MkdirTemp("", "bt")
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
		go func() { _ = server.New(ln, opts...).Serve() }()

		c, err := client.Dial(sock)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		recv(t, c) // welcome
		recv(t, c) // empty snapshot
		if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
			t.Fatalf("create a: %v", err)
		}
		a := recv(t, c).Panels[0].ID
		if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
			t.Fatalf("create b: %v", err)
		}
		b := recv(t, c).Panels[1].ID
		return c, a, b
	}

	t.Run("strict", func(t *testing.T) {
		c, a, b := setup(t)
		if err := c.Send(proto.Command{Action: "panel.rename", ID: a, Name: "dup"}); err != nil {
			t.Fatalf("rename a: %v", err)
		}
		recv(t, c)
		// Renaming b onto a's title is a conflict.
		if err := c.Send(proto.Command{Action: "panel.rename", ID: b, Name: "dup"}); err != nil {
			t.Fatalf("rename b: %v", err)
		}
		if msg := recv(t, c); msg.Type != "error" {
			t.Fatalf("a duplicate title should be rejected, got %+v", msg)
		}
		// A new group whose name collides with a panel title is a conflict too.
		if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{b}, Group: "dup"}); err != nil {
			t.Fatalf("group: %v", err)
		}
		if msg := recv(t, c); msg.Type != "error" {
			t.Fatalf("a group name colliding with a title should be rejected, got %+v", msg)
		}
	})

	t.Run("add-bypasses", func(t *testing.T) {
		c, a, b := setup(t)
		if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a}, Group: "work"}); err != nil {
			t.Fatalf("group a: %v", err)
		}
		recv(t, c)
		// Adding b to the existing "work" group reuses the name — allowed.
		if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{b}, Group: "work"}); err != nil {
			t.Fatalf("add b: %v", err)
		}
		got := recv(t, c)
		if got.Type == "error" {
			t.Fatalf("adding to an existing group should be allowed, got %+v", got)
		}
		if g := panelByID(got.Panels, b).Group; g != "work" {
			t.Fatalf("b should have joined work, got %q", g)
		}
	})

	t.Run("allow-conflict", func(t *testing.T) {
		c, a, b := setup(t, server.WithAllowNameConflict(true))
		if err := c.Send(proto.Command{Action: "panel.rename", ID: a, Name: "dup"}); err != nil {
			t.Fatalf("rename a: %v", err)
		}
		recv(t, c)
		if err := c.Send(proto.Command{Action: "panel.rename", ID: b, Name: "dup"}); err != nil {
			t.Fatalf("rename b: %v", err)
		}
		got := recv(t, c)
		if got.Type == "error" {
			t.Fatalf("with conflicts allowed a duplicate title should pass, got %+v", got)
		}
		if title := panelByID(got.Panels, b).Title; title != "dup" {
			t.Fatalf("b should be renamed to dup, got %q", title)
		}
	})
}

// startServer spins a unix-socket server with opts and returns a client advanced
// past the welcome + empty-snapshot handshake, with cleanups registered.
func startServer(t *testing.T, opts ...server.Option) *client.Client {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	dir, err := os.MkdirTemp("", "bt")
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
	go func() { _ = server.New(ln, opts...).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	recv(t, c) // welcome
	recv(t, c) // empty snapshot
	return c
}

// createShells spawns n shell panels and returns their ids, one snapshot per
// create.
func createShells(t *testing.T, c *client.Client, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids[i] = recv(t, c).Panels[i].ID
	}
	return ids
}

// TestCloseGroupInOneCommand checks panel.close accepts a batch of ids and
// broadcasts a single snapshot, so closing a whole work item is one command
// rather than one round-trip per member (gap #6).
func TestCloseGroupInOneCommand(t *testing.T) {
	c := startServer(t)
	ids := createShells(t, c, 3)

	// Group the first two, then close the group in one command.
	if err := c.Send(proto.Command{Action: "panel.group", IDs: ids[:2], Group: "work"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	recv(t, c)
	if err := c.Send(proto.Command{Action: "panel.close", IDs: ids[:2]}); err != nil {
		t.Fatalf("close group: %v", err)
	}
	got := recv(t, c)
	if got.Type != "panels" {
		t.Fatalf("a batch close should broadcast one panels snapshot, got %+v", got)
	}
	if len(got.Panels) != 1 || got.Panels[0].ID != ids[2] {
		t.Fatalf("only the third panel should remain, got %+v", got.Panels)
	}

	// A single id still closes one panel (back-compat).
	if err := c.Send(proto.Command{Action: "panel.close", ID: ids[2]}); err != nil {
		t.Fatalf("close single: %v", err)
	}
	if got := recv(t, c); len(got.Panels) != 0 {
		t.Fatalf("the last panel should be gone, got %+v", got.Panels)
	}

	// A batch of only-unknown ids errors and broadcasts nothing.
	if err := c.Send(proto.Command{Action: "panel.close", IDs: []string{"ghost1", "ghost2"}}); err != nil {
		t.Fatalf("close ghosts: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("closing only-unknown ids should error, got %+v", msg)
	}
}

// TestCreateAgentPanelInWorkdir checks an agent panel runs its profile command
// with args in the requested working directory, and that the snapshot carries the
// agent kind and a workdir-named title. A stand-in command (sh -c pwd) prints the
// directory it was launched in, standing in for a real agent CLI.
func TestCreateAgentPanelInWorkdir(t *testing.T) {
	c := startServer(t)
	dir := t.TempDir()

	if err := c.Send(proto.Command{
		Action: "panel.create", Kind: proto.KindAgent,
		Path: "/bin/sh", Args: []string{"-c", "pwd; sleep 30"}, Dir: dir,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	snap := recv(t, c)
	if len(snap.Panels) != 1 {
		t.Fatalf("expected one panel, got %+v", snap.Panels)
	}
	p := snap.Panels[0]
	leaf := filepath.Base(dir)
	if p.Kind != proto.KindAgent {
		t.Fatalf("panel should be an agent, got kind %q", p.Kind)
	}
	if !strings.Contains(p.Title, leaf) {
		t.Fatalf("agent title should name the workdir %q, got %q", leaf, p.Title)
	}

	// Attach and confirm the process actually ran in the workdir (its pwd output).
	if err := c.Send(proto.Command{Action: "panel.attach", ID: p.ID}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	deadline := time.After(15 * time.Second)
	for {
		select {
		case msg := <-c.Output:
			if msg.ID == p.ID && strings.Contains(string(msg.Data), leaf) {
				return
			}
		case <-deadline:
			t.Fatalf("agent output never showed the workdir leaf %q", leaf)
		}
	}
}

// TestCreateAgentPanelNeedsCommand checks an agent panel without a command errors.
func TestCreateAgentPanelNeedsCommand(t *testing.T) {
	c := startServer(t)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: proto.KindAgent}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("an agent with no command should error, got %+v", msg)
	}
}

// TestNamesAreTrimmed checks the server trims surrounding whitespace from group
// and rename names, so " api " and "api" can never become two distinct work
// items, and a whitespace-only name is rejected as empty (gap #9).
func TestNamesAreTrimmed(t *testing.T) {
	c := startServer(t)
	a := createShells(t, c, 1)[0]

	// Group under a padded name: it is stored trimmed.
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a}, Group: "  api  "}); err != nil {
		t.Fatalf("group: %v", err)
	}
	if g := panelByID(recv(t, c).Panels, a).Group; g != "api" {
		t.Fatalf("the group name should be trimmed to %q, got %q", "api", g)
	}

	// Group rename with padding follows through trimmed.
	if err := c.Send(proto.Command{Action: "panel.rename", Group: "api", Name: "  backend  "}); err != nil {
		t.Fatalf("rename group: %v", err)
	}
	if g := panelByID(recv(t, c).Panels, a).Group; g != "backend" {
		t.Fatalf("the renamed group should be trimmed to %q, got %q", "backend", g)
	}

	// Panel title rename trims too.
	if err := c.Send(proto.Command{Action: "panel.rename", ID: a, Name: "  worker  "}); err != nil {
		t.Fatalf("rename panel: %v", err)
	}
	if title := panelByID(recv(t, c).Panels, a).Title; title != "worker" {
		t.Fatalf("the title should be trimmed to %q, got %q", "worker", title)
	}

	// A whitespace-only name is empty after trimming and rejected.
	if err := c.Send(proto.Command{Action: "panel.rename", ID: a, Name: "   "}); err != nil {
		t.Fatalf("rename blank: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("a whitespace-only name should be rejected, got %+v", msg)
	}
}

func TestMultiAttach(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	// Two shells, both attached at once — the group-split case.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create a: %v", err)
	}
	a := recv(t, c).Panels[0].ID
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create b: %v", err)
	}
	b := recv(t, c).Panels[1].ID

	for _, id := range []string{a, b} {
		if err := c.Send(proto.Command{Action: "panel.attach", ID: id}); err != nil {
			t.Fatalf("attach %s: %v", id, err)
		}
	}

	// Drive both; their output must arrive tagged with the right id so a client
	// can demux into per-tile emulators.
	if err := c.Send(proto.Command{Action: "panel.input", ID: a, Data: []byte("echo AAA-marker\n")}); err != nil {
		t.Fatalf("input a: %v", err)
	}
	if err := c.Send(proto.Command{Action: "panel.input", ID: b, Data: []byte("echo BBB-marker\n")}); err != nil {
		t.Fatalf("input b: %v", err)
	}

	// Both echoes race on the one shared stream, so consume it once and tick off
	// each marker as its correctly-tagged output arrives, in whatever order that
	// is. Waiting for them sequentially would drain (and drop) panel b's marker
	// while still blocking on panel a's, then starve on output it had already
	// discarded — the ordering race that made this flaky under load. A marker must
	// still arrive tagged with its own panel id; a cross-tagged one is a demux bug.
	{
		want := map[string]string{a: "AAA-marker", b: "BBB-marker"}
		seen := map[string]bool{}
		deadline := time.After(30 * time.Second)
		for len(seen) < len(want) {
			select {
			case msg := <-c.Output:
				for id, marker := range want {
					if !strings.Contains(string(msg.Data), marker) {
						continue
					}
					if msg.ID != id {
						t.Fatalf("marker %q arrived tagged with %q, not %q", marker, msg.ID, id)
					}
					seen[id] = true
				}
			case <-deadline:
				t.Fatalf("never saw both markers (saw %d of %d)", len(seen), len(want))
			}
		}
	}

	// Detaching just one stops its stream while the other keeps flowing. Poke the
	// detached panel and the live one; we must see the live one but never the
	// detached one (a is detached before its echo runs, so it can't route here).
	if err := c.Send(proto.Command{Action: "panel.detach", ID: a}); err != nil {
		t.Fatalf("detach a: %v", err)
	}
	if err := c.Send(proto.Command{Action: "panel.input", ID: a, Data: []byte("echo A-GONE\n")}); err != nil {
		t.Fatalf("input a after detach: %v", err)
	}
	if err := c.Send(proto.Command{Action: "panel.input", ID: b, Data: []byte("echo STILL-HERE\n")}); err != nil {
		t.Fatalf("input b again: %v", err)
	}
	deadline := time.After(30 * time.Second)
	for done := false; !done; {
		select {
		case msg := <-c.Output:
			if msg.ID == a && strings.Contains(string(msg.Data), "A-GONE") {
				t.Fatal("detached panel a should not stream any more")
			}
			if msg.ID == b && strings.Contains(string(msg.Data), "STILL-HERE") {
				done = true // b is live and a stayed silent up to here
			}
		case <-deadline:
			t.Fatal("never saw the live panel's output after detaching a")
		}
	}

	// Detach-all (empty id) clears the rest.
	if err := c.Send(proto.Command{Action: "panel.detach"}); err != nil {
		t.Fatalf("detach all: %v", err)
	}
}

// orderIDs is the fleet's panel ids in broadcast (and thus fleet) order.
func orderIDs(panels []proto.Panel) []string {
	ids := make([]string, len(panels))
	for i, p := range panels {
		ids[i] = p.ID
	}
	return ids
}

// TestMovePanels reorders the fleet: a block is lifted out and reinserted at the
// given index, which drives both the dashboard's item order and a group's member
// order since every frontend renders from this one order.
func TestMovePanels(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	// Three panels, in creation order a, b, c.
	var ids []string
	for range 3 {
		if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
			t.Fatalf("create: %v", err)
		}
		ids = append(ids, recv(t, c).Panels[len(ids)].ID)
	}
	a, b, cID := ids[0], ids[1], ids[2]

	// Move a to the end: order becomes b, c, a.
	if err := c.Send(proto.Command{Action: "panel.move", IDs: []string{a}, Index: 2}); err != nil {
		t.Fatalf("move a: %v", err)
	}
	if got, want := orderIDs(recv(t, c).Panels), []string{b, cID, a}; !slices.Equal(got, want) {
		t.Fatalf("after moving a to the end: order %v, want %v", got, want)
	}

	// Move c to the front: order becomes c, b, a.
	if err := c.Send(proto.Command{Action: "panel.move", IDs: []string{cID}, Index: 0}); err != nil {
		t.Fatalf("move c: %v", err)
	}
	if got, want := orderIDs(recv(t, c).Panels), []string{cID, b, a}; !slices.Equal(got, want) {
		t.Fatalf("after moving c to the front: order %v, want %v", got, want)
	}

	// An out-of-range index clamps to the end rather than erroring.
	if err := c.Send(proto.Command{Action: "panel.move", IDs: []string{cID}, Index: 99}); err != nil {
		t.Fatalf("move c clamp: %v", err)
	}
	if got, want := orderIDs(recv(t, c).Panels), []string{b, a, cID}; !slices.Equal(got, want) {
		t.Fatalf("after clamped move: order %v, want %v", got, want)
	}

	// No ids is an error; an unknown id matches nothing and also errors.
	if err := c.Send(proto.Command{Action: "panel.move"}); err != nil {
		t.Fatalf("send empty move: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("move with no ids should error, got %+v", msg)
	}
	if err := c.Send(proto.Command{Action: "panel.move", IDs: []string{"ghost"}, Index: 0}); err != nil {
		t.Fatalf("send ghost move: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("move of an unknown id should error, got %+v", msg)
	}
}

// TestPinPanels confirms the server owns the Pinned flag: a pin command sets it
// on the named panels and broadcasts the change, unpin clears it, and an unknown
// id errors.
func TestPinPanels(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	var ids []string
	for range 2 {
		if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
			t.Fatalf("create: %v", err)
		}
		ids = append(ids, recv(t, c).Panels[len(ids)].ID)
	}
	a, b := ids[0], ids[1]

	// Pin a: only a comes back pinned.
	if err := c.Send(proto.Command{Action: "panel.pin", IDs: []string{a}}); err != nil {
		t.Fatalf("pin a: %v", err)
	}
	if got := pinnedOf(recv(t, c).Panels); !got[a] || got[b] {
		t.Fatalf("after pinning a: pinned=%v, want only %s", got, a)
	}

	// Unpin a: nothing pinned again.
	if err := c.Send(proto.Command{Action: "panel.unpin", IDs: []string{a}}); err != nil {
		t.Fatalf("unpin a: %v", err)
	}
	if got := pinnedOf(recv(t, c).Panels); got[a] {
		t.Fatalf("after unpinning a: pinned=%v, want none", got)
	}

	// An unknown id matches nothing and errors.
	if err := c.Send(proto.Command{Action: "panel.pin", IDs: []string{"ghost"}}); err != nil {
		t.Fatalf("pin ghost: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("pin of an unknown id should error, got %+v", msg)
	}
}

// TestReloadAppliesSettings confirms Reload swaps the name-conflict policy on a
// running server without restarting it: a rename rejected under the strict
// default succeeds once a reload allows conflicts.
func TestReloadAppliesSettings(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	srv := server.New(ln) // strict names by default
	go func() { _ = srv.Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	var ids []string
	for range 2 {
		if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
			t.Fatalf("create: %v", err)
		}
		ids = append(ids, recv(t, c).Panels[len(ids)].ID)
	}
	a, b := ids[0], ids[1]

	if err := c.Send(proto.Command{Action: "panel.rename", ID: a, Name: "dup"}); err != nil {
		t.Fatalf("rename a: %v", err)
	}
	recv(t, c)
	// Strict: renaming b onto a's title is rejected.
	if err := c.Send(proto.Command{Action: "panel.rename", ID: b, Name: "dup"}); err != nil {
		t.Fatalf("rename b strict: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("a duplicate title should be rejected before reload, got %+v", msg)
	}

	// Hot-reload to allow name conflicts; the same rename now goes through.
	srv.Reload(true, "", 0, "", "", "")
	if err := c.Send(proto.Command{Action: "panel.rename", ID: b, Name: "dup"}); err != nil {
		t.Fatalf("rename b after reload: %v", err)
	}
	msg := recv(t, c)
	if msg.Type != "panels" {
		t.Fatalf("after a reload allowing conflicts the rename should succeed, got %+v", msg)
	}
	if got := titleOf(msg.Panels, b); got != "dup" {
		t.Fatalf("panel b should be renamed to dup, got %q", got)
	}
}

// TestReloadCmdHook confirms a server.reload command fires the
// OnReload hook — the in-cockpit reload that shares the SIGHUP routine.
func TestReloadCmdHook(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	srv := server.New(ln)
	reloaded := make(chan struct{}, 1)
	srv.OnReload(func() { reloaded <- struct{}{} })
	go func() { _ = srv.Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	if err := c.Send(proto.Command{Action: "server.reload"}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	select {
	case <-reloaded:
	case <-time.After(10 * time.Second):
		t.Fatal("server.reload should fire the OnReload hook")
	}
}

// titleOf returns the title of the panel with id, or "" if absent.
func titleOf(panels []proto.Panel, id string) string {
	for _, p := range panels {
		if p.ID == id {
			return p.Title
		}
	}
	return ""
}

// TestSignalPanel confirms a panel.signal command reaches the panel's process: a
// SIGKILL ends it, the server marks it exited, and a bad signal or unknown id is
// rejected.
func TestSignalPanel(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	// An unknown signal name is rejected.
	if err := c.Send(proto.Command{Action: "panel.signal", IDs: []string{id}, Signal: "SIGBOGUS"}); err != nil {
		t.Fatalf("bad signal: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("an unknown signal should error, got %+v", msg)
	}
	// An unknown panel is rejected.
	if err := c.Send(proto.Command{Action: "panel.signal", IDs: []string{"ghost"}, Signal: "SIGTERM"}); err != nil {
		t.Fatalf("ghost signal: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("signalling an unknown id should error, got %+v", msg)
	}

	// SIGKILL ends the process; the server broadcasts it exited.
	if err := c.Send(proto.Command{Action: "panel.signal", IDs: []string{id}, Signal: "SIGKILL"}); err != nil {
		t.Fatalf("kill: %v", err)
	}
	deadline := time.After(15 * time.Second)
	exited := false
	for !exited {
		select {
		case msg := <-c.Events:
			if msg.Type != "panels" {
				continue
			}
			for _, p := range msg.Panels {
				if p.ID == id && p.State == "exited" {
					exited = true
				}
			}
		case <-deadline:
			t.Fatal("panel was not marked exited after a SIGKILL")
		}
	}

	// Signalling the now-exited panel is rejected — its process is gone, so it is
	// not a live target and the count would otherwise be a lie.
	if err := c.Send(proto.Command{Action: "panel.signal", IDs: []string{id}, Signal: "SIGTERM"}); err != nil {
		t.Fatalf("signal exited: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("signalling an exited panel should error, got %+v", msg)
	}
}

// pinnedOf maps panel id to its Pinned flag, for asserting a snapshot.
func pinnedOf(panels []proto.Panel) map[string]bool {
	out := make(map[string]bool, len(panels))
	for _, p := range panels {
		out[p.ID] = p.Pinned
	}
	return out
}

// TestShellPanelUsesDefaultDir confirms a shell panel with no directory runs in
// the configured default workdir, not the directory the daemon was launched in.
func TestShellPanelUsesDefaultDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "batonwd")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c := startServer(t, server.WithDefaultDir(dir))

	// /bin/pwd prints its working directory and exits, so the output reveals where
	// the panel actually ran.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: proto.KindShell, Path: "/bin/pwd"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	p := recv(t, c).Panels[0]
	if err := c.Send(proto.Command{Action: "panel.attach", ID: p.ID}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	leaf := filepath.Base(dir) // a symlinked temp prefix keeps the leaf intact
	deadline := time.After(15 * time.Second)
	for {
		select {
		case msg := <-c.Output:
			if msg.ID == p.ID && strings.Contains(string(msg.Data), leaf) {
				return
			}
		case <-deadline:
			t.Fatalf("shell output never showed the default workdir leaf %q", leaf)
		}
	}
}

// TestWelcomeCarriesServerVersion checks the server reports its build version in
// the welcome so a frontend can show the backend version.
func TestWelcomeCarriesServerVersion(t *testing.T) {
	c := startServer(t, server.WithVersion("9.9.9"))
	// startServer drained welcome+panels; re-hello to re-read the welcome.
	if err := c.Send(proto.Command{Action: "hello"}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	got := recv(t, c)
	if got.Type != "welcome" || got.ServerVer != "9.9.9" {
		t.Fatalf("welcome should carry the server version, got %+v", got)
	}
}
