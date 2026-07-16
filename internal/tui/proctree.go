package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proctree"
	"github.com/cmj0121/baton/internal/proto"
)

// The process-tree overlay (modeProcTree, C-t o): a scrollable snapshot of what the
// daemon is actually running — the daemon at the root, the fleet's nested work-item
// groups, each panel under its group with its process-group-leader pid, and every
// panel's live OS descendant processes. It joins the cockpit's fleet snapshot to
// the host's process table (via internal/proctree, shared with `baton ctl tree`).
// The OS table is sampled at open and on `r`, not every frame, so it never taxes
// the render loop. esc/q close; j/k and the page keys scroll.

// openProcTree enters modeProcTree, sampling the OS process table and rendering the
// tree from the current fleet snapshot. It remembers the view to return to.
func (m model) openProcTree(from mode) model {
	m.procFrom = from
	m.mode = modeProcTree
	m.procScroll = 0
	m.procLines = m.renderProcTree()
	m.status = "process tree"
	return m
}

// closeProcTree leaves the overlay, restoring the view it was opened from and
// dropping the sampled tree so it cannot go stale in the background.
func (m model) closeProcTree() (tea.Model, tea.Cmd) {
	m.mode = m.procFrom
	m.procLines = nil
	m.procScroll = 0
	if m.mode == modeDashboard {
		m.status = "dashboard"
	}
	return m, nil
}

// renderProcTree samples the OS process table and renders the tree from the fleet
// snapshot into display lines. Each panel carries its pid on the wire, so the
// domain fleet is re-encoded to feed the shared builder. A process-table read error
// still yields a tree (the fleet with no OS descendants) rather than a blank
// overlay.
func (m model) renderProcTree() []string {
	panels := make([]proto.Panel, len(m.fleet))
	for i, p := range m.fleet {
		panels[i] = p.ToProto()
	}
	children, comm, err := proctree.OSProcessTable()
	if err != nil {
		children, comm = map[int][]int{}, map[int]string{}
	}
	root := proctree.Build(proctree.DaemonPid(), panels, children, comm)
	// The tree carries panel titles and OS process names — neither is fully under
	// baton's control — so strip any embedded terminal escapes before they reach the
	// real terminal, the way the git-output popup guards untrusted text.
	return sanitizeLines(strings.Split(strings.TrimRight(proctree.Render(root), "\n"), "\n"))
}

// handleProcTreeKey drives the overlay: j/k and the arrows scroll a line, the page
// keys a screen, home/end (g/G) jump to the ends, r re-samples the OS table; esc/q
// close.
func (m model) handleProcTreeKey(key string) (tea.Model, tea.Cmd) {
	page := max(1, m.procViewportRows()-1)
	switch key {
	case "esc", "q":
		return m.closeProcTree()
	case "r":
		m.procLines = m.renderProcTree()
		m.procScrollBy(0) // re-clamp in case the tree shrank
		m.status = "process tree · refreshed"
	case "up", "k":
		m.procScrollBy(-1)
	case "down", "j":
		m.procScrollBy(1)
	case "pgup", "ctrl+u", "b":
		m.procScrollBy(-page)
	case "pgdown", "ctrl+d", "ctrl+f", " ":
		m.procScrollBy(page)
	case "home", "g":
		m.procScroll = 0
	case "end", "G":
		m.procScrollBy(1 << 30)
	}
	return m, nil
}

// procScrollBy moves the offset by delta, clamped so the last line can rest at the
// bottom and the offset never runs negative.
func (m *model) procScrollBy(delta int) {
	maxOff := max(0, len(m.procLines)-m.procViewportRows())
	m.procScroll = clampInt(m.procScroll+delta, 0, maxOff)
}

// procViewportRows is the overlay body height, mirroring the git-output popup so the
// key handler and the view agree on the window.
func (m model) procViewportRows() int {
	return clampInt(m.height-14, 5, 40)
}

// procWidth is the overlay's inner text width, bounded and never wider than the
// screen.
func (m model) procWidth() int {
	return min(clampInt(m.width-16, 24, 140), m.width-8)
}

// procTreeView renders the overlay: a header with a scroll indicator, the windowed
// tree, and a key legend, in the cockpit's box.
func (m model) procTreeView() string {
	if len(m.procLines) == 0 {
		return configBox(mutedStyle.Render("no processes"))
	}
	width, rows := m.procWidth(), m.procViewportRows()
	off := clampInt(m.procScroll, 0, max(0, len(m.procLines)-rows))
	end := min(off+rows, len(m.procLines))

	body := make([]string, 0, rows)
	for _, l := range m.procLines[off:end] {
		body = append(body, lipgloss.NewStyle().Foreground(colInk).Width(width).Render(clipVisible(l, width)))
	}
	body = padBlock(body, rows, width)

	header := sectionStyle.Render(spaced("PROCESS TREE"))
	if len(m.procLines) > rows { // a scroll indicator only when there is more than one screen
		header += mutedStyle.Render(fmt.Sprintf("   %d–%d / %d", off+1, end, len(m.procLines)))
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		header, "", lipgloss.JoinVertical(lipgloss.Left, body...), "", m.procTreeLegend())
	return configBox(content)
}

// procTreeLegend is the overlay's key hint.
func (m model) procTreeLegend() string {
	return legend("j/k", "scroll", "g/G", "top · end", "r", "refresh", "esc", "close")
}
