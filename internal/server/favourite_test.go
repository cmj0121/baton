package server_test

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/state"
)

// favouriteOf maps panel id to its Favourite flag, for asserting a snapshot.
func favouriteOf(panels []proto.Panel) map[string]bool {
	out := make(map[string]bool, len(panels))
	for _, p := range panels {
		out[p.ID] = p.Favourite
	}
	return out
}

// groupFav returns the Favourite flag the snapshot carries for the named group.
func groupFav(snap proto.ServerMsg, group string) bool {
	for _, g := range snap.Groups {
		if g.Group == group {
			return g.Favourite
		}
	}
	return false
}

// TestFavouritePanels confirms the server owns the Favourite flag, entirely
// separate from Pinned: a panel.favourite sets it on the named panels and
// broadcasts the change, panel.unfavourite clears it, and an unknown id errors.
func TestFavouritePanels(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)

	var ids []string
	for range 2 {
		if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
			t.Fatalf("create: %v", err)
		}
		ids = append(ids, recv(t, c).Panels[len(ids)].ID)
	}
	a, b := ids[0], ids[1]

	// Favourite a: only a comes back favourited, and its Pinned stays clear.
	if err := c.Send(proto.Command{Action: "panel.favourite", ID: a}); err != nil {
		t.Fatalf("favourite a: %v", err)
	}
	snap := recv(t, c)
	if got := favouriteOf(snap.Panels); !got[a] || got[b] {
		t.Fatalf("after favouriting a: favourite=%v, want only %s", got, a)
	}
	if pinnedOf(snap.Panels)[a] {
		t.Fatalf("favourite must not touch the pinned flag")
	}

	// Unfavourite a: nothing favourited again.
	if err := c.Send(proto.Command{Action: "panel.unfavourite", ID: a}); err != nil {
		t.Fatalf("unfavourite a: %v", err)
	}
	if got := favouriteOf(recv(t, c).Panels); got[a] {
		t.Fatalf("after unfavouriting a: favourite=%v, want none", got)
	}

	// An unknown id matches nothing and errors.
	if err := c.Send(proto.Command{Action: "panel.favourite", IDs: []string{"ghost"}}); err != nil {
		t.Fatalf("favourite ghost: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("favourite of an unknown id should error, got %+v", msg)
	}
}

// TestGroupFavourite confirms group.favourite records the flag on the group and a
// fresh snapshot carries it in Groups, group.unfavourite clears it, and an empty
// group name is rejected.
func TestGroupFavourite(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)

	if err := c.Send(proto.Command{Action: "group.favourite", Group: "work"}); err != nil {
		t.Fatalf("group.favourite: %v", err)
	}
	recv(t, c) // broadcast from the mutation
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !groupFav(recv(t, c), "work") {
		t.Fatalf("group work should be favourited")
	}

	if err := c.Send(proto.Command{Action: "group.unfavourite", Group: "work"}); err != nil {
		t.Fatalf("group.unfavourite: %v", err)
	}
	recv(t, c)
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if groupFav(recv(t, c), "work") {
		t.Fatalf("group work should no longer be favourited")
	}

	if err := c.Send(proto.Command{Action: "group.favourite", Group: ""}); err != nil {
		t.Fatalf("group.favourite empty: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("an empty group name should error, got %+v", msg)
	}
}

// TestFavouritePersistsAndRestores confirms both favourite flags survive a save
// and reload: a favourited panel and a favourited group are written to the
// snapshot and come back on a fresh server's Restore.
func TestFavouritePersistsAndRestores(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	if err := c.Send(proto.Command{Action: "panel.favourite", ID: id}); err != nil {
		t.Fatalf("favourite panel: %v", err)
	}
	recv(t, c)
	if err := c.Send(proto.Command{Action: "group.favourite", Group: "work"}); err != nil {
		t.Fatalf("favourite group: %v", err)
	}
	recv(t, c)

	// Flush to disk and confirm the persisted shape carries both flags.
	srv.SaveNow()
	st, err := state.Load(stateF)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(st.Panels) != 1 || !st.Panels[0].Favourite {
		t.Fatalf("panel favourite not persisted: %+v", st.Panels)
	}
	gotGroup := false
	for _, g := range st.Groups {
		if g.Group == "work" && g.Favourite {
			gotGroup = true
		}
	}
	if !gotGroup {
		t.Fatalf("group favourite not persisted: %+v", st.Groups)
	}

	// A fresh server restoring the same file surfaces both flags on its snapshot.
	ln2, sock2, _ := listen(t)
	srv2 := server.New(ln2, server.WithStateFile(stateF))
	srv2.Restore()
	go func() { _ = srv2.Serve() }()

	c2 := dial(t, sock2)
	if err := c2.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	snap := recv(t, c2)
	if !favouriteOf(snap.Panels)[id] {
		t.Fatalf("panel favourite not restored: %+v", snap.Panels)
	}
	if !groupFav(snap, "work") {
		t.Fatalf("group favourite not restored: %+v", snap.Groups)
	}
}
