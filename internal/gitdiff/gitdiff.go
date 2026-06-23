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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// untrackedDiffCap bounds how much of an untracked file is rendered as an added
// diff, so a huge generated file (an agent's build output) cannot bloat the popup
// payload. Beyond it the body is truncated with a marker.
const untrackedDiffCap = 2000

// FileChange is one changed path in a work tree with the diff for each side.
// Index is the staged (index-side) status letter from `git status --porcelain`,
// Work the unstaged (work-tree) one; both "?" marks an untracked file. Staged and
// Unstaged hold that side's unified diff, each empty when the side is unchanged.
type FileChange struct {
	Path     string // repo-relative path (rename keeps the new path)
	Index    string // staged-side status: M, A, D, R, … or "" when unchanged
	Work     string // unstaged-side status, or "?" for an untracked file
	Staged   string // `git diff --cached` text for this file
	Unstaged string // `git diff` text for this file (untracked → whole file as added)
}

// Collect returns dir's changed files — tracked and untracked — each with its
// staged and unstaged unified diff, ordered as `git status --porcelain` reports
// them. It runs three git commands (status, diff, diff --cached) and splits the
// two diffs into per-file sections; an untracked file's content is rendered as an
// all-added diff so it shows up like the rest. An error is returned only when the
// status probe fails (e.g. dir is not a work tree).
func Collect(dir string) ([]FileChange, error) {
	status, err := runGit(dir, "-c", "core.quotepath=false", "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	unstaged, _ := runGit(dir, "-c", "core.quotepath=false", "diff")
	staged, _ := runGit(dir, "-c", "core.quotepath=false", "diff", "--cached")
	unstagedByFile := splitDiffByFile(string(unstaged))
	stagedByFile := splitDiffByFile(string(staged))

	var changes []FileChange
	for _, line := range strings.Split(strings.TrimRight(string(status), "\n"), "\n") {
		if len(line) < 4 { // "XY p" is the shortest valid entry
			continue
		}
		index := strings.TrimSpace(line[0:1])
		work := strings.TrimSpace(line[1:2])
		path := line[3:]
		if i := strings.Index(path, " -> "); i >= 0 { // a rename/copy: keep the new path
			path = path[i+4:]
		}
		fc := FileChange{Path: path, Index: index, Work: work}
		if index == "?" && work == "?" { // untracked: synthesize an added-file diff
			fc.Unstaged = renderUntracked(dir, path)
		} else {
			fc.Unstaged = unstagedByFile[path]
			fc.Staged = stagedByFile[path]
		}
		changes = append(changes, fc)
	}
	return changes, nil
}

// splitDiffByFile carves a unified diff into per-file sections keyed by the
// file's path. A section starts at each "diff --git" header and runs to the next;
// the path is read from the "+++ b/…" line (or the "--- a/…" line for a deletion,
// where +++ is /dev/null), falling back to the header's b-side.
func splitDiffByFile(diff string) map[string]string {
	out := make(map[string]string)
	if strings.TrimSpace(diff) == "" {
		return out
	}
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		if p := sectionPath(cur); p != "" {
			out[p] = strings.Join(cur, "\n") + "\n"
		}
		cur = nil
	}
	for _, line := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

// sectionPath extracts the file path from one diff section's lines.
func sectionPath(lines []string) string {
	var minus string
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+++ b/"):
			if p := l[len("+++ b/"):]; p != "" {
				return p // the +++ side is preferred and present
			}
		case strings.HasPrefix(l, "--- a/"):
			minus = l[len("--- a/"):]
		}
	}
	if minus != "" { // a deletion: +++ is /dev/null, so trust the --- side
		return minus
	}
	// Fallback: parse the header "diff --git a/<p> b/<p>" b-side.
	if len(lines) > 0 && strings.HasPrefix(lines[0], "diff --git ") {
		if i := strings.Index(lines[0], " b/"); i >= 0 {
			return lines[0][i+len(" b/"):]
		}
	}
	return ""
}

// renderUntracked renders an untracked file as an all-added unified-style diff so
// it reads like the tracked changes beside it. A binary or unreadable file, or one
// past untrackedDiffCap lines, is summarised rather than dumped in full.
func renderUntracked(dir, path string) string {
	data, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		return fmt.Sprintf("new file: %s\n(unreadable: %v)\n", path, err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return fmt.Sprintf("new file: %s\n(binary)\n", path)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "new file: %s\n", path)
	for i, ln := range lines {
		if i >= untrackedDiffCap {
			fmt.Fprintf(&b, "@@ … %d more line(s) truncated @@\n", len(lines)-i)
			break
		}
		b.WriteString("+" + ln + "\n")
	}
	return b.String()
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
