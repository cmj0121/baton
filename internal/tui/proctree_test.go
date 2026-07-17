package tui

import (
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
)

// procFleet is a small fleet with a group and an ungrouped panel, enough to render
// a multi-line tree.
func procFleet() []panel.Panel {
	return []panel.Panel{
		{ID: "1", Title: "hale", State: panel.Running, Group: "feature-x", Pid: 41180},
		{ID: "2", Title: "ellis", State: panel.Idle, Group: "feature-x", Pid: 41205},
		{ID: "3", Title: "shell", State: panel.Running, Pid: 41240},
	}
}

func TestOpenProcTree(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeDashboard, fleet: procFleet()}.openProcTree(modeDashboard)
	if m.mode != modeProcTree {
		t.Fatalf("openProcTree should enter modeProcTree, got %v", m.mode)
	}
	if m.procFrom != modeDashboard || m.procScroll != 0 {
		t.Fatalf("overlay state wrong: from=%v scroll=%d", m.procFrom, m.procScroll)
	}
	// The daemon root and every panel appear regardless of the host's OS table.
	joined := strings.Join(m.procLines, "\n")
	for _, want := range []string{"baton (daemon)", "[group: feature-x]", "[hale/running]", "[ungrouped]", "[shell/running]"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("tree missing %q:\n%s", want, joined)
		}
	}
}

// The overlay is reachable from any mode via the prefix escape C-t o, and esc
// restores the view it was opened from.
func TestProcTreePrefixOpenAndClose(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeDashboard, fleet: procFleet()}

	m = press(m, m.effPrefix()) // leader (C-t)
	m = press(m, keyProcTree)   // o → C-t o
	if m.mode != modeProcTree {
		t.Fatalf("C-t o should open the process tree, got %v", m.mode)
	}
	m = press(m, "esc")
	if m.mode != modeDashboard {
		t.Fatalf("esc should restore the dashboard, got %v", m.mode)
	}
	if m.procLines != nil {
		t.Fatalf("close should drop the sampled tree, got %v", m.procLines)
	}
}

func TestProcTreeScroll(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "pid line"
	}
	m := model{width: 120, height: 40, mode: modeProcTree, procFrom: modeDashboard, procLines: lines}

	rows := m.procViewportRows()
	maxOff := len(lines) - rows

	m = press(m, "j")
	if m.procScroll != 1 {
		t.Fatalf("j should scroll one line, got %d", m.procScroll)
	}
	m = press(m, "G")
	if m.procScroll != maxOff {
		t.Fatalf("G should rest the last line at the bottom (off %d), got %d", maxOff, m.procScroll)
	}
	m = press(m, "g")
	if m.procScroll != 0 {
		t.Fatalf("g should return to the top, got %d", m.procScroll)
	}
	m = press(m, "k")
	if m.procScroll != 0 {
		t.Fatalf("k at the top should stay at 0, got %d", m.procScroll)
	}
}

func TestCPUBar(t *testing.T) {
	// Always a fixed 8 visible cells, whatever the value (clamped both ends).
	for _, pct := range []float64{-10, 0, 12.5, 50, 99.9, 100, 250} {
		if w := lipgloss.Width(cpuBar(pct)); w != 8 {
			t.Fatalf("cpuBar(%v) visible width = %d, want 8", pct, w)
		}
	}
	// Fill tracks the value: 12.5% fills one of eight cells, 50% four, and
	// saturation fills every cell with none of the faint track left.
	count := func(s string, r rune) int { return strings.Count(s, string(r)) }
	if full, empty := count(cpuBar(12.5), '█'), count(cpuBar(12.5), '░'); full != 1 || empty != 7 {
		t.Fatalf("cpuBar(12.5) = %d full / %d empty, want 1/7", full, empty)
	}
	if full := count(cpuBar(50), '█'); full != 4 {
		t.Fatalf("cpuBar(50) full cells = %d, want 4", full)
	}
	if count(cpuBar(0), '█') != 0 {
		t.Fatalf("cpuBar(0) should have no filled cell: %q", cpuBar(0))
	}
	if count(cpuBar(100), '░') != 0 {
		t.Fatalf("cpuBar(100) should have no empty cell: %q", cpuBar(100))
	}
	// A NaN reading (a process sampled at creation, 0/0) must fold to an empty bar,
	// never panic — int(NaN) is min-int on amd64 and would blow strings.Repeat.
	if w, full := lipgloss.Width(cpuBar(math.NaN())), count(cpuBar(math.NaN()), '█'); w != 8 || full != 0 {
		t.Fatalf("cpuBar(NaN) = width %d / %d full, want 8/0", w, full)
	}
}

// loadColor bands the fill hue, boundaries inclusive at the lower edge.
func TestLoadColor(t *testing.T) {
	cases := []struct {
		pct  float64
		want lipgloss.Color
	}{
		{0, colLoadLo}, {49.9, colLoadLo},
		{50, colLoadMid}, {79.9, colLoadMid},
		{80, colLoadHi}, {100, colLoadHi},
	}
	for _, c := range cases {
		if got := loadColor(c.pct); got != c.want {
			t.Fatalf("loadColor(%v) = %v, want %v", c.pct, got, c.want)
		}
	}
}

func TestProcTreeViewRenders(t *testing.T) {
	m := model{width: 100, height: 30, mode: modeProcTree, procLines: []string{"baton (daemon) pid=1  baton", "└─ [shell/running] pid=2  zsh"}}
	out := m.procTreeView()
	if !strings.Contains(out, spaced("PROCESS TREE")) || !strings.Contains(out, "shell/running") {
		t.Fatalf("the view should show the header and the tree, got:\n%s", out)
	}
}
