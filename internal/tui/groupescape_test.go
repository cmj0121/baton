package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// The group split's prefix-reached escapes.
//
// isEscape is what makes a key REACHABLE after the leader; it is not what makes
// it work in a view that owns its own keyboard. The split and its interact mode
// each enumerate the escapes they honour and silently swallow the rest, so an
// escape added anywhere else does nothing here until it is listed — which is how
// C-t a, C-t o and C-t c all came to be dead in the one view a fleet operator
// spends the most time in.
//
// These cases drive m.Update rather than m.handleKey ON PURPOSE. handleKey is the
// generic command-mode path and resolves every escape through runAction, so a test
// written against it passes whether or not the split's own switch knows the key —
// which is exactly the false green that let the gap through.

// splitFleet is a two-member work item, one of whose members is asking for a
// human, so the split has tiles and the inbox has a row.
func splitFleet() []proto.Panel {
	return []proto.Panel{
		wire("1", "attention", inboxTailStale),
		wire("2", "running", inboxTailStale),
	}
}

// splitAt opens that group's split, optionally in interact mode, and returns it
// ready for keys. A plugin command is registered because the command picker
// refuses to open with an empty list — a real refusal, not a routing failure, and
// one that would otherwise make this case pass for the wrong reason.
func splitAt(interact bool) model {
	m := inboxModel(splitFleet()...)
	for i := range m.fleet {
		m.fleet[i].Group = "api"
	}
	m.inboxWire[0].Group, m.inboxWire[1].Group = "api", "api"
	m.pluginCommands = []proto.PluginCommand{{Name: "deploy", Desc: "ship it"}}
	m = m.zoomGroup(m.dashItems()[0])
	m.groupInteract = interact
	return m
}

// updateKeys feeds keys through the real Update path — the one a running cockpit
// uses — and returns the model they left behind.
func updateKeys(t *testing.T, m model, keys ...string) model {
	t.Helper()
	for _, k := range keys {
		out, _ := m.Update(key(k))
		next, ok := out.(model)
		if !ok {
			t.Fatalf("Update returned something other than a model for %q", k)
		}
		m = next
	}
	return m
}

// TestSplitEscapesReachTheOverlays is issue #6's "from any view" taken literally,
// plus the two escapes that were quietly missing beside it. A fleet operator lives
// in the split; an inbox that cannot be opened from there is an inbox that is not
// where the work is.
func TestSplitEscapesReachTheOverlays(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want mode
	}{
		{"inbox", "a", modeInbox},
		{"proc-tree", "o", modeProcTree},
		{"commands", "c", modeCommand},
	}
	for _, interact := range []bool{false, true} {
		for _, tc := range cases {
			name := tc.name
			if interact {
				name += "/interact"
			}
			t.Run(name, func(t *testing.T) {
				m := splitAt(interact)
				if m.mode != modeGroupZoom {
					t.Fatalf("setup: mode = %v, want the split", m.mode)
				}

				m = updateKeys(t, m, "ctrl+t", tc.key)

				if m.mode != tc.want {
					t.Fatalf("C-t %s from the split opened %v, want %v", tc.key, m.mode, tc.want)
				}
				// …and it puts the split back, rather than dumping the operator on the
				// dashboard with their tiles torn down.
				if m = updateKeys(t, m, "esc"); m.mode != modeGroupZoom {
					t.Errorf("esc should restore the split, got %v", m.mode)
				}
			})
		}
	}
}

// TestSplitInboxHoldsTheSameQueue is the half of the fix that matters beyond the
// mode switch: the queue opened from a split is built from the same fleet as
// anywhere else, and its rows are usable where they stand.
func TestSplitInboxHoldsTheSameQueue(t *testing.T) {
	m := updateKeys(t, splitAt(false), "ctrl+t", "a")

	if got := rowIDs(m); !eqIDs(got, "1") {
		t.Fatalf("the split's inbox should hold the asking member, got %v", got)
	}
	if m.inboxFrom != modeGroupZoom {
		t.Errorf("the inbox should remember the split it was opened over, got %v", m.inboxFrom)
	}
	// The row is a real row: dismissing it empties the queue, which closes the
	// overlay back onto the split rather than onto the dashboard.
	m = updateKeys(t, m, "x")
	if m.mode != modeGroupZoom {
		t.Errorf("an emptied queue should hand the split back, got %v", m.mode)
	}
}

// TestSplitStillLeavesOnTheDashboardEscape guards the escapes the split already
// honoured, since adding cases to a switch is exactly how an existing one gets
// shadowed.
func TestSplitStillLeavesOnTheDashboardEscape(t *testing.T) {
	if m := updateKeys(t, splitAt(false), "ctrl+t", keyDashboard); m.mode != modeDashboard {
		t.Errorf("C-t d should still leave the split, got %v", m.mode)
	}
	if m := updateKeys(t, splitAt(true), "ctrl+t", keyDashboard); m.mode != modeDashboard {
		t.Errorf("C-t d should still leave interact mode, got %v", m.mode)
	}
}
