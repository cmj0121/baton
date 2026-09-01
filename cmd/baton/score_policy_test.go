package main

import (
	"errors"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/score"
)

// TestScorePolicyGate pins the rule S1 was filed for: boot and reload decide
// what tunes the score store the SAME way.
//
// They did not. Boot handed openScore the config struct whatever config.Load's
// error said, while the reload path gated SetPolicy on that error being nil, so
// one mistyped weight was enough for a restart to apply the half-parsed file
// while a SIGHUP applied nothing — two live policies from one file, chosen by
// whether the operator reloaded or restarted. The gate is one function now, and
// this is what keeps it one.
func TestScorePolicyGate(t *testing.T) {
	cfg := config.ScoreConfig{
		PromoteAt: 8, UserSignalsAt: 4, WorkingSet: 9,
		Rank: config.RankConfig{Recency: 2, Cwd: 3, Profile: 3, Group: 3},
	}

	got, ok := scorePolicy(cfg, nil)
	if !ok {
		t.Fatal("a config that parsed should choose the policy")
	}
	want := score.Policy{
		PromoteAt: 8, UserSignalsAt: 4, WorkingSet: 9,
		Rank: score.Rank{Recency: 2, Cwd: 3, Profile: 3, Group: 3},
	}
	if got != want {
		t.Fatalf("policy = %+v, want %+v", got, want)
	}

	// The same struct, reached through a file that would not parse: config.Load
	// returns its partially-populated value ALONGSIDE the error, and taking it
	// would be a policy the operator's file never actually asked for.
	if got, ok := scorePolicy(cfg, errors.New("parse config: bad")); ok || got != (score.Policy{}) {
		t.Fatalf("a failed load chose %+v (ok=%v), want no policy at all", got, ok)
	}
}

// TestScorePolicyGateReachesTheStore closes the loop the gate exists for: the
// zero policy a failed load produces is the store's own defaults, not a
// half-parsed file's numbers. At boot there is nothing to keep, so the defaults
// are the fallback; on a reload the running policy stands untouched. Both are
// the one rule — a broken file never chooses.
func TestScorePolicyGateReachesTheStore(t *testing.T) {
	cfg := config.ScoreConfig{PromoteAt: 8, WorkingSet: 9}

	p, _ := scorePolicy(cfg, errors.New("parse config: bad"))
	st, reason := openScore(cfg, p, scoreOpenTimeout)
	if st == nil {
		t.Fatalf("openScore refused: %s", reason)
	}
	t.Cleanup(st.Close)
	if got := st.Policy(); got.PromoteAt == 8 || got.WorkingSet == 9 {
		t.Fatalf("the store booted on %+v, want the package defaults over a file that would not parse", got)
	}

	// And a store already running keeps what it has: SetPolicy is never reached
	// on that branch, which is what the reload path's `ok` guard does.
	before := st.Policy()
	if _, ok := scorePolicy(cfg, errors.New("parse config: bad")); ok {
		t.Fatal("a failed load must not reach SetPolicy")
	}
	if st.Policy() != before {
		t.Fatalf("the running policy moved to %+v, want %+v", st.Policy(), before)
	}

	// The warnings run on both paths and must not panic on the zero policy the
	// gate hands them, which is the shape a failed load always produces.
	warnScorePolicy(config.Config{Score: config.ScoreConfig{
		BadNumbers: []string{"score.rank.cwd"},
	}}, score.Policy{}, st)

	// And with NO store — switched off, or a directory another daemon holds —
	// there is no in-force policy to compare against, so the clamp half must say
	// nothing rather than report every key the operator set as clamped to zero.
	// The key that is not a number is still named: that is a fact about the file.
	warnScorePolicy(config.Config{Score: config.ScoreConfig{
		PromoteAt: 1, BadNumbers: []string{"score.rank.cwd"},
	}}, score.Policy{PromoteAt: 1}, nil)
}

// TestWarnScorePolicySaysWhatWasClamped covers the other half of the same
// obligation (S6): a weight the operator wrote and the store did not honour.
// It runs for its side effects — the assertion is that every branch is reachable
// and none of them panics on the shapes the daemon actually produces.
func TestWarnScorePolicySaysWhatWasClamped(t *testing.T) {
	cfg := config.Config{}
	cfg.Panel.TrackCwd = "off"

	// Asked for below the floor and past the ceiling, plus a threshold and a
	// budget the store raised, plus a cwd weight that cannot ever match.
	want := score.Policy{
		PromoteAt: 1, UserSignalsAt: implausibleUserSignalsAt + 1, WorkingSet: -4,
		Rank: score.Rank{Recency: 0.5, Cwd: 1e300, Profile: 0, Group: 3},
	}
	// What the store makes of that, written out rather than computed: the
	// clamping rules are internal/score's, and this file is checking that the
	// daemon SAYS what they did, not re-deriving them.
	// The store is what says which numbers are actually in force, so the warning
	// is driven by a real one rather than by re-deriving internal/score's rules
	// here — that is the whole reason it takes the store and not a policy.
	st, reason := openScore(config.ScoreConfig{Dir: t.TempDir()}, want, scoreOpenTimeout)
	if st == nil {
		t.Fatalf("openScore refused: %s", reason)
	}
	t.Cleanup(st.Close)
	if got := st.Policy(); got.Rank.Cwd != 1e6 || got.Rank.Recency != 1 {
		t.Fatalf("the store held %+v, want the out-of-range weights clamped", got)
	}
	warnScorePolicy(cfg, want, st)

	// And the quiet case: everything in range, nothing to say.
	warnScorePolicy(config.Config{}, st.Policy(), st)

	// The dead-config half of the same obligation, which the clamped cases cannot
	// reach: score.working-set has no ceiling, so a budget past what the rune
	// backstop can ever spend is honoured in full and warned about instead. The
	// store is opened on it so the branch reads an IN-FORCE number, the way the
	// daemon does, rather than the one the file asked for.
	big := score.Policy{WorkingSet: score.MaxReachableWorkingSet + 1}
	dead, reason := openScore(config.ScoreConfig{Dir: t.TempDir()}, big, scoreOpenTimeout)
	if dead == nil {
		t.Fatalf("openScore refused: %s", reason)
	}
	t.Cleanup(dead.Close)
	if got := dead.Policy().WorkingSet; got != big.WorkingSet {
		t.Fatalf("the store held working-set %d, want %d unclamped: #37 leaves the count to the operator", got, big.WorkingSet)
	}
	warnScorePolicy(config.Config{}, big, dead)
}
