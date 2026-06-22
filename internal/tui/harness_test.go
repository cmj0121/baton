package tui

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
)

// recordingServer stands up a minimal in-memory server that answers the hello
// handshake and records every command the client sends. It lets a test drive the
// model and assert exactly what travelled over the socket — the resize on a zoom,
// the move on a reorder — without a real PTY or the full server. The returned
// channel yields each command (other than the hello/config.get handshake) in send order.
func recordingServer(t *testing.T) (*client.Client, <-chan proto.Command) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "rec.sock")
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
