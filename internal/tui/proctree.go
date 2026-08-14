package tui

import (
	"fmt"
	"math"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
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
// overlay. Unlike the plaintext `baton ctl tree`, the overlay draws each node's CPU
// as a load-coloured bar before the numbers, so it composes the line from the
// shared walk itself rather than taking proctree.Render's flat string.
func (m model) renderProcTree() []string {
	panels := make([]proto.Panel, len(m.fleet))
	for i, p := range m.fleet {
		panels[i] = p.ToProto()
	}
	children, comm, stats, err := proctree.OSProcessTable()
	if err != nil {
		children, comm, stats = map[int][]int{}, map[int]string{}, map[int]proctree.Stat{}
	}
	root := proctree.Build(proctree.DaemonPid(), panels, children, comm, stats)

	ink := lipgloss.NewStyle().Foreground(colInk)
	rows := proctree.Rows(root)
	lines := make([]string, len(rows))
	for i, r := range rows {
		n := r.Node
		// Panel titles and OS process names are not fully under baton's control, so
		// strip any embedded terminal escapes from that text before it is styled and
		// reaches the real terminal — the way the git-output popup guards untrusted
		// text. The state LED, CPU bar, and numbers are added after, from our values.
		var label string
		if n.Kind == proctree.KindPanel && n.Panel != nil {
			label = procPanelLabel(n, ink)
		} else {
			label = ink.Render(sanitizeText(proctree.LabelText(n)))
		}
		line := ink.Render(r.Prefix) + label
		if n.RSS > 0 {
			line += "  " + cpuBar(n.CPU) + ink.Render(proctree.ResourceText(n))
		}
		lines[i] = line
	}
	return lines
}

// procPanelLabel renders a panel node for the overlay: its lifecycle as a single
// coloured LED — no "/running" word, the colour is what splits running (green) from
// idle (amber) — then the panel title and its pid/comm. Title and comm are
// untrusted, so they are sanitised before styling.
func procPanelLabel(n *proctree.Node, ink lipgloss.Style) string {
	info := states[panel.ParseState(n.Panel.State)]
	s := lipgloss.NewStyle().Foreground(info.color).Render(info.led) + " " + ink.Render(sanitizeText(n.Panel.Name))
	if n.Pid > 0 {
		s += ink.Render(fmt.Sprintf(" pid=%d", n.Pid))
	}
	if n.Comm != "" {
		s += ink.Render("  " + sanitizeText(n.Comm))
	}
	return s
}

// Load-band colours for the CPU bar: green under half, amber past half, red near
// saturation — the LED palette's running / idle / attention hues.
var (
	colLoadLo  = lipgloss.Color("42")  // green
	colLoadMid = lipgloss.Color("220") // amber
	colLoadHi  = lipgloss.Color("203") // red
)

// cpuBar renders pct (0–100, clamped) as a fixed 8-cell meter with eighth-of-a-cell
// resolution: the filled run coloured by load band, the empty track faint. A
// multi-core process that reads above 100% simply saturates the bar.
func cpuBar(pct float64) string {
	const cells = 8
	// CPUPercent can return NaN for a process sampled at its very creation (0/0);
	// NaN slips past the < and > clamps, and int(NaN) is min-int on amd64, which
	// would blow strings.Repeat's count into an allocation panic. Fold it to empty.
	if math.IsNaN(pct) || pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	eighths := int(pct/100*float64(cells*8) + 0.5)
	full := eighths / 8
	var b strings.Builder
	for i := 0; i < full; i++ {
		b.WriteRune('█')
	}
	if rem := eighths % 8; rem > 0 && full < cells {
		b.WriteRune([]rune("▏▎▍▌▋▊▉")[rem-1])
		full++
	}
	return lipgloss.NewStyle().Foreground(loadColor(pct)).Render(b.String()) +
		lipgloss.NewStyle().Foreground(colFaint).Render(strings.Repeat("░", cells-full))
}

// loadColor picks the CPU bar's fill hue from the load band: green under half,
// amber from half, red near saturation.
func loadColor(pct float64) lipgloss.Color {
	switch {
	case pct >= 80:
		return colLoadHi
	case pct >= 50:
		return colLoadMid
	default:
		return colLoadLo
	}
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
		return m.popupBox(mutedStyle.Render("no processes"))
	}
	width, rows := m.procWidth(), m.procViewportRows()
	off := clampInt(m.procScroll, 0, max(0, len(m.procLines)-rows))
	end := min(off+rows, len(m.procLines))

	body := make([]string, 0, rows)
	for _, l := range m.procLines[off:end] {
		// Each line is already styled (colInk text, a load-coloured CPU bar), so only
		// clip and pad it to width here — a second Foreground would cut the bar's
		// colour at its reset.
		body = append(body, lipgloss.NewStyle().Width(width).Render(clipVisible(l, width)))
	}
	body = padBlock(body, rows, width)

	header := sectionStyle.Render(spaced("PROCESS TREE"))
	if len(m.procLines) > rows { // a scroll indicator only when there is more than one screen
		header += mutedStyle.Render(fmt.Sprintf("   %d–%d / %d", off+1, end, len(m.procLines)))
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		header, "", lipgloss.JoinVertical(lipgloss.Left, body...), "", m.procTreeLegend())
	return m.popupBox(content)
}

// procTreeLegend is the overlay's key hint.
func (m model) procTreeLegend() string {
	return legend("j/k", "scroll", "g/G", "top · end", "r", "refresh", "esc", "close")
}
