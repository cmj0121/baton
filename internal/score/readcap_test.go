package score

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the READ bound's. compactAtBytes bounds how large this package
// lets the event log GROW; nothing bounded how large a file it was willing to
// READ, and #56's own note says where that ends — a 1.217 GB log OOM-killed
// inside Open, "the one boot failure that cannot heal, because the process that
// dies is the process that would have shrunk the file". Reproduced on this branch
// a quarter of the way there: a 256 MiB log took Open to 607 MiB of live heap and
// 1161 MiB allocated, and returned no error at all.

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

// TestOversizedEventLogIsRefusedNotRead: an event log past the cap fails Open
// with a reason naming the file, rather than being read into memory.
//
// Failing Open is the RIGHT outcome and not a compromise: cmd/baton's openScore
// already boots the daemon without a store when Open fails and carries the reason
// to score.status, score.submit and the boot log (#38's lifecycle — corrupt score
// files never block the fleet). So the refusal turns an OOM-kill on every start
// into a fleet that runs and says which file is in the way.
func TestOversizedEventLogIsRefusedNotRead(t *testing.T) {
	dir := t.TempDir()
	sparseFile(t, filepath.Join(dir, "score-events.jsonl"), maxScoreFileBytes+1)

	s, err := Open(dir, Policy{})
	if s != nil {
		s.Close()
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Open err = %v, want ErrFileTooLarge", err)
	}
	if !strings.Contains(err.Error(), "score-events.jsonl") {
		t.Errorf("the refusal should name the file, got %q", err)
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
