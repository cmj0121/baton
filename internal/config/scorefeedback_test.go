package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestFeedbackIsOnDefaultsOn is the half of the score config that decides whether
// a fleet can ever fill its own memory. Unset must mean ON: an empty store renders
// no block at all, so a default install whose briefs carry no hint tells its
// agents nothing, and a subsystem that is enabled, healthy and permanently silent
// is indistinguishable from one that is switched off.
func TestFeedbackIsOnDefaultsOn(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		name string
		cfg  ScoreConfig
		want bool
	}{
		{"absent section", ScoreConfig{}, true},
		{"explicit false", ScoreConfig{Feedback: &off}, false},
		{"explicit true", ScoreConfig{Feedback: &on}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.FeedbackIsOn(); got != tc.want {
				t.Errorf("FeedbackIsOn() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestFeedbackIsIndependentOfEnabled pins the two apart. They answer different
// questions — whether the memory runs at all, and whether agents are told they
// may write to it — and folding them into one accessor here would take from the
// daemon the ability to say which of the two is why a brief carries no hint.
func TestFeedbackIsIndependentOfEnabled(t *testing.T) {
	off := false
	cfg := ScoreConfig{Enabled: &off}
	if !cfg.FeedbackIsOn() {
		t.Error("FeedbackIsOn() = false for a config that only switched the STORE off; want the two keys independent")
	}
	cfg = ScoreConfig{Feedback: &off}
	if !cfg.IsEnabled() {
		t.Error("IsEnabled() = false for a config that only switched the HINT off; want the two keys independent")
	}
}

// TestFeedbackKeysParse reads both keys out of a file rather than a literal,
// because the thing that breaks is the yaml tag and a struct literal cannot see
// it. A profile that never names the key must stay nil and not default to false:
// nil is what the daemon reads as "inherit the fleet's answer", so a tag typo
// that turned every unset profile into an explicit no would switch the hint off
// fleet-wide with nothing in the file saying so.
func TestFeedbackKeysParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	const src = `
score:
    feedback: false
panel:
    agents:
        chatty:
            command: claude
            score-feedback: true
        quiet:
            command: claude
            score-feedback: false
        silent:
            command: claude
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Score.FeedbackIsOn() {
		t.Error("score.feedback: false did not reach ScoreConfig.Feedback")
	}
	for name, want := range map[string]*bool{"chatty": boolp(true), "quiet": boolp(false), "silent": nil} {
		got := cfg.Panel.Agents[name].ScoreFeedback
		switch {
		case want == nil && got != nil:
			t.Errorf("%s: score-feedback = %v; want nil, so the profile inherits the fleet's answer", name, *got)
		case want != nil && got == nil:
			t.Errorf("%s: score-feedback = nil; want %v", name, *want)
		case want != nil && got != nil && *want != *got:
			t.Errorf("%s: score-feedback = %v; want %v", name, *got, *want)
		}
	}
}

func boolp(b bool) *bool { return &b }
