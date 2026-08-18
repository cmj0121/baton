package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// lensFleetFixture: two work items, two profiles, two directories, three states.
func lensFleetFixture() []panel.Panel {
	return []panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "api a", State: panel.Running, Group: "backend", Profile: "claude", Cwd: "/w/api"},
		{ID: "2", Kind: panel.Agent, Title: "api b", State: panel.Attention, Group: "backend", Profile: "claude", Cwd: "/w/api"},
		{ID: "3", Kind: panel.Agent, Title: "heavy", State: panel.Running, Group: "frontend", Profile: "heavy", Cwd: "/w/web"},
		{ID: "4", Kind: panel.Shell, Title: "lone", State: panel.Idle, Cwd: "/w/web"},
	}
}

func lensModel() model {
	m := baseModel()
	m.mode = modeDashboard
	m.fleet = lensFleetFixture()
	return m
}

// TestLensRebucketsTheSameFleet: a lens is "compute a different parent path", so
// everything downstream — the tree, the fold, the cursor, the renderers — works on
// it unchanged.
func TestLensRebucketsTheSameFleet(t *testing.T) {
	for _, tc := range []struct {
		lens lens
		want []string
	}{
		{lensWork, []string{"backend", "frontend", "4"}},
		{lensProfile, []string{"claude", "heavy", "shells"}},
		{lensState, []string{"running", "attention", "idle"}},
	} {
		m := lensModel()
		m.lens = tc.lens
		got := m.topLevel()
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("group by %s: want %v, got %v", tc.lens, tc.want, got)
		}
	}
}

// TestLensByDirectoryNests is the case the lens exists for. Twelve git worktrees
// are twelve sibling directories: bucketed on the whole path they make twelve
// buckets holding one panel apiece, which is worse than no structure at all.
// Re-based against the fleet's common prefix and nested, they gather under the
// directory that actually holds them.
func TestLensByDirectoryNests(t *testing.T) {
	m := baseModel()
	m.mode, m.lens = modeDashboard, lensDir
	for i := 0; i < 4; i++ {
		m.fleet = append(m.fleet, panel.Panel{
			ID: string(rune('a' + i)), Title: "agent", State: panel.Running,
			Cwd: "/Users/x/work/baton-worktrees/feat-" + string(rune('0'+i)),
		})
	}
	m.fleet = append(m.fleet,
		panel.Panel{ID: "api", Title: "api", State: panel.Running, Cwd: "/Users/x/work/api"},
		panel.Panel{ID: "home", Title: "home", State: panel.Idle, Cwd: "/Users/x"})

	rows := rowNames(m.dashItems())
	// `work/api` holds one panel and no sub-directories, so it is promoted away and
	// its panel sits directly under `work` — the bucket would have cost two rows to
	// say what that panel's own directory column already says.
	want := []string{"work", "  work/baton-worktrees", "    #a", "    #b", "    #c", "    #d", "  #api", "#home"}
	if strings.Join(rows, "|") != strings.Join(want, "|") {
		t.Fatalf("the directory lens should nest under the shared prefix:\n got %v\nwant %v", rows, want)
	}
}

// TestLoneDirectoryBucketsArePromoted: a bucket holding nothing but one panel does
// not earn a row — it would cost two rows to say what the panel's own directory
// column already says. A bucket with sub-directories under it is kept whatever it
// holds directly: that one is carrying structure, not just a name.
func TestLoneDirectoryBucketsArePromoted(t *testing.T) {
	fleet := []panel.Panel{
		{ID: "1", Group: "trees/feat-a"},
		{ID: "2", Group: "trees/feat-b"},
		{ID: "3", Group: "trees"},
		{ID: "4", Group: "solo"},
	}
	got := map[string]string{}
	for _, p := range promoteLoneDirs(append([]panel.Panel(nil), fleet...)) {
		got[p.ID] = p.Group
	}
	for id, want := range map[string]string{
		"1": "trees", // a lone leaf: promoted to its parent
		"2": "trees",
		"3": "trees", // already there, and `trees` keeps its row: it has children
		"4": "",      // a lone top-level bucket promotes to no bucket at all
	} {
		if got[id] != want {
			t.Errorf("panel %s should land in %q, got %q", id, want, got[id])
		}
	}
}

// TestCommonDirPrefix: the deepest directory every panel sits under, ignoring the
// ones whose directory is unknown rather than letting them collapse it.
func TestCommonDirPrefix(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fleet []panel.Panel
		want  string
	}{
		{"a shared tree", []panel.Panel{{Cwd: "/a/b/c"}, {Cwd: "/a/b/d"}}, "/a/b"},
		{"nothing shared", []panel.Panel{{Cwd: "/a"}, {Cwd: "/b"}}, ""},
		{"one panel", []panel.Panel{{Cwd: "/a/b/c"}}, "/a/b/c"},
		{"unknown directories ignored", []panel.Panel{{Cwd: "/a/b"}, {Cwd: ""}, {Cwd: "/a/b/c"}}, "/a/b"},
		{"an empty fleet", nil, ""},
	} {
		if got := commonDirPrefix(tc.fleet); got != tc.want {
			t.Errorf("%s: commonDirPrefix = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestLensNeverMutatesTheFleet is the property that makes a lens safe: switching
// to `group by: state` must not move a single panel into a group called
// "attention". The model's own fleet keeps the real work items.
func TestLensNeverMutatesTheFleet(t *testing.T) {
	m := lensModel()
	before := make([]string, len(m.fleet))
	for i, p := range m.fleet {
		before[i] = p.Group
	}

	m.lens = lensState
	_ = m.dashItems()
	m = m.cycleLens(1)
	_ = m.dashItems()

	for i, p := range m.fleet {
		if p.Group != before[i] {
			t.Fatalf("the lens rewrote panel %s's group: %q -> %q", p.ID, before[i], p.Group)
		}
	}
}

// TestLensRefusesReorganising: a lens bucket is not a work item, so the verbs
// that change the fleet's shape are refused with a reason rather than acting or
// silently doing nothing.
func TestLensRefusesReorganising(t *testing.T) {
	c, cmds := recordingServer(t)
	m := lensModel()
	m.client = c
	m.lens = lensState
	m.cursorOnPanel(t, "1")

	for _, key := range []string{keyMark, keyGroup, keyAdd, keyUngroup, keyRename, " "} {
		m = press(m, key)
		if !strings.Contains(m.status, "is a view, not a work item") {
			t.Errorf("%q should be refused under a lens, got status %q", key, m.status)
		}
	}
	if m.grabbing() {
		t.Fatal("a grab must not start under a lens")
	}
	select {
	case cmd := <-cmds:
		t.Fatalf("a refused verb must send nothing, got %+v", cmd)
	default:
	}
}

// TestLensAllowsTheVerbsThatDoNotReshape: closing, signalling and dispatching act
// on panel ids, which mean the same thing whatever the tree is drawn from.
func TestLensAllowsTheVerbsThatDoNotReshape(t *testing.T) {
	m := lensModel()
	m.lens = lensProfile
	m.cursorOnPanel(t, "1")

	it, ok := m.selectedItem()
	if !ok || len(it.ids()) != 1 || it.ids()[0] != "1" {
		t.Fatalf("a panel row resolves to its own id under any lens, got %+v", it)
	}
	// A bucket row still resolves to the panels it holds, so a signal reaches them.
	m.cursorOnGroup(t, "claude")
	it, _ = m.selectedItem()
	if len(it.ids()) != 2 {
		t.Fatalf("the claude bucket should hold two panels, got %v", it.ids())
	}
}

// TestCycleLensKeepsTheCursorOnThePanel: buckets differ across lenses, so a row
// number means nothing — landing somewhere arbitrary in a fifty-row tree is
// indistinguishable from the dashboard having lost your place.
func TestCycleLensKeepsTheCursorOnThePanel(t *testing.T) {
	m := lensModel()
	m.cursorOnPanel(t, "3")

	for i := 0; i < len(lensOrder); i++ {
		m = m.cycleLens(1)
		it, ok := m.selectedItem()
		if !ok || it.kind != itemPanel || it.panel.ID != "3" {
			t.Fatalf("after cycling to %s the cursor left panel 3: %+v", m.lens, it)
		}
	}
	if !m.lens.real() {
		t.Fatalf("a full cycle should return to work items, got %s", m.lens)
	}
}

// TestCycleLensFromAGroupRowFollowsItsFirstPanel: a work item does not exist in
// any other lens, so the cursor follows the panel the row held.
func TestCycleLensFromAGroupRowFollowsItsFirstPanel(t *testing.T) {
	m := lensModel()
	m.cursorOnGroup(t, "backend")
	m = m.cycleLens(1)

	it, ok := m.selectedItem()
	if !ok || it.kind != itemPanel || it.panel.ID != "1" {
		t.Fatalf("the cursor should follow backend's first panel, got %+v", it)
	}
}

// TestCycleLensCancelsAGrab: a row in the air has nowhere to land in a tree built
// from different parents.
func TestCycleLensCancelsAGrab(t *testing.T) {
	m := lensModel()
	m.cursorOnPanel(t, "4")
	m = m.startGrab()
	if !m.grabbing() {
		t.Fatal("setup: the grab should have started")
	}
	m = m.cycleLens(1)
	if m.grabbing() {
		t.Fatal("switching lens must put the carried row down")
	}
}

// TestLensShowsInTheHeading: the tree looks the same under a lens as under the
// fleet's own work items, so the view has to say which it is.
func TestLensShowsInTheHeading(t *testing.T) {
	m := lensModel()
	m.width, m.height = 160, 40

	if strings.Contains(stripANSI(m.View()), "group by") {
		t.Fatal("the real tree is the default and needs no badge")
	}
	m.lens = lensState
	if !strings.Contains(stripANSI(m.View()), "group by: state") {
		t.Fatalf("a lens must be stated on the heading, got:\n%s", stripANSI(m.View()))
	}
}

// TestLensBindingCycles checks the key is bound, not only the method.
func TestLensBindingCycles(t *testing.T) {
	m := lensModel()
	m = press(m, keyLens)
	if m.lens.real() {
		t.Fatal("z should move off the work-item tree")
	}
	if !strings.Contains(m.status, "group by") {
		t.Fatalf("the status should name the lens, got %q", m.status)
	}
}

// TestProfileReachesTheCockpit is the wire half of the profile lens: the server
// has always recorded the profile, and it never travelled.
func TestProfileReachesTheCockpit(t *testing.T) {
	p := panel.FromProto(proto.Panel{ID: "1", Kind: "agent", Title: "x", Profile: "claude"})
	if p.Profile != "claude" {
		t.Fatalf("the profile should decode off the wire, got %q", p.Profile)
	}
	if p.ToProto().Profile != "claude" {
		t.Fatalf("…and encode back onto it, got %q", p.ToProto().Profile)
	}
}

// TestLensRefusesTheMutationsThatLookLikeViews covers the two verbs that are easy
// to miss when scoping a projection, because neither reads like a reorganisation.
//
// Favouriting a BUCKET would send group.favourite naming a group the server does
// not have, and the flag would then sit in its state for a "group" called
// `attention` for good. Reordering under a lens would write the lens's own
// ordering back into the fleet as a permanent one, still there after the lens was
// switched off.
func TestLensRefusesTheMutationsThatLookLikeViews(t *testing.T) {
	c, cmds := recordingServer(t)
	m := lensModel()
	m.client = c
	m.mode, m.lens = modeDashboard, lensState

	m.cursorOnGroup(t, "running") // a bucket, not a work item
	m = press(m, keyFavourite)
	if !strings.Contains(m.status, "is a view, not a work item") {
		t.Errorf("favouriting a bucket should be refused, got %q", m.status)
	}
	if len(m.favGroups) != 0 {
		t.Errorf("no bucket should be recorded as a favourite, got %v", m.favGroups)
	}

	m = press(m, "shift+down")
	if !strings.Contains(m.status, "is a view, not a work item") {
		t.Errorf("reordering under a lens should be refused, got %q", m.status)
	}

	select {
	case cmd := <-cmds:
		t.Fatalf("a refused verb must send nothing, got %+v", cmd)
	default:
	}
}

// TestLensStillFavouritesAPanel: a panel is a panel under any lens, so the one
// mutation that means the same thing everywhere is still allowed.
func TestLensStillFavouritesAPanel(t *testing.T) {
	c, cmds := recordingServer(t)
	m := lensModel()
	m.client = c
	m.mode, m.lens = modeDashboard, lensProfile
	m.cursorOnPanel(t, "1")

	m = press(m, keyFavourite)
	if got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.favourite" }); got.ID != "1" {
		t.Fatalf("favouriting a panel should still work under a lens, got %+v", got)
	}
}
