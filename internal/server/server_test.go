package server_test

import (
	"net"
	"path/filepath"
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
	case <-time.After(2 * time.Second):
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

	deadline := time.After(3 * time.Second)
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
	case <-time.After(2 * time.Second):
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
		deadline := time.After(3 * time.Second)
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

	deadline := time.After(3 * time.Second)
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

	// Merge semantics: a third panel in its own group, renamed onto "backend",
	// joins the existing members — group identity is just the name.
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
	got = recv(t, c)
	for _, p := range got.Panels {
		if p.Group != "backend" {
			t.Fatalf("after merge every panel should be in backend, got %s=%q", p.ID, p.Group)
		}
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

	waitFor := func(id, marker string) {
		deadline := time.After(3 * time.Second)
		for {
			select {
			case msg := <-c.Output:
				if msg.ID == id && strings.Contains(string(msg.Data), marker) {
					return
				}
				if msg.ID != id && strings.Contains(string(msg.Data), marker) {
					t.Fatalf("marker %q arrived tagged with %q, not %q", marker, msg.ID, id)
				}
			case <-deadline:
				t.Fatalf("never saw %q for panel %s", marker, id)
			}
		}
	}
	waitFor(a, "AAA-marker")
	waitFor(b, "BBB-marker")

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
	deadline := time.After(3 * time.Second)
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
