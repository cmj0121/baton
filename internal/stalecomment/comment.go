package stalecomment

import (
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// commentLine is one line of comment text on a line the range added.
type commentLine struct {
	File string
	Line int
	Text string
}

var hasLetter = regexp.MustCompile(`[A-Za-z]`)

// collectComments returns the comment text on every line the range added, in
// the order the diff presented the files.
//
// go/scanner does the comment/code split that the awk version hand-rolled, and
// does it by the language's own rules: a "//" inside a string literal is not a
// comment and a quote inside a comment does not open a string. A block comment
// is one token spanning several lines, so its text is redistributed back over
// the lines it covers and filtered against the added set line by line.
func collectComments(root string, added addedLines) []commentLine {
	var out []commentLine
	for _, rel := range added.files {
		abs := filepath.Join(root, rel)
		src, err := os.ReadFile(abs)
		if err != nil {
			// A path the diff names but the tree does not have: a rename away,
			// or a path git quoted in the "+++" header because it holds a
			// space. Nothing to read, and the raw-diff cross-check below is
			// what notices if that swallowed the whole range.
			continue
		}
		out = append(out, scanFile(rel, src, added.lines[rel])...)
	}
	return out
}

// scanFile pulls the comment text on the wanted lines of one file. Several
// comments on the same line are joined, so a line is one record however it was
// written -- the awk version emitted one comment field per input line and the
// counts are compared against it.
func scanFile(rel string, src []byte, want map[int]bool) []commentLine {
	fset := token.NewFileSet()
	f := fset.AddFile(rel, fset.Base(), len(src))

	var s scanner.Scanner
	// Errors are dropped rather than raised: a file that does not tokenise
	// cleanly still yields its comments, and buildIndex has already refused the
	// whole run if a tracked .go file does not parse.
	s.Init(f, src, func(token.Position, string) {}, scanner.ScanComments)

	byLine := map[int]string{}
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.COMMENT {
			continue
		}
		start := f.Position(pos).Line
		for i, text := range strings.Split(lit, "\n") {
			line := start + i
			if !want[line] {
				continue
			}
			text = strings.TrimSpace(stripMarkers(text))
			if text == "" {
				continue
			}
			if prev := byLine[line]; prev != "" {
				byLine[line] = prev + " " + text
			} else {
				byLine[line] = text
			}
		}
	}

	nums := make([]int, 0, len(byLine))
	for line := range byLine {
		nums = append(nums, line)
	}
	sort.Ints(nums)

	out := make([]commentLine, 0, len(nums))
	for _, line := range nums {
		text := byLine[line]
		// A line of pure punctuation carries no name and is not evidence of
		// anything, and the awk version did not count it either.
		if !hasLetter.MatchString(text) {
			continue
		}
		out = append(out, commentLine{File: rel, Line: line, Text: text})
	}
	return out
}

// stripMarkers drops the comment delimiters so the reported text reads the way
// the comment does.
func stripMarkers(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "//")
	s = strings.TrimPrefix(s, "/*")
	s = strings.TrimSuffix(s, "*/")
	return s
}

// ---------------------------------------------------------------------------
// What counts as a code-shaped name.
//
// The symbol table on the other side of this comparison got real, but this side
// did not and cannot: a comment is prose, and English is full of tokens a naive
// identifier pattern accepts -- every capitalised word that opens a sentence,
// every acronym. So a bare word is never code-shaped, however it is capitalised.
// A name qualifies only on evidence no English word carries: an underscore, or a
// case change past the first letter.
//
//	explainLocked  yes -- lower-to-upper inside the word
//	parseURL       yes
//	max_retries    yes -- underscore
//	Panel, The     no  -- a capitalised word is a sentence opening
//	TODO, API, ID  no  -- an acronym has no case change
//	NaNs, IDs, PRs no  -- acronym plurals; English
//
// ---------------------------------------------------------------------------
var (
	identOnly     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	acronymPlural = regexp.MustCompile(`^[A-Z]+s$`)
	capitalWord   = regexp.MustCompile(`^[A-Z][a-z]*$`)
	notIdentChar  = regexp.MustCompile(`[^A-Za-z0-9_]+`)
	hasUpper      = regexp.MustCompile(`[A-Z]`)
	hasLower      = regexp.MustCompile(`[a-z]`)
	hasAlnum      = regexp.MustCompile(`[A-Za-z0-9]`)
)

func shaped(tok string) bool {
	if !identOnly.MatchString(tok) {
		return false
	}
	if acronymPlural.MatchString(tok) {
		return false
	}
	// The alphanumeric test is not redundant: a bare "_" is a legal Go
	// identifier and appears in prose as a rule, an underline, or the blank it
	// is in the language, and it names nothing.
	if strings.Contains(tok, "_") && hasAlnum.MatchString(tok) {
		return true
	}
	return hasUpper.MatchString(tok) && hasLower.MatchString(tok) && !capitalWord.MatchString(tok)
}

// shapedNames returns the distinct code-shaped names in the comment text,
// sorted.
func shapedNames(comments []commentLine) []string {
	seen := map[string]bool{}
	for _, c := range comments {
		for _, tok := range notIdentChar.Split(c.Text, -1) {
			if shaped(tok) {
				seen[tok] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
