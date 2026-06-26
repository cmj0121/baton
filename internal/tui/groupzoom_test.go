package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestRenderTileHeadStaysOneRow guards against a long title (with a pin glyph or
// interact badge taking head space) wrapping the head onto a second row: a tile
// is exactly head (1) + body (emuRows) + border (2) tall.
func TestRenderTileHeadStaysOneRow(t *testing.T) {
	const emuCols, emuRows = 24, 5
	long := "claude · refactor the auth module and write the tests"
	for _, tc := range []struct {
		name string
		mut  func(*model, panel.Panel)
	}{
		{"pinned", func(m *model, p panel.Panel) { m.groupPinned = map[string]bool{p.ID: true} }},
		{"interacting", func(m *model, _ panel.Panel) { m.groupInteract = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := baseModel()
			p := panel.Panel{ID: "x", Title: long, State: panel.Running}
			tc.mut(&m, p)
			if h := lipgloss.Height(m.renderTile(p, true, emuCols, emuRows, gtileGap)); h != emuRows+3 {
				t.Fatalf("head should stay one row: tile height %d, want %d", h, emuRows+3)
			}
		})
	}
}

func TestGroupMembers(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.groupName = "api"
	ids := ""
	for _, p := range m.groupMembers() {
		ids += p.ID
	}
	if ids != "136" {
		t.Fatalf("api members should be 1,3,6 in order, got %q", ids)
	}
}

func TestGroupZoomNavigation(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0]) // api, 3 members
	if m.mode != modeGroupZoom || m.groupName != "api" {
		t.Fatalf("zoomGroup should enter the split, got mode=%v group=%q", m.mode, m.groupName)
	}

	step := func(k string) {
		nm, _ := m.handleGroupZoomKey(key(k))
		m = nm.(model)
	}
	step("tab")
	if m.groupFocus != 1 {
		t.Fatalf("tab should move focus to 1, got %d", m.groupFocus)
	}
	step("tab")
	step("tab") // 2 -> wrap to 0
	if m.groupFocus != 0 {
		t.Fatalf("tab should wrap to 0, got %d", m.groupFocus)
	}
	step("shift+tab")
	if m.groupFocus != 2 {
		t.Fatalf("shift+tab should wrap back to 2, got %d", m.groupFocus)
	}
}

func TestGroupZoomEnterAndBack(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0])
	m.groupFocus = 1 // member id "3"

	nm, _ := m.handleGroupZoomKey(key("enter"))
	m = nm.(model)
	if m.mode != modeZoom || m.zoomID != "3" || m.zoomGroupOrigin != "api" {
		t.Fatalf("enter should zoom member 3 with group origin, got mode=%v id=%q origin=%q", m.mode, m.zoomID, m.zoomGroupOrigin)
	}

	// prefix+g no longer leaves a zoom — the freed C-t g slot now opens the git
	// menu on the zoomed agent. esc closes it back to the zoom.
	nm, _ = m.handleZoomKey(key("ctrl+t")) // arm the prefix
	m = nm.(model)
	nm, _ = m.handleZoomKey(key("g"))
	m = nm.(model)
	if m.mode != modeGit {
		t.Fatalf("prefix+g should open the git menu, got mode=%v", m.mode)
	}
	nm, _ = m.handleGitKey("esc")
	m = nm.(model)
	if m.mode != modeZoom {
		t.Fatalf("esc should close the git menu back to the zoom, got mode=%v", m.mode)
	}

	// prefix+b (back) pops back to the group split and forgets the origin.
	nm, _ = m.handleZoomKey(key("ctrl+t")) // arm the prefix
	m = nm.(model)
	nm, _ = m.handleZoomKey(key("b"))
	m = nm.(model)
	if m.mode != modeGroupZoom || m.groupName != "api" || m.zoomGroupOrigin != "" {
		t.Fatalf("prefix+b should return to the api split, got mode=%v group=%q origin=%q", m.mode, m.groupName, m.zoomGroupOrigin)
	}
}

// TestBackPopsOneLevel covers the back action's contextual pop in each view: the
// split → dashboard, a dashboard-opened zoom → dashboard (the case C-t g never
// handled), the summary sub-view → parent group, and the dashboard as the root.
func TestBackPopsOneLevel(t *testing.T) {
	t.Run("dashboard is the root", func(t *testing.T) {
		nm, _ := baseModel().runAction(actBack)
		if got := nm.(model); got.mode != modeDashboard || !strings.Contains(got.status, "dashboard") {
			t.Fatalf("back on the dashboard should no-op with a hint, got mode=%v status=%q", got.mode, got.status)
		}
	})

	t.Run("dashboard zoom backs to the dashboard", func(t *testing.T) {
		m := baseModel()
		m.fleet = groupedFleet()
		m = m.zoomInto(panel.Panel{ID: "1", Title: "lone", State: panel.Running})
		if m.zoomGroupOrigin != "" {
			t.Fatalf("a direct zoom should carry no group origin, got %q", m.zoomGroupOrigin)
		}
		nm, _ := m.runAction(actBack)
		if got := nm.(model); got.mode != modeDashboard {
			t.Fatalf("back from a dashboard zoom should reach the dashboard, got mode=%v", got.mode)
		}
	})

	t.Run("bare b in the split backs to the dashboard", func(t *testing.T) {
		m := baseModel()
		m.fleet = groupedFleet()
		m = m.zoomGroup(m.dashItems()[0])
		nm, _ := m.handleGroupZoomKey(key(keyBack))
		if got := nm.(model); got.mode != modeDashboard {
			t.Fatalf("bare b in the split should reach the dashboard, got mode=%v", got.mode)
		}
	})

	t.Run("back from the summary sub-view returns to the parent group", func(t *testing.T) {
		m := baseModel()
		m.fleet = groupedFleet()
		m = m.zoomGroup(m.dashItems()[0])
		m.summaryScope = true
		nm, _ := m.runAction(actBack)
		if got := nm.(model); got.mode != modeGroupZoom || got.summaryScope {
			t.Fatalf("back in the summary scope should return to the group split, got mode=%v summary=%v", got.mode, got.summaryScope)
		}
	})
}

func TestGroupZoomExit(t *testing.T) {
	// The bare dashboard key (d) and esc both leave the split for the dashboard.
	for _, k := range []string{"d", "esc"} {
		t.Run(k, func(t *testing.T) {
			m := baseModel()
			m.fleet = groupedFleet()
			m = m.zoomGroup(m.dashItems()[0])
			nm, _ := m.handleGroupZoomKey(key(k))
			if nm.(model).mode != modeDashboard {
				t.Fatalf("%q should exit the split to the dashboard", k)
			}
		})
	}
}

func TestRemoveFocusedMember(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0]) // api, members 1,3,6
	m.groupFocus = 1                  // member id "3"

	nm, _ := m.handleGroupZoomKey(key(keyRemove))
	m = nm.(model)
	if !strings.Contains(m.status, "removed") {
		t.Fatalf("removing the focused member should report it, got %q", m.status)
	}

	// An out-of-range focus is a no-op, not a panic.
	m.groupFocus = 99
	before := m.status
	nm, _ = m.handleGroupZoomKey(key(keyRemove))
	if got := nm.(model).status; got != before {
		t.Fatalf("removing with an out-of-range focus should be a no-op, got %q", got)
	}
}

// TestRemoveMemberLive drives the real path: group two shells, open the split,
// remove the focused one, and confirm the server drops it from the group.
func TestRemoveMemberLive(t *testing.T) {
	c, a := zoomServer(t)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create second: %v", err)
	}
	b := (<-c.Events).Panels[1].ID
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a, b}, Group: "grp"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	snap := <-c.Events

	m := model{client: c, width: 100, height: 30, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m.fleet = mergeFleet(snap.Panels)
	m = m.zoomGroup(m.dashItems()[0])
	m.groupFocus = 0 // member a

	m = m.removeFocusedMember()
	got := <-c.Events // the broadcast after the server drops a from the group
	groupOf := func(id string) string {
		for _, p := range got.Panels {
			if p.ID == id {
				return p.Group
			}
		}
		return "<missing>"
	}
	if g := groupOf(a); g != "" {
		t.Fatalf("member a should have left grp, got %q", g)
	}
	if g := groupOf(b); g != "grp" {
		t.Fatalf("member b should remain in grp, got %q", g)
	}
}

func TestGroupZoomRenders(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0])
	if m.View() == "" {
		t.Fatal("group zoom should render its split")
	}
}

func TestGroupZoomLiveTileRenders(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.mode = modeGroupZoom
	m.groupName = "api"

	// Inject a live emulator for member #1 and feed it output: the tile body
	// should show the emulator's screen, exactly emuRows tall.
	emu := vt.NewSafeEmulator(30, 6)
	_, _ = emu.Write([]byte("hello-live"))
	m.groupEmus = map[string]*vt.SafeEmulator{"1": emu}

	body := m.tileBody(m.groupMembers()[0], 30, 6, false, false) // member id "1"
	if len(body) != 6 {
		t.Fatalf("a live tile should have 6 rows, got %d", len(body))
	}
	if !strings.Contains(strings.Join(body, ""), "hello-live") {
		t.Fatalf("live tile should show emulator output, got %q", strings.Join(body, ""))
	}
	// A passive tile draws no cursor; the interacting tile overlays one.
	if strings.Contains(strings.Join(body, ""), "\x1b[7m") {
		t.Fatal("a passive tile should not draw a cursor")
	}
	cursored := m.tileBody(m.groupMembers()[0], 30, 6, true, true)
	if !strings.Contains(strings.Join(cursored, ""), "\x1b[7m") {
		t.Fatalf("the interacting tile should overlay a reverse-video cursor, got %q", strings.Join(cursored, ""))
	}
	if m.View() == "" {
		t.Fatal("live group zoom should render")
	}
	m.closeGroupEmus()
	if m.groupEmus != nil {
		t.Fatal("closeGroupEmus should drop the emulator map")
	}
}

// A direct zoom from the dashboard has no group origin, so ctrl+g is just input.
func TestDirectZoomHasNoGroupOrigin(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.cursor = 1 // lone shell #2
	nm, _ := m.activate()
	m = nm.(model)
	if m.mode != modeZoom || m.zoomGroupOrigin != "" {
		t.Fatalf("a dashboard zoom should carry no group origin, got mode=%v origin=%q", m.mode, m.zoomGroupOrigin)
	}
}

// TestGroupZoomLiveMosaic drives the real end-to-end path: group two shells,
// open the split, and confirm each tile streams its own panel's output.
func TestGroupZoomLiveMosaic(t *testing.T) {
	c, a := zoomServer(t) // server + client + one shell (id a)

	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create second: %v", err)
	}
	b := (<-c.Events).Panels[1].ID

	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a, b}, Group: "grp"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	snap := <-c.Events

	m := model{client: c, width: 100, height: 30, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m.fleet = mergeFleet(snap.Panels)
	m = m.zoomGroup(m.dashItems()[0])
	if len(m.groupEmus) != 2 {
		t.Fatalf("expected 2 live tiles, got %d", len(m.groupEmus))
	}

	if err := c.Send(proto.Command{Action: "panel.input", ID: a, Data: []byte("echo AAA-xyz\n")}); err != nil {
		t.Fatalf("input a: %v", err)
	}
	if err := c.Send(proto.Command{Action: "panel.input", ID: b, Data: []byte("echo BBB-xyz\n")}); err != nil {
		t.Fatalf("input b: %v", err)
	}

	// Pump output into the matching tile emulator, exactly as Update does.
	gotA, gotB := false, false
	deadline := time.After(5 * time.Second)
	for !gotA || !gotB {
		select {
		case msg := <-c.Output:
			if emu := m.groupEmus[msg.ID]; emu != nil {
				_, _ = emu.Write(msg.Data)
			}
			if e := m.groupEmus[a]; e != nil && strings.Contains(e.Render(), "AAA-xyz") {
				gotA = true
			}
			if e := m.groupEmus[b]; e != nil && strings.Contains(e.Render(), "BBB-xyz") {
				gotB = true
			}
		case <-deadline:
			t.Fatalf("tiles never rendered both outputs: a=%v b=%v", gotA, gotB)
		}
	}

	if !strings.Contains(m.View(), "GROUP") {
		t.Fatal("group split view should show its footer")
	}

	// A member exiting mid-split: the server flags that tile's stream once, and
	// the client paints it into the tile (the panel stays in the split).
	if err := c.Send(proto.Command{Action: "panel.input", ID: a, Data: []byte("exit\n")}); err != nil {
		t.Fatalf("exit a: %v", err)
	}
	exitDeadline := time.After(5 * time.Second)
	for sawExit := false; !sawExit; {
		select {
		case msg := <-c.Output:
			if emu := m.groupEmus[msg.ID]; emu != nil {
				_, _ = emu.Write(msg.Data)
			}
			if msg.ID == a && strings.Contains(string(msg.Data), "process exited") {
				sawExit = true
			}
		case <-exitDeadline:
			t.Fatal("the exited member's tile never saw the exit marker")
		}
	}

	next, _ := m.exitGroupZoom()
	m = next.(model)
	if m.groupEmus != nil {
		t.Fatal("exiting the split should tear down the tiles")
	}
}

// snapshot turns a panel fleet into the "panels" server message a broadcast
// carries, so a test can drive applyEvent's reconciliation directly.
func snapshot(fleet []panel.Panel) proto.ServerMsg {
	ps := make([]proto.Panel, len(fleet))
	for i, p := range fleet {
		ps[i] = proto.Panel{ID: p.ID, Kind: p.Kind.String(), Title: p.Title, State: p.State.String(), Group: p.Group}
	}
	return proto.ServerMsg{Type: "panels", Panels: ps}
}

func TestGroupAutoExitsWhenEmptied(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0]) // api

	// A snapshot where every api member is gone leaves the split for the dash.
	m.applyEvent(snapshot([]panel.Panel{
		{ID: "2", Kind: panel.Shell, Title: "lone shell", State: panel.Idle},
	}))
	if m.mode != modeDashboard || m.groupName != "" {
		t.Fatalf("an emptied group should drop to the dashboard, got mode=%v group=%q", m.mode, m.groupName)
	}
}

func TestGroupFocusClampsOnShrink(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0]) // api, 3 members
	m.groupFocus = 2                  // last member

	// api shrinks to a single member: the focus must fall back onto a real tile.
	m.applyEvent(snapshot([]panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "api · a", State: panel.Running, Group: "api"},
	}))
	if m.mode != modeGroupZoom {
		t.Fatalf("a still-populated group should stay in the split, got mode=%v", m.mode)
	}
	if m.groupFocus != 0 {
		t.Fatalf("focus should clamp onto the remaining tile, got %d", m.groupFocus)
	}
}

func TestPruneMarksOnSnapshot(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.marked = map[string]bool{"2": true, "5": true}

	// Panel 5 vanishes from the fleet: its stale mark is dropped, 2 survives.
	m.applyEvent(snapshot([]panel.Panel{
		{ID: "2", Kind: panel.Shell, Title: "lone shell", State: panel.Idle},
	}))
	if m.marked["5"] {
		t.Fatal("a mark on a departed panel should be pruned")
	}
	if !m.marked["2"] {
		t.Fatal("a mark on a surviving panel should remain")
	}
}

func TestLiveMembersCap(t *testing.T) {
	m := baseModel()
	m.groupName = "big"
	fleet := make([]panel.Panel, 0, maxGroupTiles+5)
	for i := 0; i < maxGroupTiles+5; i++ {
		fleet = append(fleet, panel.Panel{ID: string(rune('a' + i)), Title: "p", State: panel.Running, Group: "big"})
	}
	m.fleet = fleet
	if got := len(m.groupMembers()); got != maxGroupTiles+5 {
		t.Fatalf("the group should keep all %d members, got %d", maxGroupTiles+5, got)
	}
	if got := len(m.tileMembers()); got != maxGroupTiles {
		t.Fatalf("live tiles should cap at %d, got %d", maxGroupTiles, got)
	}
}

// bigGroup is a group of n members all filed under name, for the cap and overflow
// paths.
func bigGroup(name string, n int) []panel.Panel {
	fleet := make([]panel.Panel, 0, n)
	for i := 0; i < n; i++ {
		fleet = append(fleet, panel.Panel{
			ID: string(rune('a' + i)), Title: "p", State: panel.Running, Group: name,
		})
	}
	return fleet
}

// TestGroupPinCuratesTiles checks that pinning over-cap switches the split to the
// pinned set: the pinned panel becomes the only tile and everyone else, including
// the formerly-auto-filled tiles, collapses into the summary. Unpinning restores
// the default fill.
func TestGroupPinCuratesTiles(t *testing.T) {
	m := baseModel()
	m.fleet = bigGroup("big", maxGroupTiles+4) // a..t: default tiles a..p, collapsed q..t
	m = m.zoomGroup(m.dashItems()[0])

	tiles, collapsed := m.splitMembers()
	if indexOfMember(tiles, "q") >= 0 || indexOfMember(collapsed, "q") < 0 {
		t.Fatal("q should start collapsed, not a tile")
	}

	// Pin q (a collapsed member) — the grid curates to just the pinned set. (The
	// real keystroke path through the summary scope is covered by
	// TestGroupPinCapRefused; here we drive the pin set directly.)
	m.groupPinned = map[string]bool{"q": true}
	m.reconcileGroupTiles("")
	tiles, _ = m.splitMembers()
	if len(tiles) != 1 || indexOfMember(tiles, "q") < 0 {
		t.Fatalf("the only tile should be the pinned q, got %v", ids(tiles))
	}
	// Every member is still reachable: the one tile plus the summary slot folding
	// the other 19, so focus walks 2 slots.
	if got := m.focusCount(); got != 2 {
		t.Fatalf("focus should walk the tile + summary slot (2), got %d", got)
	}

	// Unpinning restores the default fill with q back in the collapsed set.
	delete(m.groupPinned, "q")
	m.reconcileGroupTiles("")
	tiles, _ = m.splitMembers()
	if len(tiles) != maxGroupTiles || indexOfMember(tiles, "q") >= 0 {
		t.Fatal("unpinning should restore the default fill with q back in the summary")
	}
}

// ids is the member ids of a slice, for test diagnostics.
func ids(ps []panel.Panel) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

// TestGroupPinCapRefused checks the pin set cannot exceed maxGroupTiles. With the
// cap's worth already pinned, an unpinned collapsed member — reached through the
// summary scope, where it shows as a tile — cannot be pinned too.
func TestGroupPinCapRefused(t *testing.T) {
	m := baseModel()
	m.fleet = bigGroup("big", maxGroupTiles+4)
	m = m.zoomGroup(m.dashItems()[0])
	m.groupPinned = map[string]bool{}
	for i := 0; i < maxGroupTiles; i++ {
		m.groupPinned[string(rune('a'+i))] = true // pin the cap's worth
	}

	// q..t are the unpinned, collapsed members; open the summary scope so they show
	// as tiles, then focus the first and try to pin it.
	m.summaryScope = true
	m.groupFocus = 0
	if p, ok := m.focusedMember(); !ok || p.ID != "q" {
		t.Fatalf("summary scope should focus the first collapsed member q, got %q ok=%v", p.ID, ok)
	}
	before := m.pinnedCount()
	m = m.togglePin()
	if m.pinnedCount() != before {
		t.Fatalf("pinning beyond the cap should be refused, count went %d→%d", before, m.pinnedCount())
	}
	if !strings.Contains(m.status, "unpin one first") {
		t.Fatalf("status should explain the cap, got %q", m.status)
	}
}

// TestPinsSurviveSnapshotInSummaryScope is a regression test: a "panels" snapshot
// landing while the summary sub-view is open used to rebuild the pin set from the
// scope-narrowed members (the collapsed half), dropping the pinned tiles' flags and
// reverting the user's curation on exit. The pin set must derive from the parent
// group's FULL membership regardless of scope.
func TestPinsSurviveSnapshotInSummaryScope(t *testing.T) {
	m := baseModel()
	m.fleet = bigGroup("big", maxGroupTiles+4) // a..t
	m = m.zoomGroup(m.dashItems()[0])

	// Curate: pin a and b, so the group shows just those tiles and folds the rest.
	m.groupPinned = map[string]bool{"a": true, "b": true}
	m.reconcileGroupTiles("")

	// Open the summary sub-view (scoped to the collapsed half), then a server
	// snapshot lands carrying the authoritative pins for a and b.
	m.summaryScope = true
	ps := make([]proto.Panel, len(m.fleet))
	for i, p := range m.fleet {
		ps[i] = proto.Panel{ID: p.ID, Kind: p.Kind.String(), Title: p.Title, State: p.State.String(), Group: "big", Pinned: p.ID == "a" || p.ID == "b"}
	}
	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: ps})

	if !m.groupPinned["a"] || !m.groupPinned["b"] || len(m.groupPinned) != 2 {
		t.Fatalf("pins must survive a snapshot during summary scope, got %v", m.groupPinned)
	}
}

// TestInteractOnSummaryNoOps checks interact (and the other member actions) refuse
// the summary slot with a hint, since it is not a live panel.
func TestInteractOnSummaryNoOps(t *testing.T) {
	m := baseModel()
	m.fleet = bigGroup("big", maxGroupTiles+4) // 16 tiles + 4 collapsed
	m = m.zoomGroup(m.dashItems()[0])
	m.groupFocus = maxGroupTiles // the summary slot, just past the last tile
	if !m.focusedIsSummary() {
		t.Fatalf("focus %d should rest on the summary slot", m.groupFocus)
	}

	for _, k := range []string{keyInteract, keyPin, keySignal, keyRemove} {
		nm, _ := m.handleGroupZoomKey(key(k))
		got := nm.(model)
		if got.groupInteract {
			t.Fatalf("%q should not start interact on the summary", k)
		}
		if !strings.Contains(got.status, "summary") {
			t.Fatalf("%q on the summary should hint, got %q", k, got.status)
		}
	}
}

// TestSummaryTileRenders checks the summary tile is drawn for a large group, with
// its "+N more" rollup header in place of the old tree pane.
func TestSummaryTileRenders(t *testing.T) {
	m := baseModel()
	m.fleet = bigGroup("big", maxGroupTiles+4)
	m = m.zoomGroup(m.dashItems()[0])
	m.groupFocus = maxGroupTiles // the summary slot, so it glows focused
	if !strings.Contains(m.groupZoomView(), "+4 more") {
		t.Fatal("the split should render the summary tile rolling up the collapsed members")
	}
}

// TestGroupSplitCapsVisibleTiles checks a large group streams at most N live tiles
// (default maxGroupTiles), folds the rest into the summary, and says so in the
// header.
func TestGroupSplitCapsVisibleTiles(t *testing.T) {
	m := baseModel()
	m.fleet = bigGroup("big", maxGroupTiles+4)
	m = m.zoomGroup(m.dashItems()[0])

	tiles, collapsed := m.splitMembers()
	if len(tiles) != maxGroupTiles {
		t.Fatalf("live tiles should cap at %d, got %d", maxGroupTiles, len(tiles))
	}
	if len(collapsed) != 4 {
		t.Fatalf("the 4 overflow members should be collapsed, got %d", len(collapsed))
	}
	view := m.groupZoomView()
	if !strings.Contains(view, "16 live · 4 summarised") {
		t.Fatalf("the header should report the live/summarised split, got:\n%s", view)
	}
}

// TestGroupSplitFocusReachesSummary checks focus walks the live tiles then the
// summary slot — so a large group's overflow is reachable through the summary.
func TestGroupSplitFocusReachesSummary(t *testing.T) {
	m := baseModel()
	m.fleet = bigGroup("big", maxGroupTiles+4) // 16 tiles + the summary slot
	m = m.zoomGroup(m.dashItems()[0])

	// shift+tab from the first tile wraps to the summary slot, just past the last tile.
	nm, _ := m.handleGroupZoomKey(key("shift+tab"))
	m = nm.(model)
	if m.groupFocus != maxGroupTiles {
		t.Fatalf("focus should wrap to the summary slot (%d), got %d", maxGroupTiles, m.groupFocus)
	}
	if _, ok := m.focusedMember(); ok {
		t.Fatal("the summary slot is not a tile, so the focus should not resolve to a member")
	}
	if !m.focusedIsSummary() {
		t.Fatal("the focus should rest on the summary slot")
	}
}

// TestEnterSummaryScope checks enter on the summary slot opens the collapsed
// members as their own grid (summaryScope), and esc returns to the parent group.
func TestEnterSummaryScope(t *testing.T) {
	m := baseModel()
	m.fleet = bigGroup("big", maxGroupTiles+4) // 16 tiles + 4 collapsed
	m = m.zoomGroup(m.dashItems()[0])
	m.groupFocus = maxGroupTiles // the summary slot

	nm, _ := m.handleGroupZoomKey(key("enter"))
	m = nm.(model)
	if !m.summaryScope {
		t.Fatal("enter on the summary should enter the summary scope")
	}
	if m.mode != modeGroupZoom || m.groupName != "big" {
		t.Fatalf("the sub-view should stay in the split scoped to big, got mode=%v group=%q", m.mode, m.groupName)
	}
	// The scoped view shows exactly the parent's 4 collapsed members as tiles, with
	// no further summary.
	tiles, collapsed := m.splitMembers()
	if len(tiles) != 4 || len(collapsed) != 0 {
		t.Fatalf("the summary scope should show the 4 collapsed members as tiles, got %d + %d", len(tiles), len(collapsed))
	}

	// esc returns to the parent group view, not the dashboard.
	nm, _ = m.handleGroupZoomKey(key("esc"))
	m = nm.(model)
	if m.summaryScope {
		t.Fatal("esc should clear the summary scope")
	}
	if m.mode != modeGroupZoom || m.groupName != "big" {
		t.Fatalf("esc from the sub-view should return to the parent group, got mode=%v group=%q", m.mode, m.groupName)
	}
}

// TestGroupFocusFollowsPanelAcrossSnapshot checks the split keeps focus on the
// same panel by id when the roster shifts under it (gap #8).
func TestGroupFocusFollowsPanelAcrossSnapshot(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()          // api members in fleet order: 1, 3, 6
	m = m.zoomGroup(m.dashItems()[0]) // the api group
	m.groupFocus = 2                  // rest on member #6
	if id := m.focusedMemberID(); id != "6" {
		t.Fatalf("focus should rest on member 6, got %q", id)
	}

	// A snapshot drops the first member (1); 3 and 6 remain, so #6 slides to index 1.
	nf := make([]panel.Panel, 0)
	for _, p := range groupedFleet() {
		if p.ID != "1" {
			nf = append(nf, p)
		}
	}
	m.applyEvent(snapshot(nf))

	if id := m.focusedMemberID(); id != "6" {
		t.Fatalf("focus should still follow panel 6, got %q (focus=%d)", id, m.groupFocus)
	}
	if m.groupFocus != 1 {
		t.Fatalf("panel 6 should now sit at focus index 1, got %d", m.groupFocus)
	}
}

// liveSplit opens the api group's split and injects a drained live emulator per
// member, so interact-mode key routing can be exercised without a server. Each
// tile's input side is drained (zoomReader with no client) so feedKey — which
// writes to a synchronous pipe — never blocks.
func liveSplit(t *testing.T) model {
	t.Helper()
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0]) // the api group: members 1, 3, 6
	m.groupEmus = map[string]*vt.SafeEmulator{}
	for _, id := range []string{"1", "3", "6"} {
		emu := vt.NewSafeEmulator(20, 5)
		m.groupEmus[id] = emu
		go zoomReader(emu, nil, id)
		t.Cleanup(func() { closeZoom(emu) })
	}
	return m
}

// TestGroupInteractToggle checks i enters interact mode on a live tile (and is a
// no-op with a hint on a preview-only one), and that C-t i leaves it.
func TestGroupInteractToggle(t *testing.T) {
	// Without a live tile (no client) interact cannot start.
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0])
	if got := m.enterInteract(); got.groupInteract || !strings.Contains(got.status, "live panel") {
		t.Fatalf("interact without a live tile should hint, got interact=%v status=%q", got.groupInteract, got.status)
	}

	// With live tiles, i enters interact and the footer flips to INTERACT.
	m = liveSplit(t)
	nm, _ := m.handleGroupZoomKey(key(keyInteract))
	m = nm.(model)
	if !m.groupInteract {
		t.Fatal("i should enter interact mode")
	}
	if !strings.Contains(m.groupZoomFooter(), "INTERACT") {
		t.Fatal("the split footer should show INTERACT while interacting")
	}
	if !strings.Contains(m.groupZoomView(), "⌨") {
		t.Fatal("the focused tile should wear the interact badge")
	}
	if !strings.Contains(m.groupZoomView(), "\x1b[7m") {
		t.Fatal("the interacting tile should show a cursor")
	}

	// C-t i returns to navigation.
	a, _ := m.handleGroupZoomKey(key("ctrl+t"))
	b, _ := a.(model).handleGroupZoomKey(key(keyInteract))
	if b.(model).groupInteract {
		t.Fatal("C-t i should stop interacting")
	}
}

// TestGroupInteractCapturesBareKeys checks that while interacting the split's own
// navigation keys are handed to the focused program instead of steering the split;
// only the prefixed escapes still act.
func TestGroupInteractCapturesBareKeys(t *testing.T) {
	m := liveSplit(t)
	m.groupFocus = 0
	m = m.enterInteract()

	// Keys that would navigate, remove, resize, or open help now go to the program.
	for _, k := range []string{"j", "tab", keyRemove, "+", keyHelp} {
		nm, _ := m.handleGroupZoomKey(key(k))
		m = nm.(model)
	}
	if m.groupFocus != 0 {
		t.Fatalf("bare keys in interact should not move focus, got %d", m.groupFocus)
	}
	if m.mode != modeGroupZoom {
		t.Fatalf("bare keys in interact should stay in the split, got mode=%v", m.mode)
	}

	// The bare dashboard key is captured too; only C-t d leaves.
	nm, _ := m.handleGroupZoomKey(key(m.bindingKey(actDashboard)))
	if nm.(model).mode != modeGroupZoom {
		t.Fatalf("the bare dashboard key should be captured by interact, got mode=%v", nm.(model).mode)
	}
	a, _ := m.handleGroupZoomKey(key("ctrl+t"))
	d, _ := a.(model).handleGroupZoomKey(key(m.bindingKey(actDashboard)))
	if d.(model).mode != modeDashboard {
		t.Fatal("C-t d should still leave interact for the dashboard")
	}
}

// TestGroupInteractEndsWhenPanelLeaves checks interact stops when a snapshot pulls
// the panel being typed into out of the group, so keys never land on a tile the
// focus merely fell onto.
func TestGroupInteractEndsWhenPanelLeaves(t *testing.T) {
	m := liveSplit(t)
	m.groupFocus = 0 // member "1"
	m = m.enterInteract()
	if !m.groupInteract {
		t.Fatal("expected to be interacting")
	}

	nf := groupedFleet()
	for i := range nf {
		if nf[i].ID == "1" {
			nf[i].Group = "" // member 1 leaves the api group
		}
	}
	m.applyEvent(snapshot(nf))
	if m.groupInteract {
		t.Fatal("interact should end when the focused panel leaves the group")
	}
}

// TestGroupInteractDrivesPanel is the end-to-end path: group two shells, open the
// split, interact with the focused tile, type a command, and confirm that tile —
// and only that tile — reflects it, all without zooming in.
func TestGroupInteractDrivesPanel(t *testing.T) {
	c, a := zoomServer(t)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create second: %v", err)
	}
	b := (<-c.Events).Panels[1].ID
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a, b}, Group: "grp"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	snap := <-c.Events

	m := model{client: c, width: 100, height: 30, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m.fleet = mergeFleet(snap.Panels)
	m = m.zoomGroup(m.dashItems()[0])
	m.groupFocus = 0 // focus member a
	if id := m.focusedMemberID(); id != a {
		t.Fatalf("focus should be on member a (%s), got %q", a, id)
	}

	// Enter interact and type into a — staying in the split, not a single zoom.
	nm, _ := m.handleGroupZoomKey(key(keyInteract))
	m = nm.(model)
	if !m.groupInteract || m.mode != modeGroupZoom {
		t.Fatalf("i should interact in place, got interact=%v mode=%v", m.groupInteract, m.mode)
	}
	for _, r := range "echo grp-interact" {
		nm, _ := m.handleGroupZoomKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(string(r))})
		m = nm.(model)
	}
	nm, _ = m.handleGroupZoomKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(model)

	// a's tile echoes the typed command; b's must never see it.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-c.Output:
			if emu := m.groupEmus[msg.ID]; emu != nil {
				_, _ = emu.Write(msg.Data)
			}
			if e := m.groupEmus[a]; e != nil && strings.Contains(e.Render(), "grp-interact") {
				return // success
			}
			if e := m.groupEmus[b]; e != nil && strings.Contains(e.Render(), "grp-interact") {
				t.Fatal("only the focused tile (a) should receive the keystrokes, not b")
			}
		case <-deadline:
			t.Fatal("the focused tile never echoed the interacted command")
		}
	}
}

// TestReconcileTilesLive drives the real path: group two shells, open the split,
// remove one through the server, and confirm the split tears down just that tile.
func TestReconcileTilesLive(t *testing.T) {
	c, a := zoomServer(t)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create second: %v", err)
	}
	b := (<-c.Events).Panels[1].ID
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a, b}, Group: "grp"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	snap := <-c.Events

	m := model{client: c, width: 100, height: 30, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m.fleet = mergeFleet(snap.Panels)
	m = m.zoomGroup(m.dashItems()[0])
	if len(m.groupEmus) != 2 {
		t.Fatalf("expected 2 live tiles, got %d", len(m.groupEmus))
	}

	// Remove a from the group; the broadcast snapshot drives reconciliation.
	if err := c.Send(proto.Command{Action: "panel.ungroup", IDs: []string{a}}); err != nil {
		t.Fatalf("remove a: %v", err)
	}
	m.applyEvent(<-c.Events)
	if m.mode != modeGroupZoom {
		t.Fatalf("the group still has a member, should stay in the split, got mode=%v", m.mode)
	}
	if len(m.groupEmus) != 1 || m.groupEmus[b] == nil {
		t.Fatalf("only b's tile should remain, got %d tiles", len(m.groupEmus))
	}
	if m.groupEmus[a] != nil {
		t.Fatal("a's tile should have been torn down")
	}
	next, _ := m.exitGroupZoom()
	_ = next
}

func TestTileGeometryFillsScreen(t *testing.T) {
	// Two members on a wide screen: two side-by-side tiles that split the width
	// and use the full height.
	cols, ec, er := tileGeometry(2, 100, 30, 0)
	if cols != 2 {
		t.Fatalf("2 members on a wide screen want 2 columns, got %d", cols)
	}
	if ec < 40 {
		t.Fatalf("each tile should be roughly half the width, got emuCols=%d", ec)
	}
	if er < 20 {
		t.Fatalf("a single row of tiles should use most of the height, got emuRows=%d", er)
	}

	// One member uses the whole screen.
	c1, ec1, er1 := tileGeometry(1, 80, 24, 0)
	if c1 != 1 || ec1 < 70 || er1 < 18 {
		t.Fatalf("a lone member should fill the screen, got cols=%d emuCols=%d emuRows=%d", c1, ec1, er1)
	}

	// Degenerate sizes never produce a zero or negative dimension.
	for _, tc := range [][3]int{{0, 0, 0}, {3, 1, 1}, {5, 10, 2}} {
		c, x, y := tileGeometry(tc[0], tc[1], tc[2], 0)
		if c < 1 || x < 1 || y < 1 {
			t.Fatalf("tileGeometry(%v) produced a non-positive dim: %d %d %d", tc, c, x, y)
		}
	}
}

// TestGroupZoomResizeReflows checks the split reflows on a window resize: tiles
// shrink with the screen and the panels are resized to match.
func TestGroupZoomResizeReflows(t *testing.T) {
	c, a := zoomServer(t)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create second: %v", err)
	}
	b := (<-c.Events).Panels[1].ID
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a, b}, Group: "grp"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	snap := <-c.Events

	m := model{client: c, width: 120, height: 40, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m.fleet = mergeFleet(snap.Panels)
	m = m.zoomGroup(m.dashItems()[0])
	_, before, _ := m.tileGeometry()

	nm, _ := m.Update(tea.WindowSizeMsg{Width: 50, Height: 16})
	m = nm.(model)
	_, after, _ := m.tileGeometry()

	if after >= before {
		t.Fatalf("shrinking the window should shrink the tiles: before=%d after=%d", before, after)
	}
	if m.View() == "" {
		t.Fatal("the reflowed split should still render")
	}
}

// TestAdjustGroupShown checks that + / - dial the group's visible-tile count N:
// the local groupShown updates optimistically and a clamped group.show command is
// sent for the server to own and broadcast.
func TestAdjustGroupShown(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = bigGroup("big", maxGroupTiles+4) // 20 members; default N = 16
	m = m.zoomGroup(m.dashItems()[0])

	step := func(k string) {
		nm, _ := m.handleGroupZoomKey(key(k))
		m = nm.(model)
	}

	// - drops N to 15; the command carries the clamped count and groupShown follows.
	step("-")
	if got := m.groupShown["big"]; got != maxGroupTiles-1 {
		t.Fatalf("- should set groupShown[big] to %d, got %d", maxGroupTiles-1, got)
	}
	cmd := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "group.show" })
	if cmd.Group != "big" || cmd.Count != maxGroupTiles-1 {
		t.Fatalf("group.show should target big with count %d, got %+v", maxGroupTiles-1, cmd)
	}
	if tiles, collapsed := m.splitMembers(); len(tiles) != maxGroupTiles-1 || len(collapsed) != 5 {
		t.Fatalf("N=15 should leave 15 tiles + 5 collapsed, got %d + %d", len(tiles), len(collapsed))
	}

	// + cannot exceed the hard cap: from 15, three + steps clamp at maxGroupTiles.
	for i := 0; i < 3; i++ {
		step("+")
	}
	if got := m.groupShown["big"]; got != maxGroupTiles {
		t.Fatalf("+ should clamp N at the cap %d, got %d", maxGroupTiles, got)
	}
}

// TestAdjustGroupShownClampsLow checks N never drops below 1.
func TestAdjustGroupShownClampsLow(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0]) // api, 3 members; default N = 16
	m.groupShown = map[string]int{"api": 1}

	nm, _ := m.handleGroupZoomKey(key("-"))
	m = nm.(model)
	if got := m.groupShown["api"]; got != 1 {
		t.Fatalf("- should clamp N at 1, got %d", got)
	}
}
