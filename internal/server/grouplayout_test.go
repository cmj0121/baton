package server_test

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// groupLayoutOf returns the Layout name the snapshot carries for the named group,
// or "" if the group has no entry.
func groupLayoutOf(snap proto.ServerMsg, group string) string {
	for _, g := range snap.Groups {
		if g.Group == group {
			return g.Layout
		}
	}
	return ""
}

// TestGroupLayoutSetsName checks group.layout records the chosen layout and that a
// fresh snapshot carries it in Groups, alongside the visible count on one row.
func TestGroupLayoutSetsName(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "group.layout", Group: "work", Layout: "main-vertical"}); err != nil {
		t.Fatalf("group.layout: %v", err)
	}
	recv(t, c)
	if err := c.Send(proto.Command{Action: "group.show", Group: "work", Count: 4}); err != nil {
		t.Fatalf("group.show: %v", err)
	}
	recv(t, c)

	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	snap := recv(t, c)
	if got := groupLayoutOf(snap, "work"); got != "main-vertical" {
		t.Fatalf("expected layout main-vertical, got %q (%+v)", got, snap.Groups)
	}
	if got := groupShown(snap, "work"); got != 4 {
		t.Fatalf("expected the count to ride the same row, got %d", got)
	}
}

// TestGroupLayoutClears checks an empty layout clears the override back to default.
func TestGroupLayoutClears(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "group.layout", Group: "g", Layout: "stack"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	recv(t, c)
	if err := c.Send(proto.Command{Action: "group.layout", Group: "g", Layout: ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	recv(t, c)

	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	snap := recv(t, c)
	if got := groupLayoutOf(snap, "g"); got != "" {
		t.Fatalf("expected the layout cleared, got %q", got)
	}
}

// TestGroupLayoutNeedsGroup checks an empty group name is an error.
func TestGroupLayoutNeedsGroup(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "group.layout", Group: "", Layout: "stack"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("expected an error reply for a missing group, got %q", msg.Type)
	}
}
