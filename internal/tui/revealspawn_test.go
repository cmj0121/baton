package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// revealspawn_test.go covers #98: on a fleet whose tree scrolls, a panel the
// operator just spawned is created, joins the fleet, and is never drawn.
//
// The tree is windowed around the CURSOR, and a snapshot restores the cursor by
// identity — which is right, and is the rule that must survive this fix: a
// snapshot arriving because someone else spawned something, or because an agent
// finished, must not move the selection under the operator's hand.

// scrollingFleet is a fleet large enough that the tree cannot draw it all: the
// reporting operator's shape, seven groups of three plus a loose shell.
func scrollingFleet() []proto.Panel {
	var out []proto.Panel
	id := 1
	add := func(group, title, kind, state string) {
		out = append(out, proto.Panel{ID: fmt.Sprint(id), Title: title, Kind: kind, State: state, Group: group})
		id++
	}
	for _, g := range []string{"PTT", "Perxona", "tamis", "zerg", "Fediqo", "Baton", "Catnip"} {
		add(g, "claude · "+g, "agent", "idle")
		add(g, "grok · "+g, "agent", "exited")
		add(g, "shell #"+g, "shell", "idle")
	}
	add("", "shell · loose", "shell", "idle")
	return out
}

// arrival is the snapshot after one more panel joins at the end of the fleet.
func arrival() []proto.Panel {
	return append(scrollingFleet(), proto.Panel{
		ID: "99", Title: "claude · scarlet", Kind: "agent", State: "running", Group: "Scarlet",
	})
}

// scrollingModel is a cockpit on that fleet, at a height that scrolls.
func scrollingModel(t *testing.T) model {
	t.Helper()
	pinRender(t)
	m := baseModel()
	m.width, m.height = 200, 40
	m.fleet = mergeFleet(scrollingFleet())
	m.cursor = 0
	if items, vis := len(m.dashItems()), m.treeVisibleRows(len(m.dashItems())); vis >= items {
		t.Fatalf("the fixture fits on screen (%d items, %d visible); it cannot show the bug", items, vis)
	}
	return m
}

// TestSpawnedPanelIsRevealed is the reported symptom: the operator presses A,
// the panel is created, and the dashboard does not show it.
func TestSpawnedPanelIsRevealed(t *testing.T) {
	m := scrollingModel(t)
	m = m.spawnAgent("/tmp/scarlet") // sends panel.create and arms the reveal
	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: arrival()})

	if !strings.Contains(m.View().Content, "scarlet") {
		t.Errorf("the panel the operator just spawned is not on screen:\n%s", m.View().Content)
	}
	if it, ok := m.selectedItem(); !ok || it.panel.ID != "99" {
		t.Errorf("the cursor did not land on the new panel (selected %+v)", it)
	}
}

// TestUnrelatedArrivalDoesNotMoveTheCursor is the rule the fix must not break,
// and the mutation that kills the test above: a panel arriving without a local
// spawn — another cockpit's, or a scheduler's — must leave the selection where
// the operator's hand is.
func TestUnrelatedArrivalDoesNotMoveTheCursor(t *testing.T) {
	m := scrollingModel(t)
	m.cursor = 4
	held, _ := m.selectedItem()

	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: arrival()})

	got, ok := m.selectedItem()
	if !ok {
		t.Fatal("the cursor lost its item entirely")
	}
	if got.panel.ID == "99" {
		t.Error("an arrival nobody asked for stole the cursor")
	}
	if got.panel.ID != held.panel.ID {
		t.Errorf("the cursor moved from %q to %q on an unrelated snapshot", held.panel.ID, got.panel.ID)
	}
}

// TestRevealIsSpentOnce pins that the flag is one-shot. A second snapshot must
// not chase whatever landed next.
func TestRevealIsSpentOnce(t *testing.T) {
	m := scrollingModel(t)
	m = m.spawnAgent("/tmp/scarlet")
	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: arrival()})

	second := append(arrival(), proto.Panel{
		ID: "100", Title: "someone else's panel", Kind: "shell", State: "idle",
	})
	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: second})

	if it, ok := m.selectedItem(); ok && it.panel.ID == "100" {
		t.Error("the reveal fired twice: a later arrival took the cursor")
	}
}

// TestRevealDoesNotYankAZoom covers the operator who is elsewhere when the
// spawn lands. The flag is spent either way — a reveal deferred indefinitely
// would fire on whatever snapshot happened to arrive when they came back.
func TestRevealDoesNotYankAZoom(t *testing.T) {
	m := scrollingModel(t)
	m = m.spawnAgent("/tmp/scarlet")
	m.mode, m.zoomID = modeZoom, "1"

	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: arrival()})

	if m.mode != modeZoom || m.zoomID != "1" {
		t.Errorf("a spawn landing while zoomed moved the view: mode=%v zoom=%q", m.mode, m.zoomID)
	}
	if m.pendingReveal {
		t.Error("the reveal is still armed; it will fire on an unrelated snapshot later")
	}
}

// TestEveryOperatorSpawnArmsTheReveal: A is the reported case, but p, n c and
// n . append at the end of the same fleet and were equally invisible. Fixing
// one of four would leave the rest broken for no reason anyone could state.
func TestEveryOperatorSpawnArmsTheReveal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spawn func(model) model
	}{
		{"A · agent", func(m model) model { return m.spawnAgent("/tmp/x") }},
		{"p · shell", func(m model) model { return m.spawnPanel("") }},
		{"n c · command", func(m model) model { return m.spawnFromForm("make test") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := scrollingModel(t)
			if m = tc.spawn(m); !m.pendingReveal {
				t.Errorf("%s did not arm the reveal, so its panel lands off-screen", tc.name)
			}
		})
	}
}

// TestSpawnIsSizedForThisCockpit covers #97: a panel born at ptymgr's 24x80
// floor lays its interface out for eighty columns until the first zoom, and
// whatever it printed before that keeps that shape in the replay ring.
//
// What this can assert is the DECISION — the geometry a spawn is sized to
// follows the cockpit rather than the floor. That the resize is actually sent
// is structural rather than tested: viewGeometry has two callers, the zoom and
// the spawn, and they cannot drift because there is one function. A send spy
// would be the only way to assert the wire, and the cockpit has none.
func TestSpawnIsSizedForThisCockpit(t *testing.T) {
	m := baseModel()
	m.width, m.height = 200, 50

	rows, cols := m.viewGeometry()
	if cols == 80 || rows == 24 {
		t.Errorf("a spawn on a 200x50 cockpit is sized %dx%d — that is ptymgr's floor, not this screen", cols, rows)
	}
	if cols != m.width {
		t.Errorf("cols = %d, want the cockpit's %d", cols, m.width)
	}
	if rows != m.zoomRows() {
		t.Errorf("rows = %d, want the zoom's %d — a spawn sized differently reflows on the first zoom", rows, m.zoomRows())
	}
}
