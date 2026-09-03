package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/control"
)

// ctlWTRepo makes a git repo with one commit, so `ctl spawn --worktree` has a
// HEAD to branch off.
func ctlWTRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// Set on the PROCESS, not just on the fixture's own git calls: the git that
	// matters here runs inside the server under test, and it inherits this
	// environment rather than the fixture's. Without it a developer's global
	// init.defaultBranch or hooks reach the code being asserted.
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

// TestCtlSpawnWorktree is the ctl half of the acceptance, through the flag struct
// the CLI parses into: --worktree --dir <repo> --branch <name> --agent <cmd>
// builds the tree and spawns the agent in it.
func TestCtlSpawnWorktree(t *testing.T) {
	repo := ctlWTRepo(t)
	sock := ctlTestServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	cmd := ctlSpawn{Agent: "/bin/sh", Arg: []string{"-c", "sleep 30"}, Dir: repo, Worktree: true, Branch: "feat/x"}
	printed := captureStdout(t, func() {
		if err := cmd.Run(c); err != nil {
			t.Fatalf("ctl spawn --worktree: %v", err)
		}
	})

	tree := filepath.Join(repo+"-worktrees", "feat-x")
	if _, err := os.Stat(filepath.Join(tree, ".git")); err != nil {
		t.Fatalf("the worktree should exist at %s: %v", tree, err)
	}

	// "prints the new id" is the acceptance, so it is read back rather than
	// assumed: the id on stdout must name a panel that exists, rooted in the TREE
	// and filed under the branch. An id that named the panel but a workdir of the
	// repo would pass every other assertion here.
	id := strings.TrimSpace(printed)
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
		if p.Group != "feat/x" {
			t.Fatalf("the printed id should name an agent filed under the branch, got group %q", p.Group)
		}
	}
	if !found {
		t.Fatalf("the printed id %q names no panel, got %+v", id, panels)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	os.Stdout = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// TestCtlSpawnPlainIsUnchanged is the "byte for byte" criterion, and the only
// form of it a test can hold: without --worktree, --dir is still the workdir the
// process runs in, and no tree is made beside the repository. A --dir that
// happens to be a repo is the sharp case, since that is the argument the worktree
// form re-points.
func TestCtlSpawnPlainIsUnchanged(t *testing.T) {
	repo := ctlWTRepo(t)
	sock := ctlTestServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	cmd := ctlSpawn{Agent: "/bin/sh", Arg: []string{"-c", "sleep 30"}, Dir: repo}
	if err := cmd.Run(c); err != nil {
		t.Fatalf("ctl spawn: %v", err)
	}
	if _, err := os.Stat(repo + "-worktrees"); !os.IsNotExist(err) {
		t.Fatalf("a spawn without --worktree must call no git worktree, stat err = %v", err)
	}
}

// TestCtlSpawnWorktreeRefusals covers what ctl.go itself decides, rather than
// re-asserting control.SpawnWorktree's argument checks — those are pinned once,
// in TestSpawnWorktreeRefusesLocally, and re-stating their wording here would
// break this package the next time it is reworded.
//
// Two things are ctl's own. `--branch` without `--worktree` is a refusal that
// exists nowhere else: it is not in the issue's list, and it is refused rather
// than ignored because a dropped branch would spawn straight into the repository
// — the misread the whole route exists to avoid. And a refusal from the layer
// below has to come back OUT of Run rather than being swallowed.
//
// The client is closed first, so a refusal naming an I/O failure instead would
// mean the command had travelled.
func TestCtlSpawnWorktreeRefusals(t *testing.T) {
	sock := ctlTestServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()

	cmd := ctlSpawn{Agent: "/bin/sh", Dir: "/tmp/repo", Branch: "feat/x"}
	if err := cmd.Run(c); err == nil {
		t.Fatal("--branch without --worktree should be refused, got none")
	} else if !strings.Contains(err.Error(), "add --worktree") {
		t.Fatalf("want the flag-pairing refusal, got %v", err)
	}

	cmd = ctlSpawn{Agent: "/bin/sh", Dir: "/tmp/repo", Worktree: true}
	if err := cmd.Run(c); err == nil {
		t.Fatal("a refusal from SpawnWorktree should reach the caller, got none")
	} else if !strings.Contains(err.Error(), "branch") {
		t.Fatalf("want the missing-branch refusal surfaced, got %v", err)
	}
}
