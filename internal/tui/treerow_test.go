package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
)

func samplePanelItem() dashItem {
	return dashItem{kind: itemPanel, panel: panel.Panel{
		ID: "1", Kind: panel.Agent, Title: "claude · api", State: panel.Running,
		Cwd: "/work/api", Spark: "▂▃▅▇▆▃▁", Task: "add rate limiting",
	}}
}

// TestRowNeverExceedsItsWidth is the property that keeps the tree a tree. lipgloss
// WRAPS what does not fit rather than truncating it, so a row one column too wide
// silently becomes two rows and the shape is gone.
func TestRowNeverExceedsItsWidth(t *testing.T) {
	m := baseModel()
	items := []dashItem{
		samplePanelItem(),
		{kind: itemGroup, name: "backend/api", depth: 3, members: []panel.Panel{{State: panel.Running, Spark: "▂▃▅▇▆▃▁"}}, need: 4},
		{kind: itemFold, quiet: 12, depth: 1},
		{kind: itemPanel, depth: 6, panel: panel.Panel{Title: strings.Repeat("very-long-name-", 12), State: panel.Attention, Cwd: "/a/very/deep/path/that/keeps/going/on"}},
	}
	for _, w := range []int{10, 20, 40, 54, 60, 84, 100, 114, 134, 160, 240} {
		for i, it := range items {
			for _, sel := range []bool{false, true} {
				row := m.treeRow(it, sel, w)
				if got := lipgloss.Width(row); got != w {
					t.Errorf("item %d at width %d (selected=%v): row is %d columns", i, w, sel, got)
				}
				if n := strings.Count(row, "\n"); n != 0 {
					t.Errorf("item %d at width %d: row wrapped onto %d extra lines", i, w, n)
				}
			}
		}
	}
}

// TestRowColumnsAppearWithWidth: each column joins at its breakpoint and not
// before, in the order of how much it says about a panel you cannot see.
func TestRowColumnsAppearWithWidth(t *testing.T) {
	m := baseModel()
	it := samplePanelItem()
	for _, tc := range []struct {
		width  int
		want   []string
		absent []string
	}{
		{40, []string{"claude · api"}, []string{"running", "/work/api", "▂▃▅", "rate limiting"}},
		{colStateAt, []string{"claude · api", "AGENT", "running"}, []string{"/work/api", "rate limiting"}},
		{colDirAt, []string{"running", "/work/api"}, []string{"rate limiting"}},
		{colSparkAt, []string{"/work/api", "▂▃▅▇▆▃▁"}, []string{"rate limiting"}},
		{colTaskAt, []string{"▂▃▅▇▆▃▁", "▸ add rate limiting"}, nil},
	} {
		row := stripANSI(m.treeRow(it, false, tc.width))
		for _, want := range tc.want {
			if !strings.Contains(row, want) {
				t.Errorf("width %d should carry %q, got %q", tc.width, want, row)
			}
		}
		for _, no := range tc.absent {
			if strings.Contains(row, no) {
				t.Errorf("width %d should not carry %q yet, got %q", tc.width, no, row)
			}
		}
	}
}

// TestRowColumnsAlignAcrossRows: the column block is a fixed width for every row
// in one render. Sizing it per row is the obvious implementation and it does not
// work — a row carrying a task gets a wider block than one without, and its state
// lands somewhere else from the state on the row above.
func TestRowColumnsAlignAcrossRows(t *testing.T) {
	m := baseModel()
	rows := []dashItem{
		samplePanelItem(),
		{kind: itemPanel, panel: panel.Panel{Title: "no task", Kind: panel.Shell, State: panel.Idle}},
		{kind: itemGroup, name: "backend", members: []panel.Panel{{State: panel.Running}}},
		{kind: itemPanel, depth: 2, panel: panel.Panel{Title: "nested", Kind: panel.Agent, State: panel.Exited}},
	}
	const width = 160
	_, block := columnBudget(width)

	for i, it := range rows {
		row := stripANSI(m.treeRow(it, false, width))
		// The block always occupies the last `block` columns, so every row's columns
		// begin at the same offset whatever its name, depth or contents.
		if len(row) < block {
			t.Fatalf("row %d is shorter than the column block", i)
		}
		if got := lipgloss.Width(m.treeRow(it, false, width)); got != width {
			t.Fatalf("row %d is %d columns, want %d", i, got, width)
		}
	}

	// The state label starts at the same COLUMN on a panel row and a group row.
	// Measured in display columns rather than bytes: a row carries ●, · and box
	// glyphs, so a byte offset says nothing about where a reader's eye lands.
	at := func(it dashItem) int {
		row := stripANSI(m.treeRow(it, false, width))
		i := strings.Index(row, "running")
		if i < 0 {
			return -1
		}
		return lipgloss.Width(row[:i])
	}
	if a, b := at(rows[0]), at(rows[2]); a != b || a < 0 {
		t.Fatalf("state should align on panel and group rows: %d vs %d", a, b)
	}
}

// TestRowShowsWhatTheCardDid: the row replaced a card, so it has to carry what the
// card carried. This is the check that the replacement was an upgrade rather than
// a trade.
func TestRowShowsWhatTheCardDid(t *testing.T) {
	m := baseModel()
	p := panel.Panel{
		ID: "1", Kind: panel.Agent, Title: "worker", State: panel.Running,
		Cwd: "/work/api", Spark: "▂▃▅", Task: "do the thing", Favourite: true,
	}
	row := stripANSI(m.treeRow(dashItem{kind: itemPanel, panel: p}, false, 200))
	for _, want := range []string{"⊙", "worker", "AGENT", "running", "/work/api", "▂▃▅", "▸ do the thing"} {
		if !strings.Contains(row, want) {
			t.Errorf("the row should carry %q as the card did, got %q", want, row)
		}
	}
}

// TestGroupRowSummarises: a work item says how many panels it holds, how many
// sub-groups, and how many want a human — beside its name, where it reads as part
// of the name rather than as a column that is empty on every panel row.
func TestGroupRowSummarises(t *testing.T) {
	m := baseModel()
	it := dashItem{kind: itemGroup, name: "backend", need: 3, members: []panel.Panel{
		{State: panel.Running, Group: "backend/api"},
		{State: panel.Idle, Group: "backend/db"},
		{State: panel.Idle, Group: "backend"},
	}}
	row := stripANSI(m.treeRow(it, false, 160))
	for _, want := range []string{"backend", "(3)", "▣2", "3"} {
		if !strings.Contains(row, want) {
			t.Errorf("a group row should carry %q, got %q", want, row)
		}
	}
}

// TestExpansionShowsInTheGlyph: ▾ open, ▸ shut. It is the group's status glyph
// rather than an extra column — a row that is a container already says so by
// having one.
func TestExpansionShowsInTheGlyph(t *testing.T) {
	m := baseModel()
	base := dashItem{kind: itemGroup, name: "g", members: []panel.Panel{{State: panel.Idle}}}

	open, shut := base, base
	open.expanded = true
	if !strings.Contains(stripANSI(m.treeRow(open, false, 80)), "▾") {
		t.Error("an expanded group should show ▾")
	}
	if !strings.Contains(stripANSI(m.treeRow(shut, false, 80)), "▸") {
		t.Error("a collapsed group should show ▸")
	}
}

// TestNestingDrawsBranchGlyphs: the indentation and the ├─/└─ are the only things
// telling a reader where one work item's contents stop.
func TestNestingDrawsBranchGlyphs(t *testing.T) {
	m := baseModel()
	mid := dashItem{kind: itemPanel, depth: 1, panel: panel.Panel{Title: "mid", State: panel.Idle}}
	end := dashItem{kind: itemPanel, depth: 1, last: true, panel: panel.Panel{Title: "end", State: panel.Idle}}

	if !strings.Contains(stripANSI(m.treeRow(mid, false, 80)), "├─ ") {
		t.Error("a middle child should carry ├─")
	}
	if !strings.Contains(stripANSI(m.treeRow(end, false, 80)), "└─ ") {
		t.Error("the last child should carry └─")
	}
	top := dashItem{kind: itemPanel, panel: panel.Panel{Title: "top", State: panel.Idle}}
	if row := stripANSI(m.treeRow(top, false, 80)); strings.Contains(row, "├") || strings.Contains(row, "└") {
		t.Errorf("a top-level row has no branch to draw, got %q", row)
	}
}

// TestDeepRowKeepsItsName: nesting eats the width from the left, so a deep row
// drops the columns describing it rather than the name identifying it.
func TestDeepRowKeepsItsName(t *testing.T) {
	m := baseModel()
	it := samplePanelItem()
	it.depth = 12
	row := stripANSI(m.treeRow(it, false, 70))
	if !strings.Contains(row, "claude") {
		t.Fatalf("a deep row should keep enough of its name to identify it, got %q", row)
	}
}

// TestClipMeasuresTheWayTheLayoutDoes: the package's truncate counts East Asian
// ambiguous glyphs as two cells while lipgloss counts them as one, so a sparkline
// truncated by truncate comes out as "▂▃▅…" in a column with room for all of it.
func TestClipMeasuresTheWayTheLayoutDoes(t *testing.T) {
	const bars = "▂▃▅▇▆▃▁"
	if got := clip(bars, lipgloss.Width(bars)); got != bars {
		t.Fatalf("clip cut a string that fits: %q", got)
	}
	if got := lipgloss.Width(clip(bars, 4)); got > 4 {
		t.Fatalf("clip returned %d columns for a budget of 4", got)
	}
	if clip("anything", 0) != "" {
		t.Fatal("a zero budget renders nothing")
	}
}

// TestPreviewIsOffByDefault: the tree carries the state, the directory and the
// task the pane used to be the only place to see, so the pane earns its columns
// while you are watching a fleet and spends them for nothing while you are
// reorganising one.
func TestPreviewIsOffByDefault(t *testing.T) {
	m := baseModel()
	if m.preview {
		t.Fatal("the preview pane should start hidden")
	}
	m.mode, m.width, m.height = modeDashboard, 200, 40
	m.fleet = []panel.Panel{{ID: "1", Title: "solo", State: panel.Running, Cwd: "/work"}}
	m.showTree = true // one panel is a card; the pane this asserts about is the tree's

	// With the pane hidden the tree spends the whole width: a row reaches the task
	// column, which it could not if 48 columns were going to a pane beside it.
	wide := stripANSI(m.View())
	m.preview = true
	narrow := stripANSI(m.View())
	if len(wide) == 0 || len(narrow) == 0 {
		t.Fatal("both layouts should render")
	}
	if !strings.Contains(narrow, "state") {
		t.Fatalf("the preview should show the selected panel's metadata, got:\n%s", narrow)
	}
	if strings.Contains(wide, "state") {
		t.Fatalf("with the pane hidden nothing should draw its metadata block, got:\n%s", wide)
	}
}

// TestPreviewToggleBinding: v flips the pane and the flip is persisted, so a
// cockpit comes back the way it was left.
func TestPreviewToggleBinding(t *testing.T) {
	m := baseModel()
	m.mode = modeDashboard
	m = press(m, keyPreview)
	if !m.preview {
		t.Fatal("v should show the preview pane")
	}
	if !strings.Contains(m.status, "preview") {
		t.Fatalf("the status should say what happened, got %q", m.status)
	}
	m = press(m, keyPreview)
	if m.preview {
		t.Fatal("v should hide it again")
	}
}

// TestPreviewNotOfferedWhenItWouldCrampTheTree: two narrow panes tell you less
// than one usable one.
func TestPreviewNotOfferedWhenItWouldCrampTheTree(t *testing.T) {
	m := baseModel()
	m.mode, m.preview, m.height = modeDashboard, true, 40
	m.fleet = []panel.Panel{{ID: "1", Title: "solo", State: panel.Running}}

	m.width = 80 // 80 - chrome - 48 leaves the tree under previewMinTree
	if strings.Contains(stripANSI(m.View()), "state") {
		t.Fatal("a narrow terminal should keep the whole width for the tree")
	}
}
