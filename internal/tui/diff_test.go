package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestDiffFromDashboardAgent checks D on an agent selection sends panel.diff for
// that panel and stashes the diff title.
func TestDiffFromDashboardAgent(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{{ID: "a1", Kind: panel.Agent, Title: "claude · auth", State: panel.Running}}
	m.cursor = 0

	m = press(m, keyDiff)
	if m.pendingEphemeralTitle == "" {
		t.Fatal("D should stash a pending diff title for the zoom")
	}
	diff := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.diff" })
	if diff.ID != "a1" {
		t.Fatalf("panel.diff should target the selected agent, got %+v", diff)
	}
}

// TestDiffFromDashboardShell checks D on a shell selection sends nothing and sets
// the agent-only hint.
func TestDiffFromDashboardShell(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{{ID: "s1", Kind: panel.Shell, Title: "shell", State: panel.Running}}
	m.cursor = 0

	m = press(m, keyDiff)
	if m.pendingEphemeralTitle != "" {
		t.Fatal("a shell selection must not stash a diff title")
	}
	if m.status != "diff: select an agent panel" {
		t.Fatalf("expected the agent-only hint, got %q", m.status)
	}
	select {
	case got := <-cmds:
		if got.Action == "panel.diff" {
			t.Fatalf("a shell selection must not send panel.diff, got %+v", got)
		}
	default:
	}
}

// TestDiffFromDashboardGroup checks D on a group card sends nothing and hints — a
// group is not a single agent panel.
func TestDiffFromDashboardGroup(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = groupedFleet()
	m.cursor = 0 // the api group card

	m = press(m, keyDiff)
	if m.pendingEphemeralTitle != "" {
		t.Fatal("a group selection must not stash a diff title")
	}
	if m.status != "diff: select an agent panel" {
		t.Fatalf("expected the agent-only hint on a group, got %q", m.status)
	}
	select {
	case got := <-cmds:
		if got.Action == "panel.diff" {
			t.Fatalf("a group selection must not send panel.diff, got %+v", got)
		}
	default:
	}
}

// TestDiffReplyAutoZooms checks a {type:"diff"} reply auto-zooms the new id and
// flags the zoom ephemeral.
func TestDiffReplyAutoZooms(t *testing.T) {
	c, _ := recordingServer(t)
	m := baseModel()
	m.client = c
	m.pendingEphemeralTitle = "diff · claude · auth"

	m.applyEvent(proto.ServerMsg{Type: "ephemeral", ID: "diff:9"})
	if m.mode != modeZoom {
		t.Fatalf("a diff reply should enter modeZoom, got mode=%v", m.mode)
	}
	if m.zoomID != "diff:9" {
		t.Fatalf("the zoom should be the replied id, got %q", m.zoomID)
	}
	if !m.zoomEphemeral {
		t.Fatal("the diff zoom must be flagged ephemeral")
	}
	if m.zoomTitle != "diff · claude · auth" {
		t.Fatalf("the zoom should take the stashed title, got %q", m.zoomTitle)
	}
	if m.pendingEphemeralTitle != "" {
		t.Fatal("the pending title should be consumed by the reply")
	}
}

// TestDiffReplyFallbackTitle checks the zoom title falls back to "diff" with no
// stashed title.
func TestDiffReplyFallbackTitle(t *testing.T) {
	c, _ := recordingServer(t)
	m := baseModel()
	m.client = c

	m.applyEvent(proto.ServerMsg{Type: "ephemeral", ID: "diff:1"})
	if m.zoomTitle != "diff" {
		t.Fatalf("an unstashed diff should fall back to %q, got %q", "diff", m.zoomTitle)
	}
}

// TestDiffDismissClosesEphemeral checks leaving a diff zoom via C-t d closes the
// transient panel and clears the flag.
func TestDiffDismissClosesEphemeral(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.applyEvent(proto.ServerMsg{Type: "ephemeral", ID: "diff:9"})

	// C-t d dismisses the zoom.
	next, _ := m.handleZoomKey(key(m.effPrefix()))
	m = next.(model)
	next, _ = m.handleZoomKey(key(keyDashboard))
	m = next.(model)

	if m.zoomEphemeral {
		t.Fatal("dismissing the diff zoom should clear the ephemeral flag")
	}
	closed := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.close" })
	if closed.ID != "diff:9" {
		t.Fatalf("dismiss should close the transient panel diff:9, got %+v", closed)
	}
}

// TestNormalZoomKeepsPanel checks leaving a normal (non-diff) zoom via C-t d does
// NOT send panel.close.
func TestNormalZoomKeepsPanel(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m = m.zoomInto(panel.Panel{ID: "p7", Title: "shell", State: panel.Running})
	if m.zoomEphemeral {
		t.Fatal("a normal zoom must not be ephemeral")
	}

	next, _ := m.handleZoomKey(key(m.effPrefix()))
	m = next.(model)
	next, _ = m.handleZoomKey(key(keyDashboard))
	m = next.(model)

	// Drain commands briefly; none should be a panel.close.
	for {
		select {
		case got := <-cmds:
			if got.Action == "panel.close" {
				t.Fatalf("a normal zoom dismiss must not close the panel, got %+v", got)
			}
			continue
		default:
		}
		break
	}
}

// TestDiffErrorReplySetsStatus checks an {type:"error"} reply surfaces on the
// status line (the intended "pop-up").
func TestDiffErrorReplySetsStatus(t *testing.T) {
	m := baseModel()
	m.applyEvent(proto.ServerMsg{Type: "error", Error: "not a git repository: /x"})
	if m.status != "error: not a git repository: /x" {
		t.Fatalf("an error reply should set the status, got %q", m.status)
	}
}

// TestDiffFromZoomAgent checks C-t D in a zoom of an agent panel sends panel.diff
// for the zoomed id.
func TestDiffFromZoomAgent(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{{ID: "a1", Kind: panel.Agent, Title: "claude · auth", State: panel.Running}}
	m = m.zoomInto(m.fleet[0])

	next, _ := m.handleZoomKey(key(m.effPrefix())) // arm the prefix
	m = next.(model)
	next, _ = m.handleZoomKey(key(keyDiff))
	m = next.(model)

	diff := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.diff" })
	if diff.ID != "a1" {
		t.Fatalf("C-t D should diff the zoomed agent, got %+v", diff)
	}
}

// TestDiffOfADiffRejected checks C-t D inside a diff zoom does not request another
// diff (no diff-of-a-diff).
func TestDiffOfADiffRejected(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.applyEvent(proto.ServerMsg{Type: "ephemeral", ID: "diff:9"})
	if !m.zoomEphemeral {
		t.Fatal("setup: the diff reply should have flagged the zoom ephemeral")
	}

	next, _ := m.handleZoomKey(key(m.effPrefix()))
	m = next.(model)
	next, _ = m.handleZoomKey(key(keyDiff))
	m = next.(model)

	if m.pendingEphemeralTitle != "" {
		t.Fatal("a diff-of-a-diff must not stash a new title")
	}
	for {
		select {
		case got := <-cmds:
			if got.Action == "panel.diff" {
				t.Fatalf("a diff-of-a-diff must not send panel.diff, got %+v", got)
			}
			continue
		default:
		}
		break
	}
}

// TestDiffFromGroupFocusedAgent checks bare D in the split requests the diff of the
// focused agent member.
func TestDiffFromGroupFocusedAgent(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = groupedFleet() // api = agents 1,3,6
	m = m.zoomGroup(m.dashItems()[0])
	if m.mode != modeGroupZoom {
		t.Fatalf("expected the split, got mode=%v", m.mode)
	}
	focus, _ := m.focusedMember()

	next, _ := m.handleGroupZoomKey(key(keyDiff))
	m = next.(model)
	diff := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.diff" })
	if diff.ID != focus.ID {
		t.Fatalf("bare D should diff the focused member %s, got %+v", focus.ID, diff)
	}
}
