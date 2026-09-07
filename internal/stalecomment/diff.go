package stalecomment

import (
	"regexp"
	"strconv"
	"strings"
)

// addedLines is the set of line numbers a range adds to one file, in the order
// the diff presented the files.
type addedLines struct {
	files []string                // first appearance order, as git emitted it
	lines map[string]map[int]bool // path -> added line numbers
}

func (a *addedLines) add(file string, line int) {
	if a.lines == nil {
		a.lines = map[string]map[int]bool{}
	}
	if _, seen := a.lines[file]; !seen {
		a.lines[file] = map[int]bool{}
		a.files = append(a.files, file)
	}
	a.lines[file][line] = true
}

var hunkHeader = regexp.MustCompile(`\+([0-9]+)(?:,([0-9]+))?`)

// rawCommentLine is the independent count of what there was to check: a dumb
// pattern over the raw diff that shares no code with the scanner. If the scanner
// reports zero examined comment lines while this says there were some to
// examine, the sweep is broken, and saying so is the only honest thing left.
var rawCommentLine = regexp.MustCompile(`^\+.*(//|/\*)`)

// parseDiff reads a `git diff --unified=0` and returns the lines it adds plus
// the independent raw count of added lines that look like comments.
//
// Line numbers come out of the hunk headers: "@@ -old,n +new,m @@" means m lines
// starting at new. Which file a hunk belongs to is the "+++ b/<path>" header
// above it -- and that header is only read while inside one, because an added
// line inside a block comment can itself begin "++" and arrive looking exactly
// like the header.
func parseDiff(diff string) (added addedLines, rawComments int) {
	file := ""
	inHeader := false

	for _, line := range splitLines(diff) {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, inHeader = "", true
		case inHeader && strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(line[4:], "b/")
			inHeader = false
		case strings.HasPrefix(line, "@@"):
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil || file == "" {
				continue
			}
			start, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			count := 1
			if m[2] != "" {
				if count, err = strconv.Atoi(m[2]); err != nil {
					continue
				}
			}
			for k := 0; k < count; k++ {
				added.add(file, start+k)
			}
		}
		if rawCommentLine.MatchString(line) {
			rawComments++
		}
	}
	return added, rawComments
}
