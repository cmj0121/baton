package score

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file covers the review round on #40: the operator-side weight cap, the
// bounded staleness gate, the missing-file re-projection, the post-write
// fingerprint race, and the batched reconcile append.

// TestOversizedOperatorLineIsKeptButWithheld is the asymmetry documented at
// maxEntryRunes. A submission over the cap is refused, because the submitter can
// retry. An operator's line over the cap is theirs — I3 says their text wins —
// so it is kept verbatim in the file and in the store, and only its injection is
// withheld, with the withholding counted so the server can say so.
func TestOversizedOperatorLineIsKeptButWithheld(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	huge := strings.Repeat("x", 50_000)
	writeMD(t, dir, "- [abc123] a normal note\n- [def456] "+huge+"\n")

	reconcile(t, s)

	// Kept: both entries are in the store, and the file is untouched.
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want both lines held", s.Len())
	}
	if got := readFile(t, dir, scoreMD); !strings.Contains(got, huge) {
		t.Error("the operator's long line was rewritten; their text must win")
	}

	// Withheld: it never reaches a brief, and the block stays small.
	block := s.RenderBlock(Context{Panel: "p1"})
	if strings.Contains(block, huge[:1000]) {
		t.Fatalf("the over-long entry reached the injected block (%d bytes)", len(block))
	}
	if !strings.Contains(block, "a normal note") {
		t.Fatalf("the normal entry was withheld too:\n%s", block)
	}
	if len(block) > 1000 {
		t.Fatalf("injected block is %d bytes; the weight cap did not hold", len(block))
	}

	// Visible: silence would be worse than the bug.
	if h := s.Health(); h.Oversized != 1 {
		t.Fatalf("health = %+v, want one entry counted as too long to inject", h)
	}

	// And the gauge falls again when the operator shortens it.
	writeMD(t, dir, "- [abc123] a normal note\n- [def456] now it fits\n")
	reconcile(t, s)
	if h := s.Health(); h.Oversized != 0 {
		t.Fatalf("health = %+v, want the gauge cleared", h)
	}
	if got := s.RenderBlock(Context{}); !strings.Contains(got, "now it fits") {
		t.Fatalf("the shortened entry is still withheld:\n%s", got)
	}
}

// TestSameSizeEditInsideOneMtimeTickIsSeen is the coarse-granularity case: an
// operator fixes a one-character typo, and the filesystem reports the same size
// and the same mtime. The gate must not miss it on the READ path, where a miss
// would otherwise persist until the next write rather than until the next read.
func TestSameSizeEditInsideOneMtimeTickIsSeen(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	writeMD(t, dir, "- [abc123] one typo\n")
	reconcile(t, s)
	// Age it so the always-stale window is not what catches the edit — the point
	// is that the fingerprint itself is wide enough on a real filesystem.
	ageMD(t, dir)
	reconcile(t, s)
	pinned := time.Now().Add(-time.Hour)

	// Same byte count, same mtime pinned back to what the store last saw.
	path := filepath.Join(dir, scoreMD)
	if err := os.WriteFile(path, []byte("- [abc123] two typo\n"), 0o600); err != nil {
		t.Fatalf("write score.md: %v", err)
	}
	if err := os.Chtimes(path, pinned, pinned); err != nil {
		t.Fatalf("chtimes score.md: %v", err)
	}

	reconcile(t, s)
	if got := s.Render(Context{})[0].Text; got != "two typo" {
		t.Fatalf("text = %q: a same-size edit inside one mtime tick was missed on the read path", got)
	}
}

// TestMissingScoreMDIsReprojected separates the two statements the recovery
// table used to conflate. An EMPTY score.md is the operator saying "forget
// these"; a MISSING one is a missing file — an rsync that skipped it, a restore
// without it — and reading that as "forget everything" takes the whole fleet
// memory out behind one log line.
func TestMissingScoreMDIsReprojected(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	first := submitAs(t, s, "keep the build green", Provenance{Source: "agent", SourcePanel: "p1"})
	submit(t, s, "ask before force-pushing")

	if err := os.Remove(filepath.Join(dir, scoreMD)); err != nil {
		t.Fatalf("remove score.md: %v", err)
	}
	d := reconcile(t, s)

	if s.Len() != 2 {
		t.Fatalf("entries = %d, want both survived a file that went missing", s.Len())
	}
	md := readFile(t, dir, scoreMD)
	if !strings.Contains(md, "- ["+first.Id+"] keep the build green") {
		t.Fatalf("score.md was not re-projected:\n%s", md)
	}
	if !strings.Contains(md, "# This file is baton's fleet memory") {
		t.Fatalf("the re-projection dropped the header that teaches the format:\n%s", md)
	}
	if d.Reprojected != 2 || d.Retired != 0 {
		t.Fatalf("pass = %+v, want two re-projections and no retirement", d)
	}
	// The provenance survives, because nothing was retired and re-admitted.
	if got := s.Render(Context{})[0]; got.Provenance.SourcePanel != "p1" {
		t.Fatalf("entry = %+v, want the original submitter kept", got)
	}

	// The same file, present but emptied, still means what it always meant.
	writeMD(t, dir, "# I meant it\n")
	d = reconcile(t, s)
	if s.Len() != 0 {
		t.Fatalf("entries = %d, want an emptied file to retire them", s.Len())
	}
	if d.Retired != 2 {
		t.Fatalf("pass = %+v, want two retirements", d)
	}
}

// TestMissingScoreMDOnAFreshStoreJustSeeds keeps the first-run path honest: no
// entries means nothing to re-project, only the header to write.
func TestMissingScoreMDOnAFreshStoreJustSeeds(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if s.Len() != 0 {
		t.Fatalf("entries = %d, want 0", s.Len())
	}
	if d := s.Boot(); d.Reprojected != 0 {
		t.Fatalf("boot recovery = %+v, want a first run to re-project nothing", d)
	}
	if md := readFile(t, dir, scoreMD); !strings.Contains(md, "- [e7f3a2]") {
		t.Fatalf("the header should still teach the entry format:\n%s", md)
	}
}

// TestRenameSaveAfterAnAppendIsCaught is the post-write fingerprint race. The
// store appends to score.md; an editor's rename-save lands immediately after and
// clobbers the appended line. Fingerprinting the path at that moment would
// record the clobbering file as the store's own work and the gate would report
// "in sync" forever — the fleet told about an entry the file does not have, with
// no reconcile ever noticing.
func TestRenameSaveAfterAnAppendIsCaught(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	submit(t, s, "the appended one")

	// The editor's save: a fresh file, written and renamed over the target, with
	// the appended line gone. Its mtime is pinned into the past so only the
	// dropped fingerprint — not the always-stale window — can catch it.
	tmp := filepath.Join(dir, "editor.tmp")
	if err := os.WriteFile(tmp, []byte("# the operator's own file\n"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, scoreMD)); err != nil {
		t.Fatalf("rename over score.md: %v", err)
	}
	ageMD(t, dir)

	d := reconcile(t, s)
	if s.Len() != 0 {
		t.Fatalf("entries = %d, want the clobbered append noticed", s.Len())
	}
	if d.Retired != 1 {
		t.Fatalf("pass = %+v, want the clobbered entry retired", d)
	}
}

// TestReconcileBatchesItsAppends guards the syscall pattern, not the log: a pass
// over a thousand changed lines writes one batch and fsyncs once. Per-line
// durability measured at ~2.7 ms per line on local APFS, so an unbatched pass
// over this file takes seconds — on the dispatch path, holding the store mutex
// and every other score-touching connection with it. The bound below sits an
// order of magnitude under that and an order of magnitude over a batched pass.
func TestReconcileBatchesItsAppends(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive")
	}
	dir := t.TempDir()
	s := openStore(t, dir)

	const n = 1000
	var md strings.Builder
	for i := range n {
		// Distinct wordings on purpose: identical ones fold into a single entry
		// now, and this test needs a thousand CHANGED lines in one pass.
		md.WriteString("- entry number ")
		md.WriteString(strconv.Itoa(i))
		md.WriteString(strings.Repeat("x", 1+i%5))
		md.WriteString("\n")
	}
	writeMD(t, dir, md.String())
	reconcile(t, s) // admits or retires in one batch
	if s.Len() != n {
		t.Fatalf("entries = %d, want %d", s.Len(), n)
	}

	writeMD(t, dir, "# all gone\n")
	start := time.Now()
	d := reconcile(t, s) // admits or retires in one batch
	elapsed := time.Since(start)

	if s.Len() != 0 {
		t.Fatalf("entries = %d, want 0", s.Len())
	}
	if d.Retired != n {
		t.Fatalf("pass = %+v, want %d retirements", d, n)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("emptying %d entries took %s; the pass is fsyncing per line again", n, elapsed)
	}
	t.Logf("emptied %d entries in %s", n, elapsed)

	// The log still carries one event per change — only the syscall pattern was
	// batched, and every line is still its own parsable record.
	var retired int
	for _, ev := range events(t, dir) {
		if ev.Event == EventRetired {
			retired++
		}
	}
	if retired != n {
		t.Fatalf("log holds %d retired events, want %d — batching must not lose records", retired, n)
	}
}

// TestPreR1LogRecordAdoptsTextWithoutAUserSignal covers the log records that
// predate events carrying their text. Replaying one leaves the entry's wording
// unknown, and score.md supplies it — but unknown is not "was empty", so
// adopting it must not look like an operator edit. A manufactured user signal
// there would feed the one thing invariant I6 says agents cannot reach alone.
//
// The adoption is still RECORDED, sourced to the recovery pass: an unrecorded
// one would be redone at every boot, and the wording would exist only in
// score.md — so deleting the line would retire an entry whose text the log
// never learned, which is a soft edge on I7.
func TestPreR1LogRecordAdoptsTextWithoutAUserSignal(t *testing.T) {
	dir := t.TempDir()
	writeMD(t, dir, "- [abc123] the wording the file has\n")
	// A pre-R1 record: no text, and the source as a plain field with no
	// provenance object.
	old := `{"schema":1,"event":"submitted","id":"abc123","at":"2026-08-30T00:00:00Z","source":"agent"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, scoreEvents), []byte(old), 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}

	s := openStore(t, dir)
	got := s.Render(Context{})[0]
	if got.Text != "the wording the file has" {
		t.Fatalf("text = %q, want the file's wording adopted", got.Text)
	}
	if got.Reinforcements != 0 {
		t.Fatalf("reinforcements = %d, want no manufactured user signal", got.Reinforcements)
	}
	if len(got.Aliases) != 0 {
		t.Fatalf("aliases = %v, want none — there was no prior wording to supersede", got.Aliases)
	}
	if d := s.Boot(); d.Superseded != 0 || d.Adopted != 1 {
		t.Fatalf("boot recovery = %+v, want the wording adopted, not superseded", d)
	}
	// The plain source field is the only provenance such a record carries, and
	// R3 ranks on it, so it must survive rather than default to empty.
	if got.Provenance.Source != "agent" {
		t.Fatalf("provenance = %+v, want the source the old record did carry", got.Provenance)
	}

	// Recorded, and attributed to nobody: replay must not read it as a signal.
	var edits int
	for _, ev := range events(t, dir) {
		if ev.Event != EventEdited {
			continue
		}
		edits++
		if ev.Source == "user" {
			t.Fatalf("the adoption was attributed to the operator: %+v", ev)
		}
		if ev.Text != "the wording the file has" {
			t.Fatalf("edited event = %+v, want the adopted wording", ev)
		}
	}
	if edits != 1 {
		t.Fatalf("%d edited events, want the adoption recorded exactly once", edits)
	}

	// Reopening replays that record, so the wording is known and nothing is
	// adopted a second time — and the log now holds the text the entry carries.
	s.Close()
	re := openStore(t, dir)
	got = re.Render(Context{})[0]
	if got.Text != "the wording the file has" || got.Reinforcements != 0 {
		t.Fatalf("reopened entry = %+v, want the recorded wording and no signal", got)
	}
	if before, after := len(events(t, dir)), 2; before != after {
		t.Fatalf("log holds %d events, want %d — the adoption was redone", before, after)
	}
}

// TestReplayPlacesARestoredEntryOnce checks the replay's own output rather than
// what the following reconcile makes of it: a delete-then-restore names the id
// in two submitted events, and the rebuilt list must still hold one entry.
// Open's reconcile hides a duplicate here today, which is exactly why R7's
// compaction would find it the hard way.
func TestReplayPlacesARestoredEntryOnce(t *testing.T) {
	dir := t.TempDir()
	log := strings.Join([]string{
		`{"schema":1,"event":"submitted","id":"abc123","at":"2026-08-30T00:00:00Z","text":"first life","source":"user","provenance":{"source":"user"}}`,
		`{"schema":1,"event":"retired","id":"abc123","at":"2026-08-30T00:00:01Z"}`,
		`{"schema":1,"event":"submitted","id":"abc123","at":"2026-08-30T00:00:02Z","text":"second life","source":"user","provenance":{"source":"user"}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, scoreEvents), []byte(log), 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}

	s := newStore(dir)
	if err := s.replayLocked(); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(s.entries) != 1 {
		t.Fatalf("replay yielded %+v, want one entry", s.entries)
	}
	if s.entries[0].Text != "second life" {
		t.Fatalf("entry = %+v, want the wording of its second life", s.entries[0])
	}
}

// TestParseLineScrubsTheId closes the id half of the score.md trust boundary:
// Entry.Id rides score.list into a cockpit that draws it into a terminal, so a
// control byte is no safer there than in a wording.
func TestParseLineScrubsTheId(t *testing.T) {
	id, text, ok := parseLine("- [ab\x1bc123] planted\n")
	if !ok {
		t.Fatal("the line should still parse; only the id is scrubbed")
	}
	if strings.ContainsRune(id, 0x1b) {
		t.Fatalf("id = %q, want the escape dropped", id)
	}
	if text != "planted" {
		t.Fatalf("text = %q", text)
	}
	if _, _, ok := parseLine("- [\x1b\x07] nothing left"); ok {
		t.Error("an id that scrubs away to nothing must not parse as an entry")
	}
}

// TestCacheWriteFailureIsCounted keeps the ops blind spot lit: the store is
// right not to fail a durable mutation over its cache, but a rising count is the
// early symptom of the disk that will break the next append.
func TestCacheWriteFailureIsCounted(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if err := os.Remove(filepath.Join(dir, scoreJSON)); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove snapshot: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, scoreJSON), 0o700); err != nil {
		t.Fatalf("mkdir over the snapshot: %v", err)
	}

	submit(t, s, "still durable")
	if h := s.Health(); h.CacheWriteFailures == 0 {
		t.Fatalf("health = %+v, want the failed cache write counted", h)
	}
}

// TestReattributionIsCounted covers the LOW finding: an id the log knows, whose
// submission was torn away, re-enters as the operator's — so the agent that
// really submitted it is lost. R3 ranks on provenance, so the loss is counted
// rather than folded into the ordinary admission count.
func TestReattributionIsCounted(t *testing.T) {
	dir := t.TempDir()
	writeMD(t, dir, "- [abc123] came back as the operator's\n")
	// A log that names the id without a surviving submission: the torn tail.
	log := `{"schema":1,"event":"user-signal","id":"abc123","at":"2026-08-30T00:00:00Z","source":"user"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, scoreEvents), []byte(log), 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}

	s := openStore(t, dir)
	if d := s.Boot(); d.Admitted != 1 || d.Reattributed != 1 {
		t.Fatalf("boot recovery = %+v, want one admission whose attribution was lost", d)
	}
	// A line the log never knew at all is an ordinary admission, not a lost one.
	writeMD(t, dir, "- [abc123] came back as the operator's\n- [feedab] brand new\n")
	if d := reconcile(t, s); d.Admitted != 1 || d.Reattributed != 0 {
		t.Fatalf("pass = %+v, want the new line admitted without a reattribution", d)
	}
}

// TestViewIsOneConsistentLook covers the read seam: View reconciles the
// operator's file and answers from that same pass, under one hold of the store
// lock, so a reply can never pair an entry total with a rendered total taken
// from a different reading of score.md.
func TestViewIsOneConsistentLook(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	submit(t, s, "a normal note")

	// An editor save the store has not seen: the submitted line replaced by one
	// under an id of the operator's own, plus a line over the weight cap.
	writeMD(t, dir, "- [abc123] taken as the operator's\n- "+strings.Repeat("x", 50_000)+"\n")

	v, err := s.View(Context{Panel: "p1"})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if v.Total != 2 {
		t.Fatalf("view = %+v, want both lines held by the store", v)
	}
	if len(v.Entries) != 1 || v.Entries[0].Text != "taken as the operator's" {
		t.Fatalf("entries = %+v, want only the injectable one", v.Entries)
	}
	if v.Health.Oversized != 1 {
		t.Fatalf("health = %+v, want the withheld line counted", v.Health)
	}
	if v.Delta.Admitted != 2 || v.Delta.Retired != 1 {
		t.Fatalf("delta = %+v, want the pass's own work reported", v.Delta)
	}
	if !strings.Contains(v.Block, "taken as the operator's") || strings.Contains(v.Block, "xxxx") {
		t.Fatalf("block = %q, want the injectable entry and nothing withheld", v.Block)
	}

	// A second look over an untouched file reports a pass that did nothing,
	// rather than repeating the first one's numbers.
	ageMD(t, dir)
	if v, err := s.View(Context{}); err != nil || v.Delta != (Delta{}) {
		t.Fatalf("second view = %+v (%v), want a pass that changed nothing", v.Delta, err)
	}
}

// TestViewServesTheLastReadWhenTheFileCannotBeRead is "stale beats absent" at
// the seam: a score.md that has become unreadable yields the error for the
// server to log AND the last view the store did manage to read, because a brief
// on slightly old memory beats no brief at all.
func TestViewServesTheLastReadWhenTheFileCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	submit(t, s, "still readable")

	// score.md as a directory: every read of it fails for as long as it lasts.
	path := filepath.Join(dir, scoreMD)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove score.md: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir over score.md: %v", err)
	}

	v, err := s.View(Context{})
	if err == nil {
		t.Fatal("View hid an unreadable score.md")
	}
	if len(v.Entries) != 1 || v.Entries[0].Text != "still readable" {
		t.Fatalf("entries = %+v, want the last view the store did read", v.Entries)
	}
	if v.Unlocked {
		t.Fatal("a locked store reported itself unlocked")
	}
}

// TestViewOnTheDisabledStoreIsInert keeps the nil-store contract on the seam
// every read now goes through.
func TestViewOnTheDisabledStoreIsInert(t *testing.T) {
	var disabled *Store
	v, err := disabled.View(Context{})
	if err != nil || v.Entries != nil || v.Block != "" || v.Total != 0 {
		t.Fatalf("disabled view = %+v (%v), want the zero view", v, err)
	}
}
