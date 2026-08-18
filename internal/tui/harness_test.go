package tui

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// recordingServer stands up a minimal in-memory server that answers the hello
// handshake and records every command the client sends. It lets a test drive the
// model and assert exactly what travelled over the socket — the resize on a zoom,
// the move on a reorder — without a real PTY or the full server. The returned
// channel yields each command (other than the hello/config.get handshake) in send order.
func recordingServer(t *testing.T) (*client.Client, <-chan proto.Command) {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "rec.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	cmds := make(chan proto.Command, 128)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Seed the handshake the client's readLoop expects.
		enc := json.NewEncoder(conn)
		_ = enc.Encode(proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion})
		_ = enc.Encode(proto.ServerMsg{Type: "panels"})

		dec := json.NewDecoder(conn)
		for {
			var cmd proto.Command
			if err := dec.Decode(&cmd); err != nil {
				return
			}
			if cmd.Action == "hello" || cmd.Action == "config.get" {
				continue // handshake, not a user action
			}
			select {
			case cmds <- cmd:
			default:
			}
		}
	}()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, cmds
}

// shortTempDir is a temp root with a SHORT name. macOS caps a unix socket path
// near 104 bytes and t.TempDir() embeds the test's own name, so a descriptively
// named test would otherwise fail to bind — a failure that says nothing at all
// about the behaviour under test.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// waitCmd pulls commands until one satisfies match, or fails on timeout.
func waitCmd(t *testing.T, cmds <-chan proto.Command, match func(proto.Command) bool) proto.Command {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case c := <-cmds:
			if match(c) {
				return c
			}
		case <-deadline:
			t.Fatal("timed out waiting for a matching command")
			return proto.Command{}
		}
	}
}

// cursorOnPanel points the dashboard cursor at a panel row by id, and cursorOnGroup
// at a group row by path.
//
// Tests address rows by IDENTITY rather than by index because the tree's row
// numbers move: expanding a work item, folding a level's quiet panels, or floating
// a favourite all shift everything below them. A hard-coded index encodes one
// particular shape of the projection, and every test that carried one had to be
// rewritten the first time the shape changed — which is the argument for not
// carrying one again.
func (m *model) cursorOnPanel(t *testing.T, id string) {
	t.Helper()
	for i, it := range m.dashItems() {
		if it.kind == itemPanel && it.panel.ID == id {
			m.cursor = i
			return
		}
	}
	t.Fatalf("no dashboard row for panel %q", id)
}

func (m *model) cursorOnGroup(t *testing.T, path string) {
	t.Helper()
	for i, it := range m.dashItems() {
		if it.kind == itemGroup && it.name == path {
			m.cursor = i
			return
		}
	}
	t.Fatalf("no dashboard row for group %q", path)
}

// topLevel is the depth-0 rows of the dashboard, named: a group by its path, a
// panel by its id, and the quiet fold as "fold". It is what a test asserting the
// SHAPE of the dashboard should read, so that a change to what a group contains
// does not rewrite an assertion about what sits at the top.
func (m model) topLevel() []string {
	var out []string
	for _, it := range m.dashItems() {
		if it.depth != 0 {
			continue
		}
		switch it.kind {
		case itemGroup:
			out = append(out, it.name)
		case itemFold:
			out = append(out, "fold")
		default:
			out = append(out, it.panel.ID)
		}
	}
	return out
}

// itemForPanel returns the dashboard row for a panel id, addressed by identity
// rather than by position for the reason cursorOnPanel is.
func itemForPanel(t *testing.T, m model, id string) dashItem {
	t.Helper()
	for _, it := range m.dashItems() {
		if it.kind == itemPanel && it.panel.ID == id {
			return it
		}
	}
	t.Fatalf("no dashboard row for panel %q", id)
	return dashItem{}
}

// rowAt renders one dashboard row at a width wide enough for every column, which
// is what a test asserting a row's CONTENT wants: the narrow cases are the
// business of the width-breakpoint tests, not of every assertion about what a row
// says.
const testRowWidth = 160

func (m model) rowOf(it dashItem) string { return m.treeRow(it, false, testRowWidth) }
func (m model) rowOfPanel(p panel.Panel) string {
	return m.treeRow(dashItem{kind: itemPanel, panel: p}, false, testRowWidth)
}
