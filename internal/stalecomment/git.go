// Package stalecomment finds identifiers that live only in comments.
//
// A rename or a deletion leaves the old name sitting in the comment beside it.
// The compiler never sees a comment, so nothing catches that but a reader. This
// sweep does the mechanical half: it walks every comment line a range *adds* to
// a .go file, pulls out every code-shaped name, and asks whether that name still
// appears anywhere the code can reach -- the tree's own identifiers, the
// exported surface of the packages it imports, its string literals, its sibling
// proto/yaml sources, and its filenames. A name that survives only in prose is
// the residue of an edit that moved on without it.
//
// It needs no list of what was renamed. That is the point: it does not care
// whether the rename was deliberate, and it has no way to be told the wrong
// answer by an out-of-date list.
//
// What it does NOT catch, so that nobody reads a green run as more than it is:
// a comment that enumerates cases and goes stale when a case is added, and a
// comment that states an arithmetic result and goes stale when the function
// changes. Neither involves an identifier, so no sweep can see either. The only
// form of those claims that survives the next edit is an assertion in a test.
package stalecomment

import (
	"bytes"
	"os/exec"
	"strings"
)

// Exit codes. Exit 2 is the whole reason this sweep is worth having: a check
// that cannot distinguish "I looked and found nothing" from "I never looked"
// reports a green light for work it never read. Every path that cannot honestly
// claim to have examined something returns ExitUnchecked instead of ExitOK.
const (
	ExitOK        = 0 // comment lines were examined and every name in them exists
	ExitStale     = 1 // a name was found that exists in comments but not in code
	ExitUnchecked = 2 // nothing was checked -- a failure, never a pass
)

// repo runs git inside one working tree.
type repo struct{ dir string }

// runGit runs a git command and returns its stdout. stderr is dropped: every
// caller here already treats a non-zero status as the whole answer.
func (r repo) runGit(args ...string) (string, error) {
	out, err := r.runGitBytes(args...)
	return string(out), err
}

func (r repo) runGitBytes(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// splitLines splits output that is one record per line, dropping the trailing
// empty field.
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// splitZ splits git's -z output, which is NUL-terminated rather than
// NUL-separated, so the trailing field is always empty.
func splitZ(b []byte) []string {
	var out []string
	for _, f := range bytes.Split(b, []byte{0}) {
		if len(f) > 0 {
			out = append(out, string(f))
		}
	}
	return out
}

// baseCandidates are the refs a default base is looked for among, in no
// significant order -- the nearest wins, not the first.
var baseCandidates = []string{"origin/main", "main", "GITHUB/main", "upstream/main"}

// resolveBase picks the revision the sweep compares HEAD against.
//
// With no explicit base it takes the NEAREST merge-base among the candidates
// rather than the first that resolves: a remote-tracking ref can be stale, or
// belong to a mirror nobody pushes to, and taking it on faith sweeps every
// commit since that mirror last moved. In this repo `origin` is an unreachable
// gitea and origin/main sat 130 commits behind `main`, so first-match swept a
// whole release's worth of history and reported eight names from work that had
// nothing to do with the branch.
//
// Nearest is the right rule in both directions: a stale local `main` loses to a
// fresher remote just as a stale remote loses to a fresher local.
func (r repo) resolveBase(explicit string) (string, error) {
	if explicit != "" {
		return r.revParse(explicit)
	}

	head, err := r.revParse("HEAD")
	if err != nil {
		return "", err
	}

	base := ""
	for _, candidate := range baseCandidates {
		if _, err := r.revParse(candidate); err != nil {
			continue
		}
		out, err := r.runGit("merge-base", candidate, "HEAD")
		if err != nil {
			continue
		}
		mb := strings.TrimSpace(out)
		if mb == "" {
			continue
		}
		if base == "" || r.isAncestor(base, mb) {
			base = mb
		}
	}
	if base == "" || base == head {
		// The candidate is HEAD itself, which leaves nothing to compare, so
		// fall back to the commit before it.
		return r.revParse("HEAD~1")
	}
	return base, nil
}

// revParse resolves a revision to a full commit SHA, or fails.
func (r repo) revParse(rev string) (string, error) {
	out, err := r.runGit("rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", errNoSuchRev
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", errNoSuchRev
	}
	return sha, nil
}

func (r repo) isAncestor(ancestor, descendant string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = r.dir
	return cmd.Run() == nil
}

type revError string

func (e revError) Error() string { return string(e) }

const errNoSuchRev = revError("revision does not resolve to a commit")
