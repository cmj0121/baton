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

// TestZoomMarkRefusesADeadScreen: an exited panel has no screen worth learning
// from, and the cockpit says so rather than sending a mark the daemon would
// write anyway.
func TestZoomMarkRefusesADeadScreen(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	p := panel.Panel{ID: "a1", Kind: panel.Agent, Title: "claude · api", State: panel.Exited}
	m.fleet = []panel.Panel{p}
	m = m.zoomInto(p)

	nm, _ := m.handleZoomKey(key("ctrl+t"))
	m = nm.(model)
	nm, _ = m.handleZoomKey(key(keyAttentionMark))
	m = nm.(model)

	if !strings.Contains(m.status, "nothing to mark") {
		t.Errorf("status = %q, want a refusal", m.status)
	}
	select {
	case cmd := <-cmds:
		if cmd.Action == "attention.mark" {
			t.Fatal("an exited panel must not be marked")
		}
	case <-time.After(50 * time.Millisecond):
	}
}
