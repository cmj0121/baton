package vtirm

import (
	"math/rand"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// TestMaskST tables what a chunk looks like on its way to the parser: a 0x9C inside a
// string becomes 0xBF, and every byte outside one comes out exactly as it went in.
func TestMaskST(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The bug from the issue: 服 is E6 9C 8D.
		{"a CJK title", "\x1b]0;◐ 服務架構\x07AB", "\x1b]0;◐ \xe6\xbf\x8d務架構\x07AB"},
		// Claude Code's ✳ spinner is E2 9C B3, and its title ends on ESC \ rather than BEL.
		{"a Dingbat title ended by ST", "\x1b]0;✳ x\x1b\\AB", "\x1b]0;\xe2\xbf\xb3 x\x1b\\AB"},
		{"a DCS payload", "\x1bP1$r服\x1b\\", "\x1bP1$r\xe6\xbf\x8d\x1b\\"},
		{"a DCS that starts its payload at once", "\x1bPq服\x1b\\", "\x1bPq\xe6\xbf\x8d\x1b\\"},
		{"two strings in a row", "\x1b]0;服\x07\x1b]2;服\x07", "\x1b]0;\xe6\xbf\x8d\x07\x1b]2;\xe6\xbf\x8d\x07"},

		// Everything below must come out byte for byte as it went in.
		{"on-screen text", "服務 ✳", "服務 ✳"},
		{"text after a title", "\x1b]0;t\x07服", "\x1b]0;t\x07服"},
		{"text after a CAN-aborted title", "\x1b]0;t\x18服", "\x1b]0;t\x18服"},
		{"text after a SUB-aborted title", "\x1b]0;t\x1a服", "\x1b]0;t\x1a服"},
		{"text after a CAN-aborted DCS", "\x1bPq\x18服", "\x1bPq\x18服"},
		// The table ends an APC on any byte from 0x80 up, so the character after it is
		// printed text, and a rewrite there would not keep the string open anyway.
		{"an APC", "\x1b_G服\x1b\\", "\x1b_G服\x1b\\"},
		{"a PM", "\x1b^服\x1b\\", "\x1b^服\x1b\\"},
		{"a SOS", "\x1bX服\x1b\\", "\x1bX服\x1b\\"},
		// A BEL ends an OSC but is payload in a DCS, so the 服 after it is still inside.
		{"BEL does not end a DCS", "\x1bPq\x07服\x1b\\", "\x1bPq\x07\xe6\xbf\x8d\x1b\\"},
		// A UTF-8 lead swallows the next bytes whole, ESC included, so this ] is text.
		{"an ESC inside a rune starts nothing", "\xe6\x1b]0;服", "\xe6\x1b]0;服"},
		{"a CSI", "\x1b[31m服\x1b[0m", "\x1b[31m服\x1b[0m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var f Filter
			if got := string(f.MaskST([]byte(c.in))); got != c.want {
				t.Errorf("MaskST(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestMaskSTAcrossChunks splits each case at every offset: the state has to survive a
// boundary between ESC and ], inside the title, and inside the very rune the 0x9C is in.
func TestMaskSTAcrossChunks(t *testing.T) {
	for _, in := range []string{
		"\x1b]0;◐ 服務架構\x07AB",
		"\x1b]0;✳ x\x1b\\AB",
		"\x1b]0;t\x18服",
		"\x1bPq\x07服\x1b\\服",
		"\xe6\x1b]0;服",
	} {
		var whole Filter
		want := string(whole.MaskST([]byte(in)))
		for i := 0; i <= len(in); i++ {
			var f Filter
			got := string(f.MaskST([]byte(in[:i]))) + string(f.MaskST([]byte(in[i:])))
			if got != want {
				t.Errorf("MaskST(%q) split at %d = %q, want %q", in, i, got, want)
			}
		}
	}
}

// TestMaskSTTracksTheParser is the guard on the risk that matters most: the mask has to
// agree with the emulator's parser on where every string starts and ends, or it rewrites
// a 0x9C in printed text. It feeds random streams — heavy in ESC, the string introducers,
// their terminators, 0x9C and multi-byte runes — to both, the parser getting the masked
// bytes as the emulator would, and compares their states after every byte.
func TestMaskSTTracksTheParser(t *testing.T) {
	alphabet := []string{
		"\x1b", "]", "P", "_", "^", "X", "[", "\\", "\x07", "\x18", "\x1a", "\x9c", "\x9d",
		"\x90", "\x80", "\xbf", "\xe6", "\xe2", "\xf0", "a", "0", ";", "$", "服", "✳", "é",
	}
	rng := rand.New(rand.NewSource(148))
	for round := range 2000 {
		var stream []byte
		for range 1 + rng.Intn(40) {
			stream = append(stream, alphabet[rng.Intn(len(alphabet))]...)
		}
		var f Filter
		p := ansi.NewParser()
		for i := range stream {
			for _, c := range f.MaskST(stream[i : i+1]) {
				p.Advance(c)
			}
			want := p.State()
			got := f.vtState
			if want == parser.Utf8State {
				if f.utf8Left == 0 {
					t.Fatalf("round %d: %q at byte %d: parser mid-rune, mask is not", round, stream, i)
				}
				continue
			}
			if f.utf8Left != 0 || got != want {
				t.Fatalf("round %d: %q at byte %d: mask in %s (rune %d left), parser in %s",
					round, stream, i, parser.StateNames[got], f.utf8Left, parser.StateNames[want])
			}
		}
	}
}

// TestMaskSTFastPath pins the cost on an ordinary chunk: the caller's own slice back, no
// allocation, even when it carries a title with nothing to rewrite.
func TestMaskSTFastPath(t *testing.T) {
	var f Filter
	b := []byte("\x1b]0;title\x07\x1b[31m服務\x1b[0m")
	if allocs := testing.AllocsPerRun(100, func() { _ = f.MaskST(b) }); allocs != 0 {
		t.Errorf("MaskST allocated %v times on a chunk with nothing to rewrite", allocs)
	}
	if got := f.MaskST(b); &got[0] != &b[0] {
		t.Error("MaskST copied a chunk it did not change")
	}

	in := []byte("\x1b]0;服\x07")
	if f.MaskST(in); string(in) != "\x1b]0;服\x07" {
		t.Errorf("MaskST wrote through to the caller's slice: %q", in)
	}

	var nilFilter *Filter
	if got := nilFilter.MaskST(in); &got[0] != &in[0] {
		t.Error("a nil Filter must pass the chunk through untouched")
	}
}
