// Package tui is the reference frontend: a keyboard-driven cockpit that attaches
// to the server and renders a mission-control dashboard of every live panel.
package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/client"
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
	colSurface = lipgloss.Color("236") // panel & status-bar background

	// Accents and semantics.
	colBlue  = lipgloss.Color("25")  // deep-blue mode-segment fill
	colGreen = lipgloss.Color("36")  // healthy connection
	colCyan  = lipgloss.Color("80")  // keycaps, clock, accents
	colRed   = lipgloss.Color("167") // error connection
	colDark  = lipgloss.Color("16")  // text on bright segments

	colAgent = lipgloss.Color("75") // agent-panel count (blue)
	colShell = lipgloss.Color("73") // shell-panel count (teal)
)

var (
	bannerStyle = lipgloss.NewStyle().Bold(true).Foreground(colBrand)
	subStyle    = lipgloss.NewStyle().Foreground(colMuted)
	mutedStyle  = lipgloss.NewStyle().Foreground(colMuted)
	inkStyle    = lipgloss.NewStyle().Foreground(colInk)

	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(colBrandHi)

	barStyle = lipgloss.NewStyle().Background(colSurface).Foreground(colInk)
)

type mode int

const (
	modeDashboard mode = iota
	modeKeyMap
	modePanelConfig
	modeZoom
)

type model struct {
	client *client.Client
	fleet  []panel.Panel // dummy + live panels shown on the dashboard

	mode     mode
	prefix   bool // armed by the prefix key, consumes the next key as a binding
	cursor   int  // selection index (panel card, or key-map row)
	status   string
	endpoint string // where we are attached: "local", or a host/IP for remote
	version  string // negotiated protocol version, surfaced in the key map

	confirmClose bool      // ask y/n before closing a panel (toggled in the key map)
	pendingClose bool      // a close is awaiting y/n confirmation
	now          time.Time // wall clock shown in the footer, ticked every second

	prefixKey string    // leader key armed before a binding (default ctrl+t)
	binds     []binding // editable copy of the bindings (nil ⇒ the defaults)
	editing   bool      // capturing the next key press as a rebind
	editIdx   int       // binding being rebound; editPrefix means the leader key

	shellPath string       // configured default shell binary path ("" = system shell)
	input     inputPurpose // active text-input overlay, or inputNone
	inputBuf  string       // text typed into the overlay

	zoomID     string           // panel being zoomed (modeZoom)
	zoomTitle  string           // its title, for the zoom footer
	zoomArmed  bool             // prefix pressed inside a zoom, awaiting the verb
	zoomExited bool             // the zoomed panel has exited — a read-only result view
	emu        *vt.SafeEmulator // terminal emulator rendering the zoomed panel

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
		client:       c,
		mode:         modeDashboard,
		status:       "attaching…",
		endpoint:     c.Endpoint(),
		confirmClose: p.confirmClose,
		now:          time.Now(),
		prefixKey:    p.prefix,
		binds:        p.binds,
		shellPath:    p.shellPath,
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

// lookup resolves a bare key pressed after the prefix to its binding.
func (m model) lookup(key string) (binding, bool) {
	for _, b := range m.keymap() {
		if b.key == key {
			return b, true
		}
	}
	return binding{}, false
}

// --- bubbletea event plumbing ---

type eventMsg proto.ServerMsg
type panelOutputMsg proto.ServerMsg
type connClosedMsg struct{}
type tickMsg time.Time

func waitEvent(ch chan proto.ServerMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return connClosedMsg{}
		}
		return eventMsg(msg)
	}
}

// waitOutput streams a zoomed panel's PTY output. It runs alongside waitEvent on
// a separate channel so a burst of output never blocks control messages.
func waitOutput(ch chan proto.ServerMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return connClosedMsg{}
		}
		return panelOutputMsg(msg)
	}
}

// tick drives the footer clock, firing once a second.
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitEvent(m.client.Events), waitOutput(m.client.Output), tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.mode == modeZoom && m.emu != nil {
			m.emu.Resize(m.width, m.zoomRows())
			m.sendf(proto.Command{Action: "panel.resize", ID: m.zoomID, Rows: m.zoomRows(), Cols: m.width})
		}
		return m, nil

	case eventMsg:
		m.applyEvent(proto.ServerMsg(msg))
		return m, waitEvent(m.client.Events)

	case panelOutputMsg:
		if m.mode == modeZoom && m.emu != nil && proto.ServerMsg(msg).ID == m.zoomID {
			_, _ = m.emu.Write(proto.ServerMsg(msg).Data)
		}
		return m, waitOutput(m.client.Output)

	case connClosedMsg:
		m.status = "server closed the connection"
		m.quitting = true
		return m, tea.Quit

	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()

	case tea.KeyMsg:
		if m.mode == modeZoom {
			return m.handleZoomKey(msg)
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
		m.fleet = mergeFleet(sm.Panels)
		m.clampCursor()
	case "error":
		m.status = "error: " + sm.Error
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

	// A key following the prefix is interpreted as a binding.
	if m.prefix {
		m.prefix = false
		if b, ok := m.lookup(key); ok {
			return m.runAction(b.act)
		}
		if key == pkey {
			m.status = "dashboard" // prefix-prefix is a no-op for now
			return m, nil
		}
		m.status = "no binding for " + key
		return m, nil
	}

	switch key {
	case pkey:
		m.prefix = true
		return m, nil
	case keyCtrlC:
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		m.move(-m.cols())
		return m, nil
	case "down", "j":
		m.move(m.cols())
		return m, nil
	case "left", "h", "shift+tab":
		m.move(-1)
		return m, nil
	case "right", "l", "tab":
		m.move(1)
		return m, nil

	case "e":
		// Edit the selected row's key (key map only): the leader key on the
		// prefix row, otherwise the binding at that row.
		if m.mode == modeKeyMap {
			switch kind, idx := m.keyMapRow(); kind {
			case rowPrefix:
				m.editing = true
				m.editIdx = editPrefix
				m.status = "press the new prefix key  ·  esc cancels"
			case rowBinding:
				m.editing = true
				m.editIdx = idx
				m.status = "press the new key for " + fmt.Sprintf("%q", m.keymap()[idx].desc) + "  ·  esc cancels"
			case rowSetting:
			}
		}
		if m.mode == modePanelConfig {
			return m.editShellPath(), nil
		}
		return m, nil

	case "enter":
		return m.activate()
	case "esc":
		if m.mode != modeDashboard {
			return m.runAction(actDashboard)
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

// shellLabel describes a configured shell path; an empty path means the system
// default.
func shellLabel(path string) string {
	if path == "" {
		return "system default"
	}
	return path
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
	case actClose:
		if len(m.fleet) == 0 {
			m.status = "no panel to close"
		} else if m.confirmClose {
			m.pendingClose = true
			m.status = "close " + m.fleet[m.cursor].Title + "? (y/n)"
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
	case actToggleMap:
		if m.mode == modeKeyMap {
			m.mode = modeDashboard
		} else {
			m.mode = modeKeyMap
		}
		m.cursor = 0
		m.status = "key map"
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
	// Dashboard: zoom into the selected panel.
	if m.cursor >= 0 && m.cursor < len(m.fleet) {
		return m.zoomInto(m.fleet[m.cursor]), nil
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
		switch key {
		case m.bindingKey(actDashboard):
			return m.zoomDetach()
		case m.effPrefix():
			if m.emu != nil {
				feedKey(m.emu, k) // prefix+prefix sends a literal prefix
			}
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
	m.zoomID, m.zoomTitle, m.zoomArmed, m.zoomExited = "", "", false, false
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

// closeSelected asks the server to close the highlighted panel and drops it from
// the local fleet for immediate feedback; the server's broadcast then re-syncs
// the authoritative list.
func (m *model) closeSelected() {
	if m.cursor < 0 || m.cursor >= len(m.fleet) {
		return
	}
	p := m.fleet[m.cursor]
	if m.client != nil {
		if err := m.client.Send(proto.Command{Action: "panel.close", ID: p.ID}); err != nil {
			m.status = "close failed: " + err.Error()
			return
		}
	}
	m.fleet = slices.Delete(m.fleet, m.cursor, m.cursor+1)
	m.clampCursor()
	m.status = "closed · " + p.Title
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

func (m model) itemCount() int {
	switch m.mode {
	case modeKeyMap:
		return len(m.keymap()) + 2 // prefix row + bindings + the settings toggle
	case modePanelConfig:
		return 1 // the default-shell row
	default:
		return len(m.fleet)
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

// cols is how many panel cards sit on a row at the current width (1–3). The key
// map, panel config, and tree view are single-column lists.
func (m model) cols() int {
	if m.mode != modeDashboard || m.treeView() {
		return 1
	}
	c := m.width / (cardWidth + cardGap)
	return min(3, max(1, c))
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

	header := lipgloss.JoinVertical(lipgloss.Center,
		bannerStyle.Render(banner),
		"",
		subStyle.Render("a next-gen, agent-friendly terminal multiplexer"),
	)

	var body string
	switch {
	case m.input != inputNone:
		body = m.inputView()
	case m.mode == modeKeyMap:
		body = m.keyMapView()
	case m.mode == modePanelConfig:
		body = m.panelConfigView()
	default:
		body = m.dashboardView()
	}

	content := lipgloss.JoinVertical(lipgloss.Center, header, "", body)
	// Center the cockpit over the terminal's own (transparent) background; the
	// panels carry their own surface colour for depth.
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
	footer := zoomFooter(m.width, m.zoomTitle, keyLabel(m.effPrefix()), keyLabel(m.bindingKey(actDashboard)), m.zoomExited)
	return strings.Join(lines, "\n") + "\n" + footer
}

// dashboardView renders the status summary strip above the fleet. A small fleet
// shows as a card grid; once it grows past treeThreshold it switches to a
// space-efficient tree + preview split.
func (m model) dashboardView() string {
	heading := sectionStyle.Render(spaced("FLEET")) + mutedStyle.Render(fmt.Sprintf("   %d panel(s)", len(m.fleet)))
	summary := m.summaryStrip()
	body := m.cardGrid()
	if m.treeView() {
		body = m.treeAndPreview()
	}
	return lipgloss.JoinVertical(lipgloss.Center, heading, "", summary, "", body)
}

// treeView reports whether the fleet is large enough to swap the card grid for
// the tree + preview split.
func (m model) treeView() bool {
	return m.mode == modeDashboard && len(m.fleet) > treeThreshold
}

// summaryStrip is a row of chips counting panels in each state.
func (m model) summaryStrip() string {
	counts := map[panel.State]int{}
	for _, p := range m.fleet {
		counts[p.State]++
	}
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

// cardGrid lays the fleet out as a responsive grid of panel cards.
func (m model) cardGrid() string {
	if len(m.fleet) == 0 {
		return ""
	}
	cols := m.cols()
	rows := make([]string, 0, (len(m.fleet)+cols-1)/cols)
	for i := 0; i < len(m.fleet); i += cols {
		end := min(i+cols, len(m.fleet))
		cards := make([]string, 0, cols)
		for j := i; j < end; j++ {
			cards = append(cards, m.renderCard(m.fleet[j], j == m.cursor))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
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

	led := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render(info.led)
	title := lipgloss.NewStyle().Foreground(titleColor).Bold(true).Render(truncate(p.Title, cardInner-2))
	head := led + " " + title

	badge := kindBadge(p.Kind)
	state := lipgloss.NewStyle().Foreground(info.color).Render(info.label)
	kindLine := badge + "  " + state

	spark := lipgloss.NewStyle().Foreground(info.color).Render(sparkFor(p.State))
	footer := spark + "  " + mutedStyle.Render(truncate(p.Activity, cardInner-lipgloss.Width(spark)-2))

	style := lipgloss.NewStyle().
		Width(cardWidth-2).
		Padding(0, 1).
		MarginRight(cardGap).
		Background(colSurface).
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
// panel names on the left, and a preview window with the selected panel's
// metadata and a recent output tail on the right. The two panes share a height
// so they read as a unit and stay within the dashboard's vertical space.
func (m model) treeAndPreview() string {
	previewW := m.width - treeListWidth - 2 // 1-cell gutter, leave a little air
	previewW = min(64, max(34, previewW))

	visible := m.treeVisibleRows()
	start, end := scrollWindow(m.cursor, len(m.fleet), visible)

	tree := m.renderTree(start, end, visible)
	preview := m.renderPreview(previewW - 4) // inner text width

	h := max(lipgloss.Height(tree), lipgloss.Height(preview))
	pane := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Background(colSurface)

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

// treeVisibleRows is how many panel rows fit in the tree pane at the current
// height, after reserving the banner, headings, summary, footer, and borders. It
// never claims more rows than there are panels.
func (m model) treeVisibleRows() int {
	const reserved = 18 // banner + headings + summary + footer + pane chrome
	v := m.height - reserved
	if v < 3 {
		v = 3
	}
	if v > len(m.fleet) {
		v = len(m.fleet)
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

// renderTree is the left pane: the windowed slice [start,end) of the fleet, one
// line per panel (LED + name), with the selected row lit in the brand colour and
// a position counter in the header when the list scrolls.
func (m model) renderTree(start, end, visible int) string {
	header := sectionStyle.Render(spaced("PANELS"))
	if visible < len(m.fleet) {
		header += mutedStyle.Render(fmt.Sprintf("  %d/%d", m.cursor+1, len(m.fleet)))
	}

	rows := make([]string, 0, visible+2)
	rows = append(rows, header, "")
	for i := start; i < end; i++ {
		p := m.fleet[i]
		info := states[p.State]
		led := lipgloss.NewStyle().Foreground(info.color).Render(info.led)
		selected := i == m.cursor
		caret := "  "
		if selected {
			caret = "▸ "
		}
		// Mark the clipped edges so it is clear the list continues.
		if i == start && start > 0 {
			caret = mutedStyle.Render("↑ ")
		} else if i == end-1 && end < len(m.fleet) {
			caret = mutedStyle.Render("↓ ")
		}
		name := truncate(p.Title, treeListWidth-9)
		row := lipgloss.NewStyle().Width(treeListWidth - 4)
		if selected {
			row = row.Foreground(colDark).Background(colBrand).Bold(true)
		} else {
			row = row.Foreground(colInk)
		}
		rows = append(rows, row.Render(caret+led+" "+name))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// renderPreview is the right pane: the selected panel's title, kind/state, a
// small metadata block, and a faux tail of recent output.
func (m model) renderPreview(width int) string {
	if m.cursor < 0 || m.cursor >= len(m.fleet) {
		return mutedStyle.Render("no panel selected")
	}
	p := m.fleet[m.cursor]
	info := states[p.State]

	title := lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render(truncate(p.Title, width))
	led := lipgloss.NewStyle().Foreground(info.color).Render(info.led)
	statusLine := led + " " + kindBadge(p.Kind) + "  " + lipgloss.NewStyle().Foreground(info.color).Render(info.label)
	rule := mutedStyle.Render(strings.Repeat("─", width))

	meta := lipgloss.JoinVertical(lipgloss.Left,
		metaRow("state", info.label, info.color),
		metaRow("kind", p.Kind.String(), colInk),
		metaRow("activity", p.Activity, colInk),
		metaRow("signal", sparkFor(p.State), info.color),
	)

	out := []string{mutedStyle.Render(spaced("OUTPUT"))}
	for _, line := range previewLines(p) {
		out = append(out, mutedStyle.Render(truncate(line, width)))
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		title, statusLine, rule, meta, "", lipgloss.JoinVertical(lipgloss.Left, out...),
	)
}

// metaRow formats one aligned "label  value" line for the preview pane.
func metaRow(label, value string, valColor lipgloss.Color) string {
	l := mutedStyle.Render(fmt.Sprintf("%-9s", label))
	v := lipgloss.NewStyle().Foreground(valColor).Render(value)
	return l + " " + v
}

// previewLines fakes a short tail of recent output, shaped by the panel's kind
// and state, so the preview pane reads like a live window.
func previewLines(p panel.Panel) []string {
	switch {
	case p.State == panel.Attention:
		return []string{"▌ I need your input to continue:", "▌ > Apply the proposed changes? (y/n)"}
	case p.State == panel.Exited:
		return []string{"$ done — process exited cleanly", "  (press C-t w to dismiss)"}
	case p.Kind == panel.Shell && p.State == panel.Running:
		return []string{"$ go build ./...", "  compiling internal/tui …", "  ok  0.6s"}
	case p.Kind == panel.Shell:
		return []string{"$ tail -f app.log", "  (no new lines)"}
	case p.State == panel.Idle:
		return []string{"… waiting for the next instruction", "  idle"}
	default:
		return []string{"› thinking …", "› drafting the next edit", "› streaming tokens ▁▂▃▅▇"}
	}
}

// keyMapView renders the editable key-binding list plus a settings toggle, all
// reachable with one cursor. Select a row and press e to rebind it by typing the
// new key.
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

	// Rows 1..N: the bindings.
	for i, b := range binds {
		selected := i+1 == m.cursor
		// Show the live capture prompt on the row being rebound.
		keys := chord(prefLbl, b.key, selected)
		if m.editing && m.editIdx == i {
			keys = keycapHotStyle.Render(prefLbl) + " " + keycapHotStyle.Render("…type a key")
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

// footer renders the cockpit telemetry strip: a magenta brand cap, a blue mode
// segment, an amber attention counter (only when agents need you), the live
// fleet breakdown (agents vs shells) in the middle, and a clock plus a green
// connection segment on the right that flips to red on error. The key hints
// live in the C-t k key map, not here, so the strip stays a status readout.
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

	// Right caps: optional PREFIX badge · clock · connection status.
	statusBg := colGreen
	if strings.HasPrefix(m.status, "error") {
		statusBg = colRed
	}
	right := seg("⏱ "+m.now.Format("15:04:05"), colDark, colCyan) + seg("● "+m.status, colInk, statusBg)
	if m.prefix {
		right = seg("PREFIX", colDark, colBrandHi) + right
	}

	// Middle: the agents-vs-shells fleet breakdown, filling the leftover space.
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	return left + m.renderCounts(gap) + right
}

// renderCounts lays the per-kind panel tally onto a surface-coloured strip
// exactly width cells wide: how many agent panels and how many shell panels are
// in the fleet. It drops to blank rather than wrap when the strip is too narrow.
func (m model) renderCounts(width int) string {
	agents, shells := 0, 0
	for _, p := range m.fleet {
		if p.Kind == panel.Shell {
			shells++
		} else {
			agents++
		}
	}

	chip := func(n int, label string, c lipgloss.Color) string {
		dot := lipgloss.NewStyle().Background(colSurface).Foreground(c).Render("●")
		num := lipgloss.NewStyle().Background(colSurface).Foreground(c).Bold(true).Render(fmt.Sprintf(" %d ", n))
		lab := lipgloss.NewStyle().Background(colSurface).Foreground(colMuted).Render(label)
		return dot + num + lab
	}
	sep := lipgloss.NewStyle().Background(colSurface).Foreground(colFaint).Render("   ·   ")
	body := " " + chip(agents, "agents", colAgent) + sep + chip(shells, "shells", colShell)

	if lipgloss.Width(body) > width {
		body = "" // too tight — keep just the surface fill
	}
	return barStyle.Width(width).Render(body)
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
