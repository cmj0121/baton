package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestGroupNestsMarkedGroup: grouping a fully-marked group plus a lone panel under a
// new name nests the group (a rename to a path) and attaches the panel directly, so
// grouping a group carries its name into the new parent instead of flattening it.
func TestGroupNestsMarkedGroup(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "db a", State: panel.Running, Group: "db"},
		{ID: "2", Kind: panel.Agent, Title: "db b", State: panel.Idle, Group: "db"},
		{ID: "3", Kind: panel.Shell, Title: "lone", State: panel.Idle},
	}
	m.marked = map[string]bool{"1": true, "2": true, "3": true}

	m = m.commitGroup("backend")

	rn := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.rename" })
	if rn.Group != "db" || rn.Name != "backend/db" {
		t.Fatalf("db should nest as backend/db, got group=%q name=%q", rn.Group, rn.Name)
	}
	gp := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.group" })
	if gp.Group != "backend" || len(gp.IDs) != 1 || gp.IDs[0] != "3" {
		t.Fatalf("the lone panel should group directly into backend, got %+v", gp)
	}
}

// TestAddNestsMarkedGroup: adding a marked group to the selected group nests it under
// that group's path.
func TestAddNestsMarkedGroup(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{
		{ID: "1", Kind: panel.Agent, State: panel.Running, Group: "db"},
		{ID: "2", Kind: panel.Agent, State: panel.Idle, Group: "db"},
		{ID: "9", Kind: panel.Agent, State: panel.Running, Group: "backend"}, // the target
	}
	m.marked = map[string]bool{"1": true, "2": true}
	m.cursor = 1 // the backend group card (dashItems: db, backend)

	m = m.addMarkedToGroup()

	rn := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.rename" })
	if rn.Group != "db" || rn.Name != "backend/db" {
		t.Fatalf("db should nest under backend, got group=%q name=%q", rn.Group, rn.Name)
	}
}

// TestGroupLonePanelsStayFlat: a selection of only lone panels still groups flat —
// one panel.group, no rename — so the common case is unchanged.
func TestGroupLonePanelsStayFlat(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{{ID: "1", State: panel.Running}, {ID: "2", State: panel.Idle}}
	m.marked = map[string]bool{"1": true, "2": true}

	m = m.commitGroup("api")

	gp := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.group" })
	if gp.Group != "api" || len(gp.IDs) != 2 {
		t.Fatalf("lone panels should group flat into api, got %+v", gp)
	}
	select {
	case extra := <-cmds:
		t.Fatalf("no other command expected for a lone-panel group, got %+v", extra)
	default:
	}
}
