package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// limitsPayload is a full rate-limit reading: both windows, one per-model ceiling
// present and one absent, and an enabled credit balance.
func limitsPayload(at time.Time) *proto.LimitsInfo {
	monthly, used, pct := 65.0, 11.7, 18.0
	return &proto.LimitsInfo{
		FiveHour:       &proto.LimitWindow{UsedPercent: 62, ResetsAt: at.Add(2*time.Hour + 14*time.Minute).Format(time.RFC3339)},
		SevenDay:       &proto.LimitWindow{UsedPercent: 34, ResetsAt: at.Add(74 * time.Hour).Format(time.RFC3339)},
		SevenDayOpus:   &proto.LimitWindow{UsedPercent: 91, ResetsAt: at.Add(74 * time.Hour).Format(time.RFC3339)},
		SevenDaySonnet: nil,
		Credit:         &proto.LimitCredit{Enabled: true, MonthlyUSD: &monthly, UsedUSD: &used, UsedPercent: &pct},
		Source:         "oauth",
		At:             at.Format(time.RFC3339),
	}
}

// limitsModel is a cockpit whose daemon has reported the reading above.
func limitsModel(mode usageMode) model {
	m := usageModel(mode)
	m.usageInfo.Limits = limitsPayload(usageNow)
	m.usageInfo.Panels = map[string]proto.PanelUsage{
		"p1": {Tokens: 800_000, CostUSD: 8},
		"p2": {Tokens: 300_000, CostUSD: 3},
	}
	return m
}

func TestUsageLimitsSegment(t *testing.T) {
	m := limitsModel(usageLimits)
	got := m.usageLimitsText()
	want := "5h ▓▓▓▓▓░░░ 2:14:00 · 7d ▓▓▓░░░░░ 3d2h"
	if got != want {
		t.Errorf("segment =\n%q\nwant\n%q", got, want)
	}
	if cap := m.usageCap(); !strings.Contains(cap, "5h") || !strings.Contains(cap, "7d") {
		t.Errorf("the footer cap dropped a window: %q", cap)
	}
}

// A bar at zero would assert a full tank on an account that may be minutes from a
// refusal, so no reading means no segment at all.
func TestUsageLimitsSegmentEmpty(t *testing.T) {
	m := limitsModel(usageLimits)
	m.usageInfo.Limits = nil
	if got := m.usageLimitsText(); got != "" {
		t.Errorf("segment with no reading = %q, want empty", got)
	}
	if got := m.usageCap(); got != "" {
		t.Errorf("footer cap with no reading = %q, want empty", got)
	}
}

// A window that has gone away drops out; the other one still reports.
func TestUsageLimitsSegmentPartial(t *testing.T) {
	m := limitsModel(usageLimits)
	m.usageInfo.Limits.SevenDay = nil
	got := m.usageLimitsText()
	if strings.Contains(got, "7d") {
		t.Errorf("an absent window was rendered: %q", got)
	}
	if !strings.HasPrefix(got, "5h ") {
		t.Errorf("segment = %q, want the five-hour window alone", got)
	}
	// A window with no reset keeps its bar and loses only the countdown.
	m.usageInfo.Limits.FiveHour.ResetsAt = ""
	if got := m.usageLimitsText(); got != "5h ▓▓▓▓▓░░░" {
		t.Errorf("segment without a reset = %q", got)
	}
}

// The statusline source is a push: it stops arriving when the fleet goes quiet,
// so an old reading is marked rather than dropped or trusted silently.
func TestUsageLimitsStaleMark(t *testing.T) {
	m := limitsModel(usageLimits)
	if strings.HasPrefix(m.usageLimitsText(), "~") {
		t.Error("a fresh reading was marked stale")
	}
	m.now = usageNow.Add(20 * time.Minute)
	if !strings.HasPrefix(m.usageLimitsText(), "~ ") {
		t.Errorf("an old reading was not marked: %q", m.usageLimitsText())
	}
	// Something stamped it without saying when; it must never pass as current.
	m.now = usageNow
	m.usageInfo.Limits.At = "not a timestamp"
	if !m.usageLimitsStale() {
		t.Error("an unreadable stamp passed as current")
	}
}

// The quota view colours by the fullest window, not by the clock: an account at
// 91% with three days left is in more trouble than the calendar suggests.
func TestLimitsPressureColour(t *testing.T) {
	m := limitsModel(usageLimits)
	if got := m.usagePressureColor(); got != colRed {
		t.Errorf("colour with a 91%% window = %v, want red", got)
	}
	m.usageInfo.Limits.SevenDayOpus.UsedPercent = 80
	if got := m.usagePressureColor(); got != colAmber {
		t.Errorf("colour with an 80%% window = %v, want amber", got)
	}
	m.usageInfo.Limits.FiveHour.UsedPercent = 10
	m.usageInfo.Limits.SevenDay.UsedPercent = 10
	m.usageInfo.Limits.SevenDayOpus.UsedPercent = 10
	if got := m.usagePressureColor(); got != colBlue {
		t.Errorf("colour with everything quiet = %v, want blue", got)
	}
	// The window view is unchanged — it still colours by how far the clock has run.
	m.usageMode = usageWindow
	if got := m.usagePressureColor(); got != colBlue {
		t.Errorf("window view colour = %v, want blue an hour into five", got)
	}
}

func TestUsageViewRenders(t *testing.T) {
	m := limitsModel(usageWindow)
	m.mode = modeUsage
	out := stripANSI(m.usageView())

	for _, want := range []string{
		spaced("ACCOUNT USAGE"), "oauth", "just now",
		"Session (5h)", "resets 2:14:00",
		"Week (all)", "resets 3d2h",
		"Week (Opus)",
		"Extra credit", "$11.70 / $65.00",
		"Burning this window", "share", "of 5h",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the overlay is missing %q:\n%s", want, out)
		}
	}
	// The bars carry the fill; a percentage beside them would say it twice, in a
	// form that has to be read rather than seen.
	for _, unwanted := range []string{"62%", "34%", "91%", "18%"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a quota percentage was drawn beside its bar (%s):\n%s", unwanted, out)
		}
	}
	// The per-model ceiling the source did not report gets no row at all.
	if strings.Contains(out, "Week (Sonnet)") {
		t.Errorf("an absent per-model ceiling was drawn:\n%s", out)
	}
	// The roster is heaviest first, and the last column is the share of the window
	// multiplied by how much of the five-hour quota is gone: 800k/1.2M × 62% ≈ 41%.
	api, web := strings.Index(out, "claude · api"), strings.Index(out, "claude · web")
	if api < 0 || web < 0 || api > web {
		t.Errorf("the roster is not heaviest-first:\n%s", out)
	}
	if !strings.Contains(out, "41%") {
		t.Errorf("the of-5h column is missing its 41%%:\n%s", out)
	}
}

// An empty box would leave someone wondering whether it was broken.
func TestUsageViewNoReading(t *testing.T) {
	m := limitsModel(usageWindow)
	m.usageInfo.Limits = nil
	m.mode = modeUsage
	out := stripANSI(m.usageView())
	if !strings.Contains(out, "no quota reading yet") {
		t.Errorf("the overlay said nothing about having nothing:\n%s", out)
	}
}

// A disabled balance is a feature switched off, not money already spent.
func TestUsageViewDisabledCredit(t *testing.T) {
	m := limitsModel(usageWindow)
	m.usageInfo.Limits.Credit = &proto.LimitCredit{Enabled: false}
	m.mode = modeUsage
	if out := stripANSI(m.usageView()); strings.Contains(out, "Extra credit") {
		t.Errorf("a disabled credit balance was drawn as a row:\n%s", out)
	}
}

// An uncapped balance has no ceiling to be a fraction of; the spend still shows.
func TestUsageViewUncappedCredit(t *testing.T) {
	m := limitsModel(usageWindow)
	used := 42.5
	m.usageInfo.Limits.Credit = &proto.LimitCredit{Enabled: true, UsedUSD: &used}
	m.mode = modeUsage
	out := stripANSI(m.usageView())
	if !strings.Contains(out, "$42.50 / uncapped") {
		t.Errorf("an uncapped balance was not spelled out:\n%s", out)
	}
}

func TestUsageOverlayOpensAndCloses(t *testing.T) {
	m := limitsModel(usageWindow)
	m.mode = modeDashboard
	opened := m.openUsage(modeDashboard)
	if opened.mode != modeUsage || opened.usageFrom != modeDashboard {
		t.Fatalf("openUsage left mode=%v from=%v", opened.mode, opened.usageFrom)
	}
	closed, _ := opened.closeUsage()
	if closed.(model).mode != modeDashboard {
		t.Errorf("closeUsage did not restore the dashboard: %v", closed.(model).mode)
	}
	// `u` cycles the footer segment from inside the overlay, without leaving it.
	cycled, _ := opened.handleUsageKey("u")
	if got := cycled.(model); got.mode != modeUsage || got.usageMode == opened.usageMode {
		t.Errorf("u left mode=%v usageMode=%v", got.mode, got.usageMode)
	}
}
