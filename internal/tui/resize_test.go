package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestZoomSendsResize proves a zoom resizes the panel's PTY to the full screen
// (less the footer row) before attaching, so the program redraws to the size it
// is now shown at — the SIGWINCH the kernel raises when the window size changes.
func TestZoomSendsResize(t *testing.T) {
	c, cmds := recordingServer(t)
	m := model{client: c, width: 80, height: 24,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	m = m.zoomInto(panel.Panel{ID: "7", Title: "sh #7"})

	got := waitCmd(t, cmds, func(c proto.Command) bool {
		return c.Action == "panel.resize" && c.ID == "7"
	})
	if got.Cols != 80 || got.Rows != m.zoomRows() {
		t.Fatalf("zoom resize = %dx%d, want %dx%d", got.Cols, got.Rows, 80, m.zoomRows())
	}
}

// TestGroupZoomSendsFullResize proves that dropping from a group tile into a
// single zoom resizes that panel's PTY up from its small tile to the full screen,
// so the program is told its window grew.
func TestGroupZoomSendsFullResize(t *testing.T) {
	c, cmds := recordingServer(t)
	fleet := []panel.Panel{
		{ID: "A", Group: "g", Title: "A"},
		{ID: "B", Group: "g", Title: "B"},
	}
	m := model{client: c, width: 80, height: 24, fleet: fleet, mode: modeGroupZoom,
		groupName: "g", groupFocus: 0, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m.attachGroupMembers() // tiles attach at their small size first

	next, _ := m.zoomFocusedMember()
	m = next.(model)

	// The zoom resize for A carries the full width, distinct from its tile resize.
	got := waitCmd(t, cmds, func(c proto.Command) bool {
		return c.Action == "panel.resize" && c.ID == "A" && c.Cols == 80
	})
	if got.Rows != m.zoomRows() {
		t.Fatalf("group→zoom resize rows = %d, want %d", got.Rows, m.zoomRows())
	}
}
