package score

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file covers R3 (#42): the ranking that decides which few entries a brief
// carries, and the working-set budget that decides how many.
//
// The subject is ORDER. Nothing here may change a tier — #37 demotes nothing —
// and nothing here may read a clock, which is invariant I5 and the whole of #38
// §5.

// ids is the working set as a list of ids, which is what a determinism check
// actually compares: the same entries, in the same order.
func ids(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Id
	}
	return out
}

// seedFleet fills a store with entries from three different panels, reinforcing
// some of them, so that tier, recency and every context dimension vary across
// it — plus ONE entry too long to inject, because that is the only input on
// which the two selection paths differ structurally: renderLocked filters
// Injectable BEFORE ranking, orderRanked ranks everything and filters when it
// marks Active. Without one here, a regression that let a withheld entry eat a
// slot of the working-set budget in one path and not the other would pass every
// test in this file.
//
// It returns the directory, closed, ready to be reopened.
func seedFleet(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s := openStore(t, dir)

	panels := []Provenance{
		{Source: "agent", SourcePanel: "p1", SourceCwd: "/work/auth", SourceProfile: "claude", SourceGroup: "auth"},
		{Source: "agent", SourcePanel: "p2", SourceCwd: "/work/api", SourceProfile: "codex", SourceGroup: "api"},
		{Source: "user"},
	}
	for i := range 12 {
		e := submitAs(t, s, fmt.Sprintf("the fleet keeps doing the thing number %d", i), panels[i%len(panels)])
		// Every third entry is said again, so recency and tier both vary.
		if i%3 == 0 {
			if err := s.Reinforce(e.Id, "agent"); err != nil {
				t.Fatalf("Reinforce: %v", err)
			}
		}
	}
	// The over-long one goes in through the FILE: a submission this long is
	// refused outright, while an operator's own line is kept and only its
	// injection withheld (see maxEntryRunes). It lands mid-file rather than last
	// so no path can pass by treating it as a tail.
	md := readFile(t, dir, scoreMD)
	lines := strings.SplitAfter(md, "\n")
	at := len(lines) / 2
	writeMD(t, dir, strings.Join(lines[:at], "")+
		"- "+strings.Repeat("y", maxEntryRunes+1)+"\n"+
		strings.Join(lines[at:], ""))
	reconcile(t, s)
	if got := s.Health().Oversized; got != 1 {
		t.Fatalf("seeded fleet withholds %d entries, want exactly 1", got)
	}
	s.Close()
	return dir
}

// TestRankIsDeterministicAcrossOpens is #38's verification check 4: the same log
// and the same context yield the same working set, on two independent Opens and
// after a reload that puts the same policy back.
//
// Two OPENS rather than two calls, because a single store could agree with
// itself out of a cache while the replay that a second machine would run
// disagrees. Everything the ranking reads is rebuilt by that replay.
func TestRankIsDeterministicAcrossOpens(t *testing.T) {
	dir := seedFleet(t)
	ctx := Context{Panel: "p1", Cwd: "/work/auth", Profile: "claude", Group: "auth"}

	first := openStore(t, dir)
	want := ids(first.Render(ctx))
	if len(want) != defaultWorkingSet {
		t.Fatalf("working set = %d entries, want the budget of %d", len(want), defaultWorkingSet)
	}
	first.Close()

	second := openStore(t, dir)
	if got := ids(second.Render(ctx)); !equalIDs(got, want) {
		t.Fatalf("a second Open ranked %v, want %v", got, want)
	}
	// A reload that lands on the same numbers must land on the same order too:
	// the policy is compared, never remembered from the pass that used it.
	second.SetPolicy(Policy{})
	if got := ids(second.Render(ctx)); !equalIDs(got, want) {
		t.Fatalf("after a reload to the same policy the order was %v, want %v", got, want)
	}
	// And the same context asked twice in a row is the same answer, since a map
	// iteration anywhere on the path would show up here first.
	if got := ids(second.Render(ctx)); !equalIDs(got, want) {
		t.Fatalf("a repeated Render gave %v, want %v", got, want)
	}
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRankReadsNoClock is invariant I5 head-on. The log's timestamps are
// rewritten to run BACKWARDS — the shape a laptop that slept, an NTP correction
// and a timezone change all produce — and the working set must not move.
//
// Timestamps are recorded for the person reading the history and nothing ranks
// on them; if anything ever does, this is the test that says so.
func TestRankReadsNoClock(t *testing.T) {
	dir := seedFleet(t)
	ctx := Context{Panel: "p2", Cwd: "/work/api", Profile: "codex", Group: "api"}

	before := openStore(t, dir)
	want := ids(before.Render(ctx))
	before.Close()

	path := filepath.Join(dir, scoreEvents)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var out []string
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		rec["at"] = at.Format(time.RFC3339Nano)
		at = at.Add(-time.Hour) // every record older than the one before it
		redone, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		out = append(out, string(redone))
	}
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}

	after := openStore(t, dir)
	if got := ids(after.Render(ctx)); !equalIDs(got, want) {
		t.Fatalf("a log whose clock runs backwards ranked %v, want the unchanged %v", got, want)
	}
}

// TestRecencyIsTheLastReinforcementPosition is decision 3: an OLD entry the
// fleet said again outranks a newer one nothing has repeated, at the same tier.
// Creation position would rank them the other way round.
func TestRecencyIsTheLastReinforcementPosition(t *testing.T) {
	s := openStore(t, t.TempDir())
	old := submit(t, s, "the oldest thing the fleet knows")
	recent := submit(t, s, "something said once, later")

	// One reinforcement, which is not enough to earn a tier at the default
	// threshold of three occurrences — so tier cannot be what decides this.
	if err := s.Reinforce(old.Id, "agent"); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}

	got := s.Render(Context{})
	if len(got) != 2 {
		t.Fatalf("Render = %+v, want both entries", got)
	}
	if got[0].Tier != got[1].Tier {
		t.Fatalf("the fixture promoted something: %+v", got)
	}
	if got[0].Id != old.Id {
		t.Fatalf("the reinforced entry ranked %v, want %s first — recency is the LAST reinforcement", ids(got), old.Id)
	}
	_ = recent
}

// TestContextWeightsAreIndependent is decision 2: cwd, profile and group each
// multiply on their own, so a full match is the weight cubed and a partial match
// is exactly the factors that matched.
func TestContextWeightsAreIndependent(t *testing.T) {
	prov := Provenance{Source: "agent", SourcePanel: "p1", SourceCwd: "/work/auth", SourceProfile: "claude", SourceGroup: "auth"}
	tests := []struct {
		name string
		ctx  Context
		want Factors
	}{
		{"nothing matches", Context{Cwd: "/elsewhere", Profile: "codex", Group: "api"},
			Factors{Cwd: 1, Profile: 1, Group: 1}},
		{"cwd alone", Context{Cwd: "/work/auth"},
			Factors{Cwd: defaultRankWeight, Profile: 1, Group: 1}},
		{"profile alone", Context{Profile: "claude"},
			Factors{Cwd: 1, Profile: defaultRankWeight, Group: 1}},
		{"group alone", Context{Group: "auth"},
			Factors{Cwd: 1, Profile: 1, Group: defaultRankWeight}},
		{"all three", Context{Cwd: "/work/auth", Profile: "claude", Group: "auth"},
			Factors{Cwd: defaultRankWeight, Profile: defaultRankWeight, Group: defaultRankWeight}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openStore(t, t.TempDir())
			submitAs(t, s, "one entry, so recency cannot vary", prov)

			r := explainOne(t, s, tt.ctx)
			if r.Factors.Cwd != tt.want.Cwd || r.Factors.Profile != tt.want.Profile || r.Factors.Group != tt.want.Group {
				t.Fatalf("factors = %+v, want cwd/profile/group %v/%v/%v",
					r.Factors, tt.want.Cwd, tt.want.Profile, tt.want.Group)
			}
		})
	}
}

// TestAnEmptyDimensionNeverMatches is the rule an entry with no recorded value
// is ranked by: unknown is not a value that agrees with anything, itself
// included. Otherwise every entry the operator submitted from their own cockpit
// — which records no cwd, profile or group at all — would take a full match on
// every dispatch whose context the server could not fill.
func TestAnEmptyDimensionNeverMatches(t *testing.T) {
	s := openStore(t, t.TempDir())
	submitAs(t, s, "an entry the operator typed", Provenance{Source: "user"})

	for _, ctx := range []Context{{}, {Cwd: "/work/auth", Profile: "claude", Group: "auth"}} {
		r := explainOne(t, s, ctx)
		if r.Factors.Cwd != 1 || r.Factors.Profile != 1 || r.Factors.Group != 1 {
			t.Fatalf("context %+v matched an entry that records nothing: %+v", ctx, r.Factors)
		}
	}

	// And the other direction: an entry that DOES record a value is not matched
	// by a dispatch that records none.
	s2 := openStore(t, t.TempDir())
	submitAs(t, s2, "an entry from a panel", Provenance{Source: "agent", SourceCwd: "/work/auth"})
	if r := explainOne(t, s2, Context{}); r.Factors.Cwd != 1 {
		t.Fatalf("an empty context matched a recorded cwd: %+v", r.Factors)
	}
}

// explainOne is the single ranked entry of a store that holds exactly one.
func explainOne(t *testing.T, s *Store, ctx Context) Ranked {
	t.Helper()
	v, err := s.Explain(ctx)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if len(v.Ranked) != 1 {
		t.Fatalf("Explain returned %d entries, want exactly one", len(v.Ranked))
	}
	return v.Ranked[0]
}

// TestWeightOfOneDisablesItsDimension is decision 4's other half, and the only
// rule an operator has to remember about the knob: at 1.0 the dimension
// multiplies by one whether it matches or not.
func TestWeightOfOneDisablesItsDimension(t *testing.T) {
	prov := Provenance{Source: "agent", SourceCwd: "/work/auth", SourceProfile: "claude", SourceGroup: "auth"}
	ctx := Context{Cwd: "/work/auth", Profile: "claude", Group: "auth"}
	off := Rank{Recency: 1, Cwd: 1, Profile: 1, Group: 1}

	s := openStore(t, t.TempDir())
	first := submitAs(t, s, "one thing the fleet does", prov)
	second := submitAs(t, s, "another thing the fleet does", prov)
	s.SetPolicy(Policy{Rank: off})

	v, err := s.Explain(ctx)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	for _, r := range v.Ranked {
		if r.Factors != (Factors{Tier: 1, Recency: 1, Cwd: 1, Profile: 1, Group: 1}) {
			t.Fatalf("with every weight at 1.0 the factors were %+v, want all ones", r.Factors)
		}
		if r.Rank != 1 {
			t.Fatalf("rank = %v, want 1 with every dimension switched off", r.Rank)
		}
	}
	// Every rank is now equal, so the order is the tie-break's alone: the more
	// recently said first, then the smaller id. Both entries are tier 1 and
	// neither was reinforced, so the second submitted is the more recent.
	if got := ids(v.Entries); !equalIDs(got, []string{second.Id, first.Id}) {
		t.Fatalf("tie order = %v, want the later submission first (%s, %s)", got, second.Id, first.Id)
	}
}

// TestClampWeight is the boundary between "not set" and "set below the floor",
// which are two different instructions and land on two different numbers.
func TestClampWeight(t *testing.T) {
	tests := []struct {
		in, want float64
	}{
		{0, defaultRankWeight},          // the value of a key nobody wrote
		{-4, defaultRankWeight},         // a negative nobody means
		{math.NaN(), defaultRankWeight}, // not a number the operator wrote
		{0.5, minRankWeight},            // below the floor: raised, never honoured as a penalty
		{1, minRankWeight},              // the off switch, kept exactly
		{2.5, 2.5},                      // anything sane is kept exactly
		{1e300, maxRankWeight},          // past the ceiling: the product would saturate
		{math.Inf(1), maxRankWeight},
	}
	for _, tt := range tests {
		if got := clampWeight(tt.in); got != tt.want {
			t.Errorf("clampWeight(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// TestClampWorkingSet reads "fewer than one entry" as unset rather than as
// switching the memory off, which is what score.enabled is for.
func TestClampWorkingSet(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if got := clampWorkingSet(n); got != defaultWorkingSet {
			t.Errorf("clampWorkingSet(%d) = %d, want the default %d", n, got, defaultWorkingSet)
		}
	}
	for _, n := range []int{1, 3, 40} {
		if got := clampWorkingSet(n); got != n {
			t.Errorf("clampWorkingSet(%d) = %d, want it honoured", n, got)
		}
	}
}

// TestWeightFloorNeverPenalisesAMatch is the reason the floor exists: at any
// weight the operator can write, a matching entry ranks at least as high as an
// identical one that does not match. Below the floor it would rank LOWER, which
// is a semantics the knob does not offer.
func TestWeightFloorNeverPenalisesAMatch(t *testing.T) {
	for _, w := range []float64{0.1, 0.5, 1, 2, 8} {
		s := openStore(t, t.TempDir())
		here := submitAs(t, s, "a thing learned in this very directory",
			Provenance{Source: "agent", SourceCwd: "/work/auth"})
		elsewhere := submitAs(t, s, "a thing learned somewhere else",
			Provenance{Source: "agent", SourceCwd: "/work/api"})
		// Recency off, so cwd is the only factor that can vary.
		s.SetPolicy(Policy{Rank: Rank{Recency: 1, Cwd: w, Profile: 1, Group: 1}})

		v, err := s.Explain(Context{Cwd: "/work/auth"})
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		byID := map[string]Ranked{}
		for _, r := range v.Ranked {
			byID[r.Id] = r
		}
		if byID[here.Id].Rank < byID[elsewhere.Id].Rank {
			t.Fatalf("cwd weight %v ranked the matching entry BELOW the unmatched one: %v < %v",
				w, byID[here.Id].Rank, byID[elsewhere.Id].Rank)
		}
	}
}

// TestTieBreakIsTotal keeps the order reproducible where the arithmetic runs
// out: equal rank goes to the more recently reinforced, and equal position to
// the smaller id. Without the second key the sort is free to place ties however
// it likes, and #38's check 4 fails on a machine whose sort differs.
func TestTieBreakIsTotal(t *testing.T) {
	// Planted, because a store cannot be made to hold two entries whose LAST
	// REINFORCEMENT is the same record: every event moves exactly one entry.
	s := &Store{policy: Policy{}.clamp(), lastAt: map[string]int{
		"aaaaaa": 4, "bbbbbb": 4, "cccccc": 9,
	}, entries: []Entry{
		{Id: "bbbbbb", Text: "b", Tier: 1},
		{Id: "cccccc", Text: "c", Tier: 1},
		{Id: "aaaaaa", Text: "a", Tier: 1},
	}}

	// c is newest, so it leads on the position key; a and b tie on position and
	// separate on id.
	want := []string{"cccccc", "aaaaaa", "bbbbbb"}
	if got := ids(s.Render(Context{})); !equalIDs(got, want) {
		t.Fatalf("tie order = %v, want %v", got, want)
	}
	// The uncapped list must break the tie the same way, or an operator's view of
	// the order would not be the order. The locked halves rather than Explain: this
	// store was planted rather than opened, so it has no files to reconcile.
	var listed []string
	listedRanked, _ := orderRanked(s.rankAllLocked(Context{}), s.policy)
	for _, r := range listedRanked {
		listed = append(listed, r.Id)
	}
	if !equalIDs(listed, want) {
		t.Fatalf("the list broke the tie as %v, want %v", listed, want)
	}
}

// TestWorkingSetBudgetIsRespected covers the knob and the two surfaces at once:
// a brief carries the budget, and the list carries everything with exactly the
// budget marked.
func TestWorkingSetBudgetIsRespected(t *testing.T) {
	for _, n := range []int{1, 3, 7, 20} {
		s := openStore(t, t.TempDir())
		s.SetPolicy(Policy{WorkingSet: n})
		for i := range 10 {
			submit(t, s, fmt.Sprintf("the fleet keeps doing the thing number %d", i))
		}

		want := min(n, 10)
		if got := len(s.Render(Context{})); got != want {
			t.Fatalf("working-set %d: a brief carried %d entries, want %d", n, got, want)
		}
		v, err := s.Explain(Context{})
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		if len(v.Ranked) != 10 {
			t.Fatalf("working-set %d: the list showed %d entries, want all 10 — the budget caps the brief, not the store", n, len(v.Ranked))
		}
		active := 0
		for _, r := range v.Ranked {
			if r.Active {
				active++
			}
		}
		if active != want {
			t.Fatalf("working-set %d: %d entries marked active, want %d", n, active, want)
		}
	}
}

// TestRankingChangesOrderNeverTier is #37's "nothing is demoted", checked
// against the one thing R3 adds that could break it. Every weight is retuned to
// something extreme and every entry's tier must be exactly what it was.
func TestRankingChangesOrderNeverTier(t *testing.T) {
	dir := seedFleet(t)
	s := openStore(t, dir)

	before := map[string]int{}
	v, err := s.Explain(Context{})
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	for _, r := range v.Ranked {
		before[r.Id] = r.Tier
	}

	for _, p := range []Policy{
		{WorkingSet: 1, Rank: Rank{Recency: 1, Cwd: 1, Profile: 1, Group: 1}},
		{WorkingSet: 50, Rank: Rank{Recency: 100, Cwd: 100, Profile: 100, Group: 100}},
		{},
	} {
		s.SetPolicy(p)
		v, err := s.Explain(Context{Cwd: "/work/auth", Profile: "claude", Group: "auth"})
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		for _, r := range v.Ranked {
			if r.Tier != before[r.Id] {
				t.Fatalf("policy %+v moved %s from tier %d to %d", p, r.Id, before[r.Id], r.Tier)
			}
		}
	}
}

// TestRankingCannotReachTierThree keeps the top tier out of the ranking's reach:
// it needs a user signal (invariant I6), and no amount of ranking pressure is
// one. Ranking reads a tier and never writes one, so the pressure here is on the
// factors rather than on the ladder — which is exactly what must not matter.
func TestRankingCannotReachTierThree(t *testing.T) {
	s := openStore(t, t.TempDir())
	s.SetPolicy(Policy{PromoteAt: 2, Rank: Rank{Recency: 100, Cwd: 100, Profile: 100, Group: 100}})
	prov := Provenance{Source: "agent", SourcePanel: "p1", SourceCwd: "/work/auth", SourceProfile: "claude", SourceGroup: "auth"}
	ctx := Context{Panel: "p1", Cwd: "/work/auth", Profile: "claude", Group: "auth"}

	for range 40 {
		submitAs(t, s, "the same observation, over and over", prov)
		if _, err := s.Explain(ctx); err != nil {
			t.Fatalf("Explain: %v", err)
		}
	}
	v, err := s.Explain(ctx)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	for _, r := range v.Ranked {
		if r.Tier > agentEarnedTier {
			t.Fatalf("an agent's repetition reached tier %d, want no more than %d", r.Tier, agentEarnedTier)
		}
		if r.Factors.Tier > float64(agentEarnedTier) {
			t.Fatalf("the tier factor was %v, want no more than %d", r.Factors.Tier, agentEarnedTier)
		}
	}
}

// TestFactorsMultiplyOutToTheReportedRank is what makes the breakdown an
// explanation rather than a decoration: an operator who multiplies the five
// numbers gets the number beside them, exactly.
func TestFactorsMultiplyOutToTheReportedRank(t *testing.T) {
	dir := seedFleet(t)
	s := openStore(t, dir)
	s.SetPolicy(Policy{Rank: Rank{Recency: 3, Cwd: 2.5, Profile: 1, Group: 4}})

	v, err := s.Explain(Context{Panel: "p1", Cwd: "/work/auth", Profile: "claude", Group: "auth"})
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if len(v.Ranked) == 0 {
		t.Fatal("Explain returned nothing to check")
	}
	for _, r := range v.Ranked {
		f := r.Factors
		if got := f.Tier * f.Recency * f.Cwd * f.Profile * f.Group; got != r.Rank {
			t.Fatalf("entry %s: factors %+v multiply to %v, but the rank reads %v", r.Id, f, got, r.Rank)
		}
		if f.Tier < 1 || f.Recency < 1 || f.Cwd < 1 || f.Profile < 1 || f.Group < 1 {
			t.Fatalf("entry %s: a factor below one penalises rather than rewards: %+v", r.Id, f)
		}
	}
	// Ranked is in rank order, which is what makes the list readable without
	// sorting it again.
	for i := 1; i < len(v.Ranked); i++ {
		if v.Ranked[i-1].Rank < v.Ranked[i].Rank {
			t.Fatalf("Ranked is out of order at %d: %v then %v", i, v.Ranked[i-1].Rank, v.Ranked[i].Rank)
		}
	}
}

// TestExplainAgreesWithTheBrief keeps the operator's view honest: the entries
// the list marks active are exactly the ones a dispatch for the same context
// would inject, in the same order. Two selection paths exist for cost reasons
// (see renderLocked), and this is what holds them together.
func TestExplainAgreesWithTheBrief(t *testing.T) {
	dir := seedFleet(t)
	s := openStore(t, dir)

	for _, ctx := range []Context{
		{},
		{Panel: "p1", Cwd: "/work/auth", Profile: "claude", Group: "auth"},
		{Cwd: "/work/api"},
		{Group: "auth", Profile: "codex"},
	} {
		for _, p := range []Policy{{}, {WorkingSet: 2}, {WorkingSet: 30}, {Rank: Rank{Recency: 1, Cwd: 9}}} {
			s.SetPolicy(p)
			v, err := s.Explain(ctx)
			if err != nil {
				t.Fatalf("Explain: %v", err)
			}
			var active []string
			for _, r := range v.Ranked {
				if r.Active {
					active = append(active, r.Id)
				}
			}
			if got := ids(s.Render(ctx)); !equalIDs(got, active) {
				t.Fatalf("context %+v policy %+v: the brief carried %v, the list marked %v", ctx, p, got, active)
			}
			// A budget wider than the store isolates the ONE filter the two paths
			// apply at different moments — renderLocked before ranking, orderRanked
			// when marking. Something must still be left unmarked here, or the case
			// that exercises it has quietly stopped doing so (see seedFleet).
			if p.WorkingSet > len(v.Ranked) && len(active) == len(v.Ranked) {
				t.Fatalf("policy %+v: every one of %d entries was marked, so nothing withheld is under test", p, len(v.Ranked))
			}
		}
	}
}

// TestOversizedIsRankedButNeverActive keeps the two reasons an entry is out of
// the brief distinguishable: too long to inject (maxEntryRunes) is not the same
// as ranked below the budget, and the list shows both.
func TestOversizedIsRankedButNeverActive(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	submit(t, s, "a short entry")
	// Through the FILE, because a submission this long is refused outright; an
	// operator's own line is kept and only its injection withheld.
	writeMD(t, dir, "- [aaaaaa] "+strings.Repeat("x", maxEntryRunes+1)+"\n- a short entry\n")
	reconcile(t, s)

	v, err := s.Explain(Context{})
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	seen := false
	for _, r := range v.Ranked {
		if r.Id != "aaaaaa" {
			continue
		}
		seen = true
		if r.Active {
			t.Fatalf("an entry too long to inject was marked active: %+v", r)
		}
		if r.Rank <= 0 {
			t.Fatalf("an oversized entry was left unranked: %+v", r)
		}
	}
	if !seen {
		t.Fatalf("the oversized entry is missing from the list: %+v", v.Ranked)
	}
	if v.Health.Oversized != 1 {
		t.Fatalf("oversized gauge = %d, want 1", v.Health.Oversized)
	}
}

// TestBlockIsCappedInBytesNotJustEntries is the backstop on what a working set
// can be made to weigh. score.working-set has no ceiling on purpose — #37 calls
// a handful a default, not a rule — so without a rune cap `working-set:
// 1000000` on a full store prepends a third of a megabyte to every direct
// dispatch, which is the failure maxEntryRunes exists to stop one level up.
func TestBlockIsCappedInBytesNotJustEntries(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	// Fifty maximal entries: far past the rune budget, well under a count budget
	// wide enough that only the runes can be what stops it.
	var md strings.Builder
	for i := range 50 {
		fmt.Fprintf(&md, "- %s%03d\n", strings.Repeat("z", maxEntryRunes-3), i)
	}
	writeMD(t, dir, md.String())
	reconcile(t, s)
	if s.Len() != 50 {
		t.Fatalf("seeded %d entries, want 50", s.Len())
	}
	s.SetPolicy(Policy{WorkingSet: 1_000_000})

	got := s.Render(Context{})
	if len(got) >= 50 {
		t.Fatalf("a working set of a million took %d of 50 entries; the rune backstop never bit", len(got))
	}
	block := renderBlock(got)
	if n := len([]rune(block)); n > maxBlockRunes+64 {
		t.Fatalf("the rendered block is %d runes, want no more than %d plus its border", n, maxBlockRunes)
	}
	// Never truncated mid-entry (#42): every entry that made it is in the block
	// whole, and the count is what stopped short.
	for _, e := range got {
		if !strings.Contains(block, e.Text) {
			t.Fatalf("entry %s was cut short in the block", e.Id)
		}
	}
	// And the drop is visible rather than silent: the list ranks all fifty and
	// marks exactly the ones the brief carries.
	v, err := s.Explain(Context{})
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if len(v.Ranked) != 50 {
		t.Fatalf("score.list showed %d entries, want all 50", len(v.Ranked))
	}
	if !equalIDs(ids(v.Entries), ids(got)) {
		t.Fatalf("the list marked %v, the brief carried %v", ids(v.Entries), ids(got))
	}
}

// TestBlockCapKeepsTheTwoPathsAgreeing is the one input on which the count
// budget and the rune backstop can disagree: renderLocked selects the
// highest-ranked few and then trims, while orderRanked walks everything and
// marks. They agree only because a rune failure ENDS both walks rather than
// skipping to something lighter — see budget.take.
func TestBlockCapKeepsTheTwoPathsAgreeing(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	// A mix of maximal and tiny entries, so a skipping rule would let the list
	// mark a light entry the brief could not have reached.
	var md strings.Builder
	for i := range 60 {
		if i%2 == 0 {
			fmt.Fprintf(&md, "- %s%03d\n", strings.Repeat("z", maxEntryRunes-3), i)
			continue
		}
		fmt.Fprintf(&md, "- a short one, number %d\n", i)
	}
	writeMD(t, dir, md.String())
	reconcile(t, s)

	for _, n := range []int{1, 7, 40, 1_000_000} {
		s.SetPolicy(Policy{WorkingSet: n})
		v, err := s.Explain(Context{})
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		var active []string
		for _, r := range v.Ranked {
			if r.Active {
				active = append(active, r.Id)
			}
		}
		if got := ids(s.Render(Context{})); !equalIDs(got, active) {
			t.Fatalf("working-set %d: the brief carried %v, the list marked %v", n, got, active)
		}
	}
}

// TestStandingTellsTheThreeExclusionsApart is the I8 obligation on the other
// half of the question. The breakdown answers "why is this entry in my brief";
// "why is this one NOT" has three answers — below the budget, the block filled,
// too long to inject — and none of them is visible from an entry's own fields.
// An operator acts differently on each: widen score.working-set, shorten entries
// or narrow the budget, or edit the line.
func TestStandingTellsTheThreeExclusionsApart(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	// Thirty maximal entries — more than the rune backstop can carry, since it
	// buys twenty-five of them — one line past the per-entry cap, and a short one
	// to be pushed below the budget.
	var md strings.Builder
	for i := range 30 {
		fmt.Fprintf(&md, "- %s%03d\n", strings.Repeat("z", maxEntryRunes-3), i)
	}
	fmt.Fprintf(&md, "- %s\n", strings.Repeat("y", maxEntryRunes+1))
	md.WriteString("- a short one nobody will reach\n")
	writeMD(t, dir, md.String())
	reconcile(t, s)

	// A budget of four, so the count is what stops it first.
	s.SetPolicy(Policy{WorkingSet: 4})
	seen := map[Standing]int{}
	v, err := s.Explain(Context{})
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	for _, r := range v.Ranked {
		if r.Standing == "" {
			t.Fatalf("entry %s was listed with no standing at all: %+v", r.Id, r)
		}
		if r.Active != (r.Standing == StandingActive) {
			t.Fatalf("entry %s: active=%v disagrees with standing %q", r.Id, r.Active, r.Standing)
		}
		seen[r.Standing]++
	}
	if seen[StandingActive] != 4 {
		t.Fatalf("standings %v, want four active under a budget of four", seen)
	}
	if seen[StandingOversized] != 1 {
		t.Fatalf("standings %v, want the over-long line reported as oversized", seen)
	}
	if seen[StandingBelowBudget] == 0 {
		t.Fatalf("standings %v, want the entries the budget did not reach", seen)
	}
	if seen[StandingBlockFull] != 0 {
		t.Fatalf("standings %v, want no block-full when the COUNT is what stopped it", seen)
	}

	// Now a budget wider than the store, so the rune backstop is the only thing
	// that can stop it — and the reason has to change with it, which is the whole
	// point of carrying one.
	s.SetPolicy(Policy{WorkingSet: 1_000_000})
	seen = map[Standing]int{}
	if v, err = s.Explain(Context{}); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	for _, r := range v.Ranked {
		seen[r.Standing]++
	}
	if seen[StandingBlockFull] == 0 {
		t.Fatalf("standings %v, want the entries the block had no room for", seen)
	}
	if seen[StandingBelowBudget] != 0 {
		t.Fatalf("standings %v, want no below-budget under a budget of a million", seen)
	}
	if seen[StandingOversized] != 1 {
		t.Fatalf("standings %v, want the over-long line still reported for its own weight", seen)
	}
	// An oversized entry never closes the budget and never eats a slot, whatever
	// its rank — which is what keeps the two selection paths agreeing.
	if seen[StandingActive] == 0 {
		t.Fatalf("standings %v, want the block filled before it stopped", seen)
	}

	// And the regime BETWEEN those two, where both caps bite: a budget wider than
	// the block can fill but narrower than the store. Every entry past the count
	// is excluded twice over, and the reason has to name the cap the operator can
	// turn — maxBlockRunes is not configurable, so answering block-full there
	// would send them to shorten entries when a wider score.working-set is what
	// would let the entry in.
	fits := seen[StandingActive] // what the rune backstop allows
	injectable := len(v.Ranked) - seen[StandingOversized]
	budget := fits + 3
	if injectable <= budget {
		t.Fatalf("the fixture needs entries past a budget of %d, and holds %d injectable", budget, injectable)
	}
	s.SetPolicy(Policy{WorkingSet: budget})
	seen = map[Standing]int{}
	if v, err = s.Explain(Context{}); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	var active []string
	pos := 0 // rank position among the injectable, which is what the count budget counts
	for _, r := range v.Ranked {
		seen[r.Standing]++
		if r.Standing == StandingActive {
			active = append(active, r.Id)
		}
		if r.Standing == StandingOversized {
			continue
		}
		pos++
		if pos > budget && r.Standing != StandingBelowBudget {
			t.Fatalf("entry %d of %d is past the budget and reads %q, want below-budget: the block cap is not the one to widen",
				pos, budget, r.Standing)
		}
		if pos <= budget && r.Standing == StandingBelowBudget {
			t.Fatalf("entry %d is inside the budget of %d and still reads below-budget", pos, budget)
		}
	}
	if seen[StandingActive] != fits || seen[StandingBlockFull] != budget-fits {
		t.Fatalf("standings %v, want %d active and %d block-full: the block still binds first", seen, fits, budget-fits)
	}
	if seen[StandingBelowBudget] != injectable-budget {
		t.Fatalf("standings %v, want %d below-budget", seen, injectable-budget)
	}
	// The standing moved to the knob the operator can turn; the STATUS gauge must
	// not have. View.BlockFull reports which cap stopped the taking first, and
	// here that is still the runes — a below-budget tail behind it must not
	// overwrite that.
	if !v.BlockFull {
		t.Fatal("BlockFull is false at a budget the rune backstop stopped first")
	}
	// The reason moved; the membership must not have. This is the same
	// differential TestStandingMatchesTheBrief runs, asked at the one budget where
	// both caps are in play.
	if got := ids(s.Render(Context{})); !equalIDs(got, active) {
		t.Fatalf("working-set %d: the brief carried %v, the list marked %v", budget, got, active)
	}
}

// TestStandingMatchesTheBrief keeps the reason honest against the thing it is a
// reason ABOUT: exactly the entries a dispatch carries are the ones the list
// calls active, at every budget and past the rune backstop.
func TestStandingMatchesTheBrief(t *testing.T) {
	dir := seedFleet(t)
	s := openStore(t, dir)

	for _, n := range []int{1, 3, 7, 100, 1_000_000} {
		s.SetPolicy(Policy{WorkingSet: n})
		v, err := s.Explain(Context{})
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		var active []string
		for _, r := range v.Ranked {
			if r.Standing == StandingActive {
				active = append(active, r.Id)
			}
		}
		if got := ids(s.Render(Context{})); !equalIDs(got, active) {
			t.Fatalf("working-set %d: the brief carried %v, standing said %v", n, got, active)
		}
	}
}

// TestBlockRunesArithmetic pins the number maxBlockRunes' doc states. The doc
// says twenty-four maximal entries, and it once said twenty-six — 8000/300, the
// division done without the function that adds the bullet and the tier wording.
// A comment that has to be recomputed by hand is one that goes stale, so the
// recomputation lives here.
func TestBlockRunesArithmetic(t *testing.T) {
	maximal := strings.Repeat("z", maxEntryRunes)
	// Every rung the ladder now reaches, since it is the DEAREST line that sets
	// the twenty-four in maxBlockRunes' doc and the dearest wording is not the
	// top one's.
	for _, tc := range []struct{ tier, want int }{{1, 25}, {2, 24}, {3, 25}} {
		e := Entry{Text: maximal, Tier: tc.tier}
		if got := maxBlockRunes / blockRunes(e); got != tc.want {
			t.Fatalf("tier %d: %d runes each buys %d entries, but maxBlockRunes' doc says %d",
				tc.tier, blockRunes(e), got, tc.want)
		}
	}
	// And the prediction really is the rendered line's length, for every tier the
	// wording branches on — which is the pairing the comment used to ask the next
	// reader to keep by hand.
	for tier := 0; tier <= 4; tier++ {
		e := Entry{Text: "an entry with a bit of text", Tier: tier}
		var b strings.Builder
		writeBlockLine(&b, e)
		if got, want := blockRunes(e), len([]rune(b.String())); got != want {
			t.Fatalf("tier %d: blockRunes = %d, but the line it stands for is %d runes",
				tier, got, want)
		}
	}

	// minBlockRunes is the other direction — the CHEAPEST line rather than the
	// dearest — and its doc says it is the shape of one, not a guess. Derived here
	// over the same tiers, because nothing else holds it: a shorter tier wording
	// added to tierWording would make the true minimum smaller, leave
	// minBlockRunes and MaxReachableWorkingSet where they are, and the daemon
	// would warn about working-set budgets that a brief can in fact reach.
	cheapest := blockRunes(Entry{Text: "z", Tier: 0})
	for tier := 1; tier <= 4; tier++ {
		cheapest = min(cheapest, blockRunes(Entry{Text: "z", Tier: tier}))
	}
	if cheapest != minBlockRunes {
		t.Fatalf("the cheapest line a tier wording allows is %d runes, but minBlockRunes is %d — MaxReachableWorkingSet is %d and should be %d",
			cheapest, minBlockRunes, MaxReachableWorkingSet, maxBlockRunes/cheapest)
	}
}

// TestRankingIsIndependentOfFileOrder is why both tie-break keys come from the
// LOG. score.md is the operator's file and they may shuffle it in an editor;
// invariant I5 wants the ranking to be a function of the log and the context.
func TestRankingIsIndependentOfFileOrder(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	a := submit(t, s, "the first thing")
	b := submit(t, s, "the second thing")
	c := submit(t, s, "the third thing")

	want := ids(s.Render(Context{}))
	writeMD(t, dir, fmt.Sprintf("- [%s] the third thing\n- [%s] the first thing\n- [%s] the second thing\n", c.Id, a.Id, b.Id))
	reconcile(t, s)

	if got := ids(s.Render(Context{})); !equalIDs(got, want) {
		t.Fatalf("shuffling score.md reordered the brief: %v, want %v", got, want)
	}
}

// BenchmarkRankWorkingSet measures the pass R3 adds to the DISPATCH path: every
// brief ranks the store to choose the few entries it carries.
//
// The selection is bounded by the working set rather than by the store, so the
// cost here is one rank per entry and an insertion into a slice a handful long
// — never a sort of the whole store, and never an allocation the size of it.
// That is the whole reason Explain is a separate entry point.
func BenchmarkRankWorkingSet(b *testing.B) {
	for _, n := range []int{100, 5000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			s, err := Open(dir, Policy{})
			if err != nil {
				b.Fatalf("Open: %v", err)
			}
			b.Cleanup(s.Close)

			var md strings.Builder
			for i := range n {
				fmt.Fprintf(&md, "- the fleet keeps doing the thing number %d\n", i)
			}
			if err := os.WriteFile(filepath.Join(dir, scoreMD), []byte(md.String()), 0o600); err != nil {
				b.Fatalf("write: %v", err)
			}
			if _, err := s.Reconcile(); err != nil {
				b.Fatalf("seed reconcile: %v", err)
			}
			if s.Len() != n {
				b.Fatalf("entries = %d, want %d", s.Len(), n)
			}

			ctx := Context{Panel: "p1", Cwd: "/work/auth", Profile: "claude", Group: "auth"}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				s.mu.Lock()
				got, _ := s.renderLocked(ctx)
				s.mu.Unlock()
				if len(got) != defaultWorkingSet {
					b.Fatalf("working set = %d entries, want %d", len(got), defaultWorkingSet)
				}
			}
		})
	}
}
