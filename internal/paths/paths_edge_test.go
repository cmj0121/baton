package paths

import (
	"strings"
	"testing"
)

func TestStateFilePairsWithSocket(t *testing.T) {
	cases := map[string]string{
		"/run/baton/baton-42.sock": "/run/baton/baton-42.state.json",
		"/tmp/x.sock":              "/tmp/x.state.json",
		"/tmp/nosuffix":            "/tmp/nosuffix.state.json",
	}
	for sock, want := range cases {
		if got := StateFile(sock); got != want {
			t.Errorf("StateFile(%q) = %q, want %q", sock, got, want)
		}
	}
}

func TestPluginFileUsesEnvOverride(t *testing.T) {
	t.Setenv("BATON_PLUGIN", "/custom/plug.lua")
	if got := PluginFile(); got != "/custom/plug.lua" {
		t.Fatalf("PluginFile() = %q, want the BATON_PLUGIN override", got)
	}
}

func TestPluginFileDefault(t *testing.T) {
	t.Setenv("BATON_PLUGIN", "")
	t.Setenv("HOME", "/home/tester")
	if got := PluginFile(); got != "/home/tester/.baton/plug-in.lua" {
		t.Fatalf("PluginFile() = %q, want $HOME/.baton/plug-in.lua", got)
	}
}

// TestHomeUnsetYieldsRelativePaths pins the CURRENT behavior when HOME is empty:
// filepath.Join drops the empty element, so the helpers produce relative paths
// (".baton/..."). This documents existing behavior; it is not an endorsement.
func TestHomeUnsetYieldsRelativePaths(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("BATON_PLUGIN", "")

	if got := LogFile(); got != ".baton/baton.log" {
		t.Errorf("LogFile() with HOME unset = %q, want %q", got, ".baton/baton.log")
	}
	if got := ConfigFile(); got != ".baton/config" {
		t.Errorf("ConfigFile() with HOME unset = %q, want %q", got, ".baton/config")
	}
	if got := PluginFile(); got != ".baton/plug-in.lua" {
		t.Errorf("PluginFile() with HOME unset = %q, want %q", got, ".baton/plug-in.lua")
	}
}

// TestSocketHomeUnsetXDGUnset covers Socket's fallback chain when both
// BATON_SOCK and XDG_RUNTIME_DIR are empty and HOME is empty too: the runtime
// dir collapses to a relative ".baton" and the socket name still carries the
// session id and suffix.
func TestSocketHomeUnsetXDGUnset(t *testing.T) {
	t.Setenv("BATON_SOCK", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", "")
	got := Socket()
	if !strings.HasPrefix(got, ".baton/baton-") || !strings.HasSuffix(got, ".sock") {
		t.Fatalf("Socket() = %q, want it under relative .baton with a -<id>.sock name", got)
	}
}
