package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// commandFleet is one of each kind, so every assertion below can show the third
// kind being told apart from BOTH neighbours rather than merely from one.
func commandFleet() []panel.Panel {
	return []panel.Panel{
		{ID: "a1", Kind: panel.Agent, Title: "claude · api", State: panel.Running},
		{ID: "c1", Kind: panel.Command, Title: "go · baton", State: panel.Running},
		{ID: "s1", Kind: panel.Shell, Title: "shell #3", State: panel.Idle},
	}
}

// TestKindBadgeNamesTheCommand: the badge is the operator's fastest read of what
// a panel IS, and an AGENT badge over a plain binary is #54 at its most visible.
func TestKindBadgeNamesTheCommand(t *testing.T) {
	got := kindBadge(panel.Command)
	if !strings.Contains(got, "COMMAND") {
		t.Fatalf("the command badge should say COMMAND, got %q", got)
	}
	if strings.Contains(got, "AGENT") || strings.Contains(got, "SHELL") {
		t.Fatalf("the command badge must not borrow another kind's label, got %q", got)
	}
}

// TestKindCountsSeparatesCommands: a summary that folds a running build into the
// shell count has told the operator something untrue about the machine — the same
// claim the old two-kind fleet made the other way round.
func TestKindCountsSeparatesCommands(t *testing.T) {
	agents, commands, shells := kindCounts(commandFleet())
	if agents != 1 || commands != 1 || shells != 1 {
		t.Fatalf("counts = %d agent / %d command / %d shell, want 1/1/1", agents, commands, shells)
	}

	breakdown := kindBreakdown(commandFleet())
	for _, want := range []string{"1 agent", "1 command", "1 shell"} {
		if !strings.Contains(breakdown, want) {
			t.Errorf("breakdown %q is missing %q", breakdown, want)
		}
	}
}

// TestProfileLensBucketsCommands: the profile lens asks "what kind of agent is
// this", and for a command panel the honest answer is "not one" — its own bucket,
// not the shells'.
func TestProfileLensBucketsCommands(t *testing.T) {
	cases := []struct {
		p    panel.Panel
		want string
	}{
		{panel.Panel{Kind: panel.Command}, "commands"},
		{panel.Panel{Kind: panel.Shell}, "shells"},
		{panel.Panel{Kind: panel.Agent}, "(no profile)"},
		{panel.Panel{Kind: panel.Command, Profile: "claude"}, "claude"}, // a named profile still wins
	}
	for _, c := range cases {
		if got := lensProfile.bucket(c.p); got != c.want {
			t.Errorf("bucket(%v, profile=%q) = %q, want %q", c.p.Kind, c.p.Profile, got, c.want)
		}
	}
}

// TestCockpitRefusesCommandPanels holds the client-side gates. They steer rather
// than enforce — the server is authoritative, and internal/server asserts that
// half — but a cockpit that OFFERED these on a command panel would be inviting
// the operator into a refusal, which is the same confusion in a friendlier voice.
//
// The agent case is asserted beside each one, so these fail on the kind being
// wrong rather than on a gate that refuses everything.
func TestCockpitRefusesCommandPanels(t *testing.T) {
	cmdPanel := panel.Panel{ID: "c1", Kind: panel.Command, Title: "go · baton", State: panel.Running}
	agentPanel := panel.Panel{ID: "a1", Kind: panel.Agent, Title: "claude · api", State: panel.Running}

	// Dispatch: the brief is typed at the process as keystrokes, and a plain binary
	// will never read them.
	m := baseModel()
	if got := m.startDispatch(cmdPanel); got.input != inputNone {
		t.Errorf("dispatch opened on a command panel, input=%v", got.input)
	} else if !strings.Contains(got.status, "agent panel") {
		t.Errorf("want an agent-only hint, got %q", got.status)
	}
	if got := m.startDispatch(agentPanel); got.input != inputDispatch {
		t.Errorf("dispatch should still open on an agent panel, input=%v", got.input)
	}

	// Diff: there is no work tree to reason about. A command panel is a process,
	// not a worker in a checkout.
	m = baseModel()
	m.requestDiff(cmdPanel)
	if !strings.Contains(m.status, "agent panel") {
		t.Errorf("diff on a command panel should be refused, got %q", m.status)
	}

	// The git menu, through the same question at its own entry point.
	m = baseModel()
	m.fleet = []panel.Panel{cmdPanel}
	m.zoomID = "c1"
	nm, _ := m.openGitPicker()
	if got := nm.(model); got.mode == modeGit {
		t.Error("the git menu opened on a command panel")
	} else if !strings.Contains(got.status, "agent panels") {
		t.Errorf("want an agent-only hint, got %q", got.status)
	}
}
