package tui

// Keybindings follow a tmux-style prefix model: press the prefix, then a verb.
//
//	prefix          ctrl-t      enter "waiting for a binding" mode
//	prefix + p      new panel   create a panel (default: shell)
//	prefix + d      dashboard   switch to the dashboard view
//	prefix + k      keys        toggle the in-view key map
//	prefix + q      detach      detach this client (the server keeps running)
const (
	keyPrefix    = "ctrl+t"
	keyNewPanel  = "p"
	keyDashboard = "d"
	keyShowMap   = "k"
	keyDetach    = "q"

	keyCtrlC = "ctrl+c" // bare emergency quit
)

// prefixLabel is how the prefix renders in hints and the key map.
const prefixLabel = "C-t"

// binding is one prefixed command, used for both the footer hint and the
// in-view key map so they never drift apart.
type binding struct {
	keys string
	desc string
}

// bindings lists the prefixed commands in display order.
var bindings = []binding{
	{prefixLabel + " p", "new shell panel"},
	{prefixLabel + " d", "dashboard"},
	{prefixLabel + " k", "toggle key map"},
	{prefixLabel + " q", "detach"},
}
