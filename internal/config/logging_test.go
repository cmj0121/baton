package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestLoggingKeysParse checks the on-disk shape of the logging block: a
// fleet-wide destination and roll size, and a profile that logs from spawn into
// a directory of its own.
func TestLoggingKeysParse(t *testing.T) {
	const in = `
panel:
  log-dir: ~/.baton/logs
  log-max-mb: 16
  agents:
    claude:
      command: claude
      log: true
      log-dir: ~/work/transcripts
    copilot:
      command: copilot
`
	var c Config
	if err := yaml.Unmarshal([]byte(in), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := c.Panel.LogDir, "~/.baton/logs"; got != want {
		t.Errorf("panel.log-dir = %q; want %q", got, want)
	}
	if got, want := c.Panel.LogMaxMB, 16; got != want {
		t.Errorf("panel.log-max-mb = %d; want %d", got, want)
	}
	claude := c.Panel.Agents["claude"]
	if !claude.Log {
		t.Errorf("panel.agents.claude.log = false; want true")
	}
	if got, want := claude.LogDir, "~/work/transcripts"; got != want {
		t.Errorf("panel.agents.claude.log-dir = %q; want %q", got, want)
	}
	if copilot := c.Panel.Agents["copilot"]; copilot.Log || copilot.LogDir != "" {
		t.Errorf("a profile that says nothing about logging must inherit: %+v", copilot)
	}
}

// TestLoggingDefaultsOff is the shape of the feature's default: nothing is
// written until a directory is named, and no profile logs on its own.
func TestLoggingDefaultsOff(t *testing.T) {
	var c Config
	if err := yaml.Unmarshal([]byte("panel:\n  shell: /bin/sh\n"), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Panel.LogDir != "" || c.Panel.LogMaxMB != 0 {
		t.Errorf("logging should be off by default, got dir=%q max=%d", c.Panel.LogDir, c.Panel.LogMaxMB)
	}
}

// TestNegativeLogMaxNormalized checks that a hand-edited nonsense roll size is
// coerced back to "use the built-in default" rather than travelling on.
func TestNegativeLogMaxNormalized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	path := filepath.Join(dir, ".baton", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("panel:\n  log-max-mb: -5\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Panel.LogMaxMB != 0 {
		t.Errorf("panel.log-max-mb = %d; want 0 (the built-in default)", c.Panel.LogMaxMB)
	}
}

// TestLoggingKeysRoundTrip checks the keys survive a Save/Load cycle, so the
// cockpit rewriting the config never drops a logging setting it does not own.
func TestLoggingKeysRoundTrip(t *testing.T) {
	c := Config{Panel: PanelDefaults{
		LogDir:   "~/.baton/logs",
		LogMaxMB: 8,
		Agents:   map[string]AgentProfile{"claude": {Command: "claude", Log: true, LogDir: "/tmp/t"}},
	}}
	data, err := yaml.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Config
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Panel.LogDir != c.Panel.LogDir || back.Panel.LogMaxMB != c.Panel.LogMaxMB {
		t.Errorf("fleet-wide logging keys did not round-trip: %+v", back.Panel)
	}
	if got := back.Panel.Agents["claude"]; !got.Log || got.LogDir != "/tmp/t" {
		t.Errorf("profile logging keys did not round-trip: %+v", got)
	}
}
