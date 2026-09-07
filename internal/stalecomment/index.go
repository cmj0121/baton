package stalecomment

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// identRun matches the way a name appears inside free text: the longest run of
// identifier characters that starts where an identifier may start. Running it
// over "4GiB" yields "GiB" and over "prompt_context.json" yields
// "prompt_context" and "json", which is the point -- a name embedded in a string
// literal or a filename still exists in the file, and calling it stale is a lie.
var identRun = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// nameIndex is every name the tree's code can reach.
type nameIndex struct {
	names     map[string]bool
	testFuncs []string // the subset `go test` would select by pattern

	GoFiles      int // tracked .go files parsed
	ImportedPkgs int // directly imported packages whose exported surface was read
	ImportErr    error
}

func newNameIndex() *nameIndex { return &nameIndex{names: map[string]bool{}} }

func (n *nameIndex) add(name string) {
	if name == "" || name == "_" || n.names[name] {
		return
	}
	n.names[name] = true
	if testFuncName.MatchString(name) {
		n.testFuncs = append(n.testFuncs, name)
	}
}

// addText harvests every identifier-shaped run out of free text.
func (n *nameIndex) addText(s string) {
	for _, m := range identRun.FindAllString(s, -1) {
		n.add(m)
	}
}

func (n *nameIndex) Len() int { return len(n.names) }

// englishSuffixes are the inflections prose puts on a name it is talking about
// rather than calling: "a SIGKILLed daemon", "two NaNs".
var englishSuffixes = []string{"ing", "es", "ed", "s", "d"}

// testPrefixes are the prefixes `go test -run` and `-bench` take off the front
// of a function name.
var testPrefixes = []string{"Test", "Benchmark", "Fuzz", "Example"}

// testFuncName matches a function `go test` would select.
var testFuncName = regexp.MustCompile(`^(Test|Benchmark|Fuzz|Example)[A-Z_]`)

// Has reports whether the code can reach this name.
//
// Three ways past a literal miss, each of them a shape the sweep was observed
// reporting wrongly, and none of them a general loosening:
//
//   - an English inflection on a stem that ends in a capital or a digit. The
//     boundary matters: "SIGKILLed" is prose about SIGKILL, while "Locked"
//     beside a "Lock" is exactly the rename this sweep exists to catch, so a
//     stem ending in lowercase is never stripped.
//   - a `go test -run`/`-bench` pattern, which is a function name minus its
//     prefix, and a pattern rather than a name: "-bench WriteSink" selects
//     BenchmarkWriteSinkPlain and BenchmarkWriteSinkDurable, so the prefix is
//     what has to exist, not the whole name.
func (n *nameIndex) Has(name string) bool {
	if n.names[name] {
		return true
	}
	for _, suffix := range englishSuffixes {
		stem, ok := strings.CutSuffix(name, suffix)
		if !ok || len(stem) < 2 {
			continue
		}
		last := rune(stem[len(stem)-1])
		if !unicode.IsUpper(last) && !unicode.IsDigit(last) {
			continue
		}
		if n.names[stem] {
			return true
		}
	}
	for _, prefix := range testPrefixes {
		want := prefix + name
		for _, fn := range n.testFuncs {
			if strings.HasPrefix(fn, want) {
				return true
			}
		}
	}
	return false
}

// isTestdata reports whether a path lies under a testdata directory, which the
// Go tool itself ignores and which may therefore hold Go that does not parse.
func isTestdata(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == "testdata" {
			return true
		}
	}
	return false
}

// buildIndex reads every name the tree's code contains.
//
// The comment/code split that the awk version hand-rolled -- block comments, raw
// strings, rune literals, escape handling, cross-line state -- is go/parser's
// job here, and its failures are loud: a .go file that does not parse returns an
// error rather than a silently truncated stream.
func buildIndex(r repo) (*nameIndex, error) {
	idx := newNameIndex()

	out, err := r.runGitBytes("ls-files", "-z", "*.go")
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	goFiles := splitZ(out)

	fset := token.NewFileSet()
	imports := map[string]bool{}
	for _, rel := range goFiles {
		if isTestdata(rel) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(r.dir, rel))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		file, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", rel, err)
		}
		idx.GoFiles++
		idx.harvest(file)
		for _, spec := range file.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			imports[p] = true
			// The package name a caller writes is the last path element unless
			// the import is aliased, and the alias is an Ident harvest already
			// took.
			idx.add(path.Base(p))
		}
	}

	idx.addImported(r, imports)

	// Non-Go tracked sources hold names too -- proto and yaml declare fields
	// that Go comments legitimately mention. Comments there are not stripped,
	// which costs recall and buys no false positives, and recall in a file
	// nobody renamed is not what this is for.
	if out, err := r.runGitBytes("ls-files", "-z", "*.proto", "*.yaml", "*.yml", "go.mod"); err == nil {
		for _, rel := range splitZ(out) {
			b, err := os.ReadFile(filepath.Join(r.dir, rel))
			if err != nil {
				continue
			}
			idx.addText(string(b))
		}
	}

	// Filenames, because a comment that says "mdheader_test.go covers #57" is
	// naming a file that exists, not an identifier that does not.
	if out, err := r.runGitBytes("ls-files", "-z"); err == nil {
		for _, rel := range splitZ(out) {
			base := filepath.Base(rel)
			idx.addText(strings.TrimSuffix(base, filepath.Ext(base)))
		}
	}

	return idx, nil
}

// harvest takes every identifier the file declares or uses, plus every name
// embedded in one of its string literals.
//
// String literals count as code on purpose, and for the same reason the awk
// version kept them in the code stream: a name that appears only in a string
// still exists in the file. It is how every environment variable in this tree
// ("BATON_SOCK", "XDG_RUNTIME_DIR") and every sibling filename a reader might
// mention ("prompt_context.json") is spelled.
func (n *nameIndex) harvest(file *ast.File) {
	ast.Inspect(file, func(node ast.Node) bool {
		switch v := node.(type) {
		case *ast.Ident:
			n.add(v.Name)
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return true
			}
			text := v.Value
			if unquoted, err := strconv.Unquote(text); err == nil {
				text = unquoted
			}
			n.addText(text)
		}
		return true
	})
}

// addImported reads the exported surface of every package the tree imports
// directly.
//
// This is the half the awk version could not have: `ExtraFiles` is a real field
// on os/exec.Cmd, and a comment saying this tree does not use it is correct
// prose about a name that exists. Only exported names are taken, because only
// those can be named from outside -- an unexported local in os/exec must not
// vouch for an unexported name this tree renamed away.
func (n *nameIndex) addImported(r repo, imports map[string]bool) {
	if len(imports) == 0 {
		return
	}
	paths := make([]string, 0, len(imports))
	for p := range imports {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	args := append([]string{"list", "-e", "-deps=false", "-f", "{{.Dir}}"}, paths...)
	cmd := exec.Command("go", args...)
	cmd.Dir = r.dir
	out, err := cmd.Output()
	if err != nil {
		// A precision loss, not a correctness one: a thinner index reports more
		// names, and every one of them is visible in the report. The sweep still
		// examined what it claims to have examined.
		n.ImportErr = err
		return
	}

	fset := token.NewFileSet()
	for _, dir := range splitLines(string(out)) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		read := false
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			src, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
			if err != nil {
				continue
			}
			read = true
			n.addExported(file)
		}
		if read {
			n.ImportedPkgs++
		}
	}
}

// addExported takes only the identifiers a caller outside the package could
// write: exported names, wherever they appear in the file.
func (n *nameIndex) addExported(file *ast.File) {
	ast.Inspect(file, func(node ast.Node) bool {
		id, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		if r := []rune(id.Name); len(r) > 0 && unicode.IsUpper(r[0]) {
			n.add(id.Name)
		}
		return true
	})
}
