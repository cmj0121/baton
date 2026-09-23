package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A walk that meets a directory and a log it cannot read skips both and counts
// them, so the under-count is said once with a number rather than not at all;
// what it can read is still summed, and a root that does not exist is no gap.
func TestTheWalkCountsWhatItCouldNotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through a 000 mode; nothing here would be unreadable")
	}
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	line := assistantLine("m1", "req-m1", "claude-opus-4-8", ts, 100, 0, 0, 0, 0)
	writeTranscript(t, root, "ok", "s1", fixedNow, line)
	writeTranscript(t, root, "shut", "s2", fixedNow, assistantLine("m2", "req-m2", "claude-opus-4-8", ts, 7, 0, 0, 0, 0))
	writeTranscript(t, root, "locked", "s3", fixedNow, assistantLine("m3", "req-m3", "claude-opus-4-8", ts, 9, 0, 0, 0, 0))
	shutDir := filepath.Join(root, "projects", "shut")
	lockedLog := filepath.Join(root, "projects", "locked", "s3.jsonl")
	for _, c := range []struct {
		path string
		mode os.FileMode
	}{{shutDir, 0o000}, {lockedLog, 0o000}} {
		if err := os.Chmod(c.path, c.mode); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = os.Chmod(shutDir, 0o755)
		_ = os.Chmod(lockedLog, 0o644)
	})

	p := newLocal(filepath.Join(root, "projects"))
	sc, err := p.walk(context.Background(), startOfDay(fixedNow), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := sc.snapshot(sc.cutoff).Input; got != 100 {
		t.Errorf("input = %d, want 100 — the readable log summed, the others not", got)
	}
	if sc.unreadable != 2 {
		t.Errorf("unreadable = %d, want 2 — the shut directory and the locked log", sc.unreadable)
	}

	missing := newLocal(filepath.Join(root, "nowhere"))
	sc, err = missing.walk(context.Background(), startOfDay(fixedNow), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if sc.unreadable != 0 {
		t.Errorf("a missing root: unreadable = %d, want 0 — a vendor never run is no gap", sc.unreadable)
	}
}
