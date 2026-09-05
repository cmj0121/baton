package usage

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// paddedAssistantLine is an assistant message of at least n bytes: a real usage
// block with a field nothing reads padded out to size. It is what an oversized
// transcript line actually looks like — valid JSON carrying real numbers, and only
// its size wrong — so the only thing that can stop it being counted is the cap.
func paddedAssistantLine(id string, ts time.Time, tokens int64, n int) string {
	line := assistantLine(id, "req-"+id, "claude-opus-4-8", ts, tokens, 0, 0, 0, 0)
	pad := fmt.Sprintf(`{"pad":%q,`, strings.Repeat("p", n))
	return pad + strings.TrimPrefix(line, "{")
}

// TestOversizedTranscriptLineIsDropped feeds the scanner a message past the cap
// and checks its tokens are not counted, that the lines either side of it still
// are, and that the drop is recorded.
//
// The resync half is the point of dropping rather than truncating: a transcript is
// newline-framed, so one bad record must cost one record. Driven unbounded, a
// single 256 MiB line in a live transcript cost the daemon 519 MiB inside one
// Fetch — and Fetch runs on a thirty-second poll.
func TestOversizedTranscriptLineIsDropped(t *testing.T) {
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	writeTranscript(t, root, "proj", "sess", fixedNow,
		assistantLine("before", "req-before", "claude-opus-4-8", ts, 100, 0, 0, 0, 0),
		paddedAssistantLine("huge", ts, 1_000_000, maxTranscriptLine+1),
		assistantLine("after", "req-after", "claude-opus-4-8", ts, 100, 0, 0, 0, 0),
	)

	sc := newScan(startOfDay(fixedNow), fixedNow)
	sc.transcript(filepath.Join(root, "projects", "proj", "sess.jsonl"), "sess")

	snap := sc.snapshot(sc.cutoff)
	if snap.Input != 200 {
		t.Fatalf("input = %d, want 200 — the two ordinary messages and not the oversized one", snap.Input)
	}
	if sc.oversized != 1 {
		t.Errorf("oversized = %d, want 1", sc.oversized)
	}
}

// TestLargeTranscriptLineIsRead is the other half, and the reason the cap is
// 16 MiB rather than bufio.Scanner's 64 KiB: a line carrying a pasted image is
// legitimate and megabytes long, and must still be counted.
func TestLargeTranscriptLineIsRead(t *testing.T) {
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	writeTranscript(t, root, "proj", "sess", fixedNow,
		paddedAssistantLine("pasted-image", ts, 7, 8<<20),
	)

	sc := newScan(startOfDay(fixedNow), fixedNow)
	sc.transcript(filepath.Join(root, "projects", "proj", "sess.jsonl"), "sess")

	if snap := sc.snapshot(sc.cutoff); snap.Input != 7 {
		t.Fatalf("input = %d, want 7 — an 8 MiB line is a pasted image, not an attack", snap.Input)
	}
	if sc.oversized != 0 {
		t.Errorf("oversized = %d, want 0", sc.oversized)
	}
}

// TestOversizedTranscriptLineAtEOF: a file whose last line runs past the cap and
// never ends in a newline must not hang, loop, or be counted.
func TestOversizedTranscriptLineAtEOF(t *testing.T) {
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	// writeTranscript terminates every line, so the unterminated tail is written as
	// part of the last one.
	writeTranscript(t, root, "proj", "sess", fixedNow,
		assistantLine("before", "req-before", "claude-opus-4-8", ts, 100, 0, 0, 0, 0)+"\n"+
			paddedAssistantLine("huge", ts, 1_000_000, maxTranscriptLine+1),
	)

	sc := newScan(startOfDay(fixedNow), fixedNow)
	sc.transcript(filepath.Join(root, "projects", "proj", "sess.jsonl"), "sess")

	if snap := sc.snapshot(sc.cutoff); snap.Input != 100 {
		t.Fatalf("input = %d, want 100", snap.Input)
	}
	if sc.oversized != 1 {
		t.Errorf("oversized = %d, want 1", sc.oversized)
	}
}
