package stalecomment

import (
	"fmt"
	"strings"
	"testing"
)

func TestShaped(t *testing.T) {
	yes := []string{"explainLocked", "parseURL", "max_retries", "O_CREATE", "xAI", "SIGKILLed", "NaNs"}
	no := []string{"Panel", "The", "TODO", "API", "ID", "IDs", "URLs", "PRs", "_", "__", "lowercase", "4GiB", ""}
	for _, tok := range yes {
		if !shaped(tok) {
			t.Errorf("%q should be code-shaped", tok)
		}
	}
	for _, tok := range no {
		if shaped(tok) {
			t.Errorf("%q should not be code-shaped", tok)
		}
	}
}

func TestShapedNamesTokenisesTheComment(t *testing.T) {
	got := shapedNames([]commentLine{
		{Text: "See explainLocked, and `max_retries`; The Deadline is advisory. IDs too."},
		{Text: "explainLocked again -- once in the set."},
	})
	if strings.Join(got, ",") != "explainLocked,max_retries" {
		t.Fatalf("got %v", got)
	}
}

// ---------------------------------------------------------------------------
// The comment/code split -- the whole thing the awk lexer existed to do.
// ---------------------------------------------------------------------------

func TestScanFileSplitsCodeFromComments(t *testing.T) {
	src := `package a

const url = "http://example.com/not-a-comment" // the real one is here
const raw = ` + "`" + `
a raw string holding // and /* and a "quote"
` + "`" + `

/* a block comment
   spanning three lines
   and closing here */
func b() {} // trailing
`
	want := map[int]string{
		3:  "the real one is here",
		8:  "a block comment",
		9:  "spanning three lines",
		10: "and closing here",
		11: "trailing",
	}

	all := map[int]bool{}
	for i := 1; i <= 20; i++ {
		all[i] = true
	}
	got := scanFile("a.go", []byte(src), all)

	if len(got) != len(want) {
		t.Fatalf("got %d comment lines, wanted %d: %+v", len(got), len(want), got)
	}
	for _, c := range got {
		if want[c.Line] != c.Text {
			t.Errorf("line %d: %q, wanted %q", c.Line, c.Text, want[c.Line])
		}
	}
}

func TestScanFileKeepsOnlyTheWantedLines(t *testing.T) {
	src := "package a\n\n// one\n// two\n// three\n"
	got := scanFile("a.go", []byte(src), map[int]bool{4: true})
	if len(got) != 1 || got[0].Line != 4 || got[0].Text != "two" {
		t.Fatalf("got %+v, wanted only line 4", got)
	}
}

func TestScanFileJoinsSeveralCommentsOnOneLine(t *testing.T) {
	src := "package a\n\nfunc b() {} /* one */ /* two */\n"
	got := scanFile("a.go", []byte(src), map[int]bool{3: true})
	if len(got) != 1 || got[0].Text != "one two" {
		t.Fatalf("got %+v, wanted one record reading \"one two\"", got)
	}
}

func TestScanFileDropsALineWithNoLetterInIt(t *testing.T) {
	src := "package a\n\n// ---------\nfunc b() {}\n"
	if got := scanFile("a.go", []byte(src), map[int]bool{3: true}); len(got) != 0 {
		t.Fatalf("got %+v, wanted nothing -- a rule carries no name", got)
	}
}

// ---------------------------------------------------------------------------
// The diff reader.
// ---------------------------------------------------------------------------

const sampleDiff = `diff --git a/one.go b/one.go
index 1111111..2222222 100644
--- a/one.go
+++ b/one.go
@@ -3,0 +4,2 @@ func x() {
+// added at four
+// and five
@@ -10,0 +12 @@ func y() {
+// added at twelve
diff --git a/two.go b/two.go
index 3333333..4444444 100644
--- a/two.go
+++ b/two.go
@@ -1,0 +2,2 @@
+/* a block that begins
+++ b/not-a-header.go
@@ -20,0 +30 @@
+// after the trap
`

func TestParseDiffReadsTheHunkHeaders(t *testing.T) {
	added, raw := parseDiff(sampleDiff)

	if fmt.Sprint(added.files) != "[one.go two.go]" {
		t.Fatalf("files %v, wanted one.go then two.go in diff order", added.files)
	}
	for _, line := range []int{4, 5, 12} {
		if !added.lines["one.go"][line] {
			t.Errorf("one.go line %d should be added", line)
		}
	}
	if added.lines["one.go"][6] {
		t.Error("one.go line 6 should not be added -- the hunk covers two lines")
	}
	// An added line whose own text begins "++" arrives in the diff with git's
	// "+" in front of it, looking exactly like a "+++ b/..." header. Read as
	// one, it steals every hunk after it -- so line 30 is the assertion, not
	// the bogus path.
	if _, ok := added.lines["not-a-header.go"]; ok {
		t.Error("a '+++ b/' line inside a hunk was read as a file header")
	}
	if !added.lines["two.go"][30] {
		t.Error("the hunk after the '+++' trap was attributed to the wrong file")
	}
	if !added.lines["two.go"][2] || !added.lines["two.go"][3] || len(added.lines["two.go"]) != 3 {
		t.Errorf("two.go %v, wanted lines 2, 3 and 30", added.lines["two.go"])
	}
	if raw != 5 {
		t.Errorf("raw comment lines %d, wanted 5", raw)
	}
}

func TestParseDiffIgnoresAHunkWithNoFile(t *testing.T) {
	added, _ := parseDiff("@@ -1,0 +2 @@\n+// orphaned\n")
	if len(added.files) != 0 {
		t.Fatalf("files %v, wanted none", added.files)
	}
}

// ---------------------------------------------------------------------------
// The index's three ways past a literal miss.
// ---------------------------------------------------------------------------

func TestHas(t *testing.T) {
	idx := newNameIndex()
	for _, name := range []string{"SIGKILL", "NaN", "Lock", "BenchmarkWriteSinkPlain", "TestFoo"} {
		idx.add(name)
	}

	known := []string{"SIGKILL", "SIGKILLed", "NaNs", "WriteSink", "WriteSinkPlain", "Foo"}
	unknown := []string{"Locked", "SIGTERMed", "SinkWrite", "Bar", "s", "ed"}
	for _, name := range known {
		if !idx.Has(name) {
			t.Errorf("%q should be reachable", name)
		}
	}
	for _, name := range unknown {
		if idx.Has(name) {
			t.Errorf("%q should not be reachable", name)
		}
	}
}
