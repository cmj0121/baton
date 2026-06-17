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

	binds   []binding // editable copy of the bindings (nil ⇒ the defaults)
	editing bool      // capturing the next key press as a rebind
	editIdx int       // which binding is being rebound

	width, height int
	quitting      bool
	restart       bool // user asked to force-restart the daemon on exit
}

// RestartRequested reports whether the cockpit exited because the user asked to
// force-restart the server. The client runner relaunches the daemon and
// re-attaches when this is set.
func (m model) RestartRequested() bool { return m.restart }

// New builds the TUI model around an attached client. The fleet starts empty and
// is filled by the server's first snapshot, which arrives right after the hello
// handshake — the server owns the panels now.
func New(c *client.Client) tea.Model {
	return model{
		client:       c,
		mode:         modeDashboard,
		status:       "attaching…",
		endpoint:     c.Endpoint(),
		confirmClose: true,
		now:          time.Now(),
		binds:        append([]binding(nil), bindings...),
	}
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

// tick drives the footer clock, firing once a second.
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitEvent(m.client.Events), tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case eventMsg:
		m.applyEvent(proto.ServerMsg(msg))
		return m, waitEvent(m.client.Events)

	case connClosedMsg:
		m.status = "server closed the connection"
		m.quitting = true
		return m, tea.Quit

	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
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

	// Capturing a rebind: the next key press (other than esc) becomes the new
	// chord for the binding under edit.
	if m.editing {
		m.editing = false
		if key == "esc" {
			m.status = "rebind cancelled"
			return m, nil
		}
		m.ensureBinds()
		old := m.binds[m.editIdx].key
		m.binds[m.editIdx].key = key
		m.status = fmt.Sprintf("rebound %q: %s → %s", m.binds[m.editIdx].desc, old, key)
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

	// A key following the prefix is interpreted as a binding.
	if m.prefix {
		m.prefix = false
		if b, ok := m.lookup(key); ok {
			return m.runAction(b.act)
		}
		if key == keyPrefix {
			m.status = "dashboard" // C-t C-t is a no-op for now
			return m, nil
		}
		m.status = "no binding for " + key
		return m, nil
	}

	switch key {
	case keyPrefix:
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
		// Edit the selected binding's chord (key map only).
		if m.mode == modeKeyMap && m.cursor < len(m.keymap()) {
			m.editing = true
			m.editIdx = m.cursor
			m.status = "press the new key for " + fmt.Sprintf("%q", m.keymap()[m.cursor].desc) + "  ·  esc cancels"
		}
		return m, nil

	case "enter":
		return m.activate()
	case "esc":
		if m.mode == modeKeyMap {
			return m.runAction(actDashboard)
		}
	}
	return m, nil
}

// runAction performs a binding's verb. Both the prefix handler and the key map's
// enter key funnel through here.
func (m model) runAction(a action) (tea.Model, tea.Cmd) {
	switch a {
	case actNewPanel:
		if err := m.client.Send(proto.Command{Action: "panel.create", Kind: proto.KindShell}); err != nil {
			m.status = "send failed: " + err.Error()
		} else {
			m.status = "requested new shell panel"
		}
	case actClose:
		if len(m.fleet) == 0 {
			m.status = "no panel to close"
		} else if m.confirmClose {
			m.pendingClose = true
			m.status = "close " + m.fleet[m.cursor].Title + "? (y/n)"
		} else {
			m.closeSelected()
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
		if m.cursor >= 0 && m.cursor < len(m.keymap()) {
			return m.runAction(m.keymap()[m.cursor].act)
		}
		// The row past the bindings is the confirm-on-close setting.
		m.confirmClose = !m.confirmClose
		m.status = "confirm on close: " + onOff(m.confirmClose)
		return m, nil
	}
	if m.cursor >= 0 && m.cursor < len(m.fleet) {
		m.status = "focus · " + m.fleet[m.cursor].Title + "  (zoom not wired yet)"
	}
	return m, nil
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
	if m.mode == modeKeyMap {
		return len(m.keymap()) + 1 // bindings + the settings toggle
	}
	return len(m.fleet)
}

// attentionCount is how many panels are flagged as needing the user.
func (m model) attentionCount() int {
	n := 0
	for _, p := range m.fleet {
		if p.State == panel.Attention {
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
// map and the tree view are single-column lists.
func (m model) cols() int {
	if m.mode == modeKeyMap || m.treeView() {
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

	header := lipgloss.JoinVertical(lipgloss.Center,
		bannerStyle.Render(banner),
		"",
		subStyle.Render("a next-gen, agent-friendly terminal multiplexer"),
	)

	var body string
	if m.mode == modeKeyMap {
		body = m.keyMapView()
	} else {
		body = m.dashboardView()
	}

	content := lipgloss.JoinVertical(lipgloss.Center, header, "", body)
	// Center the cockpit over the terminal's own (transparent) background; the
	// panels carry their own surface colour for depth.
	placed := lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center, content)
	return placed + "\n" + m.footer()
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

	binds := m.keymap()
	rows := make([]string, 0, len(binds)+8)
	rows = append(rows, sectionStyle.Render(spaced("KEY BINDINGS")), "")
	for i, b := range binds {
		selected := i == m.cursor
		descStyle := mutedStyle
		if selected {
			descStyle = inkStyle
		}
		// Show the live capture prompt on the row being rebound.
		keys := chord(b.key, selected)
		if m.editing && i == m.editIdx {
			keys = keycapHotStyle.Render(prefixLabel) + " " + keycapHotStyle.Render("…type a key")
		}
		rows = append(rows, fmt.Sprintf("%s%s   %s", caret(selected), keys, descStyle.Render(b.desc)))
	}

	// Settings: the confirm-on-close toggle, selectable as the last row.
	selected := m.cursor == len(binds)
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

	body := lipgloss.JoinVertical(lipgloss.Left, rows...)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBrand).
		Background(colSurface).
		Padding(1, 3).
		Render(body)
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
	if m.mode == modeKeyMap {
		mode = "KEY MAP"
	}
	left := seg("◈ BATON", colDark, colBrand) + seg(mode, colInk, colBlue)
	if n := m.attentionCount(); n > 0 {
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
