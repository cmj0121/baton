package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/panel"
)

// sigGroup builds a group whose members carry the given signatures, so a case can
// state "these eight look alike and these two do not" in one line. The ids run
// a, b, c… in fleet order.
func sigGroup(name string, sigs ...string) []panel.Panel {
	fleet := make([]panel.Panel, 0, len(sigs))
	for i, s := range sigs {
		fleet = append(fleet, panel.Panel{
			ID: string(rune('a' + i)), Title: "p", State: panel.Running, Group: name, Sig: s,
		})
	}
	return fleet
}

// repeatSig is n members all showing the same thing, for the "48 identical" shape.
func repeatSig(sig string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = sig
	}
	return out
}

// splitGroup zooms a fleet's only group with a visible-tile count of n and
// returns the split it produces.
func splitGroup(t *testing.T, fleet []panel.Panel, n int) (tiles, collapsed []panel.Panel) {
	t.Helper()
	m := baseModel()
	m.foldSimilar = true
	m.fleet = fleet
	m.groupShown = map[string]int{"big": n}
	m = m.zoomGroup(m.dashItems()[0])
	return m.splitMembers()
}

// TestFoldSimilarKeepsTheOutliers is the issue's actual promise, at the smallest
// size that shows it: eight of ten members look alike, so those eight fold and
// the two that differ keep the live tiles — even though positionally they are
// last in the group and would have been the first thing folded away.
func TestFoldSimilarKeepsTheOutliers(t *testing.T) {
	fleet := sigGroup("big", append(repeatSig("same", 8), "odd1", "odd2")...)
	tiles, collapsed := splitGroup(t, fleet, 4)

	if got := ids(tiles); len(got) != 2 || got[0] != "i" || got[1] != "j" {
		t.Fatalf("the two outliers should hold the live tiles, got %v", got)
	}
	if len(collapsed) != 8 {
		t.Fatalf("the eight lookalikes should fold, got %v", ids(collapsed))
	}
}

// TestFoldSimilarFallsBackWhenEverythingDiffers is the guard the fold must never
// fail: a group where no two members look alike has no majority to fold, and
// folding everything away because nothing matched would hide the whole group.
// It falls back to exactly today's positional split.
func TestFoldSimilarFallsBackWhenEverythingDiffers(t *testing.T) {
	fleet := sigGroup("big", "s0", "s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9")
	tiles, collapsed := splitGroup(t, fleet, 4)

	if got := strings.Join(ids(tiles), ","); got != "a,b,c,d" {
		t.Fatalf("with no majority the first N should tile, got %v", got)
	}
	if len(collapsed) != 6 {
		t.Fatalf("the rest should fold positionally, got %v", ids(collapsed))
	}
}

// TestFoldSimilarFallsBackWhenEverythingMatches covers the other degenerate end:
// a group where every member looks the same has no outlier to promote, so the
// similarity fold has nothing to say and the positional split stands.
func TestFoldSimilarFallsBackWhenEverythingMatches(t *testing.T) {
	tiles, collapsed := splitGroup(t, sigGroup("big", repeatSig("same", 10)...), 4)
	if got := strings.Join(ids(tiles), ","); got != "a,b,c,d" {
		t.Fatalf("an all-alike group should split positionally, got %v", got)
	}
	if len(collapsed) != 6 {
		t.Fatalf("an all-alike group should fold the overflow only, got %v", ids(collapsed))
	}
}

// TestFoldSimilarWithNoSignatures checks a daemon too old to send the field —
// every member's signature empty — degrades to today's split rather than folding
// the whole group into one tile. Unknown is not the same as identical.
func TestFoldSimilarWithNoSignatures(t *testing.T) {
	tiles, _ := splitGroup(t, bigGroup("big", 10), 4)
	if got := strings.Join(ids(tiles), ","); got != "a,b,c,d" {
		t.Fatalf("without signatures the split should be positional, got %v", got)
	}
}

// TestFoldSimilarBelowTheCap checks the fold stays out of the way of a small
// group: everything fits in the visible count, so nothing folds at all and a
// fleet of five renders exactly as it always has.
func TestFoldSimilarBelowTheCap(t *testing.T) {
	fleet := sigGroup("big", "same", "same", "same", "odd")
	tiles, collapsed := splitGroup(t, fleet, maxGroupTiles)
	if len(tiles) != 4 || collapsed != nil {
		t.Fatalf("a group inside its tile count should not fold, got %v + %v", ids(tiles), ids(collapsed))
	}
}

// TestFoldSimilarKeepsPins checks an explicit instruction beats the heuristic: a
// pinned member the fold would otherwise class as a lookalike stays a live tile,
// and takes from the tile budget first.
func TestFoldSimilarKeepsPins(t *testing.T) {
	fleet := sigGroup("big", append(repeatSig("same", 8), "odd1", "odd2")...)
	m := baseModel()
	m.foldSimilar = true
	m.fleet = fleet
	m.groupShown = map[string]int{"big": 4}
	m = m.zoomGroup(m.dashItems()[0])
	m.groupPinned = map[string]bool{"a": true}

	tiles, collapsed := m.splitMembers()
	if got := strings.Join(ids(tiles), ","); got != "a,i,j" {
		t.Fatalf("the pin should tile alongside the outliers, got %v", got)
	}
	if len(collapsed) != 7 {
		t.Fatalf("the remaining lookalikes should fold, got %v", ids(collapsed))
	}
}

// TestFoldSimilarCapsTheTiles checks the tile budget still binds: a group with
// more outliers than N cannot spin up an emulator per outlier, so the overflow
// folds too.
func TestFoldSimilarCapsTheTiles(t *testing.T) {
	sigs := append(repeatSig("same", 11), "o1", "o2", "o3", "o4", "o5", "o6")
	tiles, collapsed := splitGroup(t, sigGroup("big", sigs...), 4)
	if len(tiles) != 4 {
		t.Fatalf("the tile budget should cap the outliers at N, got %v", ids(tiles))
	}
	if got := strings.Join(ids(tiles), ","); got != "l,m,n,o" {
		t.Fatalf("the first four outliers should tile, in fleet order, got %v", got)
	}
	if len(collapsed) != 13 {
		t.Fatalf("everyone else should fold, got %v", ids(collapsed))
	}
	// Both halves stay in fleet order, so the summary roster reads the way the
	// group has always been listed.
	if got := strings.Join(ids(collapsed), ","); got != "a,b,c,d,e,f,g,h,i,j,k,p,q" {
		t.Fatalf("the folded half should stay in fleet order, got %v", got)
	}
}

// TestFoldSimilarOffIsTheOldSplit checks settings.fold-similar: false gives back
// the positional split exactly, on a group the similarity fold would otherwise
// have partitioned the other way round.
func TestFoldSimilarOffIsTheOldSplit(t *testing.T) {
	m := baseModel()
	m.foldSimilar = false
	m.fleet = sigGroup("big", append(repeatSig("same", 8), "odd1", "odd2")...)
	m.groupShown = map[string]int{"big": 4}
	m = m.zoomGroup(m.dashItems()[0])

	tiles, collapsed := m.splitMembers()
	if got := strings.Join(ids(tiles), ","); got != "a,b,c,d" {
		t.Fatalf("fold-similar off should tile the first N, got %v", got)
	}
	if got := strings.Join(ids(collapsed), ","); got != "e,f,g,h,i,j" {
		t.Fatalf("fold-similar off should fold the rest positionally, got %v", got)
	}
}

// TestSummaryTileNamesTheFold checks the tile says which question it answered:
// "+8 identical" when the folded members all look alike, and a distinct count
// beside "+N more" when they do not.
func TestSummaryTileNamesTheFold(t *testing.T) {
	fleet := sigGroup("big", append(repeatSig("same", 8), "odd1", "odd2")...)
	m := baseModel()
	m.foldSimilar = true
	m.fleet = fleet
	m.groupShown = map[string]int{"big": 4}
	m = m.zoomGroup(m.dashItems()[0])

	_, collapsed := m.splitMembers()
	if got := m.renderSummaryTile(collapsed, false, 40, 12, gtileGap); !strings.Contains(got, "+8 identical") {
		t.Fatalf("a similarity fold should name itself, got:\n%s", got)
	}

	// A mixed fold reports how much variety it swept up instead.
	mixed := sigGroup("big", "s0", "s1", "s2", "s3", "s4", "s5")
	out := m.renderSummaryTile(mixed, false, 40, 12, gtileGap)
	if !strings.Contains(out, "+6 more") || !strings.Contains(out, "6 distinct") {
		t.Fatalf("a mixed fold should report its distinct count, got:\n%s", out)
	}

	// Members with no signature at all say nothing about how alike they are.
	plain := m.renderSummaryTile(bigGroup("big", 3), false, 40, 12, gtileGap)
	if !strings.Contains(plain, "+3 more") || strings.Contains(plain, "identical") {
		t.Fatalf("unknown signatures should not claim identity, got:\n%s", plain)
	}
}

// TestFoldSimilarAt50 is the issue's headline case: 50 shells, one broadcast, 48
// identical and 2 that differ — and the 2 are the ones left on screen.
//
// The allocation budget here is exactly that and nothing more: a guard that the
// split does not start allocating per member. It is NOT an anti-quadratic
// tripwire and must not be read as one — a pairwise implementation allocates
// nothing per comparison and comes in UNDER this budget, which is measured fact
// rather than speculation. TestFoldSimilarStaysLinear is the guard that bites.
func TestFoldSimilarAt50(t *testing.T) {
	fleet := sigGroup("big", append(repeatSig("same", 48), "odd1", "odd2")...)
	pinned := map[string]bool{}

	tiles, collapsed, ok := partitionSimilar(fleet, pinned, maxGroupTiles)
	if !ok || len(tiles) != 2 || len(collapsed) != 48 {
		t.Fatalf("48 identical + 2 different should tile the 2, got ok=%v %d tiles %d folded",
			ok, len(tiles), len(collapsed))
	}

	const budget = 24 // measured at 15; the slack is for map growth on another runtime
	if got := testing.AllocsPerRun(100, func() { partitionSimilar(fleet, pinned, maxGroupTiles) }); got > budget {
		t.Fatalf("splitting 50 members took %v allocations, budget %d", got, budget)
	}
}

// TestFoldSimilarStaysLinear is the real anti-quadratic guard, and it counts
// rather than times or measures allocations because neither of those can tell the
// two implementations apart: at fifty members both are far too fast to time, and
// comparing strings allocates nothing — QA wrote the pairwise version and it came
// in cheaper on allocations than this one.
//
// So partitionSimilar reports every signature it examines through sigProbe, and
// this asserts the count grows with the member count rather than with its square.
// The contract is "look at a signature, ping the probe". Measured against QA's
// pairwise shape honouring the same contract: this split examines 100 signatures
// at 50 members and 400 at 200, where the pairwise one examines 5000 and 80000 —
// so it blows the very first bound rather than sneaking under it.
func TestFoldSimilarStaysLinear(t *testing.T) {
	t.Cleanup(func() { sigProbe = nil })
	seen := 0
	sigProbe = func() { seen++ }

	// Two examinations per member today: one to tally, one to test against the
	// modal signature. Four leaves room for a third pass without inviting a fourth.
	const perMember = 4
	measure := func(n int) int {
		fleet := sigGroup("big", append(repeatSig("same", n-2), "odd1", "odd2")...)
		seen = 0
		partitionSimilar(fleet, map[string]bool{}, maxGroupTiles)
		return seen
	}
	for _, n := range []int{50, 200} {
		if got := measure(n); got > perMember*n {
			t.Fatalf("splitting %d members examined %d signatures, budget %d — the split is not linear",
				n, got, perMember*n)
		}
	}
}

// TestFoldSimilarPrefDefaultsOn checks the setting reaches the split: unset means
// on (the fleet case is the one the fold exists for), and false switches it off.
func TestFoldSimilarPrefReachesTheSplit(t *testing.T) {
	m := baseModel().applyPrefs(prefsFromConfig(config.Config{}))
	if !m.foldSimilar {
		t.Fatal("fold-similar should default on — the fleet case is the one it exists for")
	}
	off := false
	m = m.applyPrefs(prefsFromConfig(config.Config{Settings: config.Settings{FoldSimilar: &off}}))
	if m.foldSimilar {
		t.Fatal("fold-similar: false should switch the fold off")
	}
}
