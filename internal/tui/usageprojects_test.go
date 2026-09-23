package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/proto"
)

// projectsModel is the limits cockpit with a daemon that sent project rows, in
// the order the daemon sends them: a real project's vendor rows together, then
// the per-vendor (other) bucket.
func projectsModel(t *testing.T) model {
	t.Helper()
	t.Setenv("HOME", "/h")
	m := limitsModel(usageWindow)
	m.mode = modeUsage
	m.usageInfo.Vendors = []proto.VendorUsage{
		{Vendor: "claude", State: vendorReading, WeekSince: "2026-07-06T14:00:00Z", WeekQuota: true},
		{Vendor: "grok", State: vendorReading, WeekSince: "2026-07-06T14:00:00Z", WeekQuota: true},
	}
	m.usageInfo.Projects = []proto.ProjectUsage{
		{Project: "/h/mylab/baton", Vendor: "claude", SessionTokens: 300_000, SessionCostUSD: 3, WeekTokens: 1_000_000, WeekCostUSD: 20},
		{Project: "/h/mylab/baton", Vendor: "grok", WeekTokens: 200_000, WeekCostUSD: 2},
		{Project: "/srv/api", Vendor: "claude", SessionTokens: 5_000, WeekTokens: 90_000, WeekCostUSD: 1},
		{Project: proto.ProjectOther, Vendor: "claude", WeekTokens: 4_000},
	}
	return m
}

// onTab is m with the overlay turned to tab.
func onTab(m model, tab usageTab) model {
	m.usageTab = tab
	return m
}

// lineWith is the index of the first rendered line containing all of parts.
func lineWith(lines []string, parts ...string) int {
	for i, l := range lines {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(l, p)
		}
		if ok {
			return i
		}
	}
	return -1
}

// tab walks Account → Session → Week and wraps; shift+tab walks back. Neither
// leaves the overlay, and reopening it lands on the tab it was closed on.
func TestTabTurnsTheOverlayPages(t *testing.T) {
	m := projectsModel(t)
	press := func(m model, key string) model {
		t.Helper()
		next, _ := m.handleUsageKey(key)
		nm := next.(model)
		if nm.mode != modeUsage {
			t.Fatalf("%s left the overlay: mode %v", key, nm.mode)
		}
		return nm
	}
	for i, want := range []usageTab{usageTabSession, usageTabWeek, usageTabAccount, usageTabSession} {
		m = press(m, "tab")
		if m.usageTab != want {
			t.Fatalf("tab #%d landed on %d, want %d", i+1, m.usageTab, want)
		}
	}
	for i, want := range []usageTab{usageTabAccount, usageTabWeek, usageTabSession} {
		m = press(m, "shift+tab")
		if m.usageTab != want {
			t.Fatalf("shift+tab #%d landed on %d, want %d", i+1, m.usageTab, want)
		}
	}
	// p is no longer a binding.
	if m = press(m, "p"); m.usageTab != usageTabSession {
		t.Errorf("p turned the page to %d", m.usageTab)
	}
	closed, _ := m.closeUsage()
	if got := closed.(model).openUsage(modeDashboard).usageTab; got != usageTabSession {
		t.Errorf("reopening the overlay landed on tab %d, want the session tab", got)
	}
}

// The legend names tab and no longer names p, on every tab.
func TestTheLegendNamesTab(t *testing.T) {
	for tab := range usageTabCount {
		out := stripANSI(onTab(projectsModel(t), tab).usageView())
		if !strings.Contains(out, "tab switch") || !strings.Contains(out, "u cycle footer") || !strings.Contains(out, "esc close") {
			t.Errorf("tab %d: the legend does not offer tab, u and esc:\n%s", tab, out)
		}
		if strings.Contains(out, "p projects") || strings.Contains(out, "p panels") {
			t.Errorf("tab %d: the legend still offers p:\n%s", tab, out)
		}
	}
}

// The tab bar sits right under the title and lights the open tab alone.
func TestTheTabBarMarksTheOpenTab(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	names := []string{"Account", "Session", "Week"}
	for tab := range usageTabCount {
		m := onTab(projectsModel(t), tab)
		bar := m.usageTabBar()
		for i, name := range names {
			style := tabHotStyle
			if i == 0 {
				style = style.PaddingLeft(0)
			}
			hot := strings.Contains(bar, style.Render(name))
			if hot != (usageTab(i) == tab) {
				t.Errorf("tab %d: %s lit = %v", tab, name, hot)
			}
		}
		if lines := strings.Split(stripANSI(m.usageView()), "\n"); lineWith(lines, "Account", "Session", "Week", " · ") != lineWith(lines, "A C C O U N T")+1 {
			t.Errorf("tab %d: the tab bar is not the line under the title:\n%s", tab, strings.Join(lines, "\n"))
		}
		// The first label starts in the title's column, not one cell in from it.
		lines := strings.Split(stripANSI(m.usageView()), "\n")
		title := lines[lineWith(lines, "A C C O U N T")]
		tabs := lines[lineWith(lines, "Account", "Session", "Week", " · ")]
		if got, want := strings.Index(tabs, "Account"), strings.Index(title, "A C C O U N T"); got != want {
			t.Errorf("tab %d: the tab bar starts at byte %d, the title at %d", tab, got, want)
		}
	}
}

// The Account tab is the page the overlay always had: bars, roster, roll, and no
// project table. The project tabs have none of the account page.
func TestTheTabsSplitTheAccountFromTheProjects(t *testing.T) {
	account := []string{"Session (5h)", "Burning this window", "Agent"}
	{
		m := projectsModel(t)
		out := stripANSI(onTab(m, usageTabAccount).usageView())
		for _, want := range account {
			if !strings.Contains(out, want) {
				t.Errorf("the account tab lost %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "~/mylab/baton") || strings.Contains(out, "Project") {
			t.Errorf("the account tab draws the projects:\n%s", out)
		}
		for _, tab := range []usageTab{usageTabSession, usageTabWeek} {
			out := stripANSI(onTab(m, tab).usageView())
			for _, gone := range append(account, "5h left", "resets 2:14:00", "no quota reading") {
				if strings.Contains(out, gone) {
					t.Errorf("tab %d still draws %q:\n%s", tab, gone, out)
				}
			}
			if !strings.Contains(out, "~/mylab/baton") {
				t.Errorf("tab %d does not draw the projects:\n%s", tab, out)
			}
		}
	}
	// Without a quota reading the account tab still says why, and the project
	// tabs still draw: they are baton's own reading, not the vendor's.
	m := projectsModel(t)
	m.usageInfo.Limits = nil
	if out := stripANSI(m.usageView()); !strings.Contains(out, "no quota reading yet") || !strings.Contains(out, "Agent") {
		t.Errorf("the account tab without limits lost its reason or its roll:\n%s", out)
	}
	if out := stripANSI(onTab(m, usageTabWeek).usageView()); !strings.Contains(out, "~/mylab/baton") || strings.Contains(out, "no quota reading") {
		t.Errorf("the week tab without limits:\n%s", out)
	}
}

// The session tab counts the window alone: a project or an agent idle in it is
// left out, and the rest are shares of the window's total.
func TestTheSessionTabCountsTheWindow(t *testing.T) {
	m := onTab(projectsModel(t), usageTabSession)
	lines := strings.Split(stripANSI(m.usageView()), "\n")
	baton := lineWith(lines, "▸ ~/mylab/baton", "98%", "300.0K", "$3.00")
	claude := lineWith(lines, "    claude", "98%", "300.0K", "$3.00")
	api := lineWith(lines, "▸ /srv/api", "2%", "5.0K")
	if baton < 0 || claude != baton+1 || api != baton+2 {
		t.Errorf("rows out of place: baton %d, claude %d, api %d\n%s", baton, claude, api, strings.Join(lines, "\n"))
	}
	for _, gone := range []string{"grok", "(other)", "tok"} {
		if lineWith(lines[max(baton, 0):], gone) >= 0 {
			t.Errorf("the session table draws %q:\n%s", gone, strings.Join(lines, "\n"))
		}
	}
	if lineWith(lines, "this window · resets 4:00:00") < 0 {
		t.Errorf("no window note:\n%s", strings.Join(lines, "\n"))
	}
	// A cost the reader did not price is a dash, not $0.00.
	if row := strings.TrimSpace(strings.Trim(lines[max(api, 0)], "│ ")); strings.Contains(row, "$0.00") || !strings.HasSuffix(row, "-") {
		t.Errorf("an unpriced cost is not a dash: %q", lines[max(api, 0)])
	}
}

// The week tab draws every agent that spent in the week, heaviest first under
// each project, and a bucket after the projects.
func TestTheWeekTabCountsTheWeek(t *testing.T) {
	m := onTab(projectsModel(t), usageTabWeek)
	lines := strings.Split(stripANSI(m.usageView()), "\n")
	baton := lineWith(lines, "▸ ~/mylab/baton", "93%", "1.2M", "$22.00")
	claude := lineWith(lines, "    claude", "77%", "1.0M", "$20.00")
	grok := lineWith(lines, "    grok", "15%", "200.0K", "$2.00")
	api := lineWith(lines, "▸ /srv/api", "7%", "90.0K", "$1.00")
	other := lineWith(lines, "(other)", "<1%", "4.0K")
	if baton < 0 || claude != baton+1 || grok != baton+2 || api != baton+3 || other != api+2 {
		t.Errorf("rows out of place: baton %d, claude %d, grok %d, api %d, other %d\n%s",
			baton, claude, grok, api, other, strings.Join(lines, "\n"))
	}
	if strings.Contains(strings.Join(lines, "\n"), "/h/mylab") {
		t.Error("the home directory was not shortened")
	}
}

// Each tab orders by its own scope, not the daemon's week order, and a bucket
// comes after every project however much it weighs.
func TestEachTabRanksByItsScope(t *testing.T) {
	m := projectsModel(t)
	m.usageInfo.Projects = []proto.ProjectUsage{
		{Project: "/w/heavy-week", Vendor: "claude", SessionTokens: 10, WeekTokens: 9_000},
		{Project: "/w/heavy-now", Vendor: "grok", SessionTokens: 500, WeekTokens: 600},
		{Project: "/w/heavy-now", Vendor: "claude", SessionTokens: 800, WeekTokens: 900},
		{Project: proto.ProjectOther, Vendor: "claude", SessionTokens: 99_999, WeekTokens: 99_999},
		{Project: "/w/tie-b", Vendor: "claude", SessionTokens: 10, WeekTokens: 1},
	}
	for _, c := range []struct {
		tab   usageTab
		order []string
	}{
		{usageTabSession, []string{"/w/heavy-now", "claude", "grok", "/w/heavy-week", "/w/tie-b", "(other)"}},
		{usageTabWeek, []string{"/w/heavy-week", "/w/heavy-now", "claude", "grok", "/w/tie-b", "(other)"}},
	} {
		lines := strings.Split(stripANSI(onTab(m, c.tab).usageView()), "\n")
		at := -1
		for _, want := range c.order {
			i := lineWith(lines[at+1:], want)
			if i < 0 {
				t.Errorf("tab %d: %q not after line %d:\n%s", c.tab, want, at, strings.Join(lines, "\n"))
				break
			}
			at += 1 + i
		}
	}
}

// Buckets are drawn muted, bar included; a real project's name is not.
func TestBucketRowsAreMuted(t *testing.T) {
	// A test binary has no TTY, so lipgloss renders plain text by default and
	// every style would compare equal to none.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	m := onTab(projectsModel(t), usageTabWeek)
	l := m.projectLayout()
	out := m.usageView()
	if !strings.Contains(out, mutedStyle.Render(pad(proto.ProjectOther, l.name))) {
		t.Error("(other) is not muted")
	}
	if strings.Contains(out, mutedStyle.Render(pad("/srv/api", l.name))) {
		t.Error("a real project was drawn muted")
	}
	for _, label := range []string{"(temporary)", "(unresolved) -p-x", "(unattributed)"} {
		m.usageInfo.Projects = []proto.ProjectUsage{
			{Project: "/srv/api", Vendor: "claude", WeekTokens: 3},
			{Project: label, Vendor: "claude", WeekTokens: 1},
		}
		out := m.usageView()
		if !strings.Contains(out, mutedStyle.Render(pad(label, l.name))) {
			t.Errorf("%s is not muted", label)
		}
		if !strings.Contains(out, mutedStyle.Render("▓▓▓▓░░░░░░░░░░░░")) {
			t.Errorf("%s's bar is not muted", label)
		}
	}
}

// The week note says what the week tab measures: one phrase when the agents
// agree, per agent when they do not, and nothing about an agent that has no row
// in the table.
func TestTheWeekHeaderSaysWhichWeek(t *testing.T) {
	for _, c := range []struct {
		name    string
		vendors []proto.VendorUsage
		want    string
	}{
		{"quota, agreed", nil, "week: since 07-06 14:00"},
		{"rolling", []proto.VendorUsage{
			{Vendor: "claude", WeekSince: "2026-07-01T10:00:00Z"},
			{Vendor: "grok", WeekSince: "2026-07-01T10:00:00Z"},
		}, "week: last 7 days"},
		{"mixed", []proto.VendorUsage{
			{Vendor: "claude", WeekSince: "2026-07-06T14:00:00Z", WeekQuota: true},
			{Vendor: "grok", WeekSince: "2026-07-01T10:00:00Z"},
			{Vendor: "codex", WeekSince: "2026-07-01T10:00:00Z"}, // no row in the table
		}, "week: claude since 07-06 14:00 · grok last 7 days"},
	} {
		m := onTab(projectsModel(t), usageTabWeek)
		if c.vendors != nil {
			m.usageInfo.Vendors = c.vendors
		}
		lines := strings.Split(stripANSI(m.usageView()), "\n")
		i := lineWith(lines, c.want)
		if i < 0 {
			t.Errorf("%s: no %q in\n%s", c.name, c.want, strings.Join(lines, "\n"))
			continue
		}
		if strings.Contains(lines[i], "codex") {
			t.Errorf("%s: the header names an agent with no row: %q", c.name, lines[i])
		}
	}
}

// A window with no countdown to show — an older daemon, or one past its end —
// still names the scope, and claims no reset.
func TestTheWindowNoteWithoutACountdown(t *testing.T) {
	m := onTab(projectsModel(t), usageTabSession)
	m.usageInfo.Resets = false
	out := stripANSI(m.usageView())
	if !strings.Contains(out, "  this window\n") && !strings.Contains(out, "  this window ") {
		t.Errorf("no bare window note:\n%s", out)
	}
	if strings.Contains(out, "resets") {
		t.Errorf("a window with no reset claims one:\n%s", out)
	}
}

// No project rows is a muted reason, never an empty table — with a quota reading
// or without one; and a scope with nothing spent says so for that scope.
func TestNoProjectsSaysWhy(t *testing.T) {
	for _, withLimits := range []bool{true, false} {
		for _, tab := range []usageTab{usageTabSession, usageTabWeek} {
			m := onTab(projectsModel(t), tab)
			m.usageInfo.Projects = nil
			if !withLimits {
				m.usageInfo.Limits = nil
			}
			out := stripANSI(m.usageView())
			if !strings.Contains(out, "no per-project figures") || strings.Contains(out, "Project ") {
				t.Errorf("limits %v tab %d: nil projects did not read as a reason:\n%s", withLimits, tab, out)
			}
		}
	}
	m := projectsModel(t)
	m.usageInfo.Projects = []proto.ProjectUsage{{Project: "/srv/api", Vendor: "claude", WeekTokens: 5}}
	if out := stripANSI(onTab(m, usageTabSession).usageView()); !strings.Contains(out, "nothing spent this window yet") || strings.Contains(out, "Project ") {
		t.Errorf("an idle window did not read as one:\n%s", out)
	}
	m.usageInfo.Projects = []proto.ProjectUsage{{Project: "/srv/api", Vendor: "claude", SessionCostUSD: 1}}
	if out := stripANSI(onTab(m, usageTabWeek).usageView()); !strings.Contains(out, "nothing spent this week yet") {
		t.Errorf("an idle week did not read as one:\n%s", out)
	}
}

// manyProjects is n one-agent projects, heaviest first: two table lines each.
func manyProjects(n int) []proto.ProjectUsage {
	var rows []proto.ProjectUsage
	for i := range n {
		rows = append(rows, proto.ProjectUsage{Project: fmt.Sprintf("/p/%02d", i), Vendor: "claude",
			SessionTokens: int64(1000 - i), WeekTokens: int64(1000 - i)})
	}
	return rows
}

// The table is bounded by the terminal: whole projects only, and a line saying
// how many more. With no height known it keeps the old ten-line floor, and a
// short terminal never takes it below that.
func TestTheProjectsTableIsBoundedByTheTerminal(t *testing.T) {
	for _, c := range []struct {
		height        int
		last, missing string
		more          string
	}{
		{0, "/p/04", "/p/05", "+25 more"},  // floor of 10 lines: five projects
		{12, "/p/04", "/p/05", "+25 more"}, // short terminal: still the floor
		{40, "/p/12", "/p/13", "+17 more"}, // 40 - 1 - 4 - 8 = 27 lines: thirteen projects
		{80, "/p/29", "", ""},              // room for all thirty
	} {
		m := onTab(projectsModel(t), usageTabWeek)
		m.height = c.height
		m.usageInfo.Projects = manyProjects(30)
		section := strings.Join(m.usageScopeSection(usageTabWeek), "\n")
		if !strings.Contains(section, c.last) || (c.missing != "" && strings.Contains(section, c.missing)) {
			t.Errorf("height %d: not bounded after %s:\n%s", c.height, c.last, stripANSI(section))
		}
		if c.more != "" && !strings.Contains(section, c.more) {
			t.Errorf("height %d: no %q:\n%s", c.height, c.more, stripANSI(section))
		}
		if c.more == "" && strings.Contains(section, "more") {
			t.Errorf("height %d: a more line with everything drawn:\n%s", c.height, stripANSI(section))
		}
		// And at a real height the whole popup fits the screen: nothing is clipped
		// by fitPopup, whose mark is a lone ellipsis line.
		if c.height >= 40 {
			if out := stripANSI(m.usageView()); strings.Contains(out, "\n │   …") || lipgloss.Height(m.usageView()) > c.height-1 {
				t.Errorf("height %d: the popup overflows (%d lines)", c.height, lipgloss.Height(m.usageView()))
			}
		}
	}
}

// The zh-TW cockpit draws the tabs and the table in its own words, the format
// strings with their arguments in place.
func TestTheProjectTabsInZhTW(t *testing.T) {
	m := projectsModel(t)
	m.lang = i18n.ZhTW
	for _, c := range []struct {
		tab  usageTab
		want []string
	}{
		{usageTabSession, []string{"帳號  ·  工作階段  ·  本週", "專案", "占比", "token", "費用", "本窗口 · 重置於 4:00:00", "tab 切換"}},
		{usageTabWeek, []string{"本週：自 07-06 14:00 起", "費用"}},
	} {
		out := stripANSI(onTab(m, c.tab).usageView())
		for _, want := range c.want {
			if !strings.Contains(out, want) {
				t.Errorf("zh-TW tab %d is missing %q:\n%s", c.tab, want, out)
			}
		}
		if strings.Contains(out, "%!") {
			t.Errorf("a format string lost its argument:\n%s", out)
		}
	}
	idle := m
	idle.usageInfo.Projects = []proto.ProjectUsage{{Project: "/srv/api", Vendor: "claude", WeekTokens: 5}}
	if out := stripANSI(onTab(idle, usageTabSession).usageView()); !strings.Contains(out, "本窗口還沒有任何用量") {
		t.Errorf("zh-TW has no reason for an idle window:\n%s", out)
	}
	m.usageInfo.Projects = nil
	if out := stripANSI(onTab(m, usageTabWeek).usageView()); !strings.Contains(out, "沒有各專案的數字") {
		t.Errorf("zh-TW has no reason for missing projects:\n%s", out)
	}
}

// bigProjectsModel is projectsModel at a terminal width, with figures as long as
// a cell holds and names as long as a path gets.
func bigProjectsModel(t *testing.T, width int, lang i18n.Lang) model {
	m := projectsModel(t)
	m.width, m.lang = width, lang
	m.usageInfo.Projects = []proto.ProjectUsage{
		{Project: "/h/xrspace/scarlet/var/cache/worktrees/pr-review-darkhold/pr-4419", Vendor: "claude",
			SessionTokens: 123_456_789, SessionCostUSD: 1234.56, WeekTokens: 987_654_321, WeekCostUSD: 9876.54},
		{Project: "/h/xrspace/scarlet/var/cache/worktrees/pr-review-darkhold/pr-4419", Vendor: "grok",
			SessionTokens: 512, WeekTokens: 1_200_000_000},
		{Project: "/h/xrspace/scarlet/var/cache/worktrees/pr-review-darkhold/pr-4420", Vendor: "claude",
			SessionTokens: 5_000, WeekTokens: 555_000_000, WeekCostUSD: 12345.67},
		{Project: "(unresolved) -Users-me-scarlet-var-cache-worktrees-pr-review-darkhold-pr-4421", Vendor: "claude",
			SessionTokens: 40_000, SessionCostUSD: 0.4, WeekTokens: 70_000, WeekCostUSD: 0.7},
	}
	return m
}

// fieldEnds is the display column just past each whitespace-separated field of a
// plain line, left to right.
func fieldEnds(line string) []int {
	var ends []int
	in := false
	for i, r := range line {
		if unicode.IsSpace(r) {
			if in {
				ends = append(ends, lipgloss.Width(line[:i]))
			}
			in = false
			continue
		}
		in = true
	}
	if in {
		ends = append(ends, lipgloss.Width(line))
	}
	return ends
}

// Every figure column's right edge is the same display cell on every line of the
// table — header, project totals, agent sub-rows and buckets — in both
// languages and at every width the layout changes shape at; and the header's
// name, the scope note and every project name share one left edge, with the
// mark in the two cells before it.
//
// The figures are read off the finished, colour-stripped screen line and
// measured independently of the code that placed them: the last two or three
// fields of each line are its figures, whatever the layout meant them to be.
func TestTheProjectColumnsLineUp(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhTW} {
		for _, width := range []int{100, 80, 60, 46} {
			for _, tab := range []usageTab{usageTabSession, usageTabWeek} {
				m := bigProjectsModel(t, width, lang)
				l := m.projectLayout()
				figures := 2
				if l.cost {
					figures = 3
				}
				section := m.usageScopeSection(tab)
				// The note, then the header, then the rows; the more line never appears
				// with four projects.
				var want []int
				for i, line := range section[1:] {
					plain := stripANSI(line)
					ends := fieldEnds(plain)
					if len(ends) < figures {
						t.Fatalf("%s w%d tab %d: line %d has %d fields: %q", lang, width, tab, i, len(ends), plain)
					}
					got := ends[len(ends)-figures:]
					if want == nil {
						want = got
						continue
					}
					if fmt.Sprint(got) != fmt.Sprint(want) {
						t.Errorf("%s w%d tab %d: figure edges %v, header's %v:\n%s", lang, width, tab, got, want,
							stripANSI(strings.Join(section, "\n")))
					}
				}
				// Left edges: every name starts two cells in, after the mark or its gap.
				for i, line := range section {
					plain := stripANSI(line)
					lead := strings.TrimLeft(strings.TrimPrefix(plain, "▸"), " ")
					indent := lipgloss.Width(plain) - lipgloss.Width(lead)
					if strings.HasPrefix(plain, "    ") {
						indent -= 2 // an agent row sits two cells in under its project
					}
					if indent != 2 {
						t.Errorf("%s w%d tab %d: line %d starts at column %d, want 2: %q", lang, width, tab, i, indent, plain)
					}
				}
			}
		}
	}
}

// fieldsOf is the plain table lines' fields, flattened, for checking a figure
// was not cut.
func fieldsOf(section []string) []string {
	return strings.Fields(stripANSI(strings.Join(section, "\n")))
}

// At every width the table fits the popup with each figure whole; the name
// gives way first, then the bar, then the cost column — and never a number.
func TestTheProjectsTableFitsNarrowTerminals(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhTW} {
		for _, c := range []struct {
			width   int
			layout  projectLayout
			want    []string // whole fields
			missing []string
		}{
			{100, projectLayout{name: 40, bar: 16, cost: true}, []string{"123.5M", "$1234.56", "512", "5.0K"}, nil},
			{80, projectLayout{name: 26, bar: 16, cost: true}, []string{"123.5M", "$1234.56", "512", "5.0K"}, nil},
			{60, projectLayout{name: 12, bar: 10, cost: true}, []string{"123.5M", "$1234.56", "512", "5.0K"}, nil},
			{46, projectLayout{name: 21}, []string{"123.5M", "512", "5.0K"}, []string{"$", "▓", "░"}},
		} {
			m := bigProjectsModel(t, c.width, lang)
			if got := m.projectLayout(); got != c.layout {
				t.Errorf("%s at %d: layout %+v, want %+v", lang, c.width, got, c.layout)
			}
			section := m.usageScopeSection(usageTabSession)
			for _, line := range section {
				if w := lipgloss.Width(line); w > m.popupWidth() {
					t.Errorf("%s at %d: a %d-cell line in a %d-cell popup: %q", lang, c.width, w, m.popupWidth(), stripANSI(line))
				}
			}
			fields := strings.Join(fieldsOf(section), "|")
			for _, w := range c.want {
				if !strings.Contains("|"+fields+"|", "|"+w+"|") {
					t.Errorf("%s at %d: %q is not a whole field in\n%s", lang, c.width, w, stripANSI(strings.Join(section, "\n")))
				}
			}
			for _, w := range c.missing {
				if strings.Contains(stripANSI(strings.Join(section[1:], "\n")), w) {
					t.Errorf("%s at %d: %q should not be drawn:\n%s", lang, c.width, w, stripANSI(strings.Join(section, "\n")))
				}
			}
		}
	}
}

// A row too wide even for the narrowest layout loses whole cells, ending in an
// ellipsis, never part of one.
func TestARowTooWideLosesWholeCells(t *testing.T) {
	m := bigProjectsModel(t, 30, i18n.EN)
	whole := map[string]bool{"123.5M": true, "5.0K": true, "40.0K": true, "512": true}
	for _, line := range m.usageScopeSection(usageTabSession) {
		plain := stripANSI(line)
		if strings.HasPrefix(plain, "▸") && !strings.HasSuffix(plain, "…") {
			t.Errorf("a row that lost cells does not say so: %q", plain)
		}
		if lipgloss.Width(line) > m.popupWidth() {
			t.Errorf("a %d-cell line in a %d-cell popup: %q", lipgloss.Width(line), m.popupWidth(), plain)
		}
		for _, f := range strings.Fields(plain) {
			if strings.ContainsAny(f[len(f)-1:], "KMB0123456789") && strings.ContainsAny(f[:1], "0123456789") && !whole[f] {
				t.Errorf("a figure was cut: %q in %q", f, plain)
			}
		}
	}
}

// Two worktrees of one repository share everything but their last segment, and
// a clipped label keeps that segment.
func TestLongLabelsKeepTheirTail(t *testing.T) {
	m := bigProjectsModel(t, 80, i18n.EN)
	lines := strings.Split(stripANSI(strings.Join(m.usageScopeSection(usageTabWeek), "\n")), "\n")
	if lineWith(lines, "…", "pr-4419") < 0 || lineWith(lines, "…", "pr-4420") < 0 {
		t.Errorf("the clipped labels lost their distinguishing tail:\n%s", strings.Join(lines, "\n"))
	}
	if got := clipLeft("~/a/b/pr-review/pr-4419", 12); lipgloss.Width(got) != 12 || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "/pr-4419") {
		t.Errorf("clipLeft = %q (%d cells), want 12 cells ending in the tail", got, lipgloss.Width(got))
	}
	if got := clipLeft("代理/專案/尾巴", 7); lipgloss.Width(got) > 7 || !strings.HasSuffix(got, "尾巴") {
		t.Errorf("clipLeft over wide runes = %q (%d cells)", got, lipgloss.Width(got))
	}
}

// A billion-token figure fits its column with its cost beside it.
func TestABillionTokenRowKeepsItsCost(t *testing.T) {
	m := bigProjectsModel(t, 80, i18n.EN)
	m.usageInfo.Projects = []proto.ProjectUsage{{Project: "/srv/big", Vendor: "claude",
		SessionTokens: 1_234_000_000, SessionCostUSD: 4321, WeekTokens: 9_876_000_000, WeekCostUSD: 9876.54}}
	for tab, want := range map[usageTab][]string{usageTabSession: {"1.2B", "$4321.00"}, usageTabWeek: {"9.9B", "$9876.54"}} {
		lines := strings.Split(stripANSI(strings.Join(m.usageScopeSection(tab), "\n")), "\n")
		if lineWith(lines, append(want, "/srv/big")...) < 0 {
			t.Errorf("%q is not on the row in\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

// Adjacent cells are two spaces apart even when both are full: a full name and
// the bar after it, two full figures, or two full header words would otherwise
// read as one run.
func TestCellsAreTwoSpacesApart(t *testing.T) {
	m := bigProjectsModel(t, 80, i18n.EN)
	out := stripANSI(strings.Join(m.usageScopeSection(usageTabWeek), "\n"))
	for _, want := range []string{"pr-4420  ▓", "555.0M  $12345.67", "share  tokens"} {
		if !strings.Contains(out, want) {
			t.Errorf("no two-space gap at %q in\n%s", want, out)
		}
	}
}

// A bucket keeps its marker whole and clips what follows from the left, so a
// long unresolved directory still reads as unresolved and still ends in the
// part that tells two of them apart.
func TestABucketKeepsItsMarker(t *testing.T) {
	m := bigProjectsModel(t, 80, i18n.EN)
	out := stripANSI(strings.Join(m.usageScopeSection(usageTabWeek), "\n"))
	if !strings.Contains(out, "(unresolved) …") || !strings.Contains(out, "-pr-4421 ") {
		t.Errorf("the bucket lost its marker or its tail:\n%s", out)
	}
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"(other)", 12, "(other)"},
		{"(unresolved) abcdefghij", 16, "(unresolved) …ij"},
		{"(unresolved) abcdefghij", 13, "(unresolved)…"}, // no room for any of the tail: clipped as text
		{"~/a/b/c/pr-4419", 10, "…c/pr-4419"},
	} {
		if got := clipLabel(c.in, c.n); got != c.want || lipgloss.Width(got) > c.n {
			t.Errorf("clipLabel(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// The table's figure forms: a bare token count, a share that never claims 0% of
// a scope it spent in, and a cost that is a dash when unpriced and drops its
// cents before it would outgrow its column.
func TestTheProjectFigureForms(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 512: "512", 5_000: "5.0K", 300_000: "300.0K", 999_949: "999.9K", 999_960: "1.0M", 1_234_567: "1.2M", 999_960_000: "1.0B", 1_200_000_000: "1.2B"} {
		if got := bareTokens(n); got != want {
			t.Errorf("bareTokens(%d) = %q, want %q", n, got, want)
		}
	}
	if got := humanTokens(300_000); got != "300.0K tok" {
		t.Errorf("humanTokens changed: %q", got)
	}
	for f, want := range map[float64]string{0: "0%", 0.001: "<1%", 0.005: "<1%", 0.006: "1%", 0.5: "50%", 0.994: "99%", 0.996: "99%", 1: "100%"} {
		if got := shareCell(f); got != want {
			t.Errorf("shareCell(%v) = %q, want %q", f, got, want)
		}
	}
	if got := shareOf(5, 0); got != 0 {
		t.Errorf("shareOf over nothing = %v", got)
	}
	for c, want := range map[float64]string{0: "-", 3: "$3.00", 99999.99: "$99999.99", 123456.78: "$123457"} {
		if got := stripANSI(costCell(c)); got != want {
			t.Errorf("costCell(%v) = %q, want %q", c, got, want)
		}
	}
}
