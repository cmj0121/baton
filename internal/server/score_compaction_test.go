package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/score"
)

// compactionNotice is the sentence server.ScoreCompaction writes, and the
// substring every assertion here counts. It is the wording rather than the
// message key because the wording is the point: an operator watching their
// fleet reorder itself has to be able to connect the two.
const compactionNotice = "recency spacing is not"

// TestARuntimeCompactionIsAnnouncedExactlyOnce is #56's last gap, which was not
// in the store but in everything downstream of it.
//
// A compaction re-spaces recency — the ORDER of the live entries survives and the
// SPACING does not — and the boot has warned about that since R7. Once the store
// gained a compactor of its own, the same re-spacing began happening with no
// restart and no line anywhere: score.status would show it to whoever thought to
// ask, and a panel's injected memory could be reordered under an operator who was
// watching, in silence.
//
// It cannot be fixed in internal/score, which does not log by design, so it is
// fixed on the read that is about to USE the re-spaced memory. What that read
// must not do is re-announce: it runs on every brief and every score.* verb, and
// a warning repeated on each of them is one nobody reads.
//
// The counter watched is Compactions and not Compacted, because Compacted
// describes the last rewrite and two rewrites of one store can describe
// themselves identically.
func TestARuntimeCompactionIsAnnouncedExactlyOnce(t *testing.T) {
	// A view as the store hands one back after a rewrite, which is what scoreLook
	// is given on the read that follows one. Driven through scoreLook rather than
	// through the notifier under it, so the wiring is what is under test and not
	// only the latch.
	compacted := func(n int) score.View {
		return score.View{
			Total:  10,
			Health: score.Health{Compactions: n, Compacted: 310, LogBefore: 8668049, LogAfter: 24865},
		}
	}

	t.Run("the first read after a rewrite says so", func(t *testing.T) {
		st, dir := scoreStore(t)
		s, _, _ := scoreServer(st)

		logged := captureLog(t)
		s.scoreLook(compacted(1), nil)
		got := logged()
		if n := strings.Count(got, compactionNotice); n != 1 {
			t.Fatalf("a runtime rewrite produced %d notices, want exactly 1:\n%s", n, got)
		}
		for _, want := range []string{
			`"level":"warn"`, `"compacted":310`, `"entries":10`,
			`"log_before":8668049`, `"log_after":24865`, dir,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the runtime notice is missing %s:\n%s", want, got)
			}
		}
	})

	t.Run("every read after that says nothing", func(t *testing.T) {
		st, _ := scoreStore(t)
		s, _, _ := scoreServer(st)
		s.scoreLook(compacted(1), nil)

		// Including a real one over the wire, because the silence has to hold for
		// the path the fleet actually takes and not only for the call above.
		logged := captureLog(t)
		for range 5 {
			s.scoreLook(compacted(1), nil)
		}
		status(t, s)
		if got := logged(); strings.Contains(got, compactionNotice) {
			t.Errorf("one rewrite was announced again by a later read:\n%s", got)
		}
	})

	t.Run("a second rewrite is announced again", func(t *testing.T) {
		// The other direction, and the one a latch gets wrong by never releasing:
		// a daemon that compacts every few hours must say so every time.
		st, _ := scoreStore(t)
		s, _, _ := scoreServer(st)
		s.scoreLook(compacted(1), nil)

		logged := captureLog(t)
		s.scoreLook(compacted(2), nil)
		if n := strings.Count(logged(), compactionNotice); n != 1 {
			t.Errorf("a second rewrite produced %d notices, want exactly 1:\n%s", n, logged())
		}
	})

	t.Run("the boot's own rewrite is not announced a second time", func(t *testing.T) {
		// cmd/baton writes the line for a boot compaction, from this same producer,
		// before any connection exists. Without WithScore taking the store's count
		// as its baseline the first read would write it again, and one rewrite would
		// reach the operator as two.
		st := compactedAtBoot(t)
		s, _, _ := scoreServer(st)

		logged := captureLog(t)
		status(t, s)
		if got := logged(); strings.Contains(got, compactionNotice) {
			t.Errorf("the first read re-announced the rewrite the boot had already reported:\n%s", got)
		}

		// And the fixture is not silent for the trivial reason: the next rewrite on
		// the same server is announced.
		logged = captureLog(t)
		s.scoreLook(compacted(st.Health().Compactions+1), nil)
		if got := logged(); !strings.Contains(got, compactionNotice) {
			t.Errorf("a rewrite past the boot's was not announced either:\n%s", got)
		}
	})
}

// TestASubmitOnlyDaemonStillHearsAboutARewrite is the shape the read-path
// announcement cannot cover, and it is the shape that CAUSES the rewrite.
//
// The compactor is woken by GROWTH, and the only verb that grows the log is
// score.submit. A daemon whose traffic is submissions and nothing else — an
// agent fleet writing observations, with no brief dispatched and no `baton ctl
// score list` typed — takes neither scoreView nor scoreExplain, so for as long
// as the announcement lived on those two alone the daemon most likely to compact
// was the one that said nothing about it.
//
// The latch is wound back to model the state a RUNTIME rewrite leaves: the
// store's counter has moved past what the server was handed at WithScore, and
// nothing has looked since. That is what a real compactLoop does eight mebibytes
// into a daemon's life, and it is the one part of it a test cannot afford to
// spend eight mebibytes of fsync'd submissions reproducing.
func TestASubmitOnlyDaemonStillHearsAboutARewrite(t *testing.T) {
	const note = "the fleet runs the linter before it opens anything"

	t.Run("the first submission after a rewrite says so", func(t *testing.T) {
		st := compactedAtBoot(t)
		s, _, _ := scoreServer(st)
		s.scoreState.compactions.Store(0)

		logged := captureLog(t)
		if got := submitAs(t, s, "p1", note); got.Type != "score" {
			t.Fatalf("the submission answered %+v, want it recorded", got)
		}
		if n := strings.Count(logged(), compactionNotice); n != 1 {
			t.Fatalf("a submit-only daemon produced %d notices for a rewrite, want exactly 1:\n%s",
				n, logged())
		}
	})

	t.Run("every submission after that says nothing", func(t *testing.T) {
		// Once per rewrite is the whole discipline, and a write path is where it is
		// easiest to lose: submissions arrive far more often than reads do.
		st := compactedAtBoot(t)
		s, _, _ := scoreServer(st)
		s.scoreState.compactions.Store(0)
		submitAs(t, s, "p1", note)

		logged := captureLog(t)
		for i := range submitBurst - 1 {
			submitAs(t, s, "p1", fmt.Sprintf("the fleet names its worktrees %d", i))
		}
		if got := logged(); strings.Contains(got, compactionNotice) {
			t.Errorf("one rewrite was announced again by a later submission:\n%s", got)
		}
	})

	t.Run("the boot's own rewrite is not announced by a submission", func(t *testing.T) {
		// The latch left where WithScore put it, which is the honest boot state:
		// cmd/baton has already written the line from the same producer, and a
		// submit that announced it again would reach the operator as two rewrites.
		st := compactedAtBoot(t)
		s, _, _ := scoreServer(st)

		logged := captureLog(t)
		if got := submitAs(t, s, "p1", note); got.Type != "score" {
			t.Fatalf("the submission answered %+v, want it recorded", got)
		}
		if got := logged(); strings.Contains(got, compactionNotice) {
			t.Errorf("a submission re-announced the rewrite the boot had already reported:\n%s", got)
		}
	})
}

// compactedAtBoot hands back an open store whose BOOT rewrote its log, which is
// the state a daemon is in when cmd/baton has already written the line.
//
// The log is padded with copies of a record the store itself wrote — repeats of
// one entry, which a rewrite drops — so the fixture never has to know the log's
// format. Eight mebibytes is score's own boot threshold; it is named here in one
// place, and the fixture asserts the rewrite actually happened rather than
// trusting the arithmetic.
func compactedAtBoot(t *testing.T) *score.Store {
	t.Helper()
	// Opened by hand rather than through scoreStoreTuned, because this one has to
	// be CLOSED before the directory is reopened below and that helper's store
	// lives to the end of the test.
	dir := t.TempDir()
	first, err := score.Open(dir, score.Policy{})
	if err != nil {
		t.Fatalf("seed the fixture store: %v", err)
	}
	e := seed(t, first, "the fleet asks before it force-pushes")
	if err := first.Reinforce(e.Id, score.SourceAgent); err != nil {
		t.Fatalf("seed a repeat: %v", err)
	}
	first.Close()

	path := filepath.Join(dir, "score-events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the fixture log: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	repeat := lines[len(lines)-1] + "\n"

	const past = 8<<20 + 1<<16
	var pad strings.Builder
	pad.Grow(past + len(repeat))
	for pad.Len() < past-len(raw) {
		pad.WriteString(repeat)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open the fixture log: %v", err)
	}
	if _, err := f.WriteString(pad.String()); err != nil {
		t.Fatalf("pad the fixture log: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close the fixture log: %v", err)
	}

	st, err := score.Open(dir, score.Policy{})
	if err != nil {
		t.Fatalf("reopen the padded fixture store: %v", err)
	}
	t.Cleanup(st.Close)
	if h := st.Health(); h.Compactions == 0 {
		t.Fatalf("the padded fixture did not compact at boot: health = %+v", h)
	}
	return st
}
