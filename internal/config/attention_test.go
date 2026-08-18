package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/cmj0121/baton/internal/attn"
)

// TestAttentionPolicy covers the parse of each rung: unset inherits, a duration
// is taken as written, an explicit zero switches the rung off (attn.Never), and
// a negative or unparseable value is reported and dropped rather than applied.
func TestAttentionPolicy(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name       string
		cfg        AttentionConfig
		wantQuiet  bool
		wantDone   time.Duration
		wantStuck  time.Duration
		wantErrHas string
	}{
		{"unset inherits everything", AttentionConfig{}, true, attn.DefaultDoneAfter, attn.DefaultStuckAfter, ""},
		{"explicit on", AttentionConfig{DoneOnQuiet: &on}, true, attn.DefaultDoneAfter, attn.DefaultStuckAfter, ""},
		{"explicit off", AttentionConfig{DoneOnQuiet: &off}, false, attn.DefaultDoneAfter, attn.DefaultStuckAfter, ""},
		{"durations as written", AttentionConfig{DoneAfter: "90s", StuckAfter: "30m"}, true, 90 * time.Second, 30 * time.Minute, ""},
		{"zero switches a rung off", AttentionConfig{StuckAfter: "0"}, true, attn.DefaultDoneAfter, 0, ""},
		{"unparseable done-after is dropped", AttentionConfig{DoneAfter: "soon"}, true, attn.DefaultDoneAfter, attn.DefaultStuckAfter, "panel.done-after"},
		{"negative stuck-after is dropped", AttentionConfig{StuckAfter: "-5m"}, true, attn.DefaultDoneAfter, attn.DefaultStuckAfter, "panel.stuck-after"},
		{"both bad are both reported", AttentionConfig{DoneAfter: "x", StuckAfter: "y"}, true, attn.DefaultDoneAfter, attn.DefaultStuckAfter, "panel.done-after"},
	}
	for _, tc := range cases {
		p, err := tc.cfg.Policy()
		if tc.wantErrHas == "" && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if tc.wantErrHas != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErrHas)) {
			t.Errorf("%s: error = %v, want one naming %q", tc.name, err, tc.wantErrHas)
		}
		if p.DoneQuiet() != tc.wantQuiet || p.Done() != tc.wantDone || p.Stuck() != tc.wantStuck {
			t.Errorf("%s: quiet=%v done=%v stuck=%v, want %v/%v/%v",
				tc.name, p.DoneQuiet(), p.Done(), p.Stuck(), tc.wantQuiet, tc.wantDone, tc.wantStuck)
		}
	}

	// Both fields are reported, so one typo cannot hide the other.
	_, err := AttentionConfig{DoneAfter: "x", StuckAfter: "y"}.Policy()
	if err == nil || !strings.Contains(err.Error(), "panel.stuck-after") {
		t.Errorf("both bad fields should be reported, got %v", err)
	}

	// A profile's error names the profile's own line, not the fleet-wide one the
	// user would find perfectly fine.
	_, err = AttentionConfig{StuckAfter: "soon"}.ProfilePolicy("claude")
	if err == nil || !strings.Contains(err.Error(), "panel.agents.claude.stuck-after") {
		t.Errorf("a profile error should name the profile's key, got %v", err)
	}
}

// TestAttentionInline checks the knobs really are inlined into both the panel
// block and an agent profile, so a profile can restate one line and nothing else.
func TestAttentionInline(t *testing.T) {
	const src = `
panel:
  stuck-after: 10m
  agents:
    claude:
      command: claude
      stuck-after: 30m
`
	var c Config
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fleet, err := c.Panel.Attention.Policy()
	if err != nil {
		t.Fatalf("fleet policy: %v", err)
	}
	if fleet.Stuck() != 10*time.Minute {
		t.Errorf("fleet stuck-after = %v, want 10m", fleet.Stuck())
	}
	prof, err := c.Panel.Agents["claude"].Attention.ProfilePolicy("claude")
	if err != nil {
		t.Fatalf("profile policy: %v", err)
	}
	if got := fleet.Merge(prof); got.Stuck() != 30*time.Minute || got.Done() != attn.DefaultDoneAfter {
		t.Errorf("merged = %v/%v, want 60s/30m", got.Done(), got.Stuck())
	}
}

// TestAttentionInlineZeroIsNever checks "0 = never" survives the YAML parser: a
// bare 0 is a value yaml reads happily, and it has to come out the other side as
// "switch this rung off" rather than as "the user said nothing", which would
// hand the profile the fleet-wide threshold it was trying to escape.
func TestAttentionInlineZeroIsNever(t *testing.T) {
	const src = `
panel:
  stuck-after: 10m
  agents:
    shellbot:
      command: bash
      stuck-after: 0
`
	var c Config
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fleet, err := c.Panel.Attention.Policy()
	if err != nil {
		t.Fatalf("fleet policy: %v", err)
	}
	prof, err := c.Panel.Agents["shellbot"].Attention.ProfilePolicy("shellbot")
	if err != nil {
		t.Fatalf("profile policy: %v", err)
	}
	if prof.StuckAfter != attn.Never {
		t.Errorf("an explicit 0 should parse to attn.Never, got %v", prof.StuckAfter)
	}
	if got := fleet.Merge(prof); got.Stuck() != 0 {
		t.Errorf("the profile should switch stuck off, got %v", got.Stuck())
	}
}
