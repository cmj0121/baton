package main

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/attn"
	"github.com/cmj0121/baton/internal/config"
)

// TestAttentionPolicies covers the projection from config onto the fleet-wide
// quiet ladder and its per-profile overrides: an empty config runs on the
// built-in defaults, a profile restating one line inherits the rest, and an
// unreadable value is reported and dropped rather than applied.
func TestAttentionPolicies(t *testing.T) {
	t.Run("an empty config runs on the built-ins", func(t *testing.T) {
		fleet, perAgent := attentionPolicies(config.Config{})
		if !fleet.IsZero() || perAgent != nil {
			t.Fatalf("nothing configured should stay unset, got %+v / %+v", fleet, perAgent)
		}
		if fleet.Done() != attn.DefaultDoneAfter || fleet.Stuck() != attn.DefaultStuckAfter {
			t.Errorf("defaults = %v/%v", fleet.Done(), fleet.Stuck())
		}
	})

	t.Run("a profile restates one line and inherits the rest", func(t *testing.T) {
		fleet, perAgent := attentionPolicies(config.Config{Panel: config.PanelDefaults{
			Attention: config.AttentionConfig{DoneAfter: "90s", StuckAfter: "10m"},
			Agents: map[string]config.AgentProfile{
				"claude": {Command: "claude", Attention: config.AttentionConfig{StuckAfter: "30m"}},
			},
		}})
		if fleet.Done() != 90*time.Second || fleet.Stuck() != 10*time.Minute {
			t.Fatalf("fleet ladder = %v/%v", fleet.Done(), fleet.Stuck())
		}
		merged := fleet.Merge(perAgent["claude"])
		if merged.Done() != 90*time.Second || merged.Stuck() != 30*time.Minute {
			t.Fatalf("merged ladder = %v/%v, want 90s/30m", merged.Done(), merged.Stuck())
		}
	})

	t.Run("a profile that configures nothing is not carried", func(t *testing.T) {
		_, perAgent := attentionPolicies(config.Config{Panel: config.PanelDefaults{
			Agents: map[string]config.AgentProfile{"claude": {Command: "claude"}},
		}})
		if _, ok := perAgent["claude"]; ok {
			t.Error("a profile with no attention block should not enter the map")
		}
	})

	t.Run("an unreadable fleet ladder falls back to the built-ins", func(t *testing.T) {
		fleet, _ := attentionPolicies(config.Config{Panel: config.PanelDefaults{
			Attention: config.AttentionConfig{StuckAfter: "soon"},
		}})
		if fleet.Stuck() != attn.DefaultStuckAfter {
			t.Errorf("a bad value should leave the rung unset, got %v", fleet.Stuck())
		}
	})

	t.Run("an unreadable profile ladder falls back to the fleet", func(t *testing.T) {
		_, perAgent := attentionPolicies(config.Config{Panel: config.PanelDefaults{
			Attention: config.AttentionConfig{StuckAfter: "10m"},
			Agents: map[string]config.AgentProfile{
				"claude": {Command: "claude", Attention: config.AttentionConfig{StuckAfter: "-1m"}},
			},
		}})
		if _, ok := perAgent["claude"]; ok {
			t.Error("a profile whose ladder did not parse should be dropped entirely")
		}
	})

	t.Run("an inverted fleet ladder disables stuck", func(t *testing.T) {
		fleet, _ := attentionPolicies(config.Config{Panel: config.PanelDefaults{
			Attention: config.AttentionConfig{DoneAfter: "10m", StuckAfter: "1m"},
		}})
		if fleet.Stuck() != 0 {
			t.Errorf("a ladder whose rungs are out of order should switch stuck off, got %v", fleet.Stuck())
		}
		if fleet.Done() != 10*time.Minute {
			t.Errorf("the rung that was written should survive, got %v", fleet.Done())
		}
	})

	t.Run("an inverted merged ladder disables stuck for that profile only", func(t *testing.T) {
		// Neither block is wrong on its own: the profile only lengthens done-after,
		// and inherits a stuck-after that now sits below it.
		fleet, perAgent := attentionPolicies(config.Config{Panel: config.PanelDefaults{
			Attention: config.AttentionConfig{StuckAfter: "10m"},
			Agents: map[string]config.AgentProfile{
				"slow": {Command: "slow", Attention: config.AttentionConfig{DoneAfter: "30m"}},
			},
		}})
		if fleet.Stuck() != 10*time.Minute {
			t.Errorf("the fleet ladder should be untouched, got %v", fleet.Stuck())
		}
		if got := fleet.Merge(perAgent["slow"]); got.Stuck() != 0 {
			t.Errorf("stuck should be off for the profile, got %v", got.Stuck())
		}
	})
}

// TestReloadableSettingsCarriesAttention checks the ladder reaches the server
// settings, which is what makes it hot-reloadable on SIGHUP like every other
// knob rather than fixed at daemon start.
func TestReloadableSettingsCarriesAttention(t *testing.T) {
	rc := reloadableSettings(config.Config{Panel: config.PanelDefaults{
		Attention: config.AttentionConfig{DoneAfter: "90s"},
		Agents: map[string]config.AgentProfile{
			"claude": {Command: "claude", Attention: config.AttentionConfig{StuckAfter: "30m"}},
		},
	}})
	if rc.settings.Attention.Done() != 90*time.Second {
		t.Errorf("done-after did not reach the settings: %v", rc.settings.Attention.Done())
	}
	if got := rc.settings.AgentAttention["claude"].Stuck(); got != 30*time.Minute {
		t.Errorf("the profile override did not reach the settings: %v", got)
	}
}
