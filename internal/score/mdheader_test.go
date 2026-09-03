package score

import (
	"path/filepath"
	"strings"
	"testing"
)

// mdheader_test.go covers #57: an ordinary markdown bullet silently becomes
// fleet memory. The rule itself is unchanged — a bare "- " line is the only way
// an operator adds an entry by hand — so what these tests pin is its VISIBILITY:
// the rule is written into the file the operator is typing in, and it survives
// everything a reconcile pass does to that file.

// headerLines returns the "#" lines at the top of content, in order, stopping at
// the first line that is not one.
func headerLines(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}
		out = append(out, line)
	}
	return out
}

// memoryLines returns the lines of a score.md that would become memory — an
// entry line, or a bare bullet the next pass would admit. It is the property
// the "a fresh install seeds nothing" tests are actually about: what the file
// contributes to an agent's brief, rather than how many bytes are in it. Since
// #57 those two differ, because the file now opens with the rule that says
// which of its lines are memory.
func memoryLines(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		if _, _, ok := parseLine(line); ok {
			out = append(out, line)
			continue
		}
		if _, ok := parseBullet(line); ok {
			out = append(out, line)
		}
	}
	return out
}

// TestFreshMDTeachesTheBareBulletRule is #57's first acceptance criterion: a
// fresh score.md carries the rule, and the lines that carry it are not entries.
//
// The second half is the one worth asserting rather than assuming. The header
// shows the entry format by example, so it contains the substring "- [" and the
// substring "- " — the exact two prefixes parseLine and parseBullet cut on — and
// a header that started an entry would be baton putting its own instructions
// into every agent's prompt, which is the failure #57 is about, inverted.
func TestFreshMDTeachesTheBareBulletRule(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "score")
	s := openStore(t, dir)

	md := readFile(t, dir, scoreMD)
	if md == "" {
		t.Fatal("a fresh score.md is empty: the rule an operator cannot guess is written nowhere they will look")
	}
	got := headerLines(md)
	if len(got) != len(mdHeader) {
		t.Fatalf("fresh score.md opens with %d comment lines, want %d:\n%s", len(got), len(mdHeader), md)
	}
	for i, want := range mdHeader {
		if got[i] != want {
			t.Errorf("header line %d = %q, want %q", i, got[i], want)
		}
	}

	// The rule, not just any comment. "- " is the whole of what makes a line
	// memory, so the header has to say so in those bytes.
	if !strings.Contains(md, `"- "`) {
		t.Errorf("the header never names the %q prefix, which is the entire rule:\n%s", "- ", md)
	}

	// Not entries — neither at parse time nor after a pass over the file.
	for _, line := range mdHeader {
		if _, _, ok := parseLine(line); ok {
			t.Errorf("header line %q parses as an entry", line)
		}
		if _, ok := parseBullet(line); ok {
			t.Errorf("header line %q parses as a bullet", line)
		}
	}
	if d := reconcile(t, s); d != (Delta{}) {
		t.Errorf("a pass over the header alone changed something: %+v", d)
	}
	if s.Len() != 0 {
		t.Errorf("the header became %d entries, want 0", s.Len())
	}
	if block := s.RenderBlock(Context{}); block != "" {
		t.Errorf("the header reached an agent's brief: %q", block)
	}
}

// TestHeaderSurvivesReconcileThatAdmitsFoldsAndRetires is the assertion the
// issue does not make and the whole design rests on.
//
// mdHeader is written ONCE, by projectLocked, on a score.md that does not
// exist. Every later write of the file is the reconcile pass's whole-file
// rewrite, and that rewrite is safe for the header only because it FILTERS the
// lines it read — appending each non-entry line to `out` as it found it — rather
// than regenerating the file from the entry set. If that were ever to change,
// the header would have to be re-emitted on every pass instead, which is a
// different change; this test is what says which of the two the code is.
//
// So the pass under test is made to do all three of the things that move lines
// around: admit a new bullet (which rewrites the file to write an id back), fold
// a duplicate (which DELETES a line, the only line the pass ever removes), and
// retire an entry whose line is gone.
func TestHeaderSurvivesReconcileThatAdmitsFoldsAndRetires(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "score")
	s := openStore(t, dir)

	keep := submit(t, s, "run the linter before claiming a task is done")
	gone := submit(t, s, "never force-push a shared branch")

	// The file an operator leaves behind: the header untouched, one entry kept,
	// one entry's line deleted, a duplicate of the kept entry, and a bare bullet
	// of their own. Prose between them, to pin that the header is not a special
	// case but the ordinary treatment of a line that is not an entry.
	lines := append([]string(nil), mdHeader...)
	lines = append(lines,
		"",
		"## my own notes",
		formatLine(keep.Id, keep.Text),
		"- run the linter before claiming a task is done",
		"- ask dana whether the migration can slip a week",
		"",
	)
	writeMD(t, dir, strings.Join(lines, "\n"))

	d := reconcile(t, s)
	switch {
	case d.Admitted != 1:
		t.Fatalf("Admitted = %d, want 1 — the pass did not admit", d.Admitted)
	case d.Folded != 1:
		t.Fatalf("Folded = %d, want 1 — the pass did not fold", d.Folded)
	case d.Retired != 1:
		t.Fatalf("Retired = %d, want 1 — the pass did not retire", d.Retired)
	}

	md := readFile(t, dir, scoreMD)
	got := headerLines(md)
	if len(got) != len(mdHeader) {
		t.Fatalf("after the pass score.md opens with %d comment lines, want %d:\n%s", len(got), len(mdHeader), md)
	}
	for i, want := range mdHeader {
		if got[i] != want {
			t.Fatalf("the pass rewrote header line %d to %q, want %q", i, got[i], want)
		}
	}
	if !strings.Contains(md, "## my own notes") {
		t.Errorf("the pass dropped the operator's own prose:\n%s", md)
	}
	// The retired entry's line is gone, and the bare bullet came back with an id
	// — so the file really was rewritten, and the header survived a rewrite
	// rather than a pass that declined to write.
	if strings.Contains(md, gone.Text) {
		t.Errorf("the retired entry's text is still in the file:\n%s", md)
	}
	if strings.Contains(md, "\n- ask dana") {
		t.Errorf("the admitted bullet kept no id, so no rewrite happened:\n%s", md)
	}
}

// TestHeaderSurvivesRefine covers the FOURTH writer of score.md, which #57's
// acceptance criteria do not name and which could break the header just as
// completely: replaceMDLineLocked, behind Reword, Merge and Lower.
//
// Its doc says every other byte is written back as it was read. That was read
// rather than proved, and the whole design rests on exactly this class of claim
// — a header written once at creation is only safe while every later writer of
// the file preserves what it did not put there.
func TestHeaderSurvivesRefine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "score")
	s := openStore(t, dir)

	keep := submit(t, s, "run the linter before claiming a task is done")
	drop := submit(t, s, "never force-push a shared branch")

	if err := s.Reword(keep.Id, "run the linter first"); err != nil {
		t.Fatalf("Reword: %v", err)
	}
	// Merge takes the OTHER branch of replaceMDLineLocked: the one that deletes a
	// line outright rather than replacing it, which is where a writer that
	// mishandled offsets would take a header line with it.
	if err := s.Merge(keep.Id, drop.Id); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	md := readFile(t, dir, scoreMD)
	got := headerLines(md)
	if len(got) != len(mdHeader) {
		t.Fatalf("after a refine score.md opens with %d comment lines, want %d:\n%s", len(got), len(mdHeader), md)
	}
	for i, want := range mdHeader {
		if got[i] != want {
			t.Fatalf("a refine rewrote header line %d to %q, want %q", i, got[i], want)
		}
	}
	if !strings.Contains(md, "run the linter first") {
		t.Errorf("the reword did not reach the file:\n%s", md)
	}
	if strings.Contains(md, drop.Text) {
		t.Errorf("the merge did not remove the merged-away line:\n%s", md)
	}
}

// TestOperatorProseStillBecomesAnEntry is #57's fourth acceptance criterion,
// stated as its own test because it is the one thing the fix must NOT change.
// Narrowing parseBullet to require a marker would regress the only way an entry
// is added by hand, so the trap stays open and only its signage changes.
func TestOperatorProseStillBecomesAnEntry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "score")
	s := openStore(t, dir)

	lines := append([]string(nil), mdHeader...)
	lines = append(lines,
		"- ask dana whether the migration can slip a week",
		"  - rotate the staging credentials", // indented: still a bullet, after TrimSpace
		"* not a bullet baton takes",
		"+ nor this one",
		"1. nor this one either",
		"",
	)
	writeMD(t, dir, strings.Join(lines, "\n"))

	if d := reconcile(t, s); d.Admitted != 2 {
		t.Fatalf("Admitted = %d, want 2 — a bare bullet and an indented one", d.Admitted)
	}
	texts := map[string]bool{}
	for _, e := range s.Render(Context{}) {
		texts[e.Text] = true
	}
	for _, want := range []string{
		"ask dana whether the migration can slip a week",
		"rotate the staging credentials",
	} {
		if !texts[want] {
			t.Errorf("%q did not become an entry; the authoring path regressed", want)
		}
	}
	for _, notEntry := range []string{"not a bullet baton takes", "nor this one", "nor this one either"} {
		if texts[notEntry] {
			t.Errorf("%q became an entry; only %q bullets are taken", notEntry, "- ")
		}
	}
}
