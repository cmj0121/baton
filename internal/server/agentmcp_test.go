package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// claudeShim writes an executable named "claude" that records the argv it was
// launched with and then parks, and returns its path and the record's path.
//
// Named claude because that is how agents.MCPConfigArgs decides a backend takes
// the flag — by the command's basename — and a shim is the only way to ask what
// a panel was ACTUALLY launched with. The launched spec is a copy the server
// does not retain, so nothing on the Server can answer it; the process itself
// can.
func claudeShim(t *testing.T) (command, argv string) {
	t.Helper()
	dir := t.TempDir()
	command = filepath.Join(dir, "claude")
	argv = filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argv + "\nexec sleep 30\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil { //nolint:gosec // a test shim must be executable
		t.Fatal(err)
	}
	return command, argv
}

// launchedArgs waits for the shim to record its argv and returns it. The spawn
// is a fork, so the file appears a moment after createPanel/respawnPanel return.
func launchedArgs(t *testing.T, argv string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(argv)
		if err == nil && len(data) > 0 {
			return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		}
		if time.Now().After(deadline) {
			t.Fatalf("the panel never recorded its argv at %s (err %v)", argv, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// countFlag is how many times flag appears in argv.
func countFlag(argv []string, flag string) int {
	n := 0
	for _, a := range argv {
		if a == flag {
			n++
		}
	}
	return n
}

// TestUpgradedFleetGainsTheMemoryToolOnReRun is the regression this move exists
// for, driven through the real path: a panel whose stored spec predates
// panel.agent-mcp must gain the tool when it is re-run under a daemon that has
// the setting on.
//
// The premise is established rather than assumed. The first daemon runs with the
// setting OFF, so the spec it persists carries no MCP flag — which is exactly the
// shape of every spec written before the setting existed, and of every spec
// Restore rebuilds from such a snapshot. The second daemon is the upgrade.
//
// The mutation that kills this: append the flag in spawnPanel again and drop
// withAgentMCP from startPanel. respawnPanel then replays the frozen, flagless
// spec and the agent comes back with no way to write to the memory — silently,
// which is how a whole fleet sat unwritable without one log line about it.
func TestUpgradedFleetGainsTheMemoryToolOnReRun(t *testing.T) {
	stateF := filepath.Join(t.TempDir(), "state.json")
	command, argv := claudeShim(t)

	first, dir := identityServer(t, WithStateFile(stateF), WithAgentMCP(false))
	id, err := first.createPanel(originOperator, proto.KindAgent, command, nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if got := launchedArgs(t, argv); countFlag(got, "--mcp-config") != 0 {
		t.Fatalf("the setting was off; the panel should carry no MCP config, got %v", got)
	}
	if spec := first.specs[id].Spec; slices.Contains(spec.Args, "--mcp-config") {
		t.Fatalf("the stored spec should be the user's command line, got %v", spec.Args)
	}

	first.SaveNow()
	first.Shutdown()
	if err := os.Remove(argv); err != nil {
		t.Fatal(err)
	}

	s := newHostServer(t, WithStateFile(stateF), WithAgentMCP(true))
	s.Restore()
	if spec := s.specs[id].Spec; slices.Contains(spec.Args, "--mcp-config") {
		t.Fatalf("the restored spec should still be flagless, got %v", spec.Args)
	}
	if err := s.respawnPanel(id); err != nil {
		t.Fatalf("respawn: %v", err)
	}

	got := launchedArgs(t, argv)
	if countFlag(got, "--mcp-config") != 1 {
		t.Fatalf("a re-run agent should be pointed at the memory's config exactly once, got %v", got)
	}
	// And the spec the server keeps stays the user's own: a flag baked back in
	// would be the same defect returning by the other door.
	if spec := s.specs[id].Spec; slices.Contains(spec.Args, "--mcp-config") {
		t.Errorf("the stored spec gained baton's own wiring, got %v", spec.Args)
	}
}

// TestAgentMCPIsNotAddedTwice covers the hazard this move introduces: specs
// already on disk carry the flag from when it was baked in, and appending a
// second one would hand Claude Code two conflicting config sources.
//
// The same guard is what leaves a user who passed their own --mcp-config alone,
// which is why it is written as "an MCP config is already named" rather than as
// "this is one of ours".
func TestAgentMCPIsNotAddedTwice(t *testing.T) {
	command, argv := claudeShim(t)
	s, dir := identityServer(t, WithAgentMCP(true))

	mine := filepath.Join(t.TempDir(), "mine.json")
	id, err := s.createPanel(originOperator, proto.KindAgent, command,
		[]string{"--mcp-config", mine}, dir, "", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	_ = id

	got := launchedArgs(t, argv)
	if n := countFlag(got, "--mcp-config"); n != 1 {
		t.Fatalf("an MCP config was already named; want exactly one, got %d in %v", n, got)
	}
	if !slices.Contains(got, mine) {
		t.Errorf("the user's own config should be the one that survives, got %v", got)
	}
}

// TestWithAgentMCPLeavesTheLaunchAlone: the cases that must not be wired, each
// of which would be a panel launched differently than the operator asked.
//
// The "nobody else" cases are the ones worth the test. A flag invented for a CLI
// that does not take it is not a missing feature — it is a panel that fails to
// spawn — and a conductor handed a second MCP server sees one of its own tools
// listed twice under two names.
func TestWithAgentMCPLeavesTheLaunchAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	spec := ptymgr.Spec{Command: "claude"}

	if got := withAgentMCP(true, spec); len(got.Args) != 2 || got.Args[0] != "--mcp-config" {
		t.Fatalf("a worker agent should be pointed at a config, got %v", got.Args)
	}
	if got := withAgentMCP(false, spec); len(got.Args) != 0 {
		t.Errorf("startPanel said no; the launch should be untouched, got %v", got.Args)
	}
	if got := withAgentMCP(true, ptymgr.Spec{Command: "codex"}); len(got.Args) != 0 {
		t.Errorf("this backend takes no such flag, got %v", got.Args)
	}
	// The command is resolved by its basename, so a profile running claude from an
	// absolute path — or under another profile name — is still claude.
	if got := withAgentMCP(true, ptymgr.Spec{Command: "/usr/local/bin/claude"}); len(got.Args) != 2 {
		t.Errorf("an absolute path should resolve the same, got %v", got.Args)
	}
}

// TestWiresMemoryLeavesOutEveryPanelItIsNotFor is the other half of that
// decision, where it actually lives: which panels startPanel says yes for.
func TestWiresMemoryLeavesOutEveryPanelItIsNotFor(t *testing.T) {
	agent := []panel.Panel{{ID: "1", Kind: panel.Agent}}
	if on := (&Server{agentMCP: true, panels: agent}); !on.wiresMemoryLocked("1") {
		t.Fatal("a worker agent is exactly who this is for")
	}
	for _, tc := range []struct {
		name string
		srv  *Server
		id   string
	}{
		{"the setting is off", &Server{agentMCP: false, panels: agent}, "1"},
		{"the conductor already has the whole table",
			&Server{agentMCP: true, panels: []panel.Panel{{ID: "1", Kind: panel.Agent, Conductor: true}}}, "1"},
		{"a shell is not an agent",
			&Server{agentMCP: true, panels: []panel.Panel{{ID: "1", Kind: panel.Shell}}}, "1"},
		{"a command panel is not an agent",
			&Server{agentMCP: true, panels: []panel.Panel{{ID: "1", Kind: panel.Command}}}, "1"},
		{"a transient panel is in no fleet at all", &Server{agentMCP: true}, "score:1"},
	} {
		if tc.srv.wiresMemoryLocked(tc.id) {
			t.Errorf("%s: the launch should be untouched", tc.name)
		}
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
