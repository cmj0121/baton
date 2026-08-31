package score

import (
	"strings"
	"testing"
	"time"
)

// This file covers Store.Signal — #38 §4's second source of the user signal, and
// R4's other half: a brief the user dispatched, matched against the store with
// the folding normaliser, counting where it repeats something and doing nothing
// where it does not.

// TestSignalCountsARepeatWithoutAdmittingOne is the whole difference between
// Signal and Submit. A brief is evidence that something the fleet already
// remembers still matters; it is not an observation being offered. So a brief
// that matches folds, and one that matches nothing leaves the store exactly as
// it was — no entry, no line, and no event.
func TestSignalCountsARepeatWithoutAdmittingOne(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})
	before := len(events(t, dir))

	t.Run("a miss leaves no trace", func(t *testing.T) {
		got, hit, err := s.Signal("please refactor the auth package", Provenance{Source: SourceUser})
		if err != nil {
			t.Fatalf("Signal: %v", err)
		}
		if hit || got.Id != "" {
			t.Fatalf("Signal hit=%v entry=%+v on a brief matching nothing", hit, got)
		}
		if s.Len() != 1 {
			t.Fatalf("the store holds %d entries; a brief must never admit one", s.Len())
		}
		if now := len(events(t, dir)); now != before {
			t.Fatalf("%d events after a miss, want the %d there were", now, before)
		}
		if !strings.Contains(readFile(t, dir, scoreMD), "keep the build green") ||
			strings.Contains(readFile(t, dir, scoreMD), "refactor") {
			t.Fatalf("score.md changed on a miss:\n%s", readFile(t, dir, scoreMD))
		}
	})

	t.Run("a hit counts", func(t *testing.T) {
		// Not byte-identical: the match is the folding normaliser's, the same one
		// that decides what a repeat is everywhere else in the store.
		got, hit, err := s.Signal("Keep the build green.", Provenance{Source: SourceUser})
		if err != nil {
			t.Fatalf("Signal: %v", err)
		}
		if !hit || got.Id != e.Id {
			t.Fatalf("Signal hit=%v into %q, want a fold into %q", hit, got.Id, e.Id)
		}
		if got.Reinforcements != 1 || got.UserSignals != 1 {
			t.Fatalf("entry = %+v, want one reinforcement and one user signal", got)
		}
		if s.Len() != 1 {
			t.Fatalf("the store holds %d entries, want the one that was already there", s.Len())
		}
	})
}

// TestSignalMatchesASupersededWording is invariant I4 reaching the third door. A
// user who reworded an entry and then dispatches a brief in the OLD phrasing is
// repeating that entry, and folding is what says so — the alias index is the
// store's answer to what a repeat is, and Signal must ask it rather than keeping
// a narrower one of its own.
func TestSignalMatchesASupersededWording(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})
	writeMD(t, dir, "- ["+e.Id+"] the build must stay green\n")
	reconcile(t, s)

	got, hit, err := s.Signal("keep the build green", Provenance{Source: SourceUser})
	if err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if !hit || got.Id != e.Id {
		t.Fatalf("Signal hit=%v into %q, want the old wording to fold into %q", hit, got.Id, e.Id)
	}
	if got.Text != "the build must stay green" {
		t.Fatalf("text = %q, want the surviving wording", got.Text)
	}
}

// TestSignalIsAUserSignalWhenTheConnectionSaid keeps the store out of the
// decision #38 §4 gives the server. Signal stamps what it is handed and nothing
// else: an agent-sourced call counts toward recurrence and moves no user signal,
// so the top tier cannot be reached by a caller that simply calls this a lot.
func TestSignalIsAUserSignalWhenTheConnectionSaid(t *testing.T) {
	for _, tc := range []struct {
		source      string
		wantSignals int
	}{
		{SourceUser, 3},
		{SourceAgent, 0},
	} {
		t.Run(tc.source, func(t *testing.T) {
			s := openStore(t, t.TempDir())
			submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})
			for range 3 {
				if _, hit, err := s.Signal("keep the build green", Provenance{Source: tc.source}); err != nil || !hit {
					t.Fatalf("Signal hit=%v: %v", hit, err)
				}
			}

			got := s.Render(Context{})[0]
			if got.Reinforcements != 3 {
				t.Fatalf("reinforcements = %d, want every brief counted whatever its source", got.Reinforcements)
			}
			if got.UserSignals != tc.wantSignals {
				t.Fatalf("user signals = %d for source %q, want %d", got.UserSignals, tc.source, tc.wantSignals)
			}
			// Four occurrences either way, so what separates the two tiers is the
			// ceiling and not the ladder.
			want := agentEarnedTier
			if tc.wantSignals >= defaultUserSignalsAt {
				want = maxEarnedTier
			}
			if got.Tier != want {
				t.Fatalf("tier = %d for source %q, want %d", got.Tier, tc.source, want)
			}
		})
	}
}

// TestSignalRecordsWhichDoorTheRepeatCameThrough is #38 §4's accepted cost made
// bearable. A brief may coincidentally repeat an entry it has nothing to do
// with, and the operator's only way to notice is the record the fold leaves —
// which, without FromSignal, is indistinguishable from a submission they did
// make. It names the surviving entry beside the brief that matched it, so the
// coincidence is legible and the fix is the one every reinforcement has: edit
// the line.
func TestSignalRecordsWhichDoorTheRepeatCameThrough(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})

	before := time.Now().UTC()
	f, hit, err := s.Signal("Keep the build green!", Provenance{Source: SourceUser})
	if err != nil || !hit {
		t.Fatalf("Signal hit=%v: %v", hit, err)
	}
	switch {
	case !f.FromSignal:
		t.Fatalf("fold = %+v, want FromSignal: a submission fold reads identically otherwise", f)
	case f.FromFile:
		t.Fatalf("fold = %+v, want FromFile false: a brief is not a line in score.md", f)
	case f.Id != e.Id || f.Text != "keep the build green":
		t.Fatalf("fold = %+v, want the surviving entry named", f)
	case f.Repeat != "Keep the build green!":
		t.Fatalf("fold = %+v, want the brief's own wording, so a coincidence is legible", f)
	case f.Prov.Source != SourceUser || !f.Counted || f.Duplicates != 1:
		t.Fatalf("fold = %+v, want one counted user repeat", f)
	case f.UserSignals != 1 || f.Reinforcements != 1:
		t.Fatalf("fold = %+v, want where the entry stands after it", f)
	case f.At.Before(before) || f.At.After(time.Now().UTC()):
		t.Fatalf("fold At = %v, want the moment the fold happened", f.At)
	}

	// NOT buffered, which is the point: a signal's record is returned so the
	// caller can log it on the dispatch that caused it. Buffering it too would
	// log the same fold twice, once now and once whenever the next read landed.
	v, err := s.View(Context{})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(v.Folds) != 0 {
		t.Fatalf("folds = %+v, want a signal's record returned rather than buffered", v.Folds)
	}

	// A SUBMISSION fold still buffers, so the two doors really do differ here and
	// the check above is not passing because folds stopped being recorded at all.
	if _, folded, serr := s.Submit("keep the build green.", Provenance{Source: SourceUser}); serr != nil || !folded {
		t.Fatalf("Submit folded=%v: %v", folded, serr)
	}
	again, err := s.View(Context{})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(again.Folds) != 1 || again.Folds[0].FromSignal {
		t.Fatalf("folds = %+v, want the submission's record buffered for this read", again.Folds)
	}
}

// TestSignalIsMarkedInTheDurableLog is the other half of visibility. A fold
// record lives until the next read drains it; the log outlives the daemon, and
// "which of my briefs promoted this" is a question asked days later. Without the
// mark, a brief's fold and a `ctl score submit` fold are byte-identical on disk.
func TestSignalIsMarkedInTheDurableLog(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})

	if _, _, err := s.Signal("keep the build green", Provenance{Source: SourceUser}); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if _, folded, err := s.Submit("Keep the build green!", Provenance{Source: SourceUser}); err != nil || !folded {
		t.Fatalf("Submit folded=%v: %v", folded, err)
	}

	var signals, plain int
	for _, ev := range events(t, dir) {
		if ev.Event != EventFolded {
			continue
		}
		if ev.Signal {
			signals++
		} else {
			plain++
		}
	}
	if signals != 1 || plain != 1 {
		t.Fatalf("folded events: %d marked as a signal and %d not, want one of each", signals, plain)
	}
}

// TestSignalReconcilesFirst is invariant I2 on the third door, and it matters
// more here than on the others: Signal runs on the dispatch path, where the
// operator is most likely to have score.md open. A line they have just deleted
// must not swallow a brief, and one they have just typed should match it.
func TestSignalReconcilesFirst(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	gone := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})

	// The operator deletes that line and types another, without the store having
	// read the file since.
	writeMD(t, dir, "- always run the tests first\n")

	got, hit, err := s.Signal("keep the build green", Provenance{Source: SourceUser})
	if err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if hit {
		t.Fatalf("Signal folded into %+v, want a retired wording to match nothing", got)
	}
	got, hit, err = s.Signal("always run the tests first", Provenance{Source: SourceUser})
	if err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if !hit || got.Text != "always run the tests first" || got.Id == gone.Id {
		t.Fatalf("Signal hit=%v entry=%+v, want the line the operator just typed", hit, got)
	}
}

// TestSignalRefusesNothingAndWeighsNothing pins the two boundary shapes a brief
// arrives in that a submission never would. maxEntryRunes exists to keep a heavy
// entry out of every future brief, and Signal admits nothing at all — so a brief
// longer than any entry is not an error, it simply matches nothing. An empty one
// is the same answer arrived at sooner.
func TestSignalRefusesNothingAndWeighsNothing(t *testing.T) {
	s := openStore(t, t.TempDir())
	e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})

	for _, text := range []string{"", "   ", strings.Repeat("z", maxEntryRunes*10)} {
		got, hit, err := s.Signal(text, Provenance{Source: SourceUser})
		if err != nil {
			t.Fatalf("Signal(%d runes): %v", len([]rune(text)), err)
		}
		if hit {
			t.Fatalf("Signal(%d runes) folded into %+v, want no match", len([]rune(text)), got)
		}
	}
	if got := s.Render(Context{})[0]; got.Reinforcements != 0 || got.Id != e.Id {
		t.Fatalf("entry = %+v, want it untouched", got)
	}

	// And the disabled store refuses plainly rather than panicking, like every
	// other mutation here.
	var disabled *Store
	if _, _, err := disabled.Signal("keep the build green", Provenance{Source: SourceUser}); err == nil {
		t.Error("disabled Signal succeeded, want refusal")
	}
}

// TestSignalSurvivesAReplay is the durability half: a brief's reinforcement is
// an ordinary fold event with the user's source on it, so it replays like every
// other one and a restart cannot lose the rung it bought.
func TestSignalSurvivesAReplay(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})
	for range 3 {
		if _, hit, err := s.Signal("keep the build green", Provenance{Source: SourceUser}); err != nil || !hit {
			t.Fatalf("Signal hit=%v: %v", hit, err)
		}
	}
	before := s.Render(Context{})[0]
	if before.Tier != maxEarnedTier {
		t.Fatalf("entry = %+v, want the top tier before the restart", before)
	}
	// The events say `folded` with the user's source — never `user-signal`, since
	// the name is not where the distinction lives.
	for _, ev := range events(t, dir) {
		if ev.Event == EventUserSignal {
			t.Fatalf("a brief emitted %q; the distinction belongs in Source", EventUserSignal)
		}
	}
	s.Close()

	after := openStore(t, dir).Render(Context{})[0]
	if after.Id != e.Id || after.Tier != before.Tier ||
		after.UserSignals != before.UserSignals || after.Reinforcements != before.Reinforcements {
		t.Fatalf("after replay = %+v, want %+v", after, before)
	}
}
