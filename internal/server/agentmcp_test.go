package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentMCPArgsOnlyWhereTheyWork: the flag is added for a backend that takes
// one, for nobody else, and never when the setting is off.
//
// The "nobody else" half is the one worth a test. A flag invented for a CLI that
// does not take it is not a missing feature — it is a panel that fails to spawn,
// so a backend baton has not been taught about must start exactly as it did
// before this existed.
func TestAgentMCPArgsOnlyWhereTheyWork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	on := &Server{agentMCP: true}

	got := on.agentMCPArgs("claude")
	if len(got) != 2 || got[0] != "--mcp-config" {
		t.Fatalf("claude should be pointed at a config, got %v", got)
	}
	// The command is resolved by its basename, so a profile running claude from an
	// absolute path — or under another profile name — is still claude.
	if abs := on.agentMCPArgs("/usr/local/bin/claude"); len(abs) != 2 || abs[1] != got[1] {
		t.Errorf("an absolute path should resolve the same, got %v", abs)
	}
	for _, other := range []string{"codex", "aider", "gemini", "bash"} {
		if extra := on.agentMCPArgs(other); extra != nil {
			t.Errorf("%s takes no such flag, got %v", other, extra)
		}
	}
	off := &Server{agentMCP: false}
	if extra := off.agentMCPArgs("claude"); extra != nil {
		t.Errorf("the setting is off, got %v", extra)
	}
}

// TestAgentMCPConfigIsBatonsOwn: the config a worker panel loads is written in
// baton's directory and carries the score-only server.
//
// Both halves are the point. In baton's directory, because a worker panel runs in
// the operator's repository and a dotfile written there would turn up in their
// `git status` and, sooner or later, in a commit — which is exactly why the
// conductor's .mcp.json could not simply be reused. And score-only, because the
// full table drives the fleet: spawn, close, signal. That is the conductor's job,
// and a worker given it could close the panel next to it.
func TestAgentMCPConfigIsBatonsOwn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := writeAgentMCPConfig()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".baton"); !strings.HasPrefix(path, want) {
		t.Errorf("the config should live under %s, got %s", want, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("the config is not valid JSON: %v\n%s", err, data)
	}
	srv, ok := cfg.MCPServers["baton"]
	if !ok {
		t.Fatalf("no baton server in the config:\n%s", data)
	}
	if strings.Join(srv.Args, " ") != "mcp --score-only" {
		t.Errorf("a worker must get the score-only server, got %v", srv.Args)
	}
}
