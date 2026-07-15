package server

import "testing"

func TestTrimToLastScreenReset(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no reset keeps everything", "plain scrollback\nmore", "plain scrollback\nmore"},
		{"trims to a trailing clear", "old junk\x1b[2J\x1b[Hprompt$ ", "\x1b[2J\x1b[Hprompt$ "},
		{
			// The bug: the ring evicted vim's ESC[?1049h, so only its drawing and the
			// later ESC[?1049l (exit alt) survive. Trimming to that exit drops the stray
			// drawing that would otherwise land on the primary grid.
			"trims away vim drawing left after a truncated alt-screen enter",
			"~\x1b[5;1H~\x1b[10;1H:wq\x1b[?1049l\rprompt$ ",
			"\x1b[?1049l\rprompt$ ",
		},
		{
			// A program still in the alternate screen (vim running) with the enter as the
			// last reset: it is kept, so the alt-screen view reconstructs faithfully.
			"keeps the alternate screen while it is still open",
			"shell output\x1b[?1049h\x1b[Hediting buffer",
			"\x1b[?1049h\x1b[Hediting buffer",
		},
		{"trims to the last of several resets", "a\x1b[2Jb\x1b[2Jc", "\x1b[2Jc"},
		{"RIS is a reset", "garbage\x1bcfresh", "\x1bcfresh"},
		{"older alt-screen code (47) counts", "x\x1b[?47lback", "\x1b[?47lback"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(trimToLastScreenReset([]byte(c.in))); got != c.want {
				t.Errorf("trimToLastScreenReset(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestReplayConditioningComposes checks the two replay filters compose the way
// attach applies them: trim to the last screen reset, then strip queries from what
// survives — so pre-clear drawing AND a stale query after it both vanish.
func TestReplayConditioningComposes(t *testing.T) {
	in := []byte("vim paint\x1b[20;1H~\x1b[2J\x1b[Hready\x1b[c$ ")
	got := string(stripReplayQueries(trimToLastScreenReset(in)))
	if want := "\x1b[2J\x1b[Hready$ "; got != want {
		t.Errorf("conditioned replay = %q, want %q", got, want)
	}
}

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
		{"in-band-resize enable", "\x1b[?2048h", ""}, // answered with an immediate "48;rows;cols…t" report — a bogus resize on every re-attach
		{"in-band-resize enable amid output", "row\x1b[?2048hcol", "rowcol"},
		{"keeps in-band-resize disable", "\x1b[?2048l", "\x1b[?2048l"}, // reset triggers no report; nothing to strip
		{"keeps alt-screen enable", "\x1b[?1049h", "\x1b[?1049h"},      // a mode-set that draws — must survive for trimToLastScreenReset
		{"keeps cursor-visibility set", "\x1b[?25h", "\x1b[?25h"},      // an ordinary mode-set, not a report trigger
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
