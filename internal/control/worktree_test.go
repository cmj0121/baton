package control_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/control"
)

// wtRepo makes a fresh git repo with one commit, so it has a HEAD to branch off.
//
// GIT_CONFIG_GLOBAL/SYSTEM are set on the PROCESS, not only on the fixture's own
// git calls: the git that matters here runs inside the server under test, and it
// inherits this environment rather than the fixture's. Without it a developer's
// global init.defaultBranch or hooks reach the code being asserted.
func wtRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=baton", "GIT_AUTHOR_EMAIL=baton@example.com",
		"GIT_COMMITTER_NAME=baton", "GIT_COMMITTER_EMAIL=baton@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
	)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	return dir
}

// TestSpawnWorktree is the ctl/MCP acceptance at the layer both share: a repo, a
// branch and an agent go in; a tree, an agent rooted IN that tree, and a group
// named for the branch come out, and the returned id names the new panel.
//
// The group assertion is the one that matters. dir means the repository here and
// the workdir everywhere else on this client, so a panel that landed in the repo
// ungrouped would be the misread this whole route exists to avoid — and it is
// exactly what a spawn that quietly ignored the worktree would produce. That the
// panel's workdir IS the tree is pinned once at the primitive, in the server's
// TestWorktreeSpawnWithoutAPanel; what is asserted here is that this client
// reaches it with the right repo and branch.
func TestSpawnWorktree(t *testing.T) {
	repo := wtRepo(t)
	sock := startServer(t)

	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	id, err := c.SpawnWorktree("/bin/sh", []string{"-c", "sleep 30"}, repo, "feature/ctl")
	if err != nil {
		t.Fatalf("SpawnWorktree: %v", err)
	}
	if id == "" {
		t.Fatal("SpawnWorktree returned an empty id")
	}

	tree := filepath.Join(repo+"-worktrees", "feature-ctl")
	if _, err := os.Stat(filepath.Join(tree, ".git")); err != nil {
		t.Fatalf("the worktree should exist at %s: %v", tree, err)
	}

	panels, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, p := range panels {
		if p.ID != id {
			continue
		}
		found = true
		if p.Group != "feature/ctl" {
			t.Fatalf("the new agent should be filed under the branch, got group %q", p.Group)
		}
	}
	if !found {
		t.Fatalf("the returned id %q names no panel, got %+v", id, panels)
	}
}

// TestSpawnWorktreeRefusesLocally holds the "before any git runs" promise at its
// strongest: the client is CLOSED first, so anything that reached the socket
// would fail with an I/O error instead. Each refusal naming its own missing
// argument is the proof that none of them left the process.
//
// The server keeps the same two promises for a client that does not check — the
// branch ahead of the rev-parse that decides whether dir is a repository, and the
// command ahead of the tree being built. Those are asserted where they live, in
// the server's TestWTAddNoBranch and TestWTAddNoAgentLeavesNoTree; this client
// short-circuits before either can be reached from here.
func TestSpawnWorktreeRefusesLocally(t *testing.T) {
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for _, tc := range []struct {
		name           string
		agent, repo    string
		branch, expect string
	}{
		{"no branch", "/bin/sh", "/tmp/repo", "", "branch"},
		{"no repo", "/bin/sh", "", "feature/x", "repository"},
		{"no agent", "", "/tmp/repo", "feature/x", "agent command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.SpawnWorktree(tc.agent, nil, tc.repo, tc.branch)
			if err == nil {
				t.Fatal("want a refusal, got none")
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Fatalf("want a refusal naming %q, got %v", tc.expect, err)
			}
		})
	}
}

// TestSpawnWorktreeNonRepo: the repository check is the server's, because the
// tree is built on the server's host — so this one does travel. It must still
// leave the fleet exactly as it found it.
func TestSpawnWorktreeNonRepo(t *testing.T) {
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	before, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := c.SpawnWorktree("/bin/sh", nil, t.TempDir(), "feature/nope"); err == nil {
		t.Fatal("a worktree spawn against a non-repo should fail")
	} else if !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("want the non-repo refusal, got %v", err)
	}
	after, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("a refused worktree spawn must leave the fleet unchanged: %d -> %d", len(before), len(after))
	}
}

// TestSpawnPanelUnchanged is the "byte for byte" criterion at the layer both
// front ends share. SpawnPanel is the method that did not change, and the sharp
// case is a dir that IS a repository — the one argument the worktree route
// re-points — because a careless implementation reads the repo-ness and opens a
// tree nobody asked for.
func TestSpawnPanelUnchanged(t *testing.T) {
	repo := wtRepo(t)
	sock := startServer(t)

	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	id, err := c.SpawnPanel("/bin/sh", []string{"-c", "sleep 30"}, repo)
	if err != nil {
		t.Fatalf("SpawnPanel: %v", err)
	}
	if id == "" {
		t.Fatal("SpawnPanel returned an empty id")
	}
	if _, err := os.Stat(repo + "-worktrees"); !os.IsNotExist(err) {
		t.Fatalf("a plain spawn must call no git worktree, stat err = %v", err)
	}

	// And it is filed nowhere: the group comes from the branch, and there is no
	// branch on this path.
	panels, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range panels {
		if p.ID == id && p.Group != "" {
			t.Fatalf("a plain spawn should join no work item, got group %q", p.Group)
		}
	}
}
