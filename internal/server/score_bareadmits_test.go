package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/score"
)

// score_bareadmits_test.go is #57's second half. The first put the rule into
// score.md for an operator who has not been bitten yet; this is for the one who
// has — four TODO lines in their own file, now riding into every panel's prompt,
// with the only record of it on a daemon log line they have no reason to read.
//
// The trap itself is untouched: parseBullet still admits any "- " line, because
// that is the only way an entry is added by hand. What changes is that the store
// says how often it has sprung.

// operatorNotes is the file from #57's report, verbatim in shape: an operator
// keeping their own markdown TODO list in score.md.
const operatorNotes = `# baton's fleet memory
- ask dana whether the migration can slip a week
- rotate the staging credentials
- chase the flaky auth test
- book the retro room
`

// TestStatusReportsBareAdmits is #57's third acceptance criterion. Four lines of
// an operator's own notes go into score.md; score.status says four.
func TestStatusReportsBareAdmits(t *testing.T) {
	st, dir := scoreStore(t)
	s, _, _ := scoreServer(st)

	if got := status(t, s).BareAdmits; got != 0 {
		t.Fatalf("bare_admits on a fresh store = %d, want 0", got)
	}

	editScoreMD(t, dir, operatorNotes)

	got := status(t, s)
	if got.BareAdmits != 4 {
		t.Fatalf("bare_admits = %d, want 4 — the operator's four notes", got.BareAdmits)
	}
	if got.Entries != 4 {
		t.Fatalf("entries = %d, want 4", got.Entries)
	}
}

// TestBareAdmitsSurvivesTheNextStatusCall is the reason the counter is
// cumulative rather than "what the last pass did", and it is the assertion that
// makes that a fact instead of an argument.
//
// score.status is answered off a View, and a View's reconcile is GATED on
// score.md having moved since the last one. The pass that admits the operator's
// notes therefore happens once — on whichever read got there first, which in a
// running fleet is a dispatch — and every later status call runs no pass at all.
// A per-pass figure carried onto the payload would be correct exactly once and
// read 0 for every question the operator actually asks.
//
// So: reconcile, then ask twice more without touching the file. All three
// answers must be four.
func TestBareAdmitsSurvivesTheNextStatusCall(t *testing.T) {
	st, dir := scoreStore(t)
	s, _, _ := scoreServer(st)

	editScoreMD(t, dir, operatorNotes)

	for i, want := range []int{4, 4, 4} {
		if got := status(t, s).BareAdmits; got != want {
			t.Fatalf("bare_admits on status call %d = %d, want %d — the count did not outlive its pass", i+1, got, want)
		}
	}

	// And it keeps counting rather than resetting. The file is edited as it now
	// STANDS — the first pass wrote an id into each of those four lines — because
	// replacing it with the id-less original would retire all four and re-admit
	// them, which is the store working correctly and not the arithmetic this
	// test is about.
	md, err := os.ReadFile(filepath.Join(dir, "score.md"))
	if err != nil {
		t.Fatalf("read score.md: %v", err)
	}
	editScoreMD(t, dir, string(md)+"- renew the on-call rota\n- delete the old bucket\n")
	if got := status(t, s).BareAdmits; got != 6 {
		t.Fatalf("bare_admits after two more notes = %d, want 6", got)
	}
}

// TestBareAdmitsCountsBulletsNotReadmissions is why this is its own counter and
// not score.Delta.Admitted, which the issue proposed carrying over as-is.
//
// Admitted covers two different lines: a bare bullet the file swallowed, and a
// line whose [id] names no live entry — a re-admission of something the store
// once knew, typically its own line surviving a log the operator truncated.
// Only the first is the trap. Reporting the pair under one name would answer
// "did baton just eat my notes" with yes on a pass that ate nothing.
func TestBareAdmitsCountsBulletsNotReadmissions(t *testing.T) {
	st, dir := scoreStore(t)
	s, _, _ := scoreServer(st)

	editScoreMD(t, dir, strings.Join([]string{
		"- [a1b2c3] an id this store never issued",
		"- one note of the operator's own",
		"",
	}, "\n"))

	got := status(t, s)
	if got.Entries != 2 {
		t.Fatalf("entries = %d, want 2 — both lines are admitted, which is unchanged", got.Entries)
	}
	if got.BareAdmits != 1 {
		t.Fatalf("bare_admits = %d, want 1 — only the bullet with no id is the trap", got.BareAdmits)
	}
}

// TestBareAdmitsIgnoresSubmissions pins the other edge: the counter is about
// score.md, not about the store. An entry submitted through score.submit or by
// an agent is not a line the operator's file swallowed, however it is written
// back into the file afterwards.
func TestBareAdmitsIgnoresSubmissions(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)

	if _, _, err := st.Submit("prefer table-driven tests", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	got := status(t, s)
	if got.Entries != 1 {
		t.Fatalf("entries = %d, want 1", got.Entries)
	}
	if got.BareAdmits != 0 {
		t.Fatalf("bare_admits = %d, want 0 — a submission is not a swallowed line", got.BareAdmits)
	}
}
