package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/panel"
)

// TestApplyThemeEmptyRestoresDefaults: applying an empty theme leaves every themed
// colour at its built-in default. This is the parity guarantee — an unset TUI.yaml
// renders identically to before theming existed.
func TestApplyThemeEmptyRestoresDefaults(t *testing.T) {
	// Dirty the palette first, then reset, to prove restore (not just no-op).
	applyTheme(config.Theme{Brand: "1", BrandHi: "2", Running: "3"})
	applyTheme(config.Theme{})
	t.Cleanup(func() { applyTheme(config.Theme{}) })

	if colBrand != defColBrand {
		t.Errorf("colBrand = %v, want default %v", colBrand, defColBrand)
	}
	if colBrandHi != defColBrandHi {
		t.Errorf("colBrandHi = %v, want default %v", colBrandHi, defColBrandHi)
	}
	for st, want := range defLED {
		if got := states[st].color; got != want {
			t.Errorf("LED %v = %v, want default %v", st, got, want)
		}
	}
}

// TestApplyThemeOverrides: a partial theme overrides only the tokens it names and
// leaves the rest at their defaults.
func TestApplyThemeOverrides(t *testing.T) {
	applyTheme(config.Theme{Brand: "200", Attention: "#abcdef"})
	t.Cleanup(func() { applyTheme(config.Theme{}) })

	if colBrand != lipgloss.Color("200") {
		t.Errorf("colBrand = %v, want 200", colBrand)
	}
	if colBrandHi != defColBrandHi {
		t.Errorf("colBrandHi changed to %v; only Brand was set", colBrandHi)
	}
	if got := states[panel.Attention].color; got != lipgloss.Color("#abcdef") {
		t.Errorf("attention LED = %v, want #abcdef", got)
	}
	if got := states[panel.Running].color; got != defLED[panel.Running] {
		t.Errorf("running LED changed to %v; only Attention was set", got)
	}
	// The cached banner style must track the override too.
	if got := bannerStyle.GetForeground(); got != lipgloss.Color("200") {
		t.Errorf("bannerStyle foreground = %v, want 200 (rebuild missed)", got)
	}
}

// TestApplyThemeNewTokens checks the three tokens the attention states brought
// with them. done and stuck are ordinary LEDs; failed is not a lifecycle state
// at all, so it themes a package var rather than a states entry — and it still
// has to restore to its built-in when the theme leaves it out.
func TestApplyThemeNewTokens(t *testing.T) {
	applyTheme(config.Theme{Done: "21", Stuck: "#ff8800", Failed: "196"})
	t.Cleanup(func() { applyTheme(config.Theme{}) })

	if got := states[panel.Done].color; got != lipgloss.Color("21") {
		t.Errorf("done LED = %v, want 21", got)
	}
	if got := states[panel.Stuck].color; got != lipgloss.Color("#ff8800") {
		t.Errorf("stuck LED = %v, want #ff8800", got)
	}
	if colFailed != lipgloss.Color("196") {
		t.Errorf("colFailed = %v, want 196", colFailed)
	}
	if got := stateInfoFor(panel.Panel{State: panel.Exited, ExitCode: 1}).color; got != lipgloss.Color("196") {
		t.Errorf("the failed rendering should follow the token, got %v", got)
	}

	applyTheme(config.Theme{})
	if colFailed != defColFailed {
		t.Errorf("an empty theme should restore colFailed, got %v want %v", colFailed, defColFailed)
	}
}

// TestApplyThemeKeepsGlyphAndLabel: theming a state recolours its LED without
// disturbing its glyph or label.
func TestApplyThemeKeepsGlyphAndLabel(t *testing.T) {
	before := states[panel.Idle]
	applyTheme(config.Theme{Idle: "99"})
	t.Cleanup(func() { applyTheme(config.Theme{}) })

	got := states[panel.Idle]
	if got.led != before.led || got.label != before.label {
		t.Errorf("glyph/label changed: %+v vs %+v", got, before)
	}
	if got.color != lipgloss.Color("99") {
		t.Errorf("idle LED = %v, want 99", got.color)
	}
}
