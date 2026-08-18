package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// rowNames renders a projection the way a reader would check it: the depth as
// indentation, then a group by path or a panel by id.
func rowNames(items []dashItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		name := ""
		switch it.kind {
		case itemGroup:
			name = it.name
		case itemFold:
			name = "fold"
		default:
			name = "#" + it.panel.ID
		}
		out[i] = strings.Repeat("  ", it.depth) + name
	}
	return out
}

func treeModel(fleet []panel.Panel) model {
	m := baseModel()
	m.fleet = fleet
	return m
}

// TestBuildTreeKeepsFleetOrder: a group occupies the slot of its FIRST member, at
// every level. That is what makes the tree's shape stable — the cursor does not
// jump when a snapshot arrives with the same fleet, because nothing about the
// projection depends on anything but fleet order.
func TestBuildTreeKeepsFleetOrder(t *testing.T) {
	m := treeModel([]panel.Panel{
		{ID: "1", Title: "lone a"},
		{ID: "2", Title: "api a", Group: "backend/api"},
		{ID: "3", Title: "lone b"},
		{ID: "4", Title: "db a", Group: "backend/db"},
		{ID: "5", Title: "backend d", Group: "backend"},
		{ID: "6", Title: "api b", Group: "backend/api"},
	})

	want := []string{
		"#1",
		"backend", // takes panel 2's slot: the first member of anything under it
		"  backend/api",
		"    #2",
		"    #6",
		"  backend/db",
		"    #4",
		"  #5", // a panel filed directly in backend, after the sub-groups it followed
		"#3",
	}
	if got := rowNames(m.dashItems()); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tree shape:\n got %v\nwant %v", got, want)
	}
}

// TestBuildTreeSurvivesAMalformedPath: a group path with an empty segment must not
// mint a nameless node. The server validates paths, but the cockpit renders
// whatever the wire hands it, and a blank row would be unselectable and unnameable.
func TestBuildTreeSurvivesAMalformedPath(t *testing.T) {
	m := treeModel([]panel.Panel{{ID: "1", Title: "odd", Group: "a//b"}})
	for _, it := range m.dashItems() {
		if it.kind == itemGroup && panel.GroupLeaf(it.name) == "" {
			t.Fatalf("a doubled separator produced a nameless group row: %+v", it)
		}
	}
	if got, want := rowNames(m.dashItems()), []string{"a", "  a/b", "    #1"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestTreeSkipsTheSingletons: the conductor and the global shell are marks in the
// FLEET heading, never rows — the same contract the flat projection kept.
func TestTreeSkipsTheSingletons(t *testing.T) {
	m := treeModel([]panel.Panel{
		{ID: "1", Title: "work"},
		{ID: "2", Title: "conductor", Conductor: true},
		{ID: "3", Title: "home", GlobalShell: true},
	})
	if got, want := rowNames(m.dashItems()), []string{"#1"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestGroupsAreExpandedByDefault: the tree replaced a card grid that showed every
// loose panel, so opening closed would show a person fewer panels than they had a
// moment ago. Crowding is the quiet fold's job.
func TestGroupsAreExpandedByDefault(t *testing.T) {
	m := treeModel([]panel.Panel{{ID: "1", Title: "a", Group: "g"}, {ID: "2", Title: "b", Group: "g"}})
	items := m.dashItems()
	if len(items) != 3 || !items[0].expanded {
		t.Fatalf("a group should open expanded, got %v", rowNames(items))
	}
}

// TestCollapseHidesRowsAndNothingElse is the invariant the whole feature rests on:
// expansion is a VIEW state. A closed group still owns its subtree, so every bulk
// verb means the same thing on it as an open one.
func TestCollapseHidesRowsAndNothingElse(t *testing.T) {
	fleet := []panel.Panel{
		{ID: "1", Title: "backend d", Group: "backend"},
		{ID: "2", Title: "api a", Group: "backend/api"},
		{ID: "3", Title: "lone"},
	}
	open := treeModel(fleet)
	shut := treeModel(fleet)
	shut.collapsed = map[string]bool{"backend": true}

	if got, want := rowNames(shut.dashItems()), []string{"backend", "#3"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("a collapsed group should hide its rows, got %v want %v", got, want)
	}

	var a, b dashItem
	for _, it := range open.dashItems() {
		if it.kind == itemGroup && it.name == "backend" {
			a = it
		}
	}
	for _, it := range shut.dashItems() {
		if it.kind == itemGroup && it.name == "backend" {
			b = it
		}
	}
	if len(a.members) != len(b.members) || len(a.ids()) != len(b.ids()) {
		t.Fatalf("open and shut must own the same subtree: %d vs %d", len(a.members), len(b.members))
	}
	if len(b.ids()) != 2 {
		t.Fatalf("backend owns two panels either way, got %v", b.ids())
	}
}

// TestCollapsingASubGroupLeavesItsParentAlone: the state is keyed by full path, so
// shutting backend/api does not shut backend, and two groups whose last segment
// happens to match are not the same row.
func TestCollapsingASubGroupLeavesItsParentAlone(t *testing.T) {
	m := treeModel([]panel.Panel{
		{ID: "1", Title: "api a", Group: "backend/api"},
		{ID: "2", Title: "api b", Group: "frontend/api"},
	})
	m.collapsed = map[string]bool{"backend/api": true}

	want := []string{"backend", "  backend/api", "frontend", "  frontend/api", "    #2"}
	if got := rowNames(m.dashItems()); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("collapse is per path:\n got %v\nwant %v", got, want)
	}
}

// TestQuietFoldsPerLevel: each level folds its own quiet panels. A crowded work
// item tidies itself without the top level having to know, which is the whole
// reason the fold moved off a single flag.
func TestQuietFoldsPerLevel(t *testing.T) {
	fleet := []panel.Panel{{ID: "busy", Title: "busy", State: panel.Running, Group: "g"}}
	for i := 0; i < 5; i++ {
		fleet = append(fleet, panel.Panel{ID: "gq" + string(rune('0'+i)), Title: "gq", State: panel.Idle, Group: "g"})
	}
	for i := 0; i < 5; i++ {
		fleet = append(fleet, panel.Panel{ID: "tq" + string(rune('0'+i)), Title: "tq", State: panel.Idle})
	}
	m := treeModel(fleet)
	m.foldQuiet = 2

	var folds []dashItem
	for _, it := range m.dashItems() {
		if it.kind == itemFold {
			folds = append(folds, it)
		}
	}
	if len(folds) != 2 {
		t.Fatalf("expected a fold at each level, got %d: %v", len(folds), rowNames(m.dashItems()))
	}
	byParent := map[string]int{}
	for _, f := range folds {
		byParent[f.parent] = f.quiet
	}
	if byParent["g"] != 5 || byParent[""] != 5 {
		t.Fatalf("each fold counts ITS level: %v", byParent)
	}
}

// TestQuietFoldOpensOneLevelAtATime: opening the top level's fold must not open a
// work item's, or clearing the clutter at one level would undo it at every other.
func TestQuietFoldOpensOneLevelAtATime(t *testing.T) {
	fleet := []panel.Panel{{ID: "busy", Title: "busy", State: panel.Running, Group: "g"}}
	for i := 0; i < 5; i++ {
		fleet = append(fleet, panel.Panel{ID: "gq" + string(rune('0'+i)), Title: "gq", State: panel.Idle, Group: "g"})
		fleet = append(fleet, panel.Panel{ID: "tq" + string(rune('0'+i)), Title: "tq", State: panel.Idle})
	}
	m := treeModel(fleet)
	m.foldQuiet = 2
	m.foldOpen = map[string]bool{"g": true}

	shown := map[string]bool{}
	for _, it := range m.dashItems() {
		if it.kind == itemPanel {
			shown[it.panel.ID] = true
		}
	}
	if !shown["gq0"] {
		t.Fatal("the work item's fold is open, so its quiet panels should have rows")
	}
	if shown["tq0"] {
		t.Fatal("the top level's fold is still shut; its quiet panels must stay folded")
	}
}

// TestLastMarksTheEndOfEachLevel: the branch glyphs close correctly, which is the
// only thing telling a reader where one work item's contents stop.
func TestLastMarksTheEndOfEachLevel(t *testing.T) {
	m := treeModel([]panel.Panel{
		{ID: "1", Title: "a", Group: "g"},
		{ID: "2", Title: "b", Group: "g"},
		{ID: "3", Title: "lone"},
	})
	var lasts []string
	for _, it := range m.dashItems() {
		if it.last {
			lasts = append(lasts, rowNames([]dashItem{it})[0])
		}
	}
	if strings.Join(lasts, "|") != "  #2|#3" {
		t.Fatalf("expected the last row of each level, got %v", lasts)
	}
}

// TestFilterNarrowsRowsNotMembers guards the hazard that the obvious
// implementation walks into: building the tree from a pre-filtered fleet makes a
// group row own only what you can see, so `w` on it destroys a different amount
// depending on what is typed in the search box.
func TestFilterNarrowsRowsNotMembers(t *testing.T) {
	m := treeModel([]panel.Panel{
		{ID: "1", Title: "keep me", Group: "g"},
		{ID: "2", Title: "other", Group: "g"},
		{ID: "3", Title: "elsewhere"},
	})
	m.filter = "keep"

	items := m.dashItems()
	if got, want := rowNames(items), []string{"g", "  #1"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("the filter should draw the hit under its group, got %v want %v", got, want)
	}
	if len(items[0].ids()) != 2 {
		t.Fatalf("the group still owns both panels under a filter, got %v", items[0].ids())
	}
}

// TestLabelIsTheLastSegment: a nested row names itself by its own segment, since
// its ancestors are already drawn above it. name stays the full path, because that
// is the identity every server op takes.
func TestLabelIsTheLastSegment(t *testing.T) {
	m := treeModel([]panel.Panel{{ID: "1", Title: "x", Group: "backend/api"}})
	for _, it := range m.dashItems() {
		if it.kind != itemGroup || it.name != "backend/api" {
			continue
		}
		if it.label() != "api" {
			t.Fatalf("label should be the segment, got %q", it.label())
		}
		return
	}
	t.Fatal("no row for backend/api")
}
