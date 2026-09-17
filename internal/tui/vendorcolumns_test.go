package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// See the note at the top of vendorusage_test.go: lipgloss strips colour without
// a TTY, so nothing here asserts on a colour through a rendered string.

// quotaModel is a cockpit whose vendor roll has both halves to draw: a limits
// reading for the account, and a fleet of panels to attribute.
func quotaModel(vendors []proto.VendorUsage, lim *proto.LimitsInfo, panels []panel.Panel, spend map[string]proto.PanelUsage) model {
	return model{
		defaultAgent: "claude",
		now:          time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		usageMode:    usageWindow,
		fleet:        panels,
		usageInfo:    &proto.UsageInfo{Vendors: vendors, Limits: lim, Panels: spend},
	}
}

// rowFor is the roll's row for one vendor, by name.
func rowFor(t *testing.T, rows []string, vendor string) string {
	t.Helper()
	for _, r := range rows {
		if strings.Contains(r, vendor) {
			return r
		}
	}
	t.Fatalf("no row for %q in:\n%s", vendor, strings.Join(rows, "\n"))
	return ""
}

// The columns say what is LEFT. The bars above already say what is gone, and a
// row read for "can I start another agent here" is answered by the headroom —
// a cell showing 38% where 62% remains is not a smaller number, it is the
// opposite reading.
func TestQuotaColumnsShowWhatIsLeftNotWhatIsSpent(t *testing.T) {
	m := quotaModel(
		[]proto.VendorUsage{{Vendor: "claude", State: "reading"}},
		&proto.LimitsInfo{
			FiveHour: &proto.LimitWindow{UsedPercent: 38},
			SevenDay: &proto.LimitWindow{UsedPercent: 29},
		}, nil, nil)
	row := rowFor(t, m.usageVendorSection(), "claude")
	for _, want := range []string{"62%", "71%"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q does not carry %s remaining", row, want)
		}
	}
	for _, spent := range []string{"38%", "29%"} {
		if strings.Contains(row, spent) {
			t.Errorf("row %q shows the share spent (%s) where the share left belongs", row, spent)
		}
	}
}

// A quota is the vendor's own statement about its own account, and the only one
// baton holds is the Anthropic reading. Lending it to the row below would publish
// a ceiling grok has never named — and would do it in the most convincing place
// on the screen, a column of figures that all look alike.
func TestOnlyTheVendorTheLimitsBelongToGetsQuotaColumns(t *testing.T) {
	m := quotaModel(
		[]proto.VendorUsage{
			{Vendor: "claude", State: "reading"},
			{Vendor: "grok", State: "reading"},
		},
		&proto.LimitsInfo{
			FiveHour: &proto.LimitWindow{UsedPercent: 38},
			SevenDay: &proto.LimitWindow{UsedPercent: 29},
		},
		[]panel.Panel{{ID: "1", Profile: "grok"}},
		map[string]proto.PanelUsage{"1": {Tokens: 210_000}})

	rows := m.usageVendorSection()
	grok := rowFor(t, rows, "grok")
	for _, borrowed := range []string{"62%", "71%", "38%", "29%"} {
		if strings.Contains(grok, borrowed) {
			t.Errorf("grok's row carries %s, a share of a ceiling grok never published: %q", borrowed, grok)
		}
	}
	// It keeps the column that IS its own: what the fleet spent running it.
	if !strings.Contains(grok, "210.0K tok") {
		t.Errorf("grok's row lost its own spend: %q", grok)
	}
	if claude := rowFor(t, rows, "claude"); !strings.Contains(claude, "62%") {
		t.Errorf("the vendor the reading belongs to has no quota column: %q", claude)
	}
}

// Every readable agent counts down to its own reset, because "62% left" and "62%
// left, and it refills in four minutes" are different decisions — and because an
// agent with no quota at all still has a window its figure rolls over on.
//
// The two instants come from different places and must not be swapped: claude's
// is the quota's own reset, grok's is the end of the window baton measured its
// spend over. Each row carries its own.
func TestEveryReadableAgentCountsDownToItsOwnReset(t *testing.T) {
	quotaReset := time.Date(2026, 9, 6, 14, 14, 31, 0, time.UTC).Format(time.RFC3339)
	grokWindow := time.Date(2026, 9, 6, 13, 2, 33, 0, time.UTC).Format(time.RFC3339)
	m := quotaModel([]proto.VendorUsage{
		{Vendor: "claude", State: "reading", Tokens: 1_200_000},
		{Vendor: "grok", State: "reading", Tokens: 58_000_000, Windows: []proto.VendorWindow{
			{Label: "window", UsedPercent: 40, ResetsAt: grokWindow},
		}},
	}, &proto.LimitsInfo{FiveHour: &proto.LimitWindow{UsedPercent: 38, ResetsAt: quotaReset}}, nil, nil)

	rows := m.usageVendorSection()
	claude, grok := rowFor(t, rows, "claude"), rowFor(t, rows, "grok")
	if !strings.Contains(claude, "2:14:31") {
		t.Errorf("claude's row does not count down to its quota's reset: %q", claude)
	}
	if !strings.Contains(grok, "1:02:33") {
		t.Errorf("grok has a window of its own and its row does not count down to it: %q", grok)
	}
	if strings.Contains(grok, "2:14:31") {
		t.Errorf("grok's row borrowed the account's reset: %q", grok)
	}
	if strings.Contains(claude, "1:02:33") {
		t.Errorf("claude's row shows grok's window: %q", claude)
	}
}

// Grok publishes a weekly credit pool and no five-hour throttle. Its 7d cell
// fills from that pool; its 5h cell stays a dash; Anthropic's numbers stay on
// claude's row.
func TestGrokWeekQuotaFillsTheWeekColumnNotTheSessionOne(t *testing.T) {
	quotaReset := time.Date(2026, 9, 6, 14, 14, 31, 0, time.UTC).Format(time.RFC3339)
	weekReset := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	m := quotaModel([]proto.VendorUsage{
		{Vendor: "claude", State: "reading", Tokens: 1_200_000},
		{Vendor: "grok", State: "reading", Tokens: 58_000_000, Windows: []proto.VendorWindow{
			{Label: "7d", UsedPercent: 36, ResetsAt: weekReset},
			{Label: "window", UsedPercent: 40, ResetsAt: time.Date(2026, 9, 6, 13, 2, 33, 0, time.UTC).Format(time.RFC3339)},
		}},
	}, &proto.LimitsInfo{
		FiveHour: &proto.LimitWindow{UsedPercent: 38, ResetsAt: quotaReset},
		SevenDay: &proto.LimitWindow{UsedPercent: 29, ResetsAt: quotaReset},
	}, nil, nil)

	rows := m.usageVendorSection()
	grok := rowFor(t, rows, "grok")
	if !strings.Contains(grok, "64%") {
		t.Errorf("grok's week cell is not what is left of its own pool: %q", grok)
	}
	for _, borrowed := range []string{"62%", "71%", "38%", "29%", "36%", "40%"} {
		if strings.Contains(grok, borrowed) {
			t.Errorf("grok's row carries %s, which is not its remaining week share: %q", borrowed, grok)
		}
	}
	if strings.Contains(grok, "2:14:31") {
		t.Errorf("grok's row borrowed claude's reset: %q", grok)
	}
	if !strings.Contains(grok, "3d") {
		t.Errorf("grok's row does not count down to its own week reset: %q", grok)
	}
	if !strings.Contains(grok, "58.0M tok") {
		t.Errorf("grok's spent column was lost: %q", grok)
	}
	claude := rowFor(t, rows, "claude")
	if !strings.Contains(claude, "62%") || !strings.Contains(claude, "71%") {
		t.Errorf("claude's quota columns moved: %q", claude)
	}
	if strings.Contains(claude, "64%") {
		t.Errorf("grok's week landed on claude: %q", claude)
	}
}

// A readable vendor that has stated no window gets the mark. A countdown baton
// invented would be worse than none — it is a promise about when a number will
// change, made on behalf of a vendor that never made it.
func TestAVendorThatStatesNoWindowGetsNoCountdown(t *testing.T) {
	m := quotaModel([]proto.VendorUsage{{Vendor: "grok", State: "reading", Tokens: 5}},
		&proto.LimitsInfo{FiveHour: &proto.LimitWindow{
			UsedPercent: 38,
			ResetsAt:    time.Date(2026, 9, 6, 14, 14, 31, 0, time.UTC).Format(time.RFC3339),
		}}, nil, nil)
	if row := rowFor(t, m.usageVendorSection(), "grok"); strings.Contains(row, ":") {
		t.Errorf("row %q carries a countdown nobody stated", row)
	}
}

// The panel column is baton's own attribution, and it is grouped by the profile
// each panel was spawned from — not by the fleet default, which would pile every
// agent's spend onto one name.
func TestThePanelColumnGroupsSpendByTheAgentThePanelRuns(t *testing.T) {
	m := quotaModel(
		[]proto.VendorUsage{
			{Vendor: "claude", State: "reading"},
			{Vendor: "grok", State: "reading"},
		}, nil,
		[]panel.Panel{
			{ID: "1", Profile: "claude"},
			{ID: "2", Profile: "claude"},
			{ID: "3", Profile: "grok"},
		},
		map[string]proto.PanelUsage{
			"1": {Tokens: 800_000},
			"2": {Tokens: 400_000},
			"3": {Tokens: 210_000},
		})

	rows := m.usageVendorSection()
	claude := rowFor(t, rows, "claude")
	if !strings.Contains(claude, "1.2M tok") {
		t.Errorf("claude's row does not sum its two panels: %q", claude)
	}
	if !strings.Contains(claude, "2 panels") {
		t.Errorf("claude's row does not count its panels: %q", claude)
	}
	grok := rowFor(t, rows, "grok")
	if strings.Contains(grok, "1.2M") {
		t.Errorf("claude's spend landed on grok's row: %q", grok)
	}
	if !strings.Contains(grok, "1 panel") || strings.Contains(grok, "1 panels") {
		t.Errorf("grok's row does not count its one panel in the singular: %q", grok)
	}
}

// A panel that names no profile is attributed to nobody. Charging it to the fleet
// default would put tokens nobody can trace under a name — in the one column of
// the row baton is the sole author of.
func TestAProfilelessPanelIsChargedToNobody(t *testing.T) {
	m := quotaModel(
		[]proto.VendorUsage{{Vendor: "claude", State: "reading"}}, nil,
		[]panel.Panel{{ID: "1"}},
		map[string]proto.PanelUsage{"1": {Tokens: 900_000}})
	if row := rowFor(t, m.usageVendorSection(), "claude"); strings.Contains(row, "900.0K") {
		t.Errorf("a panel with no profile was charged to the default agent: %q", row)
	}
}

// A vendor with no reading is one short mark, not four empty columns and not the
// daemon's sentence. What it must never grow is a figure: a percentage on this
// row would be the account's, borrowed by an agent that has never published one.
func TestARowWithNoReadingGrowsNoColumns(t *testing.T) {
	m := quotaModel([]proto.VendorUsage{
		{Vendor: "codex", State: "no-source", Reason: "baton has no usage source for this agent"},
	}, &proto.LimitsInfo{FiveHour: &proto.LimitWindow{UsedPercent: 38}}, nil, nil)

	row := rowFor(t, m.usageVendorSection(), "codex")
	if !strings.Contains(row, noReadingCell) {
		t.Errorf("row %q does not mark itself as having no reading", row)
	}
	if strings.Contains(row, "baton has no usage source") {
		t.Errorf("row %q spells out a reason that is the mark's job and the footer's", row)
	}
	if strings.Contains(row, "%") {
		t.Errorf("row %q acquired a percentage from a reading that is not its own", row)
	}
	// The mark stands in for a whole row, so it must not be read as one column that
	// happens to be unknown.
	if noReadingCell == unknownCell {
		t.Error("a row with no reading is indistinguishable from a single unknown cell")
	}
}

// The state a row is in still has to be readable without colour, because the two
// states that carry no figure now share the same text. The mark is what separates
// them, and a pipe or a colour-blind reader has only the mark.
func TestTheMarkStillSeparatesTheTwoStatesThatShowNothing(t *testing.T) {
	m := quotaModel([]proto.VendorUsage{
		{Vendor: "codex", State: "no-source", Reason: "baton has no usage source for this agent"},
		{Vendor: "gemini", State: "absent", Reason: "not installed on the fleet's machine"},
	}, nil, nil, nil)

	rows := m.usageVendorSection()
	codex, gemini := rowFor(t, rows, "codex"), rowFor(t, rows, "gemini")
	if strings.TrimPrefix(codex, "codex") == strings.TrimPrefix(gemini, "gemini") {
		t.Errorf("an installed agent baton cannot read and one that is not there render alike:\n%s\n%s", codex, gemini)
	}
}

// The regression this column exists to stop (#111): grok had been running all
// day, and the roll showed three dashes. Attribution needs a session id, and
// withSessionID hands one to Claude Code alone — so for every other agent the
// panel column can never be filled, and a row with nothing else on it says
// nothing about an agent baton has read the books of.
func TestAVendorBatonCannotAttributeStillShowsWhatItSpent(t *testing.T) {
	m := quotaModel(
		[]proto.VendorUsage{{Vendor: "grok", State: "reading", Tokens: 58_000_000, CostUSD: 8.08}},
		&proto.LimitsInfo{FiveHour: &proto.LimitWindow{UsedPercent: 38}},
		nil, nil) // no panel of grok's can ever reach UsageInfo.Panels

	row := rowFor(t, m.usageVendorSection(), "grok")
	if !strings.Contains(row, "58.0M tok") {
		t.Errorf("grok's row carries no figure at all: %q", row)
	}
	if strings.Contains(row, "62%") {
		t.Errorf("grok's row borrowed the account's quota: %q", row)
	}
}

// The two token figures are different measurements and the row keeps them apart.
// The vendor's reader sees every session on the machine; the panel column sees
// only what the fleet spawned, and on a machine where the same agent is also run
// from another terminal the gap between them is itself the reading.
func TestTheVendorsOwnReadingAndTheFleetsAttributionAreSeparateColumns(t *testing.T) {
	m := quotaModel(
		[]proto.VendorUsage{{Vendor: "claude", State: "reading", Tokens: 1_200_000}}, nil,
		[]panel.Panel{{ID: "1", Profile: "claude"}},
		map[string]proto.PanelUsage{"1": {Tokens: 800_000}})

	row := rowFor(t, m.usageVendorSection(), "claude")
	for _, want := range []string{"1.2M tok", "800.0K tok", "1 panel"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q is missing %q — the two readings are not both on it", row, want)
		}
	}
}

// A reader that looked and found an empty window is a true zero, but it shares a
// column with agents whose figure is a real total. "0 tok" there reads as a claim
// about the agent rather than about the window.
func TestAVendorWithNothingSpentShowsAMarkNotAZero(t *testing.T) {
	cell := vendorSpentCell(proto.VendorUsage{Vendor: "claude", State: "reading"})
	if strings.Contains(cell, "0") {
		t.Errorf("spent cell = %q, want a mark rather than a count of zero", cell)
	}
	if strings.TrimSpace(cell) == "" {
		t.Error("spent cell rendered blank; it is indistinguishable from a vendor with no reader")
	}
}
