package server_test

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// peerAnswer sends one peer verb and returns its reply. It reads by TYPE and
// never by position: a peer connection is as chatty as any other, and a flag
// flipped from the cockpit puts a fleet broadcast on its queue ahead of the
// answer to its own command.
func peerAnswer(t *testing.T, c *client.Client, cmd proto.Command) proto.ServerMsg {
	t.Helper()
	if err := c.Send(cmd); err != nil {
		t.Fatalf("%s: %v", cmd.Action, err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case msg, ok := <-c.Events:
			if !ok {
				t.Fatalf("%s: the connection closed", cmd.Action)
			}
			switch msg.Type {
			case "error", "peers", "tail", "notice":
				return msg
			}
		case <-deadline:
			t.Fatalf("%s: timed out waiting for the reply", cmd.Action)
		}
	}
}

// peerTailUntil polls peer.tail until the target's output holds want, and
// returns that output. The bytes come from the target's own PTY, so this is
// what actually reached it rather than what the server meant to write.
func peerTailUntil(t *testing.T, c *client.Client, target, want string) string {
	t.Helper()
	var last string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		msg := peerAnswer(t, c, proto.Command{Action: "peer.tail", ID: target, Count: 4096})
		if msg.Type != "tail" || msg.ID != target {
			t.Fatalf("peer.tail %s should answer its tail, got %+v", target, msg)
		}
		if last = string(msg.Data); strings.Contains(last, want) {
			return last
		}
	}
	t.Fatalf("panel %s never showed %q; its tail is %q", target, want, last)
	return ""
}

// TestPeerVerbsOverTheSocket runs one conversation end to end against real
// panels: a worker lists its group-mates, types into an agent and into a shell,
// reads both back, and is refused the moment the operator turns the flag off.
func TestPeerVerbsOverTheSocket(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))

	var mu sync.Mutex
	var sends []map[string]any
	srv.SetEventSink(func(name string, fields map[string]any) {
		if name != "peer.send" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		sends = append(sends, fields)
	})
	serve(t, srv)

	cockpit := dial(t, sock)
	me := groupedPanel(t, cockpit, "agent", "work")
	agent := groupedPanel(t, cockpit, "agent", "work")
	shell := groupedPanel(t, cockpit, "shell", "work")
	groupedPanel(t, cockpit, "agent", "elsewhere")
	myTitle := panelByID(recvList(t, cockpit), me).Title
	if myTitle == "" {
		t.Fatal("setup: the sending panel needs a title for the prefix to carry")
	}

	worker := dialAs(t, sock, me, "")
	wantRefused := func(when, want string) {
		t.Helper()
		for _, cmd := range []proto.Command{
			{Action: "peer.list"},
			{Action: "peer.tail", ID: agent},
			{Action: "peer.send", ID: agent, Data: []byte("refused-text")},
		} {
			msg := peerAnswer(t, worker, cmd)
			if msg.Type != "error" || !strings.HasPrefix(msg.Error, "peer:") || !strings.Contains(msg.Error, want) {
				t.Fatalf("%s %s: want a peer refusal naming %q, got %+v", cmd.Action, when, want, msg)
			}
		}
	}

	// Off by default: being in a group is not the permission.
	wantRefused("before the flag is on", `off for group "work"`)

	setPeers(t, cockpit, "work", true)
	list := peerAnswer(t, worker, proto.Command{Action: "peer.list"})
	var ids []string
	for _, p := range list.Panels {
		ids = append(ids, p.ID)
	}
	if list.Type != "peers" || !slices.Equal(ids, []string{agent, shell}) {
		t.Fatalf("peer.list = %q %v, want \"peers\" %v", list.Type, ids, []string{agent, shell})
	}

	// An agent target is told who is typing.
	if msg := peerAnswer(t, worker, proto.Command{Action: "peer.send", ID: agent, Data: []byte("hello-peer")}); msg.Type != "notice" || !strings.Contains(msg.Notice, agent) {
		t.Fatalf("peer.send to an agent should answer a notice naming it, got %+v", msg)
	}
	peerTailUntil(t, worker, agent, "[from "+myTitle+" ("+me+")] hello-peer")

	// A shell target gets the bytes as sent, submitted: the command RUNS, which a
	// prefix in front of it would have prevented.
	if msg := peerAnswer(t, worker, proto.Command{Action: "peer.send", ID: shell, Data: []byte("echo peer-$((40+2))")}); msg.Type != "notice" {
		t.Fatalf("peer.send to a shell should answer a notice, got %+v", msg)
	}
	if out := peerTailUntil(t, worker, shell, "peer-42"); strings.Contains(out, "[from") {
		t.Fatalf("a shell target must get the bytes verbatim, its terminal shows %q", out)
	}

	// R6: the operator turns the flag off mid-conversation, and the next call of
	// each verb is refused.
	setPeers(t, cockpit, "work", false)
	wantRefused("after the flag is turned off", `off for group "work"`)
	if out := string(peerAnswer(t, cockpit, proto.Command{Action: "panel.tail", ID: agent, Count: 4096}).Data); strings.Contains(out, "refused-text") {
		t.Fatalf("a refused send must not reach the target, its terminal shows %q", out)
	}

	// The conductor fence covers all three, flag on or not: peer.tail must not be
	// the way round the panel.tail fence.
	setPeers(t, cockpit, "work", true)
	conductor := dialAs(t, sock, me, "conductor")
	for _, cmd := range []proto.Command{
		{Action: "peer.list"},
		{Action: "peer.tail", ID: agent},
		{Action: "peer.send", ID: agent, Data: []byte("refused-text")},
	} {
		msg := peerAnswer(t, conductor, cmd)
		if msg.Type != "error" || !strings.HasPrefix(msg.Error, "peer:") || !strings.Contains(msg.Error, "conductor role") {
			t.Fatalf("a conductor connection must be refused %s, got %+v", cmd.Action, msg)
		}
	}

	// Exactly the two admitted sends were announced, each with its real size.
	mu.Lock()
	defer mu.Unlock()
	if len(sends) != 2 {
		t.Fatalf("want one peer.send event per admitted send, got %+v", sends)
	}
	for i, want := range []struct {
		to    string
		bytes int
	}{{agent, len("hello-peer")}, {shell, len("echo peer-$((40+2))")}} {
		f := sends[i]
		if f["from"] != me || f["to"] != want.to || f["group"] != "work" || f["bytes"] != want.bytes {
			t.Fatalf("peer.send event %d = %+v, want from %s to %s in work, %d bytes", i, f, me, want.to, want.bytes)
		}
	}
}
