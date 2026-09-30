package tui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestClipVisibleCounts cells, not runes: a scrollback line of CJK captured at a
// wider size must come back no wider than its tile, or the terminal wraps it and
// every row below shifts (#140).
func TestClipVisibleCounts(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"中文中文中文", 6, "中文中\x1b[0m"},
		{"中文中文中文", 5, "中文\x1b[0m"}, // a wide glyph that would straddle the cut is dropped
		{"ab中c", 4, "ab中\x1b[0m"},
		{"\x1b[31m中文\x1b[0m中", 4, "\x1b[31m中文\x1b[0m\x1b[0m"},
		{"abc", 5, "abc"}, // fits: no clip, no reset
	}
	for _, c := range cases {
		got := clipVisible(c.in, c.width)
		if got != c.want {
			t.Errorf("clipVisible(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
		if w := ansi.StringWidth(got); w > c.width {
			t.Errorf("clipVisible(%q, %d) is %d cells wide", c.in, c.width, w)
		}
	}
}
