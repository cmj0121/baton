package stalecomment

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Main runs the sweep and returns the process exit code.
//
// args is the command line after the program name; the first is an optional base
// revision, which the BASE environment variable also supplies.
func Main(args []string, stdout, stderr io.Writer) int {
	base := os.Getenv("BASE")
	if len(args) > 0 && args[0] != "" {
		base = args[0]
	}

	cwd, err := os.Getwd()
	if err != nil {
		return unchecked(stdout, fmt.Sprintf("the working directory is unreadable: %v", err))
	}
	root, err := repo{dir: cwd}.runGit("rev-parse", "--show-toplevel")
	if err != nil {
		return unchecked(stdout, "this is not a git working tree")
	}

	return Run(strings.TrimSpace(root), base, stdout, stderr)
}

// unchecked reports that the sweep could not look, which is a failure and never
// a pass. The version of this sweep that was thrown away exited 0 on an empty
// range: a check that cannot distinguish "I looked and found nothing" from "I
// never looked" reports a green light for work it never read.
func unchecked(w io.Writer, reason string) int {
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintf(w, "!! NOTHING CHECKED: %s\n", reason)
	_, _ = fmt.Fprintln(w, "   This is a failure, not a pass. The sweep did not examine any")
	_, _ = fmt.Fprintln(w, "   comment line, so it cannot vouch for this range.")
	return ExitUnchecked
}

// Run sweeps the comments HEAD adds over base in the working tree rooted at
// root, and writes the report.
func Run(root, base string, stdout, stderr io.Writer) int {
	r := repo{dir: root}

	baseSHA, err := r.resolveBase(base)
	if err != nil {
		if base != "" {
			return unchecked(stdout, fmt.Sprintf("base revision %q does not resolve to a commit", base))
		}
		return unchecked(stdout, "no base revision could be resolved (is this a shallow or single-commit clone?)")
	}
	headSHA, err := r.revParse("HEAD")
	if err != nil {
		return unchecked(stdout, "HEAD does not resolve to a commit")
	}
	if baseSHA == headSHA {
		return unchecked(stdout, fmt.Sprintf("the range %s..HEAD is empty (base and HEAD are the same commit)", short(base, baseSHA)))
	}

	_, _ = fmt.Fprintf(stdout, ">> sweeping comments added by %s..%s\n", baseSHA[:12], headSHA[:12])

	idx, err := buildIndex(r)
	if err != nil {
		// This one refusal replaces the awk version's `package`/`func` probe.
		// That probe asked whether the index looked like it came from Go at all,
		// because a hand-rolled lexer that routed code into the comment stream
		// left an empty index and no other trace. A parser cannot fail that way
		// quietly: either the file parses, or it names the file and the position
		// where it stopped. The probe's second half -- an index that came back
		// empty across parsed files -- is not checked, because a parsed Go file
		// always yields at least its own package name.
		return unchecked(stdout, fmt.Sprintf("a tracked .go file could not be read as Go, so no index could be built: %v", err))
	}
	if idx.ImportErr != nil {
		_, _ = fmt.Fprintf(stderr, ">> warning: the imported packages could not be listed (%v);\n", idx.ImportErr)
		_, _ = fmt.Fprintln(stderr, "   names from outside this module will read as stale.")
	}
	_, _ = fmt.Fprintf(stdout, ">> indexed %d names across %d tracked .go files and %d imported package(s)\n",
		idx.Len(), idx.GoFiles, idx.ImportedPkgs)

	diff, err := r.runGit("diff", "--unified=0", baseSHA, "--", "*.go")
	if err != nil {
		return unchecked(stdout, "git diff failed, so the range could not be read")
	}
	added, rawComments := parseDiff(diff)
	comments := collectComments(r.dir, added)

	if len(comments) == 0 && rawComments > 0 {
		return unchecked(stdout, fmt.Sprintf("the diff adds %d comment line(s) but the sweep examined 0 of them", rawComments))
	}
	if len(comments) == 0 {
		_, _ = fmt.Fprintln(stdout, "")
		_, _ = fmt.Fprintln(stdout, ">> the range adds no comment lines to any .go file, confirmed by an")
		_, _ = fmt.Fprintln(stdout, "   independent count of the raw diff. There is nothing here to sweep.")
		return ExitOK
	}

	names := shapedNames(comments)
	var stale []string
	for _, name := range names {
		if !idx.Has(name) {
			stale = append(stale, name)
		}
	}

	_, _ = fmt.Fprintf(stdout, ">> examined %d added comment line(s), %d distinct code-shaped name(s)\n", len(comments), len(names))

	if len(stale) == 0 {
		_, _ = fmt.Fprintln(stdout, "")
		_, _ = fmt.Fprintln(stdout, ">> every name in the added comments still exists in code.")
		return ExitOK
	}

	_, _ = fmt.Fprintln(stdout, "")
	_, _ = fmt.Fprintf(stdout, "!! %d name(s) appear in added comments but nowhere in the tree's code:\n", len(stale))
	_, _ = fmt.Fprintln(stdout, "------------------------------------------------------------")
	for _, name := range stale {
		_, _ = fmt.Fprintf(stdout, "  %s\n", name)
		for _, ev := range evidence(comments, name, 3) {
			_, _ = fmt.Fprintf(stdout, "      %s:%d  %s\n", ev.File, ev.Line, ev.Text)
		}
	}
	_, _ = fmt.Fprintln(stdout, "------------------------------------------------------------")
	_, _ = fmt.Fprintln(stdout, "   Each is a name the comment claims exists and the code does not have.")
	_, _ = fmt.Fprintln(stdout, "   Usually a rename the comment beside it did not follow. If the name is")
	_, _ = fmt.Fprintln(stdout, "   prose rather than an identifier, reword it -- the comment reads as a")
	_, _ = fmt.Fprintln(stdout, "   reference to code either way, which is the same problem in miniature.")
	return ExitStale
}

// evidence returns up to n comment lines carrying the name as a whole word, in
// the order the diff presented them -- the order the evidence arrived in, not
// the order a locale would sort it.
func evidence(comments []commentLine, name string, n int) []commentLine {
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	var out []commentLine
	for _, c := range comments {
		if word.MatchString(c.Text) {
			out = append(out, c)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// short names the base the way the caller asked for it, so the message reads
// back what was typed rather than a SHA the caller never saw.
func short(asked, sha string) string {
	if asked != "" {
		return asked
	}
	return sha[:12]
}
