package config

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/usage"
)

// TestUsageWindowDuration: an empty field takes the built-in window, an explicit
// zero opts out of the countdown, and a typo falls back to the default rather
// than silently disabling the number the feature exists for.
func TestUsageWindowDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"":         usage.DefaultWindow,
		"5h":       5 * time.Hour,
		" 168h ":   168 * time.Hour,
		"90m":      90 * time.Minute,
		"0":        0,
		"-1h":      0,
		"nonsense": usage.DefaultWindow,
	}
	for in, want := range cases {
		if got := (UsageConfig{Window: in}).WindowDuration(); got != want {
			t.Errorf("Window %q = %v, want %v", in, got, want)
		}
	}
}

// TestUsageThresholds: a pair that does not describe rising pressure is rejected
// wholesale rather than half-honoured, so the segment's colours always mean what
// they look like.
func TestUsageThresholds(t *testing.T) {
	cases := []struct {
		name         string
		warn, alarm  float64
		wantW, wantA float64
	}{
		{"unset", 0, 0, DefaultUsageWarnAt, DefaultUsageAlarmAt},
		{"honoured", 0.5, 0.8, 0.5, 0.8},
		{"alarm at the top", 0.5, 1, 0.5, 1},
		{"alarm below warn", 0.9, 0.5, DefaultUsageWarnAt, DefaultUsageAlarmAt},
		{"equal", 0.6, 0.6, DefaultUsageWarnAt, DefaultUsageAlarmAt},
		{"warn out of range", 1.5, 0.9, DefaultUsageWarnAt, DefaultUsageAlarmAt},
		{"alarm over one", 0.5, 1.2, DefaultUsageWarnAt, DefaultUsageAlarmAt},
		{"only one set", 0.5, 0, DefaultUsageWarnAt, DefaultUsageAlarmAt},
	}
	for _, c := range cases {
		w, a := (UsageConfig{WarnAt: c.warn, AlarmAt: c.alarm}).Thresholds()
		if w != c.wantW || a != c.wantA {
			t.Errorf("%s: thresholds = %v/%v, want %v/%v", c.name, w, a, c.wantW, c.wantA)
		}
	}
}
