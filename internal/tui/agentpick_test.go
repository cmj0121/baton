package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/proto"
)

// detected is a model whose daemon reported these backends, in this order.
func detected(names ...string) []proto.AgentBackend {
	out := make([]proto.AgentBackend, 0, len(names))
	for _, n := range names {
		out = append(out, proto.AgentBackend{Name: n, Command: n})
	}
	return out
}

// TestNewAgentSkipsThePickerForOneBackend is the promise made to everyone running
// a single agent CLI: nothing about their A changed.
func TestNewAgentSkipsThePickerForOneBackend(t *testing.T) {
	m := baseModel()
	m.backends = detected("claude")

	m = press(m, keyNewAgent)
	if m.mode == modeAgentPick {
		t.Fatal("a list of one is a keystroke, not a choice — the picker should not open")
	}
	if m.input != inputAgentDir {
		t.Fatalf("A should go straight to the workdir overlay, got %v", m.input)
	}
}

// TestNewAgentPicksThenAsksWhere drives the two-overlay flow: choose the backend,
// then say where — and the spawn runs the one that was chosen, not the default.
func TestNewAgentPicksThenAsksWhere(t *testing.T) {
	m := baseModel()
	m.backends = detected("claude", "codex", "aider")

	m = press(m, keyNewAgent)
	if m.mode != modeAgentPick {
		t.Fatalf("A should open the picker when there is a choice, mode=%v", m.mode)
	}
	if m.agentCursor != 0 {
		t.Fatalf("the cursor should start on the default (claude), got row %d", m.agentCursor)
	}

	m = press(m, "down") // codex
	m = press(m, "enter")
	if m.mode != modeDashboard || m.input != inputAgentDir {
		t.Fatalf("enter should close the picker onto the workdir overlay, mode=%v input=%v", m.mode, m.input)
	}
	if m.pendingAgent != "codex" || !strings.Contains(m.status, "codex") {
		t.Fatalf("the choice should be carried into the prompt, pending=%q status=%q", m.pendingAgent, m.status)
	}

	m.inputBuf = "~/work"
	m = press(m, "enter")
	if !strings.Contains(m.status, "codex") {
		t.Fatalf("the spawn should name the chosen backend, got %q", m.status)
	}
	if m.pendingAgent != "" {
		t.Fatal("the choice is spent once spawned — the next A starts from the default again")
	}
}

// TestAgentPickerStartsOnTheDefault checks enter alone is always the "yes, the
// usual one" answer, whichever row that is.
func TestAgentPickerStartsOnTheDefault(t *testing.T) {
	m := baseModel()
	m.backends = detected("claude", "codex", "aider")
	m.defaultAgent = "aider"

	m = press(m, keyNewAgent)
	if m.agentList[m.agentCursor].Name != "aider" {
		t.Fatalf("the cursor should open on the default, got %q", m.agentList[m.agentCursor].Name)
	}
	m = press(m, "esc")
	if m.mode != modeDashboard || m.pendingAgent != "" {
		t.Fatalf("esc should back out choosing nothing, mode=%v pending=%q", m.mode, m.pendingAgent)
	}
}

// TestPanelConfigSetsTheDefaultAgent drives the config half: the row opens the
// same picker, and the choice lands in the file the cockpit owns.
func TestPanelConfigSetsTheDefaultAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := baseModel()
	m.backends = detected("claude", "codex")

	m = press(m, "ctrl+t", "P")
	m = press(m, "down")
	if m.cursor != panelRowAgent {
		t.Fatalf("down should land on the default-agent row, cursor=%d", m.cursor)
	}
	m = press(m, "e")
	if m.mode != modeAgentPick || m.agentPurpose != agentForDefault {
		t.Fatalf("e should open the picker to set the default, mode=%v purpose=%v", m.mode, m.agentPurpose)
	}
	m = press(m, "down", "enter")
	if m.mode != modePanelConfig {
		t.Fatalf("enter should return to the panel-config page, mode=%v", m.mode)
	}
	if m.defaultAgent != "codex" {
		t.Fatalf("the default should be the chosen backend, got %q", m.defaultAgent)
	}
	if got := loadPrefs().defaultAgent; got != "codex" {
		t.Fatalf("default-agent not persisted, got %q", got)
	}
}

// TestResolveAgentLayering pins the order the daemon detects with: the user's own
// profile wins over a detected backend of the same name, and a detected backend
// spawns without any profile being written at all.
func TestResolveAgentLayering(t *testing.T) {
	m := baseModel()
	m.backends = detected("claude", "codex")

	if prof, name, ok := m.resolveAgentNamed("codex"); !ok || name != "codex" || prof.Command != "codex" {
		t.Fatalf("a detected backend should spawn unconfigured, got %+v %q ok=%v", prof, name, ok)
	}

	m.agents = map[string]config.AgentProfile{"codex": {Command: "codex", Args: []string{"--full-auto"}}}
	if prof, _, ok := m.resolveAgentNamed("codex"); !ok || len(prof.Args) != 1 {
		t.Fatalf("the user's own profile should win, got %+v ok=%v", prof, ok)
	}
}

// TestResolveAgentBlamesTheMachineNotTheConfig covers the message this feature
// exists to fix: a default that names a real backend which is simply not
// installed used to read as a config error.
func TestResolveAgentBlamesTheMachineNotTheConfig(t *testing.T) {
	m := baseModel()
	m.backends = detected("claude")
	m.defaultAgent = "codex"

	if _, name, ok := m.resolveAgent(); ok || name != "codex" {
		t.Fatalf("an uninstalled default should not resolve, got %q ok=%v", name, ok)
	}
	msg := m.agentUnavailable("codex")
	if !strings.Contains(msg, "not found") || !strings.Contains(msg, "re-detect") {
		t.Fatalf("the message should blame the machine and point at the re-detect, got %q", msg)
	}
	if got := m.defaultAgentLabel(); !strings.Contains(got, "not found") {
		t.Fatalf("the panel-config row should say the default is missing, got %q", got)
	}

	// A name nothing has ever heard of is still a config error, and still says so.
	m.backends = nil
	if msg := m.agentUnavailable("ghost"); !strings.Contains(msg, "no agent profile") {
		t.Fatalf("an unknown name should read as a config error, got %q", msg)
	}
}

// TestAvailableAgentsFallsBackWithoutDetection covers the version skew that the
// daemon's lifetime makes real: a fresh cockpit attached to a daemon that never
// detected anything still offers what the config names.
func TestAvailableAgentsFallsBackWithoutDetection(t *testing.T) {
	m := baseModel()
	if list := m.availableAgents(); len(list) != 1 || list[0].Name != "claude" {
		t.Fatalf("an unconfigured cockpit should still offer the built-in, got %+v", list)
	}

	m.agents = map[string]config.AgentProfile{"zeta": {Command: "zeta"}, "alpha": {Command: "alpha"}}
	list := m.availableAgents()
	if len(list) != 3 || list[0].Name != "alpha" || list[1].Name != "zeta" || list[2].Name != "claude" {
		t.Fatalf("the fallback should offer the configured profiles, sorted, plus the built-in, got %+v", list)
	}
}

// TestConfigMessageCarriesTheBackends proves the list arrives the way it is sent:
// on the config push, beside the config rather than inside it.
func TestConfigMessageCarriesTheBackends(t *testing.T) {
	m := baseModel()
	m.applyEvent(proto.ServerMsg{Type: "config", Agents: detected("claude", "aider")})
	if len(m.backends) != 2 || m.backends[1].Name != "aider" {
		t.Fatalf("the config push should land the detected backends, got %+v", m.backends)
	}
}
