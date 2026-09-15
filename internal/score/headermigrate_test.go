package score

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// headermigrate_test.go covers #95: a score.md written before #57 carries the
// header from before it, so the operators who have used Score the longest are
// the ones who never see the rule #57 exists to teach.
//
// The whole design rests on one distinction — bytes baton wrote versus bytes
// the operator wrote — and the tests that matter are the ones proving the
// second kind is never touched. SCORE.md promises "delete them and they stay
// deleted"; a migration that passes the happy path and breaks that promise is
// worse than no migration.

// seedHeaderV1 is seedLocked's header from 3915d1c, byte for byte. It is the
// literal found on a real store, and the reason an exact match is proof rather
// than a guess: nothing but baton produces these bytes.
var seedHeaderV1 = []string{
	"# This file is baton's fleet memory — one entry per line, like:",
	"#   - [e7f3a2] the agent was asked to gain permission",
	"# Edit or delete lines freely; anything that is not an entry is ignored.",
}

// storeWithMD opens a store over a score.md written by hand, so a test can
// start from a file an older build would have left behind.
func storeWithMD(t *testing.T, content string) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "score")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, scoreMD), []byte(content), 0o600); err != nil {
		t.Fatalf("write score.md: %v", err)
	}
	return openStore(t, dir), dir
}

// headOf is the leading comment block of a score.md.
func headOf(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}
		out = append(out, line)
	}
	return out
}

// TestOldHeaderIsBroughtForward is the issue: an untouched pre-#57 header
// becomes the current one, and the rule the operator could not infer is finally
// in the file they type in.
func TestOldHeaderIsBroughtForward(t *testing.T) {
	old := strings.Join(seedHeaderV1, "\n") + "\n- [abc123] run the linter first\n"
	s, dir := storeWithMD(t, old)

	if _, err := s.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	md := readFile(t, dir, scoreMD)
	got := headOf(md)
	if len(got) != len(mdHeader) {
		t.Fatalf("header is %d lines, want the current %d:\n%s", len(got), len(mdHeader), md)
	}
	for i, want := range mdHeader {
		if got[i] != want {
			t.Fatalf("header line %d = %q, want %q", i, got[i], want)
		}
	}
	// The sentence that is the whole of #57.
	if !strings.Contains(md, `ANY line beginning "- "`) {
		t.Errorf("the migrated header still does not name the rule:\n%s", md)
	}
}

// TestMigrationKeepsEveryEntry is the guard the migration owes the fleet: it
// rewrites a comment block and must be a no-op for memory.
func TestMigrationKeepsEveryEntry(t *testing.T) {
	old := strings.Join(seedHeaderV1, "\n") + "\n" +
		"- [aaa111] first\n- [bbb222] second\n- [ccc333] third\n"
	s, dir := storeWithMD(t, old)

	if _, err := s.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	md := readFile(t, dir, scoreMD)
	var ids []string
	for _, line := range strings.Split(md, "\n") {
		if id, _, ok := parseLine(line); ok {
			ids = append(ids, id)
		}
	}
	if strings.Join(ids, ",") != "aaa111,bbb222,ccc333" {
		t.Errorf("entries after the migration = %v, want all three in order", ids)
	}
}

// TestMigrationIsIdempotent pins that the current header is not itself
// superseded. Get that wrong and the store rewrites the file it just wrote, on
// every pass, forever — a read path silently turned into a write path.
func TestMigrationIsIdempotent(t *testing.T) {
	old := strings.Join(seedHeaderV1, "\n") + "\n- [abc123] a note\n"
	s, dir := storeWithMD(t, old)

	if _, err := s.Reconcile(); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	first := readFile(t, dir, scoreMD)
	for i := 0; i < 3; i++ {
		if _, err := s.Reconcile(); err != nil {
			t.Fatalf("Reconcile %d: %v", i+2, err)
		}
	}
	if got := readFile(t, dir, scoreMD); got != first {
		t.Errorf("a second pass changed the file:\n%s\n--- then ---\n%s", first, got)
	}
}

// TestEditedHeaderIsLeftAlone is the promise SCORE.md makes in writing, and the
// mutation that kills the test above: a migration keyed on anything looser than
// an exact match would trample the operator's own words.
func TestEditedHeaderIsLeftAlone(t *testing.T) {
	edited := []string{
		seedHeaderV1[0],
		"#   - [e7f3a2] MY OWN NOTE ABOUT WHAT THIS FILE IS FOR",
		seedHeaderV1[2],
	}
	content := strings.Join(edited, "\n") + "\n- [abc123] a note\n"
	s, dir := storeWithMD(t, content)

	if _, err := s.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := headOf(readFile(t, dir, scoreMD))
	if strings.Join(got, "\n") != strings.Join(edited, "\n") {
		t.Errorf("an edited header was rewritten:\n%v\nwant:\n%v", got, edited)
	}
}

// TestDeletedHeaderStaysDeleted is the other half of the promise. "Delete them
// and they stay deleted" is the specific sentence a migration would break.
func TestDeletedHeaderStaysDeleted(t *testing.T) {
	s, dir := storeWithMD(t, "- [abc123] a note\n")

	if _, err := s.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	md := readFile(t, dir, scoreMD)
	if strings.Contains(md, "#") {
		t.Errorf("a header the operator deleted came back:\n%s", md)
	}
	if !strings.Contains(md, "abc123") {
		t.Errorf("the entry went missing:\n%s", md)
	}
}

// TestPartiallyDeletedHeaderIsLeftAlone is conservative on purpose: a
// half-kept header is a decision, and an exact match is the only evidence that
// bytes are baton's rather than the operator's.
func TestPartiallyDeletedHeaderIsLeftAlone(t *testing.T) {
	kept := seedHeaderV1[:2]
	content := strings.Join(kept, "\n") + "\n- [abc123] a note\n"
	s, dir := storeWithMD(t, content)

	if _, err := s.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := headOf(readFile(t, dir, scoreMD))
	if strings.Join(got, "\n") != strings.Join(kept, "\n") {
		t.Errorf("a partially kept header was rewritten:\n%v\nwant:\n%v", got, kept)
	}
}

// TestCurrentHeaderIsNotSuperseded is the invariant behind idempotence, asserted
// against the table itself rather than through a store — so the day a third
// header is written, the failure names the cause instead of showing up as a
// file that rewrites itself.
func TestCurrentHeaderIsNotSuperseded(t *testing.T) {
	cur := strings.Join(mdHeader, "\n")
	for i, old := range supersededHeaders {
		if strings.Join(old, "\n") == cur {
			t.Fatalf("supersededHeaders[%d] IS the current header: every pass would rewrite score.md", i)
		}
	}
	if len(supersededHeaders) == 0 {
		t.Error("no superseded headers are listed, so nothing can ever be migrated")
	}
}
