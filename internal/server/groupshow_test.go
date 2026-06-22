package server_test

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/state"
)

// groupShown returns the Shown count the snapshot carries for the named group, or
// 0 if the group has no entry.
func groupShown(snap proto.ServerMsg, group string) int {
	for _, g := range snap.Groups {
		if g.Group == group {
			return g.Shown
		}
	}
	return 0
}

// TestGroupShowSetsCount checks that group.show records the visible count and that
// a fresh panel.list snapshot carries it in Groups.
func TestGroupShowSetsCount(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "group.show", Group: "work", Count: 3}); err != nil {
		t.Fatalf("group.show: %v", err)
	}
	recv(t, c) // broadcast snapshot from the mutation

	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	snap := recv(t, c)
	if got := groupShown(snap, "work"); got != 3 {
		t.Fatalf("expected Shown 3 for group work, got %d (%+v)", got, snap.Groups)
	}
}

// TestGroupShowClampsRange checks the count is clamped to [minVisible, maxVisible]
// = [1, 16] on both ends.
func TestGroupShowClampsRange(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)

	// Above the cap clamps to 16.
	if err := c.Send(proto.Command{Action: "group.show", Group: "big", Count: 99}); err != nil {
		t.Fatalf("group.show high: %v", err)
	}
	recv(t, c)
	// Below the floor clamps to 1.
	if err := c.Send(proto.Command{Action: "group.show", Group: "small", Count: -4}); err != nil {
		t.Fatalf("group.show low: %v", err)
	}
	recv(t, c)

	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	snap := recv(t, c)
	if got := groupShown(snap, "big"); got != 16 {
		t.Fatalf("over-range count should clamp to 16, got %d", got)
	}
	if got := groupShown(snap, "small"); got != 1 {
		t.Fatalf("under-range count should clamp to 1, got %d", got)
	}
}

// TestGroupShowEmptyGroupErrors checks an empty group name is rejected.
func TestGroupShowEmptyGroupErrors(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "group.show", Group: "  ", Count: 2}); err != nil {
		t.Fatalf("group.show: %v", err)
	}
	if got := recv(t, c); got.Type != "error" {
		t.Fatalf("expected an error for an empty group, got %+v", got)
	}
}

// TestGroupShowPersistRoundTrip checks the visible count survives a save and a new
// server's Restore, surfacing again on a snapshot's Groups.
func TestGroupShowPersistRoundTrip(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "group.show", Group: "work", Count: 5}); err != nil {
		t.Fatalf("group.show: %v", err)
	}
	recv(t, c)

	srv.SaveNow()
	st, err := state.Load(stateF)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	found := false
	for _, g := range st.Groups {
		if g.Group == "work" && g.Shown == 5 {
			found = true
		}
	}
	if !found {
		t.Fatalf("state file did not record the group count: %+v", st.Groups)
	}

	// A fresh server on the same file restores the count.
	ln2, sock2, _ := listen(t)
	srv2 := server.New(ln2, server.WithStateFile(stateF))
	srv2.Restore()
	go func() { _ = srv2.Serve() }()

	c2 := dial(t, sock2)
	if err := c2.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	snap := recv(t, c2)
	if got := groupShown(snap, "work"); got != 5 {
		t.Fatalf("restored count should be 5, got %d (%+v)", got, snap.Groups)
	}
}

// TestGroupShowRenameMovesCount checks renameGroup carries the visible count to the
// new name and drops the old key.
func TestGroupShowRenameMovesCount(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	// A member is needed: renameGroup errors if no panel sits under the old name.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{id}, Group: "old"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	recv(t, c)
	if err := c.Send(proto.Command{Action: "group.show", Group: "old", Count: 4}); err != nil {
		t.Fatalf("group.show: %v", err)
	}
	recv(t, c)

	if err := c.Send(proto.Command{Action: "panel.rename", Group: "old", Name: "new"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	recv(t, c)

	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	snap := recv(t, c)
	if got := groupShown(snap, "new"); got != 4 {
		t.Fatalf("count should follow the rename to new, got %d (%+v)", got, snap.Groups)
	}
	if got := groupShown(snap, "old"); got != 0 {
		t.Fatalf("old name should no longer carry a count, got %d", got)
	}
}

// TestGroupShowDissolveDropsCount checks that dissolving a whole named group via
// ungroup removes its visible count.
func TestGroupShowDissolveDropsCount(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{id}, Group: "doomed"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	recv(t, c)
	if err := c.Send(proto.Command{Action: "group.show", Group: "doomed", Count: 2}); err != nil {
		t.Fatalf("group.show: %v", err)
	}
	recv(t, c)

	if err := c.Send(proto.Command{Action: "panel.ungroup", Group: "doomed"}); err != nil {
		t.Fatalf("ungroup: %v", err)
	}
	recv(t, c)

	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	snap := recv(t, c)
	if got := groupShown(snap, "doomed"); got != 0 {
		t.Fatalf("dissolving the group should drop its count, got %d (%+v)", got, snap.Groups)
	}
}
