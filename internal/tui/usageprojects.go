package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// The usage overlay's projects table (p inside v U): where the spend went, by
// project, in the window and in the week, across every agent baton can read.
//
// The roster answers "which panel do I stop"; this answers "what has this week
// gone on", which is a question about work rather than about processes — and a
// project outlives every panel that ever worked on it. The daemon has already
// done the hard parts: named each project once across both scopes and every
// vendor, folded its worktrees in, ranked the rows and capped them. The cockpit
// only groups and draws, and it does not re-sort: the order is the daemon's, so
// every client shows the same list.

// The projects table's column widths. The name is wide because it is a path, and
// it is the column that gives way first: a figure cell holds "999.9M tok ·
// $9999.99" with its cost, "999.9M tok" without, and neither is ever cut.
const (
	projectNameWidth    = 30 // the widest the name gets
	projectNameMinWidth = 12 // the narrowest before the cells give up their cost
	projectCellWidth    = 21 // a cell with its cost
	projectTokenWidth   = 10 // a cell without
	projectGap          = 2  // the space in front of every cell
)

// gap is the space in front of a cell.
var gap = strings.Repeat(" ", projectGap)

// projectLayout is the table's shape at one popup width: how wide the name and
// the figure cells are. Whether a cell carries its cost follows from its width
// (see projectCell), so the narrow layout needs no flag to drop it.
//
// The popup is not a scroller, and its edge cuts a row wherever it ends — which
// turned "$1234.56" into "$123" on an 80-column terminal, a wrong figure with
// nothing to say it was one. So the widths come from the space there is: the
// name shrinks first, to a floor that still names something; past that the
// cells drop their cost and keep the tokens; and a row that still does not fit
// loses whole cells (fitRow), never part of one.
type projectLayout struct {
	name, cell int
}

func (m model) projectLayout() projectLayout {
	avail := m.popupWidth()
	// A row is the two-cell mark, the name, and two cells each behind a two-space
	// gap — one space let two right-aligned figures run together into one.
	if name := avail - 2 - 2*(projectGap+projectCellWidth); name >= projectNameMinWidth {
		return projectLayout{name: min(name, projectNameWidth), cell: projectCellWidth}
	}
	name := avail - 2 - 2*(projectGap+projectTokenWidth)
	return projectLayout{name: clampInt(name, projectNameMinWidth, projectNameWidth), cell: projectTokenWidth}
}

// fitRow joins a row's cells while they fit the popup, and ends it with an
// ellipsis where the next whole cell would not. A cell is a figure, and half a
// figure is a different figure.
func (m model) fitRow(cells ...string) string {
	avail := m.popupWidth()
	var b strings.Builder
	w := 0
	for _, c := range cells {
		cw := lipgloss.Width(c)
		if w+cw > avail {
			if w < avail {
				b.WriteString("…")
			}
			break
		}
		b.WriteString(c)
		w += cw
	}
	return b.String()
}

// clipLeft fits a label into n display cells by cutting its HEAD, keeping the
// tail: two worktrees of one repository differ in their last segment, and a
// right-hand clip made every row under ~/…/worktrees/pr-review-darkhold/ read the
// same.
func clipLeft(text string, n int) string {
	if lipgloss.Width(text) <= n {
		return text
	}
	if n < 1 {
		return ""
	}
	rs := []rune(text)
	w, i := 0, len(rs)
	for i > 0 {
		rw := lipgloss.Width(string(rs[i-1]))
		if w+rw > n-1 { // leave a cell for the ellipsis
			break
		}
		w += rw
		i--
	}
	return "…" + string(rs[i:])
}

// clipLabel is clipLeft that keeps a bucket's marker. "(unresolved) <dir>" is
// read by its marker first — it says the row is not a project — and by the tail
// of the directory second, so the marker stays whole and the directory gives way
// from its head. A marker that does not fit alone is clipped like any text.
func clipLabel(label string, n int) string {
	marker, rest, ok := strings.Cut(label, ") ")
	if !strings.HasPrefix(label, "(") || !ok {
		return clipLeft(label, n)
	}
	marker += ") "
	room := n - lipgloss.Width(marker)
	if room < 2 { // the ellipsis and one cell of the directory, or it says nothing
		return clip(label, n)
	}
	return marker + clipLeft(rest, room)
}

// usageProjectLines is how many table lines the section draws below its header.
// A project takes one line plus one per agent, so this is a handful of projects,
// which is what fits the popup beside the bars and the roll — the same bound the
// roster keeps with usageBurners, for the same reason: the tail is not what the
// table is read for.
const usageProjectLines = 10

// usageProjectSection is the projects table with its header, or one muted line
// saying why there is none.
func (m model) usageProjectSection() []string {
	var rows []proto.ProjectUsage
	if m.usageInfo != nil {
		rows = m.usageInfo.Projects
	}
	if len(rows) == 0 {
		// Nil is an older daemon, or one with nothing attributed yet; both are "no
		// figures", and an empty table would read as "no projects spent anything".
		return []string{mutedStyle.Render(clip(m.tr("usage.view.no-projects",
			"no per-project figures — an older daemon, or nothing spent yet"), m.popupWidth()))}
	}

	l := m.projectLayout()
	out := []string{mutedStyle.Render(m.fitRow("  "+pad(m.tr("usage.view.project", "Project"), l.name),
		gap+padLeft(m.tr("usage.view.project-session", "session"), l.cell),
		gap+padLeft(m.tr("usage.view.project-week", "week"), l.cell)))}
	if note := m.usageWeekNote(rows); note != "" {
		out = append(out, mutedStyle.Render(clip("  "+note, m.popupWidth())))
	}

	groups := groupProjects(rows)
	lines := 0
	for i, g := range groups {
		if lines+1+len(g) > usageProjectLines {
			out = append(out, mutedStyle.Render("  "+fmt.Sprintf(
				m.tr("usage.view.more-projects", "+%d more"), len(groups)-i)))
			break
		}
		out = append(out, m.projectGroupRows(g, l)...)
		lines += 1 + len(g)
	}
	return out
}

// groupProjects splits the wire rows into one run per project. The daemon sends
// a project's vendor rows next to each other, so a run is consecutive rows with
// one label; grouping by adjacency rather than by map keeps the daemon's order.
func groupProjects(rows []proto.ProjectUsage) [][]proto.ProjectUsage {
	var out [][]proto.ProjectUsage
	for i, r := range rows {
		if i == 0 || r.Project != rows[i-1].Project {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], r)
	}
	return out
}

// projectGroupRows is one project: its total across agents, then a row per
// agent, indented beneath it.
//
// A label in parentheses is not a project but a bucket — (other), (temporary),
// (unresolved) …, (unattributed) — and is drawn muted, so the eye goes to the
// rows that name real work.
func (m model) projectGroupRows(g []proto.ProjectUsage, l projectLayout) []string {
	var total proto.ProjectUsage
	for _, r := range g {
		total.SessionTokens += r.SessionTokens
		total.SessionCostUSD += r.SessionCostUSD
		total.WeekTokens += r.WeekTokens
		total.WeekCostUSD += r.WeekCostUSD
	}
	bucket := strings.HasPrefix(g[0].Project, "(")
	name := pad(clipLabel(usage.ShortenHome(g[0].Project), l.name), l.name)
	mark := lipgloss.NewStyle().Foreground(colBrand).Render("▸ ")
	if bucket {
		name, mark = mutedStyle.Render(name), "  "
	}
	out := []string{m.fitRow(mark+name,
		gap+projectCell(total.SessionTokens, total.SessionCostUSD, l),
		gap+projectCell(total.WeekTokens, total.WeekCostUSD, l))}
	for _, r := range g {
		out = append(out, m.fitRow("    "+mutedStyle.Render(pad(r.Vendor, l.name-2)),
			gap+projectCell(r.SessionTokens, r.SessionCostUSD, l),
			gap+projectCell(r.WeekTokens, r.WeekCostUSD, l)))
	}
	return out
}

// projectCell is one scope's figure, "1.2M tok · $3.40", or a dash when the
// scope saw nothing — a project idle this window is not "0 tok", it is a row
// whose window column has nothing in it.
//
// The cost is there only when the whole figure fits the cell: never in the
// narrow layout, and not for one too long for the wide one ($10000 and up). The
// tokens then stand alone rather than the cell being clipped through the middle
// of a number.
func projectCell(tokens int64, cost float64, l projectLayout) string {
	if tokens <= 0 && cost <= 0 {
		return mutedStyle.Render(padLeft(unknownCell, l.cell))
	}
	cell := humanTokens(tokens)
	if withCost := joinDot(cell, fmt.Sprintf("$%.2f", cost)); cost > 0 && lipgloss.Width(withCost) <= l.cell {
		cell = withCost
	}
	return padLeft(cell, l.cell)
}

// usageWeekNote says what the week column measures. It is not the same thing for
// every agent: a vendor that states its quota reset has a week that starts
// there, and one that never has gets the last seven days — and the column's
// figures mean different things in the two cases, so the header says which.
// When the agents in the table agree it says it once; when they do not, it says
// it per agent rather than pretending to one week.
func (m model) usageWeekNote(rows []proto.ProjectUsage) string {
	in := make(map[string]bool)
	for _, r := range rows {
		in[r.Vendor] = true
	}
	type week struct {
		vendor, phrase string
	}
	var weeks []week
	for _, v := range m.usageInfo.Vendors {
		if !in[v.Vendor] || v.WeekSince == "" {
			continue
		}
		weeks = append(weeks, week{v.Vendor, m.weekPhrase(v)})
	}
	if len(weeks) == 0 {
		return ""
	}
	same := true
	for _, w := range weeks[1:] {
		same = same && w.phrase == weeks[0].phrase
	}
	if same {
		return m.tr("usage.view.week-is", "week: ") + weeks[0].phrase
	}
	parts := make([]string, 0, len(weeks))
	for _, w := range weeks {
		parts = append(parts, w.vendor+" "+w.phrase)
	}
	return m.tr("usage.view.week-is", "week: ") + strings.Join(parts, " · ")
}

// weekPhrase is one vendor's week in words: "since 09-21 14:00" for a quota
// week, "last 7 days" for a rolling one. The instant is numeric so it reads the
// same in every language, and in the cockpit's own zone, where the operator is.
func (m model) weekPhrase(v proto.VendorUsage) string {
	if !v.WeekQuota {
		return m.tr("usage.view.last-7d", "last 7 days")
	}
	since := v.WeekSince // a stamp this cockpit cannot read is shown as sent, not guessed at
	if t, err := time.Parse(time.RFC3339, v.WeekSince); err == nil {
		since = t.In(m.now.Location()).Format("01-02 15:04")
	}
	return fmt.Sprintf(m.tr("usage.view.since", "since %s"), since)
}
