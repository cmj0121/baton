package server

import "testing"

func TestStripReplayQueries(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"device attributes", "a\x1b[cb", "ab"},
		{"DA with params", "\x1b[?62;1;6;22c", ""}, // a primary-DA query, the source of the "62;1;6;22c" garbage
		{"secondary DA", "x\x1b[>0cy", "xy"},
		{"cursor position request", "\x1b[6n", ""},
		{"device status report", "p\x1b[5nq", "pq"},
		{"xtversion", "\x1b[>0q", ""},
		{"decrqm", "\x1b[?2026$p", ""},
		{"osc bg colour query (BEL)", "\x1b]11;?\x07", ""},
		{"osc fg colour query (ST)", "\x1b]10;?\x1b\\", ""},
		{"osc palette query", "\x1b]4;1;?\x07", ""},
		{
			// The exact shape the bug report showed: a DA reply and an OSC-11 reply that a
			// replay re-triggers. Both their query forms are stripped from the replay.
			"mixed queries around content",
			"hello\x1b[c world\x1b]11;?\x07!",
			"hello world!",
		},
		{"keeps rendering sequences", "\x1b[1mbold\x1b[0m\x1b[2J\x1b[H", "\x1b[1mbold\x1b[0m\x1b[2J\x1b[H"},
		{"keeps cursor moves and colours", "\x1b[31mred\x1b[10;5Hmoved", "\x1b[31mred\x1b[10;5Hmoved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(stripReplayQueries([]byte(c.in))); got != c.want {
				t.Errorf("stripReplayQueries(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
