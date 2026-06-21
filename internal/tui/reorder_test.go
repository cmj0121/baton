package tui

import (
	"reflect"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// lone builds a fleet of ungrouped panels with the given ids, in order.
func lone(ids ...string) []panel.Panel {
	out := make([]panel.Panel, len(ids))
	for i, id := range ids {
		out[i] = panel.Panel{ID: id, Title: id}
	}
	return out
}

func TestMoveTarget(t *testing.T) {
	abc := lone("A", "B", "C")
	abcUnits := [][]string{{"A"}, {"B"}, {"C"}}

	// A grouped fleet: A and C belong to group "g"; B and D stand alone. The
	// dashboard folds it into three items — the group (anchored at A), B, D.
	grouped := []panel.Panel{
		{ID: "A", Group: "g"}, {ID: "B"}, {ID: "C", Group: "g"}, {ID: "D"},
	}
	groupedItems := [][]string{{"A", "C"}, {"B"}, {"D"}}
	groupMembers := [][]string{{"A"}, {"C"}} // the two members within group "g"

	cases := []struct {
		name      string
		fleet     []panel.Panel
		units     [][]string
		sel, dir  int
		wantBlock []string
		wantIndex int
		wantOK    bool
	}{
		{"lone later", abc, abcUnits, 1, +1, []string{"B"}, 2, true},
		{"lone earlier", abc, abcUnits, 1, -1, []string{"B"}, 0, true},
		{"first cannot go earlier", abc, abcUnits, 0, -1, nil, 0, false},
		{"last cannot go later", abc, abcUnits, 2, +1, nil, 0, false},
		{"out of range", abc, abcUnits, 9, +1, nil, 0, false},
		{"zero direction", abc, abcUnits, 1, 0, nil, 0, false},
		// A whole group moves as a block and lands contiguously after B.
		{"group block later", grouped, groupedItems, 0, +1, []string{"A", "C"}, 1, true},
		// A lone panel hops earlier past the group's last member.
		{"panel past group", grouped, groupedItems, 2, -1, []string{"D"}, 1, true},
		// Reordering within the group: C moves ahead of A.
		{"member earlier", grouped, groupMembers, 1, -1, []string{"C"}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			block, index, ok := moveTarget(c.fleet, c.units, c.sel, c.dir)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(block, c.wantBlock) {
				t.Errorf("block = %v, want %v", block, c.wantBlock)
			}
			if index != c.wantIndex {
				t.Errorf("index = %d, want %d", index, c.wantIndex)
			}
		})
	}
}

// TestReorderDashItemSendsMove drives the dashboard reorder key path and asserts
// the panel.move that reaches the server moves the selected panel later.
func TestReorderDashItemSendsMove(t *testing.T) {
	c, cmds := recordingServer(t)
	m := model{client: c, width: 80, height: 24, fleet: lone("A", "B", "C"), cursor: 1,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	m.reorderDashItem(+1) // the command travels over the socket; the returned model is just status

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.move" })
	if !reflect.DeepEqual(got.IDs, []string{"B"}) || got.Index != 2 {
		t.Fatalf("panel.move = %+v, want IDs [B] Index 2", got)
	}
}

// TestReorderDashItemEdge keeps the first item put and says so.
func TestReorderDashItemEdge(t *testing.T) {
	m := model{fleet: lone("A", "B", "C"), cursor: 0}
	m = m.reorderDashItem(-1)
	if m.status != "already first" {
		t.Fatalf("status = %q, want %q", m.status, "already first")
	}
}

// TestReorderGroupMemberSendsMove drives the group-view reorder and asserts the
// focused member is moved one slot earlier among its group's members.
func TestReorderGroupMemberSendsMove(t *testing.T) {
	c, cmds := recordingServer(t)
	fleet := []panel.Panel{{ID: "A", Group: "g", Title: "A"}, {ID: "B", Group: "g", Title: "B"}}
	m := model{client: c, width: 80, height: 24, fleet: fleet, mode: modeGroupZoom,
		groupName: "g", groupFocus: 1, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	m.reorderGroupMember(-1) // assert on what is sent, not the returned status model

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.move" })
	if !reflect.DeepEqual(got.IDs, []string{"B"}) || got.Index != 0 {
		t.Fatalf("panel.move = %+v, want IDs [B] Index 0", got)
	}
}
