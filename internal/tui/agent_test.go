package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/proto"
)

// parkedAgentOffer types dir into A's workdir overlay and commits it, leaving
// the here/isolate confirm parked. It presses A first only when the overlay is
// not already open, so a picker that has just handed off still works.
func parkedAgentOffer(t *testing.T, m model, dir string) model {
	t.Helper()
	if m.input != inputAgentDir {
		m = press(m, keyNewAgent)
	}
	if m.input != inputAgentDir {
		t.Fatalf("A should open the workdir overlay, got %v", m.input)
	}
	m.inputBuf = dir
	m = press(m, "enter")
	if !m.pendingSpawn {
		t.Fatal("workdir enter should park the here/isolate offer")
	}
	if m.input != inputNone {
		t.Fatal("the offer is status-line-only; the overlay should be closed")
	}
	return m
}

// TestNewAgentFlow drives the new-agent action: A opens the workdir overlay named
// for the default profile, prefilled with the working directory. Submitting the
// workdir parks a here/isolate confirm; a second enter spawns in that directory
// (no client, so spawnAgent just reports it).
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

	m = parkedAgentOffer(t, m, "~/work")
	if !strings.Contains(m.status, "here") || !strings.Contains(m.status, "isolate") {
		t.Fatalf("the offer should name both answers, got %q", m.status)
	}
	if strings.Contains(m.status, "spawning") {
		t.Fatalf("the parked offer must not look like a spawn already fired, got %q", m.status)
	}

	m = press(m, "enter")
	if m.pendingSpawn {
		t.Fatal("the second enter should spend the offer")
	}
	if !strings.Contains(m.status, "spawning") || !strings.Contains(m.status, "claude") {
		t.Fatalf("spawn status = %q", m.status)
	}
	if strings.Contains(m.status, "isolate") {
		t.Fatalf("here-spawn status must not imply isolate happened, got %q", m.status)
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

// TestNewAgentEnterEnterSpawnsHere is the default answer on the parked offer:
// A, dir, enter, enter still creates an agent in that directory.
func TestNewAgentEnterEnterSpawnsHere(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m = parkedAgentOffer(t, m, "/tmp/proj")
	m = press(m, "enter")

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.create" })
	if got.Kind != proto.KindAgent {
		t.Fatalf("here should spawn an agent, got %+v", got)
	}
	if got.Dir != expandDir("/tmp/proj") {
		t.Fatalf("Dir should be the expanded workdir, got %q want %q", got.Dir, expandDir("/tmp/proj"))
	}
}

// TestNewAgentWIsolatesOnABranch is the other answer: w asks for a branch, then
// sends the same targetless worktree-add as n w, with the picked profile.
func TestNewAgentWIsolatesOnABranch(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m = parkedAgentOffer(t, m, "/tmp/proj")
	m = press(m, "w")
	if m.input != inputIsolateBranch {
		t.Fatalf("w should open the isolate branch field, got %v", m.input)
	}
	if m.pendingClose {
		t.Fatal("w on the offer must not arm a panel close")
	}

	m.inputBuf = ""
	nm, _ := m.commitInput()
	m = nm.(model)
	if m.input != inputIsolateBranch {
		t.Fatalf("an empty branch should reopen the field, got %v", m.input)
	}

	m.inputBuf = "feature/iso"
	nm, _ = m.commitInput()
	m = nm.(model)

	got := waitCmd(t, cmds, isGit("worktree-add"))
	if got.ID != "" {
		t.Fatalf("A+w has no source panel, so it must send no id, got %q", got.ID)
	}
	if got.Dir != expandDir("/tmp/proj") || got.Name != "feature/iso" {
		t.Fatalf("worktree-add should carry the typed workdir and branch, got %+v", got)
	}
	prof, name, ok := m.resolveAgentNamed(m.effDefaultAgent())
	if !ok {
		t.Fatal("the test fleet should resolve a default agent")
	}
	if got.Path != prof.Command || got.Profile != name {
		t.Fatalf("with nothing picked, isolate should use the fleet default (%s / %s), got %+v", name, prof.Command, got)
	}
	if m.pendingAgent != "" || m.spawnDir != "" || m.pendingSpawn {
		t.Fatal("the isolate send should spend the parked spawn")
	}
}

// TestNewAgentOfferKeysDoNotLeak is why the confirm sits beside pendingClose:
// w must not close a panel, and n must not open the spawn family.
func TestNewAgentOfferKeysDoNotLeak(t *testing.T) {
	m := baseModel()
	m.fleet = sampleFleet()
	before := len(m.fleet)
	m = parkedAgentOffer(t, m, "/tmp/proj")

	mw := press(m, "w")
	if mw.pendingClose {
		t.Fatal("w on the offer must not arm a panel close")
	}
	if len(mw.fleet) != before {
		t.Fatal("w on the offer must not close a panel")
	}
	if mw.input != inputIsolateBranch {
		t.Fatalf("w should ask for a branch, got input=%v", mw.input)
	}

	mn := press(m, "n")
	if len(mn.pending) != 0 {
		t.Fatalf("n on the offer must not start the n landing, pending=%v", mn.pending)
	}
	if mn.input == inputWorktreeRepo || mn.input == inputNewPanelCmd {
		t.Fatalf("n on the offer must not open an n-family overlay, input=%v", mn.input)
	}
	if !strings.Contains(mn.status, "spawning") {
		t.Fatalf("n on the offer is here, status=%q", mn.status)
	}
}

// TestNewAgentOfferCancelClearsPending: esc or any other key on the parked
// confirm drops the picker choice, so the next A cannot inherit it.
func TestNewAgentOfferCancelClearsPending(t *testing.T) {
	for _, key := range []string{"esc", "x"} {
		t.Run(key, func(t *testing.T) {
			c, cmds := recordingServer(t)
			m := baseModel()
			m.client = c
			m.backends = detected("claude", "codex")
			m = press(m, keyNewAgent)
			m = press(m, "down", "enter") // codex
			m = parkedAgentOffer(t, m, "/tmp/proj")
			if m.pendingAgent != "codex" {
				t.Fatalf("the pick should be held until spawn or abort, pending=%q", m.pendingAgent)
			}

			m = press(m, key)
			if m.pendingSpawn || m.pendingAgent != "" || m.spawnDir != "" {
				t.Fatalf("cancel must clear the offer, pendingSpawn=%v pendingAgent=%q spawnDir=%q",
					m.pendingSpawn, m.pendingAgent, m.spawnDir)
			}
			if len(m.pending) != 0 {
				t.Fatalf("cancel should consume the key, pending=%v", m.pending)
			}
			noMatch(t, cmds, func(c proto.Command) bool {
				return c.Action == "panel.create" || c.Action == "panel.git"
			})

			m = press(m, keyNewAgent)
			if m.mode != modeAgentPick {
				t.Fatalf("next A should reopen the picker, mode=%v", m.mode)
			}
			if m.agentList[m.agentCursor].Name != "claude" {
				t.Fatalf("the picker should start on the default, not the aborted pick, got %q", m.agentList[m.agentCursor].Name)
			}
			if m.pendingAgent != "" {
				t.Fatalf("aborted pick leaked into the next A, pending=%q", m.pendingAgent)
			}
		})
	}
}

// TestNewAgentIsolateBranchEscAborts: backing out of the branch field is still
// an abort — the picker choice must not survive it.
func TestNewAgentIsolateBranchEscAborts(t *testing.T) {
	m := baseModel()
	m.backends = detected("claude", "codex")
	m = press(m, keyNewAgent)
	m = press(m, "down", "enter")
	m = parkedAgentOffer(t, m, "/tmp/proj")
	m = press(m, "w", "esc")
	if m.pendingAgent != "" || m.spawnDir != "" || m.input != inputNone || m.pendingSpawn {
		t.Fatalf("esc on the branch field should abort, pendingAgent=%q spawnDir=%q input=%v pendingSpawn=%v",
			m.pendingAgent, m.spawnDir, m.input, m.pendingSpawn)
	}
}
