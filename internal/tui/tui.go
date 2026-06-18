// Package tui is the reference frontend: a keyboard-driven cockpit that attaches
// to the server and renders a mission-control dashboard of every live panel.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	vt "github.com/charmbracelet/x/vt"

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

	lastStatus string // status seen on the previous tick, to detect when it settles
	statusAge  int    // ticks the status has gone unchanged, for the transient fade
	endpoint   string // where we are attached: "local", or a host/IP for remote
	version    string // negotiated protocol version, surfaced in the key map

	confirmClose      bool      // ask y/n before closing a panel (toggled in the key map)
	allowNameConflict bool      // let two work items share a name (server enforces; kept to round-trip config)
	pendingClose      bool      // a close is awaiting y/n confirmation
	now               time.Time // wall clock shown in the footer, ticked every second

	cpuPct   float64 // system-wide CPU load %, sampled each tick for the footer
	memUsed  uint64  // system memory in use, bytes
	memTotal uint64  // total system memory, bytes

	prefixKey string    // leader key armed before a binding (default ctrl+t)
	binds     []binding // editable copy of the bindings (nil ⇒ the defaults)
	editing   bool      // capturing the next key press as a rebind
	editIdx   int       // binding being rebound; editPrefix means the leader key

	shellPath    string                         // configured default shell binary path ("" = system shell)
	defaultAgent string                         // agent profile the new-agent action spawns ("" = claude)
	agents       map[string]config.AgentProfile // user-configured agent profiles
	input        inputPurpose                   // active text-input overlay, or inputNone
	inputBuf     string                         // text typed into the overlay
	helpFrom     mode                           // the view the key map (?) was opened from, to restore on esc

	renameID    string // panel id being renamed via inputRename ("" if a group)
	renameGroup string // group being renamed via inputRename ("" if a panel)

	zoomID     string           // panel being zoomed (modeZoom)
	zoomTitle  string           // its title, for the zoom footer
	zoomArmed  bool             // prefix pressed inside a zoom, awaiting the verb
	zoomExited bool             // the zoomed panel has exited — a read-only result view
	emu        *vt.SafeEmulator // terminal emulator rendering the zoomed panel

	groupName       string                      // work item being split-viewed (modeGroupZoom)
	groupFocus      int                         // focused member, indexing tiles then the tree list
	groupArmed      bool                        // prefix pressed in the split, awaiting an escape
	groupInteract   bool                        // keys drive the focused tile in place (i), no zoom
	groupCols       int                         // tile columns; 0 = auto-fit to the window
	groupPinned     map[string]bool             // member ids the user pinned to a live tile in this view
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
	inputNewPanelCmd              // the prefix+n new-panel command popup
	inputAgentDir                 // the workdir for a new agent panel
	inputGroupName                // naming a new group from the marked panels
	inputRename                   // renaming the selected panel or group
)

// RestartRequested reports whether the cockpit exited because the user asked to
// force-restart the server. The client runner relaunches the daemon and
// re-attaches when this is set.
func (m model) RestartRequested() bool { return m.restart }

// New builds the TUI model around an attached client. The fleet starts empty and
// is filled by the server's first snapshot, which arrives right after the hello
// handshake — the server owns the panels now.
func New(c *client.Client) tea.Model {
	p := loadPrefs()
	return model{
		client:            c,
		mode:              modeDashboard,
		status:            "attaching…",
		endpoint:          c.Endpoint(),
		confirmClose:      p.confirmClose,
		allowNameConflict: p.allowNameConflict,
		now:               time.Now(),
		prefixKey:         p.prefix,
		binds:             p.binds,
		shellPath:         p.shellPath,
		defaultAgent:      p.defaultAgent,
		agents:            p.agents,
	}
}

// editPrefix is the editIdx sentinel meaning the leader key is being rebound.
const editPrefix = -1

// rowKind classifies a row of the key-map panel.
type rowKind int

const (
	rowPrefix  rowKind = iota // the leader key (row 0)
	rowBinding                // a binding (rows 1..N)
	rowSetting                // the confirm-on-close toggle (last row)
)

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
		return rowSetting, 0
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

// tick drives the footer clock, firing once a second.
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitEvent(m.client.Events), waitOutput(m.client.Output), waitStats(m.client.Stats), waitTelemetry(m.client.Telemetry), tick())
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
		return m, waitEvent(m.client.Events)

	case panelOutputMsg:
		sm := proto.ServerMsg(msg)
		if m.mode == modeZoom && m.emu != nil && sm.ID == m.zoomID {
			_, _ = m.emu.Write(sm.Data)
		}
		if m.mode == modeGroupZoom {
			if emu := m.groupEmus[sm.ID]; emu != nil {
				_, _ = emu.Write(sm.Data) // demux by id into the member's tile
			}
		}
		return m, waitOutput(m.client.Output)

	case statsEventMsg:
		m.applyEvent(proto.ServerMsg(msg))
		return m, waitStats(m.client.Stats)

	case telemetryEventMsg:
		m.applyTelemetry(proto.ServerMsg(msg))
		return m, waitTelemetry(m.client.Telemetry)

	case connClosedMsg:
		m.status = "server closed the connection"
		m.quitting = true
		return m, tea.Quit

	case tickMsg:
		m.now = time.Time(msg)
		m.ageStatus()
		return m, tick()

	case tea.KeyMsg:
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
		if onDash {
			m.restoreCursor(selKind, selID, selGroup, hadSel)
		} else {
			m.clampCursor()
		}
		m.pruneMarks()
		if m.mode == modeGroupZoom {
			m.reconcileGroupTiles(focusID)
		}
	case "stats":
		m.cpuPct = sm.CPU
		m.memUsed, m.memTotal = sm.MemUsed, sm.MemTotal
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
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()

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
			return m.editShellPath(), nil
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
		m.move(-m.cols())
		return m, nil
	case "down", "j":
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

// editShellPath opens the text-input overlay to edit the default shell, seeded
// with the current value.
func (m model) editShellPath() model {
	m.input = inputShellPath
	m.inputBuf = m.shellPath
	m.status = "default shell · type a path (blank = system), enter to save"
	return m
}

// handleInput routes a keystroke to the active text-input overlay: printable
// runes append, backspace deletes, enter submits, esc cancels.
func (m model) handleInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.input = inputNone
		m.status = "cancelled"
	case tea.KeyEnter:
		return m.commitInput()
	case tea.KeyBackspace:
		if r := []rune(m.inputBuf); len(r) > 0 {
			m.inputBuf = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		m.inputBuf += " "
	case tea.KeyRunes:
		m.inputBuf += string(k.Runes)
	}
	return m, nil
}

// commitInput applies the typed text to whatever opened the overlay.
func (m model) commitInput() (tea.Model, tea.Cmd) {
	buf := strings.TrimSpace(m.inputBuf)
	purpose := m.input
	m.input = inputNone

	switch purpose {
	case inputShellPath:
		m.shellPath = buf
		if err := m.saveConfig(); err != nil {
			m.status = "save failed: " + err.Error()
		} else {
			m.status = "default shell · " + shellLabel(buf)
		}
	case inputNewPanelCmd:
		return m.spawnPanel(buf), nil
	case inputAgentDir:
		return m.spawnAgent(buf), nil
	case inputGroupName:
		return m.commitGroup(buf), nil
	case inputRename:
		return m.commitRename(buf), nil
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

// workingDir is the client's current directory, the default workdir offered for a
// new agent. Empty if it cannot be determined.
func workingDir() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
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
	m.status = "keys"
	return m
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
		m.inputBuf = workingDir()
		m.status = fmt.Sprintf("new %s agent · type the workdir, enter to spawn", name)
	case actClose:
		if it, ok := m.selectedItem(); !ok {
			m.status = "no panel to close"
		} else if m.confirmClose {
			m.pendingClose = true
			m.status = "close " + it.title() + "? (y/n)"
		} else {
			m.closeSelected()
		}
	case actPurge:
		if n := m.countState(panel.Exited); n == 0 {
			m.status = "no exited panels to purge"
		} else {
			m.sendf(proto.Command{Action: "panel.purge"})
			m.status = fmt.Sprintf("purging %d exited panel(s)", n)
		}
	case actDashboard:
		m.mode = modeDashboard
		m.cursor = 0
		m.status = "dashboard"
	case actHelp:
		return m.openHelp(m.mode), nil
	case actEditMap:
		return m.openEditMap(m.mode), nil
	case actPanelConfig:
		m.mode = modePanelConfig
		m.cursor = 0
		m.status = "panel config"
	case actRestart:
		m.restart = true
		m.quitting = true
		m.status = "restarting the server…"
		return m, tea.Quit
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
			m.confirmClose = !m.confirmClose
			m.status = "confirm on close: " + onOff(m.confirmClose)
			if err := m.saveConfig(); err != nil {
				m.status = "toggled, but save failed: " + err.Error()
			}
		}
		return m, nil
	}
	if m.mode == modePanelConfig {
		return m.editShellPath(), nil // the only row is the default shell
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
	m.zoomGroupOrigin = "" // a direct zoom; the group path sets this after
	m.zoomExited = p.State == panel.Exited
	if m.width > 0 && m.height > 1 {
		m.emu = vt.NewSafeEmulator(m.width, m.zoomRows())
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
			}
			return m, nil
		}
		if key == m.bindingKey(actDetach) { // C-t q detaches from a zoom too
			return m.runAction(actDetach)
		}
		if b, ok := m.lookupCmd(key); ok && b.act == actHelp { // C-t ? → the key list
			return m.openHelp(modeZoom), nil
		}
		return m, nil
	}
	if key == m.effPrefix() {
		m.zoomArmed = true
		return m, nil
	}
	if m.emu != nil {
		feedKey(m.emu, k)
	}
	return m, nil
}

// zoomDetach leaves the zoom, returning to a refreshed dashboard.
func (m model) zoomDetach() (tea.Model, tea.Cmd) {
	m.sendf(proto.Command{Action: "panel.detach", ID: m.zoomID})
	closeZoom(m.emu) // stops the zoomReader goroutine (Read returns io.EOF)
	m.mode = modeDashboard
	m.emu = nil
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
		return len(m.keymap()) + 2 // prefix row + bindings + the settings toggle
	case modePanelConfig:
		return 1 // the default-shell row
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

func (m model) View() string {
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
		raw := strings.Split(m.emu.Render(), "\n")
		cur := m.emu.CursorPosition()
		for i := range lines {
			if i < len(raw) {
				lines[i] = raw[i]
			}
			if i == cur.Y {
				lines[i] = overlayCursor(lines[i], cur.X)
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
	summary := m.summaryStrip()
	body := m.cardGrid(items)
	if len(items) > treeThreshold {
		body = m.treeAndPreview(items)
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
	kc := func(s string) string { return keycapStyle.Render(s) }
	pfx := keyLabel(m.effPrefix())
	dash := keyLabel(m.bindingKey(actDashboard))
	detach := keyLabel(m.bindingKey(actDetach))

	// helpRow is one key line tagged with the purpose section it sorts under, so
	// every stage's list groups by category just like the editable key map.
	type helpRow struct{ cat, keys, desc string }

	var title string
	var rows []helpRow
	switch m.helpFrom {
	case modeGroupZoom:
		title = "GROUP VIEW"
		rows = []helpRow{
			{"Navigation", kc("tab") + " " + kc("S-tab"), "focus the next / previous panel"},
			{"Navigation", kc(keyLabel(keyInteract)), "interact: type into the focused panel in place"},
			{"Navigation", kc("enter"), "zoom the focused panel"},
			{"Navigation", kc("+") + " " + kc("-"), "more / fewer columns"},
			{"Work items", kc(keyLabel(keyPin)), "pin / unpin the focused panel to a live tile"},
			{"Work items", kc(keyLabel(keyRemove)), "remove the focused panel from the group"},
			{"View", kc(keyLabel(m.bindingKey(actHelp))), "this key list"},
			{"View", kc(dash) + " " + kc("esc"), "back to the dashboard"},
			{"View", kc(pfx) + " " + kc(keyLabel(keyInteract)), "stop interacting (while in interact)"},
			{"View", kc(pfx) + " " + kc(dash), "dashboard (works in every view)"},
			{"View", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actEditMap))), "edit the key map"},
			{"Session", kc(pfx) + " " + kc(detach), "detach (server keeps running)"},
		}
	case modeZoom:
		title = "ZOOM"
		rows = []helpRow{
			{"Navigation", kc("type"), "drive the program directly"},
			{"Navigation", kc(pfx) + " " + kc(pfx), "send a literal " + pfx},
			{"View", kc(pfx) + " " + kc(dash), "back to the dashboard"},
			{"View", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actGroupView))), "back to the group view"},
			{"View", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actHelp))), "this key list"},
			{"View", kc(pfx) + " " + kc(keyLabel(m.bindingKey(actEditMap))), "edit the key map"},
			{"Session", kc(pfx) + " " + kc(detach), "detach (server keeps running)"},
		}
	default: // dashboard — single keys for commands, C-t for the escapes
		title = "DASHBOARD"
		rows = []helpRow{
			{"Navigation", kc("hjkl") + " " + kc("↑↓←→"), "move"},
			{"Navigation", kc("S-←") + " " + kc("S-→"), "reorder the selected item"},
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
	out := []string{sectionStyle.Render(spaced(title + " KEYS"))}
	for _, cat := range []string{"Navigation", "Panels", "Work items", "View", "Session"} {
		shown := false
		for _, r := range rows {
			if r.cat != cat {
				continue
			}
			if !shown {
				out = append(out, "", "  "+subhead.Render(cat))
				shown = true
			}
			out = append(out, keyCol.Render(r.keys)+mutedStyle.Render(r.desc))
		}
	}
	out = append(out, "", mutedStyle.Render("esc  back   ·   "+keyLabel(pfx)+" "+keyLabel(m.bindingKey(actEditMap))+"  edit"))
	return configBox(lipgloss.JoinVertical(lipgloss.Left, out...))
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
	rows := make([]string, 0, len(binds)+9)
	rows = append(rows, sectionStyle.Render(spaced("KEY BINDINGS")), "")

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
	rows = append(rows, fmt.Sprintf("%s%s   %s", caret(prefSel), prefKeys, desc(prefSel, "prefix · leader key")))

	// Rows 1..N: the bindings, under a sub-header per purpose group. Prefixed
	// commands show as a [C-t][key] chord; the bare group verbs show as a single
	// keycap.
	subhead := lipgloss.NewStyle().Foreground(colFaint).Bold(true)
	prevCat := ""
	for i, b := range binds {
		if b.cat != prevCat {
			rows = append(rows, "", "  "+subhead.Render(b.cat))
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
		rows = append(rows, fmt.Sprintf("%s%s   %s", caret(selected), keys, desc(selected, b.desc)))
	}

	// Settings: the confirm-on-close toggle, selectable as the last row.
	selected := m.cursor == len(binds)+1
	box := lipgloss.NewStyle().Foreground(colDark).Background(checkColor(m.confirmClose)).Bold(true).Padding(0, 1)
	check := box.Render(onOff(m.confirmClose))
	label := mutedStyle.Render("confirm before closing a panel")
	if selected {
		label = inkStyle.Render("confirm before closing a panel")
	}
	rows = append(rows,
		"",
		sectionStyle.Render(spaced("SETTINGS")),
		"",
		fmt.Sprintf("%s%s   %s", caret(selected), check, label),
	)

	// In-panel legend (the footer no longer carries key hints) and the
	// negotiated protocol version, tucked here rather than the status bar.
	legendKey := lipgloss.NewStyle().Foreground(colCyan).Bold(true)
	legend := mutedStyle.Render("↑↓ move") + "   " +
		legendKey.Render("tab") + mutedStyle.Render(" section") + "   " +
		legendKey.Render("e") + mutedStyle.Render(" edit") + "   " +
		legendKey.Render("enter") + mutedStyle.Render(" run") + "   " +
		legendKey.Render("esc") + mutedStyle.Render(" back")
	ver := m.version
	if ver == "" {
		ver = proto.ProtocolVersion
	}
	about := lipgloss.NewStyle().Foreground(colFaint).Render("protocol " + ver)
	rows = append(rows, "", mutedStyle.Render(strings.Repeat("─", lipgloss.Width(legend))), legend, about)

	return configBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
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

// panelConfigView renders the panel-defaults tab. For now its one row is the
// default shell that new panels run.
func (m model) panelConfigView() string {
	caret := "  "
	labelStyle := mutedStyle
	if m.cursor == 0 {
		caret = lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ ")
		labelStyle = inkStyle
	}
	value := lipgloss.NewStyle().Foreground(colCyan).Render(shellLabel(m.shellPath))
	row := fmt.Sprintf("%s%-16s%s", caret, labelStyle.Render("default shell"), value)

	legendKey := lipgloss.NewStyle().Foreground(colCyan).Bold(true)
	legend := mutedStyle.Render("↑↓ move") + "   " +
		legendKey.Render("e") + mutedStyle.Render(" edit") + "   " +
		legendKey.Render("esc") + mutedStyle.Render(" back")

	return configBox(lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render(spaced("PANEL CONFIG")), "",
		row, "",
		mutedStyle.Render("C-t p spawns this · C-t n picks a command per panel"),
		"", mutedStyle.Render(strings.Repeat("─", lipgloss.Width(legend))), legend,
	))
}

// inputView renders the active text-input overlay as a centred popup.
func (m model) inputView() string {
	title, prompt, action := "INPUT", "value", "save"
	switch m.input {
	case inputShellPath:
		title, prompt = "DEFAULT SHELL", "shell path  (blank = system default)"
	case inputNewPanelCmd:
		title, prompt, action = "NEW PANEL", "command to run  (blank = system shell)", "spawn"
	case inputAgentDir:
		title, prompt, action = "NEW AGENT", "working directory  (blank = home)", "spawn"
	case inputGroupName:
		title, prompt, action = "NEW GROUP", "work-item name", "create"
	case inputRename:
		title, prompt, action = "RENAME", "new name", "save"
	}

	field := lipgloss.NewStyle().Width(46).Foreground(colInk).Background(colSurface).Render("› " + m.inputBuf + "▌")
	legendKey := lipgloss.NewStyle().Foreground(colCyan).Bold(true)
	legend := legendKey.Render("enter") + mutedStyle.Render(" "+action) + "   " +
		legendKey.Render("esc") + mutedStyle.Render(" cancel")

	return configBox(lipgloss.JoinVertical(lipgloss.Left,
		sectionStyle.Render(spaced(title)), "",
		mutedStyle.Render(prompt), field,
		"", legend,
	))
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
	if n := m.countState(panel.Attention); n > 0 {
		left += seg(fmt.Sprintf("◆ %d", n), colDark, states[panel.Attention].color)
	}
	return m.statusBar(left, m.helpHint())
}

// statusBar composes a full-width footer for any view: the view's left caps, a
// middle hint, and the always-present right side — system stats, the clock, and
// the connection status (green, red on error). The status is clipped to whatever
// space is left and the hint drops when too narrow, so the strip never spills
// onto a second line and swallows the footer.
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
	caps := prefixBadge + stats + clock
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
