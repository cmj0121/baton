package tui

import (
	"strings"
	"testing"

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

func TestProcTreeViewRenders(t *testing.T) {
	m := model{width: 100, height: 30, mode: modeProcTree, procLines: []string{"baton (daemon) pid=1  baton", "└─ [shell/running] pid=2  zsh"}}
	out := m.procTreeView()
	if !strings.Contains(out, spaced("PROCESS TREE")) || !strings.Contains(out, "shell/running") {
		t.Fatalf("the view should show the header and the tree, got:\n%s", out)
	}
}
