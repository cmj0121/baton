package usage

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLocalFetchContextCancelled: a cancelled context stops the walk with the
// context error once it reaches a fresh (non-stale) transcript, and Fetch
// surfaces that error rather than swallowing it as a missing dir.
func TestLocalFetchContextCancelled(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "proj1", "sess1", fixedNow,
		assistantLine("msg-1", "req-1", "claude-opus-4-8", fixedNow.Add(-time.Hour), 100, 50, 0, 0, 0),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newLocal(filepath.Join(root, "projects")).Fetch(ctx)
	if err == nil {
		t.Fatal("cancelled context should surface an error from Fetch")
	}
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestScanTranscriptOpenError: scanning an unopenable path leaves the snapshot
// untouched (the file is skipped, not fatal).
func TestScanTranscriptOpenError(t *testing.T) {
	sc := newScan(fixedNow)
	sc.transcript(filepath.Join(t.TempDir(), "does-not-exist.jsonl"), "sess1")
	if !sc.snapshot(sc.cutoff).Empty() {
		t.Fatalf("open error should leave snapshot empty: %+v", sc.snapshot(sc.cutoff))
	}
}

// TestFoldEntryBadJSON: a line that carries the "usage" substring but is not
// valid JSON is ignored without panicking or mutating the snapshot.
func TestFoldEntryBadJSON(t *testing.T) {
	sc := newScan(fixedNow)
	sc.fold([]byte(`{ this is not "usage" json `), "sess1")
	if !sc.snapshot(sc.cutoff).Empty() {
		t.Fatalf("bad JSON should be ignored: %+v", sc.snapshot(sc.cutoff))
	}
}

// TestFoldEntryNilUsage: a well-formed line whose message has no usage block
// (usage:null) is ignored — the "usage" substring gate can pass on such lines.
func TestFoldEntryNilUsage(t *testing.T) {
	sc := newScan(fixedNow)
	line := `{"type":"assistant","timestamp":"2026-07-08T09:00:00Z","message":{"id":"m","usage":null}}`
	sc.fold([]byte(line), "sess1")
	if !sc.snapshot(sc.cutoff).Empty() {
		t.Fatalf("nil usage should be ignored: %+v", sc.snapshot(sc.cutoff))
	}
}

// TestFoldEntryBadTimestamp: a usage line with an unparseable timestamp is
// filtered out (the time.Parse error path), leaving the snapshot empty.
func TestFoldEntryBadTimestamp(t *testing.T) {
	sc := newScan(fixedNow)
	line := `{"type":"assistant","timestamp":"not-a-time","message":{"id":"m","model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":50}}}`
	sc.fold([]byte(line), "sess1")
	if !sc.snapshot(sc.cutoff).Empty() {
		t.Fatalf("bad timestamp should be ignored: %+v", sc.snapshot(sc.cutoff))
	}
}

// TestFoldEntryNoCacheCreationTier: when a message's usage carries a
// cache_creation_input_tokens total but no cache_creation tier breakdown, the
// whole write is priced at the 5-minute rate rather than dropped.
func TestFoldEntryNoCacheCreationTier(t *testing.T) {
	sc := newScan(startOfDay(fixedNow))
	ts := fixedNow.Add(-time.Hour).Format(time.RFC3339)
	line := `{"type":"assistant","requestId":"r","timestamp":"` + ts + `","message":{"id":"m","model":"claude-opus-4-8","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":400}}}`
	sc.fold([]byte(line), "sess1")

	if sc.snapshot(sc.cutoff).CacheWrite != 400 {
		t.Fatalf("CacheWrite = %d, want 400", sc.snapshot(sc.cutoff).CacheWrite)
	}
	// opus input rate $5/MTok, 5-minute write multiplier 1.25.
	want := 400.0 / 1e6 * 5 * 1.25
	if math.Abs(sc.snapshot(sc.cutoff).CostUSD-want) > 1e-9 {
		t.Fatalf("cost = %v, want %v (whole write priced at 5m rate)", sc.snapshot(sc.cutoff).CostUSD, want)
	}
}

// TestFoldEntryNoDedupKeys: a usage line with neither a message id nor a
// requestId skips the dedup map and is still counted (the empty-key branch).
func TestFoldEntryNoDedupKeys(t *testing.T) {
	sc := newScan(startOfDay(fixedNow))
	ts := fixedNow.Add(-time.Hour).Format(time.RFC3339)
	line := `{"type":"assistant","timestamp":"` + ts + `","message":{"model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":5}}}`
	sc.fold([]byte(line), "sess1")
	sc.fold([]byte(line), "sess1")

	if sc.snapshot(sc.cutoff).Input != 20 || sc.snapshot(sc.cutoff).Output != 10 {
		t.Fatalf("keyless lines should each count: in=%d out=%d, want 20/10", sc.snapshot(sc.cutoff).Input, sc.snapshot(sc.cutoff).Output)
	}
	if len(sc.seen) != 0 {
		t.Fatalf("keyless lines should not populate the dedup map, got %d entries", len(sc.seen))
	}
}

// TestClaudeProjectsDirConfigOverride: CLAUDE_CONFIG_DIR wins and its /projects
// child is returned.
func TestClaudeProjectsDirConfigOverride(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/custom/cfg")
	if got := claudeProjectsDir(); got != filepath.Join("/custom/cfg", "projects") {
		t.Fatalf("claudeProjectsDir() = %q, want /custom/cfg/projects", got)
	}
}

// TestClaudeProjectsDirHome: with no override, the ~/.claude/projects path under
// the home directory is used.
func TestClaudeProjectsDirHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir on this host")
	}
	if got := claudeProjectsDir(); got != filepath.Join(home, ".claude", "projects") {
		t.Fatalf("claudeProjectsDir() = %q, want %q", got, filepath.Join(home, ".claude", "projects"))
	}
}

// TestClaudeProjectsDirNoHome: when neither the override nor a home directory is
// available, the relative .claude/projects fallback is used.
func TestClaudeProjectsDirNoHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	// Clearing HOME (and, on some platforms, its cousins) makes os.UserHomeDir fail.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("UserHomeDir still resolves without HOME on this host")
	}
	if got := claudeProjectsDir(); got != filepath.Join(".claude", "projects") {
		t.Fatalf("claudeProjectsDir() = %q, want .claude/projects", got)
	}
}
