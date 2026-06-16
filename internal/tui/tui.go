// Package tui is the reference frontend: a keyboard-driven cockpit that attaches
// to the server and (for now) renders a banner dashboard.
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
)

const banner = `██████╗  █████╗ ████████╗ ██████╗ ███╗   ██╗
██╔══██╗██╔══██╗╚══██╔══╝██╔═══██╗████╗  ██║
██████╔╝███████║   ██║   ██║   ██║██╔██╗ ██║
██╔══██╗██╔══██║   ██║   ██║   ██║██║╚██╗██║
██████╔╝██║  ██║   ██║   ╚██████╔╝██║ ╚████║
╚═════╝ ╚═╝  ╚═╝   ╚═╝    ╚═════╝ ╚═╝  ╚═══╝`

var (
	bannerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	subStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	boxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("205")).Padding(1, 4)
	barStyle    = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("250"))
	hintStyle   = barStyle.Foreground(lipgloss.Color("244"))
	statusStyle = barStyle.Foreground(lipgloss.Color("252"))
	prefixStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("205")).Padding(0, 1)
)

type mode int

const (
	modeDashboard mode = iota
)

type model struct {
	client *client.Client
	panels []proto.Panel

	mode    mode
	prefix  bool // armed by the prefix key, consumes the next key as a binding
	showMap bool // render the key map in the dashboard
	status  string

	width, height int
	quitting      bool
}

// New builds the TUI model around an attached client.
func New(c *client.Client) tea.Model {
	return model{client: c, mode: modeDashboard, status: "attaching…"}
}

// --- bubbletea event plumbing ---

type eventMsg proto.ServerMsg
type connClosedMsg struct{}

func waitEvent(ch chan proto.ServerMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return connClosedMsg{}
		}
		return eventMsg(msg)
	}
}

func (m model) Init() tea.Cmd {
	return waitEvent(m.client.Events)
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

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) applyEvent(sm proto.ServerMsg) {
	switch sm.Type {
	case "welcome":
		m.status = "attached · " + sm.Version
	case "panels":
		m.panels = sm.Panels
	case "error":
		m.status = "error: " + sm.Error
	}
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()

	// A key following the prefix is interpreted as a binding.
	if m.prefix {
		m.prefix = false
		switch key {
		case keyNewPanel:
			if err := m.client.Send(proto.Command{Action: "panel.create", Kind: proto.KindShell}); err != nil {
				m.status = "send failed: " + err.Error()
			} else {
				m.status = "requested new shell panel"
			}
		case keyDashboard:
			m.mode = modeDashboard
			m.showMap = false
			m.status = "dashboard"
		case keyShowMap:
			m.showMap = !m.showMap
			m.status = "key map"
		case keyDetach:
			m.quitting = true
			return m, tea.Quit
		case keyPrefix:
			m.status = "dashboard" // C-t C-t is a no-op for now
		default:
			m.status = "no binding for " + key
		}
		return m, nil
	}

	switch key {
	case keyPrefix:
		m.prefix = true
		return m, nil
	case keyCtrlC:
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

// --- rendering ---

func (m model) View() string {
	if m.quitting {
		return "baton: detached (server still running)\n"
	}
	if m.width == 0 || m.height == 0 {
		return "" // wait for the first size message
	}

	title := bannerStyle.Render(banner)
	sub := subStyle.Render("a next-gen, agent-friendly terminal multiplexer")

	content := m.panelList()
	if m.showMap {
		content = keyMapView()
	}

	box := boxStyle.Render(lipgloss.JoinVertical(lipgloss.Center, title, "", sub, "", content))
	body := lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center, box)

	return body + "\n" + m.footer()
}

// panelList renders the panel count and one line per panel.
func (m model) panelList() string {
	lines := make([]string, 0, len(m.panels)+1)
	lines = append(lines, fmt.Sprintf("%d panel(s)", len(m.panels)))
	for _, p := range m.panels {
		lines = append(lines, subStyle.Render("• "+p.Title))
	}
	return lipgloss.JoinVertical(lipgloss.Center, lines...)
}

// keyMapView renders the full key-binding map.
func keyMapView() string {
	rows := make([]string, 0, len(bindings)+1)
	rows = append(rows, "key bindings")
	for _, b := range bindings {
		rows = append(rows, subStyle.Render(fmt.Sprintf("%-7s  %s", b.keys, b.desc)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// footer renders the status bar: a full-width background with the key-binding
// hint on the left and the current status on the right. The hint is clamped to
// the available width so the bar never wraps.
func (m model) footer() string {
	right := statusStyle.Render(m.status + " ")
	if m.prefix {
		right = prefixStyle.Render("PREFIX") + statusStyle.Render(" "+m.status+" ")
	}

	avail := m.width - lipgloss.Width(right)
	if avail < 0 {
		avail = 0
	}
	left := hintStyle.MaxWidth(avail).Render(" " + bindingHints() + " ")

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	return left + barStyle.Render(strings.Repeat(" ", gap)) + right
}

// bindingHints is the compact hint shown in the status bar; the full list lives
// in the C-t k key map.
func bindingHints() string {
	return prefixLabel + " k  keys   ·   " + prefixLabel + " q  detach"
}
