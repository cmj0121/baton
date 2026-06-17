package tui

import "github.com/charmbracelet/lipgloss"

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
	keyPrefix    = "ctrl+t"
	keyNewPanel  = "p"
	keyClose     = "w"
	keyDashboard = "d"
	keyShowMap   = "k"
	keyRestart   = "S" // shift+s
	keyDetach    = "q"

	keyCtrlC = "ctrl+c" // bare emergency quit
)

// prefixLabel is how the prefix renders in hints and the key map.
const prefixLabel = "C-t"

// action is the verb a binding performs; the prefix handler and the navigable
// key map both resolve to one of these, so they can never drift apart.
type action int

const (
	actNewPanel action = iota
	actClose
	actDashboard
	actToggleMap
	actRestart
	actDetach
)

// binding is one prefixed command: the bare key pressed after the prefix, the
// human description, and the action it triggers.
type binding struct {
	key  string // bare key after the prefix, e.g. "p"
	desc string
	act  action
}

// bindings lists the prefixed commands in display order. This is the single
// source of truth for both the footer hint and the in-view key map.
var bindings = []binding{
	{keyNewPanel, "spawn a new shell panel", actNewPanel},
	{keyClose, "close the selected panel", actClose},
	{keyDashboard, "jump back to the dashboard", actDashboard},
	{keyShowMap, "toggle this key map", actToggleMap},
	{keyRestart, "force-restart the server", actRestart},
	{keyDetach, "detach (server keeps running)", actDetach},
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

// chord renders a prefixed binding as two keycaps, e.g. [C-t][p]. When hot the
// caps glow in the brand colour (used for the selected key-map row).
func chord(key string, hot bool) string {
	cap := keycapStyle
	if hot {
		cap = keycapHotStyle
	}
	return cap.Render(prefixLabel) + " " + cap.Render(key)
}
