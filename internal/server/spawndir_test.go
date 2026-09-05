package server_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// fakeClaude writes an executable named "claude" and returns its path. The name
// is what matters: withStatusLine only resolves a status line for a command whose
// base name is Claude Code's, so a panel spawned under any other name never
// reaches the settings files at all.
func fakeClaude(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec cat\n"), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return p
}

// TestPanelCreateSurvivesAFifoInTheDirItNames drives the whole chain the way a
// peer does — a real socket, a real panel.create, a directory of the peer's
// choosing — against the shape that used to park the handler for good: a
// `.claude/settings.json` that is a FIFO.
//
// createPanel calls startPanel, which resolves the Claude Code status line out of
// the panel's working directory. A plain open of a FIFO with no writer never
// returns, so this command produced NO reply at all: the client timed out, the
// goroutine stayed in open(2) for the daemon's whole life, and a conductor spawn
// taken this way left its singleton reservation held so no conductor could be
// opened again.
//
// The assertion is that a reply — any reply — comes back. What the spawn then
// does is not this test's business; that it answers is.
func TestPanelCreateSurvivesAFifoInTheDirItNames(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, ".claude", "settings.json"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// A private CLAUDE_CONFIG_DIR so the developer's own settings never decide this.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	c := startServer(t, server.WithUsageLimits(nil, fakeClaude(t)))
	if err := c.Send(proto.Command{
		Action: "panel.create", Kind: proto.KindAgent, Path: fakeClaude(t), Dir: dir,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Not recv: a blocked daemon sends nothing, and the failure this test exists
	// for has to read as "the peer's directory wedged the spawn" rather than as a
	// generic timeout in whichever helper waited last.
	select {
	case msg, ok := <-c.Events:
		if !ok {
			t.Fatal("event channel closed instead of answering the spawn")
		}
		_ = msg
	case <-time.After(5 * time.Second):
		t.Fatal("panel.create never answered: the daemon is blocked on a FIFO in the directory the peer named")
	}
}

// TestPanelCreateStillReadsARealSettingsFile is the other half — the guard must
// not have cost the feature it protects. A status line configured in the peer's
// directory has to still be found and wrapped.
func TestPanelCreateStillReadsARealSettingsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"statusLine":{"type":"command","command":"echo hi"}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	c := startServer(t, server.WithUsageLimits(nil, fakeClaude(t)))
	if err := c.Send(proto.Command{
		Action: "panel.create", Kind: proto.KindAgent, Path: fakeClaude(t), Dir: dir,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg := recv(t, c); msg.Type == "error" {
		t.Fatalf("panel.create = error %q, want a spawn", msg.Error)
	}
}
