package score

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file covers R1 (#40): the boot recovery table, reconcile of every edit
// kind an operator can make, ids that are never reused, honest outcomes, and
// the single-writer claim — plus #38's first three verification checks.

// openStore opens a store and releases its directory claim when the test ends,
// so a test may reopen the same directory.
func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	t.Cleanup(s.Close)
	return s
}

// submit records a note as the operator would, failing the test on refusal. The
// tests whose subject IS the provenance still call Submit directly.
func submit(t *testing.T, s *Store, text string) Entry {
	t.Helper()
	e, _, err := s.Submit(text, Provenance{Source: "user"})
	if err != nil {
		t.Fatalf("Submit(%q): %v", text, err)
	}
	return e
}

// submitAs records a note with the provenance under test, failing on refusal.
// The plain submit above hardcodes the operator, and sixteen call sites had
// re-expanded the whole Submit call just to name a panel.
func submitAs(t *testing.T, s *Store, text string, prov Provenance) Entry {
	t.Helper()
	e, _, err := s.Submit(text, prov)
	if err != nil {
		t.Fatalf("Submit(%q, %+v): %v", text, prov, err)
	}
	return e
}

// unwritable takes the write bits off path for the rest of the test and returns
// the undo, so a test can watch a write fail and then watch the retry succeed.
// The store rewrites score.md through a sibling temp file, so passing the
// DIRECTORY is how a rewrite is made to fail and nothing else.
func unwritable(t *testing.T, path string) (restore func()) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	mode := fi.Mode().Perm()
	if err := os.Chmod(path, mode&^0o222); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	restore = func() { _ = os.Chmod(path, mode) }
	t.Cleanup(restore)
	return restore
}

// reconcileMustFail runs a pass that a path made unwritable should stop. It is
// where the run-as-root skip is stated — once, rather than at every site that
// takes a write bit away: root ignores the permission bits, and so do a few
// filesystems, and neither is a failure of the store.
func reconcileMustFail(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.Reconcile(); err == nil {
		t.Skip("the path this test made unwritable is still writable; this test needs an unprivileged user")
	}
}

// reconcile runs one pass and returns what THAT pass changed, failing the test
// on error.
func reconcile(t *testing.T, s *Store) Delta {
	t.Helper()
	d, err := s.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return d
}

// writeMD replaces score.md the way an editor would, and pushes its mtime
// forward so the change is visible to Reconcile's fingerprint gate on a
// filesystem with coarse timestamps.
func writeMD(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, scoreMD)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write score.md: %v", err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes score.md: %v", err)
	}
}

// ageMD pushes score.md's mtime an hour into the past, out of the always-stale
// window, so a test can exercise the fingerprint gate itself.
func ageMD(t *testing.T, dir string) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, scoreMD), past, past); err != nil {
		t.Fatalf("chtimes score.md: %v", err)
	}
}

// events decodes the whole event log, failing on anything unparsable.
func events(t *testing.T, dir string) []event {
	t.Helper()
	var out []event
	for _, line := range strings.Split(strings.TrimSpace(readFile(t, dir, scoreEvents)), "\n") {
		if line == "" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

// hasEvent reports whether the log carries an event of this name for this id.
func hasEvent(evs []event, name, id string) bool {
	for _, ev := range evs {
		if ev.Event == name && ev.Id == id {
			return true
		}
	}
	return false
}

// TestRecoveryMDLineWithNoLogRecord is the table's first row: the operator
// added the line by hand, or the server crashed after writing score.md and
// before logging. Either way the text exists and the user can see it, so it is
// admitted as a user-sourced entry and logged now.
func TestRecoveryMDLineWithNoLogRecord(t *testing.T) {
	dir := t.TempDir()
	writeMD(t, dir, "# notes\n- [abc123] the agent was asked to gain permission\n")

	s := openStore(t, dir)
	entries := s.Render(Context{})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if got := entries[0]; got.Id != "abc123" || got.Tier != 1 || got.Provenance.Source != "user" {
		t.Fatalf("admitted entry = %+v, want id abc123 at tier 1 sourced user", got)
	}
	if d := s.Boot(); d.Admitted != 1 || d.Retired != 0 {
		t.Fatalf("boot recovery = %+v, want one admission and no retirement", d)
	}

	// "and log it now": the admission is durable, with the text, so the next boot
	// needs no second guess.
	evs := events(t, dir)
	if len(evs) != 1 || evs[0].Event != EventSubmitted || evs[0].Id != "abc123" {
		t.Fatalf("log = %+v, want one submitted event for abc123", evs)
	}
	if evs[0].Text != "the agent was asked to gain permission" || evs[0].Source != "user" {
		t.Fatalf("logged event = %+v, want the user's text and source", evs[0])
	}
	if got := readFile(t, dir, scoreMD); got != "# notes\n- [abc123] the agent was asked to gain permission\n" {
		t.Errorf("recovery rewrote a file that needed no id assigned:\n%q", got)
	}
}

// TestRecoveryLogEntryTheFileLacks is the table's second row: the operator
// deleted the line, or the server crashed before writing score.md. The file
// wins over the machine, so the entry retires — into the log, not out of it.
func TestRecoveryLogEntryTheFileLacks(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "this one gets deleted", Provenance{Source: "agent", SourcePanel: "p1"})
	s.Close()

	// The operator empties score.md in their editor and the daemon restarts.
	writeMD(t, dir, "# nothing here any more\n")
	re := openStore(t, dir)

	if re.Len() != 0 {
		t.Fatalf("entries after the line was deleted = %d, want 0", re.Len())
	}
	if d := re.Boot(); d.Retired != 1 {
		t.Fatalf("boot recovery = %+v, want one retirement", d)
	}
	evs := events(t, dir)
	if !hasEvent(evs, EventRetired, e.Id) {
		t.Fatalf("log has no retired event for %s: %+v", e.Id, evs)
	}
	// I7: nothing is destroyed. The text the entry carried is still readable in
	// the log that admitted it.
	if !strings.Contains(readFile(t, dir, scoreEvents), "this one gets deleted") {
		t.Error("retiring destroyed the entry's text; the log must keep it")
	}
}

// TestRecoveryRebuildsFromLogWithoutTheSnapshot is the table's third row and
// #38's first verification check in one: score.json is a cache, so deleting or
// corrupting it costs nothing, and the rebuild is byte-identical.
func TestRecoveryRebuildsFromLogWithoutTheSnapshot(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	first := submitAs(t, s, "keep the build green", Provenance{Source: "agent", SourcePanel: "p1", SourceProfile: "claude", SourceCwd: "/work"})
	submit(t, s, "ask before force-pushing")
	if err := s.Reinforce(first.Id, "agent"); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}
	// A reword too, so the rebuild has to replay an alias as well as a counter.
	writeMD(t, dir, "- ["+first.Id+"] keep the build green, always\n- [b0b0b0] ask before force-pushing\n")
	reconcile(t, s)
	want := readFile(t, dir, scoreJSON)
	s.Close()

	if err := os.Remove(filepath.Join(dir, scoreJSON)); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}
	re := openStore(t, dir)
	if got := readFile(t, dir, scoreJSON); got != want {
		t.Fatalf("cache rebuilt from the log differs:\n got: %s\nwant: %s", got, want)
	}
	if re.Len() != 2 {
		t.Fatalf("entries after the rebuild = %d, want 2", re.Len())
	}
	got := re.Render(Context{})[0]
	if got.Text != "keep the build green, always" || len(got.Aliases) != 1 || got.Aliases[0] != "keep the build green" {
		t.Fatalf("rebuilt entry = %+v, want the reworded text with the old wording as an alias", got)
	}
	if got.Provenance.SourcePanel != "p1" || got.Provenance.SourceProfile != "claude" {
		t.Fatalf("rebuilt provenance = %+v, want the submitting panel's", got.Provenance)
	}
}

// TestRecoveryTornLogTailIsCountedNotFatal is the table's fourth row: a torn
// append is the only damage a crash can do to an append-only file, so it is
// skipped and counted rather than failing the boot.
func TestRecoveryTornLogTailIsCountedNotFatal(t *testing.T) {
	dir := t.TempDir()
	writeMD(t, dir, "- [abc123] survives the tear\n")
	log := `{"schema":1,"event":"submitted","id":"abc123","at":"2026-08-30T00:00:00Z","text":"survives the tear","source":"user","provenance":{"source":"user"}}` + "\n" +
		`{"schema":1,"event":"user-sig` // crash mid-append
	if err := os.WriteFile(filepath.Join(dir, scoreEvents), []byte(log), 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}

	s := openStore(t, dir)
	if s.Len() != 1 {
		t.Fatalf("entries = %d, want the one the intact line describes", s.Len())
	}
	if h, d := s.Health(), s.Boot(); h.TornEvents != 1 || d.Admitted != 0 {
		t.Fatalf("health %+v / boot %+v, want exactly one torn line and no re-admission", h, d)
	}
}

// TestRecoveryFirstRunIsEmpty is the table's last row: none of the files exist,
// so there is nothing to recover and nothing to report.
func TestRecoveryFirstRunIsEmpty(t *testing.T) {
	s := openStore(t, filepath.Join(t.TempDir(), "fresh"))
	if s.Len() != 0 {
		t.Fatalf("entries on a first run = %d, want 0", s.Len())
	}
	if d, h := s.Boot(), s.Health(); d != (Delta{}) || h != (Health{}) {
		t.Fatalf("recovery on a first run = %+v / %+v, want nothing to report", d, h)
	}
}

// TestReconcileReword is the live-editor table's second row: the operator
// changed a line's wording. The entry is superseded, the old wording survives as
// an alias so a repeat of it still folds (I4), and the edit counts as a user
// signal — the strongest signal there is (#38 §4).
func TestReconcileReword(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "run the tests", Provenance{Source: "agent", SourcePanel: "p1"})

	writeMD(t, dir, "- ["+e.Id+"] run the tests before pushing\n")
	d := reconcile(t, s)

	got := s.Render(Context{})[0]
	if got.Id != e.Id {
		t.Fatalf("reword changed the id: %s -> %s", e.Id, got.Id)
	}
	if got.Text != "run the tests before pushing" {
		t.Fatalf("text = %q, want the operator's wording — the user's text wins", got.Text)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "run the tests" {
		t.Fatalf("aliases = %v, want the superseded wording kept for folding", got.Aliases)
	}
	if got.Reinforcements != 1 {
		t.Fatalf("reinforcements = %d, want the edit counted as a user signal", got.Reinforcements)
	}
	if got.Provenance.SourcePanel != "p1" {
		t.Fatalf("provenance = %+v, want the original submitter kept", got.Provenance)
	}
	if d.Superseded != 1 {
		t.Fatalf("pass = %+v, want one supersede", d)
	}
	// The history shows it as a user action.
	var edited *event
	for i, ev := range events(t, dir) {
		if ev.Event == EventEdited {
			edited = &events(t, dir)[i]
		}
	}
	if edited == nil || edited.Source != "user" || edited.Text != "run the tests before pushing" {
		t.Fatalf("edited event = %+v, want the user's new wording", edited)
	}

	// A second, identical reconcile is a no-op: no event, no second alias.
	if d := reconcile(t, s); d != (Delta{}) {
		t.Fatalf("pass over an unchanged file = %+v, want nothing changed", d)
	}
}

// TestReconcileUnidentifiedLine is the live-editor table's third row: the
// operator typed a bullet with no id. It becomes a user-sourced entry and the
// server writes the id back — touching that line only, because every other byte
// of score.md is the operator's.
func TestReconcileUnidentifiedLine(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	writeMD(t, dir, "# my notes\n- never rebase main\nremember the milk\n\n")

	reconcile(t, s)
	entries := s.Render(Context{})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want only the bullet — prose and comments are not memory", len(entries))
	}
	e := entries[0]
	if !idRe.MatchString(e.Id) || e.Text != "never rebase main" || e.Provenance.Source != "user" {
		t.Fatalf("entry = %+v, want a fresh hex id on user-sourced text", e)
	}

	want := "# my notes\n- [" + e.Id + "] never rebase main\nremember the milk\n\n"
	if got := readFile(t, dir, scoreMD); got != want {
		t.Fatalf("score.md after the id write-back:\n got: %q\nwant: %q", got, want)
	}
	if !hasEvent(events(t, dir), EventSubmitted, e.Id) {
		t.Error("the admitted line was not logged")
	}

	// The write-back is the store's own, so the next read must not treat it as a
	// fresh operator edit.
	if d := reconcile(t, s); d.Admitted != 0 {
		t.Fatalf("pass = %+v, want the write-back not re-admitted", d)
	}
}

// TestReconcileDeletedLine is the live-editor table's last row: the operator
// deleted a line while the daemon runs. The entry retires — recorded, never
// destroyed (I7) — and its id stays spent.
func TestReconcileDeletedLine(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submit(t, s, "keep me")
	drop := submit(t, s, "delete me")

	writeMD(t, dir, "- ["+keep.Id+"] keep me\n")
	d := reconcile(t, s)
	if s.Len() != 1 || s.Render(Context{})[0].Id != keep.Id {
		t.Fatalf("entries = %+v, want only the kept one", s.Render(Context{}))
	}
	if !hasEvent(events(t, dir), EventRetired, drop.Id) {
		t.Errorf("no retired event for %s", drop.Id)
	}
	if d.Retired != 1 {
		t.Fatalf("pass = %+v, want one retirement", d)
	}

	// Restoring the line brings the entry back under its own id, rather than
	// leaving the operator's text on the floor.
	writeMD(t, dir, "- ["+keep.Id+"] keep me\n- ["+drop.Id+"] delete me\n")
	reconcile(t, s)
	if s.Len() != 2 {
		t.Fatalf("entries after restoring the line = %d, want 2", s.Len())
	}
}

// TestIdsAreNeverReusedAcrossARetire is the id-burning rule (#40, Ellis' note).
// The store must not reissue an id it has retired: the log is keyed by id, so a
// reissue would graft the retired entry's history onto a new one. The check is
// deliberately white-box, because the collision it prevents cannot be forced
// through the public API — what matters is precisely that the retired id stays
// in the burned set, which is the difference between this store and the S0 one
// that consulted only the live entries.
func TestIdsAreNeverReusedAcrossARetire(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "short lived")

	writeMD(t, dir, "# all gone\n")
	reconcile(t, s)
	if s.indexLocked(e.Id) >= 0 {
		t.Fatalf("%s is still live after its line was deleted", e.Id)
	}
	if _, burned := s.burned[e.Id]; !burned {
		t.Fatalf("%s left the burned set when it retired — a later draw could reissue it", e.Id)
	}

	// Every id the log ever named is burned again on the next boot, so the rule
	// survives a restart as well as a retire.
	s.Close()
	re := openStore(t, dir)
	if _, burned := re.burned[e.Id]; !burned {
		t.Fatalf("%s was forgotten across a restart; the log names it, so it stays spent", e.Id)
	}
	if re.Len() != 0 {
		t.Fatalf("entries after the restart = %d, want 0", re.Len())
	}

	// And a duplicated line never lands two entries on one id.
	writeMD(t, dir, "- [c0ffee] one\n- [c0ffee] two\n")
	reconcile(t, re)
	entries := re.Render(Context{})
	if len(entries) != 2 || entries[0].Id == entries[1].Id {
		t.Fatalf("duplicated id yielded %+v, want two entries with distinct ids", entries)
	}
}

// TestSubmitReportsTheOutcomeItActuallyHad is Page's note on #40: the reported
// result must match what happened on disk.
func TestSubmitReportsTheOutcomeItActuallyHad(t *testing.T) {
	t.Run("cache failure is not a failed submission", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		// score.json as a directory: the snapshot's rename can never land, while
		// the append-only files stay perfectly writable.
		if err := os.Remove(filepath.Join(dir, scoreJSON)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove snapshot: %v", err)
		}
		if err := os.Mkdir(filepath.Join(dir, scoreJSON), 0o700); err != nil {
			t.Fatalf("mkdir over the snapshot: %v", err)
		}

		e := submitAs(t, s, "durable in two files", Provenance{Source: "user"})
		if s.Len() != 1 {
			t.Fatalf("entries = %d, want the submission", s.Len())
		}
		if !strings.Contains(readFile(t, dir, scoreMD), e.Text) {
			t.Error("the entry Submit acknowledged is not in score.md")
		}
		if !hasEvent(events(t, dir), EventSubmitted, e.Id) {
			t.Error("the entry Submit acknowledged is not in the log")
		}
	})

	t.Run("log failure is a failed submission", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		before := readFile(t, dir, scoreMD)
		// The log as a directory: the very first durable write fails, so nothing
		// may reach memory or score.md.
		if err := os.Mkdir(filepath.Join(dir, scoreEvents), 0o700); err != nil {
			t.Fatalf("mkdir over the log: %v", err)
		}

		if _, _, err := s.Submit("never stored", Provenance{Source: "user"}); err == nil {
			t.Fatal("Submit reported success though nothing could be logged")
		}
		if s.Len() != 0 {
			t.Fatalf("entries = %d, want the failed submission absent from memory", s.Len())
		}
		if got := readFile(t, dir, scoreMD); got != before {
			t.Errorf("score.md changed under a failed submission:\n%q", got)
		}
	})
}

// TestReconcileGateSkipsAnUnchangedFile proves the cadence decision: reconcile
// runs on the dispatch path, so a file that has not moved must not be re-parsed.
// Memory is doctored with a wording the file does not carry — a pass that
// actually read score.md would supersede it.
func TestReconcileGateSkipsAnUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	submit(t, s, "as written")
	// Age the file out of the always-stale window (see staleWindow), then let one
	// reconcile record the aged fingerprint. Without this the gate would keep
	// re-reading, which is the whole point of the window and not what is under
	// test here.
	ageMD(t, dir)
	reconcile(t, s)

	s.mu.Lock()
	s.entries[0].Text = "sentinel"
	s.mu.Unlock()

	d := reconcile(t, s)
	if got := s.Render(Context{})[0].Text; got != "sentinel" {
		t.Fatalf("text = %q: an untouched score.md was re-parsed anyway", got)
	}
	if d != (Delta{}) {
		t.Fatalf("pass = %+v, want nothing done for an untouched file", d)
	}

	// Touch it, and the very next read sees the file again.
	writeMD(t, dir, "- ["+s.Render(Context{})[0].Id+"] as written\n")
	reconcile(t, s)
	if got := s.Render(Context{})[0].Text; got != "as written" {
		t.Fatalf("text = %q, want the file's wording once it moved", got)
	}
}

// TestCrashBetweenWritesKeepsWhatTheUserTyped is #38's second verification
// check. There is no transaction across the three files, so both crash windows
// are constructed by hand and the recovery table has to land somewhere the
// operator would recognise.
func TestCrashBetweenWritesKeepsWhatTheUserTyped(t *testing.T) {
	t.Run("md written, log not", func(t *testing.T) {
		// The operator sees the line, so the line is the truth: it is kept.
		dir := t.TempDir()
		writeMD(t, dir, "- [feedab] the half-written one\n")
		s := openStore(t, dir)
		if s.Len() != 1 || s.Render(Context{})[0].Text != "the half-written one" {
			t.Fatalf("entries = %+v, want the text the user can see kept", s.Render(Context{}))
		}
		if !hasEvent(events(t, dir), EventSubmitted, "feedab") {
			t.Error("the kept line was not logged, so the next boot would guess again")
		}
	})

	t.Run("log written, md not", func(t *testing.T) {
		// The operator never saw it, and score.md is the truth for existence, so
		// it retires — and its text stays in the log either way.
		dir := t.TempDir()
		writeMD(t, dir, "# empty\n")
		log := `{"schema":1,"event":"submitted","id":"feedab","at":"2026-08-30T00:00:00Z","text":"the unseen one","source":"agent","provenance":{"source":"agent"}}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, scoreEvents), []byte(log), 0o600); err != nil {
			t.Fatalf("write events: %v", err)
		}

		s := openStore(t, dir)
		if s.Len() != 0 {
			t.Fatalf("entries = %d, want the unseen entry retired — the user's file wins", s.Len())
		}
		if !hasEvent(events(t, dir), EventRetired, "feedab") {
			t.Error("the retirement was not recorded")
		}
		if !strings.Contains(readFile(t, dir, scoreEvents), "the unseen one") {
			t.Error("the retired text was destroyed; I7 says it stays in the log")
		}
	})
}

// TestLiveEditIsVisibleToTheNextRead is #38's third verification check, and the
// reason Reconcile needed a caller at all: all three edit kinds in one save,
// reflected without a restart, each recorded as a user action.
func TestLiveEditIsVisibleToTheNextRead(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	reworded := submitAs(t, s, "say please", Provenance{Source: "agent", SourcePanel: "p1"})
	deleted := submitAs(t, s, "say thanks", Provenance{Source: "agent", SourcePanel: "p1"})

	// One editor save: reword one line, delete another, add a third with no id.
	writeMD(t, dir, "- ["+reworded.Id+"] say please, every time\n- ask before deleting a branch\n")
	reconcile(t, s)

	block := s.RenderBlock(Context{Panel: "p1"})
	if !strings.Contains(block, "say please, every time") {
		t.Errorf("the reworded line is not in the next brief:\n%s", block)
	}
	if strings.Contains(block, "say thanks") {
		t.Errorf("the deleted line is still in the next brief:\n%s", block)
	}
	if !strings.Contains(block, "ask before deleting a branch") {
		t.Errorf("the added line is not in the next brief:\n%s", block)
	}

	evs := events(t, dir)
	if !hasEvent(evs, EventEdited, reworded.Id) || !hasEvent(evs, EventRetired, deleted.Id) {
		t.Fatalf("history does not show the reword and the deletion: %+v", evs)
	}
	var added string
	for _, e := range s.Render(Context{}) {
		if e.Text == "ask before deleting a branch" {
			added = e.Id
		}
	}
	if added == "" || !hasEvent(evs, EventSubmitted, added) {
		t.Fatalf("the added line has no submitted event: %+v", evs)
	}
	for _, ev := range evs {
		if ev.Event == EventEdited && ev.Source != "user" {
			t.Errorf("edited event is not attributed to the user: %+v", ev)
		}
	}
}

// TestReconcileSanitisesOperatorText keeps the S0 guarantee on the path R1
// opened: reconciled text is operator input, so it is scrubbed exactly like a
// submission before it can ride a brief into a panel's pty.
func TestReconcileSanitisesOperatorText(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	writeMD(t, dir, "- \x1b]52;c;cGF5bG9hZA==\x07 planted by an editor\n")
	reconcile(t, s)

	e := s.Render(Context{})[0]
	for _, s := range []string{e.Text, s.RenderBlock(Context{}), readFile(t, dir, scoreMD), readFile(t, dir, scoreEvents)} {
		if strings.ContainsAny(s, "\x1b\x07") {
			t.Fatalf("a control byte survived reconcile: %q", s)
		}
	}
	if !strings.Contains(e.Text, "planted by an editor") {
		t.Fatalf("text = %q, want the readable remainder kept", e.Text)
	}
}

// TestReinforceDoesNotResurrectADeletedEntry is the write path's half of I3:
// a mutation reconciles first, so it never acts on a view the operator has
// already moved past.
func TestReinforceDoesNotResurrectADeletedEntry(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "about to go")

	writeMD(t, dir, "# gone\n")
	if err := s.Reinforce(e.Id, "user"); err == nil {
		t.Fatal("Reinforce succeeded on an entry the operator had deleted")
	}
	if s.Len() != 0 {
		t.Fatalf("entries = %d, want the deletion to have stuck", s.Len())
	}
}
