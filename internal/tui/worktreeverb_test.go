package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// dashWithClient is a dashboard wired to a recording client, so what the spawn
// verb puts on the wire can be asserted. It carries no panels: the whole point of
// n w is that it needs none.
func dashWithClient(t *testing.T) (model, <-chan proto.Command) {
	t.Helper()
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.mode = modeDashboard
	return m, cmds
}

// TestNWAsksRepoThenBranch is the verb's shape: n w asks WHERE first, in a field
// that takes a typed path, and only then asks which branch — the same prompt the
// git menu opens.
func TestNWAsksRepoThenBranch(t *testing.T) {
	m, _ := dashWithClient(t)
	m = press(m, "n", "w")
	if m.input != inputWorktreeRepo {
		t.Fatalf("n w should open the repository field, got input=%v", m.input)
	}
	if !inputIsPath(m.input) || !inputIsDir(m.input) {
		t.Fatal("the repository field must take tab completion and the C-o picker, the way A's workdir does")
	}

	m.inputBuf = "/tmp/repo"
	nm, _ := m.commitInput()
	m = nm.(model)
	if m.input != inputWorktreeBranch {
		t.Fatalf("the repository should hand over to the branch prompt, got input=%v", m.input)
	}
	if m.wtRepo != "/tmp/repo" {
		t.Fatalf("the repository should be parked across the branch prompt, got %q", m.wtRepo)
	}
}

// TestNWSendsNoPanel is the acceptance criterion that tells the primitive apart
// from the panel path: the command carries a repo and NO target id, so nothing
// about it can have been resolved from a source panel.
func TestNWSendsNoPanel(t *testing.T) {
	m, cmds := dashWithClient(t)
	m = press(m, "n", "w")
	m.inputBuf = "/tmp/repo"
	nm, _ := m.commitInput()
	m = nm.(model)
	m.inputBuf = "feature/solo"
	nm, _ = m.commitInput()
	m = nm.(model)

	got := waitCmd(t, cmds, isGit("worktree-add"))
	if got.ID != "" {
		t.Fatalf("the dashboard verb has no source panel, so it must send no id, got %q", got.ID)
	}
	if got.Dir != "/tmp/repo" || got.Name != "feature/solo" {
		t.Fatalf("worktree-add should carry the repo and the branch, got %+v", got)
	}

	// The profile is the FLEET DEFAULT, resolved the way A resolves it, since the
	// server holds no agent commands to resolve a default from.
	prof, name, ok := m.resolveAgentNamed(m.effDefaultAgent())
	if !ok {
		t.Fatal("the test fleet should resolve a default agent")
	}
	if got.Path != prof.Command || got.Profile != name {
		t.Fatalf("worktree-add should carry the fleet default (%s / %s), got %+v", name, prof.Command, got)
	}
}

// TestNWZoomForm keeps the OTHER caller honest: the git menu's w still resolves
// from the agent you are watching, so it sends that panel's id and no directory.
func TestNWZoomForm(t *testing.T) {
	m, cmds := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("w")
	m = nm.(model)
	m.inputBuf = "feature/iso"
	nm, _ = m.commitInput()
	m = nm.(model)

	got := waitCmd(t, cmds, isGit("worktree-add"))
	if got.ID != "a1" {
		t.Fatalf("the menu's form should still name the zoomed agent, got %q", got.ID)
	}
	if got.Dir != "" || got.Path != "" {
		t.Fatalf("the menu's form resolves server-side, so it should carry no dir or spec, got %+v", got)
	}
}

// TestNWNoLeakIntoMenu holds the property that makes the shared field safe: an
// n w abandoned after the repository was typed leaves that repository parked, and
// the next C-t G w must still resolve from the zoomed agent rather than spawning
// against a directory picked for a different verb in a different view.
//
// The two prompts commit through different purposes, so this cannot happen by
// construction — but the purpose split is exactly what a later refactor might
// undo, and this is the assertion that would catch it.
func TestNWNoLeakIntoMenu(t *testing.T) {
	m, cmds := dashWithClient(t)
	p := panel.Panel{ID: "a1", Kind: panel.Agent, Title: "claude", State: panel.Running}
	m.fleet = []panel.Panel{p}

	m = press(m, "n", "w")
	m.inputBuf = "/tmp/abandoned"
	nm, _ := m.commitInput()
	m = nm.(model)
	m.input = inputNone // esc out of the branch prompt

	m = m.zoomInto(p)
	m = openGitMenu(t, m)
	nm, _ = m.handleGitKey("w")
	m = nm.(model)
	if m.input != inputGitWorktree {
		t.Fatalf("the menu's w must open its OWN purpose, got input=%v", m.input)
	}
	m.inputBuf = "feature/iso"
	nm, _ = m.commitInput()
	m = nm.(model)

	got := waitCmd(t, cmds, isGit("worktree-add"))
	if got.Dir != "" || got.ID != "a1" {
		t.Fatalf("the menu's form must not inherit the abandoned repository, got %+v", got)
	}
}

// TestNWNeedsARepo holds the empty answer at each step: an empty repository and
// an empty branch both reopen their field rather than firing an op with a hole in
// it — the way the git menu's own prompts already behave.
func TestNWNeedsARepo(t *testing.T) {
	m, cmds := dashWithClient(t)
	m = press(m, "n", "w")
	m.inputBuf = ""
	nm, _ := m.commitInput()
	m = nm.(model)
	if m.input != inputWorktreeRepo {
		t.Fatalf("an empty repository should reopen the field, got input=%v", m.input)
	}

	m.inputBuf = "/tmp/repo"
	nm, _ = m.commitInput()
	m = nm.(model)
	m.inputBuf = ""
	nm, _ = m.commitInput()
	m = nm.(model)
	if m.input != inputWorktreeBranch {
		t.Fatalf("an empty branch should reopen the branch field, got input=%v", m.input)
	}
	noMatch(t, cmds, isGit("worktree-add"))
}

// TestNWListedInSpawnFamily is the discoverability criterion: ? on the dashboard
// shows n w under the n landing, beside the other spawns.
func TestNWListedInSpawnFamily(t *testing.T) {
	m := model{width: 120, height: 400, fleet: sampleFleet(), prefixKey: keyPrefix,
		binds: append([]binding(nil), bindings...), mode: modeHelp, helpFrom: modeDashboard}
	_, body := helpRows(m)

	var row string
	for _, l := range body {
		plain := stripANSI(l)
		if strings.Contains(plain, "isolated in a new worktree") {
			row = plain
			break
		}
	}
	if row == "" {
		t.Fatal("the dashboard key list should carry the new-worktree verb")
	}
	// Under a landing a member shows only the key that COMPLETES it, so the row
	// says "w" and the family header above it says "n".
	if !strings.Contains(strings.Fields(row)[0], "w") {
		t.Fatalf("n w should be listed under the n family by its completing key, got %q", row)
	}
}
