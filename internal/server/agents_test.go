package server_test

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// TestConfigGetServesDetectedAgents proves the detected backends reach a cockpit
// on the same snapshot the config does — the reason detection lives here at all,
// rather than in the frontend where the PATH would be the wrong machine's.
func TestConfigGetServesDetectedAgents(t *testing.T) {
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	srv.SetAgents([]proto.AgentBackend{
		{Name: "claude", Command: "claude"},
		{Name: "codex", Command: "codex", Args: []string{"--full-auto"}},
	})
	go func() { _ = srv.Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	select {
	case msg := <-c.Config:
		if len(msg.Agents) != 2 || msg.Agents[0].Name != "claude" {
			t.Fatalf("agents = %+v, want the published backends", msg.Agents)
		}
		if got := msg.Agents[1].Args; len(got) != 1 || got[0] != "--full-auto" {
			t.Fatalf("a backend's arguments should survive the wire, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive the config snapshot")
	}
}

// TestPushConfigCarriesDetectedAgents covers the reload path: C-t R re-detects,
// and an already-attached cockpit has to see the new answer without reattaching.
func TestPushConfigCarriesDetectedAgents(t *testing.T) {
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	go func() { _ = srv.Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	select { // the attach-time snapshot, before anything was detected
	case msg := <-c.Config:
		if len(msg.Agents) != 0 {
			t.Fatalf("nothing was detected yet, got %+v", msg.Agents)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive the first config snapshot")
	}

	srv.SetAgents([]proto.AgentBackend{{Name: "aider", Command: "aider"}})
	srv.PushConfig()

	select {
	case msg := <-c.Config:
		if len(msg.Agents) != 1 || msg.Agents[0].Name != "aider" {
			t.Fatalf("the reload push should carry the fresh list, got %+v", msg.Agents)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the reload push never arrived")
	}
}
