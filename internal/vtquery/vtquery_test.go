package vtquery

import "testing"

func TestStrip(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"device attributes", "a\x1b[cb", "ab"},
		{"DA with params", "\x1b[?62;1;6;22c", ""}, // primary-DA query, source of the "62;1;6;22c" garbage
		{"secondary DA", "x\x1b[>0cy", "xy"},
		{"cursor position request", "\x1b[6n", ""},
		{"device status report", "p\x1b[5nq", "pq"},
		{"xtversion", "\x1b[>0q", ""},
		{"decrqm", "\x1b[?2026$p", ""},
		{"osc bg colour query (BEL)", "\x1b]11;?\x07", ""},
		{"osc fg colour query (ST)", "\x1b]10;?\x1b\\", ""},
		{"osc palette query", "\x1b]4;1;?\x07", ""},
		{"in-band-resize enable", "\x1b[?2048h", ""}, // answered with an immediate "48;rows;cols…t" report
		{"in-band-resize enable amid output", "row\x1b[?2048hcol", "rowcol"},
		{"keeps in-band-resize disable", "\x1b[?2048l", "\x1b[?2048l"}, // reset triggers no report
		{"keeps alt-screen enable", "\x1b[?1049h", "\x1b[?1049h"},      // a mode-set that draws
		{"keeps cursor-visibility set", "\x1b[?25h", "\x1b[?25h"},      // ordinary mode-set, not a report trigger
		{
			// A /clear re-init burst: alt-screen re-enter draws and is kept; the DA, OSC-11
			// and in-band-resize probes that would each be answered are stripped.
			"clear re-init burst keeps drawing, drops probes",
			"\x1b[?1049h\x1b[2J\x1b[?2048h\x1b[c\x1b]11;?\x07ready$ ",
			"\x1b[?1049h\x1b[2Jready$ ",
		},
		{"keeps rendering sequences", "\x1b[1mbold\x1b[0m\x1b[2J\x1b[H", "\x1b[1mbold\x1b[0m\x1b[2J\x1b[H"},
		{"keeps cursor moves and colours", "\x1b[31mred\x1b[10;5Hmoved", "\x1b[31mred\x1b[10;5Hmoved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Strip([]byte(c.in))); got != c.want {
				t.Errorf("Strip(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestStripNoMatchReturnsSameSlice checks the allocation-free fast path: a chunk with no
// query is returned as the identical backing array, not a copy.
func TestStripNoMatchReturnsSameSlice(t *testing.T) {
	in := []byte("plain output with \x1b[31mcolour\x1b[0m but no query")
	got := Strip(in)
	if len(got) == 0 || &got[0] != &in[0] {
		t.Errorf("Strip allocated on a no-match input; want the same backing slice")
	}
}
