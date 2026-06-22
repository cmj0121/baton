package tui

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// zoomedAgent returns a model zoomed into one agent panel, wired to a recording
// client so the commands the git menu sends can be asserted.
func zoomedAgent(t *testing.T) (model, <-chan proto.Command) {
	t.Helper()
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	p := panel.Panel{ID: "a1", Kind: panel.Agent, Title: "claude · api", State: panel.Running}
	m.fleet = []panel.Panel{p}
	m = m.zoomInto(p)
	return m, cmds
}

// openGitMenu drives C-t g on the zoom and asserts the menu opened.
func openGitMenu(t *testing.T, m model) model {
	t.Helper()
	nm, _ := m.handleZoomKey(key("ctrl+t"))
	m = nm.(model)
	nm, _ = m.handleZoomKey(key("g"))
	m = nm.(model)
	if m.mode != modeGit {
		t.Fatalf("C-t g should open the git menu, got mode=%v", m.mode)
	}
	return m
}

// noMatch fails if a command matching pred arrives within a short window; other
// commands (a zoom's attach/resize) are drained and ignored.
func noMatch(t *testing.T, cmds <-chan proto.Command, pred func(proto.Command) bool) {
	t.Helper()
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case c := <-cmds:
			if pred(c) {
				t.Fatalf("unexpected command: %+v", c)
			}
		case <-deadline:
			return
		}
	}
}

func isGit(op string) func(proto.Command) bool {
	return func(c proto.Command) bool { return c.Action == "panel.git" && c.Git == op }
}

func TestGitMenuOpensOnAgentZoom(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)
	if m.gitTarget.ID != "a1" {
		t.Fatalf("the git menu should target the zoomed agent, got %q", m.gitTarget.ID)
	}
}

func TestGitMenuGatedOnShell(t *testing.T) {
	c, _ := recordingServer(t)
	m := baseModel()
	m.client = c
	p := panel.Panel{ID: "s1", Kind: panel.Shell, Title: "shell", State: panel.Running}
	m.fleet = []panel.Panel{p}
	m = m.zoomInto(p)

	nm, _ := m.handleZoomKey(key("ctrl+t"))
	m = nm.(model)
	nm, _ = m.handleZoomKey(key("g"))
	m = nm.(model)
	if m.mode == modeGit {
		t.Fatal("the git menu must not open on a shell zoom")
	}
	if m.status != "git: available on agent panels" {
		t.Fatalf("expected the agent-only hint, got %q", m.status)
	}
}

func TestGitMenuLogSendsEphemeral(t *testing.T) {
	m, cmds := zoomedAgent(t)
	m = openGitMenu(t, m)

	nm, _ := m.handleGitKey("l")
	m = nm.(model)
	got := waitCmd(t, cmds, isGit("log"))
	if got.ID != "a1" {
		t.Fatalf("log should target the zoomed agent, got %+v", got)
	}
	if m.pendingDiffTitle == "" {
		t.Fatal("an output op should stash a zoom title for the reply")
	}
	if m.mode != modeZoom {
		t.Fatalf("the menu should close back to the zoom, got mode=%v", m.mode)
	}
}

func TestGitMenuCommitSends(t *testing.T) {
	m, cmds := zoomedAgent(t)
	m = openGitMenu(t, m)
	m.handleGitKey("c") // fires the commit op; the send is the assertion below
	waitCmd(t, cmds, isGit("commit"))
}

func TestGitMenuPushConfirms(t *testing.T) {
	m, cmds := zoomedAgent(t)
	m = openGitMenu(t, m)

	// p parks a confirm and sends nothing yet.
	nm, _ := m.handleGitKey("p")
	m = nm.(model)
	if m.gitConfirmOp != "push" {
		t.Fatalf("push should park a y/n confirm, got op=%q", m.gitConfirmOp)
	}
	noMatch(t, cmds, isGit("push"))

	// y fires the push.
	nm, _ = m.handleGitKey("y")
	m = nm.(model)
	waitCmd(t, cmds, isGit("push"))
	if m.mode != modeZoom {
		t.Fatalf("after confirming, the menu should close to the zoom, got mode=%v", m.mode)
	}
}

func TestGitMenuPushDeclined(t *testing.T) {
	m, cmds := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("p")
	m = nm.(model)
	nm, _ = m.handleGitKey("n")
	m = nm.(model)
	if m.mode != modeZoom {
		t.Fatalf("declining push should return to the zoom, got mode=%v", m.mode)
	}
	noMatch(t, cmds, isGit("push"))
}

func TestGitMenuBranchInput(t *testing.T) {
	m, cmds := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("b")
	m = nm.(model)
	if m.input != inputGitBranch {
		t.Fatalf("b should open the branch-name field, got input=%v", m.input)
	}
	m.inputBuf = "feature/x"
	nm, _ = m.commitInput()
	m = nm.(model)
	got := waitCmd(t, cmds, isGit("branch"))
	if got.Name != "feature/x" {
		t.Fatalf("branch should carry the typed name, got %+v", got)
	}
}

func TestGitMenuWorktreeInput(t *testing.T) {
	m, cmds := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("w")
	m = nm.(model)
	if m.input != inputGitWorktree {
		t.Fatalf("w should open the worktree-branch field, got input=%v", m.input)
	}
	m.inputBuf = "feature/iso"
	nm, _ = m.commitInput()
	m = nm.(model)
	got := waitCmd(t, cmds, isGit("worktree-add"))
	if got.Name != "feature/iso" {
		t.Fatalf("worktree-add should carry the branch, got %+v", got)
	}
}

func TestGitMenuRemoveConfirms(t *testing.T) {
	m, cmds := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("x")
	m = nm.(model)
	if m.input != inputGitRemove {
		t.Fatalf("x should open the path field, got input=%v", m.input)
	}
	m.inputBuf = "/tmp/wt"
	nm, _ = m.commitInput()
	m = nm.(model)
	if m.gitConfirmOp != "remove" || m.gitRemovePath != "/tmp/wt" {
		t.Fatalf("a typed path should park a remove confirm, got op=%q path=%q", m.gitConfirmOp, m.gitRemovePath)
	}
	noMatch(t, cmds, isGit("worktree-remove"))

	nm, _ = m.handleGitKey("y")
	m = nm.(model)
	got := waitCmd(t, cmds, isGit("worktree-remove"))
	if got.Dir != "/tmp/wt" {
		t.Fatalf("confirmed remove should carry the path in Dir, got %+v", got)
	}
}

func TestGitMenuEscCancels(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("esc")
	m = nm.(model)
	if m.mode != modeZoom {
		t.Fatalf("esc should close the menu back to the zoom, got mode=%v", m.mode)
	}
}
