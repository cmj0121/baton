package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeRemoveWithTheTreeAsItsOwnDir is the assertion behind the orphan
// sweep's choice not to derive a repository from the tree it is retiring.
//
// The sweep holds paths and nothing else — the record baton keeps has no repo in
// it — and the question was whether git would refuse to run with its cwd inside
// the worktree it is deleting. It does not: the tree is a valid repository
// context, git resolves the real repository through it, and the removal
// completes with the directory gone. So a path is enough, and the derivation the
// sweep would otherwise need has nothing to buy.
//
// Both halves are asserted, because "no error" alone would pass on a git that
// quietly declined to do anything.
func TestWorktreeRemoveWithTheTreeAsItsOwnDir(t *testing.T) {
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(repo, "feature/own-dir", wt); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	if err := WorktreeRemove(wt, wt); err != nil {
		t.Fatalf("a worktree should be removable with itself as the git dir: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("the worktree dir should be gone, stat err=%v", err)
	}
	// …and git's own view agrees, which is what makes this a removal rather than
	// an rm: a deleted-but-still-registered tree would still be listed here.
	out, err := runGit(repo, "worktree", "list")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if strings.Contains(string(out), wt) {
		t.Fatalf("git should no longer know %q, list says:\n%s", wt, out)
	}
}

// TestWorktreeRemoveRefusesAPlainDirectory is the other half of "a path is
// enough": a directory that is not a worktree is REFUSED rather than deleted,
// even when it sits inside a repository. git resolves the repo from dir and then
// checks the path against that repo's registered worktrees, so handing this
// function a directory is not on its own enough to lose it.
func TestWorktreeRemoveRefusesAPlainDirectory(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := WorktreeRemove(sub, sub); err == nil {
		t.Fatal("a plain directory inside a repo is not a worktree and must be refused")
	}
	if _, err := os.Stat(sub); err != nil {
		t.Fatalf("the refused directory must still be there: %v", err)
	}
}

// TestWorktreeRemoveRefusesADirtyTree pins the safe default the sweep leans on to
// skip rather than destroy: no --force reaches git, so a tree holding uncommitted
// work stays exactly where it stands.
//
// The assertion is on the EFFECT, not on git's wording. git localises its
// messages — a zh-TW host answers this one with 包含修改或未追蹤的檔案 — so a
// test matching the English text would pass only on the machines that happen to
// speak it.
func TestWorktreeRemoveRefusesADirtyTree(t *testing.T) {
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(repo, "feature/dirty", wt); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	dirty(t, wt)

	if err := WorktreeRemove(wt, wt); err == nil {
		t.Fatal("a dirty worktree should be refused without --force")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("a refused worktree must be left in place: %v", err)
	}
}
