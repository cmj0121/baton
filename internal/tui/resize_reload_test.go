package tui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// clearScreenType is the message tea.ClearScreen produces; resize must force this
// full repaint so no stale cells from the old frame survive the size change.
var clearScreenType = reflect.TypeOf(tea.ClearScreen())

func isClearScreen(cmd tea.Cmd) bool {
	return cmd != nil && reflect.TypeOf(cmd()) == clearScreenType
}

// TestResizeForcesRepaint checks that a window-size change updates the dimensions
// and forces a clean full repaint (ClearScreen) on the dashboard.
func TestResizeForcesRepaint(t *testing.T) {
	m := model{width: 80, height: 24, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	got := next.(model)
	if got.width != 100 || got.height != 40 {
		t.Fatalf("resize dims = %dx%d, want 100x40", got.width, got.height)
	}
	if !isClearScreen(cmd) {
		t.Fatal("resize should force a ClearScreen repaint")
	}
}

// TestZoomResizeReloads checks that resizing while zoomed reloads the view: the
// panel PTY is resized to the new screen (SIGWINCH so the program redraws) and a
// full repaint is forced.
func TestZoomResizeReloads(t *testing.T) {
	c, cmds := recordingServer(t)
	m := model{client: c, width: 80, height: 24, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m = m.zoomInto(panel.Panel{ID: "7", Title: "sh #7"})
	// drain the zoom-in resize so we assert on the one the window change sends.
	waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.resize" && c.ID == "7" })

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	m = next.(model)

	got := waitCmd(t, cmds, func(c proto.Command) bool {
		return c.Action == "panel.resize" && c.ID == "7" && c.Cols == 120
	})
	if got.Rows != m.zoomRows() {
		t.Fatalf("zoom resize rows = %d, want %d", got.Rows, m.zoomRows())
	}
	if !isClearScreen(cmd) {
		t.Fatal("resize while zoomed should force a ClearScreen repaint")
	}
}
