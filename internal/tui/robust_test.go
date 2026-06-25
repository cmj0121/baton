package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
)

// TestScrollFooterColor checks the footer fill switches to the amber scroll
// colour in scroll mode and back to the standing bar colour otherwise.
func TestScrollFooterColor(t *testing.T) {
	m := baseModel()
	if got := m.barBG(); got != colBar {
		t.Fatalf("non-scroll footer = %v, want colBar", got)
	}
	m.scrolling = true
	if got := m.barBG(); got != colScroll {
		t.Fatalf("scroll footer = %v, want colScroll", got)
	}
}

// TestClampGroupFocus checks the render-time guard pulls a dangling focus back
// into range and that rendering a group with an out-of-range focus never panics.
func TestClampGroupFocus(t *testing.T) {
	m := baseModel()
	m.width, m.height = 100, 40
	m.mode = modeGroupZoom
	m.groupName = "g"
	m.fleet = []panel.Panel{
		{ID: "a", Group: "g", Title: "a", State: panel.Running},
		{ID: "b", Group: "g", Title: "b", State: panel.Running},
	}
	m.groupFocus = 99 // a member left, leaving the focus past the end

	m.clampGroupFocus()
	if m.groupFocus < 0 || m.groupFocus >= m.focusCount() {
		t.Fatalf("focus %d not clamped into [0,%d)", m.groupFocus, m.focusCount())
	}

	m.groupFocus = 99 // the render entry must also tolerate it without panicking
	_ = m.groupZoomView()
}

// TestTruncateByDisplayWidth checks truncation counts display cells, so a wide
// glyph occupies its two columns and the result never exceeds the width.
func TestTruncateByDisplayWidth(t *testing.T) {
	cases := []struct {
		s     string
		width int
	}{
		{"hello world", 5},
		{"日本語テスト", 5}, // wide runes (2 cells each)
		{"a日b語c", 4},  // mixed
		{"short", 20}, // no truncation
		{"x", 1},
	}
	for _, c := range cases {
		if got := truncate(c.s, c.width); lipgloss.Width(got) > c.width {
			t.Fatalf("truncate(%q,%d)=%q width %d > %d", c.s, c.width, got, lipgloss.Width(got), c.width)
		}
	}
}

// TestTinyTerminalNotice checks that a viewport below the minimum renders a
// graceful notice instead of flowing into negative-width layout math.
func TestTinyTerminalNotice(t *testing.T) {
	m := baseModel()
	m.width, m.height = 30, 5 // below minWidth (34), wide enough to show the text

	out := m.render()
	if !strings.Contains(out, "too small") {
		t.Fatalf("a tiny terminal should show the too-small notice, got:\n%q", out)
	}
}

// TestNormalSizeRendersDashboard checks the notice does not trip at a normal size.
func TestNormalSizeRendersDashboard(t *testing.T) {
	m := baseModel()
	m.width, m.height = 100, 40

	if out := m.render(); strings.Contains(out, "too small") {
		t.Fatalf("a normal terminal should render the dashboard, not the notice")
	}
}
