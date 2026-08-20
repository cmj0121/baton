package usage

import (
	"strconv"
	"testing"
	"time"
)

// limitsNow pins "now" so every reset instant and age below is deterministic.
var limitsNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

// statuslineJSON builds a status-line stdin payload carrying the given rate
// limits, wrapped in the surrounding session fields a real payload has — the
// parser must ignore them, and a fixture without them would not prove that.
func statuslineJSON(body string) []byte {
	return []byte(`{"session_id":"abc","model":{"display_name":"Opus"},` +
		`"workspace":{"current_dir":"/tmp"},"context_window":{"used_percentage":8},` +
		body + `}`)
}

func TestParseStatuslineBothWindows(t *testing.T) {
	reset5h := limitsNow.Add(2 * time.Hour)
	reset7d := limitsNow.Add(72 * time.Hour)
	raw := statuslineJSON(`"rate_limits":{` +
		`"five_hour":{"used_percentage":23.5,"resets_at":` + epoch(reset5h) + `},` +
		`"seven_day":{"used_percentage":41.2,"resets_at":` + epoch(reset7d) + `}}`)

	l, ok := ParseStatusline(raw, limitsNow)
	if !ok {
		t.Fatal("ParseStatusline reported no limits for a payload that carries both windows")
	}
	if l.Source != LimitsStatusline {
		t.Errorf("Source = %q, want %q", l.Source, LimitsStatusline)
	}
	if !l.At.Equal(limitsNow) {
		t.Errorf("At = %v, want %v", l.At, limitsNow)
	}
	if l.FiveHour == nil || l.FiveHour.UsedPercent != 23.5 {
		t.Fatalf("five_hour = %+v, want 23.5%%", l.FiveHour)
	}
	if !l.FiveHour.ResetsAt.Equal(reset5h) {
		t.Errorf("five_hour reset = %v, want %v", l.FiveHour.ResetsAt, reset5h)
	}
	if l.SevenDay == nil || l.SevenDay.UsedPercent != 41.2 {
		t.Fatalf("seven_day = %+v, want 41.2%%", l.SevenDay)
	}
	// The status-line source has no per-model breakdown and no credit balance;
	// inventing either would be worse than leaving them absent.
	if l.SevenDayOpus != nil || l.SevenDaySonnet != nil || l.Credit != nil {
		t.Error("status-line payload produced per-model or credit data it cannot know")
	}
}

// A window may be absent on its own — the documented contract — and the other one
// still has to come through.
func TestParseStatuslineOneWindowOnly(t *testing.T) {
	raw := statuslineJSON(`"rate_limits":{"five_hour":{"used_percentage":10,"resets_at":0}}`)
	l, ok := ParseStatusline(raw, limitsNow)
	if !ok {
		t.Fatal("a payload with only five_hour should still report limits")
	}
	if l.SevenDay != nil {
		t.Error("seven_day should stay absent, not appear as a zeroed window")
	}
	// resets_at 0 is the field's absence, not midnight in 1970.
	if !l.FiveHour.ResetsAt.IsZero() {
		t.Errorf("five_hour reset = %v, want zero for an absent resets_at", l.FiveHour.ResetsAt)
	}
	if _, ok := l.FiveHour.Countdown(limitsNow); ok {
		t.Error("a window with no reset must not offer a countdown")
	}
}

// Every status-line render before the session's first API response looks like
// this. It is ordinary, so it must report cleanly rather than as an error.
func TestParseStatuslineNoRateLimits(t *testing.T) {
	for name, raw := range map[string][]byte{
		"absent block": statuslineJSON(`"cost":{"total_cost_usd":0.1}`),
		"empty block":  statuslineJSON(`"rate_limits":{}`),
		"not json":     []byte("not json at all"),
	} {
		if _, ok := ParseStatusline(raw, limitsNow); ok {
			t.Errorf("%s: reported limits where there are none", name)
		}
	}
}

func TestWindowFractionClamps(t *testing.T) {
	for _, tc := range []struct {
		pct  float64
		want float64
	}{{0, 0}, {50, 0.5}, {100, 1}, {103, 1}, {-4, 0}} {
		if got := (&Window{UsedPercent: tc.pct}).Fraction(); got != tc.want {
			t.Errorf("Fraction(%v%%) = %v, want %v", tc.pct, got, tc.want)
		}
	}
	if got := (*Window)(nil).Fraction(); got != 0 {
		t.Errorf("nil window Fraction = %v, want 0", got)
	}
}

// A reading routinely outlives its own window, because the status-line source
// only samples when a panel renders. The countdown must go away rather than park
// at zero, which would read as "resetting right now" for as long as the fleet
// stayed quiet.
func TestWindowCountdownExpires(t *testing.T) {
	w := &Window{UsedPercent: 60, ResetsAt: limitsNow.Add(90 * time.Minute)}
	d, ok := w.Countdown(limitsNow)
	if !ok || d != 90*time.Minute {
		t.Fatalf("Countdown = (%v, %v), want (1h30m, true)", d, ok)
	}
	if _, ok := w.Countdown(limitsNow.Add(2 * time.Hour)); ok {
		t.Error("a window whose reset has passed must report no countdown")
	}
}

func TestCreditFraction(t *testing.T) {
	pct, limit, used := 18.0, 65.0, 11.7

	if _, ok := (*Credit)(nil).Fraction(); ok {
		t.Error("nil credit reported a fraction")
	}
	if _, ok := (&Credit{Enabled: false, UsedPercent: &pct}).Fraction(); ok {
		t.Error("a disabled credit balance reported a fraction")
	}
	// The vendor's own utilisation figure wins when it is there.
	if got, ok := (&Credit{Enabled: true, UsedPercent: &pct}).Fraction(); !ok || got != 0.18 {
		t.Errorf("Fraction = (%v, %v), want (0.18, true)", got, ok)
	}
	// …and the amounts are the fallback when it is not.
	got, ok := (&Credit{Enabled: true, MonthlyUSD: &limit, UsedUSD: &used}).Fraction()
	if !ok || got < 0.179 || got > 0.181 {
		t.Errorf("Fraction from amounts = (%v, %v), want (~0.18, true)", got, ok)
	}
	// An uncapped balance is not a full one: there is nothing to be a fraction of.
	if _, ok := (&Credit{Enabled: true, UsedUSD: &used}).Fraction(); ok {
		t.Error("an uncapped credit balance reported a fraction")
	}
}

func TestLimitsEmpty(t *testing.T) {
	if !(Limits{}).Empty() {
		t.Error("a zero Limits should be empty")
	}
	if !(Limits{Credit: &Credit{Enabled: false}}).Empty() {
		t.Error("a disabled credit block alone should still be empty")
	}
	if (Limits{Credit: &Credit{Enabled: true}}).Empty() {
		t.Error("an enabled credit block is something worth showing")
	}
	if (Limits{FiveHour: &Window{}}).Empty() {
		t.Error("a present window — even at 0%% — is a real reading")
	}
}

// The age of a reading is a fact about the reading: the status-line source is a
// push, so nothing arrives at all while the fleet is idle.
func TestLimitsAgeAndStale(t *testing.T) {
	l := Limits{FiveHour: &Window{}, At: limitsNow}
	if got := l.Age(limitsNow.Add(90 * time.Second)); got != 90*time.Second {
		t.Errorf("Age = %v, want 90s", got)
	}
	// A clock corrected backwards must not produce a reading from the future.
	if got := l.Age(limitsNow.Add(-time.Minute)); got != 0 {
		t.Errorf("Age with now before At = %v, want 0", got)
	}
	if l.Stale(limitsNow.Add(time.Minute)) {
		t.Error("a one-minute-old reading should not be stale")
	}
	if !l.Stale(limitsNow.Add(StaleAfter + time.Second)) {
		t.Error("a reading past StaleAfter should be stale")
	}
	// Something produced it without saying when; it must never show as current.
	if !(Limits{FiveHour: &Window{}}).Stale(limitsNow) {
		t.Error("an unstamped reading should be stale by definition")
	}
}

// A typo leaves the feature working on the source that needs no credential and no
// network call, rather than silently switching it off.
func TestParseLimitsSource(t *testing.T) {
	for in, want := range map[string]string{
		"":            LimitsStatusline,
		"statusline":  LimitsStatusline,
		"  OAuth  ":   LimitsOAuth,
		"off":         LimitsOff,
		"statusliné":  LimitsStatusline,
		"admin-magic": LimitsStatusline,
	} {
		if got := ParseLimitsSource(in); got != want {
			t.Errorf("ParseLimitsSource(%q) = %q, want %q", in, got, want)
		}
	}
}

// epoch renders t as the Unix-seconds literal the status-line payload uses.
func epoch(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }
