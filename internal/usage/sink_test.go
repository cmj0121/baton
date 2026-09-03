package usage

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// sinkPath is a sink file inside a fresh temp dir, including a directory level
// the sink has to create itself — a first run happens before $HOME/.baton exists.
func sinkPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "baton", "usage-limits.json")
}

// sample is one reading taken at `at`. The reset instants are pinned to
// limitsNow rather than to `at`, because that is how a real window behaves: the
// reset stays put while sample after sample is taken against it. A helper that
// slid them with the clock would make every reading look changed.
func sample(at time.Time, five, seven float64) Limits {
	return Limits{
		FiveHour: &Window{UsedPercent: five, ResetsAt: limitsNow.Add(2 * time.Hour)},
		SevenDay: &Window{UsedPercent: seven, ResetsAt: limitsNow.Add(72 * time.Hour)},
		Source:   LimitsStatusline,
		At:       at,
	}
}

func TestLimitsRoundTrip(t *testing.T) {
	path := sinkPath(t)
	pct, limit, used := 18.0, 65.0, 11.7
	want := sample(limitsNow, 62.4, 34.1)
	want.SevenDayOpus = &Window{UsedPercent: 71, ResetsAt: limitsNow.Add(72 * time.Hour)}
	want.Credit = &Credit{Enabled: true, MonthlyUSD: &limit, UsedUSD: &used, UsedPercent: &pct}

	if wrote, err := WriteLimitsIfChanged(path, want); err != nil || !wrote {
		t.Fatalf("first write = (%v, %v), want (true, nil)", wrote, err)
	}
	got, ok := ReadLimits(path)
	if !ok {
		t.Fatal("ReadLimits found nothing after a write")
	}
	if got.FiveHour.UsedPercent != 62.4 || !got.FiveHour.ResetsAt.Equal(want.FiveHour.ResetsAt) {
		t.Errorf("five_hour = %+v, want %+v", got.FiveHour, want.FiveHour)
	}
	if got.SevenDayOpus == nil || got.SevenDayOpus.UsedPercent != 71 {
		t.Errorf("seven_day_opus = %+v, want 71%%", got.SevenDayOpus)
	}
	if !got.At.Equal(limitsNow) || got.Source != LimitsStatusline {
		t.Errorf("At/Source = %v/%q, want %v/%q", got.At, got.Source, limitsNow, LimitsStatusline)
	}
	if got.Credit == nil || !got.Credit.Enabled || got.Credit.MonthlyUSD == nil || *got.Credit.MonthlyUSD != 65 {
		t.Errorf("credit = %+v, want the enabled $65 balance back", got.Credit)
	}
	// The sink file is a per-user artefact; it should not be world-readable.
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("sink file mode = %v (err %v), want 0600", fi.Mode().Perm(), err)
	}
}

// A window that has gone away is a change, not a match — an absent window and a
// zeroed one are opposite readings and must not collapse.
func TestWriteLimitsIfChangedSkipsRedundant(t *testing.T) {
	path := sinkPath(t)
	if wrote, _ := WriteLimitsIfChanged(path, sample(limitsNow, 62.4, 34.1)); !wrote {
		t.Fatal("the first reading should always be written")
	}
	// Same numbers, a moment later: not worth a disk round trip.
	soon := sample(limitsNow.Add(3*time.Second), 62.4, 34.1)
	if wrote, _ := WriteLimitsIfChanged(path, soon); wrote {
		t.Error("an unchanged reading inside RefreshAfter was rewritten")
	}
	// Same numbers, but old enough that leaving the stamp alone would start
	// marking a live reading as stale.
	later := sample(limitsNow.Add(RefreshAfter+time.Second), 62.4, 34.1)
	if wrote, _ := WriteLimitsIfChanged(path, later); !wrote {
		t.Error("an unchanged reading past RefreshAfter was not restamped")
	}
	// A moved percentage is written whatever the clock says.
	moved := sample(limitsNow.Add(RefreshAfter+2*time.Second), 63.9, 34.1)
	if wrote, _ := WriteLimitsIfChanged(path, moved); !wrote {
		t.Error("a changed reading was skipped")
	}
	dropped := sample(limitsNow.Add(RefreshAfter+3*time.Second), 63.9, 34.1)
	dropped.SevenDay = nil
	if wrote, _ := WriteLimitsIfChanged(path, dropped); !wrote {
		t.Error("a window going away is a change and must be written")
	}
}

// Every panel in the fleet runs a sink, several times a second. Two of them
// racing must never leave a reader holding a torn file.
func TestWriteLimitsConcurrent(t *testing.T) {
	path := sinkPath(t)
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct readings so nothing is skipped as redundant: every one of these
			// genuinely tries to write.
			_, _ = WriteLimitsIfChanged(path, sample(limitsNow.Add(time.Duration(i)*time.Minute), float64(i), 34.1))
		}(i)
	}
	wg.Wait()

	if _, ok := ReadLimits(path); !ok {
		t.Fatal("the sink file did not survive concurrent writers")
	}
	// No writer may leave its temporary behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("stray file left in the sink directory: %s", e.Name())
		}
	}
}

// durableReplaceFile is replaceFile with the two syncs paths.WriteFileAtomic
// does — the design replaceFile's doc rules out. It is here so the number that
// doc quotes is one a reader can re-derive rather than one they have to believe:
//
//	go test ./internal/usage -run XXX -bench WriteSink -benchtime 300x -count 6
//
// On an Apple M2 Pro SSD: 164-192 µs plain against 5.6-6.6 ms synced, more than
// thirty times the cost, paid on the path Claude Code re-runs to render a
// panel's status line.
func durableReplaceFile(path string, data []byte) (err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".usage-limits-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Kept so the pair differs by the two syncs and nothing else.
	if err = os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	if dir, derr := os.Open(filepath.Dir(path)); derr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// benchReading is one encoded sink file, the size a real one is.
var benchReading, _ = MarshalLimits(sample(limitsNow, 62.4, 34.1))

func BenchmarkWriteSinkPlain(b *testing.B) {
	path := filepath.Join(b.TempDir(), "usage-limits.json")
	for b.Loop() {
		if err := replaceFile(path, benchReading); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteSinkDurable(b *testing.B) {
	path := filepath.Join(b.TempDir(), "usage-limits.json")
	for b.Loop() {
		if err := durableReplaceFile(path, benchReading); err != nil {
			b.Fatal(err)
		}
	}
}

// A missing file is the ordinary state of a fleet that has not run a Claude Code
// turn yet, not an error to report.
func TestReadLimitsAbsentOrJunk(t *testing.T) {
	path := sinkPath(t)
	if _, ok := ReadLimits(path); ok {
		t.Error("ReadLimits reported a reading from a file that does not exist")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"not json":      "{{{",
		"empty object":  `{}`,
		"windows gone":  `{"source":"statusline","at":"2026-08-20T12:00:00Z"}`,
		"credit is off": `{"credit":{"enabled":false},"at":"2026-08-20T12:00:00Z"}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := ReadLimits(path); ok {
			t.Errorf("%s: reported a reading worth showing", name)
		}
	}
}

// An unstamped file decodes, but must never pass for current.
func TestReadLimitsUnstamped(t *testing.T) {
	path := sinkPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"five_hour":{"used_percentage":50}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l, ok := ReadLimits(path)
	if !ok {
		t.Fatal("a reading with a window but no stamp should still decode")
	}
	if !l.Stale(limitsNow) {
		t.Error("an unstamped reading passed as current")
	}
}

func TestStatuslineLimitsProvider(t *testing.T) {
	path := sinkPath(t)
	p := NewStatuslineLimits(path)
	if p.Source() != LimitsStatusline {
		t.Errorf("Source = %q, want %q", p.Source(), LimitsStatusline)
	}
	if _, ok := p.Limits(context.Background()); ok {
		t.Error("the provider reported a reading before any sink had written one")
	}
	if _, err := WriteLimitsIfChanged(path, sample(limitsNow, 62.4, 34.1)); err != nil {
		t.Fatal(err)
	}
	l, ok := p.Limits(context.Background())
	if !ok || l.FiveHour.UsedPercent != 62.4 {
		t.Errorf("Limits = (%+v, %v), want the written reading", l, ok)
	}
}

func TestBarEnds(t *testing.T) {
	// A touched window never reads as empty, and a window with room left never
	// reads as full — those are the two readings someone acts on.
	if got := Bar(0.001, 10); got != "▓░░░░░░░░░" {
		t.Errorf("Bar(0.001) = %q, want one filled cell", got)
	}
	if got := Bar(0.999, 10); got != "▓▓▓▓▓▓▓▓▓░" {
		t.Errorf("Bar(0.999) = %q, want one empty cell", got)
	}
	if got := Bar(0, 10); got != "░░░░░░░░░░" {
		t.Errorf("Bar(0) = %q, want an empty bar", got)
	}
	if got := Bar(1, 10); got != "▓▓▓▓▓▓▓▓▓▓" {
		t.Errorf("Bar(1) = %q, want a full bar", got)
	}
	if got := Bar(0.5, 0); got != "" {
		t.Errorf("Bar with no width = %q, want empty", got)
	}
}

func TestFormatLimits(t *testing.T) {
	l := sample(limitsNow, 62.4, 34.1)
	got := FormatLimits(l, limitsNow, 10)
	want := "5h ▓▓▓▓▓▓░░░░ 2:00:00 · 7d ▓▓▓░░░░░░░ 3d0h"
	if got != want {
		t.Errorf("FormatLimits =\n%q\nwant\n%q", got, want)
	}
	// A window with no reset still reports its fill; only the countdown goes.
	noReset := Limits{FiveHour: &Window{UsedPercent: 20}, At: limitsNow}
	if got := FormatLimits(noReset, limitsNow, 5); got != "5h ▓░░░░" {
		t.Errorf("FormatLimits without a reset = %q", got)
	}
	if got := FormatLimits(Limits{}, limitsNow, 10); got != "" {
		t.Errorf("FormatLimits of an empty reading = %q, want empty", got)
	}
}
