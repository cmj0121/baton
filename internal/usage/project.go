package usage

import (
	"os"
	"path/filepath"
	"strings"
)

// Project labels and how they fold.
//
// A project here is a main checkout. Measured on one machine, most of the
// directories an agent CLI keeps logs under are not one: they are worktrees the
// CLI or baton made, scratch checkouts under a temp root, and each of those is a
// separate log directory. Listed as they are, the per-project table is mostly
// noise about where a session happened to run rather than which work it served.
// So a label is folded onto the checkout it belongs to, by string rules alone —
// no git, because the worktree may be long deleted by the time its spend is read,
// and a rule that needs the directory to exist would fold a live worktree and
// leave a deleted one standing.

const (
	// UnattributedProject keys the spend whose log sits outside the reader's
	// <root>/<project>/<session> layout. It is kept rather than dropped so the
	// projects still sum to the totals.
	UnattributedProject = "(unattributed)"

	// TemporaryProject is every project under a temp root, folded into one row.
	// Scratch checkouts are named at random and never come back, so a row each
	// would be a list of names nobody can act on.
	TemporaryProject = "(temporary)"

	// unresolvedPrefix marks a project directory no log stated a path for. The
	// directory's own name follows it undecoded (see LabelProjects).
	unresolvedPrefix = "(unresolved) "
)

// ProjectHint is what one scan learned of a project directory's path.
//
// Exact says the path is certain: grok's directory name, which unescapes
// losslessly, or a Claude cwd whose encoding is the directory's name. An inexact
// path is the first cwd the scan happened to see, which may be a subdirectory the
// session had wandered into. An empty Path means nothing was learned.
type ProjectHint struct {
	Path  string
	Exact bool
}

// ProjectKey names one project directory across vendors. The vendor is part of
// the key because two vendors name directories in two encodings, and the same
// string under each need not be the same project.
type ProjectKey struct {
	Vendor string
	Dir    string // the raw directory, or UnattributedProject
}

// LabelProjects names every project directory it is given, and folds each onto
// its main checkout. It is the one place a label is made, and it is pure: the
// same directories with the same hints get the same labels whatever order they
// arrive in, and whichever scans they came from.
//
// A key seen by several scans may carry several hints. An exact one beats an
// inexact one, and between equals the lexically smallest path wins — an order a
// caller cannot perturb, unlike "the first seen". A key with no path at all is
// labelled "(unresolved) <dir>": a Claude directory name has lost every "/" and
// "." it had, and a fabricated path would read as a project the operator does
// not have.
//
// The known set the grok-worktree rule resolves against is every label here,
// each folded on its own first, across every vendor: a grok worktree of a repo
// only Claude has worked in still joins it, and a worktree resolves to a main
// checkout, never to another worktree.
func LabelProjects(hints map[ProjectKey][]ProjectHint) map[ProjectKey]string {
	raw := make(map[ProjectKey]string, len(hints))
	for k, hs := range hints {
		raw[k] = rawLabel(k, hs)
	}
	known := make([]string, 0, len(raw))
	for _, l := range raw {
		known = append(known, FoldProject(l, nil))
	}
	out := make(map[ProjectKey]string, len(raw))
	for k, l := range raw {
		out[k] = FoldProject(l, known)
	}
	return out
}

// rawLabel is one directory's label before folding: its best hint, or the
// directory's own name marked unresolved.
func rawLabel(k ProjectKey, hs []ProjectHint) string {
	if k.Dir == UnattributedProject {
		return UnattributedProject
	}
	var best ProjectHint
	for _, h := range hs {
		switch {
		case h.Path == "": // nothing learned; any hint beats it
		case best.Path == "", h.Exact && !best.Exact:
			best = h
		case h.Exact == best.Exact && h.Path < best.Path:
			best = h
		}
	}
	if best.Path == "" {
		return unresolvedPrefix + k.Dir
	}
	return best.Path
}

// FoldProject folds a project path onto the main checkout it belongs to. known is
// the set of already-folded project paths the grok-worktree rule may resolve to;
// nil is fine and means no grok worktree resolves.
//
// A label that is not a path (unresolved, unattributed) matches no rule and comes
// back as it is. For a path, first match wins:
//
//   - under a temp root (os.TempDir(), /tmp, /private/tmp, /var/folders,
//     /private/var/folders) → TemporaryProject;
//   - $GROK_HOME/worktrees/<name>[/...] or ~/.grok/worktrees/<name>[/...] → the
//     one known project whose last two segments joined with "-" equal <name>,
//     else "grok worktree <name>". Two such projects (/a/my-lab/baton and
//     /b/my/lab-baton both give "my-lab-baton") are no answer: the worktree
//     stays unfolded rather than joining whichever the known set listed first;
//
// and otherwise both of these apply, in order:
//
//   - <repo>/.claude/worktrees/<x>[/...] → <repo> (Claude Code's worktrees);
//   - <repo>-worktrees/<leaf>[/...] → <repo> (baton's default worktree layout).
//
// Both apply because they nest: a Claude worktree inside a baton worktree is
// still the one repo's work.
func FoldProject(path string, known []string) string {
	path = filepath.Clean(path)
	for _, root := range tempRoots() {
		if under(path, root) {
			return TemporaryProject
		}
	}
	for _, root := range grokWorktreeRoots() {
		rest, ok := strings.CutPrefix(path, root+"/")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "/")
		match := ""
		for _, k := range known {
			if lastTwo(k) != name || k == match {
				continue
			}
			if match != "" {
				match = "" // ambiguous: no fold
				break
			}
			match = k
		}
		if match != "" {
			return match
		}
		return "grok worktree " + name
	}
	if repo, _, ok := strings.Cut(path, "/.claude/worktrees/"); ok && repo != "" {
		path = repo
	}
	parts := strings.Split(path, "/")
	for i, seg := range parts[:len(parts)-1] {
		if repo, ok := strings.CutSuffix(seg, "-worktrees"); ok && repo != "" {
			parts[i] = repo
			return strings.Join(parts[:i+1], "/")
		}
	}
	return path
}

// ShortenHome writes a path under $HOME as ~/..., for display only: the full path
// stays the key, because two users' home directories are not the same project.
func ShortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+"/"); ok {
		return "~/" + rest
	}
	return path
}

// tempRoots are the directories a scratch checkout lives under. /private/... is
// listed beside its /... twin because macOS resolves one to the other and a cwd
// can be stated either way.
func tempRoots() []string {
	return []string{
		filepath.Clean(os.TempDir()),
		"/tmp", "/private/tmp",
		"/var/folders", "/private/var/folders",
	}
}

// grokWorktreeRoots are where grok puts its worktrees: under $GROK_HOME when set,
// and under ~/.grok regardless, since a worktree made before the override was set
// is still where it was made.
func grokWorktreeRoots() []string {
	roots := []string{filepath.Join(grokHome(), "worktrees")}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if d := filepath.Join(home, ".grok", "worktrees"); d != roots[0] {
			roots = append(roots, d)
		}
	}
	return roots
}

// under reports whether path is root or inside it — by whole segments, so
// /tmpfoo is not under /tmp.
func under(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// lastTwo is a path's last two segments joined with "-", the form grok names a
// worktree after: /Users/me/mylab/baton → "mylab-baton".
func lastTwo(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "-" + parts[len(parts)-1]
}
