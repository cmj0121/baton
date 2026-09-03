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

// TestWorktreeRemoveRefusesALockedTree is the second half of "refused cleanly
// from inside the tree", beside the dirty case. A lock is deliberate — someone
// said do not remove this — and git declines it without -f -f. The combination
// that matters is the refusal arriving with dir set to the very tree git is
// declining to remove, since that is the only shape the sweep ever calls.
func TestWorktreeRemoveRefusesALockedTree(t *testing.T) {
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(repo, "feature/locked", wt); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if out, err := runGit(repo, "worktree", "lock", wt); err != nil {
		t.Fatalf("worktree lock: %v\n%s", err, out)
	}

	if err := WorktreeRemove(wt, wt); err == nil {
		t.Fatal("a locked worktree should be refused without -f -f")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("a refused worktree must be left in place: %v", err)
	}
}

// TestWorktreeRemoveLeavesOurOwnCwdAlone is the other thing worth checking about
// running git in the tree it is deleting: removing the directory a process is
// standing in is legal but strange, and the daemon must not end up holding a
// deleted working directory.
//
// It does not, and the reason is structural rather than lucky: runGit sets
// cmd.Dir, which is the CHILD's working directory, applied by the fork/exec in
// the child alone. baton never chdir()s. Asserted rather than argued, because
// "the parent is unaffected" is exactly the kind of claim that reads as obvious
// and is worth one call to prove.
func TestWorktreeRemoveLeavesOurOwnCwdAlone(t *testing.T) {
	before, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(repo, "feature/cwd", wt); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := WorktreeRemove(wt, wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	after, err := os.Getwd()
	if err != nil {
		t.Fatalf("our working directory should still resolve after the removal: %v", err)
	}
	if after != before {
		t.Fatalf("removing a worktree must not move us: was %q, now %q", before, after)
	}
	// …and it is a real directory, not an unlinked one we merely still name.
	if _, err := os.Stat("."); err != nil {
		t.Fatalf("our working directory should still exist: %v", err)
	}
}
