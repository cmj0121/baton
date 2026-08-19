package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// TestTreeStaysLegibleAtScale is the whole point of the feature, asserted rather
// than eyeballed: a fleet with two nested work items and twenty quiet panels has
// to fit on one screen with its shape intact.
//
// The old projection would have drawn this as four rows — two group cards, a fold
// row and a lone panel — with no way to reach a panel inside a work item without
// leaving the dashboard. The tree draws every level, folds each level's quiet
// panels separately, and still lands well under a screenful.
func TestTreeStaysLegibleAtScale(t *testing.T) {
	var fleet []panel.Panel
	add := func(id, title, group, cwd string, st panel.State, spark, task string) {
		fleet = append(fleet, panel.Panel{ID: id, Kind: panel.Agent, Title: title, Group: group,
			Cwd: cwd, State: st, Spark: spark, Task: task})
	}
	add("1", "claude · auth", "backend/api", "/w/baton/api", panel.Attention, "▁▁▁", "")
	add("2", "claude · ratelimit", "backend/api", "/w/baton/api", panel.Running, "▂▃▅▇▆▃▁", "add per-key limits")
	add("3", "claude · migrate", "backend/db", "/w/baton/db", panel.Running, "▅▃▂", "split the users table")
	add("4", "claude · seed", "backend/db", "/w/baton/db", panel.Stuck, "▁", "")
	for i := 0; i < 9; i++ {
		fleet = append(fleet, panel.Panel{ID: fmt.Sprintf("q%d", i), Kind: panel.Shell,
			Title: fmt.Sprintf("shell #%d", i), Group: "backend", State: panel.Idle, Cwd: "/w/baton"})
	}
	add("5", "claude · darkmode", "frontend", "/w/web", panel.Running, "▇▆▅▃", "dark mode pass")
	add("6", "claude · a11y", "frontend", "/w/web", panel.Done, "▁▁", "")
	for i := 0; i < 11; i++ {
		fleet = append(fleet, panel.Panel{ID: fmt.Sprintf("t%d", i), Kind: panel.Shell,
			Title: fmt.Sprintf("scratch #%d", i), State: panel.Idle, Cwd: "/Users/x"})
	}
	add("7", "claude · docs", "", "/w/baton", panel.Running, "▃▅▃", "rewrite ISOLATION.md")

	m := baseModel()
	m.mode, m.width, m.height, m.foldQuiet = modeDashboard, 150, 60, 8
	m.fleet = fleet
	// This fleet folds to a handful of TOP-LEVEL rows, which the dashboard would
	// draw as cards. What is asserted here is that the tree scales, so ask for it.
	m.showTree = true

	items := m.dashItems()
	if len(fleet) != 27 {
		t.Fatalf("fixture drifted: %d panels", len(fleet))
	}
	if len(items) > 16 {
		t.Fatalf("27 panels should fold to a screenful, got %d rows:\n%v", len(items), rowNames(items))
	}

	// Every level folded its own quiet panels rather than the top level folding
	// them all: backend's nine and the top level's eleven are two separate rows.
	folds := map[string]int{}
	for _, it := range items {
		if it.kind == itemFold {
			folds[it.parent] = it.quiet
		}
	}
	if folds["backend"] != 9 || folds[""] != 11 {
		t.Fatalf("each level should fold its own quiet panels, got %v", folds)
	}

	// The hierarchy is drawn, not flattened: the sub-groups have rows of their own
	// and their panels sit under them.
	depths := map[string]int{}
	for _, it := range items {
		if it.kind == itemGroup {
			depths[it.name] = it.depth
		}
	}
	for name, want := range map[string]int{"backend": 0, "backend/api": 1, "backend/db": 1, "frontend": 0} {
		if got, ok := depths[name]; !ok || got != want {
			t.Errorf("%s should sit at depth %d, got %d (present=%v)", name, want, got, ok)
		}
	}

	// A row still reaches every panel that is asking for something, so the fleet is
	// navigable from here without entering a split.
	view := stripANSI(m.View())
	for _, want := range []string{"claude · auth", "claude · seed", "▸ add per-key limits", "/w/baton/db"} {
		if !strings.Contains(view, want) {
			t.Errorf("the dashboard should carry %q at this width", want)
		}
	}
}
