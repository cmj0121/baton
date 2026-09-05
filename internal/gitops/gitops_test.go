package gitops

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// initRepo makes a temp git repo with one empty commit and a configured identity,
// so commit-path probes behave. It returns the work-tree directory.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-q", "-m", "init")
	return dir
}

func dirty(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveReadAndAdditiveOps(t *testing.T) {
	dir := initRepo(t)
	cases := []struct {
		op       Op
		wantName string
		wantArgs []string
	}{
		{OpStatus, "git", []string{"status"}},
		{OpLog, "git", []string{"log", "--oneline", "--graph", "--decorate", "-n", "200"}},
		{OpWorktreeList, "git", []string{"worktree", "list"}},
		{OpAdd, "git", []string{"add", "-A"}},
		{OpPush, "git", []string{"push"}},
	}
	for _, c := range cases {
		t.Run(string(c.op), func(t *testing.T) {
			name, args, env, err := Resolve(c.op, dir, "", "")
			if err != nil {
				t.Fatalf("Resolve(%s) errored: %v", c.op, err)
			}
			if name != c.wantName || !slices.Equal(args, c.wantArgs) || env != nil {
				t.Fatalf("Resolve(%s) = %q %v env=%v, want %q %v nil", c.op, name, args, env, c.wantName, c.wantArgs)
			}
		})
	}
}

func TestResolveCommit(t *testing.T) {
	dir := initRepo(t)

	// A clean tree has nothing to commit.
	if _, _, _, err := Resolve(OpCommit, dir, "", ""); err == nil || !strings.Contains(err.Error(), "nothing to commit") {
		t.Fatalf("commit on a clean tree should refuse, got %v", err)
	}

	dirty(t, dir)
	name, args, env, err := Resolve(OpCommit, dir, "", "nvim")
	if err != nil {
		t.Fatalf("commit on a dirty tree errored: %v", err)
	}
	if name != "sh" || len(args) != 2 || args[0] != "-c" || !strings.Contains(args[1], "git add -A && git commit") {
		t.Fatalf("commit should stage-all then commit via sh -c, got %q %v", name, args)
	}
	if !slices.Contains(env, "GIT_EDITOR=nvim") {
		t.Fatalf("a configured editor should be injected as GIT_EDITOR, got env=%v", env)
	}

	// With no configured editor, nothing is injected — git uses its own chain.
	if _, _, env, _ := Resolve(OpCommit, dir, "", ""); env != nil {
		t.Fatalf("no configured editor should inject no env, got %v", env)
	}
}

func TestResolveBranch(t *testing.T) {
	dir := initRepo(t)

	name, args, _, err := Resolve(OpBranch, dir, "feature/x", "")
	if err != nil {
		t.Fatalf("branch errored: %v", err)
	}
	if name != "git" || !slices.Equal(args, []string{"checkout", "-b", "feature/x"}) {
		t.Fatalf("branch should checkout -b, got %q %v", name, args)
	}

	if _, _, _, err := Resolve(OpBranch, dir, "   ", ""); err == nil {
		t.Fatal("a branch with a blank name should be refused")
	}
}

func TestResolveRejections(t *testing.T) {
	dir := initRepo(t)

	// A non-repo is refused up front.
	if _, _, _, err := Resolve(OpStatus, t.TempDir(), "", ""); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("a non-repo should be refused, got %v", err)
	}
	// The synchronous worktree ops are not panel commands.
	for _, op := range []Op{OpWorktreeAdd, OpWorktreeRemove} {
		if _, _, _, err := Resolve(op, dir, "", ""); err == nil {
			t.Fatalf("%s should not resolve to a panel command", op)
		}
	}
	// An unknown op is refused.
	if _, _, _, err := Resolve(Op("frobnicate"), dir, "", ""); err == nil {
		t.Fatal("an unknown op should be refused")
	}
}

func TestWorktreeAddAndRemove(t *testing.T) {
	dir := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")

	if err := WorktreeAdd(dir, "feature/wt", wt); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("the worktree should exist at %s: %v", wt, err)
	}
	// The branch must now exist and be checked out in the worktree.
	out, err := runGit(wt, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || strings.TrimSpace(string(out)) != "feature/wt" {
		t.Fatalf("the worktree should be on feature/wt, got %q (%v)", out, err)
	}

	// A duplicate branch is refused with git's own reason, not a bare code.
	err = WorktreeAdd(dir, "feature/wt", filepath.Join(t.TempDir(), "wt2"))
	if err == nil || !strings.Contains(err.Error(), "worktree add") {
		t.Fatalf("a duplicate branch should be refused, got %v", err)
	}

	if err := WorktreeRemove(dir, wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("the worktree dir should be gone, stat err=%v", err)
	}
}

func TestWorktreeArgValidation(t *testing.T) {
	dir := initRepo(t)
	if err := WorktreeAdd(dir, "", "/tmp/x"); err == nil {
		t.Fatal("a blank branch should be refused")
	}
	if err := WorktreeAdd(dir, "b", ""); err == nil {
		t.Fatal("a blank path should be refused")
	}
	if err := WorktreeRemove(dir, ""); err == nil {
		t.Fatal("a blank remove path should be refused")
	}
}

func TestNeedsPTY(t *testing.T) {
	if !NeedsPTY(OpCommit) {
		t.Fatal("commit drives $EDITOR, so it needs a PTY")
	}
	for _, op := range []Op{OpStatus, OpLog, OpAdd, OpPush, OpBranch, OpWorktreeList} {
		if NeedsPTY(op) {
			t.Fatalf("%s is non-interactive and should not need a PTY", op)
		}
	}
}

// TestCaptureStatus checks a read-only op is captured to text: status of a dirty
// tree names the untracked file and is not flagged failed.
func TestCaptureStatus(t *testing.T) {
	dir := initRepo(t)
	dirty(t, dir)

	res, err := Capture(OpStatus, dir, "", "")
	if err != nil {
		t.Fatalf("Capture(status) errored: %v", err)
	}
	if res.Failed {
		t.Fatal("a clean `git status` should not be flagged failed")
	}
	if !strings.Contains(res.Output, "new.txt") {
		t.Fatalf("status should mention the untracked file, got %q", res.Output)
	}
}

// TestCaptureRejectsCommit checks commit is refused at the capture layer (it needs a
// terminal) with nothing to show, so the caller falls back to the PTY path.
func TestCaptureRejectsCommit(t *testing.T) {
	dir := initRepo(t)
	dirty(t, dir)

	res, err := Capture(OpCommit, dir, "", "")
	if err == nil {
		t.Fatal("commit should be refused by Capture")
	}
	if res.Output != "" {
		t.Fatalf("a refused capture should have no output, got %q", res.Output)
	}
}

// TestCaptureFailedExit checks a non-zero git exit is reported via Failed (with the
// message in Output), not as an error — push with no remote configured fails this
// way, so the popup can still show git's reason.
func TestCaptureFailedExit(t *testing.T) {
	dir := initRepo(t)

	res, err := Capture(OpPush, dir, "", "")
	if err != nil {
		t.Fatalf("a failed push should surface via Failed, not an error: %v", err)
	}
	if !res.Failed {
		t.Fatal("a push with no remote should be flagged failed")
	}
	if strings.TrimSpace(res.Output) == "" {
		t.Fatal("a failed push should carry git's message in Output")
	}
}

// TestCaptureNotARepo checks Capture surfaces the not-a-repo pre-flight error.
func TestCaptureNotARepo(t *testing.T) {
	if _, err := Capture(OpStatus, t.TempDir(), "", ""); err == nil {
		t.Fatal("Capture outside a repo should error")
	}
}

// TestCaptureBoundsGitsOutput pins the read cap.
//
// A captured op's whole output is held in memory and then sent to the cockpit as
// one gitout message, so whatever git prints becomes a socket payload. `git
// status` is the one that can run away: it collapses an untracked DIRECTORY to a
// single line, but lists root-level untracked files one per line — 60,000 of
// them measured at 3 MB, and it scales linearly with no ceiling of its own. That
// is an ordinary agent accident (a build that sprayed files beside the source),
// not an attack, which is exactly why the daemon should not have to hold it.
func TestCaptureBoundsGitsOutput(t *testing.T) {
	dir := initRepo(t)
	// Long names rather than many files: git prints one line per ROOT-level
	// untracked file, so ~110 bytes each clears maxCaptureBytes in 3,000 writes
	// instead of 5,000-odd. Untracked files must sit at the root — git collapses a
	// directory to a single line, which is why this is bounded in practice at all.
	pad := strings.Repeat("x", 80)
	for i := range 3000 {
		name := fmt.Sprintf("untracked_%s_%06d.txt", pad, i)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	res, err := Capture(OpStatus, dir, "", "")
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(res.Output) > maxCaptureBytes+capTruncationSlack {
		t.Fatalf("Capture held %d bytes; want it bounded at %d", len(res.Output), maxCaptureBytes)
	}
	if !strings.Contains(res.Output, "truncated") {
		t.Fatalf("a truncated capture must say so; got the last 120 bytes as %q", tailOf(res.Output, 120))
	}
}

// TestCaptureKeepsAShortOutputWhole is the other side of the cap: an ordinary
// status must arrive untouched and unmarked.
func TestCaptureKeepsAShortOutputWhole(t *testing.T) {
	dir := initRepo(t)
	dirty(t, dir)

	res, err := Capture(OpStatus, dir, "", "")
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if strings.Contains(res.Output, "truncated") {
		t.Fatalf("a short status must not be marked truncated: %q", res.Output)
	}
	// The filename, not git's prose: git's messages are localised and this suite
	// runs under whatever locale the developer has.
	if !strings.Contains(res.Output, "new.txt") {
		t.Fatalf("an uncapped status should still name the untracked file: %q", res.Output)
	}
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
