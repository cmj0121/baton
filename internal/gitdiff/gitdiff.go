// Package gitdiff resolves "the diff of the current work" for a panel's workdir.
// It is a set of pure helpers over git: it decides whether a directory is a git
// work tree, whether it has uncommitted work (including untracked files), and
// which command shows the diff. It depends on nothing else in baton, so the
// server can call it without dragging the PTY layer into a unit test.
//
// Every git invocation runs under a short context timeout in the given dir, so a
// hung or pathological repo can never wedge the caller.
package gitdiff

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// gitTimeout bounds every git probe. The checks are cheap (rev-parse, status),
// so a couple of seconds is generous; the timeout exists only so a wedged git
// cannot stall the caller indefinitely.
const gitTimeout = 2 * time.Second

// builtinScript is the fallback diff: a NON-MUTATING, untracked-inclusive
// working-tree-vs-HEAD diff. It stages everything into a THROWAWAY index
// (GIT_INDEX_FILE points at a temp path), so it never touches the real index,
// worktree, or refs — `git add -A` only writes to the throwaway index, and the
// trap removes it on the way out. The base is HEAD, or the empty tree when the
// repo has no commit yet, so a brand-new repo still diffs cleanly. Staging the
// whole tree is what pulls untracked files into the diff (a plain `git diff
// HEAD` would miss them — and agents create new files constantly).
//
// The throwaway path is mktemp'd then immediately removed: git refuses to treat
// a zero-byte mktemp file as an index ("index file smaller than expected"), so
// we let git create a fresh one at that path. It is seeded with a copy of the
// real index when one exists, so a partially-staged tree diffs sensibly; a
// fresh repo (no index yet) starts from an empty one.
const builtinScript = `idx=$(mktemp "${TMPDIR:-/tmp}/baton-diff.XXXXXX") || exit 1
rm -f "$idx"
trap 'rm -f "$idx"' EXIT INT TERM
real="$(git rev-parse --git-dir)/index"
[ -f "$real" ] && cp "$real" "$idx"
export GIT_INDEX_FILE="$idx"
git add -A
base=$(git rev-parse -q --verify HEAD || git hash-object -t tree /dev/null)
git diff --cached "$base"`

// IsWorkTree reports whether dir is inside a git work tree. It runs
// `git rev-parse --is-inside-work-tree` in dir: true iff git exits 0 and prints
// "true" (a bare repo, or a non-repo, prints something else or fails).
func IsWorkTree(dir string) bool {
	out, err := runGit(dir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// HasChanges reports whether dir's work tree has any uncommitted work. It runs
// `git status --porcelain`: non-empty output means there is something to diff.
// Porcelain lists UNTRACKED files too — that is the point, since the most common
// agent output is a brand-new file a tracked-only check would miss.
func HasChanges(dir string) bool {
	out, err := runGit(dir, "status", "--porcelain")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// ResolveCommand picks the command that shows dir's diff, in priority order:
//
//  1. An explicit command (the user's `panel.diff-command`) wins: run it via
//     `sh -c` so the user can write a full shell line.
//  2. Else, if the repo configures a `diff.tool`, honour it with
//     `git difftool -d --no-prompt` — the per-repo choice the user already made.
//     `--no-prompt` stops difftool stalling on a [Y/n] inside the PTY. (This
//     branch keeps git's tracked-only difftool semantics; untracked inclusion is
//     a property of the built-in and explicit paths only.)
//  3. Else, the built-in non-mutating, untracked-inclusive diff (see
//     builtinScript), run via `sh -c`.
//
// It returns the executable name and its args, ready to spawn in dir.
func ResolveCommand(dir, explicit string) (name string, args []string) {
	if explicit != "" {
		return "sh", []string{"-c", explicit}
	}
	if out, err := runGit(dir, "config", "--get", "diff.tool"); err == nil {
		if strings.TrimSpace(string(out)) != "" {
			return "git", []string{"difftool", "-d", "--no-prompt"}
		}
	}
	return "sh", []string{"-c", builtinScript}
}

// runGit runs `git args...` in dir under the package timeout and returns its
// stdout. A non-zero exit (or the timeout firing) surfaces as a non-nil error.
func runGit(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}
