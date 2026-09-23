package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
)

// TestFooterShowsOpening: the panel the cockpit is pointed at shows its opening
// cost beside the host stats.
func TestFooterShowsOpening(t *testing.T) {
	m := usageModel(usageWindow)
	m.cpuPct, m.memUsed, m.memTotal = 18, 9<<30, 16<<30
	m.usageInfo.Opening = map[string]int64{"p1": 44_002}
	m.cursor = 0

	strip := m.statsStrip()
	for _, want := range []string{"CPU", "MEM", "SYS", "44.0K"} {
		if !strings.Contains(strip, want) {
			t.Errorf("statsStrip missing %q in %q", want, strip)
		}
	}
	if !strings.Contains(m.footer(), "SYS 44.0K") {
		t.Errorf("footer lost the opening cost: %q", m.footer())
	}
}

// TestFooterOpeningFollowsFocus: another panel with no reading — a shell, an
// agent whose first turn has not landed — shows nothing, not a zero.
func TestFooterOpeningFollowsFocus(t *testing.T) {
	m := usageModel(usageWindow)
	m.memTotal = 16 << 30
	m.usageInfo.Opening = map[string]int64{"p1": 44_002}
	m.cursor = 1

	if strip := m.statsStrip(); strings.Contains(strip, "SYS") {
		t.Errorf("a panel with no reading shows SYS: %q", strip)
	}
	if !strings.Contains(m.statsStrip(), "CPU") {
		t.Error("the host stats went with the opening cost")
	}
}

// TestFooterOpeningNotForAGroup: a group's members each pay their own opening, and
// no one prompt costs their sum.
func TestFooterOpeningNotForAGroup(t *testing.T) {
	m := usageModel(usageWindow)
	m.fleet[0].Group, m.fleet[1].Group = "api", "api"
	m.usageInfo.Opening = map[string]int64{"p1": 44_002, "p2": 40_000}
	m.cursor = 0

	if strip := m.statsStrip(); strings.Contains(strip, "SYS") {
		t.Errorf("a selected group shows SYS: %q", strip)
	}
}

// TestFooterOpeningWithoutHostStats: the opening cost does not wait on the first
// host sample.
func TestFooterOpeningWithoutHostStats(t *testing.T) {
	m := usageModel(usageWindow)
	m.usageInfo.Opening = map[string]int64{"p1": 812}
	m.cursor = 0

	strip := m.statsStrip()
	if !strings.Contains(strip, "SYS") || !strings.Contains(strip, "812") || strings.Contains(strip, "CPU") {
		t.Errorf("statsStrip = %q, want SYS 812 alone", strip)
	}
}

// TestFooterWithOpeningFillsWidth holds the one-row, full-width invariant with the
// extra segment in.
func TestFooterWithOpeningFillsWidth(t *testing.T) {
	for _, w := range []int{60, 80, 120, 200} {
		m := usageModel(usageWindow)
		m.width, m.endpoint, m.status = w, "local", "dashboard"
		m.cpuPct, m.memUsed, m.memTotal = 18, 9<<30, 16<<30
		m.usageInfo.Opening = map[string]int64{"p1": 1_234_567}
		foot := m.footer()
		if strings.Contains(foot, "\n") {
			t.Fatalf("w=%d: footer wrapped", w)
		}
		if got := lipgloss.Width(foot); got != w {
			t.Fatalf("w=%d: footer width = %d", w, got)
		}
	}
}

// TestOpeningOnlyPayloadLeavesPanelViewBlank: a payload carrying only opening
// costs has no window in it, and the panel view reads it as no payload — not as
// "not attributed".
func TestOpeningOnlyPayloadLeavesPanelViewBlank(t *testing.T) {
	m := usageModel(usagePanel)
	m.usageText = ""
	m.usageInfo = &proto.UsageInfo{Opening: map[string]int64{"p1": 44_002}}
	m.cursor = 0
	if got := m.usagePanelText(); got != "" {
		t.Errorf("usagePanelText = %q, want blank", got)
	}
	if !strings.Contains(m.statsStrip(), "SYS") {
		t.Error("the footer lost SYS with no window in the payload")
	}
}
