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

// The cell counts down to the reset, because "62% left" and "62% left, and it
// refills in four minutes" are different decisions.
func TestQuotaCellCarriesTheCountdownToTheReset(t *testing.T) {
	m := quotaModel([]proto.VendorUsage{{Vendor: "claude", State: "reading"}}, &proto.LimitsInfo{
		FiveHour: &proto.LimitWindow{
			UsedPercent: 38,
			ResetsAt:    time.Date(2026, 9, 6, 14, 14, 31, 0, time.UTC).Format(time.RFC3339),
		},
	}, nil, nil)
	if row := rowFor(t, m.usageVendorSection(), "claude"); !strings.Contains(row, "2:14:31") {
		t.Errorf("row %q does not count down to the window's reset", row)
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

// A vendor with no reading keeps its one sentence. Three dashes under three
// headers would read as three separate findings about an agent whose whole story
// is that baton cannot see it.
func TestARowWithNoReadingGrowsNoColumns(t *testing.T) {
	m := quotaModel([]proto.VendorUsage{
		{Vendor: "codex", State: "no-source", Reason: "baton has no usage source for this agent"},
	}, &proto.LimitsInfo{FiveHour: &proto.LimitWindow{UsedPercent: 38}}, nil, nil)

	row := rowFor(t, m.usageVendorSection(), "codex")
	if !strings.Contains(row, "baton has no usage source for this agent") {
		t.Errorf("row %q lost the reason it has no figure", row)
	}
	if strings.Count(row, "—") > 0 {
		t.Errorf("row %q grew empty columns where one sentence belongs", row)
	}
	if strings.Contains(row, "%") {
		t.Errorf("row %q acquired a percentage from a reading that is not its own", row)
	}
}
