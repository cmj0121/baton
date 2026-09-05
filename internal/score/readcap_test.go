package score

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the READ bound's, and since the replay began streaming that bound
// covers score.md ALONE. #56's own note says where an unbounded whole-file read
// ends — a 1.217 GB log OOM-killed inside Open, "the one boot failure that cannot
// heal, because the process that dies is the process that would have shrunk the
// file" — and the cap this file was written for stopped that kill by refusing the
// read. What it could not stop was the refusal itself becoming permanent.
//
// The event log is no longer read whole, so there is nothing left for a cap on it
// to protect; see maxScoreFileBytes for why that half was removed rather than
// raised, and stream_test.go for the heal it was standing in the way of. score.md
// keeps the cap for the reasons it always had: it is held whole, it is re-read on
// every submission, and it is the one of the two a person edits by hand.

// legitimateStoreFile is the largest file these tests claim a healthy store has,
// written as a figure rather than as maxScoreFileBytes-minus-something so that a
// cap narrowed to where it bites is caught. It is compactAtBytes: the size this
// package itself decides an event log has to be rewritten at, so a store sitting
// on one is exactly a store in ordinary working order.
const legitimateStoreFile = compactAtBytes

// sparseFile makes a file of exactly n bytes without writing n bytes. Its
// contents are NULs, which is what a store file truncated by a crash or filled by
// an accident looks like — and the point is the SIZE, which is all the cap reads.
func sparseFile(t *testing.T, path string, n int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(n); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestAnOversizedEventLogIsNoLongerRefusedUnread is round 3's
// TestOversizedEventLogIsRefusedNotRead, inverted, and the inversion is this
// round's point. That refusal made the daemon boot instead of being OOM-killed,
// which was right and is kept — cmd/baton's openScore still boots the fleet
// without a store on any Open failure. What the refusal could NOT do is let the
// file heal: Open's boot compaction sits below the replay that was failing, so a
// log a byte past the cap was refused by every later start with the file
// untouched.
//
// This is the narrow claim — a file past the old cap is READ rather than refused.
// TestAnOversizedEventLogHealsItself is the wide one: read, compacted, and small
// again by the next boot.
func TestAnOversizedEventLogIsNoLongerRefusedUnread(t *testing.T) {
	dir := t.TempDir()
	sparseFile(t, filepath.Join(dir, "score-events.jsonl"), maxScoreFileBytes+1)

	s, err := Open(dir, Policy{})
	if err != nil {
		t.Fatalf("an event log past the old cap must open, got %v", err)
	}
	defer s.Close()
	// NULs are not records, so this file is damage and the store it yields is
	// empty. What matters is that it is OPEN, and that the next submission lands.
	if _, _, err := s.Submit("the fleet still remembers", Provenance{Source: SourceUser}); err != nil {
		t.Fatalf("submit after opening over an oversized log: %v", err)
	}
}

// TestOversizedMarkdownIsRefusedNotRead: score.md is the file read far more
// often — every submission reconciles against it — so the same cap covers it.
func TestOversizedMarkdownIsRefusedNotRead(t *testing.T) {
	dir := t.TempDir()
	sparseFile(t, filepath.Join(dir, "score.md"), maxScoreFileBytes+1)

	s, err := Open(dir, Policy{})
	if s != nil {
		s.Close()
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Open err = %v, want ErrFileTooLarge", err)
	}
	if !strings.Contains(err.Error(), "score.md") {
		t.Errorf("the refusal should name the file, got %q", err)
	}
}

// TestAnOrdinaryStoreFileIsStillRead is the other half, and the one that matters
// most: a cap that fired on a healthy store would cost a fleet its memory to
// protect it from nothing. The size here is compactAtBytes — the weight this
// package itself treats as the ordinary trigger to tidy up, not as trouble.
func TestAnOrdinaryStoreFileIsStillRead(t *testing.T) {
	dir := t.TempDir()
	rec := []byte(`{"id":"aaaaaaaa","event":"submitted","text":"an observation worth keeping","at":"2026-01-01T00:00:00Z","source":"user"}` + "\n")
	f, err := os.Create(filepath.Join(dir, "score-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	batch := make([]byte, 0, 1<<20)
	for len(batch)+len(rec) <= 1<<20 {
		batch = append(batch, rec...)
	}
	for written := 0; written < legitimateStoreFile; written += len(batch) {
		if _, err := f.Write(batch); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir, Policy{})
	if err != nil {
		t.Fatalf("a %d-byte event log should open, got %v", legitimateStoreFile, err)
	}
	defer s.Close()
	if s.Len() == 0 {
		t.Fatal("the log opened but nothing was replayed out of it")
	}
}
