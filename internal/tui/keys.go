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
//	any mode:   C-t d dashboard · C-t [ scroll                      (escapes)
const (
	keyPrefix      = "ctrl+t"
	keyNewPanel    = "p"
	keyNewForm     = "c" // "choose the command" (n is rename)
	keyNewAgent    = "A" // spawn an agent panel (shift+a)
	keyConductor   = "C" // find-or-create the singleton conductor agent (shift+c)
	keyClose       = "w"
	keyRespawn     = "r" // re-run the exited panel(s) under the focus — a lone dead slot, or every exited member of the focused group
	keyPurge       = "x"
	keySignal      = "s" // open the send-signal picker for the selection / panel / group
	keySearch      = "f" // find: filter panels on the dashboard, search the scrollback in a zoom (C-t f)
	keyDiff        = "D" // show the work-tree diff of the focused agent panel (shift+d; C-t D in a zoom)
	keyDispatch    = "T" // dispatch a task to the focused agent panel (shift+t; C-t T in a zoom)
	keyQueue       = "Q" // open the task-queue manager popup (shift+q; C-t Q in a zoom)
	keyHelp        = "?" // view the key list for the current view
	keyEditMap     = "k" // edit the key map (prefix only: C-t k)
	keyPanelConfig = "P" // shift+p
	keyScroll      = "[" // enter scroll mode (prefix only: C-t [), tmux-style
	keyRestart     = "S" // shift+s
	keyReload      = "R" // shift+r — reload config (backend + cockpit), fleet kept
	keyDetach      = "q"
	keyBack        = "b" // back one level: zoom→group→dashboard (bare in command mode, C-t b in a zoom)

	keyMark    = "g" // mark / unmark the selected item
	keyGroup   = "G" // group the marked panels (shift+g)
	keyAdd     = "a" // add the marked panels to the selected group
	keyUngroup = "u" // dissolve the selected work item
	keyRename  = "e" // edit the name of the selected panel or group

	// Prefix-reached escapes, bound to the leader in every mode.
	keyDashboard = "d" // C-t d → the dashboard
	keyCommands  = "c" // C-t c → the plugin command picker
	keyScratch   = "~" // C-t ~ → toggle the floating scratch shell (any view)

	keyRemove    = "x" // in the group split: remove the focused member from the group
	keyInteract  = "i" // in the group split: drive the focused tile in place, no zoom
	keyPin       = "p" // in the group split: pin/unpin the focused member to a live tile
	keySignalAll = "S" // in the group split: signal every member (bare s signals the focused one)
	keyLayout    = "L" // in the group split: cycle the tile layout (shift+l; bare l moves focus)
	keyResize    = "z" // in the group split: enter resize mode — arrows grow/shrink the focused tile

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
	actNewAgent
	actConductor
	actClose
	actRespawn
	actPurge
	actSignal
	actSearch
	actDiff
	actDispatch
	actQueue
	actHelp
	actPanelConfig
	actRestart
	actReload
	actDetach

	actMark
	actGroup
	actAdd
	actUngroup
	actRename

	// Back pops one view level. It is a command (bare key in command mode, prefix
	// in a zoom), not an escape, so the prefix handler leaves it to lookupCmd.
	actBack

	// Escapes — bound to the prefix in every mode.
	actDashboard
	actEditMap
	actScroll
	actCommands
	actScratch
)

// isEscape reports whether an action is reached after the prefix rather than on a
// bare key — lookupCmd skips these, lookupEscape resolves them. The dashboard jump
// and the key-map editor work after the prefix in every mode; panel config opens
// this way from command mode.
func isEscape(a action) bool {
	return a == actDashboard || a == actEditMap || a == actPanelConfig || a == actScroll || a == actCommands || a == actScratch
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
	{"new-agent", keyNewAgent, "spawn an agent panel in a workdir", actNewAgent, "Panels"},
	{"conductor", keyConductor, "open the conductor — an agent that drives the fleet", actConductor, "Panels"},
	{"close", keyClose, "close the selected panel", actClose, "Panels"},
	{"respawn", keyRespawn, "re-run exited panel(s) in the selection", actRespawn, "Panels"},
	{"purge-exited", keyPurge, "purge all exited panels", actPurge, "Panels"},
	{"signal", keySignal, "send a signal to the panel(s)", actSignal, "Panels"},
	{"search", keySearch, "find panels · search the scrollback (zoom)", actSearch, "Panels"},
	{"diff", keyDiff, "show the work-tree diff (agent panel)", actDiff, "Panels"},
	{"dispatch", keyDispatch, "dispatch a task to the agent panel", actDispatch, "Panels"},
	{"queue", keyQueue, "manage the task queue (list · cancel · drain)", actQueue, "Panels"},

	{"mark", keyMark, "mark a panel for grouping", actMark, "Work items"},
	{"group", keyGroup, "group the marked panels", actGroup, "Work items"},
	{"add", keyAdd, "add the marked panels to the selected group", actAdd, "Work items"},
	{"ungroup", keyUngroup, "ungroup the selected work item", actUngroup, "Work items"},
	{"rename", keyRename, "rename the panel or group", actRename, "Work items"},

	{"help", keyHelp, "view the keys for this view", actHelp, "View"},
	{"key-map", keyEditMap, "edit the key map (prefix)", actEditMap, "View"},
	{"panel-config", keyPanelConfig, "configure panel defaults (prefix)", actPanelConfig, "View"},
	{"scroll", keyScroll, "scroll mode — line / page (prefix)", actScroll, "View"},
	{"dashboard", keyDashboard, "jump to the dashboard (prefix)", actDashboard, "View"},
	{"back", keyBack, "back one level: zoom→group→dashboard (C-t b in a zoom)", actBack, "View"},
	{"commands", keyCommands, "open the plugin command picker (prefix)", actCommands, "View"},
	{"scratch", keyScratch, "toggle a floating scratch shell (prefix)", actScratch, "View"},

	{"restart", keyRestart, "force-restart the server", actRestart, "Session"},
	{"reload", keyReload, "reload config (backend + cockpit)", actReload, "Session"},
	{"detach", keyDetach, "detach (server keeps running)", actDetach, "Session"},
}

// prefs is the cockpit state persisted to $HOME/.baton/config.
type prefs struct {
	prefix            string
	binds             []binding
	confirmClose      bool
	allowNameConflict bool
	bellEnabled       bool
	mouseEnabled      bool // mouse reporting (wheel scroll + selection); default off
	shellPath         string
	workdir           string                         // default working directory for new panels ("" = home)
	defaultAgent      string                         // agent profile the new-agent action spawns
	agents            map[string]config.AgentProfile // user-configured agent profiles
	replayKB          int                            // per-panel replay buffer in KiB (0 = server default)
	diffCommand       string                         // explicit diff command for the agent diff pop-up ("" = git diff.tool then a built-in diff)
	tui               config.TUIConfig               // cockpit appearance: colour theme and group-split layouts
}

// defaultAgentName is the built-in agent profile, used when none is configured —
// Claude is the first agent Baton ships with.
const defaultAgentName = "claude"

// builtinAgent returns the profile for a name the user has not configured. Only
// the built-in "claude" exists for now.
func builtinAgent(name string) (config.AgentProfile, bool) {
	if name == "" || name == defaultAgentName {
		return config.AgentProfile{Command: "claude"}, true
	}
	return config.AgentProfile{}, false
}

// loadPrefs reads the config file, returning defaults for anything missing or on
// any read error (so the cockpit always comes up). Defaults: prefix "ctrl+t",
// confirm-on-close on, system shell. It is the cockpit's bootstrap; the daemon then
// pushes its merged effective config (config.get → prefsFromConfig), which wins.
func loadPrefs() prefs {
	cfg, _ := config.Load() // a read error yields a zero cfg → all defaults below
	return prefsFromConfig(cfg)
}

// prefsFromConfig projects a config onto the cockpit prefs, layering the file's
// values over the built-in defaults. Shared by the local bootstrap (loadPrefs) and
// the daemon-pushed config (the "config" event), so the two can never map a field
// differently.
func prefsFromConfig(cfg config.Config) prefs {
	p := prefs{prefix: keyPrefix, binds: append([]binding(nil), bindings...), confirmClose: true, bellEnabled: true}

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
	if cfg.Settings.Bell != nil {
		p.bellEnabled = *cfg.Settings.Bell
	}
	if cfg.Settings.Mouse != nil {
		p.mouseEnabled = *cfg.Settings.Mouse
	}
	p.shellPath = cfg.Panel.Shell
	p.workdir = cfg.Panel.Workdir
	p.defaultAgent = cfg.Panel.DefaultAgent
	p.agents = cfg.Panel.Agents
	p.replayKB = cfg.Panel.ReplayKB
	p.diffCommand = cfg.Panel.DiffCommand
	p.tui = cfg.TUI
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
	bellEnabled := m.bellEnabled
	mouseEnabled := m.mouseEnabled
	return config.Config{
		Prefix: prefix,
		Keys:   keys,
		Settings: config.Settings{
			ConfirmClose:      &confirmClose,
			AllowNameConflict: &allowNameConflict,
			Bell:              &bellEnabled,
			Mouse:             &mouseEnabled,
		},
		Panel: config.PanelDefaults{
			Shell:        m.shellPath,
			Workdir:      m.workdir,
			DefaultAgent: m.defaultAgent,
			Agents:       m.agents, // round-trip the user's profiles so a save never drops them
			ReplayKB:     m.replayKB,
			DiffCommand:  m.diffCommand,
		},
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
