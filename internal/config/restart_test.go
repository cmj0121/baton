package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/cmj0121/baton/internal/cwd"
	"github.com/cmj0121/baton/internal/restart"
)

// TestRestartConfigParses: the on-disk form reads the way a human writes it, and
// the inline mapping puts the same four keys inside both the fleet-wide panel
// block and an agent profile.
func TestRestartConfigParses(t *testing.T) {
	var c Config
	src := `
panel:
  restart: on-failure
  restart-max: 3
  restart-backoff: 1s
  restart-healthy: 45s
  agents:
    claude:
      command: claude
      restart: never
`
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}

	fleet, err := c.Panel.Restart.Policy()
	if err != nil {
		t.Fatalf("fleet policy: %v", err)
	}
	want := restart.Policy{Mode: restart.OnFailure, Max: 3, Backoff: time.Second, Healthy: 45 * time.Second}
	if fleet != want {
		t.Errorf("fleet = %+v, want %+v", fleet, want)
	}

	agent, err := c.Panel.Agents["claude"].Restart.Policy()
	if err != nil {
		t.Fatalf("agent policy: %v", err)
	}
	if agent != (restart.Policy{Mode: restart.Never}) {
		t.Errorf("agent = %+v, want a mode-only override", agent)
	}
}

// TestRestartConfigEmptyIsUnset: a config that says nothing about restarting must
// produce a policy that restarts nothing, not one full of zeroes that reads as a
// configured policy.
func TestRestartConfigEmptyIsUnset(t *testing.T) {
	p, err := RestartConfig{}.Policy()
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsZero() {
		t.Fatalf("an empty block produced %+v, want the zero policy", p)
	}
}

// TestRestartConfigRejectsAlways: "always" is refused by name rather than aliased
// to on-failure, so the config never says something baton does not mean. The
// policy comes back unset — a fleet that does not restart is the safe failure.
func TestRestartConfigRejectsAlways(t *testing.T) {
	p, err := RestartConfig{Restart: "always"}.Policy()
	if err == nil {
		t.Fatal("always should be reported as unavailable")
	}
	if !strings.Contains(err.Error(), "always") || !strings.Contains(err.Error(), "on-failure") {
		t.Errorf("the error should name the value and the alternatives: %v", err)
	}
	if p.Mode != "" {
		t.Errorf("a refused mode should leave the policy unset, got %+v", p)
	}
}

// TestRestartConfigRejectsBadDurations: a malformed or negative wait is reported
// rather than silently becoming zero — a "wait" of zero is a spin.
func TestRestartConfigRejectsBadDurations(t *testing.T) {
	cases := map[string]RestartConfig{
		"unparseable backoff": {RestartBackoff: "soon"},
		"negative backoff":    {RestartBackoff: "-2s"},
		"unparseable healthy": {RestartHealthy: "a while"},
		"negative healthy":    {RestartHealthy: "-1m"},
	}
	for name, rc := range cases {
		p, err := rc.Policy()
		if err == nil {
			t.Errorf("%s: accepted silently as %+v", name, p)
		}
	}
}

// TestRestartConfigReportsEveryProblem: two bad fields produce two complaints,
// so one fix does not merely reveal the next.
func TestRestartConfigReportsEveryProblem(t *testing.T) {
	_, err := RestartConfig{Restart: "always", RestartBackoff: "soon", RestartHealthy: "-1m"}.Policy()
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"always", "restart-backoff", "restart-healthy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// TestParseOptionalDuration: empty means unset, not zero.
func TestParseOptionalDuration(t *testing.T) {
	for _, in := range []string{"", "   "} {
		d, err := parseOptionalDuration(in)
		if err != nil || d != 0 {
			t.Errorf("parseOptionalDuration(%q) = %v/%v, want 0/nil", in, d, err)
		}
	}
	if d, err := parseOptionalDuration(" 90s "); err != nil || d != 90*time.Second {
		t.Errorf("parseOptionalDuration(90s) = %v/%v", d, err)
	}
}

// TestCwdSettings: the two working-directory keys parse, and a value the file got
// wrong falls back to the default rather than to "off" — failing to learn a
// directory costs a convenience, not safety.
func TestCwdSettings(t *testing.T) {
	var c Config
	src := `
panel:
  track-cwd: proc
  restore-cwd: all
`
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Panel.CwdTracking(); err != nil || got != cwd.Proc {
		t.Errorf("track-cwd = %q/%v, want proc", got, err)
	}
	if got, err := c.Panel.CwdRestore(); err != nil || got != cwd.All {
		t.Errorf("restore-cwd = %q/%v, want all", got, err)
	}

	// Unset keeps the defaults, silently.
	var empty PanelDefaults
	if got, err := empty.CwdTracking(); err != nil || got != cwd.Auto {
		t.Errorf("unset track-cwd = %q/%v, want auto", got, err)
	}
	if got, err := empty.CwdRestore(); err != nil || got != cwd.Shells {
		t.Errorf("unset restore-cwd = %q/%v, want shells", got, err)
	}

	// A wrong value is reported and falls back.
	bad := PanelDefaults{TrackCwd: "sometimes", RestoreCwd: "true"}
	if got, err := bad.CwdTracking(); err == nil || got != cwd.Auto {
		t.Errorf("bad track-cwd = %q/%v, want auto and an error", got, err)
	}
	if got, err := bad.CwdRestore(); err == nil || got != cwd.Shells {
		t.Errorf("bad restore-cwd = %q/%v, want shells and an error", got, err)
	}
}
