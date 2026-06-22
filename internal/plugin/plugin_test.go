package plugin_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/plugin"
	"github.com/cmj0121/baton/internal/proto"
)

// spawnRec is one recorded baton.spawn the fake host saw.
type spawnRec struct {
	kind, command, dir, group string
	args                      []string
}

// fakeHost is a stand-in for *server.Server: it records what the baton.* calls
// drive, so a test can assert the Lua surface lands on the host.
type fakeHost struct {
	mu       sync.Mutex
	spawned  []spawnRec
	closed   [][]string
	signals  []string
	notified []string
	panels   []proto.Panel
}

func (h *fakeHost) Spawn(kind, command string, args []string, dir, group string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spawned = append(h.spawned, spawnRec{kind, command, dir, group, args})
	return "p1", nil
}
func (h *fakeHost) Close(ids []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = append(h.closed, ids)
	return nil
}
func (h *fakeHost) Respawn(string) error                { return nil }
func (h *fakeHost) Purge() int                          { return 0 }
func (h *fakeHost) Group([]string, string) error        { return nil }
func (h *fakeHost) Ungroup([]string, string) error      { return nil }
func (h *fakeHost) Rename(string, string, string) error { return nil }
func (h *fakeHost) Move([]string, int) error            { return nil }
func (h *fakeHost) SetPinned([]string, bool) error      { return nil }
func (h *fakeHost) GroupShow(string, int) error         { return nil }
func (h *fakeHost) GroupInfos() []proto.GroupView       { return nil }
func (h *fakeHost) Signal(ids []string, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.signals = append(h.signals, name)
	return nil
}
func (h *fakeHost) Notify(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notified = append(h.notified, msg)
}
func (h *fakeHost) PanelInfos() []proto.Panel {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.panels
}

func (h *fakeHost) notifies() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notified...)
}

// writeLua writes src to a temp .lua file and returns its path.
func writeLua(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plug-in.lua")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write lua: %v", err)
	}
	return path
}

// waitFor polls cond up to a second, so a test can wait on the async event worker
// without sleeping a fixed time.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within the deadline")
}

// TestLoadRegistersEverything runs a file that exercises every pillar and checks
// the merged config, the command list, the output-gate, and a load-time fleet call.
func TestLoadRegistersEverything(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `
		baton.config{ prefix = "ctrl+a", allow_name_conflict = true, replay_kb = 64, default_agent = "aider" }
		baton.agent{ name = "aider", command = "aider", args = { "--yes" } }
		baton.bind("D", "diff")
		baton.command{ name = "hi", desc = "say hi", run = function() baton.notify("hi") end }
		baton.on("panel.attention", function(pan) baton.notify("attn:"..pan.title) end)
		baton.on("panel.output", function(pan) end)
		baton.spawn{ kind = "agent", command = "claude", args = { "-x" }, dir = "/tmp" }
	`)

	res, err := p.Load(path, config.Config{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if res.Config.Prefix != "ctrl+a" {
		t.Errorf("prefix = %q, want ctrl+a", res.Config.Prefix)
	}
	if res.Config.Settings.AllowNameConflict == nil || !*res.Config.Settings.AllowNameConflict {
		t.Errorf("allow_name_conflict not applied: %+v", res.Config.Settings.AllowNameConflict)
	}
	if res.Config.Panel.ReplayKB != 64 {
		t.Errorf("replay_kb = %d, want 64", res.Config.Panel.ReplayKB)
	}
	if a, ok := res.Config.Panel.Agents["aider"]; !ok || a.Command != "aider" || len(a.Args) != 1 {
		t.Errorf("agent aider not registered: %+v", res.Config.Panel.Agents)
	}
	if res.Config.Keys["diff"] != "D" {
		t.Errorf("bind diff = %q, want D", res.Config.Keys["diff"])
	}
	if len(res.Commands) != 1 || res.Commands[0].Name != "hi" || res.Commands[0].Desc != "say hi" {
		t.Errorf("commands = %+v, want one 'hi'", res.Commands)
	}
	if !res.WantOutput {
		t.Error("WantOutput should be true when a panel.output handler is registered")
	}

	h.mu.Lock()
	spawned := h.spawned
	h.mu.Unlock()
	if len(spawned) != 1 || spawned[0].command != "claude" || spawned[0].dir != "/tmp" || len(spawned[0].args) != 1 {
		t.Errorf("load-time spawn not delivered to host: %+v", spawned)
	}
}

// TestHookFires checks a dispatched event reaches its Lua handler, which calls back
// into the host with the event payload.
func TestHookFires(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `baton.on("panel.attention", function(pan) baton.notify("attn:"..pan.title) end)`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}

	p.Dispatch("panel.attention", map[string]any{"title": "claude·api"})
	waitFor(t, func() bool { return len(h.notifies()) == 1 })
	if got := h.notifies()[0]; got != "attn:claude·api" {
		t.Errorf("hook notified %q, want attn:claude·api", got)
	}
}

// TestRunCommand runs a registered command and rejects an unknown one.
func TestRunCommand(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `baton.command{ name = "go", run = function() baton.notify("went") end }`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}

	if err := p.RunCommand("go"); err != nil {
		t.Fatalf("run go: %v", err)
	}
	if n := h.notifies(); len(n) != 1 || n[0] != "went" {
		t.Errorf("command did not run: %+v", n)
	}
	if err := p.RunCommand("missing"); err == nil {
		t.Error("running an unknown command should error")
	}
}

// TestReloadIsFresh checks a second load rebuilds the world: the prior command and
// hooks are gone, replaced by the new file's.
func TestReloadIsFresh(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	first := writeLua(t, `baton.command{ name = "old", run = function() end }`)
	if _, err := p.Load(first, config.Config{}); err != nil {
		t.Fatalf("load first: %v", err)
	}
	second := writeLua(t, `baton.command{ name = "new", run = function() end }`)
	res, err := p.Load(second, config.Config{})
	if err != nil {
		t.Fatalf("load second: %v", err)
	}
	if len(res.Commands) != 1 || res.Commands[0].Name != "new" {
		t.Errorf("reload should leave only 'new', got %+v", res.Commands)
	}
	if err := p.RunCommand("old"); err == nil {
		t.Error("the old command should be gone after a reload")
	}
}

// TestMissingFileIsCleanNoop checks a missing plugin returns no error and keeps the
// YAML base config untouched.
func TestMissingFileIsCleanNoop(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	base := config.Config{Prefix: "ctrl+t"}
	res, err := p.Load(filepath.Join(t.TempDir(), "nope.lua"), base)
	if err != nil {
		t.Fatalf("missing file should be a clean no-op, got %v", err)
	}
	if res.Config.Prefix != "ctrl+t" {
		t.Errorf("base config should survive, prefix = %q", res.Config.Prefix)
	}
	if len(res.Commands) != 0 || res.WantOutput {
		t.Errorf("missing file should register nothing: %+v want=%v", res.Commands, res.WantOutput)
	}
}

// TestLoadErrorIsNonFatal checks a Lua syntax error returns an error but still yields
// a usable result (with the base config), never panicking.
func TestLoadErrorIsNonFatal(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `this is not lua )(`)
	res, err := p.Load(path, config.Config{Prefix: "ctrl+t"})
	if err == nil {
		t.Fatal("a broken plugin should surface an error")
	}
	if res.Config.Prefix != "ctrl+t" {
		t.Errorf("base config should still come back on error, prefix = %q", res.Config.Prefix)
	}
}
