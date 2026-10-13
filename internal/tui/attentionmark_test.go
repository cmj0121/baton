package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestZoomMarkTeachesTheClassifier: C-t m on a zoomed panel sends
// attention.mark for that panel and says so, without leaving the zoom.
func TestZoomMarkTeachesTheClassifier(t *testing.T) {
	m, cmds := zoomedAgent(t)

	nm, _ := m.handleZoomKey(key("ctrl+t"))
	m = nm.(model)
	nm, _ = m.handleZoomKey(key(keyAttentionMark))
	m = nm.(model)

	mark := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "attention.mark" })
	if mark.ID != "a1" {
		t.Errorf("attention.mark should name the zoomed panel, got %+v", mark)
	}
	if m.mode != modeZoom {
		t.Errorf("a mark should leave the zoom where it is, got mode %v", m.mode)
	}
	if !strings.Contains(m.status, "marked as a wait") {
		t.Errorf("status = %q, want the mark confirmed", m.status)
	}
}

// TestZoomMarkRefusesADeadScreen: a zoom has no row to be stale, so it refuses
// an exited panel itself rather than sending a mark the daemon would refuse.
func TestZoomMarkRefusesADeadScreen(t *testing.T) {
	m, cmds := zoomedAgentIn(t, panel.Exited)
	if m.mode != modeZoom || !m.zoomExited {
		t.Fatalf("setup: the zoom should be open on the exited panel, got mode %v exited %v", m.mode, m.zoomExited)
	}
	waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.resize" }) // the zoom's own attach and resize

	nm, _ := m.handleZoomKey(key("ctrl+t"))
	m = nm.(model)
	nm, _ = m.handleZoomKey(key(keyAttentionMark))
	m = nm.(model)

	if !strings.Contains(m.status, "nothing to mark") {
		t.Errorf("status = %q, want a refusal", m.status)
	}
	expectNoCmd(t, cmds)
}

// TestInboxMarkLeavesAnExitedRowToTheDaemon: an exited row that is not stale
// sends the mark — the daemon refuses an exited panel and says so, and its
// notice is what the status line ends on.
func TestInboxMarkLeavesAnExitedRowToTheDaemon(t *testing.T) {
	failed := wire("1", "exited", time.Minute)
	failed.ExitCode = 2
	m, cmds := clientInbox(t, failed)
	if got := rowIDs(m); !eqIDs(got, "1") {
		t.Fatalf("setup: a failed exit should be the one row, got %v", got)
	}

	m = press(m, keyAttentionMark)
	mark := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "attention.mark" })
	if mark.ID != "1" {
		t.Fatalf("attention.mark should name the exited row, got %+v", mark)
	}

	m.applyEvent(proto.ServerMsg{Type: "notice", ID: "1", Notice: "not marked: the panel has exited"})
	if m.status != "not marked: the panel has exited" {
		t.Errorf("status = %q, want the daemon's refusal", m.status)
	}
}
