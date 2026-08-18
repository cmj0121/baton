package usage

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newLocalWindow is newLocal with a window length, so the countdown paths are
// exercised rather than the calendar-day fallback.
func newLocalWindow(dir string, window time.Duration) *LocalProvider {
	return &LocalProvider{dir: dir, window: window, now: func() time.Time { return fixedNow }}
}

// writeSubagentTranscript writes a subagent transcript at
// <root>/projects/<project>/<session>/subagents/<name>.jsonl — where Claude Code
// puts the transcripts of the subagents a session spawned.
func writeSubagentTranscript(t *testing.T, root, project, session, name string, mtime time.Time, lines ...string) {
	t.Helper()
	dir := filepath.Join(root, "projects", project, session, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".jsonl")
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

// TestLocalFetchWindowOpensAtAMessage: with a window configured, the window opens
// at the message that opened it — not at "now" and not at midnight — the reset is
// that start plus the window length, and a message belonging to a window that has
// already closed is left out of both the totals and the start.
func TestLocalFetchWindowOpensAtAMessage(t *testing.T) {
	root := t.TempDir()
	oldest := fixedNow.Add(-3 * time.Hour)
	writeTranscript(t, root, "proj1", "sess1", fixedNow,
		assistantLine("stale", "r0", "claude-opus-4-8", fixedNow.Add(-9*time.Hour), 999, 999, 0, 0, 0),
		assistantLine("m1", "r1", "claude-opus-4-8", oldest, 100, 50, 0, 0, 0),
		assistantLine("m2", "r2", "claude-opus-4-8", fixedNow.Add(-time.Hour), 10, 5, 0, 0, 0),
	)

	snap, err := newLocalWindow(filepath.Join(root, "projects"), 5*time.Hour).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Resets {
		t.Fatal("a windowed local snapshot should carry a reset to count down to")
	}
	if !snap.Since.Equal(oldest) {
		t.Errorf("Since = %v, want the message that opened the window %v", snap.Since, oldest)
	}
	if want := oldest.Add(5 * time.Hour); !snap.Until.Equal(want) {
		t.Errorf("Until = %v, want %v", snap.Until, want)
	}
	if snap.Input != 110 || snap.Output != 55 {
		t.Errorf("totals = %d/%d, want 110/55 (the earlier window's message excluded)", snap.Input, snap.Output)
	}
}

// growingTranscript writes one transcript that grows with the clock: each call
// appends every message up to now and stamps the file, so a scan never sees a
// message the clock has not reached. It returns the grow function.
func growingTranscript(t *testing.T, root string, from time.Time, every time.Duration) func(now time.Time) {
	t.Helper()
	dir := filepath.Join(root, "projects", "proj1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sess1.jsonl")
	body, next := "", from
	return func(now time.Time) {
		t.Helper()
		for ; !next.After(now); next = next.Add(every) {
			body += assistantLine("m"+next.String(), "r"+next.String(), "claude-opus-4-8", next, 5, 0, 0, 0, 0) + "\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now, now); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLocalFetchAnchorHoldsAcrossMidnight: the window must not move because the
// calendar did. The scan reaches back a fixed distance from the day's start, so
// under unbroken use the oldest message it can see sits on that floor — and
// working the window out from it on every poll pinned the boundaries to local
// midnight. At 00:01 the countdown jumped back up by four hours and the reported
// spend collapsed to a single message, every night, for exactly the usage that
// produced the original report.
//
// The window in progress is carried from poll to poll instead, so midnight is not
// an event: the same window keeps running down and its spend keeps accumulating.
func TestLocalFetchAnchorHoldsAcrossMidnight(t *testing.T) {
	const window = 5 * time.Hour
	root := t.TempDir()
	day := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	grow := growingTranscript(t, root, day, 5*time.Minute)

	var now time.Time
	p := &LocalProvider{dir: filepath.Join(root, "projects"), window: window, now: func() time.Time { return now }}
	seen := map[time.Time]Snapshot{}
	for now = day.Add(6 * time.Hour); now.Before(day.Add(30 * time.Hour)); now = now.Add(15 * time.Minute) {
		grow(now)
		snap, err := p.Fetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		seen[now] = snap
	}

	// Two polls astride midnight, both inside the window that opened at 20:00.
	beforeAt, afterAt := day.Add(23*time.Hour+45*time.Minute), day.Add(24*time.Hour+15*time.Minute)
	before, after := seen[beforeAt], seen[afterAt]
	if !before.Since.Equal(after.Since) || !before.Until.Equal(after.Until) {
		t.Fatalf("midnight moved the window: %v..%v became %v..%v",
			before.Since, before.Until, after.Since, after.Until)
	}
	bd, _ := before.Countdown(beforeAt)
	ad, _ := after.Countdown(afterAt)
	if ad >= bd {
		t.Errorf("countdown went from %v to %v across midnight, want it still falling", bd, ad)
	}
	if after.TotalTokens() < before.TotalTokens() {
		t.Errorf("spend collapsed across midnight: %d became %d", before.TotalTokens(), after.TotalTokens())
	}

	// A carried anchor is only good while the scan still covers it. Days later, with
	// nothing since, it is dropped rather than chained off — and nothing recent is
	// left to open a window, so there is none.
	now = day.Add(72 * time.Hour)
	stale, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stale.Resets || !stale.Empty() {
		t.Errorf("an anchor older than the scan should be dropped, got %+v", stale)
	}
}

// TestLocalFetchIgnoresMessagesAheadOfTheClock: a transcript stamped in the
// future — a clock corrected backwards, a ~/.claude synced from a machine running
// ahead — must not open a window. One that had opened would not have started yet,
// and counting down to its end reports more time left than a window is long.
func TestLocalFetchIgnoresMessagesAheadOfTheClock(t *testing.T) {
	const window = 5 * time.Hour
	root := t.TempDir()
	writeTranscript(t, root, "proj1", "sess1", fixedNow,
		assistantLine("here", "r1", "claude-opus-4-8", fixedNow.Add(-time.Hour), 100, 0, 0, 0, 0),
		assistantLine("ahead", "r2", "claude-opus-4-8", fixedNow.Add(window), 999, 0, 0, 0, 0),
	)

	snap, err := newLocalWindow(filepath.Join(root, "projects"), window).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d, ok := snap.Countdown(fixedNow)
	if !ok || d > window {
		t.Errorf("countdown = %v/%v, want at most the %v window", d, ok, window)
	}
	if snap.Input != 100 {
		t.Errorf("Input = %d, want 100 — the message from the future left out", snap.Input)
	}
}

// TestLocalFetchWindowRunsDownAndRestarts is the regression for a countdown that
// hung at 0:00:00. The window used to be anchored on the oldest message still
// inside "the last N hours", so its end moved with the clock: under continuous
// use the oldest message was always a window old, the reset was always this
// instant, and the footer sat at zero forever instead of ever restarting.
//
// The window now opens at a message and stays where it opened, so the countdown
// falls as the clock advances and the first message after the window closes opens
// the next one with a full window to run.
func TestLocalFetchWindowRunsDownAndRestarts(t *testing.T) {
	const window = 5 * time.Hour
	root := t.TempDir()
	projects := filepath.Join(root, "projects")

	// Six hours of unbroken use, one message every five minutes: the shape that
	// used to pin the countdown, since some message is always a window old.
	var lines []string
	for at := fixedNow.Add(-6 * time.Hour); at.Before(fixedNow); at = at.Add(5 * time.Minute) {
		lines = append(lines, assistantLine("m"+at.String(), "r"+at.String(), "claude-opus-4-8", at, 10, 5, 0, 0, 0))
	}
	writeTranscript(t, root, "proj1", "sess1", fixedNow, lines...)

	at := func(now time.Time) Snapshot {
		t.Helper()
		p := &LocalProvider{dir: projects, window: window, now: func() time.Time { return now }}
		snap, err := p.Fetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return snap
	}

	// The window opened an hour ago (six hours of use is two windows: the first
	// closed a window in, the second opened on the next message).
	opened := fixedNow.Add(-time.Hour)
	first := at(fixedNow)
	if !first.Resets || !first.Since.Equal(opened) {
		t.Fatalf("window = %v..%v resets=%v, want one opened at %v", first.Since, first.Until, first.Resets, opened)
	}
	if d, ok := first.Countdown(fixedNow); !ok || d != 4*time.Hour {
		t.Errorf("countdown = %v/%v, want 4h/true", d, ok)
	}

	// Advancing the clock spends the window down rather than pushing its end along.
	later := fixedNow.Add(3 * time.Hour)
	if d, ok := at(later).Countdown(later); !ok || d != time.Hour {
		t.Errorf("three hours on, countdown = %v/%v, want 1h/true", d, ok)
	}

	// Past the reset with nothing since: no window is in progress, so there is no
	// countdown at all — never a zero that would read as "resets right now".
	past := opened.Add(window + time.Minute)
	if closed := at(past); closed.Resets || !closed.Empty() {
		t.Errorf("a closed window should report neither a reset nor spend, got %+v", closed)
	}

	// The next message opens the next window, with the whole of it left to run.
	reopened := past.Add(10 * time.Minute)
	writeTranscript(t, root, "proj1", "sess2", reopened,
		assistantLine("next", "rnext", "claude-opus-4-8", reopened, 100, 50, 0, 0, 0),
	)
	fresh := at(reopened)
	if !fresh.Resets || !fresh.Since.Equal(reopened) {
		t.Fatalf("window = %v..%v resets=%v, want one opened at %v", fresh.Since, fresh.Until, fresh.Resets, reopened)
	}
	if d, ok := fresh.Countdown(reopened); !ok || d != window {
		t.Errorf("a freshly opened window = %v/%v, want the full %v", d, ok, window)
	}
	if fresh.Input != 100 {
		t.Errorf("Input = %d, want 100 — only the new window's spend", fresh.Input)
	}
}

// TestLocalFetchWindowDisabled: window 0 keeps the calendar-day behaviour and
// reports no reset, so the footer shows spend without a countdown.
func TestLocalFetchWindowDisabled(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "proj1", "sess1", fixedNow,
		assistantLine("m1", "r1", "claude-opus-4-8", fixedNow.Add(-time.Hour), 100, 50, 0, 0, 0),
	)

	snap, err := newLocalWindow(filepath.Join(root, "projects"), 0).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Resets || !snap.Until.IsZero() {
		t.Fatalf("window 0 should report no reset, got Resets=%v Until=%v", snap.Resets, snap.Until)
	}
	if !snap.Since.Equal(startOfDay(fixedNow)) {
		t.Errorf("Since = %v, want local midnight %v", snap.Since, startOfDay(fixedNow))
	}
}

// TestLocalFetchEmptyWindowHasNoReset: a window with no messages in it is not a
// window that just opened. With nothing to anchor the start to, the snapshot
// reports no reset rather than starting a countdown at "now".
func TestLocalFetchEmptyWindowHasNoReset(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "proj1", "sess1", fixedNow,
		assistantLine("m1", "r1", "claude-opus-4-8", fixedNow.Add(-9*time.Hour), 100, 50, 0, 0, 0),
	)

	snap, err := newLocalWindow(filepath.Join(root, "projects"), 5*time.Hour).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Resets {
		t.Fatal("an empty window should not report a reset")
	}
	if !snap.Empty() {
		t.Fatalf("nothing is inside the window, so the snapshot should be empty: %+v", snap)
	}
}

// TestLocalFetchPerSession: spend is attributed to the session that made it, and
// a session's subagents fold into that same session rather than a bucket of their
// own — a panel's subagents are that panel's spend.
func TestLocalFetchPerSession(t *testing.T) {
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	writeTranscript(t, root, "proj1", "sess1", fixedNow,
		assistantLine("a1", "r1", "claude-opus-4-8", ts, 100, 0, 0, 0, 0),
	)
	writeSubagentTranscript(t, root, "proj1", "sess1", "agent-abc", fixedNow,
		assistantLine("a2", "r2", "claude-opus-4-8", ts, 30, 0, 0, 0, 0),
	)
	writeTranscript(t, root, "proj2", "sess2", fixedNow,
		assistantLine("b1", "r3", "claude-opus-4-8", ts, 7, 0, 0, 0, 0),
	)

	snap, err := newLocalWindow(filepath.Join(root, "projects"), 5*time.Hour).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Sessions["sess1"].Tokens; got != 130 {
		t.Errorf("sess1 = %d tokens, want 130 (own 100 + subagent 30)", got)
	}
	if got := snap.Sessions["sess2"].Tokens; got != 7 {
		t.Errorf("sess2 = %d tokens, want 7", got)
	}
	if snap.TotalTokens() != 137 {
		t.Errorf("total = %d, want 137", snap.TotalTokens())
	}
	if got, want := snap.Share("sess1"), 130.0/137.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("Share(sess1) = %v, want %v", got, want)
	}
	if snap.Share("nobody") != 0 {
		t.Errorf("an unseen session should hold no share, got %v", snap.Share("nobody"))
	}
}

// TestLocalFetchForkedSessionNotDoubleCounted: forking a session replays the
// parent's turns verbatim into the new transcript, keeping their ids. The same
// spend must be counted — and attributed — once, not once per file.
func TestLocalFetchForkedSessionNotDoubleCounted(t *testing.T) {
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	replayed := assistantLine("shared", "r1", "claude-opus-4-8", ts, 100, 0, 0, 0, 0)
	writeTranscript(t, root, "proj1", "aaaa-parent", fixedNow, replayed)
	writeTranscript(t, root, "proj1", "bbbb-forked", fixedNow,
		replayed, // the replayed history, same message id and timestamp
		assistantLine("fresh", "r2", "claude-opus-4-8", ts, 5, 0, 0, 0, 0),
	)

	snap, err := newLocalWindow(filepath.Join(root, "projects"), 5*time.Hour).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.TotalTokens() != 105 {
		t.Errorf("total = %d, want 105 — the replayed turn counted once", snap.TotalTokens())
	}
	if sum := snap.Sessions["aaaa-parent"].Tokens + snap.Sessions["bbbb-forked"].Tokens; sum != 105 {
		t.Errorf("per-session sum = %d, want 105 (no double attribution)", sum)
	}
}

// TestSessionOf: a session's own transcript and its subagents' transcripts both
// resolve to the session id, and a path outside the layout resolves to "".
func TestSessionOf(t *testing.T) {
	root := filepath.Join("home", "projects")
	cases := map[string]string{
		filepath.Join(root, "proj", "sess.jsonl"):                           "sess",
		filepath.Join(root, "proj", "sess", "subagents", "agent-x.jsonl"):   "sess",
		filepath.Join(root, "proj", "sess", "tool-results", "t", "x.jsonl"): "sess",
		filepath.Join(root, "loose.jsonl"):                                  "",
	}
	for path, want := range cases {
		if got := sessionOf(root, path); got != want {
			t.Errorf("sessionOf(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestSnapshotCountdown: a snapshot that knows its reset counts down to it; one
// whose window has since run out, and one whose source never saw a reset, both
// report no countdown at all rather than a zero a caller could mistake for
// "resets now" — and would then rest on for as long as it held the snapshot.
func TestSnapshotCountdown(t *testing.T) {
	s := Snapshot{Since: fixedNow.Add(-time.Hour), Until: fixedNow.Add(2 * time.Hour), Resets: true}
	if d, ok := s.Countdown(fixedNow); !ok || d != 2*time.Hour {
		t.Errorf("Countdown = %v/%v, want 2h/true", d, ok)
	}
	if d, ok := s.Countdown(s.Until); ok || d != 0 {
		t.Errorf("at the reset itself = %v/%v, want 0/false — zero is not a resting state", d, ok)
	}
	if d, ok := s.Countdown(fixedNow.Add(5 * time.Hour)); ok || d != 0 {
		t.Errorf("a window that has run out = %v/%v, want 0/false", d, ok)
	}

	unknown := Snapshot{Since: fixedNow, Until: fixedNow.Add(time.Hour)} // Resets false
	if _, ok := unknown.Countdown(fixedNow); ok {
		t.Error("a source that cannot see a reset must not report a countdown")
	}
}

// TestSnapshotSpent: the elapsed fraction drives the segment's colour, so it is
// clamped to 0–1 and refuses to report at all without a real window.
func TestSnapshotSpent(t *testing.T) {
	s := Snapshot{Since: fixedNow, Until: fixedNow.Add(4 * time.Hour), Resets: true}
	if f, ok := s.Spent(fixedNow.Add(3 * time.Hour)); !ok || math.Abs(f-0.75) > 1e-9 {
		t.Errorf("Spent = %v/%v, want 0.75/true", f, ok)
	}
	if f, ok := s.Spent(fixedNow.Add(9 * time.Hour)); !ok || f != 1 {
		t.Errorf("past the reset = %v/%v, want 1/true", f, ok)
	}
	if f, ok := s.Spent(fixedNow.Add(-time.Hour)); !ok || f != 0 {
		t.Errorf("before the start = %v/%v, want 0/true", f, ok)
	}
	if _, ok := (Snapshot{Resets: true, Since: fixedNow, Until: fixedNow}).Spent(fixedNow); ok {
		t.Error("a zero-length window cannot be a fraction of anything")
	}
	if _, ok := (Snapshot{Since: fixedNow, Until: fixedNow.Add(time.Hour)}).Spent(fixedNow); ok {
		t.Error("no reset means no pressure reading")
	}
}

// TestFormatCountdown: auto stays on the clock form under a day and only widens
// past it; dd:hh:mm always spells out days.
func TestFormatCountdown(t *testing.T) {
	cases := []struct {
		d      time.Duration
		format string
		want   string
	}{
		{2*time.Hour + 14*time.Minute + 31*time.Second, CountdownAuto, "2:14:31"},
		{45 * time.Second, CountdownAuto, "0:00:45"},
		{3*24*time.Hour + 4*time.Hour + 12*time.Minute, CountdownAuto, "3d 04:12"},
		{2*time.Hour + 14*time.Minute + 31*time.Second, CountdownFull, "0d 02:14"},
		{-time.Hour, CountdownAuto, "0:00:00"},
	}
	for _, c := range cases {
		if got := FormatCountdown(c.d, c.format); got != c.want {
			t.Errorf("FormatCountdown(%v, %q) = %q, want %q", c.d, c.format, got, c.want)
		}
	}
}

// TestAPISnapshotReportsPeriodNotReset: the api source cannot see a reset — rate
// limits live on response headers baton never receives — so it reports the period
// it queried and leaves the countdown off.
func TestAPISnapshotReportsPeriodNotReset(t *testing.T) {
	p := NewAPIProvider("sk-ant-admin01-test")
	p.now = func() time.Time { return fixedNow }
	p.base = "http://127.0.0.1:0" // every request fails; the window fields are set before any call

	snap, _ := p.Fetch(context.Background())
	if snap.Resets {
		t.Error("the api source must not claim a reset it cannot see")
	}
	if !snap.Since.Equal(startOfDay(fixedNow)) || !snap.Until.Equal(startOfDay(fixedNow).AddDate(0, 0, 1)) {
		t.Errorf("period = %v..%v, want the queried day", snap.Since, snap.Until)
	}
	if snap.Sessions != nil {
		t.Error("the api source cannot attribute spend to a session")
	}
}
