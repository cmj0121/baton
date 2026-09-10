package stalecomment

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A throwaway repository per case. The ambient git config must not reach them:
// a global template, a hook, or a configured default branch name would change
// what is committed, and the assertions would then be about the developer's
// machine rather than about the sweep.
// ---------------------------------------------------------------------------

type fixture struct {
	t    *testing.T
	root string
}

func newRepo(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "sweep-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "sweep@test")
	t.Setenv("GIT_COMMITTER_NAME", "sweep-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "sweep@test")
	t.Setenv("BASE", "")

	f := &fixture{t: t, root: t.TempDir()}
	f.run("init", "-q", "-b", "main", ".")
	return f
}

func (f *fixture) run(args ...string) {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// write puts a file in the tree, creating parents.
func (f *fixture) write(name, body string) {
	f.t.Helper()
	path := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commit(msg string) {
	f.t.Helper()
	f.run("add", "-A")
	f.run("commit", "-q", "-m", msg)
}

// sweep runs the sweep over the fixture and returns its exit code and report.
func (f *fixture) sweep(base string) (int, string) {
	f.t.Helper()
	var out, errOut bytes.Buffer
	code := Run(f.root, base, &out, &errOut)
	return code, out.String() + errOut.String()
}

// check asserts the exit code, and that the report says something.
func (f *fixture) check(name, base string, wantCode int, wantText string) {
	f.t.Helper()
	code, out := f.sweep(base)
	if code != wantCode {
		f.t.Errorf("%s: exit %d, wanted %d\n%s", name, code, wantCode, out)
		return
	}
	if wantText != "" && !strings.Contains(out, wantText) {
		f.t.Errorf("%s: exit %d as wanted, but the report never says %q\n%s", name, code, wantText, out)
	}
}

// ---------------------------------------------------------------------------
// Catching a stale name. Ported from scripts/stale-comment-sweep-test.sh.
// ---------------------------------------------------------------------------

const lockBaseline = `package lock

// explainLocked reports why the lock is held.
func explainLocked() string {
	return "held"
}
`

const lockRenamed = `package lock

// describeHold reports why the lock is held.
func describeHold() string {
	return "held"
}

// Callers must not take the mutex before explainLocked runs, or the
// two paths deadlock.
func caller() string {
	return describeHold()
}
`

func TestTheStragglerIsReported(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", lockBaseline)
	f.commit("baseline")
	f.write("lock.go", lockRenamed)
	f.commit("rename the function, miss the second comment")

	code, out := f.sweep("HEAD~1")
	if code != ExitStale {
		t.Fatalf("exit %d, wanted %d\n%s", code, ExitStale, out)
	}
	if !strings.Contains(out, "explainLocked") {
		t.Fatalf("the straggler is not named in the report:\n%s", out)
	}
	// The evidence, not only the name: the report is read by a person who has
	// to find the line.
	if !strings.Contains(out, "lock.go:8") {
		t.Fatalf("the report does not point at the comment line:\n%s", out)
	}
}

// The report is drawn on a real terminal: `make stale-comments` runs in a
// developer's shell and in the pre-commit hook. The evidence it quotes is a
// comment somebody else wrote, and baton's whole premise is agents editing files
// in panels, so "somebody else" includes one that was prompt-injected.
//
// Measured before the fix, against a comment carrying ESC ] 0 ; PWNED BEL: the
// OSC sequence reached stdout byte for byte, and would have retitled the window
// of whoever ran the sweep. The name itself needs no filter -- identOnly admits
// nothing but letters, digits and underscore -- but the comment line printed
// beneath it is arbitrary bytes out of a file the sweep did not write.
func TestTheEvidenceIsScrubbedBeforeItReachesATerminal(t *testing.T) {
	f := newRepo(t)
	f.write("a.go", "package p\n\nfunc F() {}\n")
	f.commit("baseline")
	f.write("b.go", "package p\n\n// \x1b]0;PWNED\x07 the zzQuuxWidget went away\nfunc G() {}\n")
	f.commit("a comment carrying an escape sequence")

	code, out := f.sweep("HEAD~1")
	if code != ExitStale {
		t.Fatalf("exit %d, wanted %d\n%s", code, ExitStale, out)
	}
	if !strings.Contains(out, "zzQuuxWidget") {
		t.Fatalf("the straggler is not named in the report:\n%q", out)
	}
	for _, r := range []rune{0x1b, 0x07} {
		if strings.ContainsRune(out, r) {
			t.Errorf("the comment's %#x reached the report; a terminal would act on it:\n%q", r, out)
		}
	}
	// Scrubbed, not swallowed: the reader still sees that something tried.
	if !strings.Contains(out, "]0;PWNED") {
		t.Errorf("the escape's payload was dropped along with its introducer, so nothing says it was there:\n%q", out)
	}
}

func TestTheCorrectedCommentIsQuiet(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", lockBaseline)
	f.commit("baseline")
	f.write("lock.go", lockRenamed)
	f.commit("rename the function, miss the second comment")
	f.write("lock.go", strings.ReplaceAll(lockRenamed, "explainLocked runs", "describeHold runs"))
	f.commit("fix the straggler")

	f.check("the corrected comment is quiet", "HEAD~1", ExitOK, "still exists in code")
}

func TestOrdinaryProseIsNotAnIdentifier(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", lockBaseline)
	f.commit("baseline")
	f.write("lock.go", lockBaseline+`
// Callers hold the lock. The Deadline is advisory and TODO items are tracked
// elsewhere. IDs and URLs are formatted by the caller. A rule of _ is not a
// name either.
func note() {}
`)
	f.commit("add ordinary English prose")

	f.check("ordinary prose is not an identifier", "HEAD~1", ExitOK, "still exists in code")
}

// ---------------------------------------------------------------------------
// The refusals. Each of these once had an obvious wrong answer -- exit 0 -- and
// exit 0 is indistinguishable from having read the whole range and approved it.
// ---------------------------------------------------------------------------

func TestAnEmptyRangeIsNotAPass(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", lockBaseline)
	f.commit("baseline")
	f.write("lock.go", lockRenamed)
	f.commit("second")

	f.check("an empty range is not a pass", "HEAD", ExitUnchecked, "NOTHING CHECKED")
}

func TestAnUnresolvableBaseIsNotAPass(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", lockBaseline)
	f.commit("baseline")
	f.write("lock.go", lockRenamed)
	f.commit("second")

	f.check("an unresolvable base is not a pass", "no/such/ref", ExitUnchecked, "NOTHING CHECKED")
}

func TestASingleCommitCloneIsNotAPass(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", lockBaseline)
	f.commit("baseline")

	// No base given and no HEAD~1 to fall back on.
	f.check("a single-commit clone is not a pass", "", ExitUnchecked, "NOTHING CHECKED")
}

func TestACommitWithNoAddedCommentsPassesHonestly(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", lockBaseline)
	f.commit("baseline")
	f.write("lock.go", lockBaseline+"\nfunc plain() int { return 1 }\n")
	f.commit("add code carrying no comment")

	f.check("no added comments is an honest nothing-to-do", "HEAD~1", ExitOK, "nothing here to sweep")
}

// A tree whose .go file is not Go at all is what a broken comment/code split
// used to look like from the inside. The awk version could only infer it (an
// index holding neither `package` nor `func` cannot have come from Go); a parser
// says so by name.
func TestAToolFaultIsNotDressedUpAsAFinding(t *testing.T) {
	f := newRepo(t)
	f.write("a.go", "// a file of pure comment\n")
	f.commit("baseline")
	f.write("a.go", "// a file of pure comment\n// naming mostlyProse, which exists nowhere\n")
	f.commit("a comment naming something absent")

	code, out := f.sweep("HEAD~1")
	if code != ExitUnchecked {
		t.Fatalf("exit %d, wanted %d\n%s", code, ExitUnchecked, out)
	}
	if !strings.Contains(out, "could not be read as Go") {
		t.Fatalf("the report does not say the tree was unreadable:\n%s", out)
	}
	// The name in the unreadable file must not be reported as a finding: a
	// fault in the tool arriving as a finding about the code is the one way a
	// checker can be worse than absent.
	if strings.Contains(out, "mostlyProse") {
		t.Fatalf("a tool fault was dressed up as a finding:\n%s", out)
	}
}

// The independent count of what there was to check.
//
// A path holding a space reaches the sweep quoted, because that is how git
// writes it in the "+++" header, so the scanner finds the file under a name the
// tree does not have and examines nothing. A dumb pattern over the raw diff --
// sharing no code with the scanner or with the hunk reader -- says there were
// comment lines to examine, and the disagreement is the sweep saying it is
// broken rather than saying the range is clean.
func TestExaminingNoneOfWhatTheDiffAddsIsNotAPass(t *testing.T) {
	f := newRepo(t)
	f.write("a.go", "package a\n")
	f.commit("baseline")
	f.write("a b.go", "package a\n\n// vanishedHelper used to live here.\nfunc b() {}\n")
	f.commit("a file whose name git has to quote")

	f.check("a range the sweep could not read is not a pass", "HEAD~1", ExitUnchecked, "but the sweep examined 0 of them")
}

func TestAReadableTreeIsRead(t *testing.T) {
	f := newRepo(t)
	f.write("a.go", "package a\n")
	f.commit("baseline")
	f.write("a.go", "package a\n\n// b does nothing at all.\nfunc b() {}\n")
	f.commit("real code, so the index is real")

	f.check("a readable tree is read", "HEAD~1", ExitOK, "still exists in code")
}

// Go outside testdata must parse; Go inside it need not, because the Go tool
// itself never looks there.
func TestTestdataIsNotParsed(t *testing.T) {
	f := newRepo(t)
	f.write("a.go", "package a\n")
	f.commit("baseline")
	f.write("testdata/broken.go", "this is not Go at all {{{\n")
	f.write("a.go", "package a\n\n// b does nothing at all.\nfunc b() {}\n")
	f.commit("add a fixture the Go tool would not read either")

	f.check("testdata does not break the sweep", "HEAD~1", ExitOK, "still exists in code")
}

// ---------------------------------------------------------------------------
// Not the range: the file the range touched. A comment the range did not add
// is not the branch's business, however stale it is.
// ---------------------------------------------------------------------------

func TestAnUntouchedStaleCommentIsNotTheBranchsBusiness(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", "package lock\n\n// vanishedHelper used to live here.\nfunc keep() {}\n")
	f.commit("baseline, already stale")
	f.write("lock.go", "package lock\n\n// vanishedHelper used to live here.\nfunc keep() {}\n\n// keepAlso is new and honest.\nfunc keepAlso() {}\n")
	f.commit("add an honest comment")

	f.check("an untouched comment is not swept", "HEAD~1", ExitOK, "still exists in code")
}

// ---------------------------------------------------------------------------
// The base picker: nearest merge-base, not first match.
// ---------------------------------------------------------------------------

func TestTheNearestMergeBaseWins(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", lockBaseline)
	f.commit("c1")
	f.write("lock.go", lockBaseline+"\nfunc two() {}\n")
	f.commit("c2")

	// A stale "origin/main" 1 commit behind, and a fresh local `main` at c2.
	// Taking origin/main on faith would sweep c2 as well as c3.
	f.run("update-ref", "refs/remotes/origin/main", "HEAD~1")
	f.run("checkout", "-q", "-b", "feature")
	f.write("lock.go", lockRenamed)
	f.commit("c3, the only commit this branch adds")

	r := repo{dir: f.root}
	base, err := r.resolveBase("")
	if err != nil {
		t.Fatal(err)
	}
	want, err := r.revParse("main")
	if err != nil {
		t.Fatal(err)
	}
	if base != want {
		stale, _ := r.revParse("origin/main")
		t.Fatalf("base %s, wanted the nearer %s (the stale remote is %s)", base[:8], want[:8], stale[:8])
	}
}

func TestAnExplicitBaseOverridesThePicker(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", lockBaseline)
	f.commit("c1")
	f.write("lock.go", lockRenamed)
	f.commit("c2")

	r := repo{dir: f.root}
	want, err := r.revParse("HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := r.resolveBase("HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if base != want {
		t.Fatalf("base %s, wanted %s", base, want)
	}
}

func TestMainReportsOutsideAWorkingTree(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("BASE", "")

	var out bytes.Buffer
	if code := Main(nil, &out, &out); code != ExitUnchecked {
		t.Fatalf("exit %d, wanted %d\n%s", code, ExitUnchecked, out.String())
	}
	if !strings.Contains(out.String(), "not a git working tree") {
		t.Fatalf("the report does not say why:\n%s", out.String())
	}
}

func TestMainTakesTheBaseFromTheEnvironment(t *testing.T) {
	f := newRepo(t)
	f.write("lock.go", lockBaseline)
	f.commit("baseline")
	f.write("lock.go", lockRenamed)
	f.commit("rename the function, miss the second comment")

	t.Chdir(f.root)
	t.Setenv("BASE", "HEAD~1")

	var out bytes.Buffer
	if code := Main(nil, &out, &out); code != ExitStale {
		t.Fatalf("exit %d, wanted %d\n%s", code, ExitStale, out.String())
	}
	if !strings.Contains(out.String(), "explainLocked") {
		t.Fatalf("BASE was not honoured:\n%s", out.String())
	}
}
