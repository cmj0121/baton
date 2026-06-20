package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
)

// TestNewAgentFlow drives the new-agent action: A opens the workdir overlay named
// for the default profile, prefilled with the working directory, and submitting
// spawns the agent (no client, so spawnAgent just reports it).
func TestNewAgentFlow(t *testing.T) {
	m := baseModel()

	m = press(m, keyNewAgent)
	if m.input != inputAgentDir {
		t.Fatalf("A should open the agent workdir input, got %v", m.input)
	}
	if !strings.Contains(m.status, "claude") {
		t.Fatalf("status should name the default agent profile, got %q", m.status)
	}
	if m.inputBuf == "" {
		t.Fatal("the workdir should prefill with the working directory")
	}

	m.inputBuf = "~/work"
	m = press(m, "enter")
	if m.input != inputNone {
		t.Fatal("enter should close the overlay")
	}
	if !strings.Contains(m.status, "spawning") || !strings.Contains(m.status, "claude") {
		t.Fatalf("spawn status = %q", m.status)
	}
}

// TestResolveAgent checks the default resolves to the built-in claude, a
// configured profile overrides it, and an unknown default is reported.
func TestResolveAgent(t *testing.T) {
	m := baseModel()
	if prof, name, ok := m.resolveAgent(); !ok || name != "claude" || prof.Command != "claude" {
		t.Fatalf("default should be built-in claude, got %+v %q ok=%v", prof, name, ok)
	}

	m.agents = map[string]config.AgentProfile{"copilot": {Command: "gh", Args: []string{"copilot"}}}
	m.defaultAgent = "copilot"
	if prof, name, ok := m.resolveAgent(); !ok || name != "copilot" || prof.Command != "gh" {
		t.Fatalf("configured default should resolve, got %+v %q ok=%v", prof, name, ok)
	}

	m.defaultAgent = "ghost"
	if _, name, ok := m.resolveAgent(); ok || name != "ghost" {
		t.Fatalf("unknown default should report not-ok, got %q ok=%v", name, ok)
	}
}

// TestExpandDir checks the workdir expansion: ~ and blank map to home, ~/x joins
// home, and a relative path becomes absolute.
func TestExpandDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, tc := range []struct{ in, want string }{
		{"", home},
		{"~", home},
		{"~/x", filepath.Join(home, "x")},
	} {
		if got := expandDir(tc.in); got != tc.want {
			t.Fatalf("expandDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := expandDir("subdir"); !filepath.IsAbs(got) {
		t.Fatalf("a relative path should expand to absolute, got %q", got)
	}
}

// TestDirLabel checks the home directory shortens to ~ on a path boundary, while a
// sibling that merely shares the prefix is left untouched.
func TestDirLabel(t *testing.T) {
	home, _ := os.UserHomeDir()
	sep := string(os.PathSeparator)
	for _, tc := range []struct{ in, want string }{
		{home, "~"},
		{home + sep + "work", "~" + sep + "work"},
		{home + "by" + sep + "x", home + "by" + sep + "x"}, // sibling of home, not a child
		{sep + "tmp" + sep + "x", sep + "tmp" + sep + "x"},
	} {
		if got := dirLabel(tc.in); got != tc.want {
			t.Fatalf("dirLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDefaultWorkdir prefers the configured workdir and otherwise falls back to
// home — never the client's current directory.
func TestDefaultWorkdir(t *testing.T) {
	if got := (model{workdir: "/projects"}).defaultWorkdir(); got != "/projects" {
		t.Fatalf("configured workdir should win, got %q", got)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if got := (model{}).defaultWorkdir(); got != home {
			t.Fatalf("an unset workdir should fall back to home %q, got %q", home, got)
		}
	}
}
