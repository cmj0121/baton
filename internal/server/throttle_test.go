package server

import (
	"strings"
	"testing"
	"time"
)

// TestThrottleStampsOnlyWhenItAdmits pins the throttle's stamping rule, which is
// argued at length on tooSoon and was pinned by nothing: making the REFUSING branch re-stamp — the self-lockout
// the doc singles out as the mistake to avoid — passed the whole suite, because
// every test above it either paces itself past the gap or rewinds the stamp.
//
// The clock is a parameter, so the rule is asserted directly rather than through
// a sleep: a caller polling faster than the gap must not push its own next
// admission away.
func TestThrottleStampsOnlyWhenItAdmits(t *testing.T) {
	const gap = 250 * time.Millisecond
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tt := throttle{gap: gap}

	if _, tooSoon := tt.tooSoon("c1", t0); tooSoon {
		t.Fatal("the first attempt was refused; an unstamped throttle must admit")
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
			"the throttle holds one identity, so the previous stamp is gone")
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

// rewind pushes a throttle's stamp into the past, so a test can cross its gap
// without sleeping through it. One helper for both caps rather than one each:
// they are one type, and a test that reached into the fields itself would keep
// working after the stamping rule changed under it.
func rewind(s *Server, t *throttle, by time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.at = t.at.Add(-by)
}
