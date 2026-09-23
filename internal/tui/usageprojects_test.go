package tui

import (
	"fmt"
	"strings"
	"testing"

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

// p turns the lower section to the projects and back, stays inside the overlay,
// and the legend names where p goes next.
func TestPTogglesTheLowerSection(t *testing.T) {
	m := projectsModel(t)
	if out := stripANSI(m.usageView()); !strings.Contains(out, "Burning this window") || !strings.Contains(out, "p projects") {
		t.Fatalf("the overlay did not open on the roster with p offering projects:\n%s", out)
	}
	on, _ := m.handleUsageKey("p")
	pm := on.(model)
	out := stripANSI(pm.usageView())
	if pm.mode != modeUsage || !pm.usageProjects {
		t.Fatalf("p left mode=%v projects=%v", pm.mode, pm.usageProjects)
	}
	if strings.Contains(out, "Burning this window") || !strings.Contains(out, "Project") || !strings.Contains(out, "p panels") {
		t.Errorf("projects mode still shows the roster, or the legend does not offer the panels:\n%s", out)
	}
	off, _ := pm.handleUsageKey("p")
	if off.(model).usageProjects {
		t.Error("a second p did not turn the projects off")
	}
	// Closing and reopening the overlay lands where it was left.
	closed, _ := pm.closeUsage()
	if !closed.(model).openUsage(modeDashboard).usageProjects {
		t.Error("reopening the overlay forgot the projects view")
	}
}

// The table keeps the daemon's order, one total per project with its agents
// indented under it, the home directory shortened, and the total summing the
// agents.
func TestTheProjectsTableGroupsByProject(t *testing.T) {
	m := projectsModel(t)
	m.usageProjects = true
	lines := strings.Split(stripANSI(m.usageView()), "\n")

	baton := lineWith(lines, "▸ ~/mylab/baton", "300.0K tok · $3.00", "1.2M tok · $22.00")
	claude := lineWith(lines, "    claude", "300.0K tok · $3.00", "1.0M tok · $20.00")
	grok := lineWith(lines, "    grok", "200.0K tok · $2.00")
	api := lineWith(lines, "▸ /srv/api", "5.0K tok", "90.0K tok · $1.00")
	other := lineWith(lines, "(other)", "4.0K tok")
	if baton < 0 || claude != baton+1 || grok != baton+2 || api != baton+3 || other != api+2 {
		t.Errorf("rows out of place: baton %d, claude %d, grok %d, api %d, other %d\n%s",
			baton, claude, grok, api, other, strings.Join(lines, "\n"))
	}
	// A scope that saw nothing is a dash, not "0 tok".
	if g := lines[max(grok, 0)]; strings.Contains(g, "0 tok") {
		t.Errorf("grok's empty window drew a zero: %q", g)
	}
	if strings.Contains(strings.Join(lines, "\n"), "/h/mylab") {
		t.Error("the home directory was not shortened")
	}
}

// Buckets are drawn muted; a real project's name is not.
func TestBucketRowsAreMuted(t *testing.T) {
	// A test binary has no TTY, so lipgloss renders plain text by default and
	// every style would compare equal to none.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	m := projectsModel(t)
	m.usageProjects = true
	out := m.usageView()
	if !strings.Contains(out, mutedStyle.Render(pad(proto.ProjectOther, projectNameWidth))) {
		t.Error("(other) is not muted")
	}
	if strings.Contains(out, mutedStyle.Render(pad("/srv/api", projectNameWidth))) {
		t.Error("a real project was drawn muted")
	}
	for _, label := range []string{"(temporary)", "(unresolved) -p-x", "(unattributed)"} {
		m.usageInfo.Projects = []proto.ProjectUsage{{Project: label, Vendor: "claude", WeekTokens: 1}}
		if !strings.Contains(m.usageView(), mutedStyle.Render(pad(label, projectNameWidth))) {
			t.Errorf("%s is not muted", label)
		}
	}
}

// The week header says what the week column measures: one phrase when the
// agents agree, per agent when they do not, and nothing about an agent that has
// no row in the table.
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
		m := projectsModel(t)
		m.usageProjects = true
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

// No project rows is a muted reason, never an empty table — with a quota reading
// or without one.
func TestNoProjectsSaysWhy(t *testing.T) {
	for _, withLimits := range []bool{true, false} {
		m := projectsModel(t)
		m.usageProjects = true
		m.usageInfo.Projects = nil
		if !withLimits {
			m.usageInfo.Limits = nil
		}
		out := stripANSI(m.usageView())
		if !strings.Contains(out, "no per-project figures") || strings.Contains(out, "Project ") {
			t.Errorf("limits %v: nil projects did not read as a reason:\n%s", withLimits, out)
		}
	}
	// And without a quota reading the table itself still draws.
	m := projectsModel(t)
	m.usageProjects = true
	m.usageInfo.Limits = nil
	if out := stripANSI(m.usageView()); !strings.Contains(out, "~/mylab/baton") {
		t.Errorf("the projects did not draw without a quota reading:\n%s", out)
	}
}

// The table is bounded: whole projects only, and a line saying how many more.
func TestTheProjectsTableIsBounded(t *testing.T) {
	m := projectsModel(t)
	m.usageProjects = true
	m.usageInfo.Projects = nil
	for i := range 12 {
		m.usageInfo.Projects = append(m.usageInfo.Projects,
			proto.ProjectUsage{Project: fmt.Sprintf("/p/%02d", i), Vendor: "claude", WeekTokens: int64(100 - i)})
	}
	out := stripANSI(m.usageView())
	// Two lines a project against a bound of 10: five drawn, seven more.
	if !strings.Contains(out, "/p/04") || strings.Contains(out, "/p/05") || !strings.Contains(out, "+7 more") {
		t.Errorf("the table was not bounded at five projects with +7 more:\n%s", out)
	}
}

// The zh-TW cockpit draws the table in its own words, the format strings with
// their arguments in place.
func TestTheProjectsTableInZhTW(t *testing.T) {
	m := projectsModel(t)
	m.lang = i18n.ZhTW
	m.usageProjects = true
	out := stripANSI(m.usageView())
	for _, want := range []string{"專案", "工作階段", "本週：自 07-06 14:00 起", "p 面板"} {
		if !strings.Contains(out, want) {
			t.Errorf("zh-TW overlay is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "%!") {
		t.Errorf("a format string lost its argument:\n%s", out)
	}
	m.usageInfo.Projects = nil
	if out := stripANSI(m.usageView()); !strings.Contains(out, "沒有各專案的數字") {
		t.Errorf("zh-TW has no reason for missing projects:\n%s", out)
	}
}

// bigProjectsModel is projectsModel at a terminal width, with figures as long as
// a cell holds and names as long as a path gets.
func bigProjectsModel(t *testing.T, width int, lang i18n.Lang) model {
	m := projectsModel(t)
	m.width, m.lang, m.usageProjects = width, lang, true
	m.usageInfo.Projects = []proto.ProjectUsage{
		{Project: "/h/xrspace/scarlet/var/cache/worktrees/pr-review-darkhold/pr-4419", Vendor: "claude",
			SessionTokens: 123_456_789, SessionCostUSD: 1234.56, WeekTokens: 987_654_321, WeekCostUSD: 9876.54},
		{Project: "/h/xrspace/scarlet/var/cache/worktrees/pr-review-darkhold/pr-4420", Vendor: "claude",
			SessionTokens: 5_000, WeekTokens: 555_000_000, WeekCostUSD: 12345.67},
	}
	return m
}

// At 80 columns the table fits the popup with every figure whole, cost
// included; at 60 the cells drop their cost and keep their tokens whole. In
// neither is a number cut, in either language — and a cost too long for its
// cell ($12345.67) leaves the tokens standing alone rather than half a price.
func TestTheProjectsTableFitsNarrowTerminals(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhTW} {
		for _, c := range []struct {
			width   int
			want    []string
			missing []string
		}{
			{80, []string{"123.5M tok · $1234.56", "987.7M tok · $9876.54", "5.0K tok", "555.0M tok"}, []string{"$1234.5 ", "$12345", "$123 "}},
			{60, []string{"123.5M tok", "987.7M tok", "5.0K tok", "555.0M tok"}, []string{"$"}},
		} {
			m := bigProjectsModel(t, c.width, lang)
			section := m.usageProjectSection()
			for _, line := range section {
				if w := lipgloss.Width(line); w > m.popupWidth() {
					t.Errorf("%s at %d: a %d-cell line in a %d-cell popup: %q", lang, c.width, w, m.popupWidth(), stripANSI(line))
				}
			}
			out := stripANSI(strings.Join(section, "\n"))
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("%s at %d: %q is not whole in\n%s", lang, c.width, w, out)
				}
			}
			for _, w := range c.missing {
				if strings.Contains(out, w) {
					t.Errorf("%s at %d: %q should not be drawn:\n%s", lang, c.width, w, out)
				}
			}
		}
	}
}

// A row too wide even for the narrow layout loses whole cells, ending in an
// ellipsis, never part of one.
func TestARowTooWideLosesWholeCells(t *testing.T) {
	m := bigProjectsModel(t, 30, i18n.EN)
	for _, line := range m.usageProjectSection() {
		plain := stripANSI(line)
		if strings.HasPrefix(plain, "▸") && !strings.HasSuffix(plain, "…") {
			t.Errorf("a row that lost cells does not say so: %q", plain)
		}
		if lipgloss.Width(line) > m.popupWidth() {
			t.Errorf("a %d-cell line in a %d-cell popup: %q", lipgloss.Width(line), m.popupWidth(), plain)
		}
		// Any figure that made it in is whole: "tok" follows every token count.
		for _, f := range strings.Fields(plain) {
			if strings.HasSuffix(f, "M") || strings.HasSuffix(f, "K") {
				if !strings.Contains(plain, f+" tok") {
					t.Errorf("a figure was cut: %q in %q", f, plain)
				}
			}
		}
	}
}

// Two worktrees of one repository share everything but their last segment, and
// a clipped label keeps that segment.
func TestLongLabelsKeepTheirTail(t *testing.T) {
	m := bigProjectsModel(t, 80, i18n.EN)
	out := stripANSI(strings.Join(m.usageProjectSection(), "\n"))
	a, b := lineWith(strings.Split(out, "\n"), "…", "pr-4419"), lineWith(strings.Split(out, "\n"), "…", "pr-4420")
	if a < 0 || b < 0 {
		t.Errorf("the clipped labels lost their distinguishing tail:\n%s", out)
	}
	if got := clipLeft("~/a/b/pr-review/pr-4419", 12); lipgloss.Width(got) != 12 || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "/pr-4419") {
		t.Errorf("clipLeft = %q (%d cells), want 12 cells ending in the tail", got, lipgloss.Width(got))
	}
	if got := clipLeft("代理/專案/尾巴", 7); lipgloss.Width(got) > 7 || !strings.HasSuffix(got, "尾巴") {
		t.Errorf("clipLeft over wide runes = %q (%d cells)", got, lipgloss.Width(got))
	}
}

// A billion-token figure is short enough to keep its cost at 80 columns — the
// B tier is what makes it so; "1234.0M tok" would have pushed the cost out.
func TestABillionTokenRowKeepsItsCost(t *testing.T) {
	m := bigProjectsModel(t, 80, i18n.EN)
	m.usageInfo.Projects = []proto.ProjectUsage{{Project: "/srv/big", Vendor: "claude",
		SessionTokens: 1_234_000_000, SessionCostUSD: 4321, WeekTokens: 9_876_000_000, WeekCostUSD: 9876.54}}
	out := stripANSI(strings.Join(m.usageProjectSection(), "\n"))
	for _, want := range []string{"1.2B tok · $4321.00", "9.9B tok · $9876.54"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is not in\n%s", want, out)
		}
	}
}

// Adjacent cells are two spaces apart: a full name column and the figure after
// it, and two full figure cells, would otherwise read as one run of text.
func TestCellsAreTwoSpacesApart(t *testing.T) {
	m := bigProjectsModel(t, 80, i18n.EN)
	out := stripANSI(strings.Join(m.usageProjectSection(), "\n"))
	for _, want := range []string{"pr-4419  123.5M tok", "$1234.56  987.7M tok"} {
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
	m.usageInfo.Projects = []proto.ProjectUsage{{Project: "(unresolved) -Users-me-scarlet-var-cache-worktrees-pr-review-darkhold-pr-4419",
		Vendor: "claude", WeekTokens: 5}}
	out := stripANSI(strings.Join(m.usageProjectSection(), "\n"))
	if !strings.Contains(out, "(unresolved) …") || !strings.Contains(out, "-pr-4419 ") {
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
