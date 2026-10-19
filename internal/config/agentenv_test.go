package config

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEnvironAbsentIsEmpty(t *testing.T) {
	env, err := AgentProfile{Command: "grok"}.Environ()
	if env != nil || err != nil {
		t.Fatalf("Environ() = %v, %v; want nothing", env, err)
	}
}

// TestEnvironFromYAML covers the shape a user writes, including the quoted "1"
// a YAML number needs to stay a string, and the sorted order a spawn relies on.
func TestEnvironFromYAML(t *testing.T) {
	var c Config
	src := `
panel:
  agents:
    grok:
      command: grok
      env:
        GROK_TERMINAL_THEME: "1"
        GROK_THEME: terminal
`
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	env, err := c.Panel.Agents["grok"].Environ()
	if err != nil {
		t.Fatalf("Environ: %v", err)
	}
	want := []string{"GROK_TERMINAL_THEME=1", "GROK_THEME=terminal"}
	if !slices.Equal(env, want) {
		t.Fatalf("Environ() = %v, want %v", env, want)
	}
}

// TestEnvironDropsWhatCannotBeSet is report-and-drop: each bad entry is named in
// the error and left out, and the good ones beside it survive.
func TestEnvironDropsWhatCannotBeSet(t *testing.T) {
	prof := AgentProfile{Command: "grok", Env: map[string]string{
		"GROK_THEME":     "terminal",
		"":               "x",
		"A=B":            "x",
		"BATON_ROLE":     "conductor",
		"BATON_PANEL_ID": "7",
		"NUL":            "a\x00b",
	}}
	env, err := prof.Environ()
	if !slices.Equal(env, []string{"GROK_THEME=terminal"}) {
		t.Fatalf("Environ() kept %v, want only GROK_THEME", env)
	}
	if err == nil {
		t.Fatal("dropped entries must be reported")
	}
	for _, name := range []string{`""`, `"A=B"`, "BATON_ROLE", "BATON_PANEL_ID", "NUL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

// TestEnvironIsSorted pins the order over enough keys and calls that Go's
// randomised map iteration would show through if Environ did not sort.
func TestEnvironIsSorted(t *testing.T) {
	prof := AgentProfile{Command: "x", Env: map[string]string{"E": "5", "B": "2", "D": "4", "A": "1", "C": "3"}}
	want := []string{"A=1", "B=2", "C=3", "D=4", "E=5"}
	for range 50 {
		if env, _ := prof.Environ(); !slices.Equal(env, want) {
			t.Fatalf("Environ() = %v, want %v", env, want)
		}
	}
}
