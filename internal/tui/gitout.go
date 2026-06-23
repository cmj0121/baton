package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The git-output popup (modeGitOut): a scrollable text overlay showing the captured
// output of a non-interactive git op (log/status/add/push/branch/worktree-list) —
// the text sibling of the diff popup. The server ran the op one-shot and reaped it,
// so the popup owns nothing: esc just closes it and restores the prior view. j/k
// and the page keys scroll; long lines are clipped to the popup width.

// openGitOutPopup enters modeGitOut with a captured op's output, remembering the
// view to return to. Empty output (e.g. a silent `git add -A`) still opens — a
// placeholder line stands in so the popup is never blank.
func (m model) openGitOutPopup(title, text string, failed bool) model {
	m.gitOutFrom = m.mode
	m.mode = modeGitOut
	m.gitOutTitle = title
	m.gitOutFailed = failed
	m.gitOutScroll = 0
	if strings.TrimSpace(text) == "" {
		m.gitOutLines = []string{"(no output)"}
	} else {
		m.gitOutLines = strings.Split(strings.TrimRight(text, "\n"), "\n")
	}
	m.status = title
	return m
}

// closeGitOutPopup leaves the popup, restoring the view it was opened from and
// dropping the captured text so it cannot linger.
func (m model) closeGitOutPopup() (tea.Model, tea.Cmd) {
	m.mode = m.gitOutFrom
	m.gitOutLines = nil
	m.gitOutTitle = ""
	m.gitOutFailed = false
	m.gitOutScroll = 0
	if m.mode == modeDashboard {
		m.status = "dashboard"
	}
	return m, nil
}

// handleGitOutKey drives the popup: j/k and the arrows scroll a line, the page keys
// a screen, home/end (g/G) jump to the ends; esc/q close.
func (m model) handleGitOutKey(key string) (tea.Model, tea.Cmd) {
	page := max(1, m.gitOutViewportRows()-1)
	switch key {
	case "esc", "q":
		return m.closeGitOutPopup()
	case "up", "k":
		m.gitOutScrollBy(-1)
	case "down", "j":
		m.gitOutScrollBy(1)
	case "pgup", "ctrl+u", "b":
		m.gitOutScrollBy(-page)
	case "pgdown", "ctrl+d", "ctrl+f", " ":
		m.gitOutScrollBy(page)
	case "home", "g":
		m.gitOutScroll = 0
	case "end", "G":
		m.gitOutScrollBy(1 << 30)
	}
	return m, nil
}

// gitOutScrollBy moves the offset by delta, clamped so the last line can rest at the
// bottom and the offset never runs negative.
func (m *model) gitOutScrollBy(delta int) {
	maxOff := max(0, len(m.gitOutLines)-m.gitOutViewportRows())
	m.gitOutScroll = clampInt(m.gitOutScroll+delta, 0, maxOff)
}

// gitOutViewportRows is the popup body height — the rows of output it shows. It also
// bounds the scroll, so the key handler and the view agree. It mirrors the diff
// popup's body height.
func (m model) gitOutViewportRows() int {
	return clampInt(m.height-14, 5, 40)
}

// gitOutWidth is the popup's inner text width, bounded and never wider than the
// screen, matching the diff popup's detail column sizing.
func (m model) gitOutWidth() int {
	return min(clampInt(m.width-16, 24, 140), m.width-8)
}

// gitOutView renders the popup: a header (the op, tinted red on failure) with a
// scroll indicator, the windowed output, and a key legend, in the cockpit's box.
func (m model) gitOutView() string {
	if len(m.gitOutLines) == 0 {
		return configBox(mutedStyle.Render("no output"))
	}
	width, rows := m.gitOutWidth(), m.gitOutViewportRows()
	off := clampInt(m.gitOutScroll, 0, max(0, len(m.gitOutLines)-rows))
	end := min(off+rows, len(m.gitOutLines))

	body := make([]string, 0, rows)
	for _, l := range m.gitOutLines[off:end] {
		body = append(body, lipgloss.NewStyle().Foreground(colInk).Width(width).Render(clipVisible(l, width)))
	}
	body = padBlock(body, rows, width)

	titleFg := colBrandHi
	if m.gitOutFailed {
		titleFg = colRed
	}
	header := sectionStyle.Render(spaced("GIT")) + "  " +
		lipgloss.NewStyle().Foreground(titleFg).Render(strings.TrimPrefix(m.gitOutTitle, "git "))
	if len(m.gitOutLines) > rows { // a scroll indicator only when there is more than one screen
		header += mutedStyle.Render(fmt.Sprintf("   %d–%d / %d", off+1, end, len(m.gitOutLines)))
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		header, "", lipgloss.JoinVertical(lipgloss.Left, body...), "", m.gitOutLegend())
	return configBox(content)
}

// gitOutLegend is the popup's key hint.
func (m model) gitOutLegend() string {
	k := func(s string) string { return lipgloss.NewStyle().Foreground(colCyan).Bold(true).Render(s) }
	return k("j/k") + mutedStyle.Render(" scroll  ") +
		k("g/G") + mutedStyle.Render(" top·end  ") +
		k("esc") + mutedStyle.Render(" close")
}
