package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/limits"
)

// Keybindings are modal. On the dashboard an action fires on its bare key. In a
// zoom the keys reach the live program, so an action fires on the prefix then
// that same key. The escapes are bound to the prefix in every mode, including
// the dashboard.
//
//	dashboard:  p new · w close · g g mark · g c group · ? keys  (bare)
//	zoom:       C-t p new · C-t w close · C-t g c group          (prefix + the same)
//	any mode:   C-t d dashboard · C-t [ scroll                   (escapes)
//
// The group split is the exception: it answers a hand-written set of its own
// keys (see groupzoom.go) and does not consult the matcher, so the landings
// below do not reach it. Routing it through the matcher is the follow-up that
// would make "command-mode view" mean one thing everywhere.
//
// A key is a SEQUENCE (see keyseq.go). Four of them are LANDINGS — keys that do
// nothing on their own and open a family — which is what keeps the everyday
// verbs on one key while the long tail stays reachable and discoverable:
//
//	n  new     n c form · n . here · n C conductor · n h global shell
//	v  view    v u usage · v k keycast · v p preview · v l layout · v g lens
//	g  group   g g mark · g c create · g a add · g u ungroup
//	x  purge   x x — the second tap is the confirmation
//
// No landing is also a binding of its own, so a sequence fires on its last key
// and the timeout never delays a keystroke. keyseq_test.go asserts it.
const (
	keyPrefix      = "ctrl+t"
	keyNewPanel    = "p"
	keyNewForm     = "n c" // new panel, choosing the command
	keyNewHere     = "n ." // spawn a shell panel in the focused panel's current directory — "." reads as "here"
	keyNewAgent    = "A"   // spawn an agent panel (shift+a) — one of the two spawns that keep a bare key
	keyConductor   = "n C" // find-or-create the singleton conductor agent
	keyGlobalShell = "n h" // find-or-create the singleton global shell
	keyClose       = "w"
	keyRespawn     = "r"   // re-run the exited panel(s) under the focus — a lone dead slot, or every exited member of the focused group
	keyPurge       = "x x" // purge every exited panel — a double tap, because the second one is the confirmation
	keySignal      = "s"   // open the send-signal picker for the selection / panel / group
	keySearch      = "f"   // find: filter panels on the dashboard, search the scrollback in a zoom (C-t f)
	keyFleetSearch = "/"   // grep every panel's output for a term (dashboard; C-t / in a zoom)
	keyDiff        = "D"   // show the work-tree diff of the focused agent panel (shift+d; C-t D in a zoom)
	keyDispatch    = "T"   // dispatch a task to the focused agent panel (shift+t; C-t T in a zoom)
	keyEnqueue     = "t"   // enqueue a task for the scheduler to drain onto a free agent (bare t, the everyday sibling of T; C-t t in a zoom)
	keyQueue       = "Q"   // open the task-queue manager popup (shift+q; C-t Q in a zoom)
	keyUsage       = "v u" // cycle the account usage/cost footer segment
	keyKeycast     = "v k" // toggle the key-press readout in the footer
	keyPreview     = "v p" // toggle the dashboard's detail pane beside the tree
	keyLens        = "v g" // cycle the dashboard's group-by lens: work item, directory, profile, state — bare z is the split's resize and nothing else
	keyHelp        = "?"   // view the key list for the current view
	keyEditMap     = "k"   // edit the key map (prefix only: C-t k)
	keyPanelConfig = "P"   // shift+p
	keyScroll      = "["   // enter scroll mode (prefix only: C-t [), tmux-style
	keyRestart     = "S"   // C-t S only: bare S signals a whole group in the split, and this ends the fleet
	keyReload      = "R"   // shift+r — reload config (backend + cockpit), fleet kept
	keyDetach      = "q"
	keyBack        = "b" // back one level: zoom→group→dashboard (bare in command mode, C-t b in a zoom)

	keyMark      = "g g"   // mark / unmark the selected item — the cheapest key in its family, the same finger twice
	keyGroup     = "g c"   // create a work item from the marked panels
	keyAdd       = "g a"   // add the marked panels to the selected group
	keyUngroup   = "g u"   // dissolve the selected work item
	keyRename    = "e"     // edit the name of the selected panel or group
	keyFavourite = "*"     // favourite / unfavourite the selected panel or group (sorts it to the front)
	keyGrab      = "m"     // move: pick the selected row up, carry it through the tree, drop it
	keyExpand    = "space" // show or hide what is nested under the selected row

	// keyDashLayout is the dashboard's cards-or-tree switch. It sits under the v
	// landing beside the detail pane and the lens: all three answer "how is this
	// drawn", and none of them touches the fleet. (The group split's own L cycles
	// tile layouts and is a different view's key.)
	keyDashLayout = "v l"

	// Prefix-reached escapes, bound to the leader in every mode.
	keyDashboard = "d" // C-t d → the dashboard
	keyCommands  = "c" // C-t c → the plugin command picker
	keyScratch   = "~" // C-t ~ → toggle the floating scratch shell (any view)
	keyProcTree  = "o" // C-t o → the process-tree overlay (daemon → panels → OS descendants)
	keyInbox     = "a" // C-t a → the attention inbox (the queue of panels wanting a human)
	keyRemote    = "@" // C-t @ → the remote overlay: "user@host" is what its connection list is made of
	keyLogToggle = "l" // C-t l → start / stop writing the selected panel's output to a file
	keyLogView   = "L" // C-t L → open that log in a temporary panel, following as it grows

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
	case key == " ":
		return "space" // the key map and every legend name it, they do not print it
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
	actNewHere
	actNewForm
	actNewAgent
	actConductor
	actGlobalShell
	actClose
	actRespawn
	actPurge
	actSignal
	actSearch
	actFleetSearch
	actDiff
	actDispatch
	actEnqueue
	actQueue
	actHelp
	actUsageToggle
	actKeycastToggle
	actPreviewToggle
	actLens
	actPanelConfig
	actRestart
	actReload
	actDetach

	actMark
	actGroup
	actAdd
	actUngroup
	actRename
	actFavourite
	actGrab
	actExpand
	actDashLayout

	// Back pops one view level. It is a command (bare key in command mode, prefix
	// in a zoom), not an escape, so the prefix handler leaves it to lookupCmd.
	actBack

	// Escapes — bound to the prefix in every mode.
	actDashboard
	actEditMap
	actScroll
	actCommands
	actScratch
	actProcTree
	actInbox

	// Remote access. It is an escape — prefix-reached in every view — because the
	// key behind it opens the machine to another host, which is not a key to put
	// a fingertip from the arrow keys.
	//
	// It is bound to "@" rather than to a letter because every letter that reads
	// as "remote" is taken — `r` is respawn, and costing a zoom its respawn to
	// spell a mnemonic is a bad trade. "@" is bound to nothing, and it says what
	// the overlay is: a list of user@host.
	actRemote

	// The logging pair. Both are prefix-reached in every view, including the
	// dashboard, where a bare l already moves the cursor and a bare L cycles a
	// group's layout — and where a key that writes your terminal to disk is not
	// one to put a fingertip away from the arrow keys anyway.
	actLogToggle
	actLogView
)

// isEscape reports whether an action is reached after the prefix rather than on a
// bare key — lookupCmd skips these, lookupEscape resolves them. The dashboard jump
// and the key-map editor work after the prefix in every mode; panel config opens
// this way from command mode.
// actRestart is an escape rather than a command: bare S signals every member of
// a work item in the split, and a key that ends the whole fleet must not be one
// keystroke away from that in another view.
func isEscape(a action) bool {
	return a == actDashboard || a == actEditMap || a == actPanelConfig || a == actScroll || a == actCommands ||
		a == actScratch || a == actProcTree || a == actInbox || a == actRemote || a == actLogToggle ||
		a == actLogView || a == actRestart
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
	{"new-panel-here", keyNewHere, "spawn a shell panel in the focused panel's directory", actNewHere, "Panels"},
	{"new-panel-form", keyNewForm, "new panel (choose the command)", actNewForm, "Panels"},
	{"new-agent", keyNewAgent, "spawn an agent panel in a workdir", actNewAgent, "Panels"},
	{"conductor", keyConductor, "open the conductor — an agent that drives the fleet", actConductor, "Panels"},
	{"global-shell", keyGlobalShell, "open the global shell — a host shell always one key away", actGlobalShell, "Panels"},
	{"close", keyClose, "close the selected panel", actClose, "Panels"},
	{"respawn", keyRespawn, "re-run exited panel(s) in the selection", actRespawn, "Panels"},
	{"purge-exited", keyPurge, "purge all exited panels", actPurge, "Panels"},
	{"signal", keySignal, "send a signal to the panel(s)", actSignal, "Panels"},
	{"search", keySearch, "find panels · search the scrollback (zoom)", actSearch, "Panels"},
	{"fleet-search", keyFleetSearch, "search every panel's output for a term", actFleetSearch, "Panels"},
	{"diff", keyDiff, "show the work-tree diff (agent panel)", actDiff, "Panels"},
	{"dispatch", keyDispatch, "dispatch a task to the agent panel", actDispatch, "Panels"},
	{"enqueue", keyEnqueue, "enqueue a task for any free agent (a work item, if selected)", actEnqueue, "Panels"},
	{"queue", keyQueue, "manage the task queue (list · reorder · cancel · drain)", actQueue, "Panels"},
	{"log", keyLogToggle, "start / stop logging the panel's output to a file (prefix)", actLogToggle, "Panels"},
	{"log-view", keyLogView, "open that log in a temporary panel, following it (prefix)", actLogView, "Panels"},

	{"mark", keyMark, "mark a panel for grouping", actMark, "Work items"},
	{"group", keyGroup, "group the marked panels", actGroup, "Work items"},
	{"add", keyAdd, "add the marked panels to the selected group", actAdd, "Work items"},
	{"ungroup", keyUngroup, "ungroup the selected work item", actUngroup, "Work items"},
	{"rename", keyRename, "rename the panel or group", actRename, "Work items"},
	{"favourite", keyFavourite, "favourite the panel or group (sorts it to the front)", actFavourite, "Work items"},
	{"move", keyGrab, "pick a row up — arrows carry it, enter drops it", actGrab, "Work items"},
	{"expand", keyExpand, "show or hide what is nested under the row", actExpand, "Work items"},

	{"help", keyHelp, "view the keys for this view", actHelp, "View"},
	{"usage-footer", keyUsage, "cycle the usage footer: off, window, focused panel", actUsageToggle, "View"},
	{"keycast", keyKeycast, "toggle the key-press readout in the footer", actKeycastToggle, "View"},
	{"preview", keyPreview, "toggle the detail pane beside the dashboard tree", actPreviewToggle, "View"},
	{"layout", keyDashLayout, "the dashboard's cards or tree — the tree on a small fleet", actDashLayout, "View"},
	{"group-by", keyLens, "cycle the group-by lens: work item, directory, profile, state", actLens, "View"},
	{"key-map", keyEditMap, "edit the key map (prefix)", actEditMap, "View"},
	{"panel-config", keyPanelConfig, "configure panel defaults (prefix)", actPanelConfig, "View"},
	{"scroll", keyScroll, "scroll mode — line / page (prefix)", actScroll, "View"},
	{"dashboard", keyDashboard, "jump to the dashboard (prefix)", actDashboard, "View"},
	{"proc-tree", keyProcTree, "process tree — the daemon's OS processes (prefix)", actProcTree, "View"},
	{"inbox", keyInbox, "the attention inbox — clear what needs a human (prefix)", actInbox, "View"},
	{"remote", keyRemote, "remote access — the passkey and the live connections (prefix)", actRemote, "View"},
	{"back", keyBack, "back one level: zoom→group→dashboard (C-t b in a zoom)", actBack, "View"},
	{"commands", keyCommands, "open the plugin command picker (prefix)", actCommands, "View"},
	{"scratch", keyScratch, "toggle a floating scratch shell (prefix)", actScratch, "View"},

	{"restart", keyRestart, "force-restart the server (prefix)", actRestart, "Session"},
	{"reload", keyReload, "reload config (backend + cockpit)", actReload, "Session"},
	{"detach", keyDetach, "detach (server keeps running)", actDetach, "Session"},
}

// bindDesc is a binding's description in the active language. The lookup is
// keyed by the binding's stable name, not by its key or its English text, so a
// rebind — or a reworded English line — never orphans a translation.
func (m model) bindDesc(b binding) string {
	return m.tr("bind."+b.name, b.desc)
}

// prefs is the cockpit state persisted to $HOME/.baton/config.
type prefs struct {
	prefix            string
	binds             []binding
	confirmClose      bool
	allowNameConflict bool
	bellEnabled       bool
	mouseEnabled      bool      // mouse reporting (wheel scroll + selection); default off
	usageMode         usageMode // which account usage/cost view the footer shows; default the window view
	keycast           bool      // show the key-press readout in the footer; default off
	preview           bool      // show the detail pane beside the dashboard tree; default off
	shellPath         string
	workdir           string                         // default working directory for new panels ("" = home)
	defaultAgent      string                         // agent profile the new-agent action spawns
	agents            map[string]config.AgentProfile // user-configured agent profiles
	replayKB          int                            // per-panel replay buffer in KiB (0 = server default)
	limits            limits.Limits                  // fleet-wide resource caps for new panels
	diffCommand       string                         // explicit diff command for the agent diff pop-up ("" = git diff.tool then a built-in diff)
	tui               config.TUIConfig               // cockpit appearance: colour theme and group-split layouts
	lang              i18n.Lang                      // resolved message language for the cockpit's help surfaces
	foldQuiet         int                            // settings.fold-quiet: how many quiet dashboard cards before they fold into one row; 0 = never
	foldSimilar       bool                           // group summary tile folds the lookalikes, not the latecomers; default on
	inboxDone         bool                           // a finished agent joins the attention inbox as a "review me" row; default on
	inboxSnooze       time.Duration                  // how long the inbox's `-` defers a row; default defaultInboxSnooze
	keyTimeout        time.Duration                  // settings.key-timeout: how long a landing key waits for the key after it; 0 = never
	notify            bool                           // send OSC 9 desktop notifications when panels need a human; default OFF
	notifyCoalesce    time.Duration                  // how long edges are gathered into one notification; default defaultNotifyCoalesce
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
	p := prefs{prefix: keyPrefix, binds: append([]binding(nil), bindings...), confirmClose: true, bellEnabled: true, usageMode: usageWindow,
		foldQuiet: defaultFoldQuiet, foldSimilar: true, inboxDone: true, inboxSnooze: defaultInboxSnooze,
		keyTimeout: parseKeyTimeout(cfg.Settings.KeyTimeout)}

	if cfg.Prefix != "" {
		p.prefix = cfg.Prefix
	}
	for i := range p.binds {
		if k := normSeq(cfg.Keys[p.binds[i].name]); k != "" {
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
	// usage-footer is the older on/off form of the same setting: honour it when it
	// says "off", so an existing config that hid the segment keeps hiding it, and
	// let the richer usage-mode win whenever it is set.
	if cfg.Settings.UsageFooter != nil && !*cfg.Settings.UsageFooter {
		p.usageMode = usageOff
	}
	if cfg.Settings.UsageMode != "" {
		p.usageMode = parseUsageMode(cfg.Settings.UsageMode)
	}
	if cfg.Settings.Keycast != nil {
		p.keycast = *cfg.Settings.Keycast
	}
	if cfg.Settings.DashboardPreview != nil {
		p.preview = *cfg.Settings.DashboardPreview
	}
	p.shellPath = cfg.Panel.Shell
	p.workdir = cfg.Panel.Workdir
	p.defaultAgent = cfg.Panel.DefaultAgent
	p.agents = cfg.Panel.Agents
	p.replayKB = cfg.Panel.ReplayKB
	p.limits = cfg.Panel.Limits
	p.diffCommand = cfg.Panel.DiffCommand
	p.tui = cfg.TUI
	p.lang = i18n.Detect(cfg.Settings.Language)
	if cfg.Settings.FoldQuiet != nil {
		p.foldQuiet = *cfg.Settings.FoldQuiet // 0 (or anything below it) reads as "never fold"
	}
	if cfg.Settings.FoldSimilar != nil {
		p.foldSimilar = *cfg.Settings.FoldSimilar
	}
	if cfg.Settings.InboxDone != nil {
		p.inboxDone = *cfg.Settings.InboxDone
	}
	p.inboxSnooze = parseSnooze(cfg.Settings.InboxSnooze)
	// No default to layer here: settings.notify is off until it is asked for, the
	// way mouse, keycast and remote are — a desktop toast goes somewhere baton was
	// not invited, so it waits to be invited.
	if cfg.Settings.Notify != nil {
		p.notify = *cfg.Settings.Notify
	}
	p.notifyCoalesce = parseCoalesce(cfg.Settings.NotifyCoalesce)
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
	usageFooter := m.usageMode != usageOff
	keycast := m.keycast
	preview := m.preview

	// Start from the current on-disk config so sections the cockpit does not own —
	// the queue caps and the usage source/interval, both hand-edited in this same
	// file — survive a settings toggle instead of being dropped on the rewrite. A
	// load error is surfaced rather than swallowed: overwriting a config we could
	// not parse would clobber those unowned sections from a near-empty base.
	out, err := config.Load()
	if err != nil {
		return fmt.Errorf("reload config before save: %w", err)
	}
	out.Prefix = prefix
	out.Keys = keys
	out.Settings.ConfirmClose = &confirmClose
	out.Settings.AllowNameConflict = &allowNameConflict
	out.Settings.Bell = &bellEnabled
	out.Settings.Mouse = &mouseEnabled
	out.Settings.UsageFooter = &usageFooter // kept in step so an older cockpit reading this file still hides an off segment
	out.Settings.UsageMode = m.usageMode.String()
	out.Settings.Keycast = &keycast
	out.Settings.DashboardPreview = &preview
	// The language is written ONLY when the user picked one, never as a side
	// effect of an unrelated save.
	//
	// It used to be written on every save, which quietly ended environment
	// detection for good: the first time anyone toggled the bell or rebound a
	// key, whatever the language had resolved to at that instant was stamped
	// into the file as an explicit setting, and an explicit setting beats
	// $LANG. A cockpit that came up before its config arrived resolved to
	// English, so the common outcome was a config that said `language: en` on a
	// zh_TW.UTF-8 machine, and a `?` screen that never spoke Chinese again.
	//
	// Unset, `out` keeps whatever was already on disk, so a user who did choose
	// a language keeps it and everyone else keeps their environment.
	if m.langChosen {
		out.Settings.Language = string(m.effLang())
	}
	out.Panel.Shell = m.shellPath
	out.Panel.Workdir = m.workdir
	out.Panel.DefaultAgent = m.defaultAgent
	out.Panel.Agents = m.agents // round-trip the user's profiles so a save never drops them
	out.Panel.ReplayKB = m.replayKB
	out.Panel.Limits = m.limits
	out.Panel.DiffCommand = m.diffCommand
	out.TUI = config.TUIConfig{} // the cockpit appearance lives in TUI.yaml, never the main config
	return out.Save()
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

// keycaps renders a binding as ONE KEYCAP PER KEY — [g][c], or [C-t][d] when a
// leader is named — because a sequence set in a single cap reads as one
// impossible key rather than as the run you actually press. When hot the caps
// glow in the brand colour (used for the selected key-map row).
func keycaps(prefix, key string, hot bool) string {
	cap := keycapStyle
	if hot {
		cap = keycapHotStyle
	}
	var parts []string
	if prefix != "" {
		parts = append(parts, cap.Render(prefix))
	}
	for _, t := range strings.Fields(normSeq(key)) {
		parts = append(parts, cap.Render(keyLabel(t)))
	}
	return strings.Join(parts, " ")
}
