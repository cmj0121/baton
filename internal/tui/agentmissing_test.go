package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/cmj0121/baton/internal/proto"
)

// scanned is a model's view of a daemon that reported the whole catalogue: the
// installed names first, then the ones it knows and did not find.
func scanned(installed []string, missing map[string]string) []proto.AgentBackend {
	out := make([]proto.AgentBackend, 0, len(installed)+len(missing))
	for _, n := range installed {
		out = append(out, proto.AgentBackend{Name: n, Command: n})
	}
	for _, n := range []string{"codex", "gemini", "aider", "opencode", "grok"} {
		if home, ok := missing[n]; ok {
			out = append(out, proto.AgentBackend{Name: n, Command: n, Homepage: home, Missing: true})
		}
	}
	return out
}

// TestPickerNeverOffersAMissingBackend holds the picker's rule while the same
// list starts carrying entries that cannot be spawned: what the daemon reports
// grew, what A offers did not.
func TestPickerNeverOffersAMissingBackend(t *testing.T) {
	m := baseModel()
	m.backends = scanned([]string{"claude"}, map[string]string{
		"codex": "https://github.com/openai/codex",
		"grok":  "https://github.com/xai-org/grok-build",
	})

	if got := m.availableAgents(); len(got) != 1 || got[0].Name != "claude" {
		t.Fatalf("the picker should see only what can be spawned, got %+v", got)
	}

	// And with only one spawnable backend left, A stays the single keystroke it was
	// before the misses joined the list.
	m = press(m, keyNewAgent)
	if m.mode == modeAgentPick {
		t.Fatal("two of three entries are unspawnable — that is a list of one, not a choice")
	}
}

// TestMissingBackendDoesNotResolve stops the other door into a spawn: naming a
// missing backend as the fleet default must fail the way an unknown one does,
// rather than resolving to a command that is not there and dying in the panel.
func TestMissingBackendDoesNotResolve(t *testing.T) {
	m := baseModel()
	m.backends = scanned([]string{"claude"}, map[string]string{"codex": "https://github.com/openai/codex"})
	m.defaultAgent = "codex"

	if _, _, ok := m.resolveAgentNamed("codex"); ok {
		t.Fatal("a backend the machine has not got resolved as spawnable")
	}
	if got := m.defaultAgentLabel(); !strings.Contains(got, "not found") {
		t.Fatalf("the config row should say the default is not there, got %q", got)
	}
}

// TestPanelConfigNamesWhatIsNotInstalled is the feature: the page says what baton
// knows and this machine has not got, and where to get each one.
func TestPanelConfigNamesWhatIsNotInstalled(t *testing.T) {
	m := baseModel()
	m.mode = modePanelConfig
	m.backends = scanned([]string{"claude"}, map[string]string{
		"codex": "https://github.com/openai/codex",
		"grok":  "https://github.com/xai-org/grok-build",
	})

	out := ansi.Strip(m.panelConfigView())
	for _, want := range []string{
		spaced("KNOWN, NOT INSTALLED"), // the section headers are letter-spaced
		"codex", "github.com/openai/codex",
		"grok", "github.com/xai-org/grok-build",
		"C-t R re-detects", // and it says how to act on it — with the prefix, since bare R does nothing
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page never mentions %q\n%s", want, out)
		}
	}
	// The scheme is dropped: it costs columns the popup has to find elsewhere and
	// tells the reader nothing.
	if strings.Contains(out, "https://") {
		t.Errorf("the URLs should render without their scheme\n%s", out)
	}
	// A backend that IS installed belongs in the picker, not in this list.
	if i := strings.Index(out, spaced("KNOWN, NOT INSTALLED")); i >= 0 && strings.Contains(out[i:], "claude") {
		t.Errorf("an installed backend was listed as missing\n%s", out)
	}
}

// TestPanelConfigSaysNothingWhenEverythingIsInstalled keeps the page quiet in the
// case it has nothing to add. An empty section header is a worse answer than no
// section at all.
func TestPanelConfigSaysNothingWhenEverythingIsInstalled(t *testing.T) {
	m := baseModel()
	m.mode = modePanelConfig
	m.backends = scanned([]string{"claude", "codex"}, nil)

	if out := ansi.Strip(m.panelConfigView()); strings.Contains(out, spaced("KNOWN, NOT INSTALLED")) {
		t.Errorf("nothing is missing — the section should not render\n%s", out)
	}
}

// TestTrimScheme covers the shortening on its own, including the user profile's
// empty homepage, which the page falls back to a command name for.
func TestTrimScheme(t *testing.T) {
	for in, want := range map[string]string{
		"https://opencode.ai":              "opencode.ai",
		"https://github.com/openai/codex/": "github.com/openai/codex",
		"http://example.com":               "example.com",
		"":                                 "",
	} {
		if got := trimScheme(in); got != want {
			t.Errorf("trimScheme(%q) = %q, want %q", in, got, want)
		}
	}
}
