package score

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestABurnedSetCostsTheSameWhateverFractionIsBurned is #88's whole claim, as a
// number rather than as a shape: the set is 2 MiB FLAT, so burning 700,000 ids
// into it must cost nothing measurable on top of the empty one.
//
// 700,000 is the population #83 measured — the one that cost 98 MiB in the map
// after a compaction had already shrunk it. The ceiling is half a mebibyte,
// which is 75 times under what the map needed for the same ids, so a set that
// grows per id at any rate at all fails here rather than merely looking bigger.
func TestABurnedSetCostsTheSameWhateverFractionIsBurned(t *testing.T) {
	const (
		ids     = 700_000
		ceiling = 512 << 10
	)
	b := newBurnedSet()
	runtime.GC()
	var empty runtime.MemStats
	runtime.ReadMemStats(&empty)

	for i := range uint32(ids) {
		if !b.add(idAt(i)) {
			t.Fatalf("id %s was already spent; idAt is not injective", idAt(i))
		}
	}
	if got := b.len(); got != ids {
		t.Fatalf("burned = %d, want %d", got, ids)
	}
	runtime.GC()
	var full runtime.MemStats
	runtime.ReadMemStats(&full)
	runtime.KeepAlive(b)

	grew := int64(full.HeapAlloc) - int64(empty.HeapAlloc)
	t.Logf("live heap: empty=%d bytes, %d ids=%d bytes, grew by %d",
		empty.HeapAlloc, ids, full.HeapAlloc, grew)
	if grew > ceiling {
		t.Errorf("the set grew %d bytes over %d ids, want under %d — it is not flat",
			grew, ids, ceiling)
	}
	// And the flat part is the size it is meant to be, in bytes, so a set that
	// stopped covering the whole space would be caught here rather than by a
	// reissued id somewhere far away.
	if got := len(b.bits) * 8; got != 2<<20 {
		t.Errorf("bitset = %d bytes, want %d — it no longer covers the id space", got, 2<<20)
	}
}

// TestADrawAgainstAFullIDSpaceRefusesRatherThanSpinning is the second half of
// #88: newIDLocked's loop had no bound and ran under the store mutex, so a store
// that reached the end of the id space would have hung the daemon inside s.mu
// instead of failing.
//
// The refusal is asked for with a deadline rather than merely called, because
// the failure this pins is a hang: an unbounded loop would take the test binary
// with it and report a package timeout twenty minutes later, naming nothing.
func TestADrawAgainstAFullIDSpaceRefusesRatherThanSpinning(t *testing.T) {
	s := newStore(t.TempDir(), Policy{})
	for i := range s.burned.bits {
		s.burned.bits[i] = ^uint64(0)
	}
	s.burned.n = idSpace

	type outcome struct {
		id  string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		id, err := s.newIDLocked()
		done <- outcome{id, err}
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("a full id space handed out %q; every id in it is already spent", got.id)
		}
		// The message is the whole remedy the reader gets, so it has to carry the
		// two numbers that say WHICH failure this is — a space that is full, or
		// one merely crowded enough that a thousand draws all missed.
		for _, want := range []string{"16777216 of 16777216", "1000 draws", "no free id"} {
			if !strings.Contains(got.err.Error(), want) {
				t.Errorf("refusal %q does not name %q", got.err, want)
			}
		}
	case <-time.After(30 * time.Second):
		t.Fatal("newIDLocked did not return on a full id space; it is spinning under the store lock")
	}
}

// TestADrawStillLandsWithOneIDLeftInTheSpace is the bound's other edge: it may
// refuse a store that has genuinely run out, and it may not refuse one that has
// not. A space with a single free id is as close to the bound as a store can get
// and still be owed an answer.
//
// A thousand draws find that one id with probability 1 - (1 - 1/16777216)^1000,
// which is about six in a hundred thousand — so this test asks the set directly
// rather than the draw. What it pins is that the ONE id is reachable at all:
// nothing about the bound may take an id out of circulation early.
func TestADrawStillLandsWithOneIDLeftInTheSpace(t *testing.T) {
	b := newBurnedSet()
	for i := range b.bits {
		b.bits[i] = ^uint64(0)
	}
	b.n = idSpace

	const free uint32 = 0x0f1e2d
	b.bits[free/64] &^= uint64(1) << (free % 64)
	b.n--

	if b.has(idAt(free)) {
		t.Fatalf("%s reads as spent, and it is the only id left", idAt(free))
	}
	if !b.add(idAt(free)) {
		t.Fatalf("%s could not be spent, so the space had no id left after all", idAt(free))
	}
	if b.len() != idSpace {
		t.Errorf("burned = %d, want the whole space (%d)", b.len(), idSpace)
	}
}

// TestAnIDTheOperatorTypedIsBurnedThoughNoBitCanHoldIt is the case the bitset
// alone cannot cover, and it is not hypothetical: score.md is the operator's
// file and parseLine takes the id from the line as written, so any string can
// become an id and reach the log. Every one of these must stay spent, or the
// next draw could reissue it onto a history that is not its own.
func TestAnIDTheOperatorTypedIsBurnedThoughNoBitCanHoldIt(t *testing.T) {
	for _, id := range []string{
		"AABBCC",   // upper case; ids compare byte-for-byte, so this is not aabbcc
		"my-note",  // an operator's own word
		"abc",      // too short
		"abcdef01", // too long, and the shape the event-log corpus uses
		"zzzzzz",   // the right length, and not hex
		"abcde",    // one digit short of the drawn shape
		"1",        // as short as an id can be
		"aabbcc-",  // a drawn id with something after it
		strings.Repeat("f", 64),
	} {
		t.Run(id, func(t *testing.T) {
			b := newBurnedSet()
			if b.has(id) {
				t.Fatalf("%q reads as spent in a fresh set", id)
			}
			if !b.add(id) {
				t.Fatalf("%q was refused as already spent in a fresh set", id)
			}
			if !b.has(id) {
				t.Errorf("%q did not stay spent; a later draw could reissue it", id)
			}
			if b.add(id) {
				t.Errorf("%q was spent twice, so the set does not remember it", id)
			}
			if b.len() != 1 {
				t.Errorf("burned = %d, want 1", b.len())
			}
			if got := b.list(); len(got) != 1 || got[0] != id {
				t.Errorf("list = %q, want [%q]", got, id)
			}
		})
	}
}

// TestUpperCaseAndLowerCaseIDsAreTwoIDs is the one collision a bitset invites
// and byte-comparison forbids. Everything else in the store compares ids as
// bytes, so folding case here would let "AABBCC" mark "aabbcc" spent — an entry
// silently unable to be created, on a store where an operator typed one id in
// capitals.
func TestUpperCaseAndLowerCaseIDsAreTwoIDs(t *testing.T) {
	b := newBurnedSet()
	if !b.add("AABBCC") {
		t.Fatal("AABBCC was refused in a fresh set")
	}
	if b.has("aabbcc") {
		t.Error("aabbcc reads as spent because AABBCC was; they are two ids")
	}
	if !b.add("aabbcc") {
		t.Error("aabbcc could not be spent after AABBCC was; the two share a bit")
	}
	if b.len() != 2 {
		t.Errorf("burned = %d, want 2", b.len())
	}
}

// TestEveryDrawnIDRoundTripsThroughItsBit pins idIndex against idAt at both ends
// of the space and at the word boundaries in between, because an off-by-one in
// either direction reissues an id rather than failing to compile.
func TestEveryDrawnIDRoundTripsThroughItsBit(t *testing.T) {
	for _, i := range []uint32{0, 1, 63, 64, 65, 0xffff, 0x0f1e2d, idSpace - 2, idSpace - 1} {
		id := idAt(i)
		if len(id) != 2*idBytes {
			t.Errorf("idAt(%d) = %q, want %d characters", i, id, 2*idBytes)
		}
		got, ok := idIndex(id)
		if !ok {
			t.Errorf("idIndex(%q) refused an id idAt produced", id)
			continue
		}
		if got != i {
			t.Errorf("idIndex(idAt(%d)) = %d", i, got)
		}
		b := newBurnedSet()
		if !b.add(id) || !b.has(id) || b.drawn() != 1 {
			t.Errorf("%q (bit %d) did not land in the bitset", id, i)
		}
	}
}

// TestTheBurnedSetListsBothKindsOfID pins what compaction is handed. It writes
// one bare `retired` record per id the store has ever named, so an id missing
// from this list is an id the next boot does not know is spent.
func TestTheBurnedSetListsBothKindsOfID(t *testing.T) {
	b := newBurnedSet()
	want := map[string]bool{"000000": true, "ffffff": true, "0f1e2d": true, "my-note": true, "AABBCC": true}
	for id := range want {
		b.add(id)
	}
	got := b.list()
	if len(got) != len(want) {
		t.Fatalf("list = %q, want %d ids", got, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("list produced %q, which was never burned", id)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Errorf("list dropped %v", want)
	}
}
