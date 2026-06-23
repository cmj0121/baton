package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
)

// recvUntil drains telemetry/stats until a control message of the wanted type
// arrives, so a monitor tick between a command and its reply does not fail a test.
func recvUntil(t *testing.T, c *client.Client, want string) proto.ServerMsg {
	t.Helper()
	for i := 0; i < 30; i++ {
		msg := recv(t, c)
		if msg.Type == "telemetry" || msg.Type == "stats" {
			continue
		}
		if msg.Type != want {
			t.Fatalf("expected a %q message, got %+v", want, msg)
		}
		return msg
	}
	t.Fatalf("never saw a %q message", want)
	return proto.ServerMsg{}
}

// TestGitLogCaptured checks a non-interactive output op (log) is captured and
// replied as a "gitout" message carrying the target id and text, spawning no PTY —
// the cockpit renders it in a scrollable popup rather than auto-zooming a panel.
func TestGitLogCaptured(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.git", Git: "log", ID: agentID}); err != nil {
		t.Fatalf("panel.git log: %v", err)
	}
	reply := recvUntil(t, c, "gitout")
	if reply.ID != agentID {
		t.Fatalf("gitout should carry the target id %q, got %q", agentID, reply.ID)
	}
	if !strings.Contains(reply.Text, "init") { // the seed repo's first commit subject
		t.Fatalf("log output should contain the seed commit, got %q", reply.Text)
	}
	if reply.Failed {
		t.Fatalf("a clean log should not be flagged failed")
	}
	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("a captured git op should spawn no PTY, but %d ephemeral panels exist", got)
	}
}

// TestGitCommitOpensEphemeral checks commit alone keeps the transient-PTY path: it
// needs $EDITOR, so it replies "ephemeral" (a "git:"-prefixed, auto-zoomed panel)
// rather than capturing to a popup.
func TestGitCommitOpensEphemeral(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.git", Git: "commit", ID: agentID}); err != nil {
		t.Fatalf("panel.git commit: %v", err)
	}
	reply := recvUntil(t, c, "ephemeral")
	if !strings.HasPrefix(reply.ID, "git:") {
		t.Fatalf("a git ephemeral id should be git:-prefixed, got %q", reply.ID)
	}
	if got := srv.EphemeralCount(); got != 1 {
		t.Fatalf("expected 1 tracked ephemeral panel, got %d", got)
	}
}

// TestGitOnShellRejected checks the agent-only gate and that no PTY is spawned.
func TestGitOnShellRejected(t *testing.T) {
	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	id := createShells(t, c, 1)[0]
	if err := c.Send(proto.Command{Action: "panel.git", Git: "status", ID: id}); err != nil {
		t.Fatalf("panel.git status: %v", err)
	}
	msg := recvUntil(t, c, "error")
	if !strings.Contains(msg.Error, "available on agent panels") {
		t.Fatalf("git on a shell should be gated, got %q", msg.Error)
	}
	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("the gate should spawn no PTY, but %d ephemeral panels exist", got)
	}
}

// TestGitCommitCleanTree checks commit refuses a clean tree and spawns nothing.
func TestGitCommitCleanTree(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)
	if err := os.Remove(filepath.Join(repo, "new.txt")); err != nil { // make the tree clean
		t.Fatal(err)
	}

	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.git", Git: "commit", ID: agentID}); err != nil {
		t.Fatalf("panel.git commit: %v", err)
	}
	msg := recvUntil(t, c, "error")
	if !strings.Contains(msg.Error, "nothing to commit") {
		t.Fatalf("commit on a clean tree should refuse, got %q", msg.Error)
	}
	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("a refused commit should spawn nothing, got %d ephemeral panels", got)
	}
}

// TestGitUnknownOp checks an unrecognised op is an error, not a spawn.
func TestGitUnknownOp(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.git", Git: "frobnicate", ID: agentID}); err != nil {
		t.Fatalf("panel.git frobnicate: %v", err)
	}
	msg := recvUntil(t, c, "error")
	if !strings.Contains(msg.Error, "unknown git op") {
		t.Fatalf("an unknown op should error, got %q", msg.Error)
	}
	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("an unknown op should spawn nothing, got %d ephemeral panels", got)
	}
}

// TestGitWorktreeAdd is the isolation bridge: worktree-add makes
// a worktree on a new branch and spawns an agent rooted in it, grouped under the
// branch, broadcast as a fleet change. The new worktree exists on disk.
func TestGitWorktreeAdd(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	_, sock := startDiffServer(t)
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.git", Git: "worktree-add", ID: agentID, Name: "feature/iso"}); err != nil {
		t.Fatalf("panel.git worktree-add: %v", err)
	}
	snap := recvUntil(t, c, "panels")

	var grouped int
	for _, p := range snap.Panels {
		if p.Group == "feature/iso" {
			grouped++
		}
	}
	if grouped != 1 {
		t.Fatalf("expected one agent grouped under feature/iso, got %d in %+v", grouped, snap.Panels)
	}
	wt := filepath.Join(repo+"-worktrees", "feature-iso")
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("the worktree should exist at %s: %v", wt, err)
	}
}

// TestGitWorktreeRmShell checks the agent-only gate reaches worktree-remove too
// (it routes through agentTargetSpec like every other op), so a shell is refused.
func TestGitWorktreeRmShell(t *testing.T) {
	_, sock := startDiffServer(t)
	c := dialReady(t, sock)

	id := createShells(t, c, 1)[0]
	if err := c.Send(proto.Command{Action: "panel.git", Git: "worktree-remove", ID: id, Dir: "/tmp/x"}); err != nil {
		t.Fatalf("panel.git worktree-remove: %v", err)
	}
	msg := recvUntil(t, c, "error")
	if !strings.Contains(msg.Error, "available on agent panels") {
		t.Fatalf("worktree-remove on a shell should be gated, got %q", msg.Error)
	}
}

// TestGitWorktreeRemove checks the remove path is wired and surfaces
// git's own refusal for a path that is not a worktree.
func TestGitWorktreeRemove(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	_, sock := startDiffServer(t)
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.git", Git: "worktree-remove", ID: agentID, Dir: filepath.Join(repo, "nope")}); err != nil {
		t.Fatalf("panel.git worktree-remove: %v", err)
	}
	msg := recvUntil(t, c, "error")
	if !strings.Contains(msg.Error, "worktree remove") {
		t.Fatalf("removing a non-worktree should surface git's refusal, got %q", msg.Error)
	}
}
