package server_test

import (
	"sync"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// TestEventSinkFiresOnSpawn checks the exported host Spawn lands a panel and emits a
// panel.spawn event carrying the new id — the wiring the plugin's hooks ride.
func TestEventSinkFiresOnSpawn(t *testing.T) {
	ln, _, _ := listen(t)
	srv := server.New(ln)

	var mu sync.Mutex
	var names []string
	var lastID string
	srv.SetEventSink(func(name string, fields map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		names = append(names, name)
		if id, ok := fields["id"].(string); ok {
			lastID = id
		}
	})

	id, err := srv.Spawn("shell", "", nil, t.TempDir(), "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(names) == 0 || names[0] != "panel.spawn" {
		t.Fatalf("expected a panel.spawn event, got %v", names)
	}
	if lastID != id {
		t.Errorf("event id = %q, want the spawned id %q", lastID, id)
	}
}

// TestExportedHostMethods exercises the Spawn/Close round-trip the plugin relies on:
// a spawn appears in the fleet read, a close removes it.
func TestExportedHostMethods(t *testing.T) {
	ln, _, _ := listen(t)
	srv := server.New(ln)

	id, err := srv.Spawn("shell", "", nil, t.TempDir(), "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := srv.PanelInfos(); len(got) != 1 || got[0].ID != id {
		t.Fatalf("PanelInfos after spawn = %+v, want one panel %q", got, id)
	}
	if err := srv.Close([]string{id}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := srv.PanelInfos(); len(got) != 0 {
		t.Fatalf("PanelInfos after close = %+v, want empty", got)
	}
}

// TestNoticeReachesClient checks baton.notify's backing — Notify — broadcasts a
// notice message to an attached cockpit.
func TestNoticeReachesClient(t *testing.T) {
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)

	// dial drained the handshake; send the notice and wait for it.
	srv.Notify("heads up")

	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-c.Events:
			if msg.Type == "notice" {
				if msg.Notice != "heads up" {
					t.Fatalf("notice = %q, want 'heads up'", msg.Notice)
				}
				return
			}
		case <-deadline:
			t.Fatal("did not receive the notice")
		}
	}
}

// TestConfigGetServesMergedConfig checks the config.get handshake returns whatever
// the daemon published as the effective client config and plugin commands.
func TestConfigGetServesMergedConfig(t *testing.T) {
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	srv.SetClientConfig([]byte(`{"prefix":"ctrl+a"}`))
	srv.SetPluginCommands([]proto.PluginCommand{{Name: "hi", Desc: "say hi"}})
	go func() { _ = srv.Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	select {
	case msg := <-c.Config:
		if string(msg.Config) != `{"prefix":"ctrl+a"}` {
			t.Fatalf("config = %s, want the published blob", msg.Config)
		}
		if len(msg.Commands) != 1 || msg.Commands[0].Name != "hi" {
			t.Fatalf("commands = %+v, want one 'hi'", msg.Commands)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive the config snapshot")
	}
}
