package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/config"
)

// Keybindings are modal. In the command-mode views (dashboard, group split) an
// action fires on a single key. In a zoom the keys reach the live program, so an
// action fires on the prefix then the key. Two escapes — dashboard and group
// view — are bound to the prefix and work in every mode.
//
//	dashboard:  p new · w close · g mark · G group · ? key map · …  (single keys)
//	zoom:       C-t p new · C-t w close · …                          (prefix + key)
//	any mode:   C-t d dashboard · C-t g group view                  (escapes)
const (
	keyPrefix      = "ctrl+t"
	keyNewPanel    = "p"
	keyNewForm     = "c" // "choose the command" (n is rename)
	keyClose       = "w"
	keyPurge       = "x"
	keyHelp        = "?" // view the key list for the current view
	keyEditMap     = "k" // edit the key map (prefix only: C-t k)
	keyPanelConfig = "P" // shift+p
	keyRestart     = "S" // shift+s
	keyDetach      = "q"

	keyMark    = "g" // mark / unmark the selected item
	keyGroup   = "G" // group the marked panels (shift+g)
	keyAdd     = "a" // add the marked panels to the selected group
	keyUngroup = "u" // dissolve the selected work item
	keyRename  = "e" // edit the name of the selected panel or group

	// The two universal escapes, bound to the prefix in every mode.
	keyDashboard = "d" // C-t d → the dashboard
	keyGroupView = "g" // C-t g → the group view (the split, or back from a zoom)

	keyRemove   = "x" // in the group split: remove the focused member from the group
	keyInteract = "i" // in the group split: drive the focused tile in place, no zoom

	keyCtrlC = "ctrl+c" // captured in command mode — exit is the detach binding only
	keyCtrlE = "ctrl+e" // captured in command mode — exit is the detach binding only
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
	actHelp
	actPanelConfig
	actRestart
	actDetach

	actMark
	actGroup
	actAdd
	actUngroup
	actRename

	// Escapes — bound to the prefix in every mode.
	actDashboard
	actGroupView
	actEditMap
)

// isEscape reports whether an action is reached after the prefix rather than on a
// bare key — lookupCmd skips these, lookupEscape resolves them. The dashboard and
// group-view jumps and the key-map editor work after the prefix in every mode;
// panel config opens this way from command mode.
func isEscape(a action) bool {
	return a == actDashboard || a == actGroupView || a == actEditMap || a == actPanelConfig
}

// binding is one editable command: a stable name (used to persist the key), the
// trigger key, the human description, the action it runs, and the purpose section
// it groups under in the key map. Commands fire on a single key in command mode
// and after the prefix in a zoom; the escapes always fire after the prefix.
type binding struct {
	name string // stable id for the config file, e.g. "new-panel"
	key  string
	desc string
	act  action
	cat  string // purpose section header in the key map
}

// bindings lists every editable command grouped by purpose — the order the key
// map shows them, and tab jumps between these groups. This is the single source
// of truth for the in-view key map and the config keys.
var bindings = []binding{
	{"new-panel", keyNewPanel, "spawn a new shell panel", actNewPanel, "Panels"},
	{"new-panel-form", keyNewForm, "new panel (choose the command)", actNewForm, "Panels"},
	{"close", keyClose, "close the selected panel", actClose, "Panels"},
	{"purge-exited", keyPurge, "purge all exited panels", actPurge, "Panels"},

	{"mark", keyMark, "mark a panel for grouping", actMark, "Work items"},
	{"group", keyGroup, "group the marked panels", actGroup, "Work items"},
	{"add", keyAdd, "add the marked panels to the selected group", actAdd, "Work items"},
	{"ungroup", keyUngroup, "ungroup the selected work item", actUngroup, "Work items"},
	{"rename", keyRename, "rename the panel or group", actRename, "Work items"},

	{"help", keyHelp, "view the keys for this view", actHelp, "View"},
	{"key-map", keyEditMap, "edit the key map (prefix)", actEditMap, "View"},
	{"panel-config", keyPanelConfig, "configure panel defaults (prefix)", actPanelConfig, "View"},
	{"dashboard", keyDashboard, "jump to the dashboard (prefix)", actDashboard, "View"},
	{"group-view", keyGroupView, "go to the group view (prefix)", actGroupView, "View"},

	{"restart", keyRestart, "force-restart the server", actRestart, "Session"},
	{"detach", keyDetach, "detach (server keeps running)", actDetach, "Session"},
}

// prefs is the cockpit state persisted to $HOME/.baton/config.
type prefs struct {
	prefix            string
	binds             []binding
	confirmClose      bool
	allowNameConflict bool
	shellPath         string
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
	if cfg.Settings.AllowNameConflict != nil {
		p.allowNameConflict = *cfg.Settings.AllowNameConflict
	}
	p.shellPath = cfg.Panel.Shell
	return p
}

// saveConfig persists the cockpit's whole config (prefix, key map, settings, and
// panel defaults) from the model, so saving one part never drops another. Only
// keys the user has changed from the default are written, so a later change to a
// default flows through instead of being masked by a stale persisted value.
func (m model) saveConfig() error {
	def := make(map[string]string, len(bindings))
	for _, b := range bindings {
		def[b.name] = b.key
	}
	keys := make(map[string]string)
	for _, b := range m.keymap() {
		if b.key != def[b.name] {
			keys[b.name] = b.key
		}
	}
	prefix := ""
	if m.effPrefix() != keyPrefix {
		prefix = m.effPrefix()
	}
	confirmClose := m.confirmClose
	allowNameConflict := m.allowNameConflict
	return config.Config{
		Prefix: prefix,
		Keys:   keys,
		Settings: config.Settings{
			ConfirmClose:      &confirmClose,
			AllowNameConflict: &allowNameConflict,
		},
		Panel: config.PanelDefaults{Shell: m.shellPath},
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
