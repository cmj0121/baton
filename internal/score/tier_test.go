package score

import (
	"fmt"
	"strings"
	"testing"
)

// This file covers R2's other half (#41): a tier earned by recurrence, the
// threshold's exact boundary, and the guard that keeps tier 3 out of reach until
// R4 brings the user signal invariant I6 rests on.

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

// TestTierThreeIsUnreachable is invariant I6 and #38's verification check 5. The
// top tier is what #37 reserves for the user asking repeatedly; no run of agent
// submissions may reach it, and R4 (#43) is what finally can.
func TestTierThreeIsUnreachable(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)

	e := submit(t, s, "keep the build green")
	for range 100 {
		if _, _, err := s.Submit("keep the build green", Provenance{Source: "agent", SourcePanel: "p1"}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	// Reinforce is the other counting path, and the user's own signal is on it —
	// even that stops at the earned ceiling until R4 defines the last step.
	for range 10 {
		if err := s.Reinforce(e.Id, "user"); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}

	got := s.Render(Context{})[0]
	if got.Tier != 2 {
		t.Fatalf("tier = %d after 111 reinforcements, want 2", got.Tier)
	}
	if got.Reinforcements != 110 {
		t.Fatalf("reinforcements = %d, want every repeat counted", got.Reinforcements)
	}
	for _, ev := range events(t, dir) {
		if ev.Event == EventRaised && ev.Tier > maxEarnedTier {
			t.Fatalf("the log raised %s to tier %d; recurrence may not pass %d", ev.Id, ev.Tier, maxEarnedTier)
		}
	}

	// And a replay cannot climb past it either.
	s.Close()
	if got := openStore(t, dir).Render(Context{})[0]; got.Tier != 2 {
		t.Fatalf("tier after replay = %d, want 2", got.Tier)
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

// TestReplayCannotBeTalkedPastTheCeiling keeps maxEarnedTier the single fact the
// ceiling is made of. The computed paths obey it; so must replay, or a log line
// claiming tier 3 — a hand edit, a torn record decoding oddly, a log written by
// a future build — would render an entry as "important" before R4 defines what
// earns that. The log is the operator's own file and #38 declines to be a
// boundary against filesystem access, so this is not a guard against them.
// A raise past the ceiling is IGNORED rather than clamped down to it, so the
// entry keeps exactly the tier it earned: a record that cannot be true is not
// evidence of a smaller true one, and this build under-claims rather than lies —
// the same rule panel.ParseState follows for a state string it does not know.
func TestReplayCannotBeTalkedPastTheCeiling(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	earned := submit(t, s, "keep the build green")
	for range 2 {
		if _, _, err := s.Submit("keep the build green", Provenance{Source: "agent"}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	fresh := submit(t, s, "run gofmt")

	for _, id := range []string{earned.Id, fresh.Id} {
		forged := `{"schema":1,"event":"raised","id":"` + id + `","at":"2026-08-31T00:00:00Z","tier":3}`
		if err := appendDurable(s.eventsPath, []byte(forged+"\n")); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	s.Close()

	again := openStore(t, dir)
	byID := map[string]Entry{}
	for _, e := range again.Render(Context{}) {
		byID[e.Id] = e
	}
	if got := byID[earned.Id].Tier; got != maxEarnedTier {
		t.Fatalf("tier = %d after a log line claiming 3, want the %d it earned", got, maxEarnedTier)
	}
	// Both rejections are counted rather than passed over in silence: a log
	// asking for a tier this build will not grant is a fact about the log.
	if got := again.Health().RejectedRaises; got != 2 {
		t.Fatalf("rejected raises = %d, want 2", got)
	}
	if got := byID[fresh.Id].Tier; got != 1 {
		t.Fatalf("tier = %d for an entry raised only by that line, want 1", got)
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

// TestRewordEarnsATier keeps the one path that already counted a reinforcement
// before R2 on the same ladder as the new ones: an operator rewording a line is
// a reinforcement in #38's glossary, so it climbs through the same helper rather
// than by rules of its own.
func TestRewordEarnsATier(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "say please")

	for _, text := range []string{"ask politely", "ask politely before acting"} {
		writeMD(t, dir, "- ["+e.Id+"] "+text+"\n")
		reconcile(t, s)
	}

	got := s.Render(Context{})[0]
	if got.Tier != 2 || got.Reinforcements != 2 {
		t.Fatalf("entry = %+v, want tier 2 with 2 reinforcements", got)
	}
	// Both prior wordings are kept, and both still fold (I4).
	if len(got.Aliases) != 2 {
		t.Fatalf("aliases = %v, want both prior wordings", got.Aliases)
	}
	if _, folded, err := s.Submit("Say please!", Provenance{Source: "agent"}); err != nil || !folded {
		t.Fatalf("the oldest wording did not fold: folded=%v err=%v", folded, err)
	}
}
