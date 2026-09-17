package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// deadGroup is a work item whose members have all exited — the state a whole
// fleet is in after a daemon restart.
func deadGroup() []panel.Panel {
	return []panel.Panel{
		{ID: "63", Kind: panel.Agent, Title: "claude · catnip", State: panel.Exited, Group: "catnip"},
		{ID: "64", Kind: panel.Agent, Title: "grok · catnip", State: panel.Exited, Group: "catnip"},
	}
}

// TestTheTwoReRunVerbsAreDifferentWires is the whole of #117 at this layer: two
// keys, two actions, one selection rule.
//
// r means "bring it back exactly as it was" and n r means "bring it back the way
// I would spawn it now". A cockpit that sent the same action for both would look
// identical in every other assertion — same status, same scope, same refusals —
// so the wire is the only place the difference is visible.
func TestTheTwoReRunVerbsAreDifferentWires(t *testing.T) {
	for _, tc := range []struct {
		act    action
		action string
	}{
		{actRespawn, "panel.respawn"},
		{actRelaunch, "panel.relaunch"},
	} {
		c, cmds := recordingServer(t)
		m := model{client: c, width: 80, height: 24, mode: modeDashboard,
			fleet: []panel.Panel{deadSlotPanel},
			binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

		out, _ := m.runAction(tc.act)
		if got := mustModel(t, out); got.status == "" {
			t.Errorf("%s said nothing", tc.action)
		}
		got := waitCmd(t, cmds, func(c proto.Command) bool { return c.ID == "63" })
		if got.Action != tc.action {
			t.Errorf("action = %q, want %q", got.Action, tc.action)
		}
	}
}

// TestBothVerbsFanOutOverAGroup: r on a work item re-runs every exited member,
// and n r has to match it. A verb that silently acted on one member of a group
// the other acts on wholesale would be the more dangerous of the two, since it
// is the one that changes what the panels run.
func TestBothVerbsFanOutOverAGroup(t *testing.T) {
	for _, tc := range []struct {
		act    action
		action string
	}{
		{actRespawn, "panel.respawn"},
		{actRelaunch, "panel.relaunch"},
	} {
		c, cmds := recordingServer(t)
		m := model{client: c, width: 80, height: 24, mode: modeDashboard, fleet: deadGroup(),
			binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
		m.cursor = 0 // the group's own row

		if _, ok := m.selectedItem(); !ok {
			t.Fatal("the fixture should select the group row")
		}
		out, _ := m.runAction(tc.act)
		if got := mustModel(t, out); !strings.Contains(got.status, "catnip") {
			t.Errorf("%s should name the group it acted on, got %q", tc.action, got.status)
		}

		seen := map[string]bool{}
		for range 2 {
			c := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == tc.action })
			seen[c.ID] = true
		}
		if !seen["63"] || !seen["64"] {
			t.Errorf("%s reached %v, want both exited members", tc.action, seen)
		}
	}
}

// TestALivePanelIsLeftAlone: neither verb touches a running panel, and the
// refusal says so rather than doing nothing visible.
func TestALivePanelIsLeftAlone(t *testing.T) {
	for _, act := range []action{actRespawn, actRelaunch} {
		c, _ := recordingServer(t)
		m := model{client: c, width: 80, height: 24, mode: modeDashboard,
			fleet: []panel.Panel{{ID: "1", Title: "live", State: panel.Running}},
			binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

		out, _ := m.runAction(act)
		if got := mustModel(t, out); !strings.Contains(got.status, "still running") {
			t.Errorf("a live panel should be left alone with a reason, got %q", got.status)
		}
	}
}
