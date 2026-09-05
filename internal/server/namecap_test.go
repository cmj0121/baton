package server_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// legitimateName is the longest name these tests claim anybody types, written as
// a figure rather than as maxNameRunes-minus-something so that narrowing the cap
// to where it bites is caught. Sixty-four runes is already wider than the card a
// name is drawn in.
const legitimateName = 64

// firstPanel creates one shell panel and returns its id.
func firstPanel(t *testing.T, c interface{ Send(proto.Command) error }, recvNext func() proto.ServerMsg) string {
	t.Helper()
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	snap := recvNext()
	if snap.Type != "panels" || len(snap.Panels) == 0 {
		t.Fatalf("expected a panels snapshot with one panel, got %+v", snap)
	}
	return snap.Panels[0].ID
}

// TestOversizedNameIsRefused drives a huge panel title at a live daemon and
// checks it is refused rather than kept.
//
// A name is not paid once. It is kept on the panel, written into every fleet
// snapshot, and encoded once per attached client on every fleet change: unbounded,
// one rename carrying a 900 KiB title made EVERY subsequent panels broadcast
// 900 KiB, to every cockpit, for as long as the panel lived. The frame cap bounds
// what one command allocates; this bounds what the daemon then keeps repeating.
func TestOversizedNameIsRefused(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, _ := listen(t)
	serve(t, server.New(ln))

	c := dial(t, sock)
	id := firstPanel(t, c, func() proto.ServerMsg { return recv(t, c) })

	huge := strings.Repeat("T", 512<<10)
	if err := c.Send(proto.Command{Action: "panel.rename", ID: id, Name: huge}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	msg := recv(t, c)
	if msg.Type != "error" {
		t.Fatalf("a 512 KiB title should be refused, got %s", msg.Type)
	}
	if !strings.Contains(msg.Error, "limit") {
		t.Errorf("the refusal should say why, got %q", msg.Error)
	}

	// And nothing was kept: the next snapshot is the size a snapshot should be.
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	snap := recv(t, c)
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 1<<14 {
		t.Fatalf("the fleet snapshot is %d bytes; the title was kept after all", len(b))
	}
}

// TestOversizedGroupNameIsRefused: the same cap covers a work-item name, which is
// also a path — so it is the only bound on how deeply a peer can nest one.
func TestOversizedGroupNameIsRefused(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, _ := listen(t)
	serve(t, server.New(ln))

	c := dial(t, sock)
	id := firstPanel(t, c, func() proto.ServerMsg { return recv(t, c) })

	deep := strings.TrimSuffix(strings.Repeat("a/", 5000), "/")
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{id}, Group: deep}); err != nil {
		t.Fatalf("group: %v", err)
	}
	if msg := recv(t, c); msg.Type != "error" {
		t.Fatalf("a 5000-deep group path should be refused, got %s", msg.Type)
	}
}

// TestOrdinaryNamesStillLand is the other half: the names people actually type
// have to go through, panel and group alike.
func TestOrdinaryNamesStillLand(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, _ := listen(t)
	serve(t, server.New(ln))

	c := dial(t, sock)
	id := firstPanel(t, c, func() proto.ServerMsg { return recv(t, c) })

	title := strings.Repeat("n", legitimateName)
	if err := c.Send(proto.Command{Action: "panel.rename", ID: id, Name: title}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	snap := recv(t, c)
	if snap.Type != "panels" {
		t.Fatalf("an ordinary rename was refused: %+v", snap)
	}
	if snap.Panels[0].Title != title {
		t.Fatalf("title = %q, want the name as typed", snap.Panels[0].Title)
	}

	group := strings.Repeat("g", legitimateName/2) + "/" + strings.Repeat("h", legitimateName/2-1)
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{id}, Group: group}); err != nil {
		t.Fatalf("group: %v", err)
	}
	snap = recv(t, c)
	if snap.Type != "panels" || snap.Panels[0].Group != group {
		t.Fatalf("an ordinary group name was refused: %+v", snap)
	}
}
