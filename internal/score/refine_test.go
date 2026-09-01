package score

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file covers R6 (#45): the conductor's three corrections — merge, reword,
// lower — and the two claims that bound them.
//
//   - NOTHING here counts. Not a reinforcement, not a user signal, not an
//     entry's position in the log. R4 ruled that a reword is one statement
//     re-spelled rather than a second statement, and a conductor is an agent
//     panel, so a refine that bought what a repetition buys would hand an agent
//     the currency invariant I6 reserves for the user.
//   - NOTHING here goes up. `lower` takes no target tier, and the record it
//     writes is refused on replay unless it names a rung strictly below the one
//     the entry is on.
//
// The gate that decides WHO may call any of this is the server's, not the
// store's; see internal/server's refine tests.

// refine fails the test when a correction was refused. The three verbs are three
// methods, so what it takes is the call's own result; t.Helper puts the failure
// on the caller's line, which is what says which correction it was.
func refine(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("refine: %v", err)
	}
}

// entryByID is the entry with this id, or a failure. Render is what the fleet
// actually sees, so the tests read through it rather than through the slice.
func entryByID(t *testing.T, s *Store, id string) Entry {
	t.Helper()
	for _, e := range s.Render(Context{}) {
		if e.Id == id {
			return e
		}
	}
	t.Fatalf("no entry %s in %+v", id, s.Render(Context{}))
	return Entry{}
}

// TestMergeJoinsWhatFoldingCouldNot is #38 §1's escape hatch: two observations
// that mean the same thing in different words, which the normaliser cannot join
// because folding is textual. After the merge there is one entry, and a repeat
// of the absorbed wording folds into it instead of starting the split again.
func TestMergeJoinsWhatFoldingCouldNot(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submitAs(t, s, "the agent asks before it deletes", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	gone := submitAs(t, s, "it wants permission before removing things", Provenance{Source: SourceAgent, SourcePanel: "p2"})

	refine(t, s.Merge(keep.Id, gone.Id))

	if got := s.Render(Context{}); len(got) != 1 || got[0].Id != keep.Id {
		t.Fatalf("after the merge the store holds %+v, want only %s", got, keep.Id)
	}
	// The survivor kept its own wording and its own rung: a merge is a correction
	// of the normaliser, not a second occurrence of anything.
	survivor := entryByID(t, s, keep.Id)
	if survivor.Text != "the agent asks before it deletes" || survivor.Tier != 1 {
		t.Fatalf("survivor = %+v, want its own wording still at tier 1", survivor)
	}
	if survivor.Reinforcements != 0 || survivor.UserSignals != 0 {
		t.Fatalf("survivor counters = %d/%d, want 0/0: a merge counts nothing",
			survivor.Reinforcements, survivor.UserSignals)
	}

	// The absorbed line is out of the operator's file, and the absorbed wording
	// now folds into the survivor — which is the whole point of the hatch.
	if md := readFile(t, dir, scoreMD); strings.Contains(md, gone.Id) {
		t.Fatalf("score.md still carries the absorbed entry's line:\n%s", md)
	}
	landed, folded, err := s.Submit("it wants permission before removing things",
		Provenance{Source: SourceAgent, SourcePanel: "p3"})
	if err != nil {
		t.Fatalf("Submit the absorbed wording: %v", err)
	}
	if !folded || landed.Id != keep.Id {
		t.Fatalf("the absorbed wording landed in %s (folded=%v), want a fold into %s", landed.Id, folded, keep.Id)
	}

	// Attributed to the conductor, and nothing destroyed (I7): the absorbed
	// entry's wording is on the merge record, where an operator reading their
	// history can still find it.
	evs := events(t, dir)
	if !hasEvent(evs, EventMerged, keep.Id) || !hasEvent(evs, EventRetired, gone.Id) {
		t.Fatalf("log = %+v, want a merged record for %s and a retired one for %s", evs, keep.Id, gone.Id)
	}
	for _, ev := range evs {
		if ev.Event == EventMerged {
			if ev.Source != sourceConductor {
				t.Errorf("merged record source = %q, want %q", ev.Source, sourceConductor)
			}
			if ev.Text != "it wants permission before removing things" {
				t.Errorf("merged record text = %q, want the absorbed wording", ev.Text)
			}
		}
		if ev.Event == EventRetired && ev.Id == gone.Id && ev.Source != sourceConductor {
			t.Errorf("retired record source = %q, want %q", ev.Source, sourceConductor)
		}
	}
}

// TestMergeRefusals covers the two a caller can make: an entry that is not
// there, and an entry merged into itself. Both leave the store exactly as it
// was, because a merge that half-happened would be one retired entry and no
// alias to show for it.
func TestMergeRefusals(t *testing.T) {
	s := openStore(t, t.TempDir())
	keep := submit(t, s, "the agent asks before it deletes")

	if err := s.Merge(keep.Id, "abc123"); err == nil || !strings.Contains(err.Error(), `no entry "abc123"`) {
		t.Fatalf("merge with an unknown other = %v, want a no-entry refusal", err)
	}
	if err := s.Merge(keep.Id, keep.Id); err == nil || !strings.Contains(err.Error(), "into itself") {
		t.Fatalf("merge into itself = %v, want a refusal", err)
	}
	if got := s.Render(Context{}); len(got) != 1 || len(got[0].Aliases) != 0 {
		t.Fatalf("store after two refused merges = %+v, want the one entry with no aliases", got)
	}
}

// TestRewordKeepsThePriorWordingForFolding is #38's verification check 6 at the
// store's own boundary: reword an entry through the conductor, submit the old
// wording from an agent, and it folds. The server test drives the same check
// through the conductor's connection.
func TestRewordKeepsThePriorWordingForFolding(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submitAs(t, s, "run the linter frist", Provenance{Source: SourceAgent, SourcePanel: "p1"})

	refine(t, s.Reword(e.Id, "run the linter first"))

	got := entryByID(t, s, e.Id)
	if got.Text != "run the linter first" {
		t.Fatalf("text = %q, want the corrected wording", got.Text)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "run the linter frist" {
		t.Fatalf("aliases = %v, want the prior wording kept (invariant I4)", got.Aliases)
	}
	// The operator's file carries the correction too — score.md is the truth for
	// text, so a reword the file did not learn would be undone by the next pass.
	if md := readFile(t, dir, scoreMD); !strings.Contains(md, formatLine(e.Id, "run the linter first")) {
		t.Fatalf("score.md did not take the reword:\n%s", md)
	}

	landed, folded, err := s.Submit("run the linter frist", Provenance{Source: SourceAgent, SourcePanel: "p2"})
	if err != nil {
		t.Fatalf("Submit the old wording: %v", err)
	}
	if !folded || landed.Id != e.Id {
		t.Fatalf("the old wording landed in %s (folded=%v), want a fold into %s", landed.Id, folded, e.Id)
	}
	if !hasEvent(events(t, dir), EventEdited, e.Id) {
		t.Fatal("the log carries no edited record for the reword")
	}
}

// TestRewordRefusals: an empty wording, one past the entry cap, and one that is
// not a change at all. The last is a refusal rather than a no-op because the
// entry would otherwise spend an alias slot on a wording Entry.Text already has.
func TestRewordRefusals(t *testing.T) {
	s := openStore(t, t.TempDir())
	e := submit(t, s, "run the linter first")

	for _, tc := range []struct{ arg, want string }{
		{"", "needs the new wording"},
		{"   \t  ", "needs the new wording"},
		{strings.Repeat("x", maxEntryRunes+1), "limit is"},
		{"run the linter first", "unchanged"},
	} {
		if err := s.Reword(e.Id, tc.arg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("reword to %.20q = %v, want a refusal saying %q", tc.arg, err, tc.want)
		}
	}
	if got := entryByID(t, s, e.Id); got.Text != "run the linter first" || len(got.Aliases) != 0 {
		t.Fatalf("entry after four refused rewords = %+v, want it untouched", got)
	}
}

// TestRewordScrubsWhatItStores: the new wording enters the store through the
// same filter a submission does. It is written into every agent's terminal on
// every dispatch, and being the conductor's does not change what an escape
// sequence in it would do there.
func TestRewordScrubsWhatItStores(t *testing.T) {
	s := openStore(t, t.TempDir())
	e := submit(t, s, "run the linter first")

	refine(t, s.Reword(e.Id, "run \x1b[31mthe\x1b[0m linter\nfirst"))

	got := entryByID(t, s, e.Id)
	if strings.ContainsAny(got.Text, "\x1b\n") {
		t.Fatalf("stored wording = %q, want the control bytes gone", got.Text)
	}
}

// TestLowerMovesDownOnlyAndRefusesTheBottom is half of invariant I6 under the
// conductor. The verb takes no target, so it can only step down; at the bottom
// it refuses rather than wrapping, and the entry keeps its rung.
func TestLowerMovesDownOnlyAndRefusesTheBottom(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "the agent asks before it deletes")
	// Two user signals lift it to the top rung the honest way, so there is
	// something to pull down; see Policy.ceiling.
	for range defaultUserSignalsAt + defaultPromoteAt {
		if err := s.Reinforce(e.Id, SourceUser); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	top := entryByID(t, s, e.Id)
	if top.Tier != maxEarnedTier {
		t.Fatalf("tier before lowering = %d, want %d", top.Tier, maxEarnedTier)
	}

	for want := maxEarnedTier - 1; want >= 1; want-- {
		refine(t, s.Lower(e.Id))
		if got := entryByID(t, s, e.Id).Tier; got != want {
			t.Fatalf("tier after a lower = %d, want %d: lower moves exactly one rung", got, want)
		}
	}
	if err := s.Lower(e.Id); err == nil || !strings.Contains(err.Error(), "bottom rung") {
		t.Fatalf("lower at tier 1 = %v, want a refusal", err)
	}
	if got := entryByID(t, s, e.Id).Tier; got != 1 {
		t.Fatalf("tier after the refused lower = %d, want 1", got)
	}

	// Nothing was destroyed (I7): the raise that granted the rung is still in the
	// log beside the record that took it back, both attributed.
	evs := events(t, dir)
	var raised, lowered int
	for _, ev := range evs {
		switch ev.Event {
		case EventRaised:
			raised++
		case EventLowered:
			lowered++
			if ev.Source != sourceConductor {
				t.Errorf("lowered record source = %q, want %q", ev.Source, sourceConductor)
			}
			if ev.Tier < 1 || ev.Tier >= maxEarnedTier {
				t.Errorf("lowered record names tier %d, which is not below the top rung", ev.Tier)
			}
		}
	}
	if raised != maxEarnedTier-1 || lowered != maxEarnedTier-1 {
		t.Fatalf("log has %d raises and %d lowers, want %d of each", raised, lowered, maxEarnedTier-1)
	}

	// And it can climb again the only way anything climbs: by being said again.
	// A lower gives the rung back to the ladder rather than taking it off.
	if err := s.Reinforce(e.Id, SourceUser); err != nil {
		t.Fatalf("Reinforce after the lower: %v", err)
	}
	if got := entryByID(t, s, e.Id).Tier; got != 2 {
		t.Fatalf("tier after one more reinforcement = %d, want 2", got)
	}
}

// TestALoweredRecordCannotRaiseOnReplay is the other half. lowerLocked cannot
// write a raise — it decrements — so the risk that is left is the LOG: a
// hand-edited `lowered` record naming a rung above the entry's. Replay refuses
// it and counts it, exactly as it refuses an out-of-range `raised`.
func TestALoweredRecordCannotRaiseOnReplay(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "the agent asks before it deletes")
	s.Close()

	// The operator's own file is not the target here; the log is, and #38 says
	// plainly that Score is not a boundary against filesystem access. This guards
	// the ladder against a record that cannot be true, not against a person.
	appendEventLine(t, dir, `{"schema":1,"event":"lowered","id":"`+e.Id+
		`","at":"2026-01-01T00:00:00Z","source":"conductor","tier":3}`)

	again := openStore(t, dir)
	if got := entryByID(t, again, e.Id).Tier; got != 1 {
		t.Fatalf("tier after replaying a lowered record naming 3 = %d, want 1", got)
	}
	if got := again.Health().RejectedTiers; got != 1 {
		t.Fatalf("rejected tier records = %d, want 1", got)
	}
}

// TestRefineMovesNoCounterAndNoRank is R4's ruling carried into the conductor's
// verb: a reword is one statement re-spelled, so it counts nothing — and it must
// not buy the freshness an operator's own edit legitimately buys either, because
// that is rank, and rank is what the fleet is actually told.
//
// It is the drive `agentDoors` does not do: that one runs `merge` a hundred
// times, and these are the other two thirds of the same claim.
func TestRefineMovesNoCounterAndNoRank(t *testing.T) {
	s := openStore(t, t.TempDir())
	e := submitAs(t, s, "run the linter frist", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	other := submitAs(t, s, "an unrelated observation", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	if err := s.Reinforce(e.Id, SourceUser); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}
	before := entryByID(t, s, e.Id)
	at := s.lastAt[e.Id]

	refine(t, s.Reword(e.Id, "run the linter first"))
	refine(t, s.Merge(e.Id, other.Id))
	if got := entryByID(t, s, e.Id).Tier; got > 1 {
		// Tier 2 needs defaultPromoteAt occurrences; the entry has had one
		// submission and one user signal, so anything above 1 came from a refine.
		t.Fatalf("tier = %d after a reword and a merge, want the rung it earned", got)
	}

	after := entryByID(t, s, e.Id)
	if after.Reinforcements != before.Reinforcements || after.UserSignals != before.UserSignals {
		t.Fatalf("counters moved from %d/%d to %d/%d; a refine counts nothing",
			before.Reinforcements, before.UserSignals, after.Reinforcements, after.UserSignals)
	}
	if got := s.lastAt[e.Id]; got != at {
		t.Fatalf("log position moved from %d to %d; a conductor's correction is not a movement", at, got)
	}
	// The positive control, without which the assertion above says only that
	// nothing anywhere moves a position: the OPERATOR rewording the same entry in
	// their own file does move it, which is the case noteEventLocked exists for.
	writeMD(t, s.Dir(), strings.Replace(readFile(t, s.Dir(), scoreMD),
		formatLine(e.Id, "run the linter first"), formatLine(e.Id, "run the linter first, always"), 1))
	reconcile(t, s)
	if got := s.lastAt[e.Id]; got == at {
		t.Fatalf("an operator's own reword left the log position at %d; only the conductor's counts for nothing", at)
	}
	// A LOWER moves no counter either, and the entry that took it is the one the
	// conductor named — the whole store does not shift under it.
	if err := s.Reinforce(e.Id, SourceUser); err != nil {
		t.Fatalf("Reinforce to earn a rung: %v", err)
	}
	raised := entryByID(t, s, e.Id)
	refine(t, s.Lower(e.Id))
	lowered := entryByID(t, s, e.Id)
	if lowered.Reinforcements != raised.Reinforcements || lowered.UserSignals != raised.UserSignals {
		t.Fatalf("a lower moved the counters from %d/%d to %d/%d",
			raised.Reinforcements, raised.UserSignals, lowered.Reinforcements, lowered.UserSignals)
	}
}

// TestMergeInheritsNoCountsFromWhatItAbsorbs is R6's central claim, in
// the only shape where it can break: an absorbed entry that is CARRYING
// something. Every other merge in this suite absorbs a 0/0 entry, against which
// "the survivor's counters are untouched" is true of a store that adds them.
//
// The mutation it exists to kill is one line — `survivor.Reinforcements +=
// absorbed.Reinforcements; survivor.UserSignals += absorbed.UserSignals` — which
// looks like tidiness and is the one arithmetic that lets a conductor assemble
// the top rung. reinforceLocked promotes on Reinforcements and Policy.ceiling
// reads UserSignals, so a merge that added them would let an agent panel gather
// the operator's signals off several entries onto one and lift its ceiling
// without the operator saying anything twice. That is invariant I6, reached
// sideways.
func TestMergeInheritsNoCountsFromWhatItAbsorbs(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submitAs(t, s, "the agent asks before it deletes", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	gone := submitAs(t, s, "it wants permission before removing things", Provenance{Source: SourceAgent, SourcePanel: "p2"})

	// The absorbed entry is loaded up: enough reinforcements to have earned the
	// agent rung, and enough USER signals to have lifted its own ceiling to the
	// top. Everything a merge could be tempted to carry across is present.
	for range defaultPromoteAt {
		if err := s.Reinforce(gone.Id, SourceUser); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	loaded := entryByID(t, s, gone.Id)
	if loaded.UserSignals < defaultUserSignalsAt || loaded.Reinforcements < defaultPromoteAt-1 {
		t.Fatalf("the absorbed entry = %+v, want it carrying signals worth stealing", loaded)
	}

	refine(t, s.Merge(keep.Id, gone.Id))

	survivor := entryByID(t, s, keep.Id)
	if survivor.Reinforcements != 0 || survivor.UserSignals != 0 {
		t.Fatalf("survivor counters = %d/%d after absorbing %d/%d, want 0/0: a merge counts nothing",
			survivor.Reinforcements, survivor.UserSignals, loaded.Reinforcements, loaded.UserSignals)
	}
	if survivor.Tier != 1 {
		t.Fatalf("survivor tier = %d, want 1: a merge grants no rung", survivor.Tier)
	}
	// The CEILING is the half a counter check alone would miss: UserSignals is
	// not merely a number on the entry, it is what Policy.ceiling reads, so an
	// inherited signal would show up as a rung the survivor may now climb to.
	if got := s.Policy().ceiling(survivor); got != agentEarnedTier {
		t.Fatalf("survivor ceiling = %d after the merge, want %d: the absorbed entry's user signals are not its own",
			got, agentEarnedTier)
	}

	// And it holds where it actually bites: agent traffic on the survivor still
	// stops at the agent rung, however much the entry it absorbed had earned.
	for range 20 {
		if err := s.Reinforce(keep.Id, SourceAgent); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	if got := entryByID(t, s, keep.Id).Tier; got != agentEarnedTier {
		t.Fatalf("survivor reached tier %d on agent traffic, want %d (invariant I6)", got, agentEarnedTier)
	}
	// The same on a restart, since the ceiling is rebuilt from the log's own
	// records and from nothing else.
	s.Close()
	if got := entryByID(t, openStore(t, dir), keep.Id); got.UserSignals != 0 || got.Tier != agentEarnedTier {
		t.Fatalf("replayed survivor = %+v, want %d user signals and tier %d", got, 0, agentEarnedTier)
	}
}

// TestRewordRefusesAWordingAnotherEntryAlreadySays keeps a reword from
// MANUFACTURING the split that merge exists to cure.
//
// Without the check the store ends up holding two live entries on one folding
// key: two identical lines in the operator's file, both eligible for the working
// set, and neither able to fold into the other ever again. It is the natural
// move for a conductor too — score_reword's whole job is fixing an ambiguous
// wording, so converging two near-duplicates onto one good sentence is what the
// tool invites.
func TestRewordRefusesAWordingAnotherEntryAlreadySays(t *testing.T) {
	s := openStore(t, t.TempDir())
	a := submit(t, s, "run the linter frist")
	b := submit(t, s, "run the linter first")
	refine(t, s.Reword(b.Id, "always run the linter first"))

	for _, tc := range []struct{ name, text string }{
		// Another entry's CURRENT wording, and the same wording under the
		// normaliser rather than byte for byte, which is what folding matches on.
		{"another entry's wording", "always run the linter first"},
		{"another entry's wording, normalised", "ALWAYS RUN THE LINTER FIRST!"},
		// And a wording another entry KEPT: an alias folds too (invariant I4), so
		// a reword onto one splits the counts exactly the same way.
		{"another entry's alias", "run the linter first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Reword(a.Id, tc.text)
			if err == nil || !strings.Contains(err.Error(), b.Id) {
				t.Fatalf("reword onto %q = %v, want a refusal naming %s", tc.text, err, b.Id)
			}
			if !strings.Contains(err.Error(), "merge") {
				t.Fatalf("refusal = %q, want it to name the verb that does join two entries", err)
			}
		})
	}
	// Nothing moved: the two entries still say what they said, and the store did
	// not quietly fold one into the other instead.
	if got := s.Render(Context{}); len(got) != 2 {
		t.Fatalf("store after three refused rewords = %+v, want both entries", got)
	}
	if got := entryByID(t, s, a.Id).Text; got != "run the linter frist" {
		t.Fatalf("entry %s = %q, want its own wording untouched", a.Id, got)
	}
}

// TestRewordMayLandOnItsOwnKey is the positive control for the check above,
// which would otherwise be satisfied by a store that refused every reword whose
// wording normalises to anything it has seen. An entry matching ITSELF is not a
// collision: that is every fix to capitalisation or trailing punctuation, and
// every reword back to one of this entry's own prior wordings.
func TestRewordMayLandOnItsOwnKey(t *testing.T) {
	s := openStore(t, t.TempDir())
	e := submit(t, s, "run the linter first")

	// Its own current wording under a different spelling.
	refine(t, s.Reword(e.Id, "Run the linter first."))
	if got := entryByID(t, s, e.Id).Text; got != "Run the linter first." {
		t.Fatalf("text = %q, want the recased wording", got)
	}
	// And back onto its own alias, which foldTargetLocked also answers with this
	// same entry.
	refine(t, s.Reword(e.Id, "run the linter first"))
	if got := entryByID(t, s, e.Id).Text; got != "run the linter first" {
		t.Fatalf("text = %q, want the reword back to the prior wording to have landed", got)
	}
	if got := s.Render(Context{}); len(got) != 1 {
		t.Fatalf("store = %+v, want the one entry throughout", got)
	}
}

// TestRefineReplaysIdentically is invariant I1 over the three corrections: the
// same log rebuilt on a restart yields the same entries, the same aliases, the
// same tiers and the same counts. The merge's alias and the lower's rung both
// live in the log alone, so this is what says they are really there.
func TestRefineReplaysIdentically(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submitAs(t, s, "the agent asks before it deletes", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	gone := submitAs(t, s, "it wants permission before removing things", Provenance{Source: SourceAgent, SourcePanel: "p2"})
	for range defaultUserSignalsAt + defaultPromoteAt {
		if err := s.Reinforce(keep.Id, SourceUser); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	refine(t, s.Merge(keep.Id, gone.Id))
	refine(t, s.Reword(keep.Id, "the agent asks before it removes anything"))
	refine(t, s.Lower(keep.Id))
	want := s.Render(Context{})
	s.Close()

	again := openStore(t, dir)
	got := again.Render(Context{})
	if len(got) != len(want) {
		t.Fatalf("replay holds %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Id != want[i].Id || got[i].Text != want[i].Text || got[i].Tier != want[i].Tier ||
			got[i].Reinforcements != want[i].Reinforcements || got[i].UserSignals != want[i].UserSignals ||
			strings.Join(got[i].Aliases, "|") != strings.Join(want[i].Aliases, "|") {
			t.Fatalf("replayed entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if h := again.Health(); h.RejectedTiers != 0 || h.TornEvents != 0 {
		t.Fatalf("replay health = %+v, want a clean log", h)
	}
	// The alias survived the round trip, which is the merge's only lasting
	// effect on the survivor: the absorbed wording still folds.
	landed, folded, err := again.Submit("it wants permission before removing things", Provenance{Source: SourceAgent})
	if err != nil {
		t.Fatalf("Submit the absorbed wording after a restart: %v", err)
	}
	if !folded || landed.Id != keep.Id {
		t.Fatalf("after a restart the absorbed wording landed in %s (folded=%v), want %s", landed.Id, folded, keep.Id)
	}
}

// TestMergeReportsTheAliasItEvicts pins the counter alias() exists to stop
// anyone forgetting. A merge is one of the two paths that push a wording out of
// Entry.Aliases, and the eviction is the store choosing to remember less: the
// evicted phrasing stops folding, so the next repeat of it starts a second entry
// saying what this one already says. The server reports the gauge on the very
// correction that moved it, which it cannot do if the store never counted.
//
// Dropping the counter from the merge left every test in this package passing.
func TestMergeReportsTheAliasItEvicts(t *testing.T) {
	s := openStore(t, t.TempDir())
	keep := submit(t, s, "wording 0")
	// Fill the alias list exactly to its cap, which evicts nothing on its own.
	for i := 1; i <= maxAliases; i++ {
		refine(t, s.Reword(keep.Id, fmt.Sprintf("wording %d", i)))
	}
	if got := s.Health().AliasEvictions; got != 0 {
		t.Fatalf("evictions = %d before the cap was passed, want 0", got)
	}
	gone := submit(t, s, "it wants permission before removing things")

	refine(t, s.Merge(keep.Id, gone.Id))

	if got := s.Health().AliasEvictions; got != 1 {
		t.Fatalf("evictions = %d after a merge onto a full alias list, want 1: the merge's own "+
			"eviction is the one most easily dropped, and nothing else reports it", got)
	}
	// The absorbed wording is what the merge bought, so it must be the one kept.
	if got := entryByID(t, s, keep.Id).Aliases; len(got) != maxAliases || got[len(got)-1] != gone.Text {
		t.Fatalf("survivor aliases = %v, want %d of them ending in the absorbed wording", got, maxAliases)
	}
}

// TestMergeAppendsTheRetireLast is the companion the crash test cannot be,
// because the crash test needs an unwritable directory and skips itself where
// the process can write anywhere — as root, which is any container CI without a
// USER line. That leaves the subtlest ordering in the verb asserted only on
// machines that happen to run the suite unprivileged.
//
// This asserts what it can everywhere: after a merge that SUCCEEDED, the log
// holds the alias record before the retire record. It does not prove the score.md
// rewrite landed between them — nothing short of the crash does — but it catches
// the reversal, which is the mutation a tidy-up would make: appending both
// records together reads as one durable step and is exactly what launders the
// absorbed entry's provenance on the next boot.
func TestMergeAppendsTheRetireLast(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submitAs(t, s, "the agent asks before it deletes", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	gone := submitAs(t, s, "it wants permission before removing things", Provenance{Source: SourceAgent, SourcePanel: "p2"})

	refine(t, s.Merge(keep.Id, gone.Id))

	merged, retired := -1, -1
	for i, ev := range events(t, dir) {
		switch {
		case ev.Event == EventMerged && ev.Id == keep.Id:
			merged = i
		case ev.Event == EventRetired && ev.Id == gone.Id:
			retired = i
		}
	}
	switch {
	case merged < 0 || retired < 0:
		t.Fatalf("log holds merged at %d and retired at %d; a merge writes both", merged, retired)
	case retired < merged:
		t.Fatalf("the retire is at %d and the alias at %d: the retire must be appended LAST, after the "+
			"absorbed line is out of score.md, or a crash between them leaves the file showing a line "+
			"the log says retired and the recovery pass re-admits it as the operator's own text",
			retired, merged)
	}
}

// TestMergeSurvivesACrashBetweenItsHalves is the window a merge cannot close:
// the alias is durable and the score.md rewrite that should have removed the
// absorbed line did not happen.
//
// What is asserted here is the ABSENCE of a laundering. The obvious ordering —
// both records appended together, then the file — leaves the log saying the
// entry retired while the file still shows its line, and #38 section 3's
// recovery table then re-admits that line as a fresh USER-sourced entry with its
// counts zeroed: an agent's entry silently becomes the operator's, which is the
// distinction #38 section 4 fences the top tier with. So the retire is appended
// only after the line is out of the file, and this crash therefore means the
// merge did not happen at all: both entries live, both provenances intact,
// nothing re-attributed to anyone, and the whole repair is to run it again.
func TestMergeSurvivesACrashBetweenItsHalves(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submitAs(t, s, "the agent asks before it deletes", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	gone := submitAs(t, s, "it wants permission before removing things", Provenance{Source: SourceAgent, SourcePanel: "p2"})
	if err := s.Reinforce(gone.Id, SourceUser); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}

	// The rewrite goes through a sibling temp file, so an unwritable DIRECTORY is
	// what stops it — while the append to the existing log still lands, which is
	// exactly the half-done merge this test is about.
	restore := unwritable(t, dir)
	err := s.Merge(keep.Id, gone.Id)
	if err == nil {
		t.Skip("the directory this test made unwritable is still writable; this test needs an unprivileged user")
	}
	restore()

	// The alias is durable and the retire is NOT: the record that would have made
	// the file and the log disagree was never written.
	evs := events(t, dir)
	if !hasEvent(evs, EventMerged, keep.Id) {
		t.Fatalf("log = %+v, want the alias durable before the file was touched", evs)
	}
	if hasEvent(evs, EventRetired, gone.Id) {
		t.Fatalf("log retired %s while its line is still in the file: that is the ordering the fix removes", gone.Id)
	}
	if md := readFile(t, dir, scoreMD); !strings.Contains(md, gone.Id) {
		t.Fatalf("score.md lost the absorbed line the rewrite never removed:\n%s", md)
	}

	s.Close()
	again := openStore(t, dir)
	// Both entries, exactly as they were. Nothing admitted, nothing
	// re-attributed, and the absorbed entry still carries the provenance and the
	// user signal it earned — none of which survives the other ordering.
	if got := again.Render(Context{}); len(got) != 2 {
		t.Fatalf("after the crash the store holds %+v, want both entries untouched", got)
	}
	back := entryByID(t, again, gone.Id)
	if back.Provenance.Source != SourceAgent || back.Provenance.SourcePanel != "p2" {
		t.Fatalf("absorbed entry provenance = %+v, want the agent's: a failed merge must not re-attribute it", back.Provenance)
	}
	if back.Reinforcements != 1 || back.UserSignals != 1 {
		t.Fatalf("absorbed entry counters = %d/%d, want 1/1: a failed merge must not zero them",
			back.Reinforcements, back.UserSignals)
	}
	if d := again.Boot(); d != (Delta{}) {
		t.Fatalf("boot recovery = %+v, want a pass with nothing to do", d)
	}
	// The alias the merge bought is still there and re-running is idempotent:
	// appendAlias keeps one wording per folding key, so the repair does not
	// double it.
	if alias := entryByID(t, again, keep.Id).Aliases; len(alias) != 1 {
		t.Fatalf("survivor aliases = %v, want the absorbed wording kept once", alias)
	}
	refine(t, again.Merge(keep.Id, gone.Id))
	got := again.Render(Context{})
	if len(got) != 1 || got[0].Id != keep.Id {
		t.Fatalf("the repeated merge left %+v, want only %s", got, keep.Id)
	}
	if len(got[0].Aliases) != 1 {
		t.Fatalf("survivor aliases after the repair = %v, want the one wording", got[0].Aliases)
	}
}

// TestMergeCompletesItselfWhenOnlyTheRetireIsLost is the other half of that
// ordering. With the line already out of the file, a lost `retired` record is
// the recovery table's own retire row — a live entry no line carries — so the
// next pass finishes the merge in the log, with the entry's history intact (I7)
// and nobody's provenance touched.
func TestMergeCompletesItselfWhenOnlyTheRetireIsLost(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submitAs(t, s, "the agent asks before it deletes", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	gone := submitAs(t, s, "it wants permission before removing things", Provenance{Source: SourceAgent, SourcePanel: "p2"})

	// The file loses the line, and the log never learns it: exactly the state a
	// crash between the rewrite and the second append leaves behind.
	writeMD(t, dir, strings.Replace(readFile(t, dir, scoreMD),
		formatLine(gone.Id, "it wants permission before removing things")+"\n", "", 1))

	d := reconcile(t, s)
	if d.Retired != 1 || d.Admitted != 0 || d.Reattributed != 0 {
		t.Fatalf("reconcile = %+v, want the one retirement and no re-attribution", d)
	}
	if got := s.Render(Context{}); len(got) != 1 || got[0].Id != keep.Id {
		t.Fatalf("store = %+v, want only the survivor", got)
	}
	// Nothing destroyed: the absorbed entry's own submission still names it and
	// its wording, which is what I7 asks of a retire.
	evs := events(t, dir)
	if !hasEvent(evs, EventRetired, gone.Id) || !hasEvent(evs, EventSubmitted, gone.Id) {
		t.Fatalf("log = %+v, want the retirement beside the submission it retires", evs)
	}
}

// TestRefineIsReversibleByEditingTheFile is #45's other done-when. score.md is
// the truth for text and existence (#38 §3), so an operator undoes a reword by
// typing the old line back and undoes a merge by putting the absorbed wording
// back — no verb, no flag, and no conductor required.
func TestRefineIsReversibleByEditingTheFile(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submitAs(t, s, "the agent asks before it deletes", Provenance{Source: SourceAgent, SourcePanel: "p1"})
	gone := submitAs(t, s, "it wants permission before removing things", Provenance{Source: SourceAgent, SourcePanel: "p2"})
	refine(t, s.Merge(keep.Id, gone.Id))
	refine(t, s.Reword(keep.Id, "the agent asks before it removes anything"))

	// The operator disagrees with both: they put their own wording back on the
	// survivor's line and add the absorbed observation back as a bullet.
	writeMD(t, dir, readFile(t, dir, scoreMD)+"- it wants permission before removing things\n")
	writeMD(t, dir, strings.Replace(readFile(t, dir, scoreMD),
		formatLine(keep.Id, "the agent asks before it removes anything"),
		formatLine(keep.Id, "the agent asks before it deletes"), 1))
	reconcile(t, s)

	if got := entryByID(t, s, keep.Id).Text; got != "the agent asks before it deletes" {
		t.Fatalf("text after the operator's edit = %q, want theirs to win (I3)", got)
	}
	if got := s.Render(Context{}); len(got) != 2 {
		t.Fatalf("store after the operator's edits = %+v, want the absorbed observation back", got)
	}
}

// TestRefinePreservesTheOperatorsProse: score.md is a file a person writes in,
// and a refine rewrites exactly one line of it. Everything else — their
// headings, their comments, their blank lines, and the entries they did not
// name — comes back byte for byte.
func TestRefinePreservesTheOperatorsProse(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submit(t, s, "run the linter frist")
	gone := submit(t, s, "it wants permission before removing things")
	other := submit(t, s, "an unrelated observation")

	writeMD(t, dir, "# my own heading\n\n"+
		formatLine(keep.Id, "run the linter frist")+"\n"+
		"a paragraph the operator left here\n"+
		formatLine(gone.Id, "it wants permission before removing things")+"\n\n"+
		formatLine(other.Id, "an unrelated observation")+"\n")
	reconcile(t, s)

	refine(t, s.Reword(keep.Id, "run the linter first"))
	refine(t, s.Merge(keep.Id, gone.Id))

	want := "# my own heading\n\n" +
		formatLine(keep.Id, "run the linter first") + "\n" +
		"a paragraph the operator left here\n\n" +
		formatLine(other.Id, "an unrelated observation") + "\n"
	if got := readFile(t, dir, scoreMD); got != want {
		t.Fatalf("score.md =\n%q\nwant\n%q", got, want)
	}
}

// appendEventLine appends one raw line to the event log, for the tests whose
// subject is a log this daemon would not have written.
func appendEventLine(t *testing.T, dir, line string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, scoreEvents), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open the log: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("append to the log: %v", err)
	}
}
