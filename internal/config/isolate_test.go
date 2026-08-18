package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/cmj0121/baton/internal/isolate"
)

func TestIsolationAbsentIsolatesNothing(t *testing.T) {
	for _, prof := range []AgentProfile{{Command: "claude"}, {Command: "claude", Isolate: "none"}, {Command: "claude", Isolate: " NONE "}} {
		p, err := prof.Isolation()
		if err != nil {
			t.Fatalf("Isolation(%q): %v", prof.Isolate, err)
		}
		if p.Enabled() {
			t.Fatalf("isolate %q must confine nothing", prof.Isolate)
		}
	}
}

func TestIsolationParsesAProfile(t *testing.T) {
	prof := AgentProfile{
		Command:  "claude",
		Isolate:  "docker",
		Image:    "example/agent:1",
		Mount:    "workspace+home",
		Network:  "bridge",
		EnvAllow: []string{"ANTHROPIC_API_KEY"},
		User:     " root ",
	}
	p, err := prof.Isolation()
	if err != nil {
		t.Fatalf("Isolation: %v", err)
	}
	if p.Mode != isolate.ModeDocker || p.Image != "example/agent:1" {
		t.Fatalf("mode/image = %q/%q", p.Mode, p.Image)
	}
	if p.Mount != isolate.MountHome || p.Network != isolate.NetworkBridge {
		t.Fatalf("mount/network = %q/%q", p.Mount, p.Network)
	}
	if p.User != "root" {
		t.Fatalf("User = %q, want the trimmed value", p.User)
	}
	if len(p.EnvAllow) != 1 || p.EnvAllow[0] != "ANTHROPIC_API_KEY" {
		t.Fatalf("EnvAllow = %v", p.EnvAllow)
	}
	if p.Invalid != "" {
		t.Fatalf("a well-formed profile must not be poisoned: %q", p.Invalid)
	}
}

// TestIsolationNeverFallsBackToTheHost is the property that separates this
// setting from every other one in the file: a policy baton cannot read must keep
// refusing to spawn, because the forgiving direction here is an unconfined panel.
func TestIsolationNeverFallsBackToTheHost(t *testing.T) {
	for name, prof := range map[string]AgentProfile{
		"an unknown runtime":  {Isolate: "dockerr", Image: "example/agent:1"},
		"a missing image":     {Isolate: "docker"},
		"an unknown mount":    {Isolate: "docker", Image: "example/agent:1", Mount: "everything"},
		"an unknown network":  {Isolate: "docker", Image: "example/agent:1", Network: "vpn"},
		"several at once":     {Isolate: "podman", Mount: "all", Network: "vpn"},
		"a runtime with none": {Isolate: "docker", Image: "", Mount: "workspace"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := prof.Isolation()
			if err == nil {
				t.Fatal("expected an error naming what the file got wrong")
			}
			if !p.Enabled() {
				t.Fatal("a broken policy must stay enabled, or the panel spawns unconfined")
			}
			if p.Invalid == "" {
				t.Fatal("the reason must travel with the policy so the spawn can report it")
			}
			if verr := p.Validate(); verr == nil {
				t.Fatal("a poisoned policy must fail Validate at spawn time too")
			}
		})
	}
}

func TestIsolationNamesWhatWasWrong(t *testing.T) {
	_, err := AgentProfile{Isolate: "podman", Image: "x"}.Isolation()
	if err == nil || !strings.Contains(err.Error(), "podman") {
		t.Fatalf("the error must quote the value the user wrote, got %v", err)
	}
	_, err = AgentProfile{Isolate: "docker"}.Isolation()
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("a missing image must say so, got %v", err)
	}
}

func TestIsolationKeysWithoutIsolateAreInert(t *testing.T) {
	p, err := AgentProfile{Command: "claude", Image: "example/agent:1"}.Isolation()
	if err == nil {
		t.Fatal("keys that do nothing must be reported: they read like a setting in force")
	}
	if p.Enabled() {
		t.Fatal("a profile that never asked to isolate must not be poisoned into failing every spawn")
	}
}

func TestIsolationFromYAML(t *testing.T) {
	var c Config
	src := `
panel:
  agents:
    claude:
      command: claude
      isolate: docker
      image: baton/agent:latest
      mount: workspace
      network: host
      env-allow: [ANTHROPIC_API_KEY, GH_TOKEN]
      user: root
`
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	prof, ok := c.Panel.Agents["claude"]
	if !ok {
		t.Fatal("the claude profile did not parse")
	}
	p, err := prof.Isolation()
	if err != nil {
		t.Fatalf("Isolation: %v", err)
	}
	if p.Mode != isolate.ModeDocker || p.Image != "baton/agent:latest" || len(p.EnvAllow) != 2 {
		t.Fatalf("the sketch in the issue must parse as written, got %+v", p)
	}
}
