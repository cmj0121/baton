package server

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestThrottleStampsOnlyWhenItAdmits pins the gapStamp's stamping rule, which is
// argued at length on tooSoon and was pinned by nothing: making the REFUSING
// branch re-stamp — the self-lockout the doc singles out as the mistake to
// avoid — passed the whole suite, because every test above it either paces
// itself past the gap or rewinds the stamp.
//
// The clock is a parameter, so the rule is asserted directly rather than through
// a sleep: a caller polling faster than the gap must not push its own next
// admission away.
func TestThrottleStampsOnlyWhenItAdmits(t *testing.T) {
	const gap = 250 * time.Millisecond
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tt := gapStamp{gap: gap}

	if _, tooSoon := tt.tooSoon("c1", t0); tooSoon {
		t.Fatal("the first attempt was refused; an unstamped gapStamp must admit")
	}
	// Inside the gap: refused, and the refusal must leave the stamp where it was.
	// The reported duration is what says so — it is measured from the last
	// ADMITTED attempt, so it keeps growing while a refusal would reset it.
	for _, at := range []time.Duration{10 * time.Millisecond, 100 * time.Millisecond, 240 * time.Millisecond} {
		since, tooSoon := tt.tooSoon("c1", t0.Add(at))
		if !tooSoon {
			t.Fatalf("an attempt at +%v was admitted, want it refused inside the gap", at)
		}
		if since != at {
			t.Fatalf("an attempt at +%v reported %v since the last admission, want %v: a refusal re-stamped", at, since, at)
		}
	}
	// One gap after the ADMITTED attempt — not after the last refusal, which is
	// 20ms ago and would still be inside the gap if a refusal had stamped.
	if _, tooSoon := tt.tooSoon("c1", t0.Add(260*time.Millisecond)); tooSoon {
		t.Fatal("an attempt a full gap after the last ADMITTED one was refused: " +
			"the refusing branch is stamping, which locks out a caller for as long as it keeps asking")
	}

	// A different identity is admitted at once and takes the slot, which is what
	// makes this a cap on a caller rather than on the verb.
	if _, tooSoon := tt.tooSoon("c2", t0.Add(261*time.Millisecond)); tooSoon {
		t.Fatal("a different identity was refused by another's stamp")
	}
	if _, tooSoon := tt.tooSoon("c1", t0.Add(262*time.Millisecond)); tooSoon {
		t.Fatal("the first identity was refused after the slot moved to another: " +
			"the gapStamp holds one identity, so the previous stamp is gone")
	}
}

// TestThrottlesHoldsOneStampPerIdentity is the difference between rateBuckets
// and gapStamp, asserted rather than described: the single-stamp version admits
// everything the moment two identities interleave, which is the state a busy
// fleet is always in.
//
// The stamping rule is the same one gapStamp carries and is pinned for the same
// reason — a refusal that re-stamped would lock a polling caller out for as long
// as it kept asking.
func TestThrottlesHoldsOneStampPerIdentity(t *testing.T) {
	const gap = 250 * time.Millisecond
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tt := rateBuckets{gap: gap}

	if _, tooSoon := tt.tooSoon("p1", t0); tooSoon {
		t.Fatal("the first attempt was refused; an unstamped cap must admit")
	}
	// A second identity takes its own slot and does NOT displace the first —
	// which one stamp in total cannot do.
	if _, tooSoon := tt.tooSoon("p2", t0.Add(time.Millisecond)); tooSoon {
		t.Fatal("a second identity was refused by the first's stamp")
	}
	if wait, tooSoon := tt.tooSoon("p1", t0.Add(2*time.Millisecond)); !tooSoon {
		t.Fatal("the first identity was admitted again after another acted: with one stamp in total " +
			"two callers alternating are never refused, and the cap is dead exactly when the fleet is busy")
	} else if wait != gap-2*time.Millisecond {
		t.Fatalf("the refusal said to retry in %v, want %v: the number on the line is when the "+
			"allowance comes back, which is the caller's actual question", wait, gap-2*time.Millisecond)
	}
	// Refusals do not re-stamp: one gap after the ADMITTED attempt, not after the
	// last refusal.
	if _, tooSoon := tt.tooSoon("p1", t0.Add(gap)); tooSoon {
		t.Fatal("an attempt a full gap after the last ADMITTED one was refused: the refusing branch " +
			"is stamping, which locks out a caller for as long as it keeps asking")
	}

	// Stamps that can no longer refuse anything are dropped, so the map does not
	// grow with every identity the daemon has ever seen. A FEW AT A TIME, and
	// both halves of that are asserted here: the sweep runs under Server.mu on
	// the submit path, and the key is a string the client declares, so a walk of
	// the whole map on every admission is an unbounded scan on the hot path.
	for i := range 50 {
		tt.tooSoon(strconv.Itoa(i), t0.Add(2*gap))
	}
	before := len(tt.full)
	tt.tooSoon("p1", t0.Add(10*gap))
	if n := before - len(tt.full); n > sweepPerAdmit {
		t.Errorf("one admission dropped %d stamps out of %d, want at most %d: whatever else it is, "+
			"the sweep is a scan of every identity that has ever acted, holding the daemon's lock, "+
			"on the path a busy fleet takes", n, before, sweepPerAdmit)
	}
	// And it really drains — a bound nothing ever reaches is a leak with a
	// comment. Every identity that went quiet is gone within a bounded number of
	// admissions, which is the direction a sweep deleted altogether would fail.
	for i := range 200 {
		tt.tooSoon("p1", t0.Add(time.Duration(11+i)*gap))
	}
	if n := len(tt.full); n != 1 {
		t.Errorf("the cap still holds %d stamps after every earlier identity went quiet for longer "+
			"than the %v gap, want only the one still inside it", n, gap)
	}
}

// TestTheRateCapsAreWorthTheirNames pins the two gaps in the LOOSENING
// direction, which is the direction their own comments argue about and the one
// nothing asserted. minRefineGap could be retuned to ten seconds and nothing
// failed, while the conductor's briefing went on promising "a few a second"; the
// same was true of the spawn cap.
//
// Both are bounded rather than fixed, because the point is not the exact figure
// — it is that neither may drift out of the range its documentation, and the
// text handed to the conductor, describe.
func TestTheRateCapsAreWorthTheirNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		gap  time.Duration
	}{
		{"minRefineGap", minRefineGap},
		{"minConductorSpawnGap", minConductorSpawnGap},
	} {
		// Below the floor a loop does damage at machine speed, which is what both
		// caps exist to stop; above the ceiling the guardrail has become a budget
		// and an operator driving deliberately would notice the wait.
		switch {
		case tc.gap < 50*time.Millisecond:
			t.Errorf("%s is %v: fast enough that a loop still runs at machine speed", tc.name, tc.gap)
		case tc.gap > time.Second:
			t.Errorf("%s is %v: slow enough that a person acting deliberately would be made to wait, "+
				"which is a budget rather than a guardrail", tc.name, tc.gap)
		}
	}
}

// TestThePrimersRateClaimMatchesTheCap ties the conductor's briefing to the
// constant it describes. The briefing is the one text a model reads as
// instruction, so a sentence in it is a claim about the daemon: it says
// corrections are "rate-limited to a few a second", and retuning minRefineGap to
// ten seconds used to leave that sentence standing with nothing failing.
//
// The wire test asserts the string really reaches BATON.md. This asserts the
// string is TRUE, so the claim and the enforcement fail together on a retuning
// rather than only on a removal.
func TestThePrimersRateClaimMatchesTheCap(t *testing.T) {
	const claim = "rate-limited to a few a second"
	primer := string(conductorPrimer("c1"))
	if !strings.Contains(primer, claim) {
		t.Fatalf("the briefing no longer says %q; if the wording moved, move this assertion with it", claim)
	}
	// "A few a second" is two to twenty, read generously in both directions.
	if perSecond := time.Second / minRefineGap; perSecond < 2 || perSecond > 20 {
		t.Errorf("minRefineGap is %v, which is %d corrections a second, and the briefing tells the "+
			"conductor they are %q. Retune the constant or reword the briefing — they are one claim",
			minRefineGap, perSecond, claim)
	}
}

// rewind pushes a gapStamp's stamp into the past, so a test can cross its gap
// without sleeping through it. One helper for both caps rather than one each:
// they are one type, and a test that reached into the fields itself would keep
// working after the stamping rule changed under it.
func rewind(s *Server, t *gapStamp, by time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.at = t.at.Add(-by)
}

// rewindAll is rewind for the per-actor cap: every stamp it holds moves into the
// past together, so a test that submits as several panels crosses the gap for
// all of them in one call.
func rewindAll(s *Server, t *rateBuckets, by time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for who, at := range t.full {
		t.full[who] = at.Add(-by)
	}
}
