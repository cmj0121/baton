// Package gitops resolves a git "operation" — one entry of the cockpit's git menu
// — into the concrete command to run in a panel's workdir, plus the few mutating
// helpers (worktree add/remove) the menu drives directly. It builds on gitdiff for
// the work-tree and change probes and, like gitdiff, is a set of pure helpers that
// shell out to git: the server spawns a resolved command as an ephemeral panel, so
// baton needs no in-process git library and the layer stays unit-testable.
//
// The menu is agent-only and zoom-scoped, but nothing here knows that — it takes a
// directory and an op and returns a command, leaving the gating to the caller. No
// op carries a force flag; the set is additive (status/log/diff are read-only,
// add/commit/branch/worktree-add are additive, push is plain) so a misfire never
// destroys work.
package gitops

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/gitdiff"
)

// gitTimeout bounds the synchronous helpers (worktree add/remove). The resolved
// commands themselves run as long-lived panels, not under this timeout.
const gitTimeout = 10 * time.Second

// Op is one git operation the menu offers. It rides the wire as a plain string.
type Op string

// The git operations the menu offers; each op's value is what the wire carries.
const (
	OpLog            Op = "log"           // git log of the workdir
	OpStatus         Op = "status"        // git status
	OpAdd            Op = "add"           // stage every change
	OpCommit         Op = "commit"        // stage all, then commit in $EDITOR
	OpPush           Op = "push"          // push to the upstream
	OpBranch         Op = "branch"        // create and switch to a new branch
	OpWorktreeList   Op = "worktree-list" // list the repo's worktrees
	OpWorktreeAdd    Op = "worktree-add"  // add a worktree on a new branch (+ spawn an agent)
	OpWorktreeRemove Op = "worktree-remove"
)

// Resolve maps an output-producing op to the command the caller spawns in the
// panel's workdir (the spawn sets the directory, so the argv carries no -C). arg
// is the op's parameter where one applies — a branch name for OpBranch; editor is
// the configured commit editor for OpCommit, injected as GIT_EDITOR (empty lets
// git use its own GIT_EDITOR / core.editor / EDITOR / vi chain). It returns the
// executable, its args, and any extra environment, or an error when the op is
// unknown here, lacks a parameter it needs, or has nothing to do.
//
// The mutating worktree ops are not resolved to a panel — they run synchronously
// through WorktreeAdd / WorktreeRemove — so they are rejected here.
func Resolve(op Op, dir, arg, editor string) (name string, args, env []string, err error) {
	if !gitdiff.IsWorkTree(dir) {
		return "", nil, nil, fmt.Errorf("not a git repository: %s", dir)
	}
	switch op {
	case OpStatus:
		return "git", []string{"status"}, nil, nil
	case OpLog:
		return "git", []string{"log", "--oneline", "--graph", "--decorate", "-n", "200"}, nil, nil
	case OpWorktreeList:
		return "git", []string{"worktree", "list"}, nil, nil
	case OpAdd:
		return "git", []string{"add", "-A"}, nil, nil
	case OpPush:
		return "git", []string{"push"}, nil, nil
	case OpCommit:
		if !gitdiff.HasChanges(dir) {
			return "", nil, nil, fmt.Errorf("nothing to commit")
		}
		if editor != "" {
			env = []string{"GIT_EDITOR=" + editor}
		}
		// Stage everything, then open the commit in the editor — the editor runs in
		// the panel's PTY, so vim / nano work as they would in a terminal.
		return "sh", []string{"-c", "git add -A && git commit"}, env, nil
	case OpBranch:
		b := strings.TrimSpace(arg)
		if b == "" {
			return "", nil, nil, fmt.Errorf("a new branch needs a name")
		}
		return "git", []string{"checkout", "-b", b}, nil, nil
	case OpWorktreeAdd, OpWorktreeRemove:
		return "", nil, nil, fmt.Errorf("%q runs synchronously, not as a panel", op)
	default:
		return "", nil, nil, fmt.Errorf("unknown git op %q", op)
	}
}

// WorktreeAdd creates a worktree at path on a new branch off the repo at dir. It
// is the additive half of the isolation bridge — the caller spawns an agent in the
// returned path afterwards. git refuses an existing branch or a non-empty path, so
// no flag forces over existing work.
func WorktreeAdd(dir, branch, path string) error {
	if strings.TrimSpace(branch) == "" {
		return fmt.Errorf("a worktree needs a branch name")
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("a worktree needs a path")
	}
	if out, err := runGit(dir, "worktree", "add", "-b", branch, path); err != nil {
		return gitErr("worktree add", out, err)
	}
	return nil
}

// WorktreeRemove removes the worktree at path. It runs plain (no --force), so git
// refuses a worktree with uncommitted changes or a locked one — the safe default,
// surfaced to the caller as the error.
func WorktreeRemove(dir, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("worktree remove needs a path")
	}
	if out, err := runGit(dir, "worktree", "remove", path); err != nil {
		return gitErr("worktree remove", out, err)
	}
	return nil
}

// runGit runs `git args...` in dir under the package timeout, returning combined
// output so a helper can surface git's own message on failure.
func runGit(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// gitErr folds git's own stderr into the error so the cockpit shows why an op was
// refused (e.g. "fatal: '<branch>' is already checked out") rather than a bare exit
// code.
func gitErr(what string, out []byte, err error) error {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("%s: %s", what, lastLine(msg))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// lastLine returns the final non-empty line of git's output, the part that names
// the actual reason.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
