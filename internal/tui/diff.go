package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
)

// The diff popup (modeDiff): a master-detail overlay over the current view, fed by
// the server's structured "diff" reply. The left column lists the target agent's
// changed files, each marked with its git status (the porcelain XY: staged side,
// then unstaged side); the right pane shows the selected file's diff — its staged
// section (git diff --staged) then its unstaged section (git diff) — scrollable.
// It owns nothing server-side: the git commands already ran and were reaped, so
// esc just closes it. tab moves focus between the two panes; j/k navigate the
// focused one.

// diffFileColWidth is the file-list column's preferred width; it shrinks on a
// narrow terminal so the detail pane always keeps room.
const diffFileColWidth = 26

// diffLineKind tags a detail line so the view styles a section header differently
// from diff content, while a single builder keeps the line count (used to clamp
// scrolling) in step with what the view renders.
type diffLineKind int

const (
	diffContent diffLineKind = iota
	diffHeader
)

type diffLine struct {
	text string
	kind diffLineKind
}

// openDiffPopup enters modeDiff with the files from a "diff" reply, remembering
// the view to return to. An empty set is ignored — the server reports "no
// uncommitted changes" before sending in that case, but guard anyway.
func (m model) openDiffPopup(title string, files []proto.DiffFile) model {
	if len(files) == 0 {
		m.status = "diff: no changes"
		return m
	}
	m.diffFrom = m.mode
	m.mode = modeDiff
	m.diffTitle = title
	m.diffFiles = files
	m.diffCursor, m.diffScroll = 0, 0
	m.diffOnDetail = false
	m.status = title
	return m
}

// closeDiffPopup leaves the popup, restoring the view it was opened from and
// dropping the captured files so they cannot linger.
func (m model) closeDiffPopup() (tea.Model, tea.Cmd) {
	m.mode = m.diffFrom
	m.diffFiles = nil
	m.diffTitle = ""
	m.diffCursor, m.diffScroll = 0, 0
	m.diffOnDetail = false
	if m.mode == modeDashboard {
		m.status = "dashboard"
	}
	return m, nil
}

// handleDiffKey drives the popup: tab (or ←/→) switches the focused pane; j/k and
// the arrows move the file selection or scroll the detail pane depending on focus;
// the page keys and home/end jump the detail pane; esc/q close.
func (m model) handleDiffKey(key string) (tea.Model, tea.Cmd) {
	page := max(1, m.diffViewportRows()-1)
	switch key {
	case "esc", "q":
		return m.closeDiffPopup()
	case "tab", "left", "right", "h", "l":
		m.diffOnDetail = !m.diffOnDetail
	case "up", "k":
		if m.diffOnDetail {
			m.diffScrollBy(-1)
		} else {
			m.diffSelect(-1)
		}
	case "down", "j":
		if m.diffOnDetail {
			m.diffScrollBy(1)
		} else {
			m.diffSelect(1)
		}
	case "pgup", "ctrl+u", "b":
		m.diffScrollBy(-page)
	case "pgdown", "ctrl+d", "ctrl+f", " ":
		m.diffScrollBy(page)
	case "home", "g":
		m.diffScroll = 0
	case "end", "G":
		m.diffScrollBy(1 << 30)
	}
	return m, nil
}

// diffSelect moves the file cursor by delta, clamps it, and resets the detail
// scroll so a new file opens at its top.
func (m *model) diffSelect(delta int) {
	m.diffCursor = clampInt(m.diffCursor+delta, 0, len(m.diffFiles)-1)
	m.diffScroll = 0
}

// diffScrollBy moves the detail-pane offset by delta, clamped so the last line
// can rest at the bottom and the offset never runs negative.
func (m *model) diffScrollBy(delta int) {
	maxOff := max(0, len(m.diffDetailLines())-m.diffViewportRows())
	m.diffScroll = clampInt(m.diffScroll+delta, 0, maxOff)
}

// diffDetailLines builds the selected file's detail lines: a staged section (when
// the file has staged changes) followed by an unstaged section, each led by a
// header. The slice's length is what diffScrollBy clamps against, so it must match
// what diffView renders one-for-one.
func (m model) diffDetailLines() []diffLine {
	if m.diffCursor < 0 || m.diffCursor >= len(m.diffFiles) {
		return nil
	}
	f := m.diffFiles[m.diffCursor]
	var lines []diffLine
	add := func(text string, kind diffLineKind) { lines = append(lines, diffLine{text, kind}) }
	if strings.TrimSpace(f.Staged) != "" {
		add("● staged — git diff --staged", diffHeader)
		for _, l := range strings.Split(strings.TrimRight(f.Staged, "\n"), "\n") {
			add(l, diffContent)
		}
	}
	if strings.TrimSpace(f.Unstaged) != "" {
		if len(lines) > 0 {
			add("", diffContent)
		}
		add("○ unstaged — git diff", diffHeader)
		for _, l := range strings.Split(strings.TrimRight(f.Unstaged, "\n"), "\n") {
			add(l, diffContent)
		}
	}
	if len(lines) == 0 {
		add("(no textual diff)", diffContent)
	}
	return lines
}

// diffViewportRows is the popup's body height — the rows the file list and the
// detail pane each show. It also bounds the detail scroll. Kept in one place so
// the key handler and the view agree.
func (m model) diffViewportRows() int {
	return clampInt(m.height-14, 5, 40)
}

// diffLayout sizes the two columns within the popup: a file column that shrinks on
// a narrow terminal, and the detail pane taking the rest (3 cells go to the " │ "
// divider). rows is diffViewportRows.
func (m model) diffLayout() (fileColW, detailW, rows int) {
	rows = m.diffViewportRows()
	innerW := min(clampInt(m.width-16, 24, 140), m.width-8) // bounded, but never wider than the screen
	fileColW = min(diffFileColWidth, innerW-14)
	fileColW = max(fileColW, 8)
	detailW = max(innerW-fileColW-3, 10)
	return
}

// diffView renders the master-detail popup: the file list (left) and the selected
// file's diff (right), divided by a hairline, under a header and a key legend.
func (m model) diffView() string {
	if len(m.diffFiles) == 0 {
		return configBox(mutedStyle.Render("no changes to diff"))
	}
	fileColW, detailW, rows := m.diffLayout()

	left := padBlock(m.diffFileRows(fileColW, rows), rows, fileColW)
	right := padBlock(m.diffDetailBlock(detailW, rows), rows, detailW)
	sepStyle := lipgloss.NewStyle().Foreground(colFaint)
	sep := make([]string, rows)
	for i := range sep {
		sep[i] = sepStyle.Render(" │ ")
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.JoinVertical(lipgloss.Left, left...),
		lipgloss.JoinVertical(lipgloss.Left, sep...),
		lipgloss.JoinVertical(lipgloss.Left, right...),
	)

	header := sectionStyle.Render(spaced("DIFF")) + "  " +
		mutedStyle.Render(strings.TrimPrefix(m.diffTitle, "diff · ")) +
		mutedStyle.Render(fmt.Sprintf("  ·  %d file(s)", len(m.diffFiles)))

	content := lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", m.diffLegend())
	return configBox(content)
}

// diffFileRows builds the file-list column: a status marker (the porcelain XY,
// staged side green, unstaged side cyan) and the path, the cursor row carated and
// brightened. The list is windowed around the cursor so a long change set scrolls.
func (m model) diffFileRows(width, rows int) []string {
	all := make([]string, len(m.diffFiles))
	for i, f := range m.diffFiles {
		mark := diffStatusMark(f) + " "
		caret, nameFg := "  ", colMuted
		if i == m.diffCursor {
			caret, nameFg = lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ "), colInk
		}
		name := lipgloss.NewStyle().Foreground(nameFg).Render(truncate(f.Path, max(1, width-4)))
		all[i] = lipgloss.NewStyle().Width(width).Render(caret + mark + name)
	}
	shown, _ := windowAround(all, m.diffCursor, rows)
	return shown
}

// diffStatusMark renders a file's two-column git status: the staged (index) side
// then the unstaged (work-tree) side, a space standing in for an unchanged side.
func diffStatusMark(f proto.DiffFile) string {
	cell := func(s string, fg lipgloss.Color) string {
		if s == "" {
			s = " "
		}
		return lipgloss.NewStyle().Foreground(fg).Render(s)
	}
	return cell(f.Index, colGreen) + cell(f.Work, colCyan)
}

// diffDetailBlock builds the detail pane: the selected file's section lines,
// windowed by the scroll offset and styled (headers emphasised, +/- lines
// coloured), each fixed to the pane width.
func (m model) diffDetailBlock(width, rows int) []string {
	lines := m.diffDetailLines()
	off := clampInt(m.diffScroll, 0, max(0, len(lines)-rows))
	end := min(off+rows, len(lines))
	out := make([]string, 0, rows)
	for _, dl := range lines[off:end] {
		if dl.kind == diffHeader {
			out = append(out, renderDiffHeader(dl.text, width))
			continue
		}
		out = append(out, renderDiffContentLine(dl.text, width))
	}
	return out
}

// renderDiffHeader styles a detail-pane section header, tinting its leading
// status glyph to echo the per-line colouring below: green for the staged side,
// cyan for the unstaged side, with the label in the highlight blue.
func renderDiffHeader(text string, width int) string {
	label := lipgloss.NewStyle().Foreground(colBrandHi).Bold(true)
	glyphFg := colBrandHi
	switch {
	case strings.HasPrefix(text, "● "):
		glyphFg = colGreen
	case strings.HasPrefix(text, "○ "):
		glyphFg = colCyan
	}
	r := []rune(text)
	if glyphFg == colBrandHi || len(r) == 0 {
		return label.Width(width).Render(truncate(text, max(1, width)))
	}
	glyph := lipgloss.NewStyle().Foreground(glyphFg).Bold(true).Render(string(r[0]))
	rest := label.Render(truncate(string(r[1:]), max(1, width-1)))
	return lipgloss.NewStyle().Width(width).Render(glyph + rest)
}

// renderDiffContentLine colours one diff content line by its lead: additions
// green, deletions red, hunk headers cyan, file/meta lines muted, context plain.
func renderDiffContentLine(line string, width int) string {
	fg := colInk
	switch {
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		fg = colMuted
	case strings.HasPrefix(line, "+"):
		fg = colGreen
	case strings.HasPrefix(line, "-"):
		fg = colRed
	case strings.HasPrefix(line, "@@"):
		fg = colCyan
	case strings.HasPrefix(line, "diff --git"), strings.HasPrefix(line, "index "), strings.HasPrefix(line, "new file"):
		fg = colMuted
	}
	return lipgloss.NewStyle().Foreground(fg).Width(width).Render(clipVisible(line, width))
}

// diffLegend is the popup's key hint, marking the focused pane.
func (m model) diffLegend() string {
	focus := "files"
	if m.diffOnDetail {
		focus = "diff"
	}
	return mutedStyle.Render("["+focus+"]  ") +
		legend("tab", "switch", "j/k", "move · scroll", "esc", "close")
}

// padBlock pads (or leaves) a column to exactly rows lines, each blank line set to
// the column width so the bordered surface stays rectangular.
func padBlock(rowsIn []string, rows, width int) []string {
	blank := lipgloss.NewStyle().Width(width).Render("")
	for len(rowsIn) < rows {
		rowsIn = append(rowsIn, blank)
	}
	return rowsIn
}

// clampInt confines v to [lo, hi]; if hi < lo (an empty range) it returns lo.
func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}
