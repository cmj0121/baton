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
	keyNewForm     = "n"
	keyClose       = "w"
	keyPurge       = "x"
	keyDashboard   = "d"
	keyShowMap     = "k"
	keyPanelConfig = "P" // shift+p
	keyRestart     = "S" // shift+s
	keyDetach      = "q"

	keyCtrlC = "ctrl+c" // bare emergency quit

	// Single dashboard keys for grouping — direct, not behind the prefix, so a
	// work item is one keystroke away. Surfaced in the key map for discoverability.
	keyMark    = "g" // mark / unmark the selected item
	keyGroup   = "G" // group the marked panels (shift+g)
	keyUngroup = "u" // dissolve the selected work item
	keyRename  = "n" // rename the selected panel or group

	// keyGroupBack steps back out of a group view. It is a prefixed shortcut
	// (BIND-g), so it works safely inside a live zoom where a bare letter would be
	// stolen by the program. It deliberately shares the "g" key with keyMark:
	// they never collide because bare keys only resolve on the dashboard and
	// prefixed ones only after the prefix in the zoom/group handlers.
	keyGroupBack = "g"
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
	actNewForm
	actClose
	actPurge
	actDashboard
	actToggleMap
	actPanelConfig
	actRestart
	actDetach

	// Group verbs, triggered by bare keys on the dashboard (and, for back, inside
	// a group/zoom view) rather than after the prefix.
	actMark
	actGroup
	actUngroup
	actRename
	actGroupBack
)

// binding is one editable command: a stable name (used to persist the key), the
// trigger key, the human description, the action it runs, and the purpose section
// it belongs to in the key map. Most fire after the prefix; bare bindings (the
// dashboard group verbs) fire on their own keystroke.
type binding struct {
	name string // stable id for the config file, e.g. "new-panel"
	key  string // the key — pressed after the prefix, or bare when bare is set
	desc string
	act  action
	bare bool   // triggered directly, not behind the prefix
	cat  string // purpose section header in the key map
}

// bindings lists every editable command grouped by purpose — the order the key
// map shows them, and tab jumps between these groups. This is the single source
// of truth for the in-view key map and the config keys.
var bindings = []binding{
	{"new-panel", keyNewPanel, "spawn a new shell panel", actNewPanel, false, "Panels"},
	{"new-panel-form", keyNewForm, "new panel (choose the command)", actNewForm, false, "Panels"},
	{"close", keyClose, "close the selected panel", actClose, false, "Panels"},
	{"purge-exited", keyPurge, "purge all exited panels", actPurge, false, "Panels"},

	{"dashboard", keyDashboard, "jump back to the dashboard", actDashboard, false, "View"},
	{"key-map", keyShowMap, "toggle this key map", actToggleMap, false, "View"},
	{"panel-config", keyPanelConfig, "configure panel defaults", actPanelConfig, false, "View"},

	{"mark", keyMark, "mark a panel for grouping", actMark, true, "Work items"},
	{"group", keyGroup, "group the marked panels", actGroup, true, "Work items"},
	{"ungroup", keyUngroup, "ungroup the selected work item", actUngroup, true, "Work items"},
	{"rename", keyRename, "rename the panel or group", actRename, true, "Work items"},
	{"group-back", keyGroupBack, "step back out of a group view", actGroupBack, false, "Work items"},

	{"restart", keyRestart, "force-restart the server", actRestart, false, "Session"},
	{"detach", keyDetach, "detach (server keeps running)", actDetach, false, "Session"},
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
