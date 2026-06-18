package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/proto"
)

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

	// prefix+g (BIND-g) pops back to the group split and forgets the origin.
	nm, _ = m.handleZoomKey(key("ctrl+t")) // arm the prefix
	m = nm.(model)
	nm, _ = m.handleZoomKey(key("g"))
	m = nm.(model)
	if m.mode != modeGroupZoom || m.groupName != "api" || m.zoomGroupOrigin != "" {
		t.Fatalf("prefix+g should return to the api split, got mode=%v group=%q origin=%q", m.mode, m.groupName, m.zoomGroupOrigin)
	}
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

	body := m.tileBody(m.groupMembers()[0], 30, 6) // member id "1"
	if len(body) != 6 {
		t.Fatalf("a live tile should have 6 rows, got %d", len(body))
	}
	if !strings.Contains(strings.Join(body, ""), "hello-live") {
		t.Fatalf("live tile should show emulator output, got %q", strings.Join(body, ""))
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

func TestGroupColumnsAdjust(t *testing.T) {
	m := baseModel()
	m.width, m.height = 40, 20 // narrow: the auto layout is a single column
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0]) // api, 3 members

	cols := func() int { c, _, _ := m.tileGeometry(); return c }
	if cols() != 1 {
		t.Fatalf("narrow auto layout should be 1 column, got %d", cols())
	}

	step := func(k string) { nm, _ := m.handleGroupZoomKey(key(k)); m = nm.(model) }

	step("+")
	if cols() != 2 {
		t.Fatalf("+ should widen to 2 columns, got %d", cols())
	}
	step("+")
	if cols() != 3 {
		t.Fatalf("+ should widen to 3 columns, got %d", cols())
	}
	step("+") // clamp at the member count
	if cols() != 3 {
		t.Fatalf("+ should clamp at 3 columns, got %d", cols())
	}
	step("-")
	if cols() != 2 {
		t.Fatalf("- should narrow back to 2 columns, got %d", cols())
	}
}
