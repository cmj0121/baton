package tui

// The cockpit half of panel output logging (docs/LOGGING.md).
//
// Two keys, both prefix-reached in every view: one starts and stops writing the
// selected panel's output to a file, the other opens that file in a temporary
// panel that follows it. Everything else here exists to make the first one
// VISIBLE — a badge on the card and a cap in the footer — because a feature that
// silently writes your terminal to disk has to say so while it does it.
//
// The file lives on the machine the FLEET runs on, not this one. So the cockpit
// never opens it directly: it asks the daemon for a panel that reads it, which is
// what makes the pair work unchanged over --remote.

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// logTarget is the panel the logging keys act on in the current view: the zoomed
// panel, the focused member of a group split, or the dashboard selection. A group
// row is not a target — logging is per panel, and quietly picking one member of a
// selected work item would be a guess.
func (m model) logTarget() (panel.Panel, bool) {
	switch m.mode {
	case modeZoom:
		return m.fleetPanel(m.zoomID)
	case modeGroupZoom:
		return m.focusedMember()
	default:
		it, ok := m.selectedItem()
		if !ok || it.kind != itemPanel {
			return panel.Panel{}, false
		}
		return it.panel, true
	}
}

// toggleLog asks the daemon to start or stop logging the target panel. The daemon
// owns the decision (it holds the file) and answers with a notice naming the path,
// or an error when no log directory is configured — so the cockpit sends and says
// nothing it does not yet know.
func (m model) toggleLog() (tea.Model, tea.Cmd) {
	p, ok := m.logTarget()
	if !ok {
		m.status = "logging: select a panel"
		return m, nil
	}
	m.sendf(proto.Command{Action: "panel.log", ID: p.ID})
	return m, nil
}

// viewLog asks the daemon to open the target panel's log in a temporary panel
// that follows the file. The reply is an "ephemeral" id, which the cockpit
// auto-zooms and reaps on the way out — the same path the git menu's commit takes.
func (m model) viewLog() (tea.Model, tea.Cmd) {
	p, ok := m.logTarget()
	if !ok {
		m.status = "log: select a panel"
		return m, nil
	}
	if !p.Logging {
		m.status = "log: this panel is not being logged — " + m.logChord() + " starts it"
		return m, nil
	}
	m.pendingEphemeralTitle = "log · " + p.Title
	m.sendf(proto.Command{Action: "panel.logview", ID: p.ID})
	return m, nil
}

// logChord renders the logging toggle as the chord a user actually presses, so
// the "not being logged" message names the key rather than describing it.
func (m model) logChord() string {
	return keyLabel(m.effPrefix()) + " " + seqLabel(m.bindingKey(actLogToggle))
}

// logBadge is the card marker for a panel whose output is being written to disk:
// a filled dot in the brand highlight, in front of the state LED. Empty for a
// panel that is not being logged, so an ordinary card is unchanged.
func (m model) logBadge(p panel.Panel) string {
	if !p.Logging {
		return ""
	}
	return lipgloss.NewStyle().Foreground(colBrandHi).Render("◉") + " "
}

// logCap is the footer segment shown while the focused panel is being logged. It
// names the file rather than merely saying "LOG", because the one question a
// badge cannot answer is where the bytes are going — and over --remote the answer
// is a path on a different machine.
func (m model) logCap() string {
	p, ok := m.logTarget()
	if !ok || !p.Logging {
		return ""
	}
	label := "◉ LOG"
	if p.LogFile != "" {
		label += " " + shortPath(p.LogFile, 28)
	}
	return seg(label, colDark, colBrandHi)
}
