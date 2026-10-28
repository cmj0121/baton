package vtquery

import "testing"

// TestBracketedPaste: the mode is read from either verb, the last toggle in the
// chunk wins, a combined private-mode list counts as much as a lone one, and a
// neighbouring mode number that merely contains 2004 is not mistaken for it.
func TestBracketedPaste(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		on, seen bool
	}{
		{"set", "\x1b[?2004h", true, true},
		{"reset", "\x1b[?2004l", false, true},
		{"last one wins on", "\x1b[?2004l\x1b[?2004h", true, true},
		{"last one wins off", "\x1b[?2004h prompt$ \x1b[?2004l", false, true},
		{"combined list set", "\x1b[?1049;2004h", true, true},
		{"combined list reset", "\x1b[?2004;1049l", false, true},
		{"combined list after lone", "\x1b[?2004h\x1b[?1;2004;25l", false, true},
		{"absent", "plain output", false, false},
		{"other private mode only", "\x1b[?1049h\x1b[?25l", false, false},
		{"lookalike mode number", "\x1b[?12004h\x1b[?20045l", false, false},
		{"not private", "\x1b[2004h", false, false},
		{"amid other output", "ready$ \x1b[0m\x1b[?2004h\x1b]0;title\x07more", true, true},
		{"unrelated mode after", "\x1b[?2004h\x1b[?25l", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			on, seen := BracketedPaste([]byte(c.in))
			if on != c.on || seen != c.seen {
				t.Fatalf("BracketedPaste(%q) = %v/%v, want %v/%v", c.in, on, seen, c.on, c.seen)
			}
		})
	}
}
