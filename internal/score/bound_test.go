package score

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// This file is the RUNTIME bound's (#56): the trigger that fires without a
// restart, and the three phases that let the marshal run off the store mutex.
//
// compact_test.go is compaction's own file and still holds everything the
// rewrite must keep, destroy and decline — none of which this changes. What is
// here is only what a compaction of a RUNNING store can get wrong: a fold that
// lands while the marshal runs counted twice, a debt that arrives too late to
// refuse, a store that stops agreeing with the file it just wrote, and a
// threshold that has drifted at the one door compact_test.go cannot reach.

// growLog appends fold records for id through the store's OWN append path until
// the log has gained more than want bytes, and returns what it gained. One batch
// and one fsync: 8 MiB of records is about 37,000 of them, and a fsync each would
// make this the slowest test in the package by two orders of magnitude.
//
// The records reach the LOG and not the entry, exactly as padLog's do — the store
// is never told the repeats happened, so its memory and the file it later
// compacts to agree with each other and the padding is simply history the rewrite
// drops. What this buys over padLog is the half padLog cannot give: the growth is
// counted by appendEvents, so the runtime trigger sees it.
func growLog(t *testing.T, s *Store, id, text string, want int64) int64 {
	t.Helper()
	ev := paddingRecord(id, text)
	rec, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal the padding record: %v", err)
	}
	width := int64(len(rec)) + 1 // the newline the encoder frames it with
	n := want/width + 1
	evs := make([]event, n)
	for i := range evs {
		evs[i] = ev
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendEvents(evs); err != nil {
		t.Fatalf("append %d padding records: %v", n, err)
	}
	return n * width
}

// waitForCompaction blocks until the store has landed MORE rewrites than done,
// and fails the test on one that failed or on none at all. The compactor is a
// goroutine, so this is the only way to read its outcome — and the deadline is
// generous because what is under test is that the rewrite happens, never how
// fast.
//
// It watches Health.Compactions, which is the monotone counter added for exactly
// this. Watching Compacted instead — a description of the LAST rewrite — meant a
// caller waiting for a second one had to zero three health fields between rounds
// so it would stop reading the first.
func waitForCompaction(t *testing.T, s *Store, done int) Health {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		h := s.Health()
		switch {
		case h.CompactionFailures > 0:
			t.Fatalf("the runtime rewrite failed: health = %+v", h)
		case h.Compactions > done:
			return h
		case time.Now().After(deadline):
			t.Fatalf("no compaction after 30s; health = %+v", h)
		}
		time.Sleep(time.Millisecond)
	}
}

// snapshotNow takes phase 1 through the store's OWN wrapper and fails the test
// unless it produced a rewrite to run. maxBytes is zero, as compactNow's is.
//
// Through the wrapper rather than through a hand-rolled Lock/Locked/Unlock, so a
// test cannot disagree with the daemon about which phase holds what.
func snapshotNow(t *testing.T, s *Store) *compaction {
	t.Helper()
	c, err := s.snapshotCompaction(0)
	if err != nil || c == nil {
		t.Fatalf("snapshot = (%v, %v), want a compaction to run", c, err)
	}
	return c
}

// beginCompaction drives a compaction through its first two phases and hands
// back the rewrite waiting to be committed, so a test can show only the
// mutations it means to land inside the marshal.
func beginCompaction(t *testing.T, s *Store) *compaction {
	t.Helper()
	c := snapshotNow(t, s)
	ok, err := c.build(s.eventsPath)
	if err != nil || !ok {
		t.Fatalf("build = (%v, %v), want the rewrite built", ok, err)
	}
	return c
}

// storeState is everything about a store that its own next boot has to
// reproduce: what each live entry holds, and where the log puts it.
//
// Keyed by id rather than compared as a slice, because s.entries is in score.md's
// order and a boot rebuilds that order from the file. The ORDER of that slice
// reaches no ranking — rankBefore keys on the position and the id — so comparing
// it would fail on a difference that means nothing.
type storeState struct {
	entries map[string]Entry
	seq     int
	lastAt  map[string]int
}

func stateOf(s *Store) storeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := storeState{entries: make(map[string]Entry, len(s.entries)), seq: s.seq, lastAt: maps.Clone(s.lastAt)}
	for _, e := range s.entries {
		st.entries[e.Id] = e
	}
	return st
}

// mustAgree fails unless a store and its own reboot hold the same thing. It is
// the single assertion behind every phase-3 claim: the tail is noted for its
// POSITIONS and never re-applied, so a count that is right in memory has to be
// right again after a restart, and one that has been applied twice shows up here
// and nowhere else.
func mustAgree(t *testing.T, live, boot storeState) {
	t.Helper()
	if len(live.entries) != len(boot.entries) {
		t.Fatalf("the running store holds %d entries and its own reboot holds %d",
			len(live.entries), len(boot.entries))
	}
	for id, want := range live.entries {
		got, ok := boot.entries[id]
		if !ok {
			t.Errorf("entry %s is in the running store and not in its own reboot", id)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("entry %s reads %+v in the running store and %+v after a restart off the log it "+
				"just wrote", id, want, got)
		}
	}
	if live.seq != boot.seq {
		t.Errorf("the running store counts %d records and its own reboot counts %d", live.seq, boot.seq)
	}
	// Reported id by id and capped, because a store with several hundred entries
	// dumps two maps nobody can read and the first disagreement is the whole
	// answer.
	shown := 0
	for id, at := range live.lastAt {
		if boot.lastAt[id] == at {
			continue
		}
		if shown++; shown > 5 {
			t.Errorf("...and %d more positions disagree", len(live.lastAt)-5)
			break
		}
		t.Errorf("entry %s sits at position %d in the running store and %d in its own reboot: the "+
			"daemon ranks one way now and another after a restart, off one unchanged file",
			id, at, boot.lastAt[id])
	}
	if len(live.lastAt) != len(boot.lastAt) {
		t.Errorf("the running store holds %d positions and its reboot holds %d",
			len(live.lastAt), len(boot.lastAt))
	}
}

// TestTheRuntimeTriggerFiresPastAThresholdOfGrowth pins compactAtBytes at the
// door #56 adds, in both directions and against the real constant.
//
// The silent half is again the one worth having, and it is a different silence
// from the boot's. A threshold that has drifted DOWN here does not merely destroy
// an ordinary store's history — it destroys it repeatedly, on a running daemon,
// re-spacing every panel's recency each time; and every test that only checks the
// rewrite happens still passes.
//
// The two halves are asserted at different levels on purpose. Below the threshold
// there is nothing to wait for, so the assertion is that the compactor was never
// woken — deterministic, where a "nothing happened within N seconds" would not
// be. Above it, the whole chain runs: appendEvents counts the growth, the trigger
// wakes the goroutine, and the goroutine rewrites the file with no restart
// anywhere.
func TestTheRuntimeTriggerFiresPastAThresholdOfGrowth(t *testing.T) {
	t.Run("growth up to the threshold does not wake the compactor", func(t *testing.T) {
		// A store with no compactor running, which is what makes this readable at
		// all: the goroutine Open starts drains the wake buffer the moment anything
		// lands in it, so on a live store "was it woken" can only be asked by
		// waiting for an outcome. newStore builds the same store without it.
		s := newStore(t.TempDir(), Policy{})

		// Exactly the threshold, which is the boundary a `<` written for a `<=`
		// makes invisible: grown is documented as a size the log must EXCEED.
		s.mu.Lock()
		s.noteGrowthLocked(compactAtBytes)
		grown := s.grown
		s.mu.Unlock()
		if grown != compactAtBytes {
			t.Fatalf("grown = %d, want the %d bytes it was told about", grown, compactAtBytes)
		}
		if len(s.wake) != 0 {
			t.Errorf("the compactor was woken by growth of exactly %d against a threshold of %d",
				compactAtBytes, compactAtBytes)
		}

		// One byte past it wakes the compactor, and takes the count back to nothing
		// in the same step. The reset is what backs the trigger off after a rewrite
		// that declines: a store whose log cannot be made smaller must not be asked
		// to marshal its whole entry set again on the very next append, and on every
		// append after that, for the life of the daemon.
		s.mu.Lock()
		s.noteGrowthLocked(1)
		armed, grown := len(s.wake), s.grown
		s.mu.Unlock()
		if armed != 1 {
			t.Fatalf("the wake buffer holds %d tokens one byte past the threshold, want exactly 1", armed)
		}
		if grown != 0 {
			t.Errorf("a crossing left %d bytes standing on the trigger, want it counting again from "+
				"nothing towards another whole %d", grown, compactAtBytes)
		}

		// A burst far past the threshold still wakes it once: the buffer is one deep,
		// so a hundred crossings do not queue a hundred marshals.
		s.mu.Lock()
		s.noteGrowthLocked(100 * compactAtBytes)
		s.mu.Unlock()
		if len(s.wake) != 1 {
			t.Errorf("the wake buffer holds %d tokens after a second crossing, want exactly 1", len(s.wake))
		}

		// And once the compactor has taken the token, another whole threshold is
		// what re-arms it — not the next append.
		<-s.wake
		s.mu.Lock()
		s.noteGrowthLocked(1)
		s.mu.Unlock()
		if len(s.wake) != 0 {
			t.Error("the append after a crossing re-armed the trigger; the count did not restart")
		}
	})

	t.Run("a store without its claim is never woken at all", func(t *testing.T) {
		// The rewrite is built from THIS store's memory and renamed over the whole
		// file, so on a directory a second daemon may also be writing it destroys
		// everything that daemon recorded below the watermark. At boot that happens
		// once; a runtime trigger would repeat it every threshold, forever.
		//
		// A filesystem that cannot lock is what makes an unlocked store and no test
		// can conjure one, so the state is built rather than provoked — which is why
		// the rule lives on the trigger and not on the goroutine.
		s := newStore(t.TempDir(), Policy{})
		s.unlocked = true
		s.mu.Lock()
		s.noteGrowthLocked(100 * compactAtBytes)
		s.mu.Unlock()
		if len(s.wake) != 0 {
			t.Error("a store running without its single-writer claim armed the runtime rewrite; " +
				"a rename there discards whatever the other daemon appended below the watermark")
		}

		// The other direction, on the same growth: with the claim, it arms.
		s.unlocked = false
		s.mu.Lock()
		s.noteGrowthLocked(100 * compactAtBytes)
		s.mu.Unlock()
		if len(s.wake) != 1 {
			t.Error("a store holding its claim declined to arm on the same growth, so the check " +
				"above is not about the claim")
		}
	})

	t.Run("growth past the threshold rewrites the log with no restart", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		keep := submit(t, s, "the fleet asks before it force-pushes")
		gone := submit(t, s, "and this one is retired")
		writeMD(t, dir, "- ["+keep.Id+"] the fleet asks before it force-pushes\n")
		reconcile(t, s)
		if h := s.Health(); h.Compacted != 0 {
			t.Fatalf("health = %+v, want nothing compacted before the growth", h)
		}

		grew := growLog(t, s, keep.Id, keep.Text, compactAtBytes)
		if grew <= compactAtBytes {
			t.Fatalf("the fixture grew the log by %d bytes, want more than the %d threshold",
				grew, compactAtBytes)
		}
		before := fileSize(t, dir, scoreEvents)
		h := waitForCompaction(t, s, 0)
		if h.LogBefore != before || h.LogAfter >= h.LogBefore {
			t.Errorf("health = %+v, want the %d bytes it replaced and something smaller after", h, before)
		}
		if got := fileSize(t, dir, scoreEvents); got != h.LogAfter {
			t.Errorf("LogAfter = %d and the log on disk is %d bytes", h.LogAfter, got)
		}

		// The two properties the rewrite may never trade away, on a running store as
		// much as on a booting one: the live entry whole, and every id it has ever
		// named still burned.
		if got := entryByID(t, s, keep.Id); got.Text != keep.Text {
			t.Errorf("entry = %+v, want the live entry carried across the runtime rewrite", got)
		}
		s.mu.Lock()
		_, burned := s.burned[gone.Id]
		s.mu.Unlock()
		if !burned {
			t.Error("a retired id was freed by a runtime rewrite; the next newcomer can inherit its history")
		}
	})
}

// TestACompactionDoesNotDoubleCountWhatLandedDuringIt is the failure the whole
// three-phase shape exists to walk past, and the one that survives every test
// that does not restart the store.
//
// A fold that lands while the marshal runs is not in the snapshot — the snapshot
// was taken at the watermark — and IS in the tail phase 3 copies onto the
// rewrite, so a replay counts it exactly once. But s.entries already has it
// applied, because it happened live. A phase 3 that re-applied the tail's effects
// would count it a second time in memory, the entry would read correctly, and the
// error would appear only at the next boot — as a doubled reinforcement count on
// an entry nobody touched.
//
// The phases are driven by hand rather than by a goroutine, because what is under
// test is an interleaving and not a schedule: every mutation between build and
// commit lands strictly inside the marshal, every time this runs.
func TestACompactionDoesNotDoubleCountWhatLandedDuringIt(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	held := submit(t, s, "the fleet keeps the build green")
	moved := submit(t, s, "the agent asks before it deletes")
	leaves := submit(t, s, "this one goes while the marshal runs")
	if err := s.Reinforce(held.Id, SourceUser); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}

	// Phase 1: the snapshot and the watermark.
	c := beginCompaction(t, s)

	// The fleet keeps working, and every door it can work through: a repeat
	// counted into an entry the snapshot holds, a brand-new entry the snapshot has
	// never heard of, an operator's reword, and a retirement of one the snapshot
	// holds as live.
	if err := s.Reinforce(held.Id, SourceUser); err != nil {
		t.Fatalf("Reinforce during the marshal: %v", err)
	}
	if err := s.Reinforce(held.Id, SourceAgent); err != nil {
		t.Fatalf("Reinforce during the marshal: %v", err)
	}
	fresh := submit(t, s, "and something nobody had said when the snapshot was taken")
	if err := s.Reword(moved.Id, "the agent asks before it deletes anything at all"); err != nil {
		t.Fatalf("Reword during the marshal: %v", err)
	}
	writeMD(t, dir, "- ["+held.Id+"] the fleet keeps the build green\n"+
		"- ["+moved.Id+"] the agent asks before it deletes anything at all\n"+
		"- ["+fresh.Id+"] and something nobody had said when the snapshot was taken\n")
	if d := reconcile(t, s); d.Retired != 1 {
		t.Fatalf("pass = %+v, want the one deleted line retired during the marshal", d)
	}

	// Phase 3: the tail, the rename, and the positions.
	written, err := s.commitCompaction(c)
	if err != nil || written == 0 {
		t.Fatalf("commit = (%d, %v), want the rewrite committed", written, err)
	}

	// What the store believes, and what its own next boot reads out of the file it
	// just wrote. A tail applied twice shows up here as a count off by exactly the
	// number of repeats that landed during the marshal.
	live := stateOf(s)
	if got := live.entries[held.Id]; got.Reinforcements != 3 || got.UserSignals != 2 {
		t.Fatalf("entry = %+v, want the three repeats it was actually given", got)
	}
	if _, still := live.entries[leaves.Id]; still {
		t.Fatalf("entry %s survived a retirement that landed during the marshal", leaves.Id)
	}
	s.Close()

	re := openStore(t, dir)
	if d := re.Boot(); d != (Delta{}) {
		t.Fatalf("the reboot's recovery pass changed %+v; the comparison below would be about that, "+
			"not about the compaction", d)
	}
	mustAgree(t, live, stateOf(re))

	// And the retired entry's id survived the rewrite that dropped its records,
	// which is the property a tail must not be allowed to quietly undo either.
	re.mu.Lock()
	_, burned := re.burned[leaves.Id]
	re.mu.Unlock()
	if !burned {
		t.Error("an id retired during the marshal was freed by the rewrite")
	}
}

// TestARewriteIsDiscardedByADebtThatArrivedDuringIt pins phase 3's one re-check.
//
// The owed refusal is made at the snapshot, and a fold that removes a line can
// make it true while the marshal runs. Compacting over one would drop the very
// fold records the next boot derives the debt from, so that boot would count a
// duplicate line the store has already folded — the tier-per-boot ladder the owed
// bookkeeping exists to stop.
//
// Both directions, because a refusal that never lets anything through passes
// every test that only checks it fires.
func TestARewriteIsDiscardedByADebtThatArrivedDuringIt(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "the fleet keeps the build green")

	c := beginCompaction(t, s)
	before := readFile(t, dir, scoreEvents)

	// The debt put on the store directly, and deliberately. How one ARRIVES is
	// compact_test.go's subject — a fold whose line removal could not land — and
	// building one that way needs the directory unwritable, which is also where a
	// discarded temp file cannot be removed, so the fixture would hide half of
	// what this asserts. What is under test here is only that phase 3 asks the
	// question again, and this is the state it has to ask it about.
	//
	// It is the ONE site left driving a phase through its Locked form rather than
	// through the store's own wrapper, and that is the point of it: the debt and
	// the commit have to happen in a SINGLE hold, so nothing can land between the
	// state being built and the phase being asked about it.
	s.mu.Lock()
	s.owed = map[string][]string{e.Id: {"the fleet keeps the build green"}}
	written, err := s.commitCompactionLocked(c)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if written != 0 {
		t.Errorf("the rewrite committed %d records over a debt that arrived while it was being built", written)
	}
	if got := readFile(t, dir, scoreEvents); got != before {
		t.Error("a discarded rewrite changed the log")
	}
	if _, serr := os.Stat(filepath.Join(dir, scoreEvents+tempSuffix)); !os.IsNotExist(serr) {
		t.Errorf("the discarded rewrite left its temp file behind: %v", serr)
	}
	if h := s.Health(); h.Compacted != 0 || h.CompactionFailures != 0 {
		t.Errorf("health = %+v, want a refusal counted as neither a rewrite nor a failure", h)
	}

	// The other direction: settle the debt and the same store compacts.
	s.mu.Lock()
	clear(s.owed)
	s.mu.Unlock()
	if compactNow(t, s) == 0 {
		t.Error("a store owing nothing declined to compact")
	}
}

// TestACompactionUnderConcurrentSubmitsAgreesWithTheFileItWrote is the same claim
// as the hand-driven interleaving above, made against a real schedule and with
// the race detector watching.
//
// It is the test that has to fail if the marshal ever reads a field the fleet is
// still writing. Phase 2 holds no lock, so everything it touches must be the copy
// phase 1 took — including each entry's ALIASES, which are a slice whose backing
// array a reword appends to in place where the capacity allows.
func TestACompactionUnderConcurrentSubmitsAgreesWithTheFileItWrote(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	// Enough entries that the marshal takes long enough for the submissions below
	// to land inside it rather than after it.
	const seeded = 400
	var ids []string
	for i := range seeded {
		e := submit(t, s, fmt.Sprintf("the fleet remembers standing thing number %d", i))
		ids = append(ids, e.Id)
	}
	// Prior wordings on some of them, so the aliases the marshal reads are slices
	// with room to be appended to.
	for i := range 20 {
		if err := s.Reword(ids[i], fmt.Sprintf("the fleet still remembers standing thing number %d", i)); err != nil {
			t.Fatalf("Reword: %v", err)
		}
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if _, _, err := s.Submit(fmt.Sprintf("worker %d noticed thing %d", w, i),
					Provenance{Source: SourceAgent, SourcePanel: fmt.Sprint(w)}); err != nil {
					return
				}
				// A reword too, because it is the one mutation that writes an
				// entry the marshal may be reading, and its ALIASES are the field
				// the snapshot's struct copy alone would leave shared.
				if err := s.Reword(ids[i%seeded], fmt.Sprintf("thing %d, as worker %d has it now", i%seeded, w)); err != nil {
					return
				}
			}
		}()
	}
	written, err := s.compact(0)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatalf("compact under load: %v", err)
	}
	if written < seeded {
		t.Fatalf("the rewrite holds %d records for a store of at least %d entries", written, seeded)
	}
	if s.Len() <= seeded {
		t.Fatalf("entries = %d, want the concurrent submissions to have landed too", s.Len())
	}

	live := stateOf(s)
	s.Close()
	re := openStore(t, dir)
	if d := re.Boot(); d != (Delta{}) {
		t.Fatalf("the reboot's recovery pass changed %+v; the comparison below would be about that", d)
	}
	mustAgree(t, live, stateOf(re))
}

// TestCloseStopsTheCompactor is the other half of Open starting it. A goroutine
// per store that outlived its store would go on rewriting a directory a second
// daemon may by then hold the claim on.
//
// The assertion is exact rather than timed: Close waits for the goroutine, so a
// token put in the wake buffer afterwards can only still be there if nothing is
// draining it.
func TestCloseStopsTheCompactor(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Policy{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	submit(t, s, "the fleet keeps the build green")
	s.Close()

	s.wake <- struct{}{}
	if len(s.wake) != 1 {
		t.Error("something is still draining the wake channel after Close; the compactor outlived its store")
	}
	// And the daemon closes one store twice on some paths — Open's own error
	// unwind is one — so this may not panic on the second.
	s.Close()
}

// compactCallers is every function allowed to run a compaction, and why. Two:
// one goroutine per store, and Open's own rewrite finishing before that
// goroutine is started.
//
// It is NOT what makes two compactions mutually exclusive — Store.compacting is,
// and that is where the difference between a lint and an exclusion is argued.
// What this still says is worth saying: a status handler that compacts on
// demand, a SIGHUP path, a second goroutine, are each a rewrite running
// somewhere nobody expected one, and the flag makes them safe rather than
// intended.
var compactCallers = map[string]string{
	"Open":        "the boot's own rewrite, run before the compactor goroutine is started",
	"compactLoop": "the one goroutine, woken when the log has grown by a whole threshold",
}

// TestCompactIsReachedFromTwoPlacesOnly reads every non-test file in the package,
// so it holds for a caller added in a file that does not exist yet.
//
// It sees the CALL and nothing else. What matters about the two it allows is
// stated on compactLoop, and this is what keeps the list at two.
func TestCompactIsReachedFromTwoPlacesOnly(t *testing.T) {
	callers := map[string]bool{}
	for _, name := range packageFiles(t) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "compact" {
					callers[fd.Name.Name] = true
				}
				return true
			})
		}
	}

	if len(callers) == 0 {
		t.Fatal("found no callers of compact at all; the walker is broken, not the code")
	}
	for fn := range callers {
		if _, known := compactCallers[fn]; !known {
			t.Errorf("%s runs a compaction, and compactCallers does not name it: two rewrites racing "+
				"for one temp file leave the log half of each, and one goroutine is the only thing "+
				"stopping them", fn)
		}
	}
	for fn := range compactCallers {
		if !callers[fn] {
			t.Errorf("compactCallers still claims %s runs a compaction; it does not any more", fn)
		}
	}
}

// TestTwoCompactionsCannotOverlap is the mutual exclusion, in both directions.
//
// compact RELEASES the store mutex twice — around the marshal, and again before
// the commit — so between the snapshot and the rename the store is unlocked and
// a second caller can reach createAtomic on the same temp name. The two rewrites
// then race for one file and the log comes back half of each.
//
// What used to stop that was the shape of the code and nothing else: one
// goroutine, kept alone by TestCompactIsReachedFromTwoPlacesOnly. Store.compacting
// says why that shape is a lint rather than the exclusion.
//
// Both directions, because a guard that never lets anything through passes every
// test that only checks it fires — and here the second direction is the ordinary
// case, which is a daemon going on bounding its log for the rest of its life.
//
// Three subtests and not two, because the endpoints are not the property. The
// first sets the flag by hand and the third asks for a rewrite after one
// finished, so between them they say the flag refuses while set and is given
// back when done — and both of them pass a compact that releases it the moment
// the snapshot returns, which is the one release that reopens the window. The
// middle subtest is the HOLD.
func TestTwoCompactionsCannotOverlap(t *testing.T) {
	t.Run("a rewrite in flight refuses a second", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		e := seedCompactable(t, s)
		before := readFile(t, dir, scoreEvents)

		// The state is BUILT rather than provoked, as the unlocked subtest above
		// builds its own. What has to be asked is what a caller arriving mid-rewrite
		// does, and staging a real marshal to still be running at the instant of a
		// second call is a schedule rather than a state. That the flag is really
		// taken and really given back by a real rewrite is the subtest below.
		s.mu.Lock()
		s.compacting = true
		s.mu.Unlock()

		if written := compactNow(t, s); written != 0 {
			t.Errorf("a second compaction wrote %d records while a rewrite was in flight; two of them "+
				"race for one temp file and the log comes back half of each", written)
		}
		if got := readFile(t, dir, scoreEvents); got != before {
			t.Error("a compaction refused for overlapping still changed the log")
		}
		if _, err := os.Stat(filepath.Join(dir, scoreEvents+tempSuffix)); !os.IsNotExist(err) {
			t.Errorf("the refused compaction reached the temp file the one in flight owns: %v", err)
		}
		if h := s.Health(); h.Compacted != 0 || h.CompactionFailures != 0 {
			t.Errorf("health = %+v, want a refusal counted as neither a rewrite nor a failure", h)
		}

		// The same store with nothing in flight, because the assertions above hold
		// just as well against a compact that never runs at all.
		s.mu.Lock()
		s.compacting = false
		s.mu.Unlock()
		if compactNow(t, s) == 0 {
			t.Error("a store with no rewrite in flight declined to compact, so the refusal above was " +
				"not about the flag")
		}
		if got := entryByID(t, s, e.Id); got.Text != e.Text {
			t.Errorf("entry = %+v, want the store still holding what it remembers", got)
		}
	})

	t.Run("the flag is held across the marshal, not only around it", func(t *testing.T) {
		// What the two subtests either side of this one pin are the ENDPOINTS: the
		// flag refuses while it is set, and a finished rewrite gives it back. Both
		// go on passing against a compact that clears it the instant the snapshot
		// returns — which releases it BEFORE the marshal, and the marshal is the
		// entire window the flag exists to cover. Measured: that mutation left the
		// whole suite green.
		//
		// So this one provokes the state instead of building it. Every caller is
		// released at once, so the ones that lose the claim ask their question
		// while the winner is inside build — and under a flag released early they
		// would all pass the snapshot together and reach createAtomic on the ONE
		// temp name they share.
		//
		// Exactly one rewrite lands, in both directions: a flag never given back
		// makes it zero, and a flag given back too early makes it more than one or
		// leaves the log half of each.
		const callers = 8
		for round := range 4 {
			dir := t.TempDir()
			s := openStore(t, dir)
			e := seedCompactable(t, s)

			var wg sync.WaitGroup
			start := make(chan struct{})
			errs := make(chan error, callers)
			for range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if _, err := s.compact(0); err != nil {
						errs <- err
					}
				}()
			}
			close(start)
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatalf("round %d: a compaction failed while %d of them ran together: %v",
					round, callers, err)
			}
			if got := s.Health().Compactions; got != 1 {
				t.Fatalf("round %d: %d rewrites landed out of %d callers released together, want 1. "+
					"More than one means the losers were let past the snapshot and built into the "+
					"temp file the winner owns; none means the claim was never given back",
					round, got, callers)
			}
			if got := entryByID(t, s, e.Id); got.Text != e.Text {
				t.Fatalf("round %d: entry = %+v, want the store still holding what it remembers",
					round, got)
			}
			// The log on disk and not only the store's memory of it, because two
			// rewrites racing for one name leave a file that is half of each and a
			// store that would go on answering from RAM regardless. The claim on
			// the directory is released first; openStore's cleanup closing it a
			// second time is the double Close the daemon's own unwind already does.
			s.Close()
			replayed := openStore(t, dir)
			if got := entryByID(t, replayed, e.Id); got.Text != e.Text {
				t.Fatalf("round %d: the log replays to %+v, want the entry the store kept", round, got)
			}
		}
	})

	t.Run("an ordinary second compaction is not refused", func(t *testing.T) {
		// The failure this half is for is a flag taken and never given back: a
		// daemon whose first rewrite is its last, which is #56 undone and which no
		// test of the refusal alone would notice.
		dir := t.TempDir()
		s := openStore(t, dir)
		e := seedCompactable(t, s)
		if compactNow(t, s) == 0 {
			t.Fatal("the first compaction wrote nothing, so this asserts nothing about the second")
		}
		// Records the next rewrite drops, so it is genuinely smaller than the log it
		// replaces and the size refusal is not what answers below.
		for range 20 {
			if err := s.Reinforce(e.Id, SourceAgent); err != nil {
				t.Fatalf("Reinforce: %v", err)
			}
		}
		if compactNow(t, s) == 0 {
			t.Error("a store that had already compacted declined a second rewrite: the flag the first " +
				"one took was never given back, and this daemon's log is bounded by nothing")
		}
		s.mu.Lock()
		held := s.compacting
		s.mu.Unlock()
		if held {
			t.Error("a finished compaction left the store marked as compacting")
		}
		// The counter that tells two rewrites apart, on the one fixture that makes
		// two. Compacted describes the LAST one and reads 1 after either, which is
		// why anything watching for "a compaction happened" watches this instead;
		// see Health.Compactions.
		if got := s.Health().Compactions; got != 2 {
			t.Errorf("Compactions = %d after two rewrites that both landed, want 2", got)
		}
	})
}

// seedCompactable leaves a store holding one live entry, one retired id, and
// enough repeats that a rewrite is strictly smaller than the log it replaces —
// which is what the size refusal asks, and what both subtests above need to be
// true before they are about anything else.
func seedCompactable(t *testing.T, s *Store) Entry {
	t.Helper()
	keep := submit(t, s, "the fleet asks before it force-pushes")
	gone := submit(t, s, "and this one is retired")
	for range 20 {
		if err := s.Reinforce(keep.Id, SourceAgent); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
		if err := s.Reinforce(gone.Id, SourceAgent); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	writeMD(t, s.dir, "- ["+keep.Id+"] "+keep.Text+"\n")
	reconcile(t, s)
	return keep
}

// TestTheLogIsBoundedWhileTheDaemonRuns is #56 stated as the property it asks
// for, rather than as the mechanism that delivers it: a store nobody ever
// restarts stops growing.
//
// Two rounds, because one proves only that a compaction can happen. The second is
// what says the bound HOLDS — the trigger re-arms, and the log comes back down
// again from a state the first rewrite produced.
func TestTheLogIsBoundedWhileTheDaemonRuns(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "the fleet asks before it force-pushes")

	var peak int64
	for round := range 2 {
		grown := growLog(t, s, e.Id, e.Text, compactAtBytes)
		if size := fileSize(t, dir, scoreEvents); size > peak {
			peak = size
		}
		// Past THIS round's own rewrite: the counter is monotone, so the second
		// round cannot be satisfied by the first one's outcome.
		waitForCompaction(t, s, round)
		after := fileSize(t, dir, scoreEvents)
		if after >= grown {
			t.Fatalf("round %d: the log is %d bytes after a rewrite of %d bytes of growth", round, after, grown)
		}
	}

	// The bound itself, named as a number: what the log reached is at most what a
	// boot tolerates plus one threshold of growth — the ceiling
	// TestCompactAtBytesIsBoundedBothWays already pins for the boot.
	if peak > 2*compactAtBytes {
		t.Errorf("the log reached %d bytes while running; the bound is %d — what it weighed at boot, "+
			"itself bounded by compactAtBytes, plus one threshold of growth", peak, 2*compactAtBytes)
	}
	if got := entryByID(t, s, e.Id); got.Text != e.Text {
		t.Errorf("entry = %+v, want the store still holding what it remembers", got)
	}
}

// TestTheSnapshotIsACopyAndNotAWindow is R7's objection kept true by
// construction: the marshal runs with no lock held, so everything it reads has to
// be something the fleet can no longer write.
//
// The cheapest way to lose that is to hand phase 2 a reference into s.entries
// rather than a copy of it — which costs nothing, reads correctly, and is a data
// race on every entry the fleet touches while the marshal runs. The store is put
// through every door that rewrites an entry in place, between the snapshot and
// the marshal, and the snapshot is asserted to still hold what it was taken with.
func TestTheSnapshotIsACopyAndNotAWindow(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "the agent asks before it deletes")
	if err := s.Reword(e.Id, "the agent asks first"); err != nil {
		t.Fatalf("Reword: %v", err)
	}
	if err := s.Reinforce(e.Id, SourceUser); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}

	c := snapshotNow(t, s)
	want := c.live[0]
	if len(want.Aliases) == 0 {
		t.Fatal("the fixture gave the entry no aliases, so half of this asserts nothing")
	}

	// Every field a live entry has, moved, strictly inside the marshal.
	for i := range 3 {
		if err := s.Reword(e.Id, fmt.Sprintf("the agent asks first, wording %d", i)); err != nil {
			t.Fatalf("Reword during the marshal: %v", err)
		}
		if err := s.Reinforce(e.Id, SourceUser); err != nil {
			t.Fatalf("Reinforce during the marshal: %v", err)
		}
	}
	s.mu.Lock()
	moved := s.entries[0]
	s.mu.Unlock()
	if reflect.DeepEqual(moved, want) {
		t.Fatal("the fixture moved nothing, so the comparison below asserts nothing")
	}
	if got := c.live[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("the snapshot reads %+v after the entry was rewritten three times, want the %+v it "+
			"was taken with: the marshal is reading an entry the fleet is still writing", got, want)
	}
}
