package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/panel"
)

// Theming resolves the user's colour tokens (config.Theme, from TUI.yaml) into the
// cockpit's package colour vars on every config apply. The vars are the single
// source every render site already reads, so a theme change reaches the banner,
// borders, selection, tile titles, and LEDs without threading a palette through
// the render tree. An empty theme restores the built-in defaults, so an unset
// TUI.yaml looks identical to before theming existed.

// Built-in defaults, captured once at package load so a reload to an empty theme
// can restore them. They mirror the historical literals for colBrand / colBrandHi
// and the states LED map exactly.
var (
	defColBrand   = colBrand
	defColBrandHi = colBrandHi
	defLED        = captureLED()
)

// captureLED snapshots the default LED colour for each lifecycle state from the
// states map, so applyTheme can reset to it when a token is unset.
func captureLED() map[panel.State]lipgloss.Color {
	m := make(map[panel.State]lipgloss.Color, len(states))
	for st, info := range states {
		m[st] = info.color
	}
	return m
}

// applyTheme writes a user theme into the package colour vars, restoring the
// built-in default for every token the theme leaves empty. It then rebuilds the
// cached styles that bake in a brand colour. A malformed colour string is handed
// to lipgloss as-is (it renders an invalid colour as the terminal default), so a
// typo can never wedge the cockpit. Called from applyPrefs on boot and on reload.
func applyTheme(t config.Theme) {
	colBrand = pick(t.Brand, defColBrand)
	colBrandHi = pick(t.BrandHi, defColBrandHi)

	setLED(panel.Spawning, t.Spawning)
	setLED(panel.Running, t.Running)
	setLED(panel.Idle, t.Idle)
	setLED(panel.Attention, t.Attention)
	setLED(panel.Exited, t.Exited)

	rebuildThemedStyles()
}

// pick returns the override colour when set, else the default.
func pick(override string, def lipgloss.Color) lipgloss.Color {
	if override != "" {
		return lipgloss.Color(override)
	}
	return def
}

// setLED overrides one state's LED colour, or restores its default when unset.
// The states map carries glyph and label too; only the colour is themed.
func setLED(st panel.State, override string) {
	info := states[st]
	info.color = pick(override, defLED[st])
	states[st] = info
}

// rebuildThemedStyles recomputes the package style vars that cache a brand colour,
// so a theme change is reflected in the banner and the highlighted keycap.
func rebuildThemedStyles() {
	bannerStyle = lipgloss.NewStyle().Bold(true).Foreground(colBrand)
	keycapHotStyle = keycapStyle.Foreground(colDark).Background(colBrand).Bold(true)
}
