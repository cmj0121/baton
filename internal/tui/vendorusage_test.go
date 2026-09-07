package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// NOTE ON ASSERTIONS IN THIS FILE. lipgloss strips colour when there is no TTY,
// so every rendered string here comes back plain and an assertion of the form
// "the row is amber" would pass against any colour at all — including none. So
// the colour choice is tested through vendorMarkColor, which returns the colour
// itself, and the rendered views are tested only for text that is genuinely in
// them. Nothing below compares two strings that a stripped renderer makes equal.

func vendorModel(def string, vendors []proto.VendorUsage) model {
	return model{
		defaultAgent: def,
		now:          time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		usageMode:    usageWindow,
		usageInfo:    &proto.UsageInfo{Vendors: vendors},
	}
}

// The footer says whose reading it is. An unlabelled number invites being read as
// the fleet's whole spend.
func TestFooterNamesTheDefaultAgent(t *testing.T) {
	m := vendorModel("claude", []proto.VendorUsage{
		{Vendor: "claude", State: "reading", Tokens: 1_200_000, CostUSD: 12.34},
		{Vendor: "codex", State: "no-source", Reason: "baton has no usage source for this agent"},
	})
	got := m.usageSegment()
	if !strings.Contains(got, "claude") {
		t.Errorf("segment %q does not name the default agent", got)
	}
	if !strings.Contains(got, "1.2M tok") {
		t.Errorf("segment %q does not carry the reading", got)
	}
}

// The name follows the default agent rather than being hard-coded to claude.
// Without this the previous test passes for the wrong reason: "claude" also
// happens to be the string a hard-coded label would contain.
func TestFooterNamesWhicheverAgentIsDefault(t *testing.T) {
	m := vendorModel("grok", []proto.VendorUsage{
		{Vendor: "claude", State: "reading", Tokens: 999, CostUSD: 9.99},
		{Vendor: "grok", State: "reading", Tokens: 5_000_000, CostUSD: 3.21},
	})
	got := m.usageSegment()
	if !strings.Contains(got, "grok") {
		t.Errorf("segment %q does not name grok, the configured default", got)
	}
	if strings.Contains(got, "9.99") || strings.Contains(got, "999 tok") {
		t.Errorf("segment %q shows claude's figures under grok's name", got)
	}
	if !strings.Contains(got, "5.0M tok") {
		t.Errorf("segment %q does not carry grok's own reading", got)
	}
}

// A default agent baton cannot account for must not borrow another vendor's
// number. The segment says why instead.
func TestFooterRefusesToShowAFigureItDoesNotHave(t *testing.T) {
	m := vendorModel("codex", []proto.VendorUsage{
		{Vendor: "claude", State: "reading", Tokens: 4_000_000, CostUSD: 40},
		{Vendor: "codex", State: "no-source", Reason: "baton has no usage source for this agent"},
	})
	got := m.usageSegment()
	if strings.Contains(got, "4.0M") || strings.Contains(got, "40.00") {
		t.Errorf("segment %q captions claude's spend with codex's name", got)
	}
	if !strings.Contains(got, "codex") {
		t.Errorf("segment %q does not name the agent it is refusing to report", got)
	}
	if !strings.Contains(got, "no usage source") {
		t.Errorf("segment %q does not say why there is no figure", got)
	}
}

// An older daemon sends no vendor list. The footer must then behave exactly as it
// did before this feature — the pre-rendered string, unnamed — rather than
// blanking or naming an agent it cannot vouch for.
func TestFooterFallsBackWhenTheDaemonSentNoVendors(t *testing.T) {
	m := model{
		defaultAgent: "claude",
		now:          time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		usageMode:    usageWindow,
		usageText:    "1.2M tok · ≈$12.34 API",
		usageInfo:    &proto.UsageInfo{Tokens: 1_200_000},
	}
	got := m.usageSegment()
	if got != "1.2M tok · ≈$12.34 API" {
		t.Errorf("segment = %q, want the daemon's own string unchanged", got)
	}
}

// The countdown belongs to the vendor whose number is beside it.
func TestFooterCountsDownTheVendorsOwnWindow(t *testing.T) {
	m := vendorModel("grok", []proto.VendorUsage{{
		Vendor: "grok", State: "reading", Tokens: 100, CostUSD: 1,
		Windows: []proto.VendorWindow{{
			Label:       "window",
			UsedPercent: 50,
			ResetsAt:    time.Date(2026, 9, 6, 14, 30, 0, 0, time.UTC).Format(time.RFC3339),
		}},
	}})
	if got := m.usageSegment(); !strings.Contains(got, "2:30:00") {
		t.Errorf("segment %q does not count down to the vendor's own reset", got)
	}
}

// A window that has already run out is not a countdown of zero.
func TestFooterShowsNoCountdownForAClosedWindow(t *testing.T) {
	m := vendorModel("grok", []proto.VendorUsage{{
		Vendor: "grok", State: "reading", Tokens: 100, CostUSD: 1,
		Windows: []proto.VendorWindow{{
			Label:    "window",
			ResetsAt: time.Date(2026, 9, 6, 11, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}},
	}})
	if got := m.usageSegment(); strings.Contains(got, "0:00:00") {
		t.Errorf("segment %q counts down to a window that already closed", got)
	}
}

// The overlay lists every vendor the daemon reported, in all three states, and
// gives the two without a figure a reason rather than a bar.
func TestOverlayListsEveryVendorWithItsStanding(t *testing.T) {
	m := vendorModel("claude", []proto.VendorUsage{
		{Vendor: "claude", State: "reading", Tokens: 1_200_000, CostUSD: 12.34},
		{Vendor: "codex", State: "no-source", Reason: "baton has no usage source for this agent"},
		{Vendor: "gemini", State: "absent", Reason: "not installed on the fleet's machine"},
	})
	rows := m.usageVendorSection()
	if len(rows) != 4 { // a header and three vendors
		t.Fatalf("got %d rows, want 4 (header + 3 vendors)", len(rows))
	}
	joined := strings.Join(rows, "\n")
	for _, want := range []string{
		"claude", "codex", "gemini",
		"1.2M tok",
		"baton has no usage source for this agent",
		"not installed on the fleet's machine",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the vendor section does not mention %q:\n%s", want, joined)
		}
	}
	// The vendor with no source must not have acquired a number.
	for _, row := range rows {
		if strings.Contains(row, "codex") && strings.Contains(row, "tok") {
			t.Errorf("codex has no source but its row carries a token figure: %q", row)
		}
	}
}

// The three states must be visually separable without colour, because colour is
// exactly what a pipe, a log or a colour-blind reader loses.
func TestTheThreeStatesHaveThreeDifferentMarks(t *testing.T) {
	reading := vendorMark(proto.VendorUsage{State: "reading"})
	noSource := vendorMark(proto.VendorUsage{State: "no-source"})
	absent := vendorMark(proto.VendorUsage{State: "absent"})
	if reading == noSource || reading == absent || noSource == absent {
		t.Errorf("marks are not distinct: reading=%q no-source=%q absent=%q", reading, noSource, absent)
	}
}

// The colours are asserted on the colour value, not on a rendered string — a
// rendered string has had the colour stripped and would compare equal whatever
// was chosen. An installed agent baton cannot account for is the gap worth
// flagging; an agent nobody installed is not.
func TestOnlyTheRealGapIsFlagged(t *testing.T) {
	var m model
	if got := m.vendorMarkColor(proto.VendorUsage{State: "no-source"}); got != colAmber {
		t.Errorf("an installed agent with no usage source is %v, want amber — it is the gap", got)
	}
	if got := m.vendorMarkColor(proto.VendorUsage{State: "absent"}); got == colAmber {
		t.Error("an agent that is not installed is flagged amber; that is not a gap in baton's reporting")
	}
	if got := m.vendorMarkColor(proto.VendorUsage{State: "reading"}); got != colBrand {
		t.Errorf("a vendor with a reading is %v, want the brand colour", got)
	}
}

// The default agent is marked in the list, so the overlay and the footer visibly
// agree about which agent the headline number belongs to.
func TestOverlayMarksTheDefaultAgent(t *testing.T) {
	m := vendorModel("codex", []proto.VendorUsage{
		{Vendor: "claude", State: "reading", Tokens: 10, CostUSD: 1},
		{Vendor: "codex", State: "no-source", Reason: "no source"},
	})
	rows := m.usageVendorSection()
	var claudeRow, codexRow string
	for _, r := range rows {
		if strings.Contains(r, "claude") {
			claudeRow = r
		}
		if strings.Contains(r, "codex") {
			codexRow = r
		}
	}
	if !strings.Contains(codexRow, "*") {
		t.Errorf("the default agent's row is not marked: %q", codexRow)
	}
	if strings.Contains(claudeRow, "*") {
		t.Errorf("a non-default agent's row is marked as default: %q", claudeRow)
	}
}

// A vendor baton read and found empty says so in words. Left blank it would look
// like the states that carry no figure at all, which is the confusion this whole
// feature exists to remove.
func TestAReadingOfNothingSaysNothingSpent(t *testing.T) {
	m := vendorModel("claude", []proto.VendorUsage{{Vendor: "claude", State: "reading"}})
	got := m.vendorStanding(proto.VendorUsage{Vendor: "claude", State: "reading"})
	if strings.TrimSpace(got) == "" {
		t.Fatal("a reading of zero rendered as blank; it is indistinguishable from having no source")
	}
	if !strings.Contains(got, "nothing spent") {
		t.Errorf("standing = %q, want it to say nothing was spent", got)
	}
}

// An older daemon sends no vendor list, and a section header over an empty list
// would suggest the fleet has no agent backends at all.
func TestOverlayShowsNoVendorSectionWithoutAList(t *testing.T) {
	m := model{usageInfo: &proto.UsageInfo{Tokens: 5}}
	if rows := m.usageVendorSection(); rows != nil {
		t.Errorf("a daemon that sent no vendor list produced %d rows", len(rows))
	}
	var empty model
	if rows := empty.usageVendorSection(); rows != nil {
		t.Errorf("a cockpit with no usage payload produced %d rows", len(rows))
	}
}

// A state from a newer daemon that this cockpit does not know must be handled as
// "no reading" and never promoted into a shown figure.
func TestAnUnknownStateIsNotAReading(t *testing.T) {
	m := vendorModel("claude", []proto.VendorUsage{
		{Vendor: "claude", State: "some-future-state", Tokens: 9_000_000, CostUSD: 90},
	})
	got := m.usageSegment()
	if strings.Contains(got, "9.0M") || strings.Contains(got, "90.00") {
		t.Errorf("segment %q showed a figure for a state it does not understand", got)
	}
	if !strings.Contains(got, "no reading") {
		t.Errorf("segment %q does not say there is no reading", got)
	}
}
