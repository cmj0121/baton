package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

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
