package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/score"
)

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
