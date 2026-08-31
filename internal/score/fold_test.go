package score

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
)

// This file covers R2's folding (#41): the normaliser's exact contract, folding
// on both input paths, aliases matching an old phrasing (#38's verification
// check 6), and the determinism invariant I1 rests on.

// TestNormalizeContract pins what the normaliser does and — just as much — what
// it refuses to do. The "not folded" half is the load-bearing one: #38 §1 chose
// textual folding over similarity, so two wordings of one observation staying
// apart is the documented behaviour, not a bug to fix later.
func TestNormalizeContract(t *testing.T) {
	same := []struct{ name, a, b string }{
		{"case", "Run The Tests", "run the tests"},
		{"trailing period", "run the tests.", "run the tests"},
		{"trailing bang and question", "run the tests!", "run the tests?"},
		{"inner whitespace", "run   the\ttests", "run the tests"},
		{"surrounding whitespace", "  run the tests  ", "run the tests"},
		{"all three at once", "  Run  the Tests...  ", "run the tests"},
	}
	for _, tt := range same {
		t.Run(tt.name, func(t *testing.T) {
			if normalize(tt.a) != normalize(tt.b) {
				t.Fatalf("normalize(%q)=%q, normalize(%q)=%q; want one key", tt.a, normalize(tt.a), tt.b, normalize(tt.b))
			}
		})
	}

	apart := []struct{ name, a, b string }{
		{"meaning", "run the tests", "execute the test suite"},
		{"word order", "tests before push", "before push tests"},
		{"inner punctuation", "don't push", "dont push"},
		{"a typo", "run the tests", "run the test"},
		{"accents", "café", "cafe"},
	}
	for _, tt := range apart {
		t.Run(tt.name, func(t *testing.T) {
			if normalize(tt.a) == normalize(tt.b) {
				t.Fatalf("normalize folded %q and %q; the normaliser is only textual by design", tt.a, tt.b)
			}
		})
	}

	// Punctuation alone normalises to nothing, and nothing is a key no entry may
	// own: two such lines share no word at all and must not merge.
	if got := normalize("..."); got != "" {
		t.Fatalf("normalize(%q) = %q, want empty", "...", got)
	}
	f := foldIndex{}
	f.addKey(normalize("..."), 0)
	if _, ok := f.lookup("!!!"); ok {
		t.Fatal("a punctuation-only line folded into another one")
	}
}

// TestNormEqAgreesWithNormalize pins the one thing normEq is allowed to be:
// normalize, with the allocation taken out. It answers the alias half of every
// submission lookup, so a disagreement here is a repeat that silently stops
// folding — or, worse, one that folds into an entry it does not belong to.
//
// The corpus deliberately includes what only normEq has to reason about: a
// trailing punctuation run it must let past the end of the key, whitespace runs
// on both sides of that boundary, and the control and format characters sanitize
// drops mid-stream.
func TestNormEqAgreesWithNormalize(t *testing.T) {
	corpus := []string{
		"", " ", "...", " . . ", "run the tests", "Run The Tests", "run the tests.",
		"run the tests...", "run the tests ... ", "  run   the\ttests  ", "run the test",
		"run the tests!?", "don't push", "dont push", "café", "cafe", "a", "ab", "a.b",
		"a. b", "tests before push", "before push tests", "RUN\u00a0THE TESTS",
		"run\x1b[1;31m the tests", "run\u200b the tests", "\ufffdrun the tests",
		"- run the tests", "run the tests -", "0", "0.",
	}
	for _, a := range corpus {
		key := normalize(a)
		for _, b := range corpus {
			want := normalize(b) == key
			if got := normEq(b, key); got != want {
				t.Fatalf("normEq(%q, %q) = %v, want %v (normalize = %q vs %q)",
					b, key, got, want, normalize(b), key)
			}
		}
	}
}

// TestNormEqMatchesNormalizeOnRandomInput is the corpus above turned into the
// RELATIONSHIP it was standing in for. The fixed pairs pin a list of answers;
// this pins the property that produces them, so a change to sanitize or
// trimmable that normEq's hand-written skip-set does not follow is caught even
// where nobody thought to add a corpus entry.
//
// The alphabet is what makes the property say anything. Wholly random strings
// almost never normalise alike, so they would only ever exercise the "these
// differ" half; drawing from a handful of runes that the transform folds
// together — a case pair, whitespace, trailing punctuation, and one each of the
// control, format and replacement characters sanitize drops — makes collisions
// the common case. Short inputs and a thousand rounds keep it inside an ordinary
// test run.
func TestNormEqMatchesNormalizeOnRandomInput(t *testing.T) {
	property := func(a, b foldable) bool {
		return normEq(string(a), normalize(string(b))) == (normalize(string(a)) == normalize(string(b)))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 1000}); err != nil {
		t.Fatalf("normEq disagreed with normalize: %v", err)
	}
}

// foldable is a string drawn from the runes the folding transform actually has
// to reason about, so quick.Check spends its rounds on inputs that fold rather
// than on ones that trivially differ. See TestNormEqMatchesNormalizeOnRandomInput.
type foldable string

// Generate implements quick.Generator.
func (foldable) Generate(rand *rand.Rand, size int) reflect.Value {
	alphabet := []rune{'a', 'A', 'b', 'B', ' ', '\t', '.', '!', '?', '-', '\u00a0', '\u200b', '\x1b', '\ufffd'}
	n := rand.Intn(min(size, 12) + 1)
	out := make([]rune, n)
	for i := range out {
		out[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return reflect.ValueOf(foldable(out))
}

// TestSubmitFoldsARepeat is the headline: a repeat is counted into the entry
// that already says it, not queued as a second line.
func TestSubmitFoldsARepeat(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)

	first := submitAs(t, s, "keep the build green", Provenance{Source: "agent", SourcePanel: "p1"})
	again, folded, err := s.Submit("  Keep the build green.  ", Provenance{Source: "agent", SourcePanel: "p2"})
	if err != nil {
		t.Fatalf("Submit repeat: %v", err)
	}

	switch {
	case !folded:
		t.Fatal("the repeat was reported as a new entry")
	case again.Id != first.Id:
		t.Fatalf("folded into %q, want %q", again.Id, first.Id)
	case again.Reinforcements != 1:
		t.Fatalf("reinforcements = %d, want 1", again.Reinforcements)
	case s.Len() != 1:
		t.Fatalf("entries = %d, want the repeat folded rather than added", s.Len())
	}

	// The surviving wording is the one already stored, and score.md gained no
	// second line for the repeat.
	if again.Text != "keep the build green" {
		t.Fatalf("text = %q, want the stored wording kept", again.Text)
	}
	if n := strings.Count(readFile(t, dir, scoreMD), "keep the build green"); n != 1 {
		t.Fatalf("score.md carries the wording %d times, want 1", n)
	}
}

// TestFoldKeepsWhoSaidIt guards the provenance a fold could lose. It leaves no
// trace in score.md and only a count in score.json, so the event log is the only
// place the second submitter can survive — and it must, or the store can say how
// often something was said but never by whom.
func TestFoldKeepsWhoSaidIt(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)

	first := submitAs(t, s, "ask before force-pushing", Provenance{Source: "agent", SourcePanel: "p1", SourceProfile: "claude", SourceCwd: "/work/a"})
	if _, _, err := s.Submit("Ask before force-pushing!", Provenance{Source: "agent", SourcePanel: "p2", SourceProfile: "codex", SourceCwd: "/work/b"}); err != nil {
		t.Fatalf("Submit repeat: %v", err)
	}

	var fold event
	var found bool
	for _, ev := range events(t, dir) {
		if ev.Event == EventFolded && ev.Id == first.Id {
			fold, found = ev, true
		}
	}
	if !found {
		t.Fatal("no folded event in the log")
	}
	switch {
	case fold.Text != "Ask before force-pushing!":
		t.Fatalf("fold text = %q, want the repeat's own wording", fold.Text)
	case fold.Prov == nil:
		t.Fatal("fold event carries no provenance")
	case fold.Prov.SourcePanel != "p2", fold.Prov.SourceProfile != "codex", fold.Prov.SourceCwd != "/work/b":
		t.Fatalf("fold provenance = %+v, want the second panel's", *fold.Prov)
	case fold.Source != "agent":
		t.Fatalf("fold source = %q, want agent", fold.Source)
	}

	// The entry keeps the FIRST submitter's provenance: a fold counts a repeat,
	// it does not reattribute the entry.
	if got := s.Render(Context{})[0].Provenance.SourcePanel; got != "p1" {
		t.Fatalf("entry panel = %q, want the original submitter's", got)
	}
}

// TestFoldSurvivesAReplay is invariant I1 at the store's own boundary: reopening
// the directory rebuilds the same counts from the log alone, with no second line
// admitted and no reinforcement lost.
func TestFoldSurvivesAReplay(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "keep the build green")
	for range 3 {
		if _, folded, err := s.Submit("KEEP THE BUILD GREEN", Provenance{Source: "agent"}); err != nil || !folded {
			t.Fatalf("Submit repeat: folded=%v err=%v", folded, err)
		}
	}
	s.Close()

	again := openStore(t, dir)
	entries := again.Render(Context{})
	if len(entries) != 1 {
		t.Fatalf("entries after replay = %d, want 1", len(entries))
	}
	if entries[0].Id != e.Id || entries[0].Reinforcements != 3 {
		t.Fatalf("replayed %+v, want %s with 3 reinforcements", entries[0], e.Id)
	}
}

// TestReconcileFoldsADuplicateLine covers the second input path: an operator
// pasting a line the store already knows is the same event as an agent
// resubmitting it, so folding runs there too.
// The duplicate line leaves score.md — the one edit the pass makes beyond
// writing ids back — and the wording they typed is preserved in the log.
func TestReconcileFoldsADuplicateLine(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "keep the build green")

	writeMD(t, dir, "- ["+e.Id+"] keep the build green\n- Keep the build green.\n")
	d := reconcile(t, s)

	switch {
	case d.Folded != 1:
		t.Fatalf("pass = %+v, want one fold", d)
	case d.Admitted != 0:
		t.Fatalf("pass = %+v, want no new entry for the duplicate", d)
	case s.Len() != 1:
		t.Fatalf("entries = %d, want 1", s.Len())
	}

	md := readFile(t, dir, scoreMD)
	if strings.Contains(md, "Keep the build green.") {
		t.Fatalf("score.md still carries the duplicate line:\n%s", md)
	}
	if !strings.Contains(md, "["+e.Id+"] keep the build green") {
		t.Fatalf("score.md lost the surviving line:\n%s", md)
	}
	// What the operator typed is not destroyed (I7) — it is on the fold event.
	if !strings.Contains(readFile(t, dir, scoreEvents), "Keep the build green.") {
		t.Fatal("the folded-away wording is nowhere in the log")
	}

	// And the fold is counted once, not once per pass: the line is gone, so the
	// next read has nothing left to fold.
	writeMD(t, dir, readFile(t, dir, scoreMD))
	reconcile(t, s)
	if got := s.Render(Context{})[0].Reinforcements; got != 1 {
		t.Fatalf("reinforcements = %d, want the paste counted exactly once", got)
	}
}

// TestReconcileFoldOrderDoesNotMatter guards the determinism I1 asks for against
// the obvious way to lose it: folding into whatever the pass happened to have
// placed already, which would make a line pasted ABOVE its twin behave
// differently from one pasted below.
func TestReconcileFoldOrderDoesNotMatter(t *testing.T) {
	for _, tt := range []struct{ name, md string }{
		{"duplicate below", "- [%s] keep the build green\n- keep the build green\n"},
		{"duplicate above", "- keep the build green\n- [%s] keep the build green\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			s := openStore(t, dir)
			e := submit(t, s, "keep the build green")

			writeMD(t, dir, strings.Replace(tt.md, "%s", e.Id, 1))
			d := reconcile(t, s)

			if d.Folded != 1 || s.Len() != 1 {
				t.Fatalf("pass = %+v with %d entries, want one fold into the one entry", d, s.Len())
			}
			if got := s.Render(Context{})[0]; got.Id != e.Id || got.Reinforcements != 1 {
				t.Fatalf("entry = %+v, want %s with one reinforcement", got, e.Id)
			}
		})
	}

	// The asymmetric case, which is where order last decided something. One of
	// the two duplicate lines is a removal the store already OWES — folded and
	// durable, its deletion outstanding — so it may not count again; the other is
	// a genuine repeat that may. The entry takes one reinforcement per pass
	// either way, and which line earns it must not depend on which the file
	// lists first.
	for _, tt := range []struct{ name, md string }{
		{"owed line first", "- [%s] run the linter first\n- Run the linter first.\n- run the linter first\n"},
		{"owed line second", "- [%s] run the linter first\n- run the linter first\n- Run the linter first.\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			s := openStore(t, dir)
			e := submit(t, s, "run the linter first")

			// Fold one duplicate with the rewrite failing: the fold is durable, the
			// line is still in the file, and its removal is what the store owes.
			writeMD(t, dir, "- ["+e.Id+"] run the linter first\n- Run the linter first.\n")
			writable := unwritable(t, dir)
			reconcileMustFail(t, s)
			writable()
			if got := s.Render(Context{})[0]; got.Reinforcements != 1 {
				t.Fatalf("setup entry = %+v, want the first fold counted once", got)
			}

			// The operator now adds a second, differently-typed duplicate. Only it
			// may count.
			writeMD(t, dir, strings.Replace(tt.md, "%s", e.Id, 1))
			d := reconcile(t, s)

			if d.Folded != 1 || s.Len() != 1 {
				t.Fatalf("pass = %+v with %d entries, want the genuine repeat counted once", d, s.Len())
			}
			if got := s.Render(Context{})[0]; got.Reinforcements != 2 {
				t.Fatalf("entry = %+v, want two reinforcements: the owed line's and the genuine repeat's", got)
			}
			if n := strings.Count(readFile(t, dir, scoreMD), "un the linter first"); n != 1 {
				t.Fatalf("score.md carries the wording %d times, want both duplicates gone", n)
			}
			// And the promotion is spent: the same file read again counts nothing.
			if d := reconcile(t, s); d.Folded != 0 {
				t.Fatalf("re-read = %+v, want nothing counted a second time", d)
			}
			if got := s.Render(Context{})[0]; got.Reinforcements != 2 {
				t.Fatalf("entry = %+v after a re-read, want the count unchanged", got)
			}
		})
	}

	// The cap the promotion must not lift: two duplicate lines that BOTH deserve
	// the count still move the entry once, so a paste cannot climb by carrying
	// several wordings of one observation.
	t.Run("two countable duplicates still count once", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		e := submit(t, s, "run the linter first")

		writeMD(t, dir, "- ["+e.Id+"] run the linter first\n- Run the linter first.\n- run the linter first\n")
		d := reconcile(t, s)

		if d.Folded != 1 {
			t.Fatalf("pass = %+v, want one reinforcement for the two duplicate lines", d)
		}
		if got := s.Render(Context{})[0]; got.Reinforcements != 1 {
			t.Fatalf("entry = %+v, want one reinforcement per entry per pass", got)
		}
	})
}

// TestReconcileDoesNotFoldIntoARetiringEntry is why the pass settles which
// entries a line still claims BEFORE it folds anything. An operator who deletes
// the id prefix from a line has not written a duplicate: the entry that line
// carried is on its way out, and counting the paste into it would take the
// operator's own line out with it.
func TestReconcileDoesNotFoldIntoARetiringEntry(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "keep the build green")

	writeMD(t, dir, "- keep the build green\n") // the id stripped, nothing else
	d := reconcile(t, s)

	if d.Folded != 0 {
		t.Fatalf("pass = %+v, want no fold into the entry it retires", d)
	}
	if s.Len() != 1 {
		t.Fatalf("entries = %d, want the operator's line still standing", s.Len())
	}
	if got := s.Render(Context{})[0]; got.Id == e.Id {
		t.Fatal("the retired entry was folded into rather than retired")
	}
	if !strings.Contains(readFile(t, dir, scoreMD), "keep the build green") {
		t.Fatal("the operator's line was dropped from score.md")
	}
}

// TestFoldsMatchAnAlias is #38's verification check 6 and invariant I4, and it
// is #41's Done-when: reword an entry, submit the OLD wording, and it folds
// rather than starting a rival entry with the history split between them.
func TestFoldsMatchAnAlias(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "say please")

	// The operator rewords the line in their editor — R1 records the prior
	// wording as an alias, and R2 is what finally reads it.
	writeMD(t, dir, "- ["+e.Id+"] ask politely before acting\n")
	reconcile(t, s)

	again, folded, err := s.Submit("Say please!", Provenance{Source: "agent", SourcePanel: "p9"})
	switch {
	case err != nil:
		t.Fatalf("Submit the old wording: %v", err)
	case !folded:
		t.Fatal("the old wording started a new entry; aliases are not matched")
	case again.Id != e.Id:
		t.Fatalf("folded into %q, want the reworded entry %q", again.Id, e.Id)
	case again.Text != "ask politely before acting":
		t.Fatalf("text = %q, want the operator's wording kept", again.Text)
	case s.Len() != 1:
		t.Fatalf("entries = %d, want 1", s.Len())
	}

	// The file path is deliberately NOT symmetric. Folding there DELETES the
	// operator's line, and the only thing that justifies deleting it is that the
	// wording is still visible on another line. A remembered wording is not: the
	// file would lose the line and show nothing resembling what they typed. So
	// the old phrasing typed back into score.md is admitted as its own entry,
	// stays visible, and R6's merge is what joins the two.
	writeMD(t, dir, "- ["+e.Id+"] ask politely before acting\n- say please\n")
	d := reconcile(t, s)
	if d.Folded != 0 || d.Admitted != 1 || s.Len() != 2 {
		t.Fatalf("pass = %+v with %d entries, want the operator's line admitted, not eaten", d, s.Len())
	}
	if !strings.Contains(readFile(t, dir, scoreMD), "say please") {
		t.Fatal("the operator's line was deleted for matching a wording the file no longer shows")
	}
}

// TestFoldNeverDeletesWhatTheLogDoesNotHold walks the two ways a folding pass
// can fail and the one thing neither may do: destroy the operator's line without
// a durable record that it existed.
//
// The log append comes first for exactly that reason, so a log that cannot be
// written stops the pass BEFORE the rewrite — the file is untouched, and nothing
// is counted. The rewrite failing afterwards is the other side of that order:
// the fold is durable while its line is still in the file, and the removal is
// owed. ONE mechanism settles that on both sides of a restart, and the subtests
// below walk it in the same process and across one.
//
// It is never written down: score.json, the only place to write it, is the
// disposable cache Open never reads. It survives a restart by being DERIVED
// instead — the fold event names the wording it removed, and score.md either
// still shows those exact bytes or does not. What it cannot promise is telling
// an owed removal from an operator who retyped the same wording verbatim in the
// meantime: it reads that conservatively and counts nothing, which is one repeat
// lost rather than one paste promoted.
func TestFoldNeverDeletesWhatTheLogDoesNotHold(t *testing.T) {
	// The log append fails: nothing counted, nothing deleted, the pass says so.
	t.Run("append fails, rewrite never runs", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		e := submit(t, s, "keep the build green")
		md := "- [" + e.Id + "] keep the build green\n- Keep the build green.\n"
		writeMD(t, dir, md)

		writable := unwritable(t, s.eventsPath)
		reconcileMustFail(t, s)

		if got := readFile(t, dir, scoreMD); got != md {
			t.Fatalf("score.md was rewritten though the fold never reached the log:\n%s", got)
		}
		if got := s.Render(Context{})[0]; got.Reinforcements != 0 {
			t.Fatalf("entry = %+v, want nothing counted", got)
		}

		// With the log writable again the same file folds normally, and the line
		// the operator typed is in the log before it leaves their file.
		writable()
		if d := reconcile(t, s); d.Folded != 1 {
			t.Fatalf("retry = %+v, want the paste folded once", d)
		}
		if !strings.Contains(readFile(t, dir, scoreEvents), "Keep the build green.") {
			t.Fatal("the folded-away wording is nowhere in the log")
		}
		if strings.Contains(readFile(t, dir, scoreMD), "Keep the build green.") {
			t.Fatal("the duplicate line survived a successful fold")
		}
	})

	// The rewrite fails: the fold is durable, the line stays, and the retry does
	// NOT count it again. Counting twice would be three occurrences of a wording
	// said twice, and one paste would earn a tier.
	t.Run("rewrite fails, the retry does not count twice", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		e := submit(t, s, "keep the build green")
		writeMD(t, dir, "- ["+e.Id+"] keep the build green\n- keep the build green\n")

		writable := unwritable(t, dir)
		reconcileMustFail(t, s)

		if got := s.Render(Context{})[0]; got.Reinforcements != 1 {
			t.Fatalf("entry = %+v, want the fold counted, since the log holds it", got)
		}
		if !strings.Contains(readFile(t, dir, scoreMD), "- keep the build green") {
			t.Fatal("the duplicate line was removed by a rewrite that failed")
		}
		// The pass reports the fold it counted, even though it could not finish,
		// and does NOT claim to have removed a line that is still in the file.
		// (The View's own pass re-reads the unchanged file and reports the same
		// removal again, uncounted — which is the suppression at work.)
		v, _ := s.View(Context{})
		if len(v.Folds) == 0 || !v.Folds[0].Counted || v.Folds[0].Removed {
			t.Fatalf("folds = %+v, want the counted fold reported as not removed", v.Folds)
		}
		for _, f := range v.Folds[1:] {
			// The fourth corner of the matrix the Fold doc promises: neither
			// counted nor removed, because the rewrite is still failing.
			if f.Counted || f.Removed {
				t.Fatalf("folds = %+v, want one paste counted once and nothing removed", v.Folds)
			}
		}

		writable()
		d2, err := s.View(Context{})
		if err != nil {
			t.Fatalf("View: %v", err)
		}
		if d2.Delta.Folded != 0 {
			t.Fatalf("retry = %+v, want the already-durable fold not counted again", d2.Delta)
		}
		if got := s.Render(Context{})[0]; got.Reinforcements != 1 || got.Tier != 1 {
			t.Fatalf("entry = %+v, want one repeat counted and no tier earned", got)
		}
		if n := strings.Count(readFile(t, dir, scoreMD), "keep the build green"); n != 1 {
			t.Fatalf("score.md carries the wording %d times, want the duplicate gone on the retry", n)
		}
		// The suppressed pass still says what it removed, marked as already
		// counted, so the deletion is never silent.
		if len(d2.Folds) == 0 {
			t.Fatal("the retry removed a line and reported nothing")
		}
		for _, f := range d2.Folds {
			if f.Counted || !f.Removed || f.Duplicates != 1 {
				t.Fatalf("folds = %+v, want the removal reported as already counted", d2.Folds)
			}
		}
		// A repeat the store chose not to count is on the gauge, not merely
		// absent from the totals.
		if s.Health().SwallowedRepeats == 0 {
			t.Fatal("a swallowed repeat went uncounted on the health gauge")
		}
	})

	// The same failure with a RESTART in the middle. What the store owes is not
	// written down — score.json is a disposable cache and recovery state has no
	// business in it — so a new process has to reach the same answer from the two
	// things that are true: the fold event names the wording it
	// removed, and score.md either still shows that wording or does not. If it
	// does, the removal never landed and is owed: take the line out, count
	// nothing. Without that, the restart is what climbs the ladder — one paste,
	// two reinforcements, tier 2.
	t.Run("rewrite fails, then the daemon restarts", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		e := submit(t, s, "keep the build green")
		writeMD(t, dir, "- ["+e.Id+"] keep the build green\n- Keep the build green.\n")

		writable := unwritable(t, dir)
		reconcileMustFail(t, s)
		if got := s.Render(Context{})[0]; got.Reinforcements != 1 {
			t.Fatalf("entry = %+v, want the fold counted once before the restart", got)
		}
		s.Close() // the daemon dies with the removal still owed

		writable()
		again := openStore(t, dir)
		got := again.Render(Context{})[0]
		if got.Reinforcements != 1 || got.Tier != 1 {
			t.Fatalf("entry = %+v after the restart, want one paste counted once and no tier earned", got)
		}
		if n := strings.Count(readFile(t, dir, scoreMD), "eep the build green"); n != 1 {
			t.Fatalf("score.md carries the wording %d times, want the owed removal done at boot", n)
		}
		if again.Health().SwallowedRepeats != 1 {
			t.Fatalf("swallowed repeats = %d, want the owed removal counted", again.Health().SwallowedRepeats)
		}

		// And the derivation is spent on that one pass: an ordinary repeat typed
		// afterwards counts normally.
		writeMD(t, dir, readFile(t, dir, scoreMD)+"- keep the build green\n")
		if d := reconcile(t, again); d.Folded != 1 {
			t.Fatalf("pass = %+v, want a later repeat counted normally", d)
		}
	})
}

// TestASwallowedFoldKeepsItsCounter covers the pass the health gauge used to lie
// about: one whose ONLY work is a repeat it declined to count.
//
// Such a pass reaches the rewrite — a line is being removed — while its delta
// stays zero, because Folded is counted inside the branch that counts. When the
// rewrite then fails, a gauge that treated "zero delta" as "this pass reverted
// itself" reverted a counter belonging to a pass that kept every other piece of
// its state: the entries, the fold record, and the removal it still owes. The
// counters now ride with the state they describe, so there is no unwind to get
// wrong.
func TestASwallowedFoldKeepsItsCounter(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "keep the build green")
	writeMD(t, dir, "- ["+e.Id+"] keep the build green\n- Keep the build green.\n")

	// The rewrite fails, so the fold is durable while its line is still in the
	// file: this pass counted, and the removal is now owed.
	writable := unwritable(t, dir)
	reconcileMustFail(t, s)
	if got := s.Health().SwallowedRepeats; got != 0 {
		t.Fatalf("swallowed = %d after the pass that counted the fold, want 0", got)
	}

	// The retry, with the rewrite still failing: the same line, already owed, so
	// nothing is counted and the delta stays zero — which is exactly the shape
	// the old unwind mistook for "this pass changed nothing".
	d, err := s.Reconcile()
	switch {
	case err == nil:
		t.Fatal("the retry's rewrite succeeded; the directory is still writable")
	case d != (Delta{}):
		t.Fatalf("retry = %+v, want a pass whose only work was a swallowed fold", d)
	}
	if got := s.Health().SwallowedRepeats; got != 1 {
		t.Fatalf("swallowed = %d, want the repeat this pass declined to count", got)
	}
	// The state that counter describes is kept too, so the pass is consistent
	// with itself rather than half reverted.
	if got := s.Render(Context{})[0]; got.Reinforcements != 1 {
		t.Fatalf("entry = %+v, want the pass to have kept the fold it counted earlier", got)
	}
	// Every buffered record, from the pass that counted and the ones that did
	// not: exactly one may claim the count, and none may claim a removal.
	v, _ := s.View(Context{}) // itself another failing pass, and another swallow
	counted := 0
	for _, f := range v.Folds {
		switch {
		case f.Removed, !f.FromFile:
			t.Fatalf("folds = %+v, want file folds that removed nothing", v.Folds)
		case f.Counted:
			counted++
		}
	}
	if counted != 1 {
		t.Fatalf("folds = %+v, want exactly one of them counted", v.Folds)
	}

	// Once the rewrite lands the line is gone, and the paste is still worth the
	// one occurrence the first pass gave it.
	before := s.Health().SwallowedRepeats
	writable()
	if _, err := s.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n := strings.Count(readFile(t, dir, scoreMD), "eep the build green"); n != 1 {
		t.Fatalf("score.md carries the wording %d times, want the owed removal done", n)
	}
	if got := s.Health().SwallowedRepeats; got != before+1 {
		t.Fatalf("swallowed = %d, want one more for the owed line this pass removed", got)
	}
	if got := s.Render(Context{})[0]; got.Reinforcements != 1 || got.Tier != 1 {
		t.Fatalf("entry = %+v, want one paste counted once and no tier earned", got)
	}
}

// TestAliasesAreDedupedAndCapped keeps an entry's memory of its own wordings
// from growing without bound, and keeps it free of distinctions the index cannot
// act on: two wordings with one folding key can only ever match the same
// repeats, so storing both costs a line in score.json and every boot's replay to
// buy nothing.
func TestAliasesAreDedupedAndCapped(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "say please")

	// A reword that only changes case and punctuation is the same wording.
	for _, text := range []string{"Say Please.", "say please!", "SAY PLEASE"} {
		writeMD(t, dir, "- ["+e.Id+"] "+text+"\n")
		reconcile(t, s)
	}
	if got := s.Render(Context{})[0].Aliases; len(got) != 1 {
		t.Fatalf("aliases = %v, want the one wording they all fold to", got)
	}

	// Genuinely different wordings accumulate, up to the cap, newest kept.
	for i := range maxAliases + 5 {
		writeMD(t, dir, fmt.Sprintf("- [%s] wording number %d\n", e.Id, i))
		reconcile(t, s)
	}
	got := s.Render(Context{})[0].Aliases
	if len(got) != maxAliases {
		t.Fatalf("aliases = %d, want the cap of %d", len(got), maxAliases)
	}
	if !strings.Contains(got[len(got)-1], fmt.Sprintf("number %d", maxAliases+3)) {
		t.Fatalf("newest alias = %q, want the most recent wording kept", got[len(got)-1])
	}
	for _, a := range got {
		if a == "say please" {
			t.Fatal("the oldest wording survived the cap; the newest should be the ones kept")
		}
	}
	// Nothing is destroyed by the cap: the dropped wording is still in the log.
	if !strings.Contains(readFile(t, dir, scoreEvents), "say please") {
		t.Fatal("a wording the cap dropped is gone from the log too")
	}
	// And the eviction is on the gauge. It fails safe — a wording that no longer
	// folds costs a duplicate entry at tier 1, never a wrong merge — but an
	// operator whose old phrasing quietly stopped matching is entitled to see
	// that the store forgot it rather than that folding broke.
	if got := s.Health().AliasEvictions; got != 5 {
		t.Fatalf("alias evictions = %d, want the 5 wordings pushed past the cap", got)
	}
}

// TestUnreportedFoldsAreCounted covers the other silent trim: the fold records
// buffered for the next read are capped, and a pass that removes more lines than
// the cap holds would otherwise leave the difference discoverable only by
// subtracting the log lines from the reported total.
func TestUnreportedFoldsAreCounted(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)

	// One entry per distinct wording, each with a duplicate line: more folds in
	// one pass than the buffer holds.
	var md strings.Builder
	const n = maxFoldNotes + 20
	for i := range n {
		fmt.Fprintf(&md, "- observation number %d\n", i)
	}
	writeMD(t, dir, md.String())
	reconcile(t, s) // admits them and writes their ids back

	kept := readFile(t, dir, scoreMD)
	md.Reset()
	md.WriteString(kept)
	for i := range n {
		fmt.Fprintf(&md, "- Observation number %d.\n", i)
	}
	writeMD(t, dir, md.String())

	v, err := s.View(Context{})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	switch {
	case v.Delta.Folded != n:
		t.Fatalf("pass = %+v, want %d folds", v.Delta, n)
	case len(v.Folds) != maxFoldNotes:
		t.Fatalf("records = %d, want the buffer's %d", len(v.Folds), maxFoldNotes)
	case v.Health.UnreportedFolds != n-maxFoldNotes:
		t.Fatalf("unreported folds = %d, want %d", v.Health.UnreportedFolds, n-maxFoldNotes)
	}
}

// TestFoldingIsOrderIndependent is invariant I1's other half: the same
// submissions in a different order leave the same entries with the same counts.
// Ids are random, so the comparison is over wording and bookkeeping.
func TestFoldingIsOrderIndependent(t *testing.T) {
	texts := []string{"keep the build green", "ask before force-pushing", "Keep the build GREEN.", "run gofmt", "ask before force-pushing"}
	shuffled := []string{"ask before force-pushing", "run gofmt", "keep the build green", "ask before force-pushing", "Keep the build GREEN."}

	fold := func(order []string) map[string]int {
		s := openStore(t, t.TempDir())
		for _, text := range order {
			submit(t, s, text)
		}
		out := map[string]int{}
		for _, e := range s.Render(Context{}) {
			out[e.Text] = e.Reinforcements
		}
		return out
	}

	a, b := fold(texts), fold(shuffled)
	if len(a) != 3 {
		t.Fatalf("entries = %v, want three distinct wordings", a)
	}
	for text, n := range a {
		if b[text] != n {
			t.Fatalf("%q has %d reinforcements in one order and %d in the other", text, n, b[text])
		}
	}
}

// TestOversizedRepeatIsStillRefused keeps the two admission rules independent:
// folding is not a way past the weight cap, and a caller that submits a 400-rune
// repeat is told the same thing as one that submits a 400-rune novelty.
func TestOversizedRepeatIsStillRefused(t *testing.T) {
	s := openStore(t, t.TempDir())
	long := strings.Repeat("a", maxEntryRunes+1)
	if _, _, err := s.Submit(long, Provenance{Source: "agent"}); err == nil {
		t.Fatal("an oversized submission was accepted")
	}
	if _, _, err := s.Submit(long, Provenance{Source: "agent"}); err == nil {
		t.Fatal("an oversized repeat was accepted")
	}
	if s.Len() != 0 {
		t.Fatalf("entries = %d, want none", s.Len())
	}
}

// TestOwedRemovalsAreBoundedNarrowly pins the two things that keep the boot
// derivation from eating repeats nobody owed. Both are cases where a fold is in
// the log and the wording is in score.md at boot — the shape the derivation
// looks for — and in neither may it fire.
//
// Everything here runs on a healthy disk. That is the point: the derivation was
// built for a failed rewrite, but what it actually matches is a fold whose exact
// bytes are back in the file, and nothing about that needs a failure.
func TestOwedRemovalsAreBoundedNarrowly(t *testing.T) {
	// A SUBMIT-path fold never touched score.md, so it can never leave a removal
	// owed — however exactly the operator later types those same bytes.
	t.Run("a submitted repeat owes no removal", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		e := submit(t, s, "run the linter first")
		if _, folded, err := s.Submit("run the linter first", Provenance{Source: "agent", SourcePanel: "p1"}); err != nil || !folded {
			t.Fatalf("setup submit: folded=%v err=%v", folded, err)
		}
		s.Close()

		// The operator now writes that wording into their file by hand, and the
		// daemon restarts before any read.
		writeMD(t, dir, "- ["+e.Id+"] run the linter first\n- run the linter first\n")
		again := openStore(t, dir)

		if got := again.Render(Context{})[0]; got.Reinforcements != 2 {
			t.Fatalf("entry = %+v, want the operator's own repeat counted", got)
		}
		if again.Health().SwallowedRepeats != 0 {
			t.Fatal("a repeat was swallowed against a removal nobody owed")
		}
	})

	// After a CLEAN fold, a retype that differs in case or punctuation is not the
	// same bytes, so it counts. Only a verbatim retype — the re-paste — is
	// mistaken for the removal the earlier fold already made.
	t.Run("a reworded retype after a clean fold counts", func(t *testing.T) {
		dir := t.TempDir()
		s := openStore(t, dir)
		e := submit(t, s, "run the linter first")
		writeMD(t, dir, "- ["+e.Id+"] run the linter first\n- Run the linter first.\n")
		if d := reconcile(t, s); d.Folded != 1 {
			t.Fatalf("setup fold = %+v", d)
		}
		s.Close()

		writeMD(t, dir, readFile(t, dir, scoreMD)+"- RUN THE LINTER FIRST!\n")
		again := openStore(t, dir)

		if got := again.Render(Context{})[0]; got.Reinforcements != 2 {
			t.Fatalf("entry = %+v, want the differently-typed repeat counted", got)
		}
		if again.Health().SwallowedRepeats != 0 {
			t.Fatalf("swallowed = %d, want the byte-exact comparison to let this through", again.Health().SwallowedRepeats)
		}
	})
}

// TestEveryOwedLineIsRemembered is why owed holds every wording an entry owes a
// removal for rather than the newest one.
//
// A failed rewrite leaves ALL of a pass's folded lines in the file, so an entry
// can owe several at once — one from the pass that first failed, another the
// operator typed while the disk was still broken. A store that remembered one of
// them counted the other on the next pass, and the one it had forgotten on the
// pass after that, alternating for as long as the failure lasted: a static file
// climbing a tier per pass. That is the exact harm the whole mechanism exists to
// prevent, and it is PERMANENT — a raised event is durable, replayed at every
// boot, and #37 demotes nothing, so the unearned tier outlives the bad disk.
//
// The threshold here is deliberately out of reach, so a single raised event
// anywhere in the log means the ladder was climbed by something other than
// recurrence.
func TestEveryOwedLineIsRemembered(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Policy{PromoteAt: 10})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	e := submit(t, s, "run the linter first")

	head := "- [" + e.Id + "] run the linter first\n"
	first := "- Run the linter first.\n"
	second := "- run the linter first\n"

	// One duplicate folds and the rewrite fails: the fold is durable, its line is
	// still in the file, and its removal is owed.
	writeMD(t, dir, head+first)
	writable := unwritable(t, dir)
	reconcileMustFail(t, s)
	writable()
	if got := s.Render(Context{})[0]; got.Reinforcements != 1 {
		t.Fatalf("entry = %+v, want the first fold counted once", got)
	}

	// The operator adds a second, differently typed duplicate while the disk is
	// still broken. It is a genuine repeat, so it counts — once.
	writeMD(t, dir, head+first+second)
	writable = unwritable(t, dir)
	reconcileMustFail(t, s)
	if got := s.Render(Context{})[0]; got.Reinforcements != 2 {
		t.Fatalf("entry = %+v, want the second duplicate counted once", got)
	}

	// And never again, however long the rewrite keeps failing: both lines are
	// owed, and both have to be remembered at the same time.
	for i := 1; i <= 5; i++ {
		reconcileMustFail(t, s)
		if got := s.Render(Context{})[0]; got.Reinforcements != 2 {
			t.Fatalf("retry %d: entry = %+v, want two occurrences and no climb", i, got)
		}
	}

	// Nor when the operator reorders the two lines between passes, which is what
	// shook a debt loose when only one was held.
	for i := 1; i <= 3; i++ {
		writable()
		writeMD(t, dir, head+second+first)
		writable = unwritable(t, dir)
		reconcileMustFail(t, s)
		if got := s.Render(Context{})[0]; got.Reinforcements != 2 {
			t.Fatalf("reorder %d: entry = %+v, want the swap to shake nothing loose", i, got)
		}
	}
	writable()

	// Durable and replayed forever, so the absence has to hold in the log rather
	// than merely in memory.
	for _, ev := range events(t, dir) {
		if ev.Event == EventRaised {
			t.Fatalf("a tier was earned by a failing disk: %+v", ev)
		}
	}

	// Recovery settles both lines at once: the pass that finally rewrites takes
	// them out, counts nothing more, and owes nothing after.
	d := reconcile(t, s)
	if d.Folded != 0 {
		t.Fatalf("recovery pass = %+v, want the already-durable folds not counted again", d)
	}
	if got := s.Render(Context{})[0]; got.Reinforcements != 2 || got.Tier != 1 {
		t.Fatalf("entry = %+v, want two occurrences and no tier", got)
	}
	if n := strings.Count(readFile(t, dir, scoreMD), "un the linter first"); n != 1 {
		t.Fatalf("score.md carries the wording %d times, want both owed lines removed", n)
	}
	// The debts are settled, so an ordinary repeat typed afterwards counts.
	writeMD(t, dir, readFile(t, dir, scoreMD)+second)
	if d := reconcile(t, s); d.Folded != 1 {
		t.Fatalf("pass = %+v, want a later repeat counted normally", d)
	}
}

// TestOwedRemovalsAreCapped pins the bound on that memory, and the direction it
// fails in. maxOwedRemovals wordings per entry is the ceiling; past it the
// OLDEST is dropped, and a forgotten debt can only make a pass COUNT a repeat it
// should have swallowed. It can never remove a line, because countable is the
// only thing owed reaches.
func TestOwedRemovalsAreCapped(t *testing.T) {
	var owed []string
	for i := 0; i < maxOwedRemovals+4; i++ {
		owed = addOwed(owed, fmt.Sprintf("wording %d", i))
	}
	if len(owed) != maxOwedRemovals {
		t.Fatalf("owed = %d wordings, want the cap at %d", len(owed), maxOwedRemovals)
	}
	if owed[0] != "wording 4" {
		t.Fatalf("owed starts at %q, want the four oldest dropped", owed[0])
	}
	if want := fmt.Sprintf("wording %d", maxOwedRemovals+3); owed[len(owed)-1] != want {
		t.Fatalf("owed ends at %q, want the newest kept (%q)", owed[len(owed)-1], want)
	}
	// The same bytes twice are one debt, so a re-paste cannot push a real one out.
	before := len(owed)
	if owed = addOwed(owed, owed[0]); len(owed) != before {
		t.Fatalf("owed = %d wordings after a repeat of one it holds, want %d", len(owed), before)
	}
}

// TestARolledBackPassAppliesNoCounters is the health gauge held to the same rule
// as the rest of the pass: a pass that commits nothing changes nothing.
//
// Nothing is unwound here, and that is the property. A pass works its counters
// out into a local and folds them into the store at the same line that commits
// its entries, so a pass that fails to append its events never reaches that line
// and the gauge was never touched to begin with — "revert" is "don't apply", and
// there is no tail left to get the condition wrong.
//
// The alias eviction below stands in for every counter the compute phase works
// out. Applying it to a pass that failed would leave the store insisting on an
// eviction that never happened, again on every retry, until the numbers claim
// more evictions than the entry has ever had wordings.
func TestARolledBackPassAppliesNoCounters(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "wording zero")

	// Fill the alias list to the cap, so the next reword must evict.
	for i := 1; i <= maxAliases; i++ {
		writeMD(t, dir, fmt.Sprintf("- [%s] wording %d\n", e.Id, i))
		reconcile(t, s)
	}
	before := s.Health()
	if before.AliasEvictions != 0 {
		t.Fatalf("evictions = %d, want the cap not yet reached", before.AliasEvictions)
	}

	// One more reword, with the log unwritable: the pass computes the eviction,
	// fails to append, and must leave both the entry and the gauge as they were.
	writable := unwritable(t, s.eventsPath)
	writeMD(t, dir, fmt.Sprintf("- [%s] wording %d\n", e.Id, maxAliases+1))
	reconcileMustFail(t, s)

	if got := s.Health(); got != before {
		t.Fatalf("health = %+v after a failed pass, want %+v", got, before)
	}
	if got := s.Render(Context{})[0]; got.Text != fmt.Sprintf("wording %d", maxAliases) {
		t.Fatalf("entry = %+v, want the failed pass to have changed nothing", got)
	}

	// With the log writable the same edit lands, and the eviction is counted
	// once — by the pass that actually made it.
	writable()
	reconcile(t, s)
	if got := s.Health().AliasEvictions; got != 1 {
		t.Fatalf("evictions = %d, want exactly the one the store made", got)
	}
}
