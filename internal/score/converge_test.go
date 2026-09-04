package score

import (
	"strings"
	"testing"
)

// This file covers #52: the operator's own merge, spelled in score.md. R6 gave
// the conductor `score_merge` and gated it on the panel the SERVER marked
// conductor, which the operator's cockpit is not — so a fleet with no conductor
// running had no merge at all, and an operator who deleted one of two entries
// saying the same thing could not buy the alias that makes a later repeat of the
// deleted wording fold rather than start a third entry.
//
// The gesture is a reword that lands on what another entry already says. Every
// test here is about the alias, because the alias is the whole of what a merge
// buys: the pair becoming one entry is visible in the file, and the fold that
// does or does not happen weeks later is not.

// TestEditingALineIntoAnotherBuysTheAlias is #52's Done-when and the assertion
// that fails today. The load-bearing half is the LAST one: after the operator's
// edit, a repeat of the absorbed wording must fold into the survivor rather than
// start a third entry. Dropping the `alias` call in reconcileLocked's
// convergence block leaves every other assertion here passing.
func TestEditingALineIntoAnotherBuysTheAlias(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submit(t, s, "run the linter before claiming a task is done")
	gone := submit(t, s, "always run the linter first")

	// The operator retypes the second line to say what the first already says.
	writeMD(t, dir, "- ["+keep.Id+"] run the linter before claiming a task is done\n"+
		"- ["+gone.Id+"] run the linter before claiming a task is done\n")
	d := reconcile(t, s)

	switch {
	case d.Merged != 1:
		t.Fatalf("pass = %+v, want one merge", d)
	case d.Retired != 1:
		t.Fatalf("pass = %+v, want the absorbed entry retired", d)
	case s.Len() != 1:
		t.Fatalf("entries = %d, want the pair joined into one", s.Len())
	}
	if got := s.Render(Context{})[0]; got.Id != keep.Id {
		t.Fatalf("survivor = %s, want %s: the entry that already said it survives", got.Id, keep.Id)
	}

	md := readFile(t, dir, scoreMD)
	if n := strings.Count(md, "run the linter before claiming a task is done"); n != 1 {
		t.Fatalf("score.md carries the wording %d times, want the absorbed line gone:\n%s", n, md)
	}
	// Nothing is destroyed (I7): the absorbed wording is in the log.
	if !strings.Contains(readFile(t, dir, scoreEvents), "always run the linter first") {
		t.Fatal("the absorbed wording is nowhere in the log")
	}

	// THE assertion. Before this change the repeat started a third entry.
	e, folded, err := s.Submit("Always run the linter first.", Provenance{Source: SourceAgent})
	switch {
	case err != nil:
		t.Fatalf("Submit: %v", err)
	case !folded:
		t.Fatal("a repeat of the absorbed wording started a new entry rather than folding")
	case e.Id != keep.Id:
		t.Fatalf("the repeat folded into %s, want the survivor %s", e.Id, keep.Id)
	case s.Len() != 1:
		t.Fatalf("entries = %d, want the repeat folded into the one entry", s.Len())
	}
}

// TestEditingALineIntoAnotherCountsNothing is R4's ruling held across the new
// door. A reword counts nothing, so a fold reached BY rewording must not move a
// counter a reword cannot — or it is the same back door in a different coat. The
// survivor gains a wording and nothing else: not the absorbed entry's
// reinforcements, not its signals, not a rung.
func TestEditingALineIntoAnotherCountsNothing(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submit(t, s, "keep the build green")
	gone := submit(t, s, "do not break the build")

	// The absorbed entry is the one with something worth stealing: reinforced to
	// the rung recurrence alone can reach.
	for range 4 {
		if err := s.Reinforce(gone.Id, SourceUser); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	}
	if got := s.Render(Context{}); len(got) != 2 {
		t.Fatalf("entries = %d, want the two the test set up", len(got))
	}

	writeMD(t, dir, "- ["+keep.Id+"] keep the build green\n"+
		"- ["+gone.Id+"] keep the build green\n")
	d := reconcile(t, s)

	if d.Merged != 1 || d.Folded != 0 || d.Raised != 0 {
		t.Fatalf("pass = %+v, want a merge that folded and raised nothing", d)
	}
	got := s.Render(Context{})[0]
	switch {
	case got.Id != keep.Id:
		t.Fatalf("survivor = %s, want %s", got.Id, keep.Id)
	case got.Reinforcements != 0:
		t.Fatalf("survivor = %+v, want none of the absorbed entry's reinforcements", got)
	case got.UserSignals != 0:
		t.Fatalf("survivor = %+v, want none of the absorbed entry's user signals", got)
	case got.Tier != 1:
		t.Fatalf("survivor = %+v, want the rung it earned and no other", got)
	}
	// And no fold record: a merge is not a repeat, so the daemon must not
	// announce one.
	if n := len(s.DrainFolds()); n != 0 {
		t.Fatalf("fold records = %d, want none: a merge counts no repeat", n)
	}
}

// TestConvergenceNeedsTheEditToCreateTheCollision is the narrowing, and with it
// R2's ruling that a line CARRYING an id is the operator's own decision about
// which entry it is. Two entries can already share a folding key — a line the
// operator restored under an id the log had retired admits as its own entry even
// where its wording duplicates a live one — and a later cosmetic fix to one of
// them must not silently retire it. The alias such a fold would buy normalises
// to the survivor's own wording, so it would fold nothing that did not already
// fold: all cost, no purchase.
func TestConvergenceNeedsTheEditToCreateTheCollision(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	a := submit(t, s, "run the tests")

	// R2's row: an id the log does not know, admitted under that id even though
	// its wording duplicates a live entry's.
	writeMD(t, dir, "- ["+a.Id+"] run the tests\n- [b0b0b0] Run the tests.\n")
	if d := reconcile(t, s); d.Admitted != 1 || s.Len() != 2 {
		t.Fatalf("setup pass = %+v with %d entries, want the restored line as its own entry", d, s.Len())
	}

	// The operator now fixes the trailing punctuation. The text changed; the
	// folding key did not.
	writeMD(t, dir, "- ["+a.Id+"] run the tests\n- [b0b0b0] Run the tests\n")
	d := reconcile(t, s)

	if d.Merged != 0 {
		t.Fatalf("pass = %+v, want no merge: the edit did not create the collision", d)
	}
	if d.Superseded != 1 || s.Len() != 2 {
		t.Fatalf("pass = %+v with %d entries, want the wording corrected and both entries standing", d, s.Len())
	}
}

// TestConvergenceWillNotFoldIntoARetiringEntry is the same rule the duplicate
// bullet already keeps: an entry the pass is about to remove is not something to
// fold into. The operator here retyped one line to say what the other said AND
// deleted the other, in one save.
func TestConvergenceWillNotFoldIntoARetiringEntry(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submit(t, s, "review the diff before pushing")
	gone := submit(t, s, "read what you are about to push")

	writeMD(t, dir, "- ["+gone.Id+"] review the diff before pushing\n")
	d := reconcile(t, s)

	if d.Merged != 0 {
		t.Fatalf("pass = %+v, want no merge into the entry it retires", d)
	}
	if s.Len() != 1 {
		t.Fatalf("entries = %d, want the operator's one line", s.Len())
	}
	got := s.Render(Context{})[0]
	if got.Id != gone.Id || got.Text != "review the diff before pushing" {
		t.Fatalf("entry = %+v, want %s carrying the operator's text", got, gone.Id)
	}
	if got.Id == keep.Id {
		t.Fatal("the deleted line's entry survived")
	}
}

// TestConvergenceOrderDoesNotMatter guards determinism (I1) the way the fold
// tests do: which of the two lines the operator edited, and which comes first in
// the file, must not change which entry survives. The survivor is the one that
// ALREADY said the wording, wherever its line sits.
func TestConvergenceOrderDoesNotMatter(t *testing.T) {
	for _, tt := range []struct{ name, md string }{
		{"survivor above", "- [%keep%] tidy the worktree\n- [%gone%] tidy the worktree\n"},
		{"survivor below", "- [%gone%] tidy the worktree\n- [%keep%] tidy the worktree\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			s := openStore(t, dir)
			keep := submit(t, s, "tidy the worktree")
			gone := submit(t, s, "clean up after yourself")

			md := strings.ReplaceAll(tt.md, "%keep%", keep.Id)
			writeMD(t, dir, strings.ReplaceAll(md, "%gone%", gone.Id))
			d := reconcile(t, s)

			if d.Merged != 1 || s.Len() != 1 {
				t.Fatalf("pass = %+v with %d entries, want one merge", d, s.Len())
			}
			if got := s.Render(Context{})[0]; got.Id != keep.Id {
				t.Fatalf("survivor = %s, want %s wherever its line sits", got.Id, keep.Id)
			}
			e, folded, err := s.Submit("clean up after yourself", Provenance{Source: SourceAgent})
			if err != nil || !folded || e.Id != keep.Id {
				t.Fatalf("Submit(absorbed wording) = %+v folded=%v err=%v, want a fold into %s", e, folded, err, keep.Id)
			}
		})
	}
}

// TestConvergenceReplaysIdentically is invariant I1 across a restart: the log
// alone must rebuild what the pass decided, alias included. A `merged` record
// replays as the alias and nothing else, which is the same rule the conductor's
// merge already relies on.
func TestConvergenceReplaysIdentically(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submit(t, s, "name the branch after the issue")
	gone := submit(t, s, "branches are named for their issue")

	writeMD(t, dir, "- ["+keep.Id+"] name the branch after the issue\n"+
		"- ["+gone.Id+"] name the branch after the issue\n")
	if d := reconcile(t, s); d.Merged != 1 {
		t.Fatalf("pass = %+v, want one merge", d)
	}
	before := s.Render(Context{})
	s.Close()

	reopened := openStore(t, dir)
	after := reopened.Render(Context{})
	if len(after) != 1 || len(before) != 1 {
		t.Fatalf("entries = %d before, %d after, want one either side", len(before), len(after))
	}
	if before[0].Id != after[0].Id || before[0].Text != after[0].Text {
		t.Fatalf("entry = %+v after the restart, want %+v", after[0], before[0])
	}
	e, folded, err := reopened.Submit("branches are named for their issue", Provenance{Source: SourceAgent})
	if err != nil || !folded || e.Id != keep.Id {
		t.Fatalf("Submit(absorbed wording) = %+v folded=%v err=%v, want the alias to have survived the restart", e, folded, err)
	}
}

// TestTheFileTeachesTheMergeGesture is why #52 lands in the file rather than in
// a verb, and it is the assertion that the two halves agree: the gesture is
// taught in the header of a fresh score.md, and performing it on that same file
// merges. A header describing something the pass does not do fails here.
//
// It also pins the header against the rewrite this pass makes. A merging pass
// rewrites score.md whole, and the operator's prose — the header included — has
// to come back byte for byte.
func TestTheFileTeachesTheMergeGesture(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	fresh := headerLines(readFile(t, dir, scoreMD))
	if len(fresh) == 0 {
		t.Fatal("a fresh score.md teaches nothing")
	}
	if taught := strings.Join(fresh, "\n"); !strings.Contains(taught, "EXACTLY what the other") {
		t.Fatalf("the header never teaches the merge gesture:\n%s", taught)
	}

	keep := submit(t, s, "ask before rewriting history")
	gone := submit(t, s, "never rewrite a shared branch")

	// The gesture exactly as the header states it, typed under the header the
	// store wrote.
	writeMD(t, dir, strings.Join(fresh, "\n")+"\n"+
		"- ["+keep.Id+"] ask before rewriting history\n"+
		"- ["+gone.Id+"] ask before rewriting history\n")
	if d := reconcile(t, s); d.Merged != 1 {
		t.Fatalf("pass = %+v, want the gesture the header teaches to merge", d)
	}
	md := readFile(t, dir, scoreMD)
	if got := headerLines(md); len(got) != len(fresh) {
		t.Fatalf("the merging rewrite kept %d header lines, want %d:\n%s", len(got), len(fresh), md)
	}
	e, folded, err := s.Submit("never rewrite a shared branch", Provenance{Source: SourceAgent})
	if err != nil || !folded || e.Id != keep.Id {
		t.Fatalf("Submit(absorbed wording) = %+v folded=%v err=%v, want the alias the header promises", e, folded, err)
	}
}

// TestConvergenceKeepsTheAliasWhenTheRewriteFails states the cost of the pass
// making ONE durable append. The alias is durable before score.md is touched, so
// a rewrite that fails cannot lose it — but the absorbed entry is retired in the
// same append while its line is still in the file, so the next pass re-admits
// that line under its own id as the operator's. The merge is half done: the
// alias is bought and paid for, and what the operator is left with is a
// duplicate line they can delete. It is reported as `reattributed` rather than
// happening silently.
func TestConvergenceKeepsTheAliasWhenTheRewriteFails(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	keep := submit(t, s, "rebase, never merge, onto main")
	gone := submit(t, s, "no merge commits on main")

	writeMD(t, dir, "- ["+keep.Id+"] rebase, never merge, onto main\n"+
		"- ["+gone.Id+"] rebase, never merge, onto main\n")
	writable := unwritable(t, dir)
	reconcileMustFail(t, s)
	writable()

	// The alias landed before the file was touched, which is the half that
	// matters: a repeat of the absorbed wording folds.
	e, folded, err := s.Submit("no merge commits on main", Provenance{Source: SourceAgent})
	if err != nil || !folded || e.Id != keep.Id {
		t.Fatalf("Submit(absorbed wording) = %+v folded=%v err=%v, want a fold into %s", e, folded, err, keep.Id)
	}
	// And the line the rewrite could not remove comes back as its own entry,
	// counted as a re-attribution rather than absorbed in silence.
	if s.Len() != 2 {
		t.Fatalf("entries = %d, want the line the rewrite could not remove back as its own", s.Len())
	}
}
