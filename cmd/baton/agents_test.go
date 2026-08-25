package main

import (
	"testing"

	"github.com/cmj0121/baton/internal/agents"
	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/proto"
)

// swapDetect makes the PATH scan answer for a machine the test describes.
func swapDetect(t *testing.T, installed ...string) {
	t.Helper()
	on := make(map[string]bool, len(installed))
	for _, n := range installed {
		on[n] = true
	}
	prev := detectBackends
	detectBackends = func(cands []agents.Backend) []agents.Scanned {
		out := make([]agents.Scanned, 0, len(cands))
		for _, b := range cands {
			out = append(out, agents.Scanned{Backend: b, Missing: !b.Isolated && !on[b.Command]})
		}
		return out
	}
	t.Cleanup(func() { detectBackends = prev })
}

// TestDetectAgentsLayersUserProfiles proves the daemon's view of the catalogue:
// the presets it found, with the user's own profiles layered over them by name.
func TestDetectAgentsLayersUserProfiles(t *testing.T) {
	swapDetect(t, "claude", "gemini", "/opt/bin/inhouse")

	cfg := config.Config{}
	cfg.Panel.Agents = map[string]config.AgentProfile{
		"gemini":  {Command: "gemini", Args: []string{"--yolo"}},
		"inhouse": {Command: "/opt/bin/inhouse"},
	}
	got := detectAgents(cfg)

	byName := map[string]proto.AgentBackend{}
	for _, b := range got {
		byName[b.Name] = b
	}
	if b, ok := byName["gemini"]; !ok || b.Missing || len(b.Args) != 1 || b.Args[0] != "--yolo" {
		t.Fatalf("the user's gemini profile should win and be installed, got %+v", b)
	}
	if b, ok := byName["claude"]; !ok || b.Missing {
		t.Fatalf("claude is on this machine, got %+v ok=%v", b, ok)
	}
	if b, ok := byName["inhouse"]; !ok || b.Missing {
		t.Fatalf("the user's own profile is on this machine, got %+v ok=%v", b, ok)
	}

	// The whole change: a backend the machine has not got is reported, not dropped,
	// so the cockpit can name it instead of pretending the catalogue is what it
	// found. It carries where to get it.
	b, ok := byName["codex"]
	if !ok || !b.Missing {
		t.Fatalf("a backend this machine lacks should be reported as missing, got %+v ok=%v", b, ok)
	}
	if b.Homepage == "" {
		t.Fatalf("a missing preset with no homepage says a name and nothing else, got %+v", b)
	}
}

// TestDetectAgentsUserProfilesCarryNoHomepage pins the layering's edge: a profile
// that overrides a preset replaces it outright, homepage included. Keeping the
// preset's URL would point someone at the official CLI to explain why THEIR
// command is missing, which is a different program with the same name.
func TestDetectAgentsUserProfilesCarryNoHomepage(t *testing.T) {
	swapDetect(t) // an empty machine, so every entry comes back missing

	cfg := config.Config{}
	cfg.Panel.Agents = map[string]config.AgentProfile{
		"claude": {Command: "/opt/bin/our-claude"},
	}
	for _, b := range detectAgents(cfg) {
		if b.Name != "claude" {
			continue
		}
		if b.Homepage != "" {
			t.Fatalf("an overridden preset should not keep the preset homepage, got %+v", b)
		}
		return
	}
	t.Fatal("the overridden profile is not in the catalogue at all")
}

// TestDetectAgentsKeepsIsolatedProfiles pins the container case end to end: the
// binary is nowhere on this host, and the profile is still offered because its
// command runs inside an image.
func TestDetectAgentsKeepsIsolatedProfiles(t *testing.T) {
	swapDetect(t) // an empty machine

	cfg := config.Config{}
	cfg.Panel.Agents = map[string]config.AgentProfile{
		"boxed": {Command: "claude", Isolate: "docker", Image: "example/agent"},
		"plain": {Command: "claude"},
	}
	byName := map[string]proto.AgentBackend{}
	for _, b := range detectAgents(cfg) {
		byName[b.Name] = b
	}
	if b, ok := byName["boxed"]; !ok || b.Missing {
		t.Fatalf("an isolated profile runs in an image, so an empty host must not mark it missing, got %+v ok=%v", b, ok)
	}
	if b, ok := byName["plain"]; !ok || !b.Missing {
		t.Fatalf("a host profile on an empty machine is missing, got %+v ok=%v", b, ok)
	}
}

// TestIsolationIntended reads the raw value the way AgentProfile.Isolation does:
// anything but a bare "none" is an intent to isolate, a typo included, so a
// mistyped runtime cannot quietly be treated as a host command.
func TestIsolationIntended(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{{"", false}, {"none", false}, {" NONE ", false}, {"docker", true}, {"dcoker", true}} {
		if got := isolationIntended(config.AgentProfile{Isolate: tc.in}); got != tc.want {
			t.Errorf("isolationIntended(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
