package tui

import (
	"strings"
	"testing"
)

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
