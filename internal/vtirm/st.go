package vtirm

import "github.com/charmbracelet/x/ansi/parser"

// stMasked is what a 0x9C inside a string sequence becomes: another UTF-8 continuation
// byte, which the parser's string states collect as payload like any other. One byte for
// one byte, so nothing is held back and nothing after it moves.
const stMasked = 0xBF

// MaskST returns b with every 0x9C that sits inside an OSC or DCS string rewritten to a
// byte the parser keeps reading through, so the string ends at its real terminator.
//
// x/ansi's parser ends those strings on 0x9C, the 8-bit ST, even when the byte is the
// middle of a UTF-8 character — and in UTF-8 it often is: 服 is E6 9C 8D, and every
// Dingbat from ✀ to ✿ (Claude Code's ✳ spinner among them) is E2 9C xx. Claude Code
// retitles its terminal with OSC 0 on every spinner frame, so "\x1b]0;◐ 服務架構\x07"
// ends after 服's first two bytes and 務架構 is printed onto the grid, moving the cursor.
// Every relative repaint after that lands on the wrong rows and the panel garbles
// (github.com/cmj0121/baton#148, upstream charmbracelet/x#848). ansi.DecodeSequence,
// which Rewrite parses with, shares the bug, so this runs before Rewrite — and before
// anything else that reads the stream on its way to the emulator.
//
// A panel stream is UTF-8, where a raw 8-bit ST is never what a program meant (Rewrite
// makes the same assumption about the 8-bit CSI), so a 0x9C inside a string is always a
// continuation byte. Rewriting it to another continuation byte costs one wrong glyph in a
// title nobody sees; dropping the whole character instead would mean holding a rune split
// across chunks.
//
// Where a string starts and ends is decided by driving the parser's own transition table,
// byte for byte as the emulator will, so a 0x9C is only touched where the emulator would
// have mistaken it for an ST: never in printed text, never after a CAN or SUB aborted the
// string, never in the bytes a UTF-8 lead swallows whole. SOS, PM and APC strings are
// left alone — the table ends those on any byte from 0x80 up, a 0xBF included, so a
// rewrite there would change nothing the emulator does.
//
// The state carries across calls, because a chunk boundary can fall anywhere: between
// ESC and ], inside the title, inside the character the 0x9C belongs to. The caller's
// slice is returned as-is unless a byte actually changes, so the walk allocates nothing
// on the stream's ordinary chunks. A nil *Filter passes b through untouched.
func (f *Filter) MaskST(b []byte) []byte {
	if f == nil {
		return b
	}
	out, copied := b, false
	for i, c := range b {
		if f.utf8Left > 0 {
			// The parser collects a rune's bytes without looking at them, whatever they
			// are, so neither can this walk.
			f.utf8Left--
			continue
		}
		if c == 0x9C && (f.vtState == parser.OscStringState || f.vtState == parser.DcsStringState) {
			if !copied {
				out, copied = append([]byte(nil), b...), true
			}
			out[i] = stMasked
			continue // 0xBF is payload in both states: the state does not change
		}
		next, _ := parser.Table.Transition(f.vtState, c)
		if next == parser.Utf8State {
			f.utf8Left = utf8Len(c) - 1
			next = parser.GroundState // where the parser lands once the rune is printed
		}
		f.vtState = next
	}
	return out
}

// utf8Len is the parser's own measure of a rune from its lead byte. The table only routes
// C2..F4 into the UTF-8 state, all of which have a length.
func utf8Len(lead byte) int {
	switch {
	case lead >= 0xF0:
		return 4
	case lead >= 0xE0:
		return 3
	default:
		return 2
	}
}
