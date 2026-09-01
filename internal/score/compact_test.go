package score

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file is compaction's (#46 R7). Every number it exercises is pinned in
// BOTH directions: a log below the threshold is asserted NOT to be rewritten,
// and a record compaction must keep is asserted to still be there. A test that
// only proves the mechanism fires passes just as happily on a threshold someone
// has loosened by two orders of magnitude, which is the lesson R6 paid for.

// padLog appends synthetic fold records for id until score-events.jsonl passes
// want bytes, and returns the size it reached. The records are real ones — the
// shape a reinforced entry actually accumulates — so a log grown this way costs
// the replay what a log grown by use would.
//
// RemovedLine is false on every one of them, which is what keeps the padding out
// of the owed derivation: a fold that took no line out of score.md owes no
// removal, so a padded store's boot has no debt and compaction is free to run.
// The tests that want a debt make one on purpose.
func padLog(t *testing.T, dir, id, text string, want int64) int64 {
	t.Helper()
	path := filepath.Join(dir, scoreEvents)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open the log: %v", err)
	}
	defer func() { _ = f.Close() }()

	prov := Provenance{Source: SourceAgent, SourcePanel: "p1", SourceProfile: "claude", SourceCwd: "/work/repo"}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// One record, marshalled once. Every iteration writes the same bytes — the id,
	// the text and the stamp are all loop-invariant — so marshalling inside the
	// loop re-encoded one identical record about 87,000 times to reach 8 MiB.
	rec, err := json.Marshal(foldEvent(id, text, prov, at, false, false))
	if err != nil {
		t.Fatalf("marshal the padding record: %v", err)
	}
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat the log: %v", err)
	}
	size := fi.Size()
	var buf strings.Builder
	for size+int64(buf.Len()) <= want {
		buf.Write(rec)
		buf.WriteByte('\n')
	}
	if _, err := f.WriteString(buf.String()); err != nil {
		t.Fatalf("pad the log: %v", err)
	}
	return size + int64(buf.Len())
}

// compactNow runs one compaction under the store's own lock and returns how many
// records it wrote, failing the test on error. maxBytes is zero, so every log is
// over the threshold and the refusal under test is whichever other one the case
// is about.
//
// The three sites that do NOT use it are the ones it cannot serve: two want the
// error itself, and two read s.burned or s.seq under the SAME hold as the
// compaction, which is the point of those tests. Every caller keeps its own
// count check — the helper removes the locking, not the assertion.
func compactNow(t *testing.T, s *Store) int {
	t.Helper()
	s.mu.Lock()
	written, err := s.compactLocked(0)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	return written
}

// seedForCompaction leaves a directory holding one live entry with a log of
// roughly want bytes, closed and ready to reopen.
func seedForCompaction(t *testing.T, want int64) (dir string, id string, size int64) {
	t.Helper()
	dir = t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "the fleet asks before it force-pushes", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	s.Close()
	return dir, e.Id, padLog(t, dir, e.Id, e.Text, want)
}

// TestCompactionFiresPastTheThresholdAndNotBelowIt pins compactAtBytes through
// the door that uses it, in both directions and against the real constant.
//
// The silent half is the one worth having. Compaction destroys the history it
// replaces, so a threshold that has drifted downward — by a tuning, by a unit
// mistake, by a refactor that compared megabytes to bytes — quietly starts
// throwing away the log of stores it was never meant to touch, and every test
// that only checks the rewrite HAPPENS still passes.
func TestCompactionFiresPastTheThresholdAndNotBelowIt(t *testing.T) {
	// Two sizes, because the threshold is documented as one the log must EXCEED
	// and a boundary written `<` instead of `<=` is invisible to any fixture that
	// never lands on it exactly.
	for _, tc := range []struct {
		name string
		size int64
	}{
		{"a log under the threshold is not rewritten", compactAtBytes - (1 << 16)},
		{"a log exactly at the threshold is not rewritten", compactAtBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, size := seedForCompaction(t, tc.size)
			// padLog stops on the first record that passes the target, so trim back
			// to the exact size the case asks for. The cut can tear the last record,
			// which replay skips and counts — the subject here is the byte count.
			if size > tc.size {
				if err := os.Truncate(filepath.Join(dir, scoreEvents), tc.size); err != nil {
					t.Fatalf("truncate the log to %d: %v", tc.size, err)
				}
				size = tc.size
			}
			if size != tc.size {
				t.Fatalf("the fixture log is %d bytes, want exactly %d", size, tc.size)
			}
			before := readFile(t, dir, scoreEvents)

			s := openStore(t, dir)
			if h := s.Health(); h.Compacted != 0 {
				t.Errorf("health = %+v, want no compaction at %d bytes against a threshold of %d",
					h, size, compactAtBytes)
			}
			if after := readFile(t, dir, scoreEvents); after != before {
				t.Errorf("a log of %d bytes was rewritten; the threshold is %d", size, compactAtBytes)
			}
		})
	}

	t.Run("past the threshold the log is rewritten", func(t *testing.T) {
		dir, id, size := seedForCompaction(t, compactAtBytes+(1<<16))
		if size <= compactAtBytes {
			t.Fatalf("the fixture log is %d bytes, want it over the %d threshold", size, compactAtBytes)
		}

		s := openStore(t, dir)
		if h := s.Health(); h.Compacted == 0 {
			t.Fatalf("health = %+v, want the compaction counted", h)
		}
		fi, err := os.Stat(filepath.Join(dir, scoreEvents))
		if err != nil {
			t.Fatalf("stat the log: %v", err)
		}
		if fi.Size() >= size {
			t.Errorf("the log is %d bytes after compaction, want less than %d", fi.Size(), size)
		}
		// One record, for the one entry, and it is the state record — not the
		// submission plus every repeat, which is the thing that was too long.
		evs := events(t, dir)
		if len(evs) != 1 || evs[0].Event != EventCompacted || evs[0].Id != id {
			t.Fatalf("the compacted log is %+v, want one %s record for %s", evs, EventCompacted, id)
		}
	})
}

// TestCompactionKeepsEveryIdItHasEverNamed is PLAN.md's decision 2, and the one
// property compaction may never trade away.
//
// burned is built from the log and from nothing else, and newIDLocked draws
// against it. A compaction that wrote only the LIVE entries — which is what
// "one current event per entry" describes, and what the obvious implementation
// does — frees every retired id, and the next id drawn can land on one and
// inherit a dead entry's whole history. Dropping the snapshot leaves nowhere
// else for burned to have been written down, so the log is the only thing that
// can carry it.
func TestCompactionKeepsEveryIdItHasEverNamed(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submit(t, s, "the fleet keeps the build green")
	gone := submit(t, s, "the fleet is about to forget this one")
	extra := submit(t, s, "and this one too")

	// Retire two of the three the way an operator does: delete their lines.
	writeMD(t, dir, "- ["+keep.Id+"] the fleet keeps the build green\n")
	if d := reconcile(t, s); d.Retired != 2 {
		t.Fatalf("pass = %+v, want the two deleted lines retired", d)
	}
	s.mu.Lock()
	before := len(s.burned)
	written, err := s.compactLocked(0) // every log is over a threshold of zero
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if written != 3 {
		t.Fatalf("compaction wrote %d records, want one per id it has ever named (3)", written)
	}
	s.Close()

	// The ids come back from the compacted log alone, which is the only place
	// they can come from now.
	re := openStore(t, dir)
	re.mu.Lock()
	after := re.burned
	re.mu.Unlock()
	if len(after) != before {
		t.Fatalf("burned holds %d ids after compaction, want the %d it named before", len(after), before)
	}
	for _, id := range []string{keep.Id, gone.Id, extra.Id} {
		if _, ok := after[id]; !ok {
			t.Errorf("id %s was un-burned by compaction; it is free to be reissued", id)
		}
	}
	// And the dead ids are burned WITHOUT coming back as entries, which is the
	// other half of the same record doing its job.
	if re.Len() != 1 {
		t.Fatalf("entries after compaction = %d, want only the live one", re.Len())
	}
}

// TestCompactionKeepsALiveEntryWhole is the other side of decision 2: what a
// compacted record has to carry for the entry to survive its own history being
// deleted. Every field an entry earns over its life is in here, because each of
// them is rebuilt by COUNTING records compaction removes.
func TestCompactionKeepsALiveEntryWhole(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	prov := Provenance{Source: SourceAgent, SourcePanel: "p1", SourceProfile: "claude", SourceCwd: "/work", SourceGroup: "api"}
	e := submitAs(t, s, "the agent asks before it deletes", prov)
	for range defaultPromoteAt + defaultUserSignalsAt {
		if err := s.Reinforce(e.Id, SourceUser); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	// A reword, so the entry carries an alias the compaction has to bring along.
	writeMD(t, dir, "- ["+e.Id+"] the agent asks before it removes anything\n")
	reconcile(t, s)
	want := entryByID(t, s, e.Id)
	if want.Tier != maxEarnedTier || want.Reinforcements == 0 || len(want.Aliases) != 1 {
		t.Fatalf("the fixture entry is %+v; it must have earned a tier, counts and an alias", want)
	}

	compactNow(t, s)
	s.Close()

	re := openStore(t, dir)
	got := entryByID(t, re, e.Id)
	if got.Text != want.Text || got.Tier != want.Tier || got.Reinforcements != want.Reinforcements ||
		got.UserSignals != want.UserSignals || got.Provenance != want.Provenance ||
		strings.Join(got.Aliases, "|") != strings.Join(want.Aliases, "|") {
		t.Fatalf("the entry came back from the compacted log as %+v, want %+v", got, want)
	}
	// The alias is not decoration: the wording it replaced still folds.
	if _, folded, err := re.Submit("the agent asks before it deletes", Provenance{Source: SourceAgent}); err != nil || !folded {
		t.Fatalf("the superseded wording folded=%v err=%v, want it to still fold (I4)", folded, err)
	}
}

// TestCompactionKeepsTheRecencyOrder is what compaction does and does not
// preserve about the ranking, asserted rather than described.
//
// Recency is a position in the log, so a rewrite necessarily moves every one of
// them. What must survive is the ORDER — the entry that last moved most recently
// still outranks the one that moved before it — and the compaction writes the
// live entries in that order to make it so. The SPACING does not survive and no
// test claims it does; see compactLocked.
func TestCompactionKeepsTheRecencyOrder(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	var made []string
	for i := range 5 {
		made = append(made, submit(t, s, fmt.Sprintf("the fleet remembers thing number %d", i)).Id)
	}
	// Move them back into a deliberate order: 3, 0, 4, 1, 2 last-reinforced.
	for _, i := range []int{3, 0, 4, 1, 2} {
		if err := s.Reinforce(made[i], SourceAgent); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	// Rank on recency alone: same tier, same (absent) context on every entry.
	want := s.Render(Context{})

	compactNow(t, s)
	s.Close()

	got := openStore(t, dir).Render(Context{})
	if len(got) != len(want) {
		t.Fatalf("render holds %d entries after compaction, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Id != want[i].Id {
			t.Fatalf("compaction reordered the working set:\n got %v\nwant %v",
				ids(got), ids(want))
		}
	}
}

// TestCompactionDeclinesWhatItMustDecline pins the four refusals, each against
// the case that would otherwise slip through it.
func TestCompactionDeclinesWhatItMustDecline(t *testing.T) {
	// A debt is DERIVED at boot from the fold records a compaction drops, so
	// compacting over one lets the next boot count a duplicate line that was
	// already folded — a tier per boot for as long as the file stays broken,
	// which is the ladder the owed bookkeeping was written to stop.
	t.Run("an owed removal", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		e := submit(t, s, "the fleet keeps the build green")
		// A duplicate line the pass folds, with the DIRECTORY unwritable so the
		// rewrite that would remove it cannot land: the fold is durable, the line
		// is still in the file, and the removal is owed.
		writeMD(t, dir, "- ["+e.Id+"] the fleet keeps the build green\n- the fleet keeps the build green\n")
		restore := unwritable(t, dir)
		reconcileMustFail(t, s)

		s.mu.Lock()
		owed := len(s.owed)
		written, err := s.compactLocked(0)
		s.mu.Unlock()
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
		if owed == 0 {
			t.Fatal("the fixture left no debt, so this asserts nothing")
		}
		if written != 0 {
			t.Errorf("compaction wrote %d records while the store owed %d removals", written, owed)
		}

		// The other direction: settle the debt and the same store compacts.
		restore()
		writeMD(t, dir, "- ["+e.Id+"] the fleet keeps the build green\n")
		reconcile(t, s)
		if written = compactNow(t, s); written == 0 {
			t.Error("a store owing nothing declined to compact")
		}
	})

	// A file over the threshold that parsed into nothing is not a log this store
	// wrote. Replacing it with an empty one would destroy the only copy.
	t.Run("a log that named no ids", func(t *testing.T) {
		dir := t.TempDir()
		garbage := strings.Repeat("this is not a log line at all\n", 100)
		if err := os.WriteFile(filepath.Join(dir, scoreEvents), []byte(garbage), 0o600); err != nil {
			t.Fatalf("write the garbage: %v", err)
		}
		s := openStore(t, dir)
		if written := compactNow(t, s); written != 0 {
			t.Errorf("compaction wrote %d records over a log it could not read", written)
		}
		if got := readFile(t, dir, scoreEvents); got != garbage {
			t.Error("compaction destroyed a file it had not understood")
		}

		// The other direction: one real entry in the same directory, and the same
		// call compacts — so what declined above was the absent ids and not the
		// garbage being unparsable, which the torn-line counter already tolerates.
		submit(t, s, "the fleet keeps the build green")
		if written := compactNow(t, s); written == 0 {
			t.Error("a log that names an id declined to compact")
		}
	})

	// A store with more burned ids than the threshold covers is already as
	// compact as this can make it. Without the check it rewrites the same file
	// at every boot for the rest of its life.
	t.Run("a rewrite that would not shrink the file", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		submit(t, s, "the fleet keeps the build green")
		if first := compactNow(t, s); first == 0 {
			t.Fatal("the first compaction declined, so the second asserts nothing")
		}
		before := readFile(t, dir, scoreEvents)

		if written := compactNow(t, s); written != 0 {
			t.Errorf("compaction rewrote %d records over a log it had just written", written)
		}
		if got := readFile(t, dir, scoreEvents); got != before {
			t.Error("a compaction that could not shrink the log rewrote it anyway")
		}
	})

	// A store with no log at all is the first run, and there is nothing to stat.
	t.Run("a log that is not there", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		s.mu.Lock()
		written, err := s.compactLocked(0)
		s.mu.Unlock()
		if err != nil || written != 0 {
			t.Errorf("compact on a store with no log = (%d, %v), want (0, nil)", written, err)
		}
	})
}

// TestCompactionSurvivesACrashMidRewrite is the risk compaction carries that
// nothing else in this package does: it rewrites the one file that IS the truth.
//
// The guarantee is writeFileAtomic's — a sibling temp file, fsync, rename,
// parent-directory fsync — so at every instant score-events.jsonl is either the
// whole old log or the whole new one and rename(2) decides which. This drives
// the half that is reachable: the rewrite fails before the rename, which is
// every failure a full or read-only disk can produce, and the old log has to be
// there afterwards, byte for byte, and still open into the same store.
func TestCompactionSurvivesACrashMidRewrite(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submitAs(t, s, "the agent asks before it deletes", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	gone := submit(t, s, "and this one is retired")
	writeMD(t, dir, "- ["+keep.Id+"] the agent asks before it deletes\n")
	reconcile(t, s)
	before := readFile(t, dir, scoreEvents)

	// The temp file's own path made unwritable: the rewrite cannot even begin,
	// let alone rename, which is where a full disk leaves it too.
	tmp := filepath.Join(dir, scoreEvents+tempSuffix)
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatalf("mkdir over the temp path: %v", err)
	}
	s.mu.Lock()
	written, err := s.compactLocked(0)
	s.mu.Unlock()
	if err == nil {
		t.Fatalf("compaction reported success (%d records) with its temp path blocked", written)
	}
	if got := readFile(t, dir, scoreEvents); got != before {
		t.Fatalf("a failed compaction changed the log:\n got: %s\nwant: %s", got, before)
	}
	s.Close()
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("remove the blocking directory: %v", err)
	}

	// The store the failed compaction left behind opens, and opens as itself.
	re := openStore(t, dir)
	if re.Len() != 1 || entryByID(t, re, keep.Id).Text != "the agent asks before it deletes" {
		t.Fatalf("the store after a failed compaction holds %+v", re.Render(Context{}))
	}
	re.mu.Lock()
	_, burned := re.burned[gone.Id]
	re.mu.Unlock()
	if !burned {
		t.Error("the retired id was lost by a compaction that never landed")
	}
}

// TestOpenCountsACompactionItCouldNotMake keeps the failure visible. A boot that
// could not shrink the log is a boot that will be slower next time and slower
// again after that, and it is the only symptom of itself.
func TestOpenCountsACompactionItCouldNotMake(t *testing.T) {
	dir, _, size := seedForCompaction(t, compactAtBytes+(1<<16))
	if err := os.Mkdir(filepath.Join(dir, scoreEvents+tempSuffix), 0o700); err != nil {
		t.Fatalf("mkdir over the temp path: %v", err)
	}

	s, err := Open(dir, Policy{})
	if err != nil {
		t.Fatalf("Open must not fail over a compaction it could not make: %v", err)
	}
	t.Cleanup(s.Close)
	if h := s.Health(); h.CompactionFailures != 1 || h.Compacted != 0 {
		t.Errorf("health = %+v, want one counted failure and no records written", h)
	}
	if s.Len() != 1 {
		t.Errorf("entries = %d, want the store fully open regardless", s.Len())
	}
	fi, err := os.Stat(filepath.Join(dir, scoreEvents))
	if err != nil {
		t.Fatalf("stat the log: %v", err)
	}
	if fi.Size() != size {
		t.Errorf("the log is %d bytes, want the %d it had before the failed rewrite", fi.Size(), size)
	}
}

// TestCompactionIsDeterministic is invariant I1 over the rewrite: the retired
// ids are read out of a MAP, so two daemons compacting the same store would
// write two different files if the order were left to the runtime — and #38's
// verification check 4 is that the same log replays identically everywhere.
func TestCompactionIsDeterministic(t *testing.T) {
	build := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		s := openStore(t, dir)
		var live string
		for i := range 12 {
			e := submit(t, s, fmt.Sprintf("the fleet remembers thing number %d", i))
			if i == 0 {
				live = "- [" + e.Id + "] the fleet remembers thing number 0\n"
			}
		}
		writeMD(t, dir, live)
		reconcile(t, s)
		s.Close() // one writer per directory; the caller opens it again
		return dir
	}
	// The same store, compacted twice from two independent opens of the same
	// files: the ids differ between runs, so this compares one directory against
	// its own second compaction rather than two directories against each other.
	dir := build(t)
	s := openStore(t, dir)
	compactNow(t, s)
	first := readFile(t, dir, scoreEvents)
	s.Close()

	// Reopen and compact again. The second call declines — the rewrite would not
	// shrink the file — so what this compares is one compaction's own output
	// against itself across a full replay, and the only thing that could differ
	// is the ORDER the ids were written in.
	re := openStore(t, dir)
	if written := compactNow(t, re); written != 0 {
		t.Fatalf("the second compaction rewrote %d records; it should have declined", written)
	}
	if got := readFile(t, dir, scoreEvents); got != first {
		t.Errorf("compaction is not stable across a reopen:\n got: %s\nwant: %s", got, first)
	}
	// The record order itself: every retired id ascending, then the live entries.
	evs := events(t, dir)
	var lastDead string
	seenLive := false
	for _, ev := range evs {
		switch ev.Event {
		case EventRetired:
			if seenLive {
				t.Fatalf("a retired record follows a live one; the order is not what replay depends on:\n%s", first)
			}
			if ev.Id <= lastDead {
				t.Fatalf("retired ids are not ascending (%q after %q); map order has reached the file", ev.Id, lastDead)
			}
			lastDead = ev.Id
		case EventCompacted:
			seenLive = true
		}
	}
}

// TestCompactAtBytesIsBoundedBothWays pins the CONSTANT, which the threshold
// test above deliberately cannot.
//
// That test builds its fixture FROM compactAtBytes, so it follows the number
// wherever the number goes: it proves the relationship — rewritten above,
// untouched below — and passes unchanged on any value at all. Both drifts from
// there are real and neither shows up as a failing assertion anywhere else, so
// each gets one here, against a measurement rather than against another
// constant.
//
// TOO SMALL costs history, and it costs it silently: compaction destroys the
// records it replaces, so a threshold an ordinary store crosses starts throwing
// away logs that were never the problem. The floor is measured off a REAL record
// — one submission, on disk, at the size this store actually writes — and
// multiplied out to the store a busy year produces.
//
// TOO LARGE costs the boot, which is what the number is for: the listener is
// already bound while the replay runs. What a megabyte of log costs that replay
// is derived once, on compactAtBytes itself, which is what the next person
// tuning the number reads; the ceiling below is that cost multiplied out.
func TestCompactAtBytesIsBoundedBothWays(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "the agent asks before it force-pushes anything to a shared branch",
		Provenance{Source: SourceAgent, SourcePanel: "p1", SourceProfile: "claude", SourceCwd: "/work/repo", SourceGroup: "api"})
	if err := s.Reinforce(e.Id, SourceAgent); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, scoreEvents))
	if err != nil {
		t.Fatalf("stat the log: %v", err)
	}
	perRecord := fi.Size() / 2

	// An ordinary busy store: two thousand entries, each said five times. That is
	// a year of a working fleet, and compaction must not be reaching for it.
	const ordinaryEntries, ordinarySaid = 2000, 5
	ordinary := perRecord * ordinaryEntries * ordinarySaid
	if ordinary >= compactAtBytes {
		t.Errorf("a %d-entry store said %d times each is %d bytes of log at %d bytes a record, and "+
			"compactAtBytes is %d: an ordinary store would have its history destroyed at every boot",
			ordinaryEntries, ordinarySaid, ordinary, perRecord, compactAtBytes)
	}
	if compactAtBytes > 16<<20 {
		t.Errorf("compactAtBytes is %d bytes; at the ~9 ms and 3 MB of transient heap a megabyte of "+
			"log costs the replay, past 16 MiB the boot is over 145 ms and 48 MB with the listener "+
			"already bound and Serve not started", compactAtBytes)
	}
}

// TestCompactionLeavesTheStoreAgreeingWithTheFileItWrote is the assertion behind
// compactLocked's last four lines, and it exists because the claim survived a
// mutation that removed them.
//
// Recency is a POSITION in the log, and compaction replaces the log. If the
// running store keeps counting from where the old file left off while the new
// file starts at one, the two never disagree about the ORDER — so every test
// about ranking still passes — and they disagree about every SPACING. The daemon
// then ranks one way until it is restarted and another way afterwards, off one
// unchanged file, which is invariant I1's whole subject read from the wrong side.
//
// So the positions are re-derived through noteEventLocked over the records that
// were just written, and this compares them against what the store's own next
// boot actually reads out of that file.
func TestCompactionLeavesTheStoreAgreeingWithTheFileItWrote(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	var live string
	for i := range 6 {
		e := submit(t, s, fmt.Sprintf("the fleet remembers thing number %d", i))
		if i < 4 {
			live += "- [" + e.Id + "] the fleet remembers thing number " + fmt.Sprint(i) + "\n"
		}
		if err := s.Reinforce(e.Id, SourceAgent); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	// Two retired, and score.md left agreeing with the store, so the reopen below
	// reconciles nothing and every position it holds came out of the log.
	writeMD(t, dir, live)
	reconcile(t, s)

	s.mu.Lock()
	_, err := s.compactLocked(0)
	seq, lastAt := s.seq, maps.Clone(s.lastAt)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	s.Close()

	re := openStore(t, dir)
	re.mu.Lock()
	reSeq, reLastAt := re.seq, re.lastAt
	re.mu.Unlock()

	if seq != reSeq {
		t.Errorf("the compacted store counts %d records and its own reboot counts %d", seq, reSeq)
	}
	// Every position is distinct, which is what makes the order compactLocked
	// writes the live entries in a total one rather than something a sort's
	// internals get to decide. One record names one id and seq advances once per
	// record, so this holds by construction — and it is asserted because
	// compactLocked's ordering leans on it.
	seen := map[int]string{}
	for id, at := range reLastAt {
		if other, dup := seen[at]; dup {
			t.Errorf("entries %s and %s both sit at position %d; the compaction order is not total",
				id, other, at)
		}
		seen[at] = id
	}
	if len(lastAt) != len(reLastAt) {
		t.Fatalf("the compacted store holds %d positions and its reboot holds %d", len(lastAt), len(reLastAt))
	}
	for id, at := range lastAt {
		if reLastAt[id] != at {
			t.Errorf("entry %s sits at position %d in the compacted store and %d in its own reboot: "+
				"the daemon ranks one way now and another after a restart, off one unchanged file",
				id, at, reLastAt[id])
		}
	}
}

// TestARecordThisBuildDoesNotKnowStillBurnsItsId is what EventCompacted's
// backward-compatibility claim actually rests on, asserted rather than argued.
//
// What it asserts is the HALF THAT HOLDS. The full claim cannot be run here,
// because this build knows the name; and the half that does not hold was
// measured on a real store instead — an older build rebuilds every entry from
// score.md as the OPERATOR's, which is why EventCompacted's comment now calls a
// compacted log a one-way door rather than a safe skip.
//
// The half asserted here is the mechanism the safe part depends on: an unknown
// record's id is burned before the switch is reached, so no id is ever reissued
// and no dead entry's history is grafted onto a newcomer, whatever else an older
// build gets wrong about the entry.
//
// It matters more for a compaction record than for anything R6 added. A build
// that skips a `merged` loses one alias; a build that skips a `compacted` has
// skipped the entry's whole state, and what stops that being data loss rather
// than under-claiming is precisely these two properties.
func TestARecordThisBuildDoesNotKnowStillBurnsItsId(t *testing.T) {
	dir := t.TempDir()
	// A log of one record whose name means nothing here — which is what a
	// compacted log looks like to a build from before R7 — and the score.md line
	// it belongs to.
	unknown := `{"schema":1,"event":"transmogrified","id":"abc123","at":"2026-09-01T00:00:00Z",` +
		`"text":"the fleet asks before it deletes","tier":3,"reinforcements":9,"user_signals":4}`
	if err := os.WriteFile(filepath.Join(dir, scoreEvents), []byte(unknown+"\n"), 0o600); err != nil {
		t.Fatalf("write the log: %v", err)
	}
	writeMD(t, dir, "- [abc123] the fleet asks before it deletes\n")

	s := openStore(t, dir)
	// The id is BURNED, so nothing can be issued it and pick up whatever the
	// record was about.
	s.mu.Lock()
	_, burned := s.burned["abc123"]
	s.mu.Unlock()
	if !burned {
		t.Fatal("an unknown record's id was not burned; it is free to be reissued")
	}
	// The entry is back from score.md, under its own id — and UNDER-claiming: the
	// tier and counts the record carried were not read, because nothing here
	// knows they were there to read.
	e := entryByID(t, s, "abc123")
	if e.Text != "the fleet asks before it deletes" {
		t.Fatalf("entry = %+v, want score.md's line admitted under the same id", e)
	}
	if e.Tier != 1 || e.Reinforcements != 0 || e.UserSignals != 0 {
		t.Errorf("entry = %+v, want tier 1 and no counts: the record was skipped, not decoded", e)
	}
	// A torn line and an unknown NAME are different things: the line parsed, so
	// nothing here is damage.
	if h := s.Health(); h.TornEvents != 0 {
		t.Errorf("health = %+v, want a record this build skipped not counted as a torn one", h)
	}
}

// TestNoTwoLiveEntriesShareALogPosition asserts the premise compactLocked's
// sort rests on, on the side the sort actually reads.
//
// The sort keys on s.lastAt as it stands BEFORE the rewrite, and the comment
// cited TestCompactionLeavesTheStoreAgreeingWithTheFileItWrote for it — which
// reads the positions AFTER, where uniqueness is true by construction because
// compaction has just written one record per id. The claim held (224 runs on
// stores with more than three live entries, no ties) and the citation asserted
// it everywhere except where it is load-bearing.
//
// A position is the index of the record that moved the entry, one record names
// one id, and seq advances once per record. So the store is put through every
// mutation that moves an entry and the positions are read straight out of it.
func TestNoTwoLiveEntriesShareALogPosition(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)

	a := submit(t, s, "the agent asks before it force-pushes")
	b := submit(t, s, "the fleet keeps the build green")
	c := submit(t, s, "a brief names the panel it is for")
	d := submit(t, s, "nobody deletes a shared branch")
	gone := submit(t, s, "this one goes away")

	// Every door that moves a live entry, so the positions under the sort are the
	// ones a working store really produces rather than five submissions in a row.
	if err := s.Reinforce(a.Id, SourceAgent); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}
	if err := s.Reword(b.Id, "the fleet keeps the build green, always"); err != nil {
		t.Fatalf("Reword: %v", err)
	}
	for range 3 { // up a rung, so there is one to come back down from
		if err := s.Reinforce(c.Id, SourceAgent); err != nil {
			t.Fatalf("Reinforce to promote: %v", err)
		}
	}
	if err := s.Lower(c.Id); err != nil {
		t.Fatalf("Lower: %v", err)
	}
	if err := s.Merge(d.Id, gone.Id); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if err := s.Reinforce(a.Id, SourceUser); err != nil {
		t.Fatalf("Reinforce again: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) < 4 {
		t.Fatalf("the fixture holds %d live entries; the claim is about a store with several", len(s.entries))
	}
	held := make(map[int]string, len(s.entries))
	for _, e := range s.entries {
		at, ok := s.lastAt[e.Id]
		if !ok {
			t.Errorf("live entry %s holds no log position at all, so the sort reads a zero for it", e.Id)
			continue
		}
		if other, dup := held[at]; dup {
			t.Errorf("entries %s and %s both sit at position %d; the sort is not total and the order "+
				"compaction carries across the rewrite is whatever the runtime felt like", other, e.Id, at)
		}
		held[at] = e.Id
	}
}

// TestTwoCompactionsOfAnUnchangedStoreAgreeToTheByte is the assertion behind the
// truncated stamp, which had none: removing `.Truncate(time.Second)` passed the
// whole repository five times out of five.
//
// The last refusal compares the rewrite's SIZE against the file's, and declines
// when it would not be smaller. time.Time marshals RFC 3339 with its trailing
// zeros trimmed, so an untruncated stamp makes a record as wide as the
// nanosecond it was written at happens to render — usually nine fractional
// digits, sometimes fewer. Two compactions of one unchanged store then differ in
// length in whichever direction the clock fell, and about one time in ten the
// second is SHORTER, passes the refusal, and rewrites a file nothing had touched.
//
// Both halves are asserted, because the outcome alone is a coin toss: the stamp
// carries no sub-second part at all, which is deterministic and is what makes
// the widths equal, and a second rewrite of an unchanged store then declines and
// leaves the bytes alone.
func TestTwoCompactionsOfAnUnchangedStoreAgreeToTheByte(t *testing.T) {
	dir, _, _ := seedForCompaction(t, compactAtBytes+(1<<16))
	s := openStore(t, dir)
	if h := s.Health(); h.Compacted == 0 {
		t.Fatalf("health = %+v, want the fixture compacted at boot", h)
	}
	first := readFile(t, dir, scoreEvents)

	// The stamp every record this wrote carries has no sub-second part, so its
	// width does not depend on the moment. This is the property; the byte
	// comparison below is what it buys.
	for i, ev := range events(t, dir) {
		if !ev.At.Equal(ev.At.Truncate(time.Second)) {
			t.Fatalf("record %d is stamped %s; a sub-second stamp makes a record as wide as the "+
				"nanosecond it was written at, and the size refusal below then compares two widths",
				i, ev.At.Format(time.RFC3339Nano))
		}
	}

	// A second rewrite of a store nothing has touched. maxBytes 0 puts the size
	// refusal on its own, which is the one under test — every other refusal is
	// already past. It must decline, and the file must be what it was.
	if written := compactNow(t, s); written != 0 {
		t.Errorf("compaction rewrote %d records over a store nothing had changed; a store that "+
			"rewrites its whole log at every boot for the rest of its life is what the refusal exists "+
			"to stop", written)
	}
	if got := readFile(t, dir, scoreEvents); got != first {
		t.Errorf("the log changed under a compaction that had nothing to do:\nbefore %d bytes\nafter  %d bytes",
			len(first), len(got))
	}
}

// TestOpenRemovesTheStoresOwnStaleTempFiles is the store cleaning up after
// itself. writeFileAtomic unwinds every failure it can see, so what this finds
// is what a SIGNAL left: one kill -9 in forty trials landed a
// score-events.jsonl.tmp in the operator's directory, where nothing read it,
// nothing wrote it again, and nothing would ever have removed it.
//
// Its absence is the point rather than any behaviour that changes: the store had
// declined to delete an operator's stale score.json on the principle that it
// does not remove files from their directory, and then left its own debris under
// the same roof. These two names are the store's own, which is a different
// thing — and the operator's file is asserted to survive right here, so the
// sweep cannot widen into one that takes it.
func TestOpenRemovesTheStoresOwnStaleTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "the fleet asks before it deletes")
	s.Close()

	// What a process killed between writeFileAtomic's create and its rename
	// leaves behind. The log's, because that is the one a kill -9 actually
	// produced.
	debris := filepath.Join(dir, scoreEvents+tempSuffix)
	if err := os.WriteFile(debris, []byte("half a record, no newline"), 0o600); err != nil {
		t.Fatalf("plant the stale temp file: %v", err)
	}
	// A DIRECTORY on the OTHER temp path, which is not debris this package left:
	// writeFileAtomic only ever creates a regular file there, and os.Remove takes
	// an empty directory as happily as a file. A sweep written without looking
	// would clear whatever is in its way rather than only what it dropped — and
	// TestOpenCountsACompactionItCouldNotMake blocks a rewrite in exactly this
	// way, so a boot that quietly unblocked it would leave that test asserting
	// nothing.
	blocked := filepath.Join(dir, scoreMD+tempSuffix)
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatalf("plant a directory on a temp path: %v", err)
	}
	// And a file that is not the store's at all.
	theirs := filepath.Join(dir, "score.json")
	if err := os.WriteFile(theirs, []byte(`{"entries":[]}`), 0o600); err != nil {
		t.Fatalf("plant the operator's file: %v", err)
	}

	s = openStore(t, dir)
	if _, err := os.Stat(debris); !os.IsNotExist(err) {
		t.Errorf("%s survived the boot; the store left its own debris in the operator's directory",
			filepath.Base(debris))
	}
	if _, err := os.Stat(blocked); err != nil {
		t.Errorf("the sweep removed a directory: %v — it may undo its own debris and nothing else", err)
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Errorf("score.json was removed: %v — the store does not delete the operator's files, "+
			"which is the principle the sweep must not have widened", err)
	}
	// And a sweep that took a live file with it would be the worse bug by far.
	if got := entryByID(t, s, e.Id); got.Text != e.Text {
		t.Fatalf("entry = %+v, want the store the boot replayed untouched by the sweep", got)
	}
}

// TestABootThatCannotRewriteTheLogCarriesTheReasonOut is the words behind the
// counter. A full-disk boot reached the operator as `compaction_failures=1` and
// the sentence "no space left on device" reached nobody — and the words are the
// half that says what to do about it.
//
// Both directions: the boot that fails carries the reason, and the boot that
// succeeds carries none. An error string standing on a healthy store sends an
// operator looking for a disk that is fine.
func TestABootThatCannotRewriteTheLogCarriesTheReasonOut(t *testing.T) {
	t.Run("a rewrite that failed says why", func(t *testing.T) {
		dir, _, _ := seedForCompaction(t, compactAtBytes+(1<<16))
		// The DIRECTORY unwritable, so writeFileAtomic cannot create the sibling it
		// renames from — where a full or read-only disk leaves the rewrite. The log
		// itself stays readable, so the replay before it still runs.
		unwritable(t, dir)
		// The probe stays: unwritable takes the bits off and says nothing about
		// whether they bind, and reconcileMustFail — where the skip usually lives —
		// runs a pass this case does not want. Asked of the filesystem before the
		// boot runs, so a root user skips the test and a broken store cannot.
		probe := filepath.Join(dir, "probe")
		if f, perr := os.Create(probe); perr == nil {
			_ = f.Close()
			_ = os.Remove(probe)
			t.Skip("this user writes into a directory with no write bit; the test needs an unprivileged one")
		}

		s := openStore(t, dir)
		h := s.Health()
		if h.CompactionFailures != 1 {
			t.Fatalf("health = %+v, want the rewrite counted as failed", h)
		}
		if h.CompactionError == "" {
			t.Fatal("the rewrite failed and the store kept the reason; the operator is left with a 1")
		}
		if !strings.Contains(h.CompactionError, scoreEvents) {
			t.Errorf("CompactionError = %q, want the failure's own words about the file it could not write",
				h.CompactionError)
		}
	})

	t.Run("a rewrite that landed says nothing", func(t *testing.T) {
		dir, _, _ := seedForCompaction(t, compactAtBytes+(1<<16))
		s := openStore(t, dir)
		h := s.Health()
		if h.Compacted == 0 {
			t.Fatalf("health = %+v, want the fixture compacted", h)
		}
		if h.CompactionFailures != 0 || h.CompactionError != "" {
			t.Errorf("health = %+v, want no failure reported on a rewrite that landed", h)
		}
	})
}

// TestCompactionReportsWhatTheLogWeighedOnEitherSide pins Health.LogBefore and
// Health.LogAfter, which are an operator's only view of the growth compaction
// exists to bound — see their doc for why Health.Compacted beside them is not
// that number.
//
// Both directions, because a pair of sizes reported on a boot that rewrote
// nothing would be describing a rewrite that did not happen.
func TestCompactionReportsWhatTheLogWeighedOnEitherSide(t *testing.T) {
	t.Run("a boot that rewrote the log", func(t *testing.T) {
		dir, _, size := seedForCompaction(t, compactAtBytes+(1<<16))
		s := openStore(t, dir)
		h := s.Health()
		if h.LogBefore != size {
			t.Errorf("LogBefore = %d, want the %d bytes the log actually weighed", h.LogBefore, size)
		}
		if h.LogAfter <= 0 || h.LogAfter >= h.LogBefore {
			t.Errorf("health = %+v, want the rewritten log smaller than what it replaced", h)
		}
		// And it is the file, not a number the store invented about it.
		fi, err := os.Stat(filepath.Join(dir, scoreEvents))
		if err != nil {
			t.Fatalf("stat the compacted log: %v", err)
		}
		if fi.Size() != h.LogAfter {
			t.Errorf("LogAfter = %d and the log on disk is %d bytes", h.LogAfter, fi.Size())
		}
	})

	t.Run("a boot that left the log alone", func(t *testing.T) {
		dir, _, _ := seedForCompaction(t, compactAtBytes-(1<<16))
		s := openStore(t, dir)
		if h := s.Health(); h.LogBefore != 0 || h.LogAfter != 0 {
			t.Errorf("health = %+v, want no sizes reported for a rewrite that never ran", h)
		}
	})
}

// TestCompactionReportsItselfWithoutABoot is E3's claim: compaction is one
// callable unit, and its five Health fields are written by the function that
// does the work rather than half by it and half by Open.
//
// Open used to set Compacted, CompactionFailures and CompactionError while
// compactLocked set LogBefore and LogAfter, so a caller that was not a boot —
// the runtime trigger this is already filed for — would have had to know to
// replicate Open's half, and nothing said so. Both directions, because a
// success that left a stale error behind and a failure that left a stale record
// count behind are the same bug read from two sides.
func TestCompactionReportsItselfWithoutABoot(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submit(t, s, "the fleet keeps the build green")
	gone := submit(t, s, "and this one is retired")
	writeMD(t, dir, "- ["+keep.Id+"] the fleet keeps the build green\n")
	reconcile(t, s)
	s.mu.Lock()
	_, burned := s.burned[gone.Id]
	s.mu.Unlock()
	if !burned {
		t.Fatal("the fixture retired nothing, so the record count below asserts nothing")
	}

	// A rewrite that lands, called directly. Nothing here is Open.
	before := fileSize(t, dir, scoreEvents)
	written := compactNow(t, s)
	h := s.Health()
	if h.Compacted != written {
		t.Errorf("Compacted = %d after a direct compaction that wrote %d records", h.Compacted, written)
	}
	if h.LogBefore != before || h.LogAfter != fileSize(t, dir, scoreEvents) {
		t.Errorf("health = %+v, want the log %d bytes before and %d after", h, before, fileSize(t, dir, scoreEvents))
	}
	if h.CompactionFailures != 0 || h.CompactionError != "" {
		t.Errorf("health = %+v, want no failure reported by a rewrite that landed", h)
	}

	// And a rewrite that cannot even begin, on the same store: the temp path
	// blocked, which is where a full or read-only disk leaves it.
	submit(t, s, "something new, so the next rewrite would shrink the file")
	tmp := filepath.Join(dir, scoreEvents+tempSuffix)
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatalf("mkdir over the temp path: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tmp) })
	s.mu.Lock()
	written, err := s.compactLocked(0)
	s.mu.Unlock()
	if err == nil {
		t.Fatalf("compaction reported success (%d records) with its temp path blocked", written)
	}
	failed := s.Health()
	if failed.CompactionFailures != 1 {
		t.Errorf("health = %+v, want the direct rewrite's failure counted without a boot to count it", failed)
	}
	if !strings.Contains(failed.CompactionError, scoreEvents) {
		t.Errorf("CompactionError = %q, want the failure's own words about the file it could not write",
			failed.CompactionError)
	}
	// And the two rules are different on purpose. The failure fields describe the
	// last ATTEMPT; the record count and the sizes describe the last rewrite that
	// LANDED, and the file on disk is still the one that landed — zeroing them
	// over a later failure would un-say something that is still true of it.
	if failed.Compacted != h.Compacted || failed.LogBefore != h.LogBefore || failed.LogAfter != h.LogAfter {
		t.Errorf("health = %+v after a failed rewrite, want the landed rewrite's numbers left standing (%+v)",
			failed, h)
	}
}

// fileSize is one of the store's files on disk, in bytes.
func fileSize(t *testing.T, dir, name string) int64 {
	t.Helper()
	fi, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("stat %s: %v", name, err)
	}
	return fi.Size()
}
