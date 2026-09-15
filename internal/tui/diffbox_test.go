package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// diffbox_test.go is the diff popup's half of #94.
//
// The issue asks for it in the same change as the inbox, and the reason is in
// the code: inboxViewportRows was DELIBERATELY diffViewportRows so the two
// frames do not jump when you move between them. Fixing only the inbox does not
// leave the diff alone — it re-introduces the jump that comment exists to
// prevent, and leaves the popup you reach from an agent panel still overflowing
// the terminal.

// TestDiffBoxFitsTheTerminal is the same overflow the inbox had: the body is
// sized from height alone while the overlay sits under the banner, so the
// composed frame is taller than the terminal and Place hands back the overflow
// rather than clipping it. The bottom edge of the box is the first thing gone.
func TestDiffBoxFitsTheTerminal(t *testing.T) {
	pinRender(t)
	for _, height := range []int{24, 28, 32, 36, 40} {
		m := diffModel()
		m.width, m.height = 120, height
		view := m.View().Content
		if got := strings.Count(view, "\n") + 1; got > height {
			t.Errorf("at height %d the diff frame is %d rows — it overflows the terminal", height, got)
		}
		if !strings.Contains(view, "╰") {
			t.Errorf("at height %d the diff box has no bottom edge:\n%s", height, view)
		}
	}
}

// TestDiffAndInboxAgreeOnSize is the property the shared sizing existed for,
// asserted directly rather than left to a comment: moving between the two
// overlays must not resize the frame under the operator.
func TestDiffAndInboxAgreeOnSize(t *testing.T) {
	pinRender(t)
	d := diffModel()
	d.width, d.height = 120, 40

	i := openedInbox(t, wire("1", "attention", 90*1000*1000*1000))
	i.width, i.height = 120, 40

	dh, ih := lipgloss.Height(d.diffView()), lipgloss.Height(i.inboxView())
	if dh != ih {
		t.Errorf("the diff popup draws %d rows and the inbox %d — moving between them jumps", dh, ih)
	}
}

// TestDiffBoxIsTheSameSizeWhenEmpty holds the diff popup to what the inbox now
// promises: a box that does not change size under a state that only changes
// what is listed.
func TestDiffBoxIsTheSameSizeWhenEmpty(t *testing.T) {
	pinRender(t)
	full := diffModel()
	full.width, full.height = 120, 40
	want := lipgloss.Height(full.diffView())

	empty := diffModel()
	empty.width, empty.height = 120, 40
	empty.diffFiles = nil
	if got := lipgloss.Height(empty.diffView()); got != want {
		t.Errorf("an empty diff draws a %d-row box, want %d", got, want)
	}
	if !strings.Contains(empty.diffView(), "no changes to diff") {
		t.Error("the empty diff no longer says so")
	}
}
