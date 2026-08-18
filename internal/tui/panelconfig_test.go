package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// TestPanelConfigRowsAlign pins the one thing the page's layout promises: the
// values line up in a column, whichever row the cursor is on.
//
// It is a test worth having because the failure is invisible to every other one.
// The rows are built by padding the label to a fixed width, and padding it after
// it has been styled counts the escape sequences instead of the letters — so the
// column silently collapses ("default shellsystem default") while every
// assertion about content keeps passing.
func TestPanelConfigRowsAlign(t *testing.T) {
	// Force a colour profile. A test binary has no TTY, so lipgloss renders plain
	// text by default — and plain text is exactly the case where padding the
	// rendered string happens to work. Without this the test cannot see the bug it
	// exists for.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	m := baseModel()
	m.mode = modePanelConfig

	// The cursor row is styled differently from the rest, so check the alignment
	// holds with the caret on each row in turn rather than only on the first.
	for cursor := 0; cursor < numPanelConfigRows; cursor++ {
		m.cursor = cursor
		labelEnd, valueStart := -1, -1
		for _, line := range strings.Split(ansi.Strip(m.panelConfigView()), "\n") {
			at := strings.Index(line, "default shell")
			if at < 0 {
				continue
			}
			labelEnd = at + len("default shell")
			valueStart = at + strings.Index(line[at:], "system default")
			break
		}
		if valueStart < 0 {
			t.Fatalf("cursor %d: the default-shell row is not on the page", cursor)
		}
		if valueStart <= labelEnd {
			t.Fatalf("cursor %d: the value starts at column %d, on top of a label ending at %d — the padding was applied to the styled string",
				cursor, valueStart, labelEnd)
		}
	}
}
