package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/panellog"
)

// TestLogPolicyProjection checks the config → daemon projection: the paths are
// expanded once, here, so the daemon never resolves a hand-written relative path
// against whatever directory it happened to be launched from; a profile with no
// directory of its own is left out entirely so it inherits.
func TestLogPolicyProjection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, agentDirs, agentLog := logPolicy(config.Config{Panel: config.PanelDefaults{
		LogDir: "~/.baton/logs",
		Agents: map[string]config.AgentProfile{
			"claude":  {Command: "claude", Log: true, LogDir: "~/work/transcripts"},
			"copilot": {Command: "copilot", Log: true},
			"plain":   {Command: "other"},
		},
	}})

	if want := filepath.Join(home, ".baton/logs"); dir != want {
		t.Errorf("fleet log-dir = %q; want the expanded %q", dir, want)
	}
	if got, want := agentDirs["claude"], filepath.Join(home, "work/transcripts"); got != want {
		t.Errorf("claude log-dir = %q; want %q", got, want)
	}
	if _, ok := agentDirs["copilot"]; ok {
		t.Errorf("a profile with no log-dir of its own must inherit, not be stored empty")
	}
	if !agentLog["claude"] || !agentLog["copilot"] {
		t.Errorf("both configured profiles should log from spawn, got %v", agentLog)
	}
	if agentLog["plain"] {
		t.Errorf("a profile that says nothing about logging must not log")
	}
}

// TestLogPolicyOffByDefault is the shape of the default: an empty config names no
// destination, which every reader treats as "logging is off".
func TestLogPolicyOffByDefault(t *testing.T) {
	dir, agentDirs, agentLog := logPolicy(config.Config{})
	if dir != "" || agentDirs != nil || agentLog != nil {
		t.Errorf("logging should be off by default, got %q %v %v", dir, agentDirs, agentLog)
	}
}

// TestLogSettingsReachTheServer checks the roll size lands on the settings the
// daemon is built from, with the built-in default filled in for an unset key.
func TestLogSettingsReachTheServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rc := reloadableSettings(config.Config{Panel: config.PanelDefaults{LogDir: "/tmp/baton-logs", LogMaxMB: 4}})
	if rc.settings.LogDir != "/tmp/baton-logs" {
		t.Errorf("LogDir = %q; want /tmp/baton-logs", rc.settings.LogDir)
	}
	if got, want := rc.settings.LogMaxBytes, panellog.MaxBytes(4); got != want {
		t.Errorf("LogMaxBytes = %d; want %d", got, want)
	}
	unset := reloadableSettings(config.Config{})
	if got, want := unset.settings.LogMaxBytes, panellog.MaxBytes(0); got != want {
		t.Errorf("an unset roll size = %d; want the built-in default %d", got, want)
	}
	// The option list is what actually carries the policy into the daemon.
	if opts := buildServerOptions(rc, ""); len(opts) == 0 {
		t.Errorf("buildServerOptions produced nothing")
	}
	if !strings.HasPrefix(rc.settings.LogDir, "/") {
		t.Errorf("the destination handed to the daemon must be absolute, got %q", rc.settings.LogDir)
	}
}
