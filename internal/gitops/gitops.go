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
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/gitdiff"
)

// gitTimeout bounds the synchronous helpers (worktree add/remove). The resolved
// commands themselves run as long-lived panels, not under this timeout.
const gitTimeout = 10 * time.Second

// captureTimeout bounds a captured op (Capture). It is looser than gitTimeout
// because push reaches the network; a slow or hung remote still cannot wedge the
// caller past this.
const captureTimeout = 30 * time.Second

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
		if err := ValidateBranch(b); err != nil {
			return "", nil, nil, err
		}
		return "git", []string{"checkout", "-b", b}, nil, nil
	case OpWorktreeAdd, OpWorktreeRemove:
		return "", nil, nil, fmt.Errorf("%q runs synchronously, not as a panel", op)
	default:
		return "", nil, nil, fmt.Errorf("unknown git op %q", op)
	}
}

// NeedsPTY reports whether an op must run in an interactive PTY panel rather than
// being captured to text: only commit, whose editor needs a terminal. Every other
// resolvable op (status/log/add/push/branch/worktree-list) is non-interactive, so
// the server captures its output for a popup. The worktree mutators run
// synchronously elsewhere and never reach here.
func NeedsPTY(op Op) bool { return op == OpCommit }

// CaptureResult is the outcome of a captured op: git's combined output text and
// whether it exited non-zero. A non-zero exit is not an error here — the output
// (git's own message, e.g. a push rejection) is exactly what the popup should
// show — so Failed flags it instead, leaving errors for pre-flight failures.
type CaptureResult struct {
	Output string // git's combined stdout+stderr
	Failed bool   // git exited non-zero; Output carries its message
}

// Capture runs a non-interactive output op in dir and returns its combined output
// as text — the popup counterpart to Resolve+spawn. It refuses the interactive op
// (commit) and any op Resolve rejects, returning those as errors with nothing to
// show. The command runs with GIT_TERMINAL_PROMPT=0 so a push that would prompt for
// credentials fails fast rather than hanging a capture that has no terminal, and
// under captureTimeout so a slow remote cannot block forever. A non-zero exit is
// reported via CaptureResult.Failed, not an error, so the caller still shows git's
// message.
func Capture(op Op, dir, arg, editor string) (CaptureResult, error) {
	if NeedsPTY(op) {
		return CaptureResult{}, fmt.Errorf("%q needs an interactive terminal, not a capture", op)
	}
	name, args, env, err := Resolve(op, dir, arg, editor)
	if err != nil {
		return CaptureResult{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), env...), "GIT_TERMINAL_PROMPT=0")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf

	if runErr := cmd.Run(); runErr != nil {
		out := buf.String()
		if strings.TrimSpace(out) == "" { // a failure with no output of its own (e.g. a timeout)
			out = runErr.Error()
		}
		return CaptureResult{Output: out, Failed: true}, nil
	}
	return CaptureResult{Output: buf.String()}, nil
}

// WorktreeAdd creates a worktree at path on a new branch off the repo at dir. It
// is the additive half of the isolation bridge — the caller spawns an agent in the
// returned path afterwards. git refuses an existing branch or a non-empty path, so
// no flag forces over existing work.
func WorktreeAdd(dir, branch, path string) error {
	if err := ValidateBranch(strings.TrimSpace(branch)); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("a worktree needs a path")
	}
	// End-of-options guard so a path that somehow begins with "-" is never read as
	// a flag; the branch is already validated, and "--" fences the positional path.
	if out, err := runGit(dir, "worktree", "add", "-b", branch, "--", path); err != nil {
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
	// "--" fences the positional path so a value beginning with "-" cannot be read
	// as a flag (e.g. slip in --force against the plain, safe default).
	if out, err := runGit(dir, "worktree", "remove", "--", path); err != nil {
		return gitErr("worktree remove", out, err)
	}
	return nil
}

// ValidateBranch rejects a branch name that git would refuse or that could be
// misread as a command-line flag. The name reaches git as an argument to
// `checkout -b` / `worktree add -b`, so a value beginning with "-" is the real
// concern — it would be parsed as an option rather than a branch (argument
// injection) — but the same pass also enforces git's check-ref-format rules, so
// an invalid name fails here with a clear message instead of a cryptic git error
// (or, worse, a surprising flag). It is deliberately conservative: it allows the
// ordinary "feature/foo-bar_1" shapes and refuses the rest.
func ValidateBranch(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("a new branch needs a name")
	case strings.HasPrefix(name, "-"):
		// The security-critical rule: a leading "-" makes the name look like a flag.
		return fmt.Errorf("invalid branch name %q: cannot start with '-'", name)
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//"):
		return fmt.Errorf("invalid branch name %q: bad use of '/'", name)
	case strings.Contains(name, ".."), strings.Contains(name, "@{"), strings.HasSuffix(name, ".lock"),
		strings.HasSuffix(name, "."), name == "@":
		return fmt.Errorf("invalid branch name %q: rejected by git's ref-format rules", name)
	}
	// Forbid whitespace, ASCII control bytes, and the metacharacters git's
	// check-ref-format bars (~ ^ : ? * [ \ and DEL).
	for _, r := range name {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return fmt.Errorf("invalid branch name %q: contains a forbidden character", name)
		}
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
