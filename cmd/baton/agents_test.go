package main

import (
	"testing"

	"github.com/cmj0121/baton/internal/agents"
	"github.com/cmj0121/baton/internal/config"
)

// swapDetect makes the PATH scan answer for a machine the test describes.
func swapDetect(t *testing.T, installed ...string) {
	t.Helper()
	on := make(map[string]bool, len(installed))
	for _, n := range installed {
		on[n] = true
	}
	prev := detectBackends
	detectBackends = func(cands []agents.Backend) []agents.Backend {
		out := make([]agents.Backend, 0, len(cands))
		for _, b := range cands {
			if b.Isolated || on[b.Command] {
				out = append(out, b)
			}
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

	byName := map[string]int{}
	for i, b := range got {
		byName[b.Name] = i
	}
	if len(got) != 3 {
		t.Fatalf("want claude + gemini + inhouse, got %+v", got)
	}
	if i, ok := byName["gemini"]; !ok || len(got[i].Args) != 1 || got[i].Args[0] != "--yolo" {
		t.Fatalf("the user's gemini profile should win, got %+v", got)
	}
	if _, ok := byName["codex"]; ok {
		t.Fatalf("a backend this machine does not have should not be offered, got %+v", got)
	}
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
	got := detectAgents(cfg)
	if len(got) != 1 || got[0].Name != "boxed" {
		t.Fatalf("only the isolated profile should survive an empty machine, got %+v", got)
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
