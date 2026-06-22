// Package tui is the reference frontend: a keyboard-driven cockpit that attaches
// to the server and renders a mission-control dashboard of every live panel.
package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	vt "github.com/charmbracelet/x/vt"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

const banner = `██████╗  █████╗ ████████╗ ██████╗ ███╗   ██╗
██╔══██╗██╔══██╗╚══██╔══╝██╔═══██╗████╗  ██║
██████╔╝███████║   ██║   ██║   ██║██╔██╗ ██║
██╔══██╗██╔══██║   ██║   ██║   ██║██║╚██╗██║
██████╔╝██║  ██║   ██║   ╚██████╔╝██║ ╚████║
╚═════╝ ╚═╝  ╚═╝   ╚═╝    ╚═════╝ ╚═╝  ╚═══╝`

// Palette. A dark theme keyed on a single primary blue: azure (39) carries the
// brand and every selection, deep blues fill the chrome, and the rest stay
// semantic so panel state still reads at a glance.
const (
	colBrand   = lipgloss.Color("39")  // primary blue — banner, borders, selection
	colBrandHi = lipgloss.Color("117") // lighter blue for highlighted text
	colInk     = lipgloss.Color("253") // near-white text
	colMuted   = lipgloss.Color("245") // dim text
	colFaint   = lipgloss.Color("239") // hairlines / inactive borders
	colSurface = lipgloss.Color("236") // modal / overlay surface (key map, input)

	// Accents and semantics.
	colBlue  = lipgloss.Color("25")  // deep-blue mode-segment fill
	colGreen = lipgloss.Color("36")  // healthy connection
	colCyan  = lipgloss.Color("80")  // keycaps, clock, accents
	colRed   = lipgloss.Color("167") // error connection
	colDark  = lipgloss.Color("16")  // text on bright segments

	colAgent = lipgloss.Color("75") // agent-panel count (blue)
	colShell = lipgloss.Color("73") // shell-panel count (teal)

	colBar = lipgloss.Color("111") // light-blue status-bar fill (the footer)
)

var (
	bannerStyle = lipgloss.NewStyle().Bold(true).Foreground(colBrand)
	subStyle    = lipgloss.NewStyle().Foreground(colMuted)
	mutedStyle  = lipgloss.NewStyle().Foreground(colMuted)
	inkStyle    = lipgloss.NewStyle().Foreground(colInk)

	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(colBrandHi)

	barStyle = lipgloss.NewStyle().Background(colBar).Foreground(colDark)
	barBold  = barStyle.Bold(true)
)

type mode int

const (
	modeDashboard mode = iota
	modeKeyMap         // the editable key map (C-t k)
	modeHelp           // the read-only key list for a view (?)
	modePanelConfig
	modeSignal  // the send-signal picker (s / C-t s)
	modeCommand // the plugin command picker (C-t c)
	modeZoom
	modeGroupZoom
)

type model struct {
	client *client.Client
	fleet  []panel.Panel // dummy + live panels shown on the dashboard

	mode   mode
	prefix bool            // armed by the prefix key, consumes the next key as a binding
	cursor int             // selection index (dashboard item, or key-map row)
	marked map[string]bool // panel ids tagged for the next group (multi-select)
	status string

	lastStatus  string // status seen on the previous tick, to detect when it settles
	statusAge   int    // ticks the status has gone unchanged, for the transient fade
	endpoint    string // where we are attached: "local", or a host/IP for remote
	version     string // negotiated protocol version, surfaced in the key map
	appVersion  string // this frontend's build version
	serverVer   string // the backend's build version, learned from the welcome
	backendDown bool   // the server connection dropped — the footer shows a red alert

	attnSeen    map[string]bool // panel ids currently flagged for attention, to fire the notification only on the rising edge
	bellPending bool            // a panel just entered attention — ring the terminal bell on the next render

	confirmClose      bool      // ask y/n before closing a panel (toggled in the key map)
	allowNameConflict bool      // let two work items share a name (server enforces; kept to round-trip config)
	bellEnabled       bool      // ring the terminal bell when a panel needs you (toggled in the key map)
	mouseEnabled      bool      // mouse reporting on — the wheel scrolls and moves the selection (toggled in the key map)
	pendingClose      bool      // a close is awaiting y/n confirmation
	pendingRestart    bool      // a force-restart is awaiting y/n confirmation
	now               time.Time // wall clock shown in the footer, ticked every second

	cpuPct   float64 // system-wide CPU load %, sampled each tick for the footer
	memUsed  uint64  // system memory in use, bytes
	memTotal uint64  // total system memory, bytes

	prefixKey string    // leader key armed before a binding (default ctrl+t)
	binds     []binding // editable copy of the bindings (nil ⇒ the defaults)
	editing   bool      // capturing the next key press as a rebind
	editIdx   int       // binding being rebound; editPrefix means the leader key

	shellPath    string                         // configured default shell binary path ("" = system shell)
	workdir      string                         // configured default working directory for new panels ("" = home)
	defaultAgent string                         // agent profile the new-agent action spawns ("" = claude)
	agents       map[string]config.AgentProfile // user-configured agent profiles
	replayKB     int                            // per-panel replay buffer in KiB, round-tripped so a save never drops it
	diffCommand  string                         // configured diff command for the agent diff pop-up, round-tripped so a save never drops it
	input        inputPurpose                   // active text-input overlay, or inputNone
	inputBuf     string                         // text typed into the overlay
	inputHint    string                         // path-completion hint shown under the field (tab), cleared on edit
	helpFrom     mode                           // the view the key map (?) was opened from, to restore on esc
	helpScroll   int                            // scroll offset for the read-only help list (it has no cursor)

	renameID    string // panel id being renamed via inputRename ("" if a group)
	renameGroup string // group being renamed via inputRename ("" if a panel)

	filter string // dashboard panel filter (substring on titles / group names); "" shows the whole fleet

	searchQuery string         // active scrollback search term ("" = no search)
	searchRe    *regexp.Regexp // compiled, case-insensitive matcher for searchQuery (nil = no search)
	searchHits  []int          // combined scrollback+screen line indices matching the term, ascending
	searchAt    int            // index into searchHits of the current match

	copySelecting bool // a copy selection is being made in scroll mode (v marks the anchor)
	copyAnchor    int  // combined line index the selection is anchored at; the span runs to the current top

	signalFrom    mode     // the view the signal picker was opened from, restored on esc / after sending
	signalTargets []string // panel ids the chosen signal is delivered to
	signalScope   string   // human label of the target(s), shown in the picker
	signalCursor  int      // highlighted row in the signal picker (last row is "other…")

	pluginCommands []proto.PluginCommand // commands a Lua plugin registered, pushed by the daemon (config.get); shown in the command picker
	commandFrom    mode                  // the view the command picker was opened from, restored on esc
	commandCursor  int                   // highlighted row in the command picker

	zoomID           string           // panel being zoomed (modeZoom)
	zoomTitle        string           // its title, for the zoom footer
	zoomEphemeral    bool             // the current zoom is a transient diff panel — dismissing it closes the panel server-side
	pendingDiffTitle string           // title to give the diff zoom, stashed when panel.diff is sent, read on the "diff" reply
	zoomArmed        bool             // prefix pressed inside a zoom, awaiting the verb
	zoomExited       bool             // the zoomed panel has exited — a read-only result view
	emu              *vt.SafeEmulator // terminal emulator rendering the zoomed panel
	scrollOff        int              // scrollback offset (lines above the live bottom) for the zoom / focused tile
	scrolling        bool             // scroll mode (C-t [): arrows / page keys navigate history, keys are not sent to the program
	cursorHidden     *bool            // tracks the zoomed program's cursor visibility (DECTCEM); nil when not zooming

	groupName       string                      // work item being split-viewed (modeGroupZoom)
	groupFocus      int                         // focused member, indexing tiles then the summary slot
	groupArmed      bool                        // prefix pressed in the split, awaiting an escape
	groupInteract   bool                        // keys drive the focused tile in place (i), no zoom
	groupShown      map[string]int              // per-group visible-tile count N, server-owned via the snapshot's Groups
	summaryScope    bool                        // the split is scoped to a group's collapsed (summarised) members
	groupPinned     map[string]bool             // member ids pinned to a live tile, derived from the fleet's server-owned Pinned flags
	groupEmus       map[string]*vt.SafeEmulator // live emulator per member tile
	zoomGroupOrigin string                      // group to return to from a single zoom, "" if none

	width, height int
	quitting      bool
	restart       bool // user asked to force-restart the daemon on exit
}

// inputPurpose is what an active text-input overlay feeds on submit.
type inputPurpose int

const (
	inputNone        inputPurpose = iota
	inputShellPath                // editing the default shell in panel config
	inputReplayKB                 // editing the per-panel replay buffer (KiB) in panel config
	inputNewPanelCmd              // the prefix+n new-panel command popup
	inputAgentDir                 // the workdir for a new agent panel
	inputGroupName                // naming a new group from the marked panels
	inputRename                   // renaming the selected panel or group
	inputSignalName               // free-form signal name/number for the picker's "other…"
	inputFilter                   // live dashboard panel filter (f)
	inputSearch                   // scrollback search term in a zoom / group tile (C-t f)
)

// RestartRequested reports whether the cockpit exited because the user asked to
// force-restart the server. The client runner relaunches the daemon and
// re-attaches when this is set.
func (m model) RestartRequested() bool { return m.restart }

// New builds the TUI model around an attached client. The fleet starts empty and
// is filled by the server's first snapshot, which arrives right after the hello
// handshake — the server owns the panels now.
func New(c *client.Client, appVersion string) tea.Model {
	m := model{
		client:     c,
		appVersion: appVersion,
		mode:       modeDashboard,
		status:     "attaching…",
		endpoint:   c.Endpoint(),
		now:        time.Now(),
	}
	return m.applyPrefs(loadPrefs())
}

// applyPrefs overlays a freshly loaded prefs onto the model — the in-place client
// reload. It refreshes only the settings the cockpit owns (the leader key, the
// key map, the toggles, and the panel defaults); live view state — the mode, the
// fleet, a zoom or split and its emulators — carries on untouched, so reloading
// never disturbs what you are watching.
func (m model) applyPrefs(p prefs) model {
	m.prefixKey = p.prefix
	m.binds = p.binds
	m.confirmClose = p.confirmClose
	m.allowNameConflict = p.allowNameConflict
	m.bellEnabled = p.bellEnabled
	m.mouseEnabled = p.mouseEnabled
	m.shellPath = p.shellPath
	m.workdir = p.workdir
	m.defaultAgent = p.defaultAgent
	m.agents = p.agents
	m.replayKB = p.replayKB
	m.diffCommand = p.diffCommand
	return m
}

// editPrefix is the editIdx sentinel meaning the leader key is being rebound.
const editPrefix = -1

// rowKind classifies a row of the key-map panel.
type rowKind int

const (
	rowPrefix  rowKind = iota // the leader key (row 0)
	rowBinding                // a binding (rows 1..N)
	rowSetting                // a toggle in the settings block (the last rows)
)

// The settings block of the key map, one toggle per row in display order. idx
// values are stable so keyMapRow, activate, and the renderer agree on them.
const (
	settingConfirmClose = iota // ask y/n before closing a panel
	settingBell                // ring the bell when a panel needs you
	settingMouse               // enable mouse reporting (wheel scroll + selection)
	numSettings
)

// settingLabel is the human label for a settings-block row.
func settingLabel(idx int) string {
	switch idx {
	case settingBell:
		return "ring the bell when a panel needs you"
	case settingMouse:
		return "enable the mouse (wheel scroll + selection)"
	default:
		return "confirm before closing a panel"
	}
}

// settingValue reports whether a settings-block toggle is currently on.
func (m model) settingValue(idx int) bool {
	switch idx {
	case settingBell:
		return m.bellEnabled
	case settingMouse:
		return m.mouseEnabled
	default:
		return m.confirmClose
	}
}

// keyMapRow resolves the current cursor to a key-map row: its kind and, for a
// binding row, the binding's index. This is the single source of truth for the
// panel's row layout (prefix, then bindings, then settings).
func (m model) keyMapRow() (rowKind, int) {
	switch {
	case m.cursor <= 0:
		return rowPrefix, 0
	case m.cursor <= len(m.keymap()):
		return rowBinding, m.cursor - 1
	default:
		return rowSetting, m.cursor - len(m.keymap()) - 1
	}
}

// keyMapAnchors are the cursor rows that start each section of the key map: the
// prefix row, the first binding of every purpose group, and the settings row.
// tab/shift+tab hop between these.
func (m model) keyMapAnchors() []int {
	binds := m.keymap()
	anchors := []int{0} // the prefix row
	prev := ""
	for i, b := range binds {
		if b.cat != prev {
			anchors = append(anchors, i+1) // binding rows follow the prefix at row 1
			prev = b.cat
		}
	}
	return append(anchors, len(binds)+1) // the settings row
}

// jumpSection moves the key-map cursor to the next (dir +1) or previous (dir -1)
// section anchor, wrapping at the ends.
func (m *model) jumpSection(dir int) {
	anchors := m.keyMapAnchors()
	at := 0
	for i, a := range anchors {
		if a <= m.cursor {
			at = i
		}
	}
	m.cursor = anchors[(at+dir+len(anchors))%len(anchors)]
}

// effPrefix is the active leader key, defaulting to keyPrefix for a zero-value
// model (so tests and the first frame still arm on ctrl+t).
func (m model) effPrefix() string {
	if m.prefixKey != "" {
		return m.prefixKey
	}
	return keyPrefix
}

// keymap is the active binding set: the model's editable copy, or the package
// defaults for a zero-value model.
func (m model) keymap() []binding {
	if m.binds != nil {
		return m.binds
	}
	return bindings
}

// ensureBinds makes the binding set mutable (copy-on-write from the defaults).
func (m *model) ensureBinds() {
	if m.binds == nil {
		m.binds = append([]binding(nil), bindings...)
	}
}

// lookupCmd resolves a command key (a single keystroke in command mode, or the
// key after the prefix in a zoom) to its binding. Escapes are excluded.
func (m model) lookupCmd(key string) (binding, bool) {
	for _, b := range m.keymap() {
		if !isEscape(b.act) && b.key == key {
			return b, true
		}
	}
	return binding{}, false
}

// lookupEscape resolves a key pressed after the prefix to a prefix-accessed
// action (the dashboard/group jumps, the key-map editor, panel config).
func (m model) lookupEscape(key string) (binding, bool) {
	for _, b := range m.keymap() {
		if isEscape(b.act) && b.key == key {
			return b, true
		}
	}
	return binding{}, false
}

// --- bubbletea event plumbing ---

type eventMsg proto.ServerMsg
type panelOutputMsg proto.ServerMsg
type statsEventMsg proto.ServerMsg
type telemetryEventMsg proto.ServerMsg
type configEventMsg proto.ServerMsg
type connClosedMsg struct{}
type tickMsg time.Time

// waitMsg returns a command that blocks for the next message on ch and wraps it
// with wrap — a per-channel type so Update can re-arm the channel that fired. A
// closed channel means the server hung up. The cockpit reads control, panel
// output, and host telemetry on separate channels so a burst on one never delays
// another; each gets its own wait command below.
func waitMsg(ch chan proto.ServerMsg, wrap func(proto.ServerMsg) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return connClosedMsg{}
		}
		return wrap(msg)
	}
}

func waitEvent(ch chan proto.ServerMsg) tea.Cmd {
	return waitMsg(ch, func(m proto.ServerMsg) tea.Msg { return eventMsg(m) })
}

func waitOutput(ch chan proto.ServerMsg) tea.Cmd {
	return waitMsg(ch, func(m proto.ServerMsg) tea.Msg { return panelOutputMsg(m) })
}

func waitStats(ch chan proto.ServerMsg) tea.Cmd {
	return waitMsg(ch, func(m proto.ServerMsg) tea.Msg { return statsEventMsg(m) })
}

func waitTelemetry(ch chan proto.ServerMsg) tea.Cmd {
	return waitMsg(ch, func(m proto.ServerMsg) tea.Msg { return telemetryEventMsg(m) })
}

func waitConfig(ch chan proto.ServerMsg) tea.Cmd {
	return waitMsg(ch, func(m proto.ServerMsg) tea.Msg { return configEventMsg(m) })
}

// tick drives the footer clock, firing once a second.
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{waitEvent(m.client.Events), waitOutput(m.client.Output), waitStats(m.client.Stats), waitTelemetry(m.client.Telemetry), waitConfig(m.client.Config), tick()}
	if m.mouseEnabled {
		cmds = append(cmds, tea.EnableMouseCellMotion) // honour the persisted mouse toggle on attach
	}
	return tea.Batch(cmds...)
}

// mouseCmd turns the terminal's mouse reporting on or off, matching the toggle.
// Cell-motion mode reports clicks and the wheel without the noise of every
// pointer move, which is all the cockpit's wheel handling needs.
func mouseCmd(on bool) tea.Cmd {
	if on {
		return tea.EnableMouseCellMotion
	}
	return tea.DisableMouse
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.mode == modeZoom && m.emu != nil {
			m.emu.Resize(m.width, m.zoomRows())
			m.sendf(proto.Command{Action: "panel.resize", ID: m.zoomID, Rows: m.zoomRows(), Cols: m.width})
		}
		if m.mode == modeGroupZoom {
			m.resizeGroupTiles() // reflow the tiles to the new screen, net of the bar
		}
		return m, nil

	case eventMsg:
		m.applyEvent(proto.ServerMsg(msg))
		return m, tea.Batch(m.takeBell(), waitEvent(m.client.Events))

	case panelOutputMsg:
		sm := proto.ServerMsg(msg)
		if m.mode == modeZoom && m.emu != nil && sm.ID == m.zoomID {
			writeEmu(m.emu, sm.Data)
		}
		if m.mode == modeGroupZoom {
			if emu := m.groupEmus[sm.ID]; emu != nil {
				writeEmu(emu, sm.Data) // demux by id into the member's tile
			}
		}
		return m, waitOutput(m.client.Output)

	case statsEventMsg:
		m.applyEvent(proto.ServerMsg(msg))
		return m, waitStats(m.client.Stats)

	case telemetryEventMsg:
		m.applyTelemetry(proto.ServerMsg(msg))
		return m, tea.Batch(m.takeBell(), waitTelemetry(m.client.Telemetry))

	case configEventMsg:
		m.applyEvent(proto.ServerMsg(msg))
		return m, waitConfig(m.client.Config)

	case connClosedMsg:
		// The backend dropped. Rather than vanish, stay up and alert in the footer
		// so the user can see it and recover — C-t S restarts the daemon and
		// reattaches, C-t q detaches. The clock tick keeps the cockpit rendering.
		m.backendDown = true
		m.status = "error: backend down — " + keyLabel(m.effPrefix()) + " S to restart"
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
		m.ageStatus()
		return m, tick()

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		if m.input != inputNone { // a text-input overlay (incl. zoom search) captures keys in every mode
			return m.handleInput(msg)
		}
		if m.scrolling { // scroll mode owns the keyboard until esc/q
			return m.handleScrollKey(msg)
		}
		switch m.mode {
		case modeZoom:
			return m.handleZoomKey(msg)
		case modeGroupZoom:
			return m.handleGroupZoomKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

// zoomRows is the rows available to the zoomed panel (the terminal height less
// the footer row).
func (m model) zoomRows() int {
	if m.height < 2 {
		return 1
	}
	return m.height - 1
}

// statusTTL is how many idle ticks a transient status survives before the footer
// settles back to its resting line.
const statusTTL = 4

// ageStatus lets a one-off status message fade: once the same status has sat
// unchanged for statusTTL ticks, the footer reverts to its resting line. Errors
// stay put — they are not noise to clear. No per-call-site setter is needed; it
// simply watches for the status to stop changing.
func (m *model) ageStatus() {
	resting := m.restingStatus()
	if m.status == resting || strings.HasPrefix(m.status, "error") {
		return
	}
	if m.status != m.lastStatus {
		m.lastStatus = m.status
		m.statusAge = 0
		return
	}
	if m.statusAge++; m.statusAge >= statusTTL {
		m.status = resting
		m.statusAge = 0
	}
}

// restingStatus is the footer's quiet, default line — where the status settles
// between actions.
func (m model) restingStatus() string {
	if m.endpoint != "" {
		return "attached · " + m.endpoint
	}
	return "dashboard"
}

// sendf sends a command if there is a live client (a no-op in tests).
func (m model) sendf(cmd proto.Command) {
	if m.client != nil {
		_ = m.client.Send(cmd)
	}
}

func (m *model) applyEvent(sm proto.ServerMsg) {
	switch sm.Type {
	case "welcome":
		m.version = sm.Version
		m.serverVer = sm.ServerVer
		m.backendDown = false // a fresh welcome means the backend is live again
		if sm.Version != proto.ProtocolVersion {
			m.status = "error: server speaks " + sm.Version + ", client " + proto.ProtocolVersion
		} else {
			m.status = "attached · " + m.endpoint
		}
	case "panels":
		// Capture what the cursor and the split focus rest on before the fleet
		// changes under them, so both can be restored to the same item by identity
		// rather than left on a raw index that now points elsewhere.
		focusID := m.focusedMemberID()
		onDash := m.mode == modeDashboard
		selKind, selID, selGroup, hadSel := dashKind(0), "", "", false
		if onDash {
			selKind, selID, selGroup, hadSel = m.selectedKey()
		}
		m.fleet = mergeFleet(sm.Panels)
		m.groupShown = shownForGroups(sm.Groups)
		if onDash {
			m.restoreCursor(selKind, selID, selGroup, hadSel)
		} else {
			m.clampCursor()
		}
		m.pruneMarks()
		if m.mode == modeGroupZoom {
			// Re-derive the pin set from the fresh, server-owned flags before the
			// tiles reconcile, so a pin toggled by another client lands here too.
			// Read the parent group's FULL membership (fleetGroup), never the
			// scope-narrowed groupMembers: in summary scope the latter is only the
			// collapsed half, so deriving from it would drop the pinned tiles'
			// flags and silently revert the user's curation on exit.
			m.groupPinned = pinsForMembers(m.fleetGroup())
			m.reconcileGroupTiles(focusID)
		}
		m.refreshAttention()
	case "stats":
		m.cpuPct = sm.CPU
		m.memUsed, m.memTotal = sm.MemUsed, sm.MemTotal
	case "diff":
		// The server spawned a transient diff panel and replied with its id. Synthesize
		// a panel for it and auto-zoom; zoomInto already sends attach+resize and clears
		// zoomGroupOrigin, so this is a direct zoom that the dismiss path then reaps.
		title := m.pendingDiffTitle
		if title == "" {
			title = "diff"
		}
		m.pendingDiffTitle = ""
		*m = m.zoomInto(panel.Panel{ID: sm.ID, Title: title, State: panel.Running})
		m.zoomEphemeral = true
	case "config":
		// The daemon pushed its merged effective config (defaults <- YAML <- plugin)
		// and the plugin command list. Apply the config over the cockpit's own and
		// refresh the picker; a malformed blob is ignored so a bad plugin never wedges
		// the frontend. Live view state is untouched, like the C-t R in-place reload.
		if len(sm.Config) > 0 {
			var cfg config.Config
			if err := json.Unmarshal(sm.Config, &cfg); err == nil {
				*m = m.applyPrefs(prefsFromConfig(cfg))
			}
		}
		m.pluginCommands = sm.Commands
	case "notice":
		// A plugin-originated toast (baton.notify). It rides the transient status line
		// and fades like any other one-off message.
		m.status = sm.Notice
	case "error":
		m.status = "error: " + sm.Error
	}
}

// applyTelemetry merges the Monitor's live fields — state, activity, sparkline —
// into the current fleet by id, leaving the panel set, order, selection, and group
// tiles untouched. Telemetry refreshes panels; it never adds or removes them (a
// structural "panels" snapshot does that). Updating in place, and skipping ids the
// fleet no longer holds, keeps a telemetry tick built just before a close — and
// delivered on its own channel, out of order with that close — from resurrecting
// the closed panel.
func (m *model) applyTelemetry(sm proto.ServerMsg) {
	live := make(map[string]proto.Panel, len(sm.Panels))
	for _, p := range sm.Panels {
		live[p.ID] = p
	}
	for i := range m.fleet {
		if p, ok := live[m.fleet[i].ID]; ok {
			m.fleet[i].State = panel.ParseState(p.State)
			m.fleet[i].Activity = p.Activity
			m.fleet[i].Spark = p.Spark
		}
	}
	m.refreshAttention()
}

// refreshAttention fires a footer notification on the rising edge of a panel
// entering the attention state — when the Monitor decides it needs you. It tracks
// the set of panels currently flagged (attnSeen) so the pop fires once per entry,
// not every tick a panel sits waiting; a panel that resolves and later needs you
// again notifies afresh. The persistent count lives in the footer badge; this is
// the one-shot nudge that names the panel the moment it calls for you. An error
// status is left in place — it is not noise to bury under a notification.
func (m *model) refreshAttention() {
	cur := make(map[string]bool)
	var fresh []string
	for _, p := range m.fleet {
		if p.State != panel.Attention {
			continue
		}
		cur[p.ID] = true
		if !m.attnSeen[p.ID] {
			fresh = append(fresh, p.Title)
		}
	}
	m.attnSeen = cur
	if len(fresh) == 0 {
		return
	}
	if m.bellEnabled {
		m.bellPending = true // audible nudge on the rising edge, even when an error status hides the text
	}
	if strings.HasPrefix(m.status, "error") {
		return
	}
	if len(fresh) == 1 {
		m.status = "◆ " + fresh[0] + " needs you"
	} else {
		m.status = fmt.Sprintf("◆ %d panels need your attention", len(fresh))
	}
}

// bell rings the terminal once by writing the BEL control byte to the tty. It is
// emitted as a command so it rides bubbletea's own output cycle; BEL prints no
// glyph and moves no cursor, so it never disturbs the alt-screen the cockpit
// draws. Sent to stderr to stay off the renderer's stdout stream.
func bell() tea.Msg {
	_, _ = os.Stderr.WriteString("\a")
	return nil
}

// takeBell returns the bell command once when a panel has just entered attention,
// clearing the pending flag so the nudge sounds a single time per rising edge.
func (m *model) takeBell() tea.Cmd {
	if !m.bellPending {
		return nil
	}
	m.bellPending = false
	return bell
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()

	// The send-signal picker owns the keyboard until a signal is chosen or esc.
	if m.mode == modeSignal {
		return m.handleSignalKey(key)
	}

	// The plugin command picker owns the keyboard until a command runs or esc.
	if m.mode == modeCommand {
		return m.handleCommandKey(key)
	}

	// A text-input overlay is open: route the keystroke to it.
	if m.input != inputNone {
		return m.handleInput(k)
	}

	// Capturing a rebind: the next key press (other than esc) becomes the new
	// chord for the binding — or the leader key — under edit.
	if m.editing {
		m.editing = false
		if key == "esc" {
			m.status = "rebind cancelled"
			return m, nil
		}
		if m.editIdx == editPrefix {
			old := m.effPrefix()
			m.prefixKey = key
			m.status = fmt.Sprintf("prefix: %s → %s", keyLabel(old), keyLabel(key))
		} else {
			m.ensureBinds()
			old := m.binds[m.editIdx].key
			m.binds[m.editIdx].key = key
			m.status = fmt.Sprintf("rebound %q: %s → %s", m.binds[m.editIdx].desc, old, key)
		}
		if err := m.saveConfig(); err != nil {
			m.status = "rebound, but save failed: " + err.Error()
		}
		return m, nil
	}

	// A close is waiting on a y/n answer. Only an explicit yes goes through;
	// anything else safely cancels.
	if m.pendingClose {
		m.pendingClose = false
		if key == "y" || key == "enter" {
			m.closeSelected()
		} else {
			m.status = "close cancelled"
		}
		return m, nil
	}

	// A force-restart is waiting on a y/n answer. It tears down the daemon and the
	// whole fleet, so it always confirms; only an explicit yes goes through.
	if m.pendingRestart {
		m.pendingRestart = false
		if key == "y" || key == "enter" {
			m.restart = true
			m.quitting = true
			m.status = "restarting the server…"
			return m, tea.Quit
		}
		m.status = "restart cancelled"
		return m, nil
	}

	pkey := m.effPrefix()

	// In command mode the prefix is only for the universal escapes (C-t d / C-t
	// g); every other action is a single key.
	if m.prefix {
		m.prefix = false
		if b, ok := m.lookupEscape(key); ok {
			return m.runAction(b.act)
		}
		if key == m.bindingKey(actDetach) { // C-t q detaches from every mode
			return m.runAction(actDetach)
		}
		m.status = "no escape for " + keyLabel(key)
		return m, nil
	}

	// In the overlays, e edits the selected row — the leader/binding key in the
	// key map, the shell path in panel config. Everywhere else it falls through to
	// the command dispatch below (on the dashboard it is the rename binding).
	if key == "e" {
		switch m.mode {
		case modeKeyMap:
			switch kind, idx := m.keyMapRow(); kind {
			case rowPrefix:
				m.editing = true
				m.editIdx = editPrefix
				m.status = "press the new prefix key  ·  esc cancels"
			case rowBinding:
				m.editing = true
				m.editIdx = idx
				m.status = "press the new key for " + fmt.Sprintf("%q", m.keymap()[idx].desc) + "  ·  esc cancels"
			}
			return m, nil
		case modePanelConfig:
			return m.editPanelRow()
		}
	}

	switch key {
	case pkey:
		m.prefix = true
		return m, nil
	case keyCtrlC, keyCtrlE:
		// Emergency-quit keys are captured here: command mode exits only through
		// the detach binding, so nudge the user toward it instead of quitting.
		m.status = m.exitHint()
		return m, nil

	case "up", "k":
		if m.mode == modeHelp { // the read-only help scrolls by its own offset
			m.scrollHelp(-1)
			return m, nil
		}
		m.move(-m.cols())
		return m, nil
	case "down", "j":
		if m.mode == modeHelp {
			m.scrollHelp(1)
			return m, nil
		}
		m.move(m.cols())
		return m, nil
	case "left", "h":
		m.move(-1)
		return m, nil
	case "right", "l":
		m.move(1)
		return m, nil
	case "shift+up", "shift+left":
		// Reorder the selected item earlier on the dashboard (no effect in the
		// single-column overlays, which are not user-orderable).
		if m.mode == modeDashboard {
			return m.reorderDashItem(-1), nil
		}
		return m, nil
	case "shift+down", "shift+right":
		if m.mode == modeDashboard {
			return m.reorderDashItem(1), nil
		}
		return m, nil
	case "tab":
		// In the key map, tab jumps to the next purpose section; elsewhere it
		// cycles the selection forward, wrapping like the group split's focus.
		if m.mode == modeKeyMap {
			m.jumpSection(1)
		} else {
			m.cycle(1)
		}
		return m, nil
	case "shift+tab":
		if m.mode == modeKeyMap {
			m.jumpSection(-1)
		} else {
			m.cycle(-1)
		}
		return m, nil

	case "enter":
		return m.activate()
	case "esc":
		// The help and the key map restore whichever view opened them (the split
		// and zoom keep their live state); other overlays fall back to the dashboard.
		if m.mode == modeHelp || m.mode == modeKeyMap {
			m.mode = m.helpFrom
			m.status = ""
			return m, nil
		}
		if m.mode == modeDashboard && m.filter != "" { // esc on the dashboard clears an applied filter first
			m.filter, m.cursor = "", 0
			m.status = "filter cleared"
			return m, nil
		}
		if m.mode != modeDashboard {
			return m.runAction(actDashboard)
		}
	default:
		// On the dashboard every command is a single key.
		if m.mode == modeDashboard {
			if b, ok := m.lookupCmd(key); ok {
				return m.runAction(b.act)
			}
		}
	}
	return m, nil
}

// The panel-config screen's rows, in display order; the cursor indexes them.
const (
	panelRowShell    = iota // the default shell new panels run
	panelRowReplayKB        // the per-panel replay buffer (KiB)
	numPanelConfigRows
)

// editPanelRow opens the editor for the selected panel-config row.
func (m model) editPanelRow() (tea.Model, tea.Cmd) {
	if m.cursor == panelRowReplayKB {
		return m.editReplayKB(), nil
	}
	return m.editShellPath(), nil
}

// editShellPath opens the text-input overlay to edit the default shell, seeded
// with the current value.
func (m model) editShellPath() model {
	m.input = inputShellPath
	m.inputBuf = m.shellPath
	m.status = "default shell · type a path (blank = system), enter to save"
	return m
}

// editReplayKB opens the text-input overlay to edit the per-panel replay buffer,
// seeded with the current value (blank when it is the server default).
func (m model) editReplayKB() model {
	m.input = inputReplayKB
	m.inputBuf = ""
	if m.replayKB > 0 {
		m.inputBuf = strconv.Itoa(m.replayKB)
	}
	m.status = "replay buffer · KiB per panel (blank = default), enter to save"
	return m
}

// replayLabel describes the configured replay buffer for the panel-config row; an
// unset (zero) value reads as the server default.
func replayLabel(kb int) string {
	if kb <= 0 {
		return "default"
	}
	return fmt.Sprintf("%d KiB", kb)
}

// commitReplayKB applies the typed replay-buffer size (KiB): blank clears it back
// to the server default, a whole non-negative number sets it, and anything else
// is rejected with the overlay left open on the attempt. The daemon reads this at
// startup, so the new size lands on the next server restart, not the running one.
func (m model) commitReplayKB(s string) model {
	if s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			m.input = inputReplayKB // keep the overlay open with the attempt
			m.inputBuf = s
			m.status = "replay buffer · enter a whole number of KiB"
			return m
		}
		m.replayKB = n
	} else {
		m.replayKB = 0 // back to the server default
	}
	if err := m.saveConfig(); err != nil {
		m.status = "save failed: " + err.Error()
		return m
	}
	m.status = "replay buffer · " + replayLabel(m.replayKB) + " · restart to apply"
	return m
}

// handleInput routes a keystroke to the active text-input overlay: printable
// runes append, backspace deletes, enter submits, esc cancels.
func (m model) handleInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		if m.input == inputFilter { // esc out of the filter clears it back to the whole fleet
			m.filter, m.cursor = "", 0
		}
		m.input, m.inputHint = inputNone, ""
		m.status = "cancelled"
	case tea.KeyEnter:
		return m.commitInput()
	case tea.KeyBackspace:
		if r := []rune(m.inputBuf); len(r) > 0 {
			m.inputBuf = string(r[:len(r)-1])
		}
		m.inputHint = ""
	case tea.KeyCtrlB: // delete the word (path segment) before the cursor
		m.inputBuf = deleteLastWord(m.inputBuf)
		m.inputHint = ""
	case tea.KeyTab: // complete a path input toward an existing directory entry
		if inputIsPath(m.input) {
			m.inputBuf, m.inputHint = completePath(m.inputBuf)
		}
	case tea.KeySpace:
		m.inputBuf += " "
		m.inputHint = ""
	case tea.KeyRunes:
		m.inputBuf += string(k.Runes)
		m.inputHint = ""
	}
	// The filter narrows the dashboard live as you type, so mirror the field into
	// the filter and keep the cursor in range against the shrinking list.
	if m.input == inputFilter {
		m.filter, m.cursor = m.inputBuf, 0
	}
	return m, nil
}

// openFilter opens the live dashboard filter, seeded with the current filter so
// reopening it lets you refine rather than restart.
func (m model) openFilter() model {
	m.input = inputFilter
	m.inputBuf = m.filter
	m.status = "filter · type to find panels · enter applies · esc clears"
	return m
}

// inputIsPath reports whether an overlay edits a filesystem path, so tab
// completion applies — the new-agent workdir, a new panel's command, and the
// default shell are all paths; group and rename names are not.
func inputIsPath(p inputPurpose) bool {
	switch p {
	case inputAgentDir, inputNewPanelCmd, inputShellPath:
		return true
	}
	return false
}

// deleteLastWord trims trailing spaces, then one trailing path separator, then
// the run up to the previous separator — so Ctrl-B clears a path segment (or a
// word) at a time, leaving the separator before it in place.
func deleteLastWord(s string) string {
	r := []rune(s)
	i := len(r)
	for i > 0 && r[i-1] == ' ' {
		i--
	}
	if i > 0 && r[i-1] == '/' {
		i--
	}
	for i > 0 && r[i-1] != ' ' && r[i-1] != '/' {
		i--
	}
	return string(r[:i])
}

// completePath extends a typed path toward the directory entries that match its
// final segment. One match completes in full (a directory gains a trailing "/");
// several share their longest common prefix and the candidates become the hint.
// It returns the (possibly unchanged) text and a hint to show under the field.
func completePath(in string) (string, string) {
	expanded := in
	if home, err := os.UserHomeDir(); err == nil {
		switch {
		case in == "~":
			expanded = home
		case strings.HasPrefix(in, "~/"):
			expanded = filepath.Join(home, in[2:])
		}
	}

	dir, base := filepath.Split(expanded)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return in, "no such directory"
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	// The last segment is byte-identical in `in` and `expanded` (only the leading
	// ~ expands), so its length locates the typed prefix to re-attach.
	prefix := in[:len(in)-len(base)]
	switch len(names) {
	case 0:
		return in, "no match"
	case 1:
		return prefix + names[0], names[0]
	default:
		return prefix + longestCommonPrefix(names), strings.Join(names, "  ")
	}
}

// longestCommonPrefix returns the longest string that prefixes every entry.
func longestCommonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
			if p == "" {
				return ""
			}
		}
	}
	return p
}

// commitInput applies the typed text to whatever opened the overlay.
func (m model) commitInput() (tea.Model, tea.Cmd) {
	buf := strings.TrimSpace(m.inputBuf)
	purpose := m.input
	m.input, m.inputHint = inputNone, ""

	switch purpose {
	case inputShellPath:
		m.shellPath = buf
		if err := m.saveConfig(); err != nil {
			m.status = "save failed: " + err.Error()
		} else {
			m.status = "default shell · " + shellLabel(buf)
		}
	case inputReplayKB:
		return m.commitReplayKB(buf), nil
	case inputNewPanelCmd:
		return m.spawnPanel(buf), nil
	case inputAgentDir:
		return m.spawnAgent(buf), nil
	case inputGroupName:
		return m.commitGroup(buf), nil
	case inputRename:
		return m.commitRename(buf), nil
	case inputSignalName:
		return m.commitOtherSignal(buf)
	case inputFilter:
		m.filter, m.cursor = buf, 0
		if buf == "" {
			m.status = "filter cleared"
		} else {
			m.status = "filter · " + buf
		}
	case inputSearch:
		return m.runSearch(buf), nil
	}
	return m, nil
}

// spawnPanel asks the server to create a shell panel running command (empty =
// the default shell).
func (m model) spawnPanel(command string) model {
	if m.client != nil {
		if err := m.client.Send(proto.Command{Action: "panel.create", Kind: proto.KindShell, Path: command}); err != nil {
			m.status = "send failed: " + err.Error()
			return m
		}
	}
	m.status = "spawning " + shellLabel(command)
	return m
}

// resolveAgent picks the agent profile the new-agent action spawns: the
// configured default (falling back to "claude"), looked up in the user's
// profiles and then the built-ins. ok is false when the named profile is unknown.
func (m model) resolveAgent() (config.AgentProfile, string, bool) {
	name := m.defaultAgent
	if name == "" {
		name = defaultAgentName
	}
	if prof, ok := m.agents[name]; ok {
		return prof, name, true
	}
	if prof, ok := builtinAgent(name); ok {
		return prof, name, true
	}
	return config.AgentProfile{}, name, false
}

// spawnAgent asks the server to create an agent panel: the resolved profile's
// command and args, run in dir — the directory the agent operates on. An empty
// dir falls back to the user's home, so an agent always lands somewhere sensible.
func (m model) spawnAgent(dir string) model {
	prof, name, ok := m.resolveAgent()
	if !ok {
		m.status = fmt.Sprintf("no agent profile %q configured", name)
		return m
	}
	dir = expandDir(dir)
	if m.client != nil {
		cmd := proto.Command{Action: "panel.create", Kind: proto.KindAgent, Path: prof.Command, Args: prof.Args, Dir: dir}
		if err := m.client.Send(cmd); err != nil {
			m.status = "send failed: " + err.Error()
			return m
		}
	}
	m.status = fmt.Sprintf("spawning %s · %s", name, dirLabel(dir))
	return m
}

// defaultWorkdir is the directory offered when spawning a panel: the user's
// configured workdir, or their home — never the client's current directory, so a
// new panel does not silently inherit wherever baton was launched from.
func (m model) defaultWorkdir() string {
	if m.workdir != "" {
		return m.workdir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

// expandDir resolves a typed workdir to an absolute path: ~ and ~/… expand to the
// home directory, a relative path resolves against the client's cwd, and an empty
// path falls back to home. The agent runs on the server, so an absolute path is
// what travels unambiguously over the socket.
func expandDir(dir string) string {
	dir = strings.TrimSpace(dir)
	home, _ := os.UserHomeDir()
	switch {
	case dir == "" || dir == "~":
		return home
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(home, dir[2:])
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// dirLabel shortens a workdir for the status line, replacing the home directory
// with ~ so a long absolute path stays readable. The match is on a path boundary,
// so a sibling like /Users/bobby is never mistaken for a child of /Users/bob.
func dirLabel(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dir
	}
	if dir == home {
		return "~"
	}
	if rest := strings.TrimPrefix(dir, home+string(os.PathSeparator)); rest != dir {
		return "~" + string(os.PathSeparator) + rest
	}
	return dir
}

// shellLabel describes a configured shell path; an empty path means the system
// default.
func shellLabel(path string) string {
	if path == "" {
		return "system default"
	}
	return path
}

// openHelp shows the read-only key list for the current view (?), remembering
// where it was opened from so esc restores it (the split and zoom keep their live
// state).
func (m model) openHelp(from mode) model {
	m.helpFrom = from
	m.mode = modeHelp
	m.helpScroll = 0 // open at the top
	m.status = "keys"
	return m
}

// scrollHelp moves the read-only help list by delta rows, clamped so the last row
// never scrolls past the bottom. The help view has no cursor, so the arrows drive
// this offset directly.
func (m *model) scrollHelp(delta int) {
	_, body := m.helpContent()
	off := m.helpScroll + delta
	if maxOff := len(body) - m.panelVisibleRows(helpReserved); off > maxOff {
		off = maxOff
	}
	if off < 0 {
		off = 0
	}
	m.helpScroll = off
}

// openEditMap shows the editable key map (C-t k), remembering the originating
// view so esc restores it.
func (m model) openEditMap(from mode) model {
	m.helpFrom = from
	m.mode = modeKeyMap
	m.cursor = 0
	m.status = "key map"
	return m
}

// runAction performs a binding's verb. Both the prefix handler and the key map's
// enter key funnel through here.
func (m model) runAction(a action) (tea.Model, tea.Cmd) {
	switch a {
	case actNewPanel:
		return m.spawnPanel(m.shellPath), nil
	case actNewForm:
		m.input = inputNewPanelCmd
		m.inputBuf = m.shellPath
		m.status = "new panel · type the command, enter to spawn"
	case actNewAgent:
		_, name, ok := m.resolveAgent()
		if !ok {
			m.status = fmt.Sprintf("no agent profile %q configured", name)
			return m, nil
		}
		m.input = inputAgentDir
		m.inputBuf = m.defaultWorkdir()
		m.status = fmt.Sprintf("new %s agent · type the workdir, enter to spawn", name)
	case actClose:
		it, ok := m.selectedItem()
		switch {
		case !ok:
			m.status = "no panel to close"
		case m.confirmClose || it.kind == itemGroup:
			// A group close retires every member at once, so w always confirms it; a
			// lone panel asks only when confirm-on-close is on (it defaults on).
			m.pendingClose = true
			m.status = it.closePrompt()
		default:
			m.closeSelected()
		}
	case actRespawn:
		// Re-run the focused panel when it is a restored (or crashed) dead slot. Only a
		// single exited panel is a valid target; a group card or a live panel is not.
		it, ok := m.selectedItem()
		switch {
		case !ok || it.kind != itemPanel:
			m.status = "no panel to re-run"
		case it.panel.State != panel.Exited:
			m.status = "panel is still running"
		default:
			m.sendf(proto.Command{Action: "panel.respawn", ID: it.panel.ID})
			m.status = "re-running " + it.panel.Title
		}
	case actPurge:
		if n := m.countState(panel.Exited); n == 0 {
			m.status = "no exited panels to purge"
		} else {
			m.sendf(proto.Command{Action: "panel.purge"})
			m.status = fmt.Sprintf("purging %d exited panel(s)", n)
		}
	case actSignal:
		// From the dashboard the target is the selection: one panel, or every live
		// member of a group folded into the selected card. Exited panels are left
		// out, so the picker's count is what will actually be delivered.
		it, ok := m.selectedItem()
		if !ok {
			m.status = "no panel to signal"
			return m, nil
		}
		members := it.members
		if it.kind == itemPanel {
			members = []panel.Panel{it.panel}
		}
		ids := liveIDs(members)
		if len(ids) == 0 {
			m.status = "no live panel to signal"
			return m, nil
		}
		scope := it.title()
		if it.kind == itemGroup {
			scope = fmt.Sprintf("%s (%d panels)", it.name, len(ids))
		}
		return m.openSignalPicker(modeDashboard, ids, scope), nil
	case actSearch:
		// On the dashboard, f opens the live panel filter. In a zoom it is reached
		// after the prefix (handleZoomKey) and searches the scrollback instead.
		return m.openFilter(), nil
	case actDiff:
		// Pop up the work-tree diff of the selected agent panel. On the dashboard the
		// target is the highlighted item; the group split reaches this on the focused
		// member, and a zoom on the zoomed panel (both via handleZoomKey paths). The
		// agent-only gate here is a UX nicety — the server is authoritative.
		switch m.mode {
		case modeGroupZoom:
			p, ok := m.focusedMember()
			if !ok {
				return m, nil
			}
			m.requestDiff(p)
		default:
			it, ok := m.selectedItem()
			if !ok || it.kind != itemPanel {
				m.status = "diff: select an agent panel"
				return m, nil
			}
			m.requestDiff(it.panel)
		}
		return m, nil
	case actDashboard:
		m.mode = modeDashboard
		m.cursor = 0
		m.scrolling, m.copySelecting = false, false // never carry scroll/copy state to the dashboard
		m = m.clearSearch()
		m.status = "dashboard"
	case actHelp:
		return m.openHelp(m.mode), nil
	case actEditMap:
		return m.openEditMap(m.mode), nil
	case actScroll:
		return m.enterScroll(), nil
	case actPanelConfig:
		m.mode = modePanelConfig
		m.cursor = 0
		m.status = "panel config"
	case actRestart:
		// A force-restart stops the daemon and starts a fresh one, ending every
		// panel it owns — so confirm before pulling the rug.
		m.pendingRestart = true
		m.status = "force-restart the server? this ends every panel · (y/n)"
		return m, nil
	case actReload:
		// Tell the daemon to re-read its config in place (the fleet keeps running),
		// then re-read the cockpit's own prefs so the key map, toggles, and panel
		// defaults all update live — no detach, no restart.
		m.sendf(proto.Command{Action: "server.reload"})
		m = m.applyPrefs(loadPrefs())
		m.status = "config reloaded · backend + cockpit"
		return m, mouseCmd(m.mouseEnabled) // re-assert mouse reporting to match the reloaded toggle
	case actDetach:
		m.quitting = true
		return m, tea.Quit

	case actMark:
		if it, ok := m.selectedItem(); ok {
			m.toggleMark(it)
			m.status = m.markStatus()
		}
	case actGroup:
		return m.startGroup(), nil
	case actAdd:
		return m.addMarkedToGroup(), nil
	case actUngroup:
		return m.ungroupSelected(), nil
	case actRename:
		return m.startRename(), nil
	case actGroupView:
		return m.enterGroupView()
	case actCommands:
		return m.openCommandPicker(m.mode), nil
	}
	return m, nil
}

// activate acts on the current selection: run the highlighted binding in the key
// map, or focus the selected panel on the dashboard.
func (m model) activate() (tea.Model, tea.Cmd) {
	if m.mode == modeKeyMap {
		switch kind, idx := m.keyMapRow(); kind {
		case rowPrefix:
			m.status = "press e to change the prefix key"
		case rowBinding:
			return m.runAction(m.keymap()[idx].act)
		case rowSetting:
			var cmd tea.Cmd
			switch idx {
			case settingBell:
				m.bellEnabled = !m.bellEnabled
				m.status = "bell: " + onOff(m.bellEnabled)
			case settingMouse:
				m.mouseEnabled = !m.mouseEnabled
				m.status = "mouse: " + onOff(m.mouseEnabled)
				cmd = mouseCmd(m.mouseEnabled) // flip the terminal's mouse reporting now
			default:
				m.confirmClose = !m.confirmClose
				m.status = "confirm on close: " + onOff(m.confirmClose)
			}
			if err := m.saveConfig(); err != nil {
				m.status = "toggled, but save failed: " + err.Error()
			}
			return m, cmd
		}
		return m, nil
	}
	if m.mode == modePanelConfig {
		return m.editPanelRow()
	}
	// Dashboard: zoom into the selected panel, or open the group's split.
	if it, ok := m.selectedItem(); ok {
		if it.kind == itemGroup {
			return m.zoomGroup(it), nil
		}
		return m.zoomInto(it.panel), nil
	}
	return m, nil
}

// zoomInto opens a terminal emulator for panel p and attaches to its PTY: output
// streams into the emulator and keystrokes are forwarded back. baton owns the
// screen, so the footer (rendered in View) is always safe.
func (m model) zoomInto(p panel.Panel) model {
	m.mode = modeZoom
	m.zoomID = p.ID
	m.zoomTitle = p.Title
	m.zoomArmed = false
	m.zoomEphemeral = false // a fresh zoom is normal; the diff path sets this true after
	m.scrollOff = 0         // a fresh zoom opens at the live bottom
	m.scrolling = false
	m.zoomGroupOrigin = "" // a direct zoom; the group path sets this after
	m.zoomExited = p.State == panel.Exited
	m.cursorHidden = nil
	if m.width > 0 && m.height > 1 {
		m.emu = vt.NewSafeEmulator(m.width, m.zoomRows())
		// Track the program's cursor visibility (DECTCEM) so a program that hides
		// its cursor — vim, a BBS reader — does not get a phantom one drawn over it.
		hidden := false
		m.cursorHidden = &hidden
		m.emu.SetCallbacks(vt.Callbacks{CursorVisibility: func(visible bool) { hidden = !visible }})
		// Drain the emulator's input side (encoded keys + query replies) to the
		// PTY. The goroutine ends when zoomDetach closes the emulator.
		go zoomReader(m.emu, m.client, p.ID)
	}
	m.sendf(proto.Command{Action: "panel.resize", ID: p.ID, Rows: m.zoomRows(), Cols: m.width})
	m.sendf(proto.Command{Action: "panel.attach", ID: p.ID})
	if m.zoomExited {
		m.status = "result · " + p.Title + " (exited)"
	} else {
		m.status = "zoomed · " + p.Title
	}
	return m
}

// fleetPanel returns the fleet panel with the given id.
func (m model) fleetPanel(id string) (panel.Panel, bool) {
	for _, p := range m.fleet {
		if p.ID == id {
			return p, true
		}
	}
	return panel.Panel{}, false
}

// requestDiff asks the server for the work-tree diff of an agent panel and stashes
// the title to give the diff zoom that the "diff" reply will open. Only agent
// panels are eligible (a client-side gate for UX; the server re-checks); a
// non-agent target sets a hint and sends nothing.
func (m *model) requestDiff(p panel.Panel) {
	if !p.IsAgent() {
		m.status = "diff: select an agent panel"
		return
	}
	m.pendingDiffTitle = "diff · " + p.Title
	m.sendf(proto.Command{Action: "panel.diff", ID: p.ID})
	m.status = "diff · " + p.Title
}

// handleZoomKey forwards keystrokes to the zoomed panel, treating the prefix as
// a leader: prefix+dashboard detaches, prefix+prefix sends a literal prefix.
func (m model) handleZoomKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.zoomArmed {
		m.zoomArmed = false
		if key == m.effPrefix() {
			if m.emu != nil {
				feedKey(m.emu, k) // prefix+prefix sends a literal prefix
			}
			return m, nil
		}
		if b, ok := m.lookupEscape(key); ok {
			switch b.act {
			case actDashboard: // C-t d → dashboard, always
				return m.zoomDetach()
			case actGroupView: // C-t g → back to the split it came from
				if m.zoomGroupOrigin != "" {
					return m.backToGroup()
				}
			case actEditMap: // C-t k → edit the key map
				return m.openEditMap(modeZoom), nil
			case actScroll: // C-t [ → scroll mode, reached on every terminal
				return m.enterScroll(), nil
			}
			return m, nil
		}
		if key == m.bindingKey(actDetach) { // C-t q detaches from a zoom too
			// A transient diff panel is reaped on the way out, even when detaching the
			// whole cockpit — it is never persisted, so it must not outlive its zoom.
			if m.zoomEphemeral {
				m.sendf(proto.Command{Action: "panel.close", ID: m.zoomID})
				m.zoomEphemeral = false
			}
			return m.runAction(actDetach)
		}
		if b, ok := m.lookupCmd(key); ok && b.act == actHelp { // C-t ? → the key list
			return m.openHelp(modeZoom), nil
		}
		if b, ok := m.lookupCmd(key); ok && b.act == actReload { // C-t R → reload config
			return m.runAction(actReload)
		}
		if b, ok := m.lookupCmd(key); ok && b.act == actSignal { // C-t s → signal this panel
			if m.zoomExited {
				m.status = "panel has exited — nothing to signal"
				return m, nil
			}
			return m.openSignalPicker(modeZoom, []string{m.zoomID}, m.zoomTitle), nil
		}
		if b, ok := m.lookupCmd(key); ok && b.act == actSearch { // C-t f → search the scrollback
			return m.openSearch(), nil
		}
		if b, ok := m.lookupCmd(key); ok && b.act == actDiff { // C-t D → diff of the zoomed agent panel
			if m.zoomEphemeral { // already a diff zoom — no diff-of-a-diff
				m.status = "diff: already showing a diff"
				return m, nil
			}
			p, ok := m.fleetPanel(m.zoomID)
			if !ok || !p.IsAgent() {
				m.status = "diff: select an agent panel"
				return m, nil
			}
			m.requestDiff(p)
			return m, nil
		}
		return m, nil
	}
	if key == m.effPrefix() {
		m.zoomArmed = true
		return m, nil
	}
	// Every bare key drives the program — including PgUp/PgDn, which a full-screen
	// reader (vim, a BBS like ptt.cc) redraws for itself. baton's own scrollback is
	// on the leader above. A keystroke also returns the view to the live bottom.
	m.scrollOff = 0
	if m.emu != nil {
		feedKey(m.emu, k)
	}
	return m, nil
}

// enterScroll starts scroll mode for the zoomed panel or the focused group tile:
// a tmux-style copy mode where the arrows and page keys walk the history and no
// key reaches the program. A no-op where there is nothing to scroll.
func (m model) enterScroll() model {
	if emu, _ := m.scrollTarget(); emu == nil {
		m.status = "nothing to scroll here"
		return m
	}
	m.scrolling = true
	m.copySelecting = false // a fresh scroll session starts with no selection
	m.status = "scroll · ↑↓ line · b/Spc page · v select · y copy · esc exits"
	return m
}

// exitScroll leaves scroll mode and returns to the live bottom, dropping any
// active search along with it.
func (m model) exitScroll() model {
	m.scrolling = false
	m.scrollOff = 0
	m.copySelecting = false
	m = m.clearSearch()
	m.status = ""
	return m
}

// scrollTarget is the emulator scroll mode drives and how many rows tall it is:
// the zoom's own emulator, or the focused group tile's. nil when neither applies
// (e.g. the focus rests on a list row, or there is no client).
func (m model) scrollTarget() (*vt.SafeEmulator, int) {
	switch m.mode {
	case modeZoom:
		return m.emu, m.zoomRows()
	case modeGroupZoom:
		if p, ok := m.focusedMember(); ok {
			_, _, rows := m.tileGeometry()
			return m.groupEmus[p.ID], rows
		}
	}
	return nil, 0
}

// handleScrollKey drives scroll mode: arrows / k / j move a line, b / space /
// PgUp / PgDn move a page, and esc or q leaves. Other keys are ignored so a
// stray press never drops you out mid-scroll.
func (m model) handleScrollKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	emu, rows := m.scrollTarget()
	if emu == nil {
		return m.exitScroll(), nil
	}
	page := max(1, rows-1)
	switch k.String() {
	case "up", "k":
		m.scrollEmu(emu, 1)
	case "down", "j":
		m.scrollEmu(emu, -1)
	case "b", "pgup", "ctrl+b":
		m.scrollEmu(emu, page)
	case " ", "pgdown", "ctrl+f":
		m.scrollEmu(emu, -page)
	case "g", "home":
		m.scrollEmu(emu, 1<<30) // clamps to the oldest line
	case "G", "end":
		m.scrollEmu(emu, -(1 << 30)) // clamps to the live bottom
	case "n": // next search hit (older) — a no-op when no search is active
		return m.gotoMatch(-1), nil
	case "N": // previous search hit (newer)
		return m.gotoMatch(1), nil
	case "v": // mark / clear the copy selection anchor
		return m.copyToggle(), nil
	case "y": // copy the selection (or the visible page) to the clipboard
		return m.yankSelection()
	case "esc", "q":
		return m.exitScroll(), nil
	}
	return m, nil
}

// mouseWheelLines is how many lines one wheel notch scrolls — a few at a time so
// the wheel feels responsive without flying past the output.
const mouseWheelLines = 3

// handleMouse routes a mouse event. Only the wheel is wired: over a zoom or a
// focused group tile it walks the scrollback (entering scroll mode on the way up
// and leaving it once back at the live bottom), and everywhere else it steps the
// selection like the arrow keys. The toggle is off by default, so these only fire
// once the user has opted into mouse reporting. Non-wheel buttons are ignored, so
// a stray click never disturbs the view.
func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.input != inputNone {
		return m, nil // a prompt (filter, search, rename…) owns the view — don't scroll behind it
	}
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	up := msg.Button == tea.MouseButtonWheelUp
	down := msg.Button == tea.MouseButtonWheelDown
	if !up && !down {
		return m, nil
	}
	// Over a scrollable emulator the wheel drives the scrollback viewport.
	if emu, _ := m.scrollTarget(); emu != nil && (m.mode == modeZoom || m.mode == modeGroupZoom) {
		if up {
			if !m.scrolling {
				m = m.enterScroll() // wheel-up from the live bottom opens scroll mode
			}
			m.scrollEmu(emu, mouseWheelLines)
		} else {
			m.scrollEmu(emu, -mouseWheelLines)
			if m.scrolling && m.scrollOff == 0 {
				m = m.exitScroll() // wheeled back to the bottom: drop out of scroll mode
			}
		}
		return m, nil
	}
	// In a zoom or split with nothing to scroll (no tile focused, no emulator yet),
	// the wheel does nothing — it must never reach back and move the hidden dashboard.
	if m.mode == modeZoom || m.mode == modeGroupZoom {
		return m, nil
	}
	// Anywhere else the wheel steps the selection, like the arrow keys.
	if up {
		m.move(-1)
	} else {
		m.move(1)
	}
	return m, nil
}

// cursorHiddenNow reports whether the zoomed program has hidden its cursor.
func (m model) cursorHiddenNow() bool {
	return m.cursorHidden != nil && *m.cursorHidden
}

// zoomDetach leaves the zoom, returning to a refreshed dashboard.
func (m model) zoomDetach() (tea.Model, tea.Cmd) {
	m.sendf(proto.Command{Action: "panel.detach", ID: m.zoomID})
	// A transient diff panel is reaped when the zoom that shows it is dismissed —
	// it is never persisted, so leaving its zoom must also close it server-side.
	if m.zoomEphemeral {
		m.sendf(proto.Command{Action: "panel.close", ID: m.zoomID})
		m.zoomEphemeral = false
	}
	closeZoom(m.emu) // stops the zoomReader goroutine (Read returns io.EOF)
	m.mode = modeDashboard
	m.emu = nil
	m.scrollOff = 0
	m.scrolling = false
	m.copySelecting = false
	m = m.clearSearch()
	m.cursorHidden = nil
	m.zoomID, m.zoomTitle, m.zoomArmed, m.zoomExited, m.zoomGroupOrigin = "", "", false, false, ""
	m.status = "dashboard"
	if m.client != nil {
		return m, func() tea.Msg { _ = m.client.Send(proto.Command{Action: "panel.list"}); return nil }
	}
	return m, nil
}

// bindingKey returns the bare key bound to an action, or "" if none.
func (m model) bindingKey(a action) string {
	for _, b := range m.keymap() {
		if b.act == a {
			return b.key
		}
	}
	return ""
}

// exitHint is the message shown when a captured emergency-quit key (Ctrl-C /
// Ctrl-E) is pressed in command mode, where leaving is only via the detach
// binding — never an accidental Ctrl-C.
func (m model) exitHint() string {
	return "exit is disabled here — press " + keyLabel(m.bindingKey(actDetach)) + " to detach"
}

// closeSelected asks the server to close the highlighted item and drops its
// panels from the local fleet for immediate feedback; the server's broadcast
// then re-syncs the authoritative list. Closing a group closes every member.
func (m *model) closeSelected() {
	it, ok := m.selectedItem()
	if !ok {
		return
	}
	ids := it.ids()
	// One command closes the whole item — a lone panel or every member of a group
	// — so the server retires them together and broadcasts a single snapshot.
	if m.client != nil {
		if err := m.client.Send(proto.Command{Action: "panel.close", IDs: ids}); err != nil {
			m.status = "close failed: " + err.Error()
			return
		}
	}
	gone := make(map[string]bool, len(ids))
	for _, id := range ids {
		gone[id] = true
		delete(m.marked, id)
	}
	m.fleet = slices.DeleteFunc(m.fleet, func(p panel.Panel) bool { return gone[p.ID] })
	m.clampCursor()
	m.status = "closed · " + it.title()
}

// move shifts the cursor by delta within the active list, clamped to its bounds.
func (m *model) move(delta int) {
	n := m.itemCount()
	if n == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
}

// cycle steps the cursor by delta and wraps at the ends, so tab walks the whole
// list as a ring — the same behaviour as the group split, where tab cycles the
// member focus. The grid arrows still clamp via move; only tab wraps.
func (m *model) cycle(delta int) {
	m.cursor = wrapIndex(m.cursor, delta, m.itemCount())
}

// wrapIndex steps index i by delta within [0, n) and wraps at both ends — the
// shared ring-step behind the dashboard's tab and the group split's focus. It is
// safe for an empty list (n <= 0), returning 0.
func wrapIndex(i, delta, n int) int {
	if n <= 0 {
		return 0
	}
	return ((i+delta)%n + n) % n
}

func (m model) itemCount() int {
	switch m.mode {
	case modeKeyMap:
		return len(m.keymap()) + 1 + numSettings // prefix row + bindings + the settings toggles
	case modePanelConfig:
		return numPanelConfigRows // default shell + replay buffer
	default:
		return len(m.dashItems())
	}
}

// countState is how many panels are in a given lifecycle state — used for the
// footer's attention badge and the purge candidate count.
func (m model) countState(s panel.State) int {
	n := 0
	for _, p := range m.fleet {
		if p.State == s {
			n++
		}
	}
	return n
}

func (m *model) clampCursor() {
	if n := m.itemCount(); m.cursor >= n {
		m.cursor = max(0, n-1)
	}
}

// selectedKey captures the identity of the selected dashboard item — a panel by
// id, or a group by name — read before a snapshot so restoreCursor can land the
// cursor back on the same item once the fleet has refolded. ok is false when
// nothing is selected.
func (m model) selectedKey() (kind dashKind, id, group string, ok bool) {
	it, ok := m.selectedItem()
	if !ok {
		return itemPanel, "", "", false
	}
	if it.kind == itemGroup {
		return itemGroup, "", it.name, true
	}
	return itemPanel, it.panel.ID, "", true
}

// restoreCursor moves the cursor back onto the item selectedKey captured, after
// the fleet changed: the same group by name, or the same panel by id. A lone
// panel that has since been folded into a group lands the cursor on that group,
// so the selection follows the panel into its new home. It falls back to a
// bounds clamp when the item is gone (closed, exited-and-purged).
func (m *model) restoreCursor(kind dashKind, id, group string, had bool) {
	if !had {
		m.clampCursor()
		return
	}
	items := m.dashItems()
	for i, it := range items {
		switch {
		case kind == itemGroup && it.kind == itemGroup && it.name == group:
			m.cursor = i
			return
		case kind == itemPanel && it.kind == itemPanel && it.panel.ID == id:
			m.cursor = i
			return
		}
	}
	// The panel may have been grouped since the last snapshot: follow it into the
	// group that now holds it.
	if kind == itemPanel {
		for i, it := range items {
			if it.kind == itemGroup && indexOfMember(it.members, id) >= 0 {
				m.cursor = i
				return
			}
		}
	}
	m.clampCursor()
}

// pruneMarks drops marks on panels the latest snapshot no longer carries, so a
// closed or exited-and-purged panel never lingers in a pending selection.
func (m *model) pruneMarks() {
	if len(m.marked) == 0 {
		return
	}
	live := make(map[string]bool, len(m.fleet))
	for _, p := range m.fleet {
		live[p.ID] = true
	}
	for id := range m.marked {
		if !live[id] {
			delete(m.marked, id)
		}
	}
}

// gridCols is how many cards fit on a row at the given width (1–3).
func gridCols(width int) int {
	return min(3, max(1, width/(cardWidth+cardGap)))
}

// cols is how many panel cards sit on a row in the current view. The key map,
// panel config, and tree view are single-column lists; the card grid uses
// gridCols (which the grid renderer calls directly, so it never rebuilds the
// item list just to count columns).
func (m model) cols() int {
	if m.mode != modeDashboard || m.treeView() {
		return 1
	}
	return gridCols(m.width)
}

// --- rendering ---

// View renders the cockpit. It isolates the frame from a panic in the render of
// untrusted program output (a misbehaving full-screen program, or an emulator
// parser edge case): rather than crash the whole TUI, it logs the stack and shows
// a recoverable placeholder so the next frame redraws clean.
func (m model) View() (out string) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Bytes("stack", debug.Stack()).Msg("recovered a render panic")
			out = "baton: a render glitch was recovered — press any key to refresh\r\n"
		}
	}()
	return m.render()
}

func (m model) render() string {
	if m.quitting {
		if m.restart {
			return "baton: restarting the server…\n"
		}
		return "baton: detached (server still running)\n"
	}
	if m.width == 0 || m.height == 0 {
		return "" // wait for the first size message
	}
	if m.mode == modeZoom {
		return m.zoomView()
	}
	if m.mode == modeGroupZoom {
		return m.groupZoomView()
	}

	header := lipgloss.JoinVertical(lipgloss.Center,
		bannerStyle.Render(banner),
		"",
		subStyle.Render("a next-gen, agent-friendly terminal multiplexer"),
		mutedStyle.Render(m.versionLine()),
	)

	var body string
	switch {
	case m.input != inputNone:
		body = m.inputView()
	case m.mode == modeHelp:
		body = m.helpView()
	case m.mode == modeKeyMap:
		body = m.keyMapView()
	case m.mode == modePanelConfig:
		body = m.panelConfigView()
	case m.mode == modeSignal:
		body = m.signalPickerView()
	case m.mode == modeCommand:
		body = m.commandPickerView()
	default:
		body = m.dashboardView()
	}

	content := lipgloss.JoinVertical(lipgloss.Center, header, "", body)
	// Center the cockpit over the terminal's own (transparent) background; the
	// panels are transparent too, so only their borders carry the brand colour.
	placed := lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center, content)
	return placed + "\n" + m.footer()
}

// zoomView renders the emulated panel screen filling the top rows, with a cursor
// drawn at the emulator's cursor cell and the zoom footer pinned to the last
// line. baton owns every cell, so the footer can never be smeared by the program
// inside.
func (m model) zoomView() string {
	rows := m.zoomRows()
	lines := make([]string, rows)
	if m.emu != nil {
		lines = m.selectWindow(m.emu, m.width, rows, m.scrollOff)
		// Draw the cursor only on the live bottom (a scrolled-back view is history),
		// and only when the program has not hidden it — so a full-screen reader that
		// turns the cursor off does not show a phantom block.
		if m.scrollOff == 0 && !m.cursorHiddenNow() {
			cur := m.emu.CursorPosition()
			if cur.Y >= 0 && cur.Y < len(lines) {
				lines[cur.Y] = overlayCursor(lines[cur.Y], cur.X)
			}
		}
	}
	footer := m.zoomFooter()
	return strings.Join(lines, "\n") + "\n" + footer
}

// dashboardView renders the status summary strip above the fleet. A small fleet
// shows as a card grid; once it grows past treeThreshold it switches to a
// space-efficient tree + preview split.
func (m model) dashboardView() string {
	items := m.dashItems() // built once and threaded through the render below
	heading := sectionStyle.Render(spaced("FLEET")) +
		mutedStyle.Render(fmt.Sprintf("   %d panel(s)  ", len(m.fleet))) + fleetBreakdown(m.fleet, items)
	if m.filter != "" {
		heading += "  " + seg("⌕ "+truncate(m.filter, 20), colDark, colCyan) +
			mutedStyle.Render(fmt.Sprintf("  %d match(es)", len(items)))
	}
	summary := m.summaryStrip()
	body := m.cardGrid(items)
	if len(items) > treeThreshold {
		body = m.treeAndPreview(items)
	}
	if m.filter != "" && len(items) == 0 {
		body = mutedStyle.Render("no panels match \"" + truncate(m.filter, 24) + "\"  ·  esc clears the filter")
	}
	return lipgloss.JoinVertical(lipgloss.Center, heading, "", summary, "", body)
}

// treeView reports whether there are enough dashboard items to swap the card
// grid for the tree + preview split. Groups count as one item, so collapsing a
// crowd of panels into a work item can drop the dashboard back to the grid.
func (m model) treeView() bool {
	return m.mode == modeDashboard && len(m.dashItems()) > treeThreshold
}

// summaryStrip is a row of chips counting panels in each state.
func (m model) summaryStrip() string {
	counts := stateCounts(m.fleet)
	chips := make([]string, 0, len(stateOrder))
	for _, st := range stateOrder {
		n := counts[st]
		if n == 0 {
			continue
		}
		info := states[st]
		led := lipgloss.NewStyle().Foreground(info.color).Render(info.led)
		chips = append(chips, fmt.Sprintf("%s %s", led, mutedStyle.Render(fmt.Sprintf("%d %s", n, info.label))))
	}
	if len(chips) == 0 {
		return mutedStyle.Render("no panels yet — press C-t p to spawn one")
	}
	return strings.Join(chips, mutedStyle.Render("   ·   "))
}

// cardGrid lays the dashboard out as a responsive grid of cards: a card per lone
// panel, and one collapsed card per group.
func (m model) cardGrid(items []dashItem) string {
	if len(items) == 0 {
		return ""
	}
	cols := gridCols(m.width) // grid mode here, so always the multi-column count
	rows := make([]string, 0, (len(items)+cols-1)/cols)
	for i := 0; i < len(items); i += cols {
		end := min(i+cols, len(items))
		cards := make([]string, 0, cols)
		for j := i; j < end; j++ {
			cards = append(cards, m.renderItemCard(items[j], j == m.cursor))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// renderItemCard draws a dashboard item: a group card for a work item, otherwise
// a panel card.
func (m model) renderItemCard(it dashItem, selected bool) string {
	if it.kind == itemGroup {
		return m.renderGroupCard(it, selected)
	}
	return m.renderCard(it.panel, selected)
}

const (
	cardWidth = 32            // outer width of one panel card, incl. border + padding
	cardGap   = 1             // horizontal margin between cards
	cardInner = cardWidth - 4 // usable text width inside the border + padding

	treeThreshold = 6  // fleets larger than this swap the grid for the tree split
	treeListWidth = 30 // outer width of the tree pane, incl. border + padding
)

// renderCard draws one panel as three tidy lines that never wrap: a status LED +
// title, a kind badge + state, and a sparkline + meta footer. The selected card
// glows in the brand colour.
func (m model) renderCard(p panel.Panel, selected bool) string {
	info := states[p.State]

	border := colFaint
	titleColor := colInk
	if selected {
		border = colBrand
		titleColor = colBrandHi
	}

	mark := ""
	if m.selecting() {
		mark = markCell(m.marked[p.ID])
	}
	led := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render(info.led)
	title := lipgloss.NewStyle().Foreground(titleColor).Bold(true).Render(truncate(p.Title, cardInner-4))
	head := mark + led + " " + title

	badge := kindBadge(p.Kind)
	state := lipgloss.NewStyle().Foreground(info.color).Render(info.label)
	kindLine := badge + "  " + state

	spark := lipgloss.NewStyle().Foreground(info.color).Render(p.Spark)
	footer := spark + "  " + mutedStyle.Render(truncate(p.Activity, cardInner-lipgloss.Width(spark)-2))

	style := lipgloss.NewStyle().
		Width(cardWidth-2).
		Padding(0, 1).
		MarginRight(cardGap).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)

	return style.Render(lipgloss.JoinVertical(lipgloss.Left, head, kindLine, footer))
}

// kindBadge tags a panel as an agent or a plain shell.
func kindBadge(kind panel.Kind) string {
	bg := colShell // shell: teal
	if kind == panel.Agent {
		bg = colAgent // agent: blue
	}
	label := strings.ToUpper(kind.String())
	return lipgloss.NewStyle().Foreground(colDark).Background(bg).Bold(true).Padding(0, 1).Render(label)
}

// treeAndPreview renders the large-fleet layout: a compact, scrolling tree of
// items (panels and groups) on the left, and a preview window for the selected
// item on the right. The two panes share a height so they read as a unit and
// stay within the dashboard's vertical space.
func (m model) treeAndPreview(items []dashItem) string {
	previewW := m.width - treeListWidth - 2 // 1-cell gutter, leave a little air
	previewW = min(64, max(34, previewW))

	visible := m.treeVisibleRows(len(items))
	start, end := scrollWindow(m.cursor, len(items), visible)

	tree := m.renderTree(items, start, end, visible)
	preview := m.renderPreview(items, previewW-4) // inner text width

	h := max(lipgloss.Height(tree), lipgloss.Height(preview))
	pane := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)

	leftPane := pane.
		Width(treeListWidth - 2).Height(h).
		BorderForeground(colBrand).
		Render(tree)
	rightPane := pane.
		Width(previewW - 2).Height(h).
		BorderForeground(colFaint).
		Render(preview)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPane, " ", rightPane)
}

// treeVisibleRows is how many item rows fit in the tree pane at the current
// height, after reserving the banner, headings, summary, footer, and borders. It
// never claims more rows than there are items (count).
func (m model) treeVisibleRows(count int) int {
	const reserved = 18 // banner + headings + summary + footer + pane chrome
	v := m.height - reserved
	if v < 3 {
		v = 3
	}
	if v > count {
		v = count
	}
	return v
}

// scrollWindow returns the [start, end) slice of a count-long list to show in a
// visible-row window, biased to keep the cursor centred and always in view.
func scrollWindow(cursor, count, visible int) (int, int) {
	if visible >= count {
		return 0, count
	}
	start := cursor - visible/2
	if start < 0 {
		start = 0
	}
	if start > count-visible {
		start = count - visible
	}
	return start, start + visible
}

// renderTree is the left pane: the windowed slice [start,end) of dashboard items,
// one line each — a panel (LED + name) or a group (▣ + name + count) — with the
// selected row lit in the brand colour and a position counter when it scrolls.
func (m model) renderTree(items []dashItem, start, end, visible int) string {
	header := sectionStyle.Render(spaced("FLEET"))
	if visible < len(items) {
		header += mutedStyle.Render(fmt.Sprintf("  %d/%d", m.cursor+1, len(items)))
	}

	rows := make([]string, 0, visible+2)
	rows = append(rows, header, "")
	for i := start; i < end; i++ {
		it := items[i]
		selected := i == m.cursor
		caret := "  "
		if selected {
			caret = "▸ "
		}
		// Mark the clipped edges so it is clear the list continues.
		if i == start && start > 0 {
			caret = mutedStyle.Render("↑ ")
		} else if i == end-1 && end < len(items) {
			caret = mutedStyle.Render("↓ ")
		}

		mark := ""
		if m.selecting() {
			mark = markCell(m.itemMarked(it))
		}

		var glyph, label string
		if it.kind == itemGroup {
			info := states[groupState(it.members)]
			glyph = lipgloss.NewStyle().Foreground(info.color).Render("▣")
			label = fmt.Sprintf("%s (%d)", it.title(), len(it.members))
		} else {
			info := states[it.panel.State]
			glyph = lipgloss.NewStyle().Foreground(info.color).Render(info.led)
			label = it.title()
		}
		name := truncate(label, treeListWidth-9-lipgloss.Width(mark))

		row := lipgloss.NewStyle().Width(treeListWidth - 4)
		if selected {
			row = row.Foreground(colDark).Background(colBrand).Bold(true)
		} else {
			row = row.Foreground(colInk)
		}
		rows = append(rows, row.Render(caret+mark+glyph+" "+name))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// renderPreview is the right pane: a metadata block for the selected panel, or a
// member roster for the selected group.
func (m model) renderPreview(items []dashItem, width int) string {
	if m.cursor < 0 || m.cursor >= len(items) {
		return mutedStyle.Render("no panel selected")
	}
	it := items[m.cursor]
	if it.kind == itemGroup {
		return m.renderGroupPreview(it, width)
	}
	p := it.panel
	info := states[p.State]

	title := lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render(truncate(p.Title, width))
	led := lipgloss.NewStyle().Foreground(info.color).Render(info.led)
	statusLine := led + " " + kindBadge(p.Kind) + "  " + lipgloss.NewStyle().Foreground(info.color).Render(info.label)
	rule := mutedStyle.Render(strings.Repeat("─", width))

	meta := lipgloss.JoinVertical(lipgloss.Left,
		metaRow("state", info.label, info.color),
		metaRow("kind", p.Kind.String(), colInk),
		metaRow("activity", p.Activity, colInk),
		metaRow("signal", p.Spark, info.color),
	)

	return lipgloss.JoinVertical(lipgloss.Left, title, statusLine, rule, meta)
}

// metaRow formats one aligned "label  value" line for the preview pane.
func metaRow(label, value string, valColor lipgloss.Color) string {
	l := mutedStyle.Render(fmt.Sprintf("%-9s", label))
	v := lipgloss.NewStyle().Foreground(valColor).Render(value)
	return l + " " + v
}

// helpView is the read-only key list for the current stage — the keys that view
// responds to — shown when ? (or C-t ? in a zoom) is pressed.
func (m model) helpView() string {
	title, body := m.helpContent()
	pfx := keyLabel(m.effPrefix())
	legend := mutedStyle.Render("esc  back   ·   " + pfx + " " + keyLabel(m.bindingKey(actEditMap)) + "  edit")
	return m.renderScrollPanel(scrollPanel{
		title:    title + " KEYS",
		body:     body,
		footer:   []string{"", legend},
		reserved: helpReserved,
		anchor:   m.helpScroll, // read-only: the arrows drive this offset directly
		centered: false,
		clipHint: mutedStyle.Render("   ↑↓ scroll"),
	})
}

// helpContent builds the read-only key list for the view help was opened from:
// the section title and the category-grouped body rows (with subheaders), ready
// for windowing. Shared by helpView and the help scroller's clamp.
func (m model) helpContent() (title string, body []string) {
	kc := func(s string) string { return keycapStyle.Render(s) }
	pfx := keyLabel(m.effPrefix())
	dash := keyLabel(m.bindingKey(actDashboard))
	detach := keyLabel(m.bindingKey(actDetach))

	// helpRow is one key line tagged with the purpose section it sorts under, so
	// every stage's list groups by category just like the editable key map.
	type helpRow struct{ cat, keys, desc string }

	var rows []helpRow
	switch m.helpFrom {
	case modeGroupZoom:
		title = "GROUP VIEW"
		rows = []helpRow{
			{"Navigation", kc("tab") + " " + kc("S-tab"), "focus the next / previous panel"},
			{"Navigation", kc(keyLabel(keyInteract)), "interact: type into the focused panel in place"},
			{"Navigation", kc("enter"), "zoom the focused panel"},
			{"Navigation", kc("+") + " " + kc("-"), "show more / fewer live tiles"},
			{"Navigation", kc("S-←→"), "reorder the focused panel"},
			{"Navigation", kc(pfx) + " " + kc(keyScroll), "scroll mode · the focused panel (↑↓ line, b/Spc page)"},
			{"Navigation", kc(pfx) + " " + kc(keySearch), "search the focused panel · n older, N newer"},
			{"Work items", kc(keyLabel(keyPin)), "pin / unpin the focused panel to a live tile"},
			{"Work items", kc(keyLabel(keySignal)) + " " + kc(keyLabel(keySignalAll)), "signal the focused panel · the whole group"},
			{"Work items", kc(keyLabel(keyRemove)), "remove the focused panel from the group"},
			{"View", kc(keyLabel(m.bindingKey(actHelp))), "this key list"},
			{"View", kc(dash) + " " + kc("esc"), "back to the dashboard"},
			{"View", kc(pfx) + " " + kc(keyLabel(keyInteract)), "stop interacting (while in interact)"},
			{"View", kc(pfx) + " " + kc(dash), "dashboard (works in every view)"},
			{"View", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actEditMap))), "edit the key map"},
			{"Session", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actReload))), "reload config (backend + cockpit)"},
			{"Session", kc(pfx) + " " + kc(detach), "detach (server keeps running)"},
		}
	case modeZoom:
		title = "ZOOM"
		rows = []helpRow{
			{"Navigation", kc("type"), "drive the program directly (PgUp/PgDn included)"},
			{"Navigation", kc(pfx) + " " + kc(keyScroll), "scroll mode · ↑↓ line, b/Spc page, esc exits"},
			{"Navigation", kc(pfx) + " " + kc(keySearch), "search the scrollback · n older, N newer"},
			{"Navigation", kc(pfx) + " " + kc(pfx), "send a literal " + pfx},
			{"Panels", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actSignal))), "send a signal to this panel"},
			{"View", kc(pfx) + " " + kc(dash), "back to the dashboard"},
			{"View", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actGroupView))), "back to the group view"},
			{"View", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actHelp))), "this key list"},
			{"View", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actEditMap))), "edit the key map"},
			{"Session", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actReload))), "reload config (backend + cockpit)"},
			{"Session", kc(pfx) + " " + kc(detach), "detach (server keeps running)"},
		}
	default: // dashboard — single keys for commands, C-t for the escapes
		title = "DASHBOARD"
		rows = []helpRow{
			{"Navigation", kc("hjkl") + " " + kc("↑↓←→"), "move"},
			{"Navigation", kc("S-←→"), "reorder the selected item"},
			{"Navigation", kc("enter"), "open / zoom"},
			{"Navigation", kc("esc"), "clear the selection"},
		}
		for _, b := range m.keymap() {
			keys := kc(keyLabel(b.key))
			if isEscape(b.act) {
				keys = kc(pfx) + " " + kc(keyLabel(b.key))
			}
			rows = append(rows, helpRow{b.cat, keys, b.desc})
		}
	}

	// Render category by category in a stable order, so the list always reads the
	// same way and matches the editable key map's grouping.
	keyCol := lipgloss.NewStyle().Width(20)
	subhead := lipgloss.NewStyle().Foreground(colFaint).Bold(true)
	for _, cat := range []string{"Navigation", "Panels", "Work items", "View", "Session"} {
		shown := false
		for _, r := range rows {
			if r.cat != cat {
				continue
			}
			if !shown {
				body = append(body, "", "  "+subhead.Render(cat))
				shown = true
			}
			body = append(body, keyCol.Render(r.keys)+mutedStyle.Render(r.desc))
		}
	}
	return title, body
}

func (m model) keyMapView() string {
	caret := func(on bool) string {
		if on {
			return lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ ")
		}
		return "  "
	}

	desc := func(on bool, s string) string {
		if on {
			return inkStyle.Render(s)
		}
		return mutedStyle.Render(s)
	}

	binds := m.keymap()
	prefLbl := keyLabel(m.effPrefix())

	// Build the scrollable body — the selectable rows and their section
	// subheaders — tracking the rendered line the cursor rests on so the window
	// can keep it in view on a short screen.
	body := make([]string, 0, len(binds)+9)
	selLine := 0

	// Row 0: the leader/prefix key.
	prefSel := m.cursor == 0
	prefCap := keycapStyle
	if prefSel {
		prefCap = keycapHotStyle
	}
	prefKeys := prefCap.Render(prefLbl)
	if m.editing && m.editIdx == editPrefix {
		prefKeys = keycapHotStyle.Render("…type a key")
	}
	body = append(body, fmt.Sprintf("%s%s   %s", caret(prefSel), prefKeys, desc(prefSel, "prefix · leader key")))
	if prefSel {
		selLine = len(body) - 1
	}

	// Rows 1..N: the bindings, under a sub-header per purpose group. Prefixed
	// commands show as a [C-t][key] chord; the bare group verbs show as a single
	// keycap.
	subhead := lipgloss.NewStyle().Foreground(colFaint).Bold(true)
	prevCat := ""
	for i, b := range binds {
		if b.cat != prevCat {
			body = append(body, "", "  "+subhead.Render(b.cat))
			prevCat = b.cat
		}
		selected := i+1 == m.cursor
		// Escapes are prefixed (C-t d); commands are a single key.
		esc := isEscape(b.act)
		var keys string
		switch {
		case m.editing && m.editIdx == i && esc:
			keys = keycapHotStyle.Render(prefLbl) + " " + keycapHotStyle.Render("…type a key")
		case m.editing && m.editIdx == i:
			keys = keycapHotStyle.Render("…type a key")
		case esc:
			keys = chord(prefLbl, b.key, selected)
		default:
			cap := keycapStyle
			if selected {
				cap = keycapHotStyle
			}
			keys = cap.Render(keyLabel(b.key))
		}
		body = append(body, fmt.Sprintf("%s%s   %s", caret(selected), keys, desc(selected, b.desc)))
		if selected {
			selLine = len(body) - 1
		}
	}

	// Settings: one selectable toggle per row, after the prefix and bindings.
	body = append(body, "", sectionStyle.Render(spaced("SETTINGS")), "")
	for i := 0; i < numSettings; i++ {
		selected := m.cursor == len(binds)+1+i
		on := m.settingValue(i)
		box := lipgloss.NewStyle().Foreground(colDark).Background(checkColor(on)).Bold(true).Padding(0, 1)
		check := box.Render(onOff(on))
		label := mutedStyle.Render(settingLabel(i))
		if selected {
			label = inkStyle.Render(settingLabel(i))
		}
		body = append(body, fmt.Sprintf("%s%s   %s", caret(selected), check, label))
		if selected {
			selLine = len(body) - 1
		}
	}

	// In-panel legend (the footer no longer carries key hints) and the negotiated
	// protocol version, pinned below the scrolling body.
	legendKey := lipgloss.NewStyle().Foreground(colCyan).Bold(true)
	legend := mutedStyle.Render("↑↓ move") + "   " +
		legendKey.Render("tab") + mutedStyle.Render(" section") + "   " +
		legendKey.Render("e") + mutedStyle.Render(" edit") + "   " +
		legendKey.Render("enter") + mutedStyle.Render(" run") + "   " +
		legendKey.Render("esc") + mutedStyle.Render(" back")
	about := lipgloss.NewStyle().Foreground(colFaint).Render(m.versionLine())

	return m.renderScrollPanel(scrollPanel{
		title:    "KEY BINDINGS",
		body:     body,
		footer:   []string{"", mutedStyle.Render(strings.Repeat("─", lipgloss.Width(legend))), legend, about},
		reserved: keyMapReserved,
		anchor:   selLine,
		centered: true,
		clipHint: mutedStyle.Render(fmt.Sprintf("   %d/%d", m.cursor+1, len(binds)+1+numSettings)),
	})
}

// The vertical chrome each overlay panel reserves around its scrollable body —
// box border + padding, header, any hint/legend lines, and the cockpit footer —
// so panelVisibleRows can size the body to never overflow the screen.
const (
	keyMapReserved      = 11 // header+blank, body, blank, rule, legend, about
	panelConfigReserved = 12 // header+blank, body, blank, hint, blank, rule, legend
	helpReserved        = 9  // header+blank, body, blank, legend
)

// panelVisibleRows is how many body rows an overlay panel shows before it
// scrolls, after reserving `reserved` rows for its chrome and the footer. An
// unsized model (the first frame, and unit tests) is treated as unbounded so the
// whole list renders; a real height never drops below a few rows so the panel
// stays usable on a tiny screen.
func (m model) panelVisibleRows(reserved int) int {
	if m.height <= 0 {
		return 1 << 30
	}
	if v := m.height - reserved; v > 3 {
		return v
	}
	return 3
}

// windowAround clips rows to a visible-row window centred on anchor (the selected
// line), keeping it in view; it returns the rows unchanged (clipped=false) when
// they already fit. The shared scroller for the cursor-driven overlay panels.
func windowAround(rows []string, anchor, visible int) (shown []string, clipped bool) {
	if visible >= len(rows) {
		return rows, false
	}
	start, end := scrollWindow(anchor, len(rows), visible)
	return rows[start:end], true
}

// windowFrom clips rows to a visible-row window starting at off, clamped so the
// last row never scrolls past the bottom — for a read-only panel with no cursor.
func windowFrom(rows []string, off, visible int) (shown []string, clipped bool) {
	if visible >= len(rows) {
		return rows, false
	}
	if maxOff := len(rows) - visible; off > maxOff {
		off = maxOff
	}
	if off < 0 {
		off = 0
	}
	return rows[off : off+visible], true
}

// scrollPanel is the shared layout for the cockpit's overlay popups — the key
// map, the help list, and panel config. It pins a title and a footer (legend,
// version, …) and windows the body to the screen height so a short terminal
// scrolls by the arrows instead of overflowing. All three render through
// renderScrollPanel so their chrome and scrolling stay identical.
type scrollPanel struct {
	title    string   // section header, shown spaced
	body     []string // the scrollable rows
	footer   []string // pinned lines below the body
	reserved int      // vertical chrome to reserve when sizing the body
	anchor   int      // the cursor line (centered) or the scroll offset (top)
	centered bool     // keep anchor in view (cursor panels) vs. anchor-as-offset (help)
	clipHint string   // appended to the title when the body is clipped
}

// renderScrollPanel windows p.body to the height and wraps it, the title, and the
// footer in the shared configBox.
func (m model) renderScrollPanel(p scrollPanel) string {
	visible := m.panelVisibleRows(p.reserved)
	body, clipped := windowFrom(p.body, p.anchor, visible)
	if p.centered {
		body, clipped = windowAround(p.body, p.anchor, visible)
	}
	header := sectionStyle.Render(spaced(p.title))
	if clipped {
		header += p.clipHint
	}
	out := make([]string, 0, len(body)+len(p.footer)+2)
	out = append(out, header, "")
	out = append(out, body...)
	out = append(out, p.footer...)
	return configBox(lipgloss.JoinVertical(lipgloss.Left, out...))
}

// configBox wraps a settings/overlay panel in the cockpit's bordered surface.
func configBox(body string) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBrand).
		Background(colSurface).
		Padding(1, 3).
		Render(body)
}

// panelConfigView renders the panel-defaults tab: the default shell and the
// per-panel replay buffer, one selectable row each.
func (m model) panelConfigView() string {
	row := func(idx int, label, value string) string {
		caret := "  "
		labelStyle := mutedStyle
		if m.cursor == idx {
			caret = lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ ")
			labelStyle = inkStyle
		}
		val := lipgloss.NewStyle().Foreground(colCyan).Render(value)
		return fmt.Sprintf("%s%-16s%s", caret, labelStyle.Render(label), val)
	}

	// One body line per row, so the cursor indexes it directly; the shared widget
	// windows it so a tiny terminal scrolls via the arrows.
	body := []string{
		row(panelRowShell, "default shell", shellLabel(m.shellPath)),
		row(panelRowReplayKB, "replay buffer", replayLabel(m.replayKB)),
	}
	legendKey := lipgloss.NewStyle().Foreground(colCyan).Bold(true)
	legend := mutedStyle.Render("↑↓ move") + "   " +
		legendKey.Render("e") + mutedStyle.Render(" edit") + "   " +
		legendKey.Render("esc") + mutedStyle.Render(" back")

	return m.renderScrollPanel(scrollPanel{
		title: "PANEL CONFIG",
		body:  body,
		footer: []string{"",
			mutedStyle.Render("replay buffer seeds scrollback · change applies on server restart"),
			"", mutedStyle.Render(strings.Repeat("─", lipgloss.Width(legend))), legend},
		reserved: panelConfigReserved,
		anchor:   m.cursor,
		centered: true,
		clipHint: mutedStyle.Render(fmt.Sprintf("   %d/%d", m.cursor+1, numPanelConfigRows)),
	})
}

// inputView renders the active text-input overlay as a centred popup.
func (m model) inputView() string {
	title, prompt, action := "INPUT", "value", "save"
	switch m.input {
	case inputShellPath:
		title, prompt = "DEFAULT SHELL", "shell path  (blank = system default)"
	case inputReplayKB:
		title, prompt = "REPLAY BUFFER", "KiB of history per panel  (blank = default)"
	case inputNewPanelCmd:
		title, prompt, action = "NEW PANEL", "command to run  (blank = system shell)", "spawn"
	case inputAgentDir:
		title, prompt, action = "NEW AGENT", "working directory  (blank = home)", "spawn"
	case inputGroupName:
		title, prompt, action = "NEW GROUP", "work-item name", "create"
	case inputRename:
		title, prompt, action = "RENAME", "new name", "save"
	case inputSignalName:
		title, prompt, action = "SEND SIGNAL", "signal name or number  (e.g. WINCH, TSTP, 28)", "send"
	case inputFilter:
		title, prompt, action = "FIND PANELS", "filter by title or group  (live)", "apply"
	case inputSearch:
		title, prompt, action = "SEARCH", "find in the scrollback", "find"
	}

	field := lipgloss.NewStyle().Width(46).Foreground(colInk).Background(colSurface).Render("› " + m.inputBuf + "▌")
	legendKey := lipgloss.NewStyle().Foreground(colCyan).Bold(true)
	legend := legendKey.Render("enter") + mutedStyle.Render(" "+action) + "   " +
		legendKey.Render("esc") + mutedStyle.Render(" cancel")
	if inputIsPath(m.input) {
		legend += "   " + legendKey.Render("tab") + mutedStyle.Render(" complete") +
			"   " + legendKey.Render("C-b") + mutedStyle.Render(" del word")
	}

	rows := []string{sectionStyle.Render(spaced(title)), "", mutedStyle.Render(prompt), field}
	if m.inputHint != "" {
		rows = append(rows, lipgloss.NewStyle().Foreground(colCyan).Width(46).Render(truncate(m.inputHint, 46)))
	}
	rows = append(rows, "", legend)
	return configBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// onOff renders a boolean as a fixed-width ON/OFF label.
func onOff(b bool) string {
	if b {
		return "ON "
	}
	return "OFF"
}

// checkColor is green when a setting is on, faint when off.
func checkColor(b bool) lipgloss.Color {
	if b {
		return colGreen
	}
	return colFaint
}

// seg renders one telemetry-strip segment: bold text on a solid colour cap.
func seg(text string, fg, bg lipgloss.Color) string {
	return lipgloss.NewStyle().Foreground(fg).Background(bg).Bold(true).Padding(0, 1).Render(text)
}

// footer renders the cockpit telemetry strip as one solid light-blue bar: a
// brand cap and a mode segment on the left, the live fleet breakdown (agents vs
// shells) filling the middle, and the host stats, a clock, and a green/red
// status cap on the right. The key hints live in the C-t k key map, not here, so
// the strip stays a status readout.
func (m model) footer() string {
	// Left caps: brand · mode · (attention).
	mode := "DASHBOARD"
	switch {
	case m.input != inputNone:
		mode = "INPUT"
	case m.mode == modeKeyMap:
		mode = "KEY MAP"
	case m.mode == modePanelConfig:
		mode = "PANEL CONFIG"
	}
	left := seg("◈ BATON", colDark, colBrand) + seg(mode, colInk, colBlue)
	return m.statusBar(left, m.helpHint())
}

// attentionBadge is the footer notification that some panel needs you: a red cap
// carried by every view's status bar, so a panel asking for input is visible
// whether you are on the dashboard, in a zoom, or in a group split. It names the
// panel when exactly one waits, and counts them when several do. Empty when the
// fleet is calm.
func (m model) attentionBadge() string {
	var names []string
	for _, p := range m.fleet {
		if p.State == panel.Attention {
			names = append(names, p.Title)
		}
	}
	if len(names) == 0 {
		return ""
	}
	label := fmt.Sprintf("◆ %d need you", len(names))
	if len(names) == 1 {
		label = "◆ " + truncate(names[0], 16) + " needs you"
	}
	return seg(label, colDark, states[panel.Attention].color)
}

// statusBar composes a full-width footer for any view: the view's left caps, a
// middle hint, and the always-present right side — system stats, the clock, and
// the connection status (green, red on error). The status is clipped to whatever
// space is left and the hint drops when too narrow, so the strip never spills
// onto a second line and swallows the footer.
// outageCap is the footer alert shown in every view when the backend connection
// has dropped: a loud red cap so it is obvious the cockpit is showing stale
// state. Empty while the backend is live.
func (m model) outageCap() string {
	if !m.backendDown {
		return ""
	}
	return seg("◼ BACKEND DOWN", colInk, colRed)
}

// frontendVersion is this build's version, defaulting to "dev" when unset (a
// zero-value model in tests, or an unstamped build).
func (m model) frontendVersion() string {
	if m.appVersion != "" {
		return m.appVersion
	}
	return "dev"
}

// versionLine summarises the build versions for the about line: this frontend,
// the backend once the welcome lands, and the negotiated protocol.
func (m model) versionLine() string {
	ver := m.version
	if ver == "" {
		ver = proto.ProtocolVersion
	}
	parts := "baton " + m.frontendVersion()
	if m.serverVer != "" {
		parts += " · backend " + m.serverVer
	}
	return parts + " · protocol " + ver
}

func (m model) statusBar(left, hint string) string {
	prefixBadge := ""
	if m.prefix {
		prefixBadge = seg("PREFIX", colDark, colBrandHi)
	}
	clock := seg("⏱ "+m.now.Format("15:04:05"), colDark, colCyan)
	stats := m.statsStrip()

	statusBg := colGreen
	if strings.HasPrefix(m.status, "error") {
		statusBg = colRed
	}
	caps := prefixBadge + m.outageCap() + m.attentionBadge() + stats + clock
	right := caps
	if budget := m.width - lipgloss.Width(left) - lipgloss.Width(caps) - 4; budget > 0 {
		right += seg("● "+truncate(m.status, budget), colInk, statusBg) // "● " + cap padding
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	if lipgloss.Width(hint) > gap {
		hint = ""
	}
	return left + barStyle.Width(gap).Render(hint) + right
}

// helpHint is the footer's standing invitation to the key list: "? keys" in a
// command-mode view, "C-t ? keys" in a zoom where the prefix is needed.
func (m model) helpHint() string {
	k := keyLabel(m.bindingKey(actHelp))
	if m.mode == modeZoom {
		k = keyLabel(m.effPrefix()) + " " + k
	}
	return barBold.Render(" "+k) + barStyle.Render(" keys ")
}

// statsStrip renders the system CPU and memory readout as a surface-coloured
// telemetry segment (e.g. "CPU 18%  MEM 9.2/16G"). It is blank until the first
// sample lands, so the footer never shows a bogus 0/0.
func (m model) statsStrip() string {
	if m.memTotal == 0 {
		return ""
	}
	body := barStyle.Render(" CPU ") + barBold.Render(fmt.Sprintf("%.0f%%", m.cpuPct)) +
		barStyle.Render("  MEM ") + barBold.Render(memLabel(m.memUsed, m.memTotal)) + barStyle.Render(" ")
	return barStyle.Render(body)
}

// memLabel formats a used/total byte pair in the total's unit, e.g. "9.2/16G".
func memLabel(used, total uint64) string {
	units := []string{"B", "K", "M", "G", "T", "P"}
	div, exp := uint64(1), 0
	for total/div >= 1024 && exp < len(units)-1 {
		div *= 1024
		exp++
	}
	return fmt.Sprintf("%.1f/%.0f%s", float64(used)/float64(div), float64(total)/float64(div), units[exp])
}

// spaced widens a short label by inserting a hair of space between letters,
// giving section headers an airy, control-panel feel (lipgloss has no tracking).
func spaced(s string) string {
	return strings.Join(strings.Split(s, ""), " ")
}

// truncate clips s to width runes, appending an ellipsis when it overflows.
func truncate(s string, width int) string {
	r := []rune(s)
	if width < 1 || len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}
