package stalecomment

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The false positives this rewrite exists to kill.
//
// Each case is one name the awk version reported over the last four hundred
// commits, in the shape that produced it. They are the whole argument for a
// symbol table, so they are asserted end to end rather than through the index's
// internals: a fixture repository, a comment naming the thing, and a green run.
// ---------------------------------------------------------------------------

// goModule makes the fixture a module, so `go list` can resolve its imports.
func (f *fixture) goModule() {
	f.write("go.mod", "module example.com/fixture\n\ngo 1.26\n")
}

// sweepAdding commits src as a second revision and sweeps the one commit.
func (f *fixture) sweepAdding(baseline, src string) (int, string) {
	f.t.Helper()
	f.write("a.go", baseline)
	f.commit("baseline")
	f.write("a.go", src)
	f.commit("the comment under test")
	return f.sweep("HEAD~1")
}

func TestAnImportedFieldIsNotStale(t *testing.T) {
	f := newRepo(t)
	f.goModule()
	code, out := f.sweepAdding("package a\n", `package a

import "os/exec"

// ExtraFiles is the one thing this tree never sets on a Cmd: a panel inherits
// no descriptor above its PTY.
func run() *exec.Cmd { return exec.Command("true") }
`)
	if code != ExitOK {
		t.Fatalf("exit %d, wanted %d -- ExtraFiles is a real os/exec field\n%s", code, ExitOK, out)
	}
}

func TestAnImportedConstantIsNotStale(t *testing.T) {
	f := newRepo(t)
	f.goModule()
	code, out := f.sweepAdding("package a\n", `package a

import "math"

// The cap is MaxInt/2, because doubling it downstream must not overflow.
func cap() int { return math.MaxInt / 2 }
`)
	if code != ExitOK {
		t.Fatalf("exit %d, wanted %d -- MaxInt is a real math constant\n%s", code, ExitOK, out)
	}
}

func TestAnEnglishInflectionOfAnIndexedNameIsNotStale(t *testing.T) {
	f := newRepo(t)
	f.goModule()
	code, out := f.sweepAdding("package a\n", `package a

import "syscall"

// A SIGKILLed daemon never runs its own cleanup, so nothing removes the file.
func sig() syscall.Signal { return syscall.SIGKILL }
`)
	if code != ExitOK {
		t.Fatalf("exit %d, wanted %d -- SIGKILLed is prose about SIGKILL\n%s", code, ExitOK, out)
	}
}

func TestANameInsideAStringLiteralIsNotStale(t *testing.T) {
	f := newRepo(t)
	code, out := f.sweepAdding("package a\n", `package a

// prompt_context.json carries prompts and no accounting at all, and 4GiB is the
// largest quantity the parser accepts.
var files = []string{"prompt_context.json", "4GiB"}
`)
	if code != ExitOK {
		t.Fatalf("exit %d, wanted %d -- both names are spelled in a literal below\n%s", code, ExitOK, out)
	}
}

func TestAGoTestPatternIsNotStale(t *testing.T) {
	f := newRepo(t)
	code, out := f.sweepAdding("package a\n", `package a

// Measured with: go test -run XXX -bench WriteSink -benchtime 300x
func BenchmarkWriteSinkPlain() {}
`)
	if code != ExitOK {
		t.Fatalf("exit %d, wanted %d -- -bench WriteSink selects BenchmarkWriteSinkPlain\n%s", code, ExitOK, out)
	}
}

// The counterweight to the four above: each concession has to leave the sweep
// able to fail, or it has bought precision with the whole point of the tool.
func TestTheConcessionsStillLeaveItAbleToFail(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			// An inflection whose stem is nowhere is still stale.
			name: "an inflection of nothing",
			src:  "package a\n\n// The vanishedHELPERs are gone.\nfunc b() {}\n",
			want: "vanishedHELPERs",
		},
		{
			// A stem ending lowercase is never stripped, because "Locked"
			// beside a "Lock" is the rename this sweep exists to catch.
			name: "a lowercase stem is not stripped",
			src:  "package a\n\n// explainLocked is gone.\nfunc explainLock() {}\n",
			want: "explainLocked",
		},
		{
			// The test-pattern rule matches a prefix of a test function, not
			// any substring of one.
			name: "a name that is not a test prefix",
			src:  "package a\n\n// See SinkWrite for the rest.\nfunc BenchmarkWriteSinkPlain() {}\n",
			want: "SinkWrite",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRepo(t)
			code, out := f.sweepAdding("package a\n", tc.src)
			if code != ExitStale {
				t.Fatalf("exit %d, wanted %d\n%s", code, ExitStale, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("the report does not name %q\n%s", tc.want, out)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The index, in the small.
// ---------------------------------------------------------------------------

func TestIdentRunReadsNamesOutOfFreeText(t *testing.T) {
	cases := map[string][]string{
		"4GiB":                {"GiB"},
		"prompt_context.json": {"prompt_context", "json"},
		"os.O_CREATE":         {"os", "O_CREATE"},
		"1234":                nil,
	}
	for in, want := range cases {
		got := identRun.FindAllString(in, -1)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q -> %v, wanted %v", in, got, want)
		}
	}
}

func TestTheIndexNeverHoldsTheBlank(t *testing.T) {
	idx := newNameIndex()
	idx.add("_")
	idx.add("")
	if idx.Len() != 0 {
		t.Fatalf("the index holds %d names, wanted none", idx.Len())
	}
}

// Only exported names come out of an imported package, because only those can
// be named from outside it. An unexported local in os/exec must not vouch for
// an unexported name this tree renamed away.
func TestOnlyExportedNamesComeOutOfADependency(t *testing.T) {
	src := `package dep

type Cmd struct {
	ExtraFiles []int
	hidden     int
}

func Run(localHelper int) {}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dep.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	idx := newNameIndex()
	idx.addExported(file)

	for _, name := range []string{"Cmd", "ExtraFiles", "Run"} {
		if !idx.names[name] {
			t.Errorf("%q is exported and should be indexed", name)
		}
	}
	for _, name := range []string{"hidden", "localHelper", "dep"} {
		if idx.names[name] {
			t.Errorf("%q is not exported and must not vouch for anything", name)
		}
	}
}

func TestIsTestdata(t *testing.T) {
	yes := []string{"testdata/a.go", "internal/tui/testdata/x/a.go"}
	no := []string{"internal/testdataish/a.go", "a/testdata.go", "internal/tui/a.go"}
	for _, p := range yes {
		if !isTestdata(p) {
			t.Errorf("%q should be testdata", p)
		}
	}
	for _, p := range no {
		if isTestdata(p) {
			t.Errorf("%q should not be testdata", p)
		}
	}
}
