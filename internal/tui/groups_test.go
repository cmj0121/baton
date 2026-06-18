package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// groupedFleet is a small fleet with two work items and two lone panels, in a
// deliberately interleaved order to exercise the fold.
func groupedFleet() []panel.Panel {
	return []panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "api · a", State: panel.Running, Group: "api"},
		{ID: "2", Kind: panel.Shell, Title: "lone shell", State: panel.Idle},
		{ID: "3", Kind: panel.Agent, Title: "api · b", State: panel.Attention, Group: "api"},
		{ID: "4", Kind: panel.Agent, Title: "db · a", State: panel.Exited, Group: "db"},
		{ID: "5", Kind: panel.Shell, Title: "lone two", State: panel.Running},
		{ID: "6", Kind: panel.Agent, Title: "api · c", State: panel.Idle, Group: "api"},
	}
}

func TestDashItemsCollapsesGroups(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	items := m.dashItems()

	// Expected order: api group (at panel 1), lone shell, db group, lone two.
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d: %+v", len(items), items)
	}
	if items[0].kind != itemGroup || items[0].name != "api" || len(items[0].members) != 3 {
		t.Fatalf("item 0 should be the 3-member api group, got %+v", items[0])
	}
	if items[1].kind != itemPanel || items[1].panel.ID != "2" {
		t.Fatalf("item 1 should be lone shell #2, got %+v", items[1])
	}
	if items[2].kind != itemGroup || items[2].name != "db" {
		t.Fatalf("item 2 should be the db group, got %+v", items[2])
	}
	if items[3].kind != itemPanel || items[3].panel.ID != "5" {
		t.Fatalf("item 3 should be lone two #5, got %+v", items[3])
	}

	// itemCount and treeView track the folded count, not the panel count.
	if got := m.itemCount(); got != 4 {
		t.Fatalf("itemCount should be 4 items, got %d", got)
	}
}

func TestGroupStateRollup(t *testing.T) {
	// The most urgent member wins: api has an Attention member.
	members := []panel.Panel{{State: panel.Idle}, {State: panel.Attention}, {State: panel.Running}}
	if got := groupState(members); got != panel.Attention {
		t.Fatalf("rollup should be attention, got %v", got)
	}
	// All exited rolls up to exited.
	if got := groupState([]panel.Panel{{State: panel.Exited}, {State: panel.Exited}}); got != panel.Exited {
		t.Fatalf("rollup should be exited, got %v", got)
	}
}

func TestToggleMarkGroupMarksAllMembers(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	items := m.dashItems()

	// Marking the api group marks all three of its members.
	m.toggleMark(items[0])
	if !m.itemMarked(items[0]) {
		t.Fatal("api group should read as marked after toggle")
	}
	if !m.selecting() {
		t.Fatal("selecting() should be true once something is marked")
	}
	if ids := m.markedIDs(); strings.Join(ids, ",") != "1,3,6" {
		t.Fatalf("markedIDs should be the api members in fleet order, got %v", ids)
	}

	// Toggling again clears them.
	m.toggleMark(items[0])
	if m.itemMarked(items[0]) || m.selecting() {
		t.Fatal("toggling the group again should clear all marks")
	}

	// A lone panel marks just itself.
	m.toggleMark(items[1])
	if ids := m.markedIDs(); strings.Join(ids, ",") != "2" {
		t.Fatalf("marking the lone panel should mark only #2, got %v", ids)
	}
}

func TestSelectedItemBounds(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.cursor = 0
	if it, ok := m.selectedItem(); !ok || it.kind != itemGroup {
		t.Fatalf("cursor 0 should select the api group, got %+v ok=%v", it, ok)
	}
	m.cursor = 99
	if _, ok := m.selectedItem(); ok {
		t.Fatal("an out-of-range cursor should not resolve to an item")
	}
}

func TestCloseGroupClosesAllMembers(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.cursor = 0 // the api group
	m.toggleMark(m.dashItems()[0])

	m.closeSelected() // no client: mutates the local fleet directly

	for _, p := range m.fleet {
		if p.Group == "api" {
			t.Fatalf("api member %s should be gone after closing the group", p.ID)
		}
	}
	if len(m.fleet) != 3 {
		t.Fatalf("3 panels should remain, got %d", len(m.fleet))
	}
	if m.selecting() {
		t.Fatal("closing the group should clear its marks")
	}
}

func TestGroupViewsRender(t *testing.T) {
	// Grid, tree, and preview all render with groups present and a selection in
	// progress, covering the group card / row / preview paths.
	for _, tc := range []struct {
		name string
		mut  func(*model)
	}{
		{"group-grid", func(m *model) { m.fleet = groupedFleet() }},
		{"group-grid-selecting", func(m *model) { m.fleet = groupedFleet(); m.toggleMark(m.dashItems()[0]) }},
		{"group-tree", func(m *model) { m.fleet = append(groupedFleet(), panel.Mock()...); m.height = 40 }},
		{"group-tree-selecting", func(m *model) {
			m.fleet = append(groupedFleet(), panel.Mock()...)
			m.height = 40
			m.toggleMark(m.dashItems()[0])
		}},
		{"group-preview", func(m *model) { m.fleet = append(groupedFleet(), panel.Mock()...); m.height = 40; m.cursor = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := baseModel()
			tc.mut(&m)
			if m.View() == "" {
				t.Fatal("expected a non-empty render")
			}
		})
	}
}

func TestZoomGroupSetsStatus(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0])
	if !strings.Contains(m.status, "api") {
		t.Fatalf("zoomGroup should name the group in the status, got %q", m.status)
	}
}

func TestMarkAndGroupFlow(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()

	// g marks the selected lone panel (#2 at item index 1).
	m.cursor = 1
	m = press(m, keyMark)
	if !m.marked["2"] {
		t.Fatal("g should mark the selected panel")
	}
	// Mark the other lone panel (#5 at item index 3) too.
	m.cursor = 3
	m = press(m, keyMark)
	if !m.marked["5"] {
		t.Fatal("g should mark the second lone panel")
	}

	// C-g opens the name overlay; typing a name and submitting groups them.
	m = press(m, keyGroup)
	if m.input != inputGroupName {
		t.Fatalf("ctrl+g should open the group-name overlay, got %v", m.input)
	}
	m = press(m, "o", "p", "s")
	m = press(m, "enter")
	if m.input != inputNone {
		t.Fatal("overlay should close after submit")
	}
	if len(m.marked) != 0 {
		t.Fatalf("marks should clear after grouping, got %v", m.marked)
	}
	if !strings.Contains(m.status, "ops") {
		t.Fatalf("status should mention the new group, got %q", m.status)
	}
}

func TestMarkTogglesOff(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.cursor = 1
	m = press(m, keyMark)
	m = press(m, keyMark) // toggle the same item off
	if m.selecting() {
		t.Fatal("toggling the mark off should end the selection")
	}
	if !strings.Contains(m.status, "cleared") {
		t.Fatalf("status should note the cleared selection, got %q", m.status)
	}
}

func TestGroupWithoutSelectionHints(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = press(m, keyGroup)
	if m.input != inputNone {
		t.Fatal("ctrl+g with nothing marked should not open an overlay")
	}
	if !strings.Contains(m.status, "select") {
		t.Fatalf("expected a hint to select first, got %q", m.status)
	}
}

func TestRenamePanelAndGroup(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()

	// e on a group (item 0 = api) opens rename with the group remembered, seeded.
	m.cursor = 0
	m = press(m, keyRename)
	if m.input != inputRename || m.renameGroup != "api" || m.inputBuf != "api" {
		t.Fatalf("e on a group should seed a group rename, got input=%v group=%q buf=%q", m.input, m.renameGroup, m.inputBuf)
	}
	m = press(m, "enter")
	if m.input != inputNone || !strings.Contains(m.status, "group") {
		t.Fatalf("group rename should commit, got input=%v status=%q", m.input, m.status)
	}

	// e on a lone panel (item 1 = #2) remembers the panel id instead.
	m.cursor = 1
	m = press(m, keyRename)
	if m.renameID != "2" || m.renameGroup != "" {
		t.Fatalf("e on a panel should rename the panel, got id=%q group=%q", m.renameID, m.renameGroup)
	}
}

func TestGroupAndRenameGuards(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()

	if got := m.commitGroup(""); !strings.Contains(got.status, "name") {
		t.Fatalf("empty group name should be rejected, got %q", got.status)
	}
	if got := m.commitGroup("x"); !strings.Contains(got.status, "no panels") {
		t.Fatalf("grouping with no marks should be rejected, got %q", got.status)
	}
	if got := m.commitRename(""); !strings.Contains(got.status, "empty") {
		t.Fatalf("empty rename should be rejected, got %q", got.status)
	}
	if got := m.commitRename("x"); !strings.Contains(got.status, "nothing") {
		t.Fatalf("rename with no target should be rejected, got %q", got.status)
	}
	empty := baseModel()
	if got := empty.startRename(); !strings.Contains(got.status, "nothing") {
		t.Fatalf("rename with no item should hint, got %q", got.status)
	}
}

func TestGroupOverlaysRender(t *testing.T) {
	for _, in := range []inputPurpose{inputGroupName, inputRename} {
		m := baseModel()
		m.fleet = groupedFleet()
		m.input = in
		m.inputBuf = "x"
		if m.View() == "" {
			t.Fatalf("overlay %v should render", in)
		}
	}
}

func TestKeyMapIncludesGroupVerbs(t *testing.T) {
	// The group verbs are editable bindings, so they show in the key map. The
	// dashboard verbs are single-key commands; dashboard/group-view are escapes.
	wantCmd := map[string]bool{"mark": true, "group": true, "add": true, "ungroup": true, "rename": true}
	wantEscape := map[string]bool{"dashboard": true, "group-view": true}
	for _, b := range bindings {
		switch {
		case wantCmd[b.name]:
			if isEscape(b.act) {
				t.Fatalf("dashboard verb %q should be a single-key command", b.name)
			}
			delete(wantCmd, b.name)
		case wantEscape[b.name]:
			if !isEscape(b.act) {
				t.Fatalf("%q should be a universal escape", b.name)
			}
			delete(wantEscape, b.name)
		}
	}
	if len(wantCmd) != 0 || len(wantEscape) != 0 {
		t.Fatalf("key map is missing bindings: cmd=%v escape=%v", wantCmd, wantEscape)
	}
}

func TestKeyMapRebindsGroupKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // rebinding persists to $HOME/.baton/config

	m := model{mode: modeKeyMap, fleet: groupedFleet(), binds: append([]binding(nil), bindings...)}
	gi := -1
	for i, b := range m.keymap() {
		if b.name == "group" {
			gi = i
			break
		}
	}
	if gi < 0 {
		t.Fatal("group binding missing from the key map")
	}

	// Edit the group row (row 0 is the prefix; bindings start at row 1) and rebind
	// it to 'z' by typing.
	m.cursor = gi + 1
	m = press(m, "e")
	if !m.editing {
		t.Fatal("e should start capturing a new key")
	}
	m = press(m, "z")
	if m.binds[gi].key != "z" {
		t.Fatalf("group should rebind to z, got %q", m.binds[gi].key)
	}

	// The rebound bare key now triggers grouping on the dashboard.
	m.mode = modeDashboard
	m.cursor = 1
	m = press(m, "g") // mark a panel (mark key unchanged)
	m = press(m, "z") // the rebound group key
	if m.input != inputGroupName {
		t.Fatalf("the rebound group key should open the group overlay, got %v", m.input)
	}

	// The old default key no longer groups.
	if _, ok := m.lookupCmd("G"); ok {
		t.Fatal("the old group key G should no longer be bound")
	}
}

func TestUngroupSelected(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()

	// u on a group dissolves it (status notes the group it ungrouped).
	m.cursor = 0 // the api group
	m = press(m, keyUngroup)
	if !strings.Contains(m.status, "api") {
		t.Fatalf("ungroup should name the dissolved group, got %q", m.status)
	}

	// u on a lone panel is a no-op with a hint.
	m.cursor = 1 // lone shell #2
	m = press(m, keyUngroup)
	if !strings.Contains(m.status, "select a group") {
		t.Fatalf("ungroup on a panel should hint, got %q", m.status)
	}
}

func TestCommandAndEscapeKeys(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()

	// A bare command opens the read-only key list (single key, no prefix), and
	// C-t k opens the editable key map.
	if got := press(m, "?"); got.mode != modeHelp {
		t.Fatal("? should open the key list")
	}
	if got := press(m, "ctrl+t", "k"); got.mode != modeKeyMap {
		t.Fatal("C-t k should open the editable key map")
	}
	// The escape C-t g enters the selected group's split.
	m.cursor = 0 // the api group
	got := press(m, "ctrl+t", "g")
	if got.mode != modeGroupZoom || got.groupName != "api" {
		t.Fatalf("C-t g should enter the group split, got mode=%v name=%q", got.mode, got.groupName)
	}
	// C-t d leaves the split for the dashboard.
	g2, _ := got.handleGroupZoomKey(key("ctrl+t"))
	g3, _ := g2.(model).handleGroupZoomKey(key("d"))
	if g3.(model).mode != modeDashboard {
		t.Fatal("C-t d should leave the split for the dashboard")
	}
}

func TestAddMarkedToGroup(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()

	// Mark the two lone panels, put the cursor on the api group, press a.
	m.cursor = 1
	m = press(m, keyMark) // mark #2
	m.cursor = 3
	m = press(m, keyMark) // mark #5
	m.cursor = 0          // the api group
	m = press(m, keyAdd)
	if len(m.marked) != 0 {
		t.Fatalf("add should clear the marks, got %v", m.marked)
	}
	if !strings.Contains(m.status, "added") {
		t.Fatalf("add should confirm in the status, got %q", m.status)
	}

	// Add with the cursor on a lone panel hints to pick a group.
	lone := baseModel()
	lone.fleet = groupedFleet()
	lone.cursor = 1
	if got := press(lone, keyAdd); !strings.Contains(got.status, "select a group") {
		t.Fatalf("add on a non-group should hint, got %q", got.status)
	}

	// enterGroupView on a lone panel hints too.
	if _, ok := lone.selectedItem(); ok {
		got, _ := lone.enterGroupView()
		if !strings.Contains(got.(model).status, "select a group") {
			t.Fatalf("group view on a non-group should hint, got %q", got.(model).status)
		}
	}
}

func TestHelpContextAndZoomFooter(t *testing.T) {
	// ? from the dashboard opens the editable key map.
	m := baseModel()
	m.fleet = groupedFleet()
	if got := press(m, "?"); got.mode != modeHelp || got.helpFrom != modeDashboard {
		t.Fatalf("? should open the key list from the dashboard, got mode=%v from=%v", got.mode, got.helpFrom)
	}

	// ? from the group split opens the read-only context help; esc returns to it.
	g := baseModel()
	g.fleet = groupedFleet()
	g = g.zoomGroup(g.dashItems()[0])
	gh, _ := g.handleGroupZoomKey(key("?"))
	gm := gh.(model)
	if gm.mode != modeHelp || gm.helpFrom != modeGroupZoom {
		t.Fatalf("? in the split should open context help, got mode=%v from=%v", gm.mode, gm.helpFrom)
	}
	if !strings.Contains(gm.helpView(), "remove the focused panel") {
		t.Fatal("the split help should list the group-view keys")
	}
	back, _ := gm.handleKey(key("esc"))
	if back.(model).mode != modeGroupZoom {
		t.Fatal("esc should return from the help to the split")
	}

	// A zoom footer carries the host stats and clock like the other views, and
	// C-t ? opens the zoom help.
	z := baseModel()
	z.mode = modeZoom
	z.zoomTitle = "sh"
	z.cpuPct, z.memUsed, z.memTotal = 10, 1<<30, 2<<30
	foot := z.zoomFooter()
	if !strings.Contains(foot, "CPU") || !strings.Contains(foot, "ZOOM") {
		t.Fatalf("zoom footer should show ZOOM + CPU/MEM, got %q", foot)
	}
	za, _ := z.handleZoomKey(key("ctrl+t"))
	zh, _ := za.(model).handleZoomKey(key("?"))
	if zm := zh.(model); zm.mode != modeHelp || zm.helpFrom != modeZoom {
		t.Fatalf("C-t ? in a zoom should open the zoom help, got mode=%v from=%v", zm.mode, zm.helpFrom)
	}
}

// TestHelpGroupsByCategory checks the ? key list carries its purpose-section
// headers in every stage, mirroring the editable key map's grouping.
func TestHelpGroupsByCategory(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()

	cases := []struct {
		from mode
		cats []string
	}{
		{modeDashboard, []string{"Navigation", "Panels", "Work items", "View", "Session"}},
		{modeGroupZoom, []string{"Navigation", "Work items", "View"}},
		{modeZoom, []string{"Navigation", "View"}},
	}
	for _, tc := range cases {
		m.helpFrom = tc.from
		view := m.helpView()
		for _, cat := range tc.cats {
			if !strings.Contains(view, cat) {
				t.Fatalf("help from %v should group under %q, got:\n%s", tc.from, cat, view)
			}
		}
	}
}

// TestExitKeysCaptured checks Ctrl-C and Ctrl-E never quit in command mode: on
// the dashboard and in the group split they only hint at the detach binding.
func TestExitKeysCaptured(t *testing.T) {
	for _, k := range []string{"ctrl+c", "ctrl+e"} {
		// Dashboard.
		d := press(baseModel(), k)
		if d.quitting || !strings.Contains(d.status, "disabled") {
			t.Fatalf("%s on the dashboard should hint, not quit; status=%q quit=%v", k, d.status, d.quitting)
		}
		// Group split.
		g := baseModel()
		g.fleet = groupedFleet()
		g = g.zoomGroup(g.dashItems()[0])
		gh, _ := g.handleGroupZoomKey(key(k))
		gm := gh.(model)
		if gm.quitting || gm.mode != modeGroupZoom || !strings.Contains(gm.status, "disabled") {
			t.Fatalf("%s in the split should hint and stay; status=%q mode=%v", k, gm.status, gm.mode)
		}
	}
}

// TestDetachWithPrefixQ checks C-t q detaches (quits the client) from every
// scenario — the dashboard, the group split, and a zoom.
func TestDetachWithPrefixQ(t *testing.T) {
	// Dashboard.
	if d := press(baseModel(), "ctrl+t", keyDetach); !d.quitting {
		t.Fatal("C-t q should detach from the dashboard")
	}

	// Group split.
	g := baseModel()
	g.fleet = groupedFleet()
	g = g.zoomGroup(g.dashItems()[0])
	ga, _ := g.handleGroupZoomKey(key("ctrl+t"))
	gd, _ := ga.(model).handleGroupZoomKey(key(keyDetach))
	if !gd.(model).quitting {
		t.Fatal("C-t q should detach from the group split")
	}

	// Zoom.
	z := baseModel()
	z.mode = modeZoom
	z.zoomID = "1"
	za, _ := z.handleZoomKey(key("ctrl+t"))
	zd, _ := za.(model).handleZoomKey(key(keyDetach))
	if !zd.(model).quitting {
		t.Fatal("C-t q should detach from a zoom")
	}
}
