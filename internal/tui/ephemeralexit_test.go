package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// ephemeralexit_test.go covers the second half of #93's cockpit surface: what
// happens when the editor is closed.
//
// A transient panel is deliberately kept out of s.panels, so it never appears
// in a "panels" snapshot and the cockpit cannot see that its process ended. The
// exit used to reach the frontend only as "[process exited]" painted into the
// pane — text a frontend can show but cannot act on — so leaving the editor
// left the operator sitting in a zoom on a dead pane.

// zoomedOnEphemeral is a cockpit zoomed on a transient panel that asked to be
// left when its process ends.
func zoomedOnEphemeral(id string, autoClose bool) model {
	return model{
		mode:               modeZoom,
		zoomID:             id,
		zoomEphemeral:      true,
		zoomEphemeralClose: autoClose,
	}
}

// TestEditorExitLeavesTheZoom is the reported bug: closing the editor has to
// put the operator back on the dashboard.
func TestEditorExitLeavesTheZoom(t *testing.T) {
	m := zoomedOnEphemeral("score:1", true)
	m.applyEvent(proto.ServerMsg{Type: "ephemeral-exit", ID: "score:1"})

	if m.mode != modeDashboard {
		t.Errorf("mode = %v after the editor exited, want the dashboard", m.mode)
	}
	if m.zoomID != "" {
		t.Errorf("zoomID = %q, want it cleared", m.zoomID)
	}
	// The panel is already reaped server-side, so the way out must not ask the
	// daemon to close an id it no longer knows.
	if m.zoomEphemeral {
		t.Error("zoomEphemeral is still set, so the exit path would send panel.close for a dead id")
	}
}

// TestFailedEditorKeepsTheZoom is the mutation that kills the test above. A
// dismissal on ANY exit would pass it and take away the one thing a failed
// editor produces: its message. "$EDITOR: command not found" is exactly what an
// operator needs and exactly what would flash past.
func TestFailedEditorKeepsTheZoom(t *testing.T) {
	m := zoomedOnEphemeral("score:1", true)
	m.applyEvent(proto.ServerMsg{Type: "ephemeral-exit", ID: "score:1", Failed: true})

	if m.mode != modeZoom {
		t.Errorf("mode = %v after a FAILED editor exited, want to stay in the zoom so its message can be read", m.mode)
	}
}

// TestDiffExitKeepsTheZoom holds the line the other way round. A diff or a git
// log has finished producing the output the operator opened it for; dismissing
// it on exit would throw that away unread, which is why the marking is per-op
// rather than for every transient panel.
func TestDiffExitKeepsTheZoom(t *testing.T) {
	m := zoomedOnEphemeral("diff:1", false)
	m.applyEvent(proto.ServerMsg{Type: "ephemeral-exit", ID: "diff:1"})

	if m.mode != modeZoom {
		t.Errorf("mode = %v after a diff exited, want the output left on screen", m.mode)
	}
}

// TestUnrelatedEphemeralExitIsIgnored pins that the id is honoured: a panel
// exiting somewhere else must not yank the operator out of what they are
// looking at.
func TestUnrelatedEphemeralExitIsIgnored(t *testing.T) {
	m := zoomedOnEphemeral("score:1", true)
	m.applyEvent(proto.ServerMsg{Type: "ephemeral-exit", ID: "score:2"})

	if m.mode != modeZoom || m.zoomID != "score:1" {
		t.Errorf("another panel's exit moved this zoom: mode=%v id=%q", m.mode, m.zoomID)
	}
}

// TestEditScoreMarksTheZoomToClose ties the two ends together: the flag the
// exit path reads is the one the score verb sets, so a rename of either cannot
// quietly leave the editor un-dismissable.
func TestEditScoreMarksTheZoomToClose(t *testing.T) {
	m := model{mode: modeDashboard}
	got, _ := m.editScore()
	nm, ok := got.(model)
	if !ok {
		t.Fatalf("editScore returned %T", got)
	}
	if !nm.pendingEphemeralClose {
		t.Error("editScore did not mark the zoom to close, so the editor's exit will be ignored")
	}
	if nm.pendingEphemeralTitle == "" {
		t.Error("editScore set no title for the zoom")
	}
}
