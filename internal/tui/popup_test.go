package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestPopupWidthIsFixed checks the pop-up width is the fixed popupCols on any
// terminal wide enough to hold it, and only shrinks when the terminal cannot.
func TestPopupWidthIsFixed(t *testing.T) {
	chrome := 2*popupPadX + 2 // the padding and the border around the content

	for _, tc := range []struct {
		name  string
		width int
		want  int
	}{
		{"unsized", 0, popupCols},
		{"exact fit", popupCols + chrome, popupCols},
		{"wide", 200, popupCols},
		{"wider still", 400, popupCols},
		{"narrow", 80, 80 - chrome},
		{"tiny", 20, popupMinCols},
	} {
		if got := (model{width: tc.width, height: 40}).popupWidth(); got != tc.want {
			t.Errorf("%s: popupWidth() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestPopupBoxHoldsItsWidth checks the box is the same width whatever it holds:
// a short line, a line far longer than the frame, and full-width CJK text all
// render at exactly popupWidth plus the chrome, on one line each.
func TestPopupBoxHoldsItsWidth(t *testing.T) {
	m := model{width: 200, height: 40}
	want := m.popupWidth() + 2*popupPadX + 2

	for _, tc := range []struct {
		name, body string
	}{
		{"short", "hi"},
		{"empty", ""},
		{"long", strings.Repeat("long content ", 40)},
		{"cjk", strings.Repeat("固定寬度", 40)},
		{"styled", mutedStyle.Render(strings.Repeat("styled ", 40))},
	} {
		box := m.popupBox(tc.body)
		if got := lipgloss.Width(box); got != want {
			t.Errorf("%s: box width = %d, want %d", tc.name, got, want)
		}
		// border (2) + padding (2) + the single content line: an over-long line is
		// clipped to the frame, never wrapped onto a second row.
		if got := lipgloss.Height(box); got != 5 {
			t.Errorf("%s: box height = %d, want 5 (no wrapping)", tc.name, got)
		}
	}
}

// TestPopupViewsShareOneWidth checks the overlays really are interchangeable in
// place: every pop-up view renders at the one width, so switching between them
// does not breathe the frame in and out.
func TestPopupViewsShareOneWidth(t *testing.T) {
	m := model{width: 160, height: 48, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	want := m.popupWidth() + 2*popupPadX + 2

	views := map[string]string{
		"help":         m.helpView(),
		"key map":      m.keyMapView(),
		"panel config": m.panelConfigView(),
		"signals":      m.signalPickerView(),
		"commands":     m.commandPickerView(),
		"git":          m.gitPickerView(),
		"queue":        m.queueView(),
		"proc tree":    m.procTreeView(),
		"fleet search": m.fleetSearchView(),
		"git output":   m.openGitOutPopup("git status", "on branch main", false).gitOutView(),
	}
	for name, view := range views {
		if got := lipgloss.Width(view); got != want {
			t.Errorf("%s view width = %d, want %d", name, got, want)
		}
	}
}
