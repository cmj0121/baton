package scrub

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPackageImportsNothingButStdlib is the one structural claim this package
// makes, and it is the claim everything else rests on.
//
// internal/score is stdlib-only, which is precisely why it could not import the
// filter from internal/server and copied it instead (#47). This package is the
// answer only for as long as depending on it costs score nothing. One import of
// internal/paths or internal/config here and score's rule is broken through a
// package it does not read, at a distance nobody would connect — and the fix
// would be to copy the filter back, which is the bug this package exists to end.
//
// The package doc says "keep it that way". This is what makes that a check
// rather than a hope: a domain-shaped path (anything with a dot before its first
// slash) fails, by file and by name.
//
// Only the non-test files are held to it. The contract is about what SHIPS, and
// this file itself imports go/parser to enforce it.
func TestPackageImportsNothingButStdlib(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list the package's files: %v", err)
	}
	fset := token.NewFileSet()
	seen := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		seen++
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote %s: %v", name, imp.Path.Value, err)
			}
			if head, _, _ := strings.Cut(path, "/"); strings.Contains(head, ".") {
				t.Errorf("%s imports %q — this package must stay stdlib-only, or internal/score has to copy the filter back", name, path)
			}
		}
	}
	if seen == 0 {
		t.Fatal("found no non-test files, so this test is checking nothing")
	}
}
