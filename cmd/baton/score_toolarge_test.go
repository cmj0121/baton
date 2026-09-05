package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/score"
)

// TestOpenScoreSurvivesAStoreFileTooBigToRead is the end this daemon needed from
// internal/score's read cap: a store file too large to load must cost the fleet
// its MEMORY, not its daemon.
//
// #56's own note is what makes this the one that matters: an oversized event log
// was "the one boot failure that cannot heal, because the process that dies is
// the process that would have shrunk the file. Every later start repeats it."
// This branch reproduced it at a quarter of the measured size — a 256 MiB log
// took Open to 607 MiB of live heap. Here the same condition arrives as an
// ordinary Open failure, which is a path this file already had: no store, a
// reason the three score surfaces report, and a fleet that comes up.
func TestOpenScoreSurvivesAStoreFileTooBigToRead(t *testing.T) {
	dir := t.TempDir()
	// Sparse: the size is the whole of what the cap reads, so nothing is written.
	f, err := os.Create(filepath.Join(dir, "score.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	logged := captureBootLog(t)
	st, reason := openScore(config.ScoreConfig{Dir: dir}, score.Policy{}, scoreOpenTimeout)
	if st != nil {
		st.Close()
		t.Fatal("a gigabyte of score.md was opened as a store")
	}
	// The reason is what score.status, score.submit and a refused correction all
	// report. It has to name the file, because the remedy is to move that file.
	if !strings.Contains(reason, "score.md") {
		t.Errorf("reason = %q; without the file in it nobody knows what to move", reason)
	}
	if !strings.Contains(reason, "too large") {
		t.Errorf("reason = %q, want it to say the file is too large", reason)
	}
	// And the person running the daemon hears it on the line they grep for a
	// store that has stopped working.
	if got := logged(); !strings.Contains(got, "score store open failed") {
		t.Errorf("the daemon went on without its memory and said nothing:\n%s", got)
	}
}
