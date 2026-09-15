package score

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// edit_test.go covers #93's guard: the window between an operator opening
// score.md in $EDITOR and saving it.
//
// The loss these pin is not a race. An editor writes back the snapshot it
// opened with, so a submission the fleet appended while the file was open is
// simply not in the save — and reconcile reads a line that is not there as a
// retirement. Five minutes with the file open is five minutes of memory that
// goes away on :w, and nothing complains, because from the store's side the
// operator removed some notes.

// hasEntry reports whether score.md carries a line for this id.
func hasEntry(t *testing.T, dir, id string) bool {
	t.Helper()
	for _, line := range strings.Split(readFile(t, dir, scoreMD), "\n") {
		if got, _, ok := parseLine(line); ok && got == id {
			return true
		}
	}
	return false
}

// TestEndEditRestoresWhatTheOperatorNeverSaw is the measured failure, guarded.
// Without EndEdit's restore the agent's line is gone after the save; the
// assertion that it survives is the whole point of the pair.
func TestEndEditRestoresWhatTheOperatorNeverSaw(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "score")
	s := openStore(t, dir)
	kept := submit(t, s, "prefer table-driven tests everywhere")

	opened, err := s.BeginEdit()
	if err != nil {
		t.Fatalf("BeginEdit: %v", err)
	}
	snapshot := readFile(t, dir, scoreMD) // what $EDITOR now holds in its buffer

	// The fleet learns something while the editor sits there.
	late := submit(t, s, "the linter runs before the tests")
	if !hasEntry(t, dir, late.Id) {
		t.Fatal("the submission never reached score.md; the rest of this test proves nothing")
	}
	if _, seen := opened[late.Id]; seen {
		t.Fatal("the snapshot already knew the late id, so the guard has nothing to separate")
	}

	// :w — the buffer goes back to disk, with the operator's own change in it and
	// without a line they never saw.
	writeMD(t, dir, strings.Replace(snapshot, kept.Text, "prefer table-driven tests", 1))

	restored, _, err := s.EndEdit(opened)
	if err != nil {
		t.Fatalf("EndEdit: %v", err)
	}

	if !hasEntry(t, dir, late.Id) {
		t.Errorf("the entry submitted during the window is gone from score.md; restored=%v\n%s",
			restored, readFile(t, dir, scoreMD))
	}
	if len(restored) != 1 || restored[0] != late.Id {
		t.Errorf("restored = %v, want exactly [%s]", restored, late.Id)
	}
	// The operator's edit is not collateral: their reword still stands.
	if md := readFile(t, dir, scoreMD); !strings.Contains(md, "prefer table-driven tests\n") {
		t.Errorf("the operator's own edit did not survive the restore:\n%s", md)
	}
}

// TestEndEditKeepsADeliberateDeletion is the mutation that kills the test above.
// A guard that simply re-appended every missing line would pass it and break
// the only way an operator retires an entry by hand — so the id the snapshot
// DID carry must stay deleted.
func TestEndEditKeepsADeliberateDeletion(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "score")
	s := openStore(t, dir)
	doomed := submit(t, s, "a note the operator is about to remove")
	other := submit(t, s, "a note they are keeping")

	opened, err := s.BeginEdit()
	if err != nil {
		t.Fatalf("BeginEdit: %v", err)
	}
	if _, seen := opened[doomed.Id]; !seen {
		t.Fatal("the snapshot does not carry the id under test")
	}

	// The operator deletes the line, which is the documented way to retire one.
	var out []string
	for _, line := range strings.Split(readFile(t, dir, scoreMD), "\n") {
		if id, _, ok := parseLine(line); ok && id == doomed.Id {
			continue
		}
		out = append(out, line)
	}
	writeMD(t, dir, strings.Join(out, "\n"))

	restored, delta, err := s.EndEdit(opened)
	if err != nil {
		t.Fatalf("EndEdit: %v", err)
	}
	if len(restored) != 0 {
		t.Fatalf("a line the operator saw and deleted was restored: %v", restored)
	}
	if delta.Retired != 1 {
		t.Errorf("delta.Retired = %d, want 1 — the deletion did not reach the store: %+v", delta.Retired, delta)
	}
	if hasEntry(t, dir, doomed.Id) {
		t.Error("the retired entry is still in score.md")
	}
	if !hasEntry(t, dir, other.Id) {
		t.Error("the entry they kept went with it")
	}
}

// TestBeginEditOpensAFileThatIsThere pins the store-off sibling of #93: a fleet
// that has never written score.md still gets a file with the header in it,
// rather than $EDITOR opening an empty buffer at a path nothing created.
func TestBeginEditOpensAFileThatIsThere(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "score")
	s := openStore(t, dir)
	if err := os.Remove(filepath.Join(dir, scoreMD)); err != nil {
		t.Fatalf("remove score.md: %v", err)
	}

	opened, err := s.BeginEdit()
	if err != nil {
		t.Fatalf("BeginEdit: %v", err)
	}
	if len(opened) != 0 {
		t.Errorf("a re-projected empty store reports %d ids, want none", len(opened))
	}
	md := readFile(t, dir, scoreMD)
	if !strings.HasPrefix(md, mdHeader[0]) {
		t.Errorf("score.md was not re-projected with its header:\n%s", md)
	}
}

// TestEndEditOnADeletedFileReprojects covers the operator who removes the whole
// file rather than a line. That is not a statement about any entry, and the
// pass already answers it by re-projecting the log — so the guard must keep its
// hands off rather than append into the gap and leave a headerless file.
func TestEndEditOnADeletedFileReprojects(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "score")
	s := openStore(t, dir)
	kept := submit(t, s, "a note that outlives the file")

	opened, err := s.BeginEdit()
	if err != nil {
		t.Fatalf("BeginEdit: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, scoreMD)); err != nil {
		t.Fatalf("remove score.md: %v", err)
	}

	restored, delta, err := s.EndEdit(opened)
	if err != nil {
		t.Fatalf("EndEdit: %v", err)
	}
	if len(restored) != 0 {
		t.Errorf("the guard appended into an absent file: %v", restored)
	}
	if delta.Reprojected != 1 {
		t.Errorf("delta.Reprojected = %d, want 1: %+v", delta.Reprojected, delta)
	}
	md := readFile(t, dir, scoreMD)
	if !strings.HasPrefix(md, mdHeader[0]) {
		t.Errorf("the re-projected file lost its header:\n%s", md)
	}
	if !hasEntry(t, dir, kept.Id) {
		t.Errorf("the entry did not come back:\n%s", md)
	}
}

// TestEndEditIsANoOpWhenNothingHappened pins the everyday case: an operator who
// opens the file, changes nothing and quits moves nothing. A guard that logged
// a restore here would cry wolf on every look.
func TestEndEditIsANoOpWhenNothingHappened(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "score")
	s := openStore(t, dir)
	submit(t, s, "a note nobody touches")

	opened, err := s.BeginEdit()
	if err != nil {
		t.Fatalf("BeginEdit: %v", err)
	}
	restored, delta, err := s.EndEdit(opened)
	if err != nil {
		t.Fatalf("EndEdit: %v", err)
	}
	if len(restored) != 0 {
		t.Errorf("restored %v after an editing session that changed nothing", restored)
	}
	if delta != (Delta{}) {
		t.Errorf("a no-op session reported %+v", delta)
	}
}
