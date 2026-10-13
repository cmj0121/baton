package control_test

import (
	"encoding/json"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
)

// peerGroup spawns a shell panel filed under group and returns its id.
func peerGroup(t *testing.T, c *control.Client, group string) string {
	t.Helper()
	id, err := c.SpawnPanel("", nil, "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := c.Do(proto.Command{Action: "panel.group", Group: group, IDs: []string{id}}); err != nil {
		t.Fatalf("group: %v", err)
	}
	return id
}

func dialAs(t *testing.T, sock, self string) *control.Client {
	t.Helper()
	c, err := control.DialSocket(sock, "", self, "")
	if err != nil {
		t.Fatalf("dial as %q: %v", self, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestPeerClientRoundTrip takes the four peer methods through a real daemon:
// the operator's flag, the list, a send and the read that shows it arrived.
func TestPeerClientRoundTrip(t *testing.T) {
	sock := startServer(t)
	cockpit := dialAs(t, sock, "")
	me := peerGroup(t, cockpit, "work")
	mate := peerGroup(t, cockpit, "work")
	peerGroup(t, cockpit, "elsewhere")
	worker := dialAs(t, sock, me)

	// Off until the operator says otherwise, and the refusal is the daemon's.
	if _, err := worker.PeerList(); err == nil || !strings.Contains(err.Error(), `peer: peer interaction is off for group "work"`) {
		t.Fatalf("PeerList with peers off: want the daemon's refusal, got %v", err)
	}
	if _, err := worker.PeerTail(mate, 0); err == nil || !strings.HasPrefix(err.Error(), "peer: ") {
		t.Fatalf("PeerTail with peers off: want a refusal, got %v", err)
	}
	if note, err := worker.PeerSend(mate, "echo never"); err == nil || !strings.HasPrefix(err.Error(), "peer: ") || note != "" {
		t.Fatalf("PeerSend with peers off: want a refusal and no notice, got %q, %v", note, err)
	}

	if err := cockpit.GroupPeers("work", true); err != nil {
		t.Fatalf("GroupPeers on: %v", err)
	}
	peers, err := worker.PeerList()
	if err != nil {
		t.Fatalf("PeerList: %v", err)
	}
	var ids []string
	for _, p := range peers {
		ids = append(ids, p.ID)
	}
	// One group-mate. The fleet has three panels, and every one of them is in
	// the "panels" broadcast this same connection has been receiving.
	if !slices.Equal(ids, []string{mate}) {
		t.Fatalf("PeerList = %v, want [%s]: the caller's reachable group-mates and nobody else", ids, mate)
	}

	note, err := worker.PeerSend(mate, "echo peer-$((40+2))")
	if err != nil || !strings.HasPrefix(note, "sent 19 bytes to ") || !strings.HasSuffix(note, "("+mate+")") {
		t.Fatalf("PeerSend should return the daemon's notice, got %q, %v", note, err)
	}
	var tail string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !strings.Contains(tail, "peer-42"); time.Sleep(20 * time.Millisecond) {
		raw, err := worker.PeerTail(mate, 4096)
		if err != nil {
			t.Fatalf("PeerTail: %v", err)
		}
		tail = string(raw)
	}
	if !strings.Contains(tail, "peer-42") {
		t.Fatalf("PeerTail never showed the command's output; got %q", tail)
	}
	// The size asked for is the size answered.
	if raw, err := worker.PeerTail(mate, 5); err != nil || len(raw) != 5 {
		t.Fatalf("PeerTail(5) = %d bytes, %v; want 5", len(raw), err)
	}

	if err := cockpit.GroupPeers("work", false); err != nil {
		t.Fatalf("GroupPeers off: %v", err)
	}
	if _, err := worker.PeerList(); err == nil {
		t.Fatal("PeerList after the operator turned peers off should be refused")
	}

	// The daemon's own refusals of the flag reach the caller.
	if err := cockpit.GroupPeers("nobody-here", true); err == nil || !strings.Contains(err.Error(), "no panels in group") {
		t.Fatalf("enabling a memberless group: want the daemon's refusal, got %v", err)
	}
}

// TestPeerClientOnAClosedConnection: a dead connection is an error from each
// method, never an empty answer.
func TestPeerClientOnAClosedConnection(t *testing.T) {
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()

	if err := c.GroupPeers("g", true); err == nil {
		t.Error("GroupPeers on a closed client should error")
	}
	if _, err := c.PeerList(); err == nil {
		t.Error("PeerList on a closed client should error")
	}
	if _, err := c.PeerTail("1", 0); err == nil {
		t.Error("PeerTail on a closed client should error")
	}
	if _, err := c.PeerSend("1", "x"); err == nil {
		t.Error("PeerSend on a closed client should error")
	}
}

// TestPeerClientNeedsAnAnswer runs the client against a daemon that knows
// nothing of the peer verbs: it answers the barrier and nothing else, and keeps
// broadcasting the fleet. "No answer" must not come back as "no peers", and the
// fleet broadcast must not come back as the peer list.
func TestPeerClientNeedsAnAnswer(t *testing.T) {
	ln, err := net.Listen("unix", filepath.Join(shortDir(t), "s.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
		fleet := proto.ServerMsg{Type: "panels", Panels: []proto.Panel{{ID: "7"}, {ID: "8"}}}
		for {
			var cmd proto.Command
			if dec.Decode(&cmd) != nil {
				return
			}
			switch cmd.Action {
			case "hello":
				_ = enc.Encode(fleet)
			case "config.get":
				_ = enc.Encode(proto.ServerMsg{Type: "config"})
			default:
				// What an unrelated broadcast looks like from here: the fleet, a tail
				// of some other panel, nothing addressed to this command.
				_ = enc.Encode(fleet)
				_ = enc.Encode(proto.ServerMsg{Type: "tail", ID: "8", Data: []byte("someone else's")})
				_ = enc.Encode(proto.ServerMsg{Type: "notice", Notice: "a plugin's broadcast"})
			}
		}
	}()

	c, err := control.DialSocket(ln.Addr().String(), "", "7", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	if peers, err := c.PeerList(); err == nil || !strings.Contains(err.Error(), "did not answer peer.list") {
		t.Errorf("PeerList with no \"peers\" reply = %v, %v; want an error, never the fleet broadcast", peers, err)
	}
	if raw, err := c.PeerTail("9", 0); err == nil || !strings.Contains(err.Error(), "did not answer peer.tail") {
		t.Errorf("PeerTail(9) answered with another panel's tail: %q, %v", raw, err)
	}
	if note, err := c.PeerSend("9", "x"); err == nil || !strings.Contains(err.Error(), "did not answer peer.send") {
		t.Errorf("PeerSend answered with a notice that was not its own: %q, %v", note, err)
	}
}

// TestPeerSendReturnsItsOwnNotice: the daemon broadcasts notices to every
// connection for its own reasons — a plugin, the fleet memory — and one of them
// can land on either side of the reply. What PeerSend returns is the notice
// that names its target.
func TestPeerSendReturnsItsOwnNotice(t *testing.T) {
	ln, err := net.Listen("unix", filepath.Join(shortDir(t), "s.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
		for {
			var cmd proto.Command
			if dec.Decode(&cmd) != nil {
				return
			}
			switch cmd.Action {
			case "hello":
				_ = enc.Encode(proto.ServerMsg{Type: "panels"})
			case "config.get":
				_ = enc.Encode(proto.ServerMsg{Type: "config"})
			case "peer.send":
				_ = enc.Encode(proto.ServerMsg{Type: "notice", Notice: "broadcast before"})
				_ = enc.Encode(proto.ServerMsg{Type: "notice", ID: cmd.ID, Notice: "sent 1 bytes to mate (" + cmd.ID + ")"})
				_ = enc.Encode(proto.ServerMsg{Type: "notice", Notice: "broadcast after"})
			}
		}
	}()

	c, err := control.DialSocket(ln.Addr().String(), "", "7", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if note, err := c.PeerSend("9", "x"); err != nil || note != "sent 1 bytes to mate (9)" {
		t.Errorf("PeerSend = %q, %v; want its own notice, not a broadcast beside it", note, err)
	}
}
