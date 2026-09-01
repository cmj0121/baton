package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/score"
)

// This file is the boot bound on the fleet memory (#46 R7). The listener is
// already bound when openScore runs and Serve has not started, so a score.dir
// that does not answer leaves the daemon holding a listening socket it will
// never serve — clients connect and hang, with nothing in the log, because the
// line that would explain it comes after the call that is stuck.

// TestWaitForStoreGivesUpAndTakesTheClaimBackWithIt is the firing direction, and
// the half of it that is easy to get wrong: the store the daemon abandoned must
// not keep the directory's single-writer claim, or the next restart is refused
// by a store nothing in the process can reach.
// It also has to be waitForStore's Close that releases the claim, and not the
// GC's. The abandoned store's *os.File carries a finalizer that closes the
// descriptor and drops the flock with it, so a run that collects at the right
// moment passes with the Close deleted — measured at 8 of 10. The test therefore
// keeps its own reference to the store the open produced: while `abandoned` is
// reachable the runtime cannot finalize anything, and the only remaining path
// from a held claim to a free one is the line under test. The handover is
// through `returned`, so the write and the read are ordered.
// And it is run EIGHT times, because the alternative it rules out does not fail
// every time. Buffer `done` and both select arms are ready once the caller has
// gone, so the goroutine keeps the store on a coin toss: one trial tells the two
// designs apart about half the time, which is a test that reports a leak as a
// flake. Eight trials leave the wrong shape a 0.4% chance of passing.
func TestWaitForStoreGivesUpAndTakesTheClaimBackWithIt(t *testing.T) {
	for trial := range 8 {
		dir := t.TempDir()
		release := make(chan struct{})
		returned := make(chan struct{})
		var abandoned *score.Store

		st, err := waitForStore(20*time.Millisecond, func() (*score.Store, error) {
			<-release // the open that has not answered yet
			s, e := score.Open(dir, score.Policy{})
			abandoned = s
			close(returned)
			return s, e
		})
		if !errors.Is(err, errScoreOpenTimeout) {
			t.Fatalf("waitForStore = %v for an open that had not returned at all, want the timeout", err)
		}
		if st != nil {
			t.Fatalf("waitForStore = %v, want no store when it gave up", st)
		}

		// The open now completes, into a caller that has gone.
		close(release)
		<-returned
		// A second is far more than the handful of microseconds between the open
		// returning and the goroutine's select, and it is the whole cost of a trial
		// that fails — which is why this one names its own deadline rather than
		// taking awaitClaim's five.
		if !claimIsFree(dir, time.Second) {
			t.Fatalf("trial %d: the abandoned store is still holding %s", trial, dir)
		}
		runtime.KeepAlive(abandoned)
	}
}

// TestWaitForStoreKeepsAnOpenThatAnswers is the silent direction. A bound that
// fires on a store which merely took a moment is the expensive mistake here: the
// fleet loses its memory until someone restarts the daemon, and the only symptom
// is a brief with no score block in it.
func TestWaitForStoreKeepsAnOpenThatAnswers(t *testing.T) {
	dir := t.TempDir()
	st, err := waitForStore(scoreOpenTimeout, func() (*score.Store, error) {
		return score.Open(dir, score.Policy{})
	})
	if err != nil || st == nil {
		t.Fatalf("waitForStore = (%v, %v), want the store it was handed within %s", st, err, scoreOpenTimeout)
	}
	st.Close()
}

// TestWaitForStoreReportsTheOpensOwnFailure keeps the bound from swallowing what
// it is not for: an open that FAILS in time is a failure, not a timeout, and the
// two reach the operator as different sentences.
func TestWaitForStoreReportsTheOpensOwnFailure(t *testing.T) {
	want := errors.New("score: the directory is held by another baton daemon")
	st, err := waitForStore(scoreOpenTimeout, func() (*score.Store, error) {
		return nil, want
	})
	if st != nil || !errors.Is(err, want) {
		t.Fatalf("waitForStore = (%v, %v), want the open's own error reported in time", st, err)
	}
	// And it is not mistaken for the deadline, which openScore tells apart with
	// exactly this test and answers with a different sentence.
	if errors.Is(err, errScoreOpenTimeout) {
		t.Errorf("an open that failed in time reads as a timeout: %v", err)
	}
}

// TestOpenScoreSaysWhenItRanOutOfTime pins the reason a timed-out open hands the
// server, which is what score.status and a refused score.submit then say. It has
// to name the directory: "score is not running" without the path leaves an
// operator with two daemons and two possible mounts to guess between.
func TestOpenScoreSaysWhenItRanOutOfTime(t *testing.T) {
	dir := t.TempDir()
	_, err := waitForStore(0, func() (*score.Store, error) {
		time.Sleep(50 * time.Millisecond)
		return score.Open(dir, score.Policy{})
	})
	if !errors.Is(err, errScoreOpenTimeout) {
		t.Fatalf("a zero deadline waited: %v", err)
	}
	// The sentence openScore builds on that branch, checked here rather than
	// through openScore itself, which cannot be made to hang without a real
	// filesystem that does.
	reason := timedOutReason(scoreOpenTimeout, dir)
	if !strings.Contains(reason, dir) || !strings.Contains(reason, scoreOpenTimeout.String()) {
		t.Errorf("timeout reason = %q, want it to name both %s and %s", reason, dir, scoreOpenTimeout)
	}
}

// TestOpenScoreSaysSoWhenItGivesUp drives openScore's own timeout branch, which
// nothing reached: the whole arm was dead code to the suite, so `return nil, ""`
// survived, deleting its log line survived, and so did widening the wait to an
// hour. It is reachable now because the deadline is a parameter — a zero one
// gives up on an open that has not started, without needing a filesystem that
// hangs.
func TestOpenScoreSaysSoWhenItGivesUp(t *testing.T) {
	dir := t.TempDir()
	logged := captureBootLog(t)

	st, reason := openScore(config.ScoreConfig{Dir: dir}, score.Policy{}, 0)
	if st != nil {
		t.Fatalf("openScore returned a store past its own deadline: %v", st)
	}
	// The reason is what score.status, score.submit and a refused correction all
	// report. Empty leaves all three saying "disabled" about a directory that is
	// merely slow.
	if !strings.Contains(reason, dir) {
		t.Errorf("the timeout reason is %q; without the directory in it an operator running two "+
			"fleets under BATON_SOCK has two daemons and two mounts to guess between", reason)
	}
	// And the operator's own surface. The reply goes to whoever asked; the line is
	// the only party the person running the daemon ever hears from.
	if got := logged(); !strings.Contains(got, "score store did not open in time") {
		t.Errorf("the daemon gave up on its memory and said nothing:\n%s", got)
	}
	// The abandoned open is still running, and it creates the store's files on its
	// way to being closed. Left alone it writes them into a directory t.TempDir is
	// already removing, which fails the test on a timer rather than on anything it
	// asserts. Waiting for the claim to come free is waiting for that goroutine to
	// be done with the directory.
	awaitClaim(t, dir)
}

// awaitClaim blocks until dir's single-writer claim can be taken, which is the
// only observable end of an open this package abandoned.
func awaitClaim(t *testing.T, dir string) {
	t.Helper()
	if !claimIsFree(dir, 5*time.Second) {
		t.Fatalf("the abandoned open never released %s", dir)
	}
}

// claimIsFree reports whether dir's single-writer claim came free within budget.
// It goes through waitFor, which is this package's own polling loop and what
// score_policy_test.go already uses — the two copies of this loop in this file
// were re-implementing it, differing only in the deadline each wanted.
func claimIsFree(dir string, budget time.Duration) bool {
	const gap = 5 * time.Millisecond
	return waitFor(func() bool {
		s, err := score.Open(dir, score.Policy{})
		if err != nil {
			return false
		}
		s.Close()
		return true
	}, int(budget/gap), gap)
}

// TestScoreOpenTimeoutIsBoundedBothWays is R6's lesson applied to the third
// number this issue introduces. A timeout is only ever wrong in one of two
// directions and each has its own cost, so each gets its own assertion.
//
// TOO SHORT is the expensive one, and it is measured against a REAL store. It
// used to be measured against an open of an empty t.TempDir — 3.9-8.8 ms of
// mkdir, flock and two stats, with no log to replay — so the floor demanded
// about 40-90 ms and a bound of 500 ms passed it 8 times out of 8, well under
// the 461-512 ms a 51.9 MB store actually boots in. The fixture below is a store
// with a real six-megabyte log — under the 8 MiB past which a boot would rewrite
// it, so what is measured is a plain replay — and it boots in about 85 ms here.
//
// TOO LONG costs a person staring at a terminal. The upper bound is loose on
// purpose — the point is that a mutation to minutes fails here, not that 10s is
// provably the best number in the range.
func TestScoreOpenTimeoutIsBoundedBothWays(t *testing.T) {
	dir := realisticStore(t)
	start := time.Now()
	st, reason := openScore(config.ScoreConfig{Dir: dir}, score.Policy{}, scoreOpenTimeout)
	healthy := time.Since(start)
	if st == nil {
		t.Fatalf("openScore on a realistic store returned none: %s", reason)
	}
	t.Cleanup(st.Close)

	if scoreOpenTimeout < replayFloorFactor*healthy {
		t.Errorf("scoreOpenTimeout is %s and a realistic store's boot took %s; the bound must not be "+
			"able to fire on a store that was merely slow, because the fleet then has no memory "+
			"until a restart", scoreOpenTimeout, healthy)
	}
	if scoreOpenTimeout > 30*time.Second {
		t.Errorf("scoreOpenTimeout is %s; past half a minute the daemon is indistinguishable from hung "+
			"to whoever is waiting on it, which is the failure the bound exists to remove", scoreOpenTimeout)
	}
}

// TestTheBootSaysWhatCompactionDidToTheStore covers the three lines
// logScoreBoot writes, each in both directions. Two of them need a filesystem
// failing in a particular way to reach through openScore, which is why the
// function takes what it says rather than a store.
func TestTheBootSaysWhatCompactionDidToTheStore(t *testing.T) {
	const dir = "/home/op/.baton"

	// A rewrite that RAN. Nothing was submitted and no config changed, and the
	// working set a panel is served can still come back in a different order,
	// because compaction re-spaces recency. `compacted=310` on its own connects
	// none of that to what the agents see.
	t.Run("a rewrite that ran warns that recency was re-spaced", func(t *testing.T) {
		logged := captureBootLog(t)
		logScoreBoot(dir, 10, score.Delta{}, score.Health{Compacted: 310, LogBefore: 8668049, LogAfter: 24865})
		got := logged()
		for _, want := range []string{
			`"level":"warn"`, "recency spacing is not", `"compacted":310`, `"entries":10`,
			`"log_before":8668049`, `"log_after":24865`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the compaction notice is missing %s:\n%s", want, got)
			}
		}
	})

	// And the silent direction, which is the one that keeps the line worth
	// reading: every boot warning an operator can ignore is a warning they will.
	t.Run("a boot that rewrote nothing says nothing about reordering", func(t *testing.T) {
		logged := captureBootLog(t)
		logScoreBoot(dir, 10, score.Delta{Admitted: 3}, score.Health{})
		if got := logged(); strings.Contains(got, "recency spacing") {
			t.Errorf("a boot that left the log alone warned about a rewrite:\n%s", got)
		}
	})

	t.Run("a rewrite that failed carries its own words", func(t *testing.T) {
		logged := captureBootLog(t)
		logScoreBoot(dir, 10, score.Delta{}, score.Health{
			CompactionFailures: 1,
			CompactionError:    "write score-events.jsonl.tmp: no space left on device",
		})
		got := logged()
		if !strings.Contains(got, "no space left on device") {
			t.Errorf("the operator gets the counter and not the reason:\n%s", got)
		}
		if !strings.Contains(got, `"level":"warn"`) {
			t.Errorf("a rewrite the store could not make was not warned about:\n%s", got)
		}
	})

	t.Run("a rewrite that landed is not reported as a failure", func(t *testing.T) {
		logged := captureBootLog(t)
		logScoreBoot(dir, 10, score.Delta{}, score.Health{Compacted: 310, LogBefore: 900, LogAfter: 100})
		if got := logged(); strings.Contains(got, "could not rewrite") {
			t.Errorf("a rewrite that landed was reported as one that did not:\n%s", got)
		}
	})

	// The counters line #38's lifecycle asks for, and its own silent half: a boot
	// that changed nothing must not write one.
	t.Run("a boot that changed nothing writes no counters", func(t *testing.T) {
		logged := captureBootLog(t)
		logScoreBoot(dir, 0, score.Delta{}, score.Health{})
		if got := logged(); got != "" {
			t.Errorf("an uneventful boot logged:\n%s", got)
		}
	})

	t.Run("a boot that recovered something writes them", func(t *testing.T) {
		logged := captureBootLog(t)
		logScoreBoot(dir, 4, score.Delta{Admitted: 4}, score.Health{})
		if got := logged(); !strings.Contains(got, "score recovered") || !strings.Contains(got, `"admitted":4`) {
			t.Errorf("the boot pass changed the operator's files and said nothing:\n%s", got)
		}
	})
}

// realisticStore leaves a directory holding a store with a six-megabyte event
// log — thirty thousand entries, under the 8 MiB past which a boot would rewrite
// it — and returns the directory, closed and ready to open.
//
// The log is grown through score.md rather than through thirty thousand Submit
// calls, because a submission is an fsync and the fixture would then cost thirty
// thousand of them. One reconcile admits the whole file in one batched append,
// which is the same log either way.
func realisticStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var md strings.Builder
	for i := range 30000 {
		fmt.Fprintf(&md, "- the fleet keeps the build green and asks before it force-pushes, note %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "score.md"), []byte(md.String()), 0o600); err != nil {
		t.Fatalf("write the fixture score.md: %v", err)
	}
	st, err := score.Open(dir, score.Policy{})
	if err != nil {
		t.Fatalf("build the fixture store: %v", err)
	}
	st.Close()
	return dir
}

// captureBootLog redirects the package's global zerolog for the rest of the test
// and hands back a reader of what was written. It swaps a package global, so it
// is safe only while this package's tests run one at a time — no test here calls
// t.Parallel().
//
// The buffer is LOCKED, because a test here reads it while a daemon goroutine is
// still logging into it, and a bare bytes.Buffer between the two is a real data
// race rather than a theoretical one.
func captureBootLog(t *testing.T) func() string {
	t.Helper()
	buf := &lockedBuffer{}
	saved := log.Logger
	log.Logger = zerolog.New(buf)
	t.Cleanup(func() { log.Logger = saved })
	return buf.String
}

// lockedBuffer is a bytes.Buffer a writer and a reader may hold at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
