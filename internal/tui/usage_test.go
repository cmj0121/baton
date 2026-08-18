package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// usageNow is the cockpit clock the countdown is measured against.
var usageNow = time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)

// usageModel builds a cockpit showing two agent panels, with a window that
// opened an hour ago and resets four hours from now. Only the first panel has
// been attributed any spend.
func usageModel(mode usageMode) model {
	since := usageNow.Add(-time.Hour)
	return model{
		width: 200, height: 30, now: usageNow, usageMode: mode,
		usageText: "1.2M tok · ≈$12.34 API",
		fleet: []panel.Panel{
			{ID: "p1", Kind: panel.Agent, Title: "claude · api"},
			{ID: "p2", Kind: panel.Agent, Title: "claude · web"},
		},
		usageInfo: &proto.UsageInfo{
			Tokens: 1_200_000, CostUSD: 12.34, Source: "local", Resets: true,
			Since:           since.Format(time.RFC3339),
			Until:           since.Add(5 * time.Hour).Format(time.RFC3339),
			WarnAt:          0.75,
			AlarmAt:         0.9,
			CountdownFormat: "auto",
			Panels: map[string]proto.PanelUsage{
				"p1": {Tokens: 300_000, CostUSD: 3},
			},
		},
	}
}

// TestUsageModeCycle: U walks off → window → panel → off, so one key covers both
// views without taking a second binding.
func TestUsageModeCycle(t *testing.T) {
	seen := []usageMode{}
	m := usageOff
	for range len(usageModes) {
		m = m.next()
		seen = append(seen, m)
	}
	want := []usageMode{usageWindow, usagePanel, usageOff}
	for i, w := range want {
		if seen[i] != w {
			t.Fatalf("cycle = %v, want %v", seen, want)
		}
	}
	if usageMode(42).next() != usageWindow {
		t.Error("an unknown mode should land on the window view")
	}
}

// TestParseUsageMode: the persisted names round-trip, and anything unrecognised
// lands on the view the segment has always shown.
func TestParseUsageMode(t *testing.T) {
	for _, m := range usageModes {
		if got := parseUsageMode(m.String()); got != m {
			t.Errorf("parseUsageMode(%q) = %v, want %v", m.String(), got, m)
		}
	}
	if parseUsageMode("  PANEL ") != usagePanel {
		t.Error("the name should parse case- and space-insensitively")
	}
	if parseUsageMode("nonsense") != usageWindow {
		t.Error("an unknown name should default to the window view")
	}
}

// TestUsageCapOff: the off mode renders nothing at all, so the strip stays clean.
func TestUsageCapOff(t *testing.T) {
	if got := usageModel(usageOff).usageCap(); got != "" {
		t.Errorf("usageCap() = %q, want empty when off", got)
	}
}

// TestUsageCapWindow: the window view carries the spend and the countdown to the
// reset, computed on the cockpit's own clock rather than the daemon's poll.
func TestUsageCapWindow(t *testing.T) {
	got := usageModel(usageWindow).usageCap()
	if !strings.Contains(got, "1.2M tok") {
		t.Errorf("window view lost the spend: %q", got)
	}
	if !strings.Contains(got, "4:00:00") {
		t.Errorf("window view should count down to the reset, got %q", got)
	}
}

// TestUsageCapWindowNoReset: a source that cannot see a reset shows the spend and
// no countdown — an invented one would be worse than none.
func TestUsageCapWindowNoReset(t *testing.T) {
	m := usageModel(usageWindow)
	m.usageInfo.Resets = false
	got := m.usageCap()
	if !strings.Contains(got, "1.2M tok") {
		t.Errorf("the spend should still show: %q", got)
	}
	if strings.Contains(got, "⏳") {
		t.Errorf("no reset means no countdown, got %q", got)
	}
}

// TestUsageCapWindowPastTheReset is the regression for a segment that hung at
// 0:00:00. The cockpit ticks the payload it was last sent once a second while the
// daemon polls once every thirty, so the held window routinely outlives itself —
// and the countdown used to floor at zero and rest there until something newer
// arrived, which, with a daemon that had stopped answering, was never.
//
// Walking the cockpit's clock through and past the reset: the countdown falls,
// and the moment the window is over it is gone rather than pinned at zero. The
// spend stays — that number is still the last thing known to be true.
func TestUsageCapWindowPastTheReset(t *testing.T) {
	m := usageModel(usageWindow)
	for _, tick := range []struct {
		on   time.Duration
		want string
	}{
		{0, "4:00:00"},
		{2 * time.Hour, "2:00:00"},
		{4*time.Hour - time.Second, "0:00:01"},
	} {
		m.now = usageNow.Add(tick.on)
		if got := m.usageCap(); !strings.Contains(got, tick.want) {
			t.Errorf("%v on, segment = %q, want a countdown of %s", tick.on, got, tick.want)
		}
	}

	for _, on := range []time.Duration{4 * time.Hour, 4*time.Hour + time.Second, 30 * time.Hour} {
		m.now = usageNow.Add(on)
		got := m.usageCap()
		if strings.Contains(got, "⏳") || strings.Contains(got, "0:00:00") {
			t.Errorf("%v on, the window is over: segment = %q, want no countdown at all", on, got)
		}
		if !strings.Contains(got, "1.2M tok") {
			t.Errorf("%v on, the spend should still show: %q", on, got)
		}
	}
}

// TestUsagePanelView: the panel view answers "who is burning it" — the focused
// panel's spend and its share of the window, which is the number the decision to
// stop an agent is made on.
func TestUsagePanelView(t *testing.T) {
	m := usageModel(usagePanel)
	m.cursor = 0
	got := m.usageCap()
	if !strings.Contains(got, "300.0K tok") {
		t.Errorf("panel view lost the panel's spend: %q", got)
	}
	if !strings.Contains(got, "25%") {
		t.Errorf("panel view should show the share of the window, got %q", got)
	}
}

// TestUsagePanelUnattributed: a panel the daemon cannot attribute says so rather
// than showing a zero that reads as "this one is free".
func TestUsagePanelUnattributed(t *testing.T) {
	m := usageModel(usagePanel)
	m.cursor = 1 // p2, which the window never saw
	got := m.usageCap()
	if !strings.Contains(got, "not attributed") {
		t.Errorf("an unattributed panel should say so, got %q", got)
	}
	if strings.Contains(got, "0 tok") {
		t.Errorf("an unattributed panel must not render as zero spend, got %q", got)
	}
}

// TestUsagePanelRollsUpAGroup: a group is one work item, and "which one do I
// stop" is asked of work items as often as of single panels — so selecting a
// group sums its members.
func TestUsagePanelRollsUpAGroup(t *testing.T) {
	m := usageModel(usagePanel)
	m.fleet[0].Group = "api"
	m.fleet[1].Group = "api"
	m.usageInfo.Panels["p2"] = proto.PanelUsage{Tokens: 300_000}
	m.cursor = 0

	title, ids, ok := m.usageFocus()
	if !ok || title != "api" || len(ids) != 2 {
		t.Fatalf("focus = %q/%v/%v, want the group and both members", title, ids, ok)
	}
	if got := m.usageCap(); !strings.Contains(got, "600.0K tok") {
		t.Errorf("a group should roll up its members, got %q", got)
	}
}

// TestUsagePressureColour: the fill tracks how far into the window the account
// is — the point is to act before it runs out — and does not move at all when
// there is no window to measure against.
func TestUsagePressureColour(t *testing.T) {
	m := usageModel(usageWindow)
	if got := m.usagePressureColor(); got != colBlue {
		t.Errorf("one hour into five = %v, want the calm fill", got)
	}

	m.now = usageNow.Add(3 * time.Hour) // 4h of 5h spent = 0.8
	if got := m.usagePressureColor(); got != colAmber {
		t.Errorf("past the warning = %v, want amber", got)
	}

	m.now = usageNow.Add(3*time.Hour + 45*time.Minute) // 4.75h of 5h = 0.95
	if got := m.usagePressureColor(); got != colRed {
		t.Errorf("past the alarm = %v, want red", got)
	}

	// Once the window has run out the colour stops reporting too, rather than
	// staying locked on red while the countdown beside it has already gone.
	m.now = usageNow.Add(4 * time.Hour)
	if got := m.usagePressureColor(); got != colBlue {
		t.Errorf("a window that has run out = %v, want no pressure reading at all", got)
	}

	m.usageInfo.Resets = false
	if got := m.usagePressureColor(); got != colBlue {
		t.Errorf("no window means no pressure reading, got %v", got)
	}

	bare := model{now: usageNow, usageMode: usageWindow}
	if got := bare.usagePressureColor(); got != colBlue {
		t.Errorf("no payload at all = %v, want the calm fill", got)
	}
}

// TestUsageModeSurvivesTheOldSetting: a config written before the segment gained
// views still hides what it was hiding, and the richer setting wins once present.
func TestUsageModeSurvivesTheOldSetting(t *testing.T) {
	off := false
	on := true
	cases := []struct {
		name string
		cfg  config.Config
		want usageMode
	}{
		{"nothing set", config.Config{}, usageWindow},
		{"old off", config.Config{Settings: config.Settings{UsageFooter: &off}}, usageOff},
		{"old on", config.Config{Settings: config.Settings{UsageFooter: &on}}, usageWindow},
		{"new wins", config.Config{Settings: config.Settings{UsageFooter: &off, UsageMode: "panel"}}, usagePanel},
		{"new alone", config.Config{Settings: config.Settings{UsageMode: "off"}}, usageOff},
	}
	for _, c := range cases {
		if got := prefsFromConfig(c.cfg).usageMode; got != c.want {
			t.Errorf("%s: usageMode = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestUsageModeLabelsAreLocalised: the status line after U reads in the cockpit's
// language, since it is the only feedback that the key did anything.
func TestUsageModeLabelsAreLocalised(t *testing.T) {
	for _, m := range usageModes {
		if m.label(i18n.EN) == "" {
			t.Errorf("mode %v has no English label", m)
		}
		if m.label(i18n.ZhTW) == m.label(i18n.EN) {
			t.Errorf("mode %v is untranslated", m)
		}
	}
}

// TestJoinDot: the separator is dropped rather than left dangling when one side
// is missing.
func TestJoinDot(t *testing.T) {
	cases := [][3]string{
		{"a", "b", "a · b"},
		{"a", "", "a"},
		{"", "b", "b"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := joinDot(c[0], c[1]); got != c[2] {
			t.Errorf("joinDot(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// TestHumanTokens: the panel view abbreviates the same way the daemon renders the
// account total, so the two lines read as one system.
func TestHumanTokens(t *testing.T) {
	cases := map[int64]string{
		1_234_567: "1.2M tok",
		9_340:     "9.3K tok",
		512:       "512 tok",
		0:         "0 tok",
	}
	for n, want := range cases {
		if got := humanTokens(n); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", n, got, want)
		}
	}
}
