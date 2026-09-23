package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// The usage overlay's project tabs (Session and Week inside v U): where the
// spend went, by project, over one scope, across every agent baton can read.
//
// The account tab answers "which panel do I stop"; these answer "what has this
// work cost", which is a question about work rather than about processes — and a
// project outlives every panel that ever worked on it. The daemon has already
// done the hard parts: named each project once across both scopes and every
// vendor, folded its worktrees in, and capped the rows. The cockpit groups them,
// orders them by the open tab's scope, and draws.
//
// One scope a tab, rather than the window and the week side by side: two scopes
// on a row left no room for a bar, and a bar is what makes "which project took
// most of it" a glance instead of a comparison of six-digit numbers.

// The project table's column widths. The name is wide because it is a path, and
// it gives way first; the bar gives way next, then the cost. The figure columns
// are sized for the longest figure they hold — "100%", "999.9M", "$99999.99" — so
// they are never cut, only dropped whole.
const (
	projectNameWidth    = 40 // the widest the name gets
	projectNameMinWidth = 12 // the narrowest before the bar gives way
	projectBarWidth     = 16 // the bar at full width, the account bars' width
	projectBarMinWidth  = 6  // the narrowest bar still worth drawing
	projectShareWidth   = 5  // "100%", or "share"
	projectTokenWidth   = 6  // "999.9M", or "tokens"
	projectCostWidth    = 9  // "$99999.99"
	projectGap          = 2  // the space in front of every cell
)

// gap is the space in front of a cell.
var gap = strings.Repeat(" ", projectGap)

// projectLayout is the table's shape at one popup width: how wide the name and
// the bar are (a zero bar is no bar column at all), and whether the cost column
// is drawn.
//
// The popup is not a scroller, and its edge cuts a row wherever it ends — which
// once turned "$1234.56" into "$123" on an 80-column terminal, a wrong figure
// with nothing to say it was one. So the widths come from the space there is:
// the name shrinks first, to a floor that still names something; then the bar
// narrows and goes; then the cost column goes; and a row that still does not fit
// loses whole cells (fitRow), never part of one.
type projectLayout struct {
	name, bar int
	cost      bool
}

func (m model) projectLayout() projectLayout {
	avail := m.popupWidth()
	// A row is the two-cell mark, the name, and each cell behind a two-space gap —
	// one space let two right-aligned figures run together into one.
	figures := 2 + projectGap + projectShareWidth + projectGap + projectTokenWidth
	withCost := figures + projectGap + projectCostWidth
	if name := avail - withCost - projectGap - projectBarWidth; name >= projectNameMinWidth {
		return projectLayout{name: min(name, projectNameWidth), bar: projectBarWidth, cost: true}
	}
	if bar := avail - withCost - projectGap - projectNameMinWidth; bar >= projectBarMinWidth {
		return projectLayout{name: projectNameMinWidth, bar: bar, cost: true}
	}
	if name := avail - withCost; name >= projectNameMinWidth {
		return projectLayout{name: min(name, projectNameWidth), cost: true}
	}
	return projectLayout{name: clampInt(avail-figures, projectNameMinWidth, projectNameWidth)}
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

// usageProjectMinLines is the fewest table lines a project tab draws below its
// header, whatever the terminal says. A project takes one line plus one per
// agent, so this is a handful of projects.
const usageProjectMinLines = 10

// usageScopeChrome is the lines a project tab spends on things other than
// project rows: the title, the tab bar and the blank under it, the scope note,
// the column header, the "+N more" line, and the blank and legend at the foot.
const usageScopeChrome = 8

// usageProjectLines is how many table lines a project tab may draw: whatever the
// terminal leaves once the popup's box and the tab's own chrome are paid for. The
// tab has the whole popup to itself, so the bound is the screen, not a guess at
// what fits beside something else; the tail past it is a count, not a scroller.
func (m model) usageProjectLines() int {
	if m.height <= 0 {
		return usageProjectMinLines
	}
	return max(usageProjectMinLines, m.height-1-popupChrome-usageScopeChrome)
}

// scopeFigures is one wire row's spend in the tab's scope.
func scopeFigures(r proto.ProjectUsage, tab usageTab) (int64, float64) {
	if tab == usageTabWeek {
		return r.WeekTokens, r.WeekCostUSD
	}
	return r.SessionTokens, r.SessionCostUSD
}

// scopeSpend is a project or one of its agents, in one scope.
type scopeSpend struct {
	label  string
	tokens int64
	cost   float64
}

// scopeProject is a project's total in one scope, and the agents under it.
type scopeProject struct {
	scopeSpend
	agents []scopeSpend
}

// isBucket says a label is not a project but a bucket — (other), (temporary),
// (unresolved) …, (unattributed).
func isBucket(label string) bool { return strings.HasPrefix(label, "(") }

// scopeProjects groups the wire rows into projects for one scope, heaviest
// first, and the scope's total they are shares of.
//
// The daemon ranks by the week, which is the wrong order for the session tab —
// a project that ate the week can be idle this window — so the cockpit re-sorts
// by the open scope. A bucket goes after every real project whatever it weighs:
// "(other)" can outweigh any single project and still not be the answer to
// "where did it go". A project, or an agent under one, that spent nothing in
// the scope is left out rather than drawn as a zero row.
func scopeProjects(rows []proto.ProjectUsage, tab usageTab) ([]scopeProject, int64) {
	index := make(map[string]int)
	var out []scopeProject
	var total int64
	for _, r := range rows {
		tokens, cost := scopeFigures(r, tab)
		if tokens <= 0 && cost <= 0 {
			continue
		}
		total += tokens
		i, ok := index[r.Project]
		if !ok {
			i = len(out)
			index[r.Project] = i
			out = append(out, scopeProject{scopeSpend: scopeSpend{label: r.Project}})
		}
		p := &out[i]
		p.tokens += tokens
		p.cost += cost
		p.agents = append(p.agents, scopeSpend{label: r.Vendor, tokens: tokens, cost: cost})
	}
	heavier := func(a, b scopeSpend) bool {
		if a.tokens != b.tokens {
			return a.tokens > b.tokens
		}
		return a.label < b.label
	}
	sort.SliceStable(out, func(i, j int) bool {
		if bi, bj := isBucket(out[i].label), isBucket(out[j].label); bi != bj {
			return bj
		}
		return heavier(out[i].scopeSpend, out[j].scopeSpend)
	})
	for _, p := range out {
		sort.SliceStable(p.agents, func(i, j int) bool { return heavier(p.agents[i], p.agents[j]) })
	}
	return out, total
}

// usageScopeSection is a project tab's page: what its scope measures, the
// column header, and the projects — or one muted line saying why there are none.
func (m model) usageScopeSection(tab usageTab) []string {
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
	projects, total := scopeProjects(rows, tab)
	if len(projects) == 0 {
		// Figures exist, just none in this scope: a quiet window after a busy week.
		reason := m.tr("usage.view.session-idle", "nothing spent this window yet")
		if tab == usageTabWeek {
			reason = m.tr("usage.view.week-idle", "nothing spent this week yet")
		}
		return []string{mutedStyle.Render(clip(reason, m.popupWidth()))}
	}

	l := m.projectLayout()
	var out []string
	if note := m.usageScopeNote(tab, rows); note != "" {
		out = append(out, mutedStyle.Render(clip("  "+note, m.popupWidth())))
	}
	out = append(out, mutedStyle.Render(m.projectRow(l, "  "+pad(m.tr("usage.view.project", "Project"), l.name),
		strings.Repeat(" ", l.bar), m.tr("usage.view.share", "share"),
		m.tr("usage.view.tokens", "tokens"), m.tr("usage.view.cost", "cost"))))

	budget, lines := m.usageProjectLines(), 0
	for i, p := range projects {
		if lines+1+len(p.agents) > budget {
			out = append(out, mutedStyle.Render("  "+fmt.Sprintf(
				m.tr("usage.view.more-projects", "+%d more"), len(projects)-i)))
			break
		}
		out = append(out, m.projectRows(p, total, l)...)
		lines += 1 + len(p.agents)
	}
	return out
}

// usageScopeNote says what the open tab's scope measures: the window and when it
// rolls over, or which week (usageWeekNote).
func (m model) usageScopeNote(tab usageTab, rows []proto.ProjectUsage) string {
	if tab == usageTabWeek {
		return m.usageWeekNote(rows)
	}
	note := m.tr("usage.view.this-window", "this window")
	if left := m.usageWindowLeft(); left != "" {
		note = joinDot(note, m.tr("usage.view.resets", "resets")+" "+left)
	}
	return note
}

// projectRow lays out one line of the table from its cells, already styled or
// not: the lead (mark and name, 2+name cells), the bar, and the three figures.
// Every line of the table — the header included — goes through here, so a
// column's right edge is the same display cell on every one of them.
func (m model) projectRow(l projectLayout, lead, bar, share, tokens, cost string) string {
	cells := []string{lead}
	if l.bar > 0 {
		cells = append(cells, gap+bar)
	}
	cells = append(cells, gap+alignRight(share, projectShareWidth), gap+alignRight(tokens, projectTokenWidth))
	if l.cost {
		cells = append(cells, gap+alignRight(cost, projectCostWidth))
	}
	return m.fitRow(cells...)
}

// alignRight is padLeft for a cell that may already carry its style: the padding
// is measured on the display width and kept outside the escapes.
func alignRight(s string, w int) string {
	return strings.Repeat(" ", max(0, w-lipgloss.Width(s))) + s
}

// projectRows is one project: its total across agents, then a row per agent,
// indented beneath it.
//
// A label in parentheses is not a project but a bucket, and is drawn muted — its
// bar too — so the eye goes to the rows that name real work. An agent row has no
// bar of its own: its share is the figure beside it, and a second bar under every
// project doubled the ink without saying more than the percentage does.
func (m model) projectRows(p scopeProject, total int64, l projectLayout) []string {
	bucket := isBucket(p.label)
	name := pad(clipLabel(usage.ShortenHome(p.label), l.name), l.name)
	mark := lipgloss.NewStyle().Foreground(colBrand).Render("▸ ")
	barStyle := lipgloss.NewStyle().Foreground(colBrand)
	if bucket {
		name, mark, barStyle = mutedStyle.Render(name), "  ", mutedStyle
	}
	fraction := shareOf(p.tokens, total)
	out := []string{m.projectRow(l, mark+name, barStyle.Render(usage.Bar(fraction, l.bar)),
		shareCell(fraction), bareTokens(p.tokens), costCell(p.cost))}
	for _, a := range p.agents {
		out = append(out, m.projectRow(l, "    "+mutedStyle.Render(pad(a.label, l.name-2)),
			strings.Repeat(" ", l.bar), shareCell(shareOf(a.tokens, total)), bareTokens(a.tokens), costCell(a.cost)))
	}
	return out
}

// shareOf is tokens as a fraction of the scope's total.
func shareOf(tokens, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(tokens) / float64(total)
}

// shareCell is a share as a percentage. A share too small to round to one
// percent says so rather than claiming 0% of a scope it did spend in.
func shareCell(fraction float64) string {
	// Rounding decides the edges, not the raw fraction: %.0f rounds a half to
	// even, so 0.5% would print "0%" for a project that did spend, and 99.6%
	// would print "100%" beside a bar that is visibly not full.
	pct := fraction * 100
	switch printed := fmt.Sprintf("%.0f", pct); {
	case pct > 0 && printed == "0":
		return "<1%"
	case pct < 100 && printed == "100":
		return "99%"
	default:
		return printed + "%"
	}
}

// costCell is a cost, or a muted dash when the reader priced nothing — an agent
// whose tokens baton cannot price is not free, and "$0.00" would say it was. A
// cost too long for its column drops its cents before it would be cut.
func costCell(cost float64) string {
	if cost <= 0 {
		return mutedStyle.Render(unknownCell)
	}
	s := fmt.Sprintf("$%.2f", cost)
	if lipgloss.Width(s) > projectCostWidth {
		s = fmt.Sprintf("$%.0f", cost)
	}
	return s
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
