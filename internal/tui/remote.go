package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
)

// The remote overlay (modeRemote, C-t @): whether this fleet accepts a cockpit
// from another machine, the passkey that admits one, and every connection that
// is attached right now — including this one, marked.
//
// It is prefix-gated on purpose. The key behind it exposes the machine, and a
// key that exposes the machine does not belong one fingertip from the arrow
// keys; the same argument the logging pair is bound this way for. The key is
// "@" because the list is made of user@host — and because a letter would have
// shadowed a command that was already using it.
//
// The asymmetry in the legend is the feature's one real rule: `k` kicks from
// either side of the pipe, while `n` and `x` — the passkey and the switch — are
// refused over a remote attach and are the fleet owner's to press on the fleet's
// own machine. The server enforces it; the overlay only stops offering it.

// openRemote enters modeRemote and asks the server for the current status. The
// list arrives as a "remote" message and every later change is pushed, so the
// overlay is never showing a snapshot it has to refresh by hand.
func (m model) openRemote(from mode) (tea.Model, tea.Cmd) {
	m.remoteFrom = from
	m.mode = modeRemote
	m.remoteSel = 0
	m.sendf(proto.Command{Action: "remote.status"})
	m.status = "remote access"
	return m, nil
}

// closeRemote leaves the overlay, restoring the view it was opened from. The
// status is kept: it costs nothing, and a re-open should not blink.
func (m model) closeRemote() (tea.Model, tea.Cmd) {
	m.mode = m.remoteFrom
	if m.mode == modeDashboard {
		m.status = "dashboard"
	}
	return m, nil
}

// handleRemoteKey drives the overlay. Movement is on the arrows alone: `k` is
// the kick, which the issue this was built from names, and a key cannot be both.
func (m model) handleRemoteKey(key string) (tea.Model, tea.Cmd) {
	info := m.remoteInfo
	switch key {
	case "esc", "q":
		return m.closeRemote()
	case "up":
		m.remoteSel = max(0, m.remoteSel-1)
	case "down":
		m.remoteSel = min(m.remoteConnCount()-1, m.remoteSel+1)
	case "r":
		m.sendf(proto.Command{Action: "remote.status"})
		m.status = "remote access · refreshed"
	case "e":
		// The local-only rule is checked before "already enabled": both are true
		// for a remote cockpit looking at a live fleet, and the one worth saying is
		// the one that explains why the key will never work from here.
		if info != nil && !info.Local {
			m.status = "the switch is thrown on the fleet's own machine, not over a remote attach"
			return m, nil
		}
		if info != nil && info.Enabled {
			m.status = "remote access is already enabled"
			return m, nil
		}
		m.sendf(proto.Command{Action: "remote.enable"})
		m.status = "enabling remote access…"
	case "n":
		if !m.remoteMayControl() {
			return m, nil
		}
		m.sendf(proto.Command{Action: "remote.rotate"})
		m.status = "rotating the passkey · live connections stay"
	case "x":
		if !m.remoteMayControl() {
			return m, nil
		}
		m.sendf(proto.Command{Action: "remote.disable"})
		m.status = "disabling remote access…"
	case "k":
		conn, ok := m.remoteSelected()
		if !ok {
			return m, nil
		}
		m.sendf(proto.Command{Action: "remote.kick", Conn: conn.ID})
		m.status = "kicking " + sanitizeText(conn.Source)
	}
	return m, nil
}

// remoteMayControl reports whether the passkey and the switch are this
// connection's to touch, setting the status line when they are not. Enabling,
// rotating and disabling are local-only; kicking and listing are not.
func (m *model) remoteMayControl() bool {
	switch {
	case m.remoteInfo == nil:
		return false
	case !m.remoteInfo.Local:
		m.status = "the passkey is changed on the fleet's own machine, not over a remote attach"
		return false
	case !m.remoteInfo.Enabled:
		m.status = "remote access is not enabled"
		return false
	}
	return true
}

// remoteConnCount is how many rows the list has.
func (m model) remoteConnCount() int {
	if m.remoteInfo == nil {
		return 0
	}
	return len(m.remoteInfo.Conns)
}

// remoteSelected is the row under the cursor.
func (m model) remoteSelected() (proto.RemoteConn, bool) {
	if m.remoteInfo == nil || m.remoteSel < 0 || m.remoteSel >= len(m.remoteInfo.Conns) {
		return proto.RemoteConn{}, false
	}
	return m.remoteInfo.Conns[m.remoteSel], true
}

// applyRemote takes a pushed status and keeps the cursor on a row that exists.
func (m *model) applyRemote(info *proto.RemoteInfo) {
	m.remoteInfo = info
	if info == nil {
		m.remoteSel = 0
		return
	}
	m.remoteSel = clampInt(m.remoteSel, 0, max(0, len(info.Conns)-1))
}

// remoteView renders the overlay: the status head, the connection table, and the
// legend for what this side of the pipe may actually do.
func (m model) remoteView() string {
	width := m.procWidth()
	rows := []string{sectionStyle.Render(spaced("REMOTE")) + "   " + m.remoteHead()}

	if m.remoteConnCount() == 0 {
		rows = append(rows, "", mutedStyle.Render("no connections"))
	} else {
		rows = append(rows, "", mutedStyle.Render(remoteHeaderRow()))
		for i, c := range m.remoteInfo.Conns {
			rows = append(rows, m.remoteRow(c, i == m.remoteSel, width))
		}
	}
	rows = append(rows, "", m.remoteLegend())
	return m.popupBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// remoteHead is the one-line status: off, or on with the passkey a local cockpit
// is allowed to read.
func (m model) remoteHead() string {
	info := m.remoteInfo
	switch {
	case info == nil:
		return mutedStyle.Render("asking the fleet…")
	case !info.Enabled:
		return mutedStyle.Render("disabled")
	case info.Passkey != "":
		return lipgloss.NewStyle().Foreground(colBrandHi).Render("enabled") +
			mutedStyle.Render(" · passkey ") +
			lipgloss.NewStyle().Bold(true).Foreground(colInk).Render(info.Passkey)
	default:
		// A remote cockpit is told the switch is on and nothing more. The code that
		// admits the NEXT cockpit is read on the machine the fleet runs on.
		return lipgloss.NewStyle().Foreground(colBrandHi).Render("enabled") +
			mutedStyle.Render(" · passkey shown on the fleet's own machine")
	}
}

// remoteHeaderRow is the column header, spaced to match remoteRow.
func remoteHeaderRow() string {
	return fmt.Sprintf("  %-22s %-9s %s", "SOURCE", "ROLE", "ATTACHED")
}

// remoteRow renders one connection. The source is a label the far end chose for
// itself, so it is sanitised before it is styled — the same rule panel titles and
// git output go through.
func (m model) remoteRow(c proto.RemoteConn, selected bool, width int) string {
	mark := "  "
	if selected {
		mark = lipgloss.NewStyle().Foreground(colBrandHi).Render("▸ ")
	}
	source := sanitizeText(c.Source)
	if c.Self {
		source += " ←"
	}
	line := fmt.Sprintf("%-22s %-9s %s", takeCols(source, 22), sanitizeText(c.Role), remoteSince(c.Since, m.now))

	style := lipgloss.NewStyle().Foreground(colInk)
	if !selected {
		style = lipgloss.NewStyle().Foreground(colMuted)
	}
	return clipVisible(mark+style.Render(line), width)
}

// remoteSince renders how long a connection has been attached, as the overlay's
// "2h 14m". An unparsable instant reads as "—" rather than as a wrong number.
func remoteSince(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "—"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch h := int(d.Hours()); {
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, int(d.Minutes())%60)
	case int(d.Minutes()) > 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// remoteLegend offers only what this connection may actually do: a fleet with
// remote off has a switch and nothing else, and a remote cockpit is not offered
// the passkey keys the server would refuse anyway.
func (m model) remoteLegend() string {
	info := m.remoteInfo
	switch {
	case info == nil:
		return legend("esc", "close")
	case !info.Enabled && info.Local:
		return legend("e", "enable remote", "↑↓", "select", "k", "kick", "esc", "close")
	case !info.Local:
		return legend("↑↓", "select", "k", "kick", "r", "refresh", "esc", "close")
	case !info.Enabled:
		return legend("↑↓", "select", "k", "kick", "esc", "close")
	default:
		return legend("↑↓", "select", "k", "kick", "n", "new passkey", "x", "disable", "esc", "close")
	}
}

// RemoteSourceLabel is how THIS cockpit names itself to a fleet it attaches to
// over ssh: the local login and hostname, e.g. "cmj@laptop.lan". It is a label
// to recognise a connection by in the overlay's list, never an identity the
// server is asked to trust — it is self-declared, exactly like the role.
func RemoteSourceLabel(user, host string) string {
	user, host = strings.TrimSpace(user), strings.TrimSpace(host)
	switch {
	case user == "" && host == "":
		return "remote"
	case user == "":
		return host
	case host == "":
		return user
	}
	return user + "@" + host
}
