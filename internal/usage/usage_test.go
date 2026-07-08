package usage

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixedNow pins "today" so transcript timestamps and the day boundary are
// deterministic regardless of the machine clock.
var fixedNow = time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)

// assistantLine builds one transcript JSONL line for an assistant message with a
// usage block, at the given timestamp.
func assistantLine(id, reqID, model string, ts time.Time, in, out, cacheRead, write5m, write1h int64) string {
	return fmt.Sprintf(`{"type":"assistant","requestId":%q,"timestamp":%q,"message":{"id":%q,"model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d}}}}`,
		reqID, ts.Format(time.RFC3339), id, model, in, out, cacheRead, write5m+write1h, write5m, write1h)
}

// writeTranscript writes lines to <dir>/projects/<project>/<session>.jsonl and
// stamps the file's mtime, which the scanner uses to skip stale files.
func writeTranscript(t *testing.T, root, project, session string, mtime time.Time, lines ...string) {
	t.Helper()
	dir := filepath.Join(root, "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, session+".jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func newLocal(dir string) *LocalProvider {
	return &LocalProvider{dir: dir, now: func() time.Time { return fixedNow }}
}

// TestLocalFetchAggregates: today's assistant messages are summed, a duplicate
// (same id+requestId) is counted once, a yesterday-dated line is filtered out by
// timestamp, and a line with no usage is ignored.
func TestLocalFetchAggregates(t *testing.T) {
	root := t.TempDir()
	today := fixedNow.Add(-1 * time.Hour)
	yesterday := fixedNow.AddDate(0, 0, -1)

	writeTranscript(t, root, "proj1", "sess1", fixedNow,
		assistantLine("msg-1", "req-1", "claude-opus-4-8", today, 1000, 500, 200, 500, 1000),
		assistantLine("msg-1", "req-1", "claude-opus-4-8", today, 1000, 500, 200, 500, 1000), // duplicate, ignored
		assistantLine("msg-2", "req-2", "claude-opus-4-8", yesterday, 9999, 9999, 0, 0, 0),   // stale, ignored
		`{"type":"user","timestamp":"2026-07-08T09:30:00Z","message":{"role":"user","content":"hi"}}`,
	)

	snap, err := newLocal(filepath.Join(root, "projects")).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Input != 1000 || snap.Output != 500 || snap.CacheRead != 200 || snap.CacheWrite != 1500 {
		t.Fatalf("tokens: in=%d out=%d read=%d write=%d, want 1000/500/200/1500",
			snap.Input, snap.Output, snap.CacheRead, snap.CacheWrite)
	}
	if snap.TotalTokens() != 3200 {
		t.Fatalf("total tokens = %d, want 3200", snap.TotalTokens())
	}
	// opus in=$5/out=$25 per MTok: read 0.1x, 5m write 1.25x, 1h write 2x of input.
	want := 1000.0/1e6*5 + 500.0/1e6*25 + 200.0/1e6*5*0.1 + 500.0/1e6*5*1.25 + 1000.0/1e6*5*2.0
	if math.Abs(snap.CostUSD-want) > 1e-9 {
		t.Fatalf("cost = %v, want %v", snap.CostUSD, want)
	}
}

// TestLocalSkipsStaleFiles: a file whose mtime predates today is skipped whole,
// even if it (contrived) holds a today-dated line — the append-only optimisation.
func TestLocalSkipsStaleFiles(t *testing.T) {
	root := t.TempDir()
	yesterday := fixedNow.AddDate(0, 0, -1)
	writeTranscript(t, root, "proj1", "old", yesterday,
		assistantLine("msg-9", "req-9", "claude-opus-4-8", fixedNow.Add(-time.Hour), 1000, 1000, 0, 0, 0),
	)
	snap, err := newLocal(filepath.Join(root, "projects")).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Empty() {
		t.Fatalf("stale file was scanned: %+v", snap)
	}
}

// TestLocalMissingDir: a projects root that does not exist is zero usage, no error.
func TestLocalMissingDir(t *testing.T) {
	snap, err := newLocal(filepath.Join(t.TempDir(), "nope", "projects")).Fetch(context.Background())
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if !snap.Empty() {
		t.Fatalf("missing dir should be empty: %+v", snap)
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		snap Snapshot
		want string
	}{
		{Snapshot{}, ""},
		{Snapshot{Input: 512}, "512 tok"},
		{Snapshot{Input: 1_200_000, CostUSD: 12.345}, "1.2M tok · ≈$12.35 API"},
		{Snapshot{Input: 9340, CostUSD: 0}, "9.3K tok"},
	}
	for _, c := range cases {
		if got := Format(c.snap); got != c.want {
			t.Errorf("Format(%+v) = %q, want %q", c.snap, got, c.want)
		}
	}
}

func TestPriceFor(t *testing.T) {
	cases := map[string]price{
		"claude-opus-4-8":   {in: 5, out: 25},
		"claude-haiku-4-5":  {in: 1, out: 5},
		"claude-sonnet-5":   {in: 3, out: 15},
		"claude-fable-5":    {in: 10, out: 50},
		"something-unknown": {in: 3, out: 15},
	}
	for model, want := range cases {
		if got := priceFor(model); got != want {
			t.Errorf("priceFor(%q) = %+v, want %+v", model, got, want)
		}
	}
}

func TestNewProviderSelection(t *testing.T) {
	t.Setenv(AdminKeyEnv, "")
	if p := NewProvider("auto"); p.Source() != "local" {
		t.Errorf("auto without key = %q, want local", p.Source())
	}
	if p := NewProvider("api"); p.Source() != "local" {
		t.Errorf("api without key falls back to %q, want local", p.Source())
	}
	t.Setenv(AdminKeyEnv, "sk-ant-admin01-test")
	if p := NewProvider("auto"); p.Source() != "api" {
		t.Errorf("auto with key = %q, want api", p.Source())
	}
	if p := NewProvider("local"); p.Source() != "local" {
		t.Errorf("explicit local = %q, want local", p.Source())
	}
}
