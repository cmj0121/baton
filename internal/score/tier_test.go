package score

import (
	"fmt"
	"strings"
	"testing"
)

// This file covers R2's other half (#41) and R4's ladder (#43): a tier earned by
// recurrence, the threshold's exact boundary, and the top rung — reachable only
// once the user has signalled for it (invariant I6).

// TestTierIsEarnedAtTheThreshold walks the boundary rather than sampling it: the
// occurrence before the threshold does not promote, and the one that meets it
// does. Off-by-one here is the whole feature.
func TestTierIsEarnedAtTheThreshold(t *testing.T) {
	for _, promoteAt := range []int{2, 3, 5} {
		t.Run(fmt.Sprintf("promote_at=%d", promoteAt), func(t *testing.T) {
			dir := t.TempDir()
			s := openStore(t, dir)
			s.SetPolicy(Policy{PromoteAt: promoteAt})

			e := submit(t, s, "keep the build green")
			if e.Tier != 1 {
				t.Fatalf("a first submission arrived at tier %d, want 1", e.Tier)
			}
			// One short of the threshold: still noted, never raised.
			for i := 2; i < promoteAt; i++ {
				said := submitAs(t, s, "keep the build green", Provenance{Source: "agent"})
				if said.Tier != 1 {
					t.Fatalf("said %d times of %d: tier %d, want 1", i, promoteAt, said.Tier)
				}
			}
			at := submitAs(t, s, "keep the build green", Provenance{Source: "agent"})
			if at.Tier != 2 {
				t.Fatalf("said %d times: tier %d, want 2", promoteAt, at.Tier)
			}

			// The raise is in the log with the tier it reached, so a replay lands
			// on the same ladder rung without recomputing it.
			var raised int
			for _, ev := range events(t, dir) {
				if ev.Event == EventRaised && ev.Id == e.Id {
					raised++
					if ev.Tier != 2 {
						t.Fatalf("raised event tier = %d, want 2", ev.Tier)
					}
				}
			}
			if raised != 1 {
				t.Fatalf("raised events = %d, want exactly one", raised)
			}
		})
	}
}

// TestTierThreeIsTheUsersToGrant is invariant I6 and #38's verification check 5,
// both halves in one place because they are one rule: the top tier is what #37
// reserves for the user asking repeatedly, so a hundred agent submissions of the
// same observation must not reach it and the user's own repetition must.
//
// The agent half deliberately uses BOTH counting paths open to an agent — a
// submission that folds, and Reinforce stamped SourceAgent — since I6 is a
// property of the ladder rather than of one door onto it. The exhaustive version
// of that argument is TestNoAgentReachableCallReachesTheTopTier, which enumerates
// the store's exported surface instead of naming two of it.
func TestTierThreeIsTheUsersToGrant(t *testing.T) {
	t.Run("a hundred agent submissions do not", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)

		e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent, SourcePanel: "p1"})
		for range 100 {
			if _, _, err := s.Submit("keep the build green", Provenance{Source: SourceAgent, SourcePanel: "p1"}); err != nil {
				t.Fatalf("Submit: %v", err)
			}
		}
		for range 10 {
			if err := s.Reinforce(e.Id, SourceAgent); err != nil {
				t.Fatalf("Reinforce: %v", err)
			}
		}

		got := s.Render(Context{})[0]
		if got.Tier != agentEarnedTier {
			t.Fatalf("tier = %d after 110 agent reinforcements, want %d", got.Tier, agentEarnedTier)
		}
		if got.Reinforcements != 110 {
			t.Fatalf("reinforcements = %d, want every repeat counted", got.Reinforcements)
		}
		if got.UserSignals != 0 {
			t.Fatalf("user signals = %d, want none: no user said any of this", got.UserSignals)
		}
		for _, ev := range events(t, dir) {
			if ev.Event == EventRaised && ev.Tier > agentEarnedTier {
				t.Fatalf("the log raised %s to tier %d; recurrence may not pass %d", ev.Id, ev.Tier, agentEarnedTier)
			}
		}

		// And a replay lands on the same rung: the tier comes from the log's raised
		// events, and no run of agent submissions wrote one above the ceiling.
		s.Close()
		if got := openStore(t, dir).Render(Context{})[0]; got.Tier != agentEarnedTier {
			t.Fatalf("tier after replay = %d, want %d", got.Tier, agentEarnedTier)
		}
	})

	t.Run("the user's own repetition does", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)

		// The entry arrives from an agent and climbs the ordinary ladder on agent
		// repeats alone, so what the user's signals add is exactly the last rung.
		e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent, SourcePanel: "p1"})
		for range defaultPromoteAt - 1 {
			if _, _, err := s.Submit("keep the build green", Provenance{Source: SourceAgent, SourcePanel: "p1"}); err != nil {
				t.Fatalf("Submit: %v", err)
			}
		}
		if got := s.Render(Context{})[0]; got.Tier != agentEarnedTier {
			t.Fatalf("tier = %d before the user says anything, want %d", got.Tier, agentEarnedTier)
		}

		for i := 1; i <= defaultUserSignalsAt; i++ {
			if err := s.Reinforce(e.Id, SourceUser); err != nil {
				t.Fatalf("Reinforce: %v", err)
			}
			got := s.Render(Context{})[0]
			if got.UserSignals != i {
				t.Fatalf("user signals = %d after %d, want them counted", got.UserSignals, i)
			}
			// The threshold's exact boundary: the signal BEFORE it lifts nothing,
			// and the one that meets it lifts the ceiling and the entry with it.
			want := agentEarnedTier
			if i >= defaultUserSignalsAt {
				want = maxEarnedTier
			}
			if got.Tier != want {
				t.Fatalf("tier = %d after %d of %d user signals, want %d", got.Tier, i, defaultUserSignalsAt, want)
			}
		}

		// Durable, and replayed: the raise is in the log with the tier it reached,
		// and the user-signal count is rebuilt from the sources on the log's own
		// records rather than carried in the snapshot cache.
		s.Close()
		again := openStore(t, dir).Render(Context{})[0]
		if again.Tier != maxEarnedTier || again.UserSignals != defaultUserSignalsAt {
			t.Fatalf("after replay = %+v, want tier %d with %d user signals", again, maxEarnedTier, defaultUserSignalsAt)
		}
	})
}

// TestUserSignalsAtBoundary walks the threshold rather than sampling it, at
// several values of the knob and with the entry starting at the rung below the
// top: the signal one short of the threshold leaves the ceiling where it is, and
// the one that meets it lifts it. Off-by-one here is the whole feature.
func TestUserSignalsAtBoundary(t *testing.T) {
	for _, signalsAt := range []int{2, 3, 5} {
		t.Run(fmt.Sprintf("user_signals_at=%d", signalsAt), func(t *testing.T) {
			s := openStore(t, t.TempDir())
			s.SetPolicy(Policy{PromoteAt: 2, UserSignalsAt: signalsAt})

			e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})
			if _, _, err := s.Submit("keep the build green", Provenance{Source: SourceAgent}); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if got := s.Render(Context{})[0]; got.Tier != agentEarnedTier {
				t.Fatalf("tier = %d, want the entry parked at the agent ceiling", got.Tier)
			}

			for i := 1; i < signalsAt; i++ {
				if err := s.Reinforce(e.Id, SourceUser); err != nil {
					t.Fatalf("Reinforce: %v", err)
				}
				if got := s.Render(Context{})[0]; got.Tier != agentEarnedTier {
					t.Fatalf("%d of %d user signals reached tier %d, want %d", i, signalsAt, got.Tier, agentEarnedTier)
				}
			}
			if err := s.Reinforce(e.Id, SourceUser); err != nil {
				t.Fatalf("Reinforce: %v", err)
			}
			if got := s.Render(Context{})[0]; got.Tier != maxEarnedTier {
				t.Fatalf("%d user signals reached tier %d, want %d", signalsAt, got.Tier, maxEarnedTier)
			}
		})
	}
}

// TestUserSignalsAtFloor is clampUserSignalsAt: #37 reserves the top tier for
// the user asking REPEATEDLY, so a threshold of one — which would grant it on a
// single signal — is not something the knob may say. Anything below two,
// including the zero of a config field nobody set, falls back to the default.
func TestUserSignalsAtFloor(t *testing.T) {
	for _, n := range []int{0, 1, -3} {
		s := openStore(t, t.TempDir())
		s.SetPolicy(Policy{PromoteAt: 2, UserSignalsAt: n})
		if got := s.Policy().UserSignalsAt; got != defaultUserSignalsAt {
			t.Fatalf("user-signals-at %d ran as %d, want the default of %d", n, got, defaultUserSignalsAt)
		}

		e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})
		if _, _, err := s.Submit("keep the build green", Provenance{Source: SourceAgent}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if err := s.Reinforce(e.Id, SourceUser); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
		if got := s.Render(Context{})[0]; got.Tier != agentEarnedTier {
			t.Fatalf("user-signals-at %d: one signal reached tier %d, want the default of %d to apply",
				n, got.Tier, defaultUserSignalsAt)
		}
	}
}

// TestAUserSignalCountsTowardTheFirstTierToo is the decision that a user signal
// IS a reinforcement, not a thing of its own: it moves the ordinary recurrence
// ladder as well as lifting the ceiling. An entry the user alone ever mentions
// therefore climbs both rungs on their repetition, which is what makes "the user
// alone suffices" true rather than a special case bolted on at the top.
func TestAUserSignalCountsTowardTheFirstTierToo(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "keep the build green")

	// The whole ladder, said by nobody but the user. Reaching the top costs
	// promote-at occurrences for the middle rung and one more after it, since
	// reinforceLocked raises at most one step per reinforcement.
	want := []int{1, 1, 2, 3}
	for i, tier := range want {
		if i > 0 {
			if err := s.Reinforce(e.Id, SourceUser); err != nil {
				t.Fatalf("Reinforce: %v", err)
			}
		}
		if got := s.Render(Context{})[0]; got.Tier != tier {
			t.Fatalf("occurrence %d: tier %d, want %d (promote_at=%d, user_signals_at=%d)",
				i+1, got.Tier, tier, defaultPromoteAt, defaultUserSignalsAt)
		}
	}
}

// TestAFoldedUserSubmissionIsAUserSignal is the rule the R2 review left for this
// issue: the user/agent distinction lives in the event's SOURCE, never in its
// NAME. A user submission that repeats something already stored emits `folded`,
// not `user-signal`, so a count keyed on the name would miss every user signal
// that duplicated an entry — which, repetition being the whole point of Score,
// is the case that matters most.
func TestAFoldedUserSubmissionIsAUserSignal(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})

	for range defaultUserSignalsAt {
		folded, wasFold, err := s.Submit("Keep the build green.", Provenance{Source: SourceUser})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if !wasFold || folded.Id != e.Id {
			t.Fatalf("Submit folded=%v into %q, want a fold into %q", wasFold, folded.Id, e.Id)
		}
	}

	got := s.Render(Context{})[0]
	if got.UserSignals != defaultUserSignalsAt {
		t.Fatalf("user signals = %d, want %d: a fold stamped %q is one",
			got.UserSignals, defaultUserSignalsAt, SourceUser)
	}
	// Meeting the threshold lifts the CEILING; it does not skip a rung, so the
	// entry is standing at the one it has climbed to and the next repeat is what
	// takes it up.
	if got.Tier != agentEarnedTier {
		t.Fatalf("tier = %d on meeting the threshold, want %d — the signal lifts the ceiling, not the entry",
			got.Tier, agentEarnedTier)
	}
	// The name really is `folded` — this test is worth nothing if the store
	// quietly started emitting `user-signal` for a submission.
	var folds int
	for _, ev := range events(t, dir) {
		switch {
		case ev.Event == EventUserSignal:
			t.Fatalf("a user SUBMISSION emitted %q; the distinction belongs in Source", EventUserSignal)
		case ev.Event == EventFolded && ev.Source == SourceUser:
			folds++
		}
	}
	if folds != defaultUserSignalsAt {
		t.Fatalf("%q events sourced %q = %d, want %d", EventFolded, SourceUser, folds, defaultUserSignalsAt)
	}
	// And the ceiling really did lift: the count is not merely recorded.
	if _, _, err := s.Submit("keep the build green!", Provenance{Source: SourceUser}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got := s.Render(Context{})[0]; got.Tier != maxEarnedTier {
		t.Fatalf("tier = %d after the next repeat, want %d", got.Tier, maxEarnedTier)
	}
}

// TestADuplicateLineInScoreMDIsAUserSignal is #38 §4's first source, and after
// R4's reword ruling it is the whole of what score.md contributes: the file
// counts when the operator SAYS A THING AGAIN in it — a second line carrying a
// wording an entry already holds — and not when they correct a line they had
// already written. See TestRewordEarnsNothing for the other half of that rule.
//
// R4's job was to make the file's door and the wire's door arrive at one piece
// of bookkeeping rather than two that happen to agree, so the entry here climbs
// on a mixture of the two.
func TestADuplicateLineInScoreMDIsAUserSignal(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})

	// The operator types the same observation again, on its own line. One pass is
	// one observation of the file, so this is one repeat however it is spelled.
	writeMD(t, dir, "- ["+e.Id+"] keep the build green\n- Keep the build green!\n")
	if d := reconcile(t, s); d.Folded != 1 {
		t.Fatalf("reconcile = %+v, want the duplicate line folded", d)
	}
	if got := s.Render(Context{})[0]; got.UserSignals != 1 {
		t.Fatalf("user signals = %d after a folded line, want it counted", got.UserSignals)
	}
	// And once more from the wire, which is the other door.
	for range 2 {
		if err := s.Reinforce(e.Id, SourceUser); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}

	got := s.Render(Context{})[0]
	if got.UserSignals != 3 {
		t.Fatalf("user signals = %d, want the folded line and the wire signals in one count", got.UserSignals)
	}
	if got.Tier != maxEarnedTier {
		t.Fatalf("tier = %d, want %d", got.Tier, maxEarnedTier)
	}

	// The count survives the restart, which is the half that could quietly differ:
	// the computed path moves it in reinforceLocked and replay moves it from the
	// records' own sources, and the two must agree.
	s.Close()
	again := openStore(t, dir).Render(Context{})[0]
	if again.UserSignals != 3 || again.Tier != maxEarnedTier {
		t.Fatalf("after replay = %+v, want tier %d with 3 user signals", again, maxEarnedTier)
	}
}

// TestLiftingTheCeilingDemotesNothing is #37's "nothing is demoted", checked at
// the moment it is easiest to break: R4 raised maxEarnedTier, and every entry
// standing at a rung it reached under the old one must still be standing there.
//
// The entries are planted rather than earned, because the state under test is a
// store whose log was written by a build with a lower ceiling — which this build
// can no longer produce through its own API.
func TestLiftingTheCeilingDemotesNothing(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	noted := submitAs(t, s, "run gofmt", Provenance{Source: SourceAgent})
	earned := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})
	for range defaultPromoteAt - 1 {
		if _, _, err := s.Submit("keep the build green", Provenance{Source: SourceAgent}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	s.Close()

	want := map[string]int{noted.Id: 1, earned.Id: agentEarnedTier}
	// Every threshold, including ones no entry in the log ever met: a tier is
	// replayed from its raised event, so retuning must move nothing either.
	for _, p := range []Policy{{}, {PromoteAt: 2, UserSignalsAt: 2}, {PromoteAt: 50, UserSignalsAt: 50}} {
		again, err := Open(dir, p)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		for _, e := range again.Render(Context{}) {
			if e.Tier != want[e.Id] {
				t.Fatalf("policy %+v: entry %s replayed at tier %d, want the %d it earned",
					p, e.Id, e.Tier, want[e.Id])
			}
		}
		again.Close()
	}
}

// TestTiersReplayIdenticallyAndIgnoreTheThreshold is invariant I1 at the point
// it is easiest to lose: a tier is replayed from the raised event, not
// recomputed from the counts, so the same log yields the same tiers whatever
// score.promote-at happens to say on the machine reading it.
func TestTiersReplayIdenticallyAndIgnoreTheThreshold(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	climbed := submit(t, s, "keep the build green")
	for range 2 {
		if _, _, err := s.Submit("Keep the build green.", Provenance{Source: "agent"}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	flat := submit(t, s, "run gofmt")
	s.Close()

	// The same log, read twice, and the second time by a store configured with a
	// threshold no entry in it ever met.
	for _, promoteAt := range []int{defaultPromoteAt, 50} {
		again, err := Open(dir, Policy{})
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		again.SetPolicy(Policy{PromoteAt: promoteAt})
		byID := map[string]Entry{}
		for _, e := range again.Render(Context{}) {
			byID[e.Id] = e
		}
		again.Close()

		if got := byID[climbed.Id]; got.Tier != 2 || got.Reinforcements != 2 {
			t.Fatalf("promote_at=%d: earned entry replayed as %+v, want tier 2 with 2 reinforcements", promoteAt, got)
		}
		if got := byID[flat.Id]; got.Tier != 1 {
			t.Fatalf("promote_at=%d: unearned entry replayed at tier %d, want 1", promoteAt, got.Tier)
		}
	}
}

// TestBootPassObeysTheConfiguredThreshold is the one moment the threshold has to
// be right before anything else happens: Open runs a full reconcile pass, and
// that pass PROMOTES. Editing score.md while the daemon is down is the expected
// workflow, so a fold or a reword found at boot is the common case, not the
// corner — and the `raised` event it writes is durable, replayed at every boot,
// and uncorrectable, because #37 demotes nothing.
//
// Both halves are asserted. Under the default the boot pass promotes, which is
// what makes the other half a real check rather than a tautology; under a
// threshold the operator actually chose, the same files leave the entry at tier
// 1 and put no `raised` record in the log at all.
func TestBootPassObeysTheConfiguredThreshold(t *testing.T) {
	// Two occurrences already in the log, and a duplicate line waiting in
	// score.md: the boot pass folds it, and the third occurrence meets the
	// default threshold exactly.
	seed := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		s := openStore(t, dir)
		e := submit(t, s, "keep the build green")
		if err := s.Reinforce(e.Id, "agent"); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
		if got := s.Render(Context{})[0]; got.Tier != 1 || got.Reinforcements != 1 {
			t.Fatalf("seed = %+v, want one repeat short of the default threshold", got)
		}
		writeMD(t, dir, "- ["+e.Id+"] keep the build green\n- Keep the build green.\n")
		s.Close()
		return dir
	}

	t.Run("the default promotes", func(t *testing.T) {
		dir := seed(t)
		s, err := Open(dir, Policy{})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(s.Close)
		if got := s.Render(Context{})[0]; got.Tier != 2 || got.Reinforcements != 2 {
			t.Fatalf("entry = %+v, want the boot pass to have earned tier 2", got)
		}
	})

	t.Run("a chosen threshold is in force at boot", func(t *testing.T) {
		dir := seed(t)
		s, err := Open(dir, Policy{PromoteAt: 10})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(s.Close)
		if got := s.Policy().PromoteAt; got != 10 {
			t.Fatalf("Policy().PromoteAt = %d, want the store built under the chosen number", got)
		}
		if got := s.Render(Context{})[0]; got.Tier != 1 || got.Reinforcements != 2 {
			t.Fatalf("entry = %+v, want the repeat counted and no tier granted", got)
		}
		// Durable and replayed forever, so the absence has to hold in the log and
		// not merely in memory.
		for _, ev := range events(t, dir) {
			if ev.Event == EventRaised {
				t.Fatalf("a raise was recorded under promote_at=10: %+v", ev)
			}
		}
	})
}

// TestStrippingAnIdStartsAnEntryOver is the one undo the store has, and it is
// here because score.md's seed header now promises it to operators — a promise
// in a file a person reads should be a test, not a belief.
//
// Nothing demotes an entry (#37), so a tier reached in error would look
// permanent. Deleting a line's id is what takes it back: the pass finds a live
// entry no line carries and retires it, and finds an id-less bullet and admits
// it fresh at tier 1. It matters more since the top tier became reachable, since
// a brief that coincidentally matched is exactly the promotion worth undoing.
func TestStrippingAnIdStartsAnEntryOver(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})
	for range 3 {
		if err := s.Reinforce(e.Id, SourceUser); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	if got := s.Render(Context{})[0]; got.Tier != maxEarnedTier {
		t.Fatalf("entry = %+v, want the top tier before the undo", got)
	}

	// One save: the id goes, the text stays.
	writeMD(t, dir, "- keep the build green\n")
	d := reconcile(t, s)
	if d.Retired != 1 || d.Admitted != 1 {
		t.Fatalf("pass = %+v, want the old entry retired and a fresh one admitted", d)
	}

	entries := s.Render(Context{})
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want exactly one", entries)
	}
	got := entries[0]
	switch {
	case got.Id == e.Id:
		t.Fatalf("the entry kept its id %q; the undo rests on it getting a new one", got.Id)
	case got.Text != "keep the build green":
		t.Fatalf("text = %q, want the operator's own wording carried over", got.Text)
	case got.Tier != 1 || got.Reinforcements != 0 || got.UserSignals != 0:
		t.Fatalf("entry = %+v, want everything it had earned reset", got)
	}
	// The id is written back, so the file is left in the shape the header teaches
	// rather than folding again on the next pass.
	if !strings.Contains(readFile(t, dir, scoreMD), "["+got.Id+"]") {
		t.Fatalf("score.md = %q, want the new id written back", readFile(t, dir, scoreMD))
	}

	// Nothing is destroyed (I7): the old entry's history is still in the log, and
	// it survives a restart as history rather than coming back as an entry.
	if !hasEvent(events(t, dir), EventRetired, e.Id) {
		t.Fatal("the retirement is not in the log")
	}
	if !hasEvent(events(t, dir), EventUserSignal, e.Id) {
		t.Fatal("the old entry's signals are gone from the log")
	}
	s.Close()
	again := openStore(t, dir).Render(Context{})
	if len(again) != 1 || again[0].Id != got.Id || again[0].Tier != 1 {
		t.Fatalf("after replay = %+v, want only the fresh entry at tier 1", again)
	}
}

// TestUserSignalsAtIsInForceAtOpen is the other half of how the knob is wired.
// SetPolicy covers the reload; this covers the boot, where there is no running
// policy to fall back on — Open runs a full reconcile pass before anything else
// happens, and that pass PROMOTES. A store built on the package default while
// the operator's file said otherwise would grant a rung under a threshold nobody
// chose, and #37 demotes nothing, so the record would be uncorrectable.
func TestUserSignalsAtIsInForceAtOpen(t *testing.T) {
	// Two occurrences and two user signals already in the log — enough for the
	// default threshold of two, and one short of the four this store is opened
	// with.
	dir := t.TempDir()
	seed := openStore(t, dir)
	e := submitAs(t, seed, "keep the build green", Provenance{Source: SourceAgent})
	for range 3 {
		if err := seed.Reinforce(e.Id, SourceUser); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	if got := seed.Render(Context{})[0]; got.Tier != maxEarnedTier {
		t.Fatalf("seed = %+v, want the default threshold to have granted the top rung", got)
	}
	seed.Close()

	s, err := Open(dir, Policy{UserSignalsAt: 4})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	if got := s.Policy().UserSignalsAt; got != 4 {
		t.Fatalf("Policy().UserSignalsAt = %d, want the store built under the chosen number", got)
	}
	// The tier already earned is replayed and stands — nothing is demoted — but
	// the store now measures the NEXT rung against the number this Open chose.
	if got := s.Render(Context{})[0]; got.Tier != maxEarnedTier || got.UserSignals != 3 {
		t.Fatalf("entry = %+v, want the earned tier replayed with its 3 signals", got)
	}
}

// TestPromoteAtFloor keeps a tier something an entry EARNS. A threshold of one
// would raise an entry on the submission that created it, which is granting
// importance rather than counting it, so anything below two — including the zero
// of a config field nobody set — falls back to the default.
func TestPromoteAtFloor(t *testing.T) {
	for _, n := range []int{0, 1, -3} {
		s := openStore(t, t.TempDir())
		s.SetPolicy(Policy{PromoteAt: n})
		if e := submit(t, s, "keep the build green"); e.Tier != 1 {
			t.Fatalf("SetPolicy(promote-at %d): a first submission arrived at tier %d, want 1", n, e.Tier)
		}
		if _, _, err := s.Submit("keep the build green", Provenance{Source: "agent"}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if got := s.Render(Context{})[0]; got.Tier != 1 {
			t.Fatalf("SetPolicy(promote-at %d): tier %d after two occurrences, want the default of %d to apply", n, got.Tier, defaultPromoteAt)
		}
	}
	// And nil is the disabled store: setting a threshold on it is a no-op, not a
	// panic, like every other method here.
	var disabled *Store
	disabled.SetPolicy(Policy{PromoteAt: 4})
}

// TestReplayIsBoundedByTheConstantAndNotByTheCeiling pins the deliberate split
// between the two bounds, because it is the one place they are not the same
// number and a later reader would otherwise be entitled to "fix" it.
//
// The COMPUTED path is bounded by Policy.ceiling, which asks whether the user
// has signalled. REPLAY is bounded by maxEarnedTier, the ladder's end, and not
// by the ceiling: a tier is replayed from its raised event rather than
// recomputed, so that the same log yields the same tiers on every machine
// (invariant I1) — and running replay through a threshold this machine
// configures would make one log mean two things. So a forged tier-3 raise is
// honoured while a forged tier-4 is not.
//
// That is not a hole in I6. Nothing WRITES a raised event above agentEarnedTier
// except reinforceLocked with the user's own signals behind it; a log saying
// otherwise was hand-edited, and #38 declines to be a boundary against someone
// with filesystem access. The bound past the ladder's end is still real, because
// tierWording has no words above it: a raise past it is IGNORED rather than
// clamped down to it, so the entry keeps exactly the tier it earned — a record
// that cannot be true is not evidence of a smaller true one, and this build
// under-claims rather than lies, the same rule panel.ParseState follows for a
// state string it does not know.
func TestReplayIsBoundedByTheConstantAndNotByTheCeiling(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	earned := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})
	for range 2 {
		if _, _, err := s.Submit("keep the build green", Provenance{Source: SourceAgent}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	fresh := submitAs(t, s, "run gofmt", Provenance{Source: SourceAgent})

	forge := func(id string, tier int) {
		t.Helper()
		forged := fmt.Sprintf(`{"schema":1,"event":"raised","id":%q,"at":"2026-08-31T00:00:00Z","tier":%d}`, id, tier)
		if err := appendDurable(s.eventsPath, []byte(forged+"\n")); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// One inside the ladder and one past its end, on entries no user ever spoke
	// for, so what separates the two outcomes is the bound and nothing else.
	forge(earned.Id, maxEarnedTier)
	forge(fresh.Id, maxEarnedTier+1)
	s.Close()

	again := openStore(t, dir)
	byID := map[string]Entry{}
	for _, e := range again.Render(Context{}) {
		byID[e.Id] = e
	}
	if got := byID[earned.Id]; got.Tier != maxEarnedTier || got.UserSignals != 0 {
		t.Fatalf("entry = %+v, want the log's tier %d honoured on an entry with no user signals",
			got, maxEarnedTier)
	}
	if got := byID[fresh.Id].Tier; got != 1 {
		t.Fatalf("tier = %d after a line claiming %d, want the 1 it earned", got, maxEarnedTier+1)
	}
	// Counted rather than passed over in silence: a log asking for a tier this
	// build will not grant is a fact about the log.
	if got := again.Health().RejectedRaises; got != 1 {
		t.Fatalf("rejected raises = %d, want 1", got)
	}
}

// TestReconcileFoldEarnsATier is the second input path: a tier is earned by
// recurrence whether the repeats arrive from an agent's submission or from the
// operator's own file — one save at a time, because one save is one observation.
func TestReconcileFoldEarnsATier(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "keep the build green")

	for i, dup := range []string{"keep the build green", "Keep the build green!"} {
		writeMD(t, dir, "- ["+e.Id+"] keep the build green\n- "+dup+"\n")
		d := reconcile(t, s)
		if d.Folded != 1 {
			t.Fatalf("save %d = %+v, want one fold", i+1, d)
		}
	}

	if got := s.Render(Context{})[0]; got.Tier != 2 || got.Reinforcements != 2 {
		t.Fatalf("entry = %+v, want tier 2 with 2 reinforcements", got)
	}
}

// TestOnePassCountsOneRepeat is the semantics a 500-line paste forced. #37's
// model is that recurrence means the observation CAME BACK after being recorded;
// a clipboard is one action, and a pass is one observation of the file, however
// many lines carry the wording. Every duplicate line still leaves the file — the
// count is what is capped, not the cleanup.
func TestOnePassCountsOneRepeat(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "keep the build green")

	var md strings.Builder
	md.WriteString("- [" + e.Id + "] keep the build green\n")
	for range 500 {
		md.WriteString("- Keep the build green.\n")
	}
	writeMD(t, dir, md.String())

	v, err := s.View(Context{})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	switch {
	case v.Delta.Folded != 1:
		t.Fatalf("pass = %+v, want one repeat counted for one paste", v.Delta)
	case v.Delta.Raised != 0:
		t.Fatalf("pass = %+v, want no tier earned by a paste", v.Delta)
	}
	if got := v.Entries[0]; got.Tier != 1 || got.Reinforcements != 1 {
		t.Fatalf("entry = %+v, want tier 1 with one reinforcement", got)
	}
	// All 500 lines are gone, and the record says how many.
	if n := strings.Count(readFile(t, dir, scoreMD), "keep the build green"); n != 1 {
		t.Fatalf("score.md carries the wording %d times, want 1", n)
	}
	if len(v.Folds) != 1 || v.Folds[0].Duplicates != 500 || !v.Folds[0].Removed || v.Folds[0].Id != e.Id {
		t.Fatalf("folds = %+v, want one record naming 500 removals", v.Folds)
	}
}

// TestRewordEarnsNothing is R4's narrowing of a behaviour R1 and R2 shipped, and
// the reason for it is the ladder R4 built on top of them.
//
// Those releases counted every score.md text change as a user reinforcement.
// That was harmless while recurrence stopped at agentEarnedTier — the worst it
// could buy was a rung an agent could reach anyway. R4 makes the user's signal
// the currency of the TOP rung, and at that price an operator fixing a typo
// three times in one line would walk it to tier 3 without ever repeating
// themselves or involving an agent. #37's model is that an observation came back
// after being recorded; one statement corrected three times is one statement.
//
// Everything else a reword does still happens, which is what makes this a
// narrowing rather than a removal: the operator's text wins, the prior wordings
// stay aliases and still fold (invariant I4), and the edit is in the log (I7).
func TestRewordEarnsNothing(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "say please")

	// Three corrections to one line — SRE's live reproduction, which took the
	// entry to tier 3 before this ruling.
	for _, text := range []string{"ask politely", "ask politely first", "ask politely before acting"} {
		writeMD(t, dir, "- ["+e.Id+"] "+text+"\n")
		if d := reconcile(t, s); d.Superseded != 1 || d.Raised != 0 {
			t.Fatalf("reword %q = %+v, want one supersede and no raise", text, d)
		}
	}

	got := s.Render(Context{})[0]
	if got.Tier != 1 || got.Reinforcements != 0 || got.UserSignals != 0 {
		t.Fatalf("entry = %+v, want three corrections to have earned nothing", got)
	}
	if got.Text != "ask politely before acting" {
		t.Fatalf("text = %q, want the operator's latest wording — their text wins", got.Text)
	}
	// Every prior wording is kept, and the oldest still folds (I4).
	if len(got.Aliases) != 3 {
		t.Fatalf("aliases = %v, want all three prior wordings", got.Aliases)
	}
	if _, folded, err := s.Submit("Say please!", Provenance{Source: SourceAgent}); err != nil || !folded {
		t.Fatalf("the oldest wording did not fold: folded=%v err=%v", folded, err)
	}
	// And the log still records what the operator did, whatever it bought them.
	var edits int
	for _, ev := range events(t, dir) {
		if ev.Event == EventEdited && ev.Source == SourceUser {
			edits++
		}
	}
	if edits != 3 {
		t.Fatalf("%q events = %d, want all three corrections recorded (I7)", EventEdited, edits)
	}

	// The replay agrees, which is the half that could quietly differ: the pass
	// counts nothing and replay must count nothing from the records it left.
	//
	// One reinforcement is expected and is not the corrections': the alias fold
	// just above is an agent repeating the oldest wording, which is a genuine
	// second saying of the thing. It is the user's count that must still be zero.
	s.Close()
	again := openStore(t, dir).Render(Context{})[0]
	if again.Tier != 1 || again.UserSignals != 0 || again.Reinforcements != 1 {
		t.Fatalf("after replay = %+v, want tier 1, no user signal, and only the alias fold counted", again)
	}
}
