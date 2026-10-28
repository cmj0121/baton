package usage

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// codexLimitsEvent is a token_count event whose rate_limits carries the two
// slots given, each either a JSON object or "null".
func codexLimitsEvent(ts time.Time, primary, secondary string) string {
	return `{"timestamp":"` + ts.UTC().Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"token_count",` +
		`"info":null,"rate_limits":{"limit_id":"codex","primary":` + primary + `,"secondary":` + secondary +
		`,"plan_type":"prolite"}}}`
}

func codexSlot(used float64, minutes int, resets time.Time) string {
	return `{"used_percent":` + strconv.FormatFloat(used, 'f', -1, 64) + `,"window_minutes":` +
		strconv.Itoa(minutes) + `,"resets_at":` + strconv.FormatInt(resets.Unix(), 10) + `}`
}

func touch(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// codexLimitsNow is the wall clock, not a fixed date: writeRollout leaves each
// file's mtime at the real now, and the reader's horizon is measured from this.
var codexLimitsNow = time.Now().UTC().Truncate(time.Second)

// The acceptance fixture: a week in primary and a five-hour window in
// secondary, each landing under its own label with its own fraction and reset.
func TestCodexWindowsLabelsBothSlotsByLength(t *testing.T) {
	home := t.TempDir()
	weekReset := codexLimitsNow.Add(72 * time.Hour)
	fiveReset := codexLimitsNow.Add(2 * time.Hour)
	writeRollout(t, home, codexLimitsNow, "rollout-a.jsonl",
		codexMetaLine("a", "/p"),
		codexLimitsEvent(codexLimitsNow.Add(-time.Minute), codexSlot(12.5, 10080, weekReset), codexSlot(40, 300, fiveReset)))

	ws, ok := codexWindowsAt(context.Background(), home+"/sessions", codexLimitsNow)
	if !ok || len(ws) != 2 {
		t.Fatalf("windows = %+v, %v; want a 5h and a 7d", ws, ok)
	}
	if ws[0].Label != WindowFiveHour || ws[0].Fraction != 0.40 || !ws[0].ResetsAt.Equal(fiveReset) {
		t.Errorf("first window = %+v, want 5h at 0.40 resetting %s", ws[0], fiveReset)
	}
	if ws[1].Label != WindowWeek || ws[1].Fraction != 0.125 || !ws[1].ResetsAt.Equal(weekReset) {
		t.Errorf("second window = %+v, want 7d at 0.125 resetting %s", ws[1], weekReset)
	}
}

// A length baton has no column for is skipped, not forced into the nearest one.
func TestCodexWindowsSkipsAnUnknownLength(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, codexLimitsNow, "rollout-a.jsonl",
		codexLimitsEvent(codexLimitsNow, codexSlot(50, 1440, codexLimitsNow.Add(time.Hour)),
			codexSlot(10, 10080, codexLimitsNow.Add(time.Hour))))
	ws, ok := codexWindowsAt(context.Background(), home+"/sessions", codexLimitsNow)
	if !ok || len(ws) != 1 || ws[0].Label != WindowWeek {
		t.Fatalf("windows = %+v, %v; want only the 7d", ws, ok)
	}

	writeRollout(t, home, codexLimitsNow, "rollout-a.jsonl",
		codexLimitsEvent(codexLimitsNow, codexSlot(50, 1440, codexLimitsNow.Add(time.Hour)), "null"))
	if ws, ok := codexWindowsAt(context.Background(), home+"/sessions", codexLimitsNow); ok || len(ws) != 0 {
		t.Errorf("only an unknown length gave %+v, %v; want no reading", ws, ok)
	}
}

// A window whose own reset has passed has rolled over since codex stated it.
func TestCodexWindowsDropsAWindowPastItsReset(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, codexLimitsNow, "rollout-a.jsonl",
		codexLimitsEvent(codexLimitsNow.Add(-6*time.Hour), codexSlot(90, 300, codexLimitsNow.Add(-time.Minute)),
			codexSlot(20, 10080, codexLimitsNow.Add(time.Hour))))
	ws, ok := codexWindowsAt(context.Background(), home+"/sessions", codexLimitsNow)
	if !ok || len(ws) != 1 || ws[0].Label != WindowWeek {
		t.Fatalf("windows = %+v, %v; want the 5h dropped and the 7d kept", ws, ok)
	}
}

// `codex resume` appends to the rollout under the date the session began, so
// the file written last wins over a newer date in the path.
func TestCodexWindowsNewestByMtimeBeatsNewerDatePath(t *testing.T) {
	home := t.TempDir()
	reset := codexLimitsNow.Add(time.Hour)
	resumed := writeRollout(t, home, codexLimitsNow.AddDate(0, 0, -7), "rollout-old.jsonl",
		codexLimitsEvent(codexLimitsNow.Add(-2*time.Hour), codexSlot(70, 10080, reset), "null"))
	today := writeRollout(t, home, codexLimitsNow, "rollout-new.jsonl",
		codexLimitsEvent(codexLimitsNow.Add(-time.Hour), codexSlot(5, 10080, reset), "null"))
	touch(t, today, codexLimitsNow.Add(-time.Hour))
	touch(t, resumed, codexLimitsNow.Add(-time.Minute))

	ws, ok := codexWindowsAt(context.Background(), home+"/sessions", codexLimitsNow)
	if !ok || len(ws) != 1 || ws[0].Fraction != 0.70 {
		t.Fatalf("windows = %+v, %v; want the resumed rollout's 0.70", ws, ok)
	}
}

// Within one rollout the latest line's block is the reading.
func TestCodexWindowsTakesTheNewestLineInAFile(t *testing.T) {
	home := t.TempDir()
	reset := codexLimitsNow.Add(time.Hour)
	writeRollout(t, home, codexLimitsNow, "rollout-a.jsonl",
		codexLimitsEvent(codexLimitsNow.Add(-2*time.Minute), codexSlot(30, 10080, reset), "null"),
		codexLimitsEvent(codexLimitsNow.Add(-time.Minute), codexSlot(31, 10080, reset), "null"),
		codexLimitsEvent(codexLimitsNow.Add(-3*time.Minute), codexSlot(29, 10080, reset), "null"))
	ws, ok := codexWindowsAt(context.Background(), home+"/sessions", codexLimitsNow)
	if !ok || len(ws) != 1 || ws[0].Fraction != 0.31 {
		t.Fatalf("windows = %+v, %v; want the newest line's 0.31", ws, ok)
	}
}

// Only the tail is read: a rollout larger than the bound still yields the block
// at its end, and the block before the bound is never seen.
func TestCodexWindowsReadsTheTailOfALargeRollout(t *testing.T) {
	home := t.TempDir()
	reset := codexLimitsNow.Add(time.Hour)
	filler := `{"type":"response_item","payload":{"text":"` + strings.Repeat("x", 1<<20) + `"}}`
	lines := []string{codexLimitsEvent(codexLimitsNow, codexSlot(99, 10080, reset), "null")}
	for range codexLimitsTail/(1<<20) + 1 {
		lines = append(lines, filler)
	}
	lines = append(lines, codexLimitsEvent(codexLimitsNow.Add(-time.Minute), codexSlot(8, 10080, reset), "null"))
	writeRollout(t, home, codexLimitsNow, "rollout-a.jsonl", lines...)

	ws, ok := codexWindowsAt(context.Background(), home+"/sessions", codexLimitsNow)
	if !ok || len(ws) != 1 || ws[0].Fraction != 0.08 {
		t.Fatalf("windows = %+v, %v; want the tail's 0.08", ws, ok)
	}
}

// No rollouts, or rollouts without a block, are no reading at all.
func TestCodexWindowsWithoutABlockIsNoReading(t *testing.T) {
	home := t.TempDir()
	if ws, ok := codexWindowsAt(context.Background(), home+"/sessions", codexLimitsNow); ok || ws != nil {
		t.Errorf("an empty tree gave %+v, %v", ws, ok)
	}
	writeRollout(t, home, codexLimitsNow, "rollout-a.jsonl", codexMetaLine("a", "/p"))
	if ws, ok := codexWindowsAt(context.Background(), home+"/sessions", codexLimitsNow); ok || ws != nil {
		t.Errorf("a rollout with no token_count gave %+v, %v", ws, ok)
	}
}

// CodexWindows reads under $CODEX_HOME, never the operator's real ~/.codex.
func TestCodexWindowsHonoursCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	now := time.Now()
	writeRollout(t, home, now, "rollout-a.jsonl",
		codexLimitsEvent(now, codexSlot(25, 10080, now.Add(time.Hour)), "null"))
	ws, ok := CodexWindows(context.Background())
	if !ok || len(ws) != 1 || ws[0].Fraction != 0.25 {
		t.Fatalf("windows = %+v, %v; want the CODEX_HOME rollout's 0.25", ws, ok)
	}
}

// The stated windows go in front of baton's own spend window, and the caller's
// slice is not aliased into the report.
func TestAttachQuotaWindowsPrepends(t *testing.T) {
	r := VendorReport{Windows: []VendorWindow{{Label: WindowSpend}}}
	ws := []VendorWindow{{Label: WindowFiveHour}, {Label: WindowWeek}}
	got := AttachQuotaWindows(r, ws)
	if len(got.Windows) != 3 || got.Windows[0].Label != WindowFiveHour || got.Windows[2].Label != WindowSpend {
		t.Fatalf("windows = %+v", got.Windows)
	}
	got.Windows[0].Label = "x"
	if ws[0].Label != WindowFiveHour {
		t.Error("the attached list aliases the caller's slice")
	}
	if same := AttachQuotaWindows(r, nil); len(same.Windows) != 1 {
		t.Errorf("a nil list changed the report: %+v", same.Windows)
	}
}

// A rollout last written before the longest window began is not opened: every
// window it could state has reset since, whatever its block claims.
func TestCodexWindowsIgnoresARolloutPastTheHorizon(t *testing.T) {
	home := t.TempDir()
	old := writeRollout(t, home, codexLimitsNow.AddDate(0, 0, -9), "rollout-old.jsonl",
		codexLimitsEvent(codexLimitsNow.AddDate(0, 0, -8), codexSlot(50, 10080, codexLimitsNow.Add(time.Hour)), "null"))
	touch(t, old, codexLimitsNow.Add(-codexLimitsHorizon-time.Minute))
	if ws, ok := codexWindowsAt(context.Background(), home+"/sessions", codexLimitsNow); ok || ws != nil {
		t.Errorf("a rollout past the horizon gave %+v, %v; want no reading", ws, ok)
	}
}

// A cancelled poll stops the walk and reports no reading.
func TestCodexWindowsHonoursACancelledContext(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, codexLimitsNow, "rollout-a.jsonl",
		codexLimitsEvent(codexLimitsNow, codexSlot(10, 10080, codexLimitsNow.Add(time.Hour)), "null"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ws, ok := codexWindowsAt(ctx, home+"/sessions", codexLimitsNow); ok || ws != nil {
		t.Errorf("a cancelled walk gave %+v, %v; want no reading", ws, ok)
	}
}
