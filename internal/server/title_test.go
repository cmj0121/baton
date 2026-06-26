package server_test

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// panelTitle returns the title the snapshot carries for the panel with id, or "".
func panelTitle(snap proto.ServerMsg, id string) string {
	for _, p := range snap.Panels {
		if p.ID == id {
			return p.Title
		}
	}
	return ""
}

// TestSetPanelTitleOverridesInSnapshot: a display title set by SetPanelTitle wins
// on the snapshot, and SetTitleHook(false) clears it back to the base title.
func TestSetPanelTitleOverridesInSnapshot(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	snap := recvUntil(t, c, "panels") // broadcast after create
	var id, base string
	for _, p := range snap.Panels {
		id, base = p.ID, p.Title
	}
	if id == "" {
		t.Fatal("no panel created")
	}

	// An override from a panel.title hook wins on the wire; the SetPanelTitle
	// broadcast carries it.
	srv.SetPanelTitle(id, "★ "+base)
	if got := panelTitle(recvUntil(t, c, "panels"), id); got != "★ "+base {
		t.Fatalf("override title = %q, want %q", got, "★ "+base)
	}

	// Removing the hook (SetTitleHook false) clears every override back to the base.
	srv.SetTitleHook(false)
	if got := panelTitle(recvUntil(t, c, "panels"), id); got != base {
		t.Fatalf("after clearing the hook, title = %q, want base %q", got, base)
	}
}
