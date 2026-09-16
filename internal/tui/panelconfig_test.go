package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/cmj0121/baton/internal/i18n"
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

// TestPanelConfigColumnsAlignInEveryLanguage: the label column is padded to a
// fixed number of DISPLAY CELLS, so the values line up whatever language the page
// is drawn in.
//
// It is the CJK half of the test above, and it catches the opposite mistake. Go's
// %-16s pads to sixteen RUNES, which is right for "default shell" and two cells
// short for every Chinese character in "預設 shell" — so a page that lines up
// perfectly in English comes out ragged in zh-TW, and every assertion written in
// English keeps passing while it does.
func TestPanelConfigColumnsAlignInEveryLanguage(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	m := baseModel()
	m.mode = modePanelConfig
	m.lang = i18n.ZhTW
	m.shellPath = "/bin/zsh"

	// Two rows whose labels differ in width: "預設 shell" is 14 cells over 9 runes,
	// "重播緩衝區" is 10 cells over 5. Padded by rune count they land three cells
	// apart; padded by display width they land on the same column.
	cols := map[string]int{}
	for _, line := range strings.Split(ansi.Strip(m.panelConfigView()), "\n") {
		for label, value := range map[string]string{"預設 shell": "/bin/zsh", "重播緩衝區": "預設"} {
			at := strings.Index(line, label)
			if at < 0 {
				continue
			}
			rest := line[at+len(label):]
			off := strings.Index(rest, value)
			if off < 0 {
				continue
			}
			// Display cells, not bytes: the label ahead of the value is CJK.
			cols[label] = lipgloss.Width(line[:at+len(label)+off])
		}
	}
	if len(cols) != 2 {
		t.Fatalf("expected both rows on the page, found %v", cols)
	}
	if cols["預設 shell"] != cols["重播緩衝區"] {
		t.Errorf("the value column moved with the label's width: %v — the padding counted runes, not cells", cols)
	}
}
