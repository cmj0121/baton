package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/config"
)

// Keybindings follow a tmux-style prefix model: press the prefix, then a verb.
//
//	prefix          ctrl-t      enter "waiting for a binding" mode
//	prefix + p      new panel   create a panel (default: shell)
//	prefix + d      dashboard   switch to the dashboard view
//	prefix + k      keys        toggle the in-view key map
//	prefix + q      detach      detach this client (the server keeps running)
//
// Inside a view the cursor moves with the arrow keys or hjkl, and (in the key
// map) enter runs the highlighted binding — so the whole surface is reachable
// without memorising the chords.
const (
	keyPrefix      = "ctrl+t"
	keyNewPanel    = "p"
	keyClose       = "w"
	keyDashboard   = "d"
	keyShowMap     = "k"
	keyPanelConfig = "P" // shift+p
	keyRestart     = "S" // shift+s
	keyDetach      = "q"

	keyCtrlC = "ctrl+c" // bare emergency quit
)

// keyLabel renders a key string as a compact label: ctrl+x → C-x, alt+x → M-x,
// otherwise the key as typed.
func keyLabel(key string) string {
	switch {
	case strings.HasPrefix(key, "ctrl+"):
		return "C-" + strings.TrimPrefix(key, "ctrl+")
	case strings.HasPrefix(key, "alt+"):
		return "M-" + strings.TrimPrefix(key, "alt+")
	default:
		return key
	}
}

// action is the verb a binding performs; the prefix handler and the navigable
// key map both resolve to one of these, so they can never drift apart.
type action int

const (
	actNewPanel action = iota
	actClose
	actDashboard
	actToggleMap
	actPanelConfig
	actRestart
	actDetach
)

// binding is one prefixed command: a stable name (used to persist the chord),
// the bare key pressed after the prefix, the human description, and the action
// it triggers.
type binding struct {
	name string // stable id for the config file, e.g. "new-panel"
	key  string // bare key after the prefix, e.g. "p"
	desc string
	act  action
}

// bindings lists the prefixed commands in display order. This is the single
// source of truth for the footer hint, the in-view key map, and the config keys.
var bindings = []binding{
	{"new-panel", keyNewPanel, "spawn a new shell panel", actNewPanel},
	{"close", keyClose, "close the selected panel", actClose},
	{"dashboard", keyDashboard, "jump back to the dashboard", actDashboard},
	{"key-map", keyShowMap, "toggle this key map", actToggleMap},
	{"panel-config", keyPanelConfig, "configure panel defaults", actPanelConfig},
	{"restart", keyRestart, "force-restart the server", actRestart},
	{"detach", keyDetach, "detach (server keeps running)", actDetach},
}

// prefs is the cockpit state persisted to $HOME/.baton/config.
type prefs struct {
	prefix       string
	binds        []binding
	confirmClose bool
	shellPath    string
}

// loadPrefs reads the config file, returning defaults for anything missing or on
// any read error (so the cockpit always comes up). Defaults: prefix "ctrl+t",
// confirm-on-close on, system shell.
func loadPrefs() prefs {
	p := prefs{prefix: keyPrefix, binds: append([]binding(nil), bindings...), confirmClose: true}

	cfg, err := config.Load()
	if err != nil {
		return p
	}
	if cfg.Prefix != "" {
		p.prefix = cfg.Prefix
	}
	for i := range p.binds {
		if k := cfg.Keys[p.binds[i].name]; k != "" {
			p.binds[i].key = k
		}
	}
	if cfg.Settings.ConfirmClose != nil {
		p.confirmClose = *cfg.Settings.ConfirmClose
	}
	p.shellPath = cfg.Panel.Shell
	return p
}

// saveConfig persists the cockpit's whole config (prefix, key map, settings, and
// panel defaults) from the model, so saving one part never drops another.
func (m model) saveConfig() error {
	keys := make(map[string]string, len(m.keymap()))
	for _, b := range m.keymap() {
		keys[b.name] = b.key
	}
	confirmClose := m.confirmClose
	return config.Config{
		Prefix:   m.effPrefix(),
		Keys:     keys,
		Settings: config.Settings{ConfirmClose: &confirmClose},
		Panel:    config.PanelDefaults{Shell: m.shellPath},
	}.Save()
}

// --- keycap rendering ---------------------------------------------------------

var (
	keycapStyle = lipgloss.NewStyle().
			Foreground(colInk).
			Background(lipgloss.Color("238")).
			Padding(0, 1)

	keycapHotStyle = keycapStyle.
			Foreground(colDark).
			Background(colBrand).
			Bold(true)
)

// chord renders a prefixed binding as two keycaps, e.g. [C-t][p], using prefix
// as the leader label. When hot the caps glow in the brand colour (used for the
// selected key-map row).
func chord(prefix, key string, hot bool) string {
	cap := keycapStyle
	if hot {
		cap = keycapHotStyle
	}
	return cap.Render(prefix) + " " + cap.Render(key)
}
