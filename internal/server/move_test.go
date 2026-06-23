package server_test

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// TestMovePanelsErrors covers the two reorder rejections: an empty id list, and a
// list that matches no live panel. Both must come back to the client as an error
// rather than silently reordering nothing.
func TestMovePanelsErrors(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	// Seed one panel so the fleet is non-empty but the bogus id still won't match.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = recv(t, c) // the create snapshot

	cases := []struct {
		name string
		cmd  proto.Command
	}{
		{"empty ids", proto.Command{Action: "panel.move", Index: 0}},
		{"no match", proto.Command{Action: "panel.move", IDs: []string{"nope"}, Index: 0}},
	}
	for _, tc := range cases {
		if err := c.Send(tc.cmd); err != nil {
			t.Fatalf("%s: send: %v", tc.name, err)
		}
		if got := recv(t, c); got.Type != "error" {
			t.Fatalf("%s: expected an error, got %+v", tc.name, got)
		}
	}
}
