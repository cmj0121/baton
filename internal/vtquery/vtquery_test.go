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

// TestReply is the regression guard for the server-side device-attributes responder:
// baton has no server emulator, so pump must answer DA queries on the PTY itself. A
// missing answer wedges a full-screen program's handshake — the concrete symptom being
// nvim's Ctrl-Z suspend never taking effect (it blocks on the DA reply). The replies
// must stay byte-identical to what the client x/vt emulator returned before the
// live-strip stopped it from answering.
func TestReply(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"DA1 bare", "\x1b[c", da1Reply},                    // nvim's suspend/resume probe
		{"DA1 with zero param", "\x1b[0c", da1Reply},        // the explicit CSI 0 c form
		{"DA1 embedded in output", "hi\x1b[cbye", da1Reply}, // answered wherever it lands
		{"DA2 secondary", "\x1b[>c", da2Reply},
		{"DA2 with params", "\x1b[>0c", da2Reply},
		{"DA3 tertiary has no canned reply", "\x1b[=c", ""},
		{"two queries answered in order", "\x1b[c\x1b[>c", da1Reply + da2Reply},
		{"no query", "plain \x1b[31mred\x1b[0m text", ""},
		{"cursor-position DSR is left to the client emulator", "\x1b[6n", ""},
		{"device-status DSR is not a DA", "\x1b[5n", ""},
		{"DECRQM mode query is not a DA", "\x1b[?2026$p", ""},
		{"modifyOtherKeys reset is not a DA query", "\x1b[>4;0m", ""}, // ends in m, seen in nvim output
		{"in-band-resize enable is not a DA", "\x1b[?2048h", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Reply([]byte(c.in))); got != c.want {
				t.Errorf("Reply(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestReplyNoQueryIsNil pins the allocation-free path: a chunk with no DA query returns
// nil so pump writes nothing back to the PTY.
func TestReplyNoQueryIsNil(t *testing.T) {
	if got := Reply([]byte("just some \x1b[1mbold\x1b[0m output")); got != nil {
		t.Errorf("Reply on a no-query chunk = %q, want nil", got)
	}
}
