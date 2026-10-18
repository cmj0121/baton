package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/vtirm"
)

// titleCases are window titles whose UTF-8 carries a 0x9C, the byte x/ansi takes for a
// string terminator (#148): 服 is E6 9C 8D, Claude Code's ✳ spinner E2 9C B3.
var titleCases = []string{
	"\x1b]0;◐ 服務架構\x07AB",
	"\x1b]0;✳ x\x1b\\AB",
	"\x1bP1$r服務\x1b\\AB",
}

// emuRow0 is the emulator's top row as plain text, trailing blanks dropped.
func emuRow0(emu *vt.SafeEmulator) string {
	row, _, _ := strings.Cut(ansi.Strip(emu.Render()), "\n")
	return strings.TrimRight(row, " ")
}

// TestWriteEmuTitleStaysOffScreen pins #148 at the choke point: a title is not printed,
// so all that reaches the grid is the AB after it, at the left margin. Before the fix the
// title ended inside 服 and 務架構 was printed ahead of AB, moving the cursor along with
// it — which is what then sent every relative repaint of the panel to the wrong rows.
func TestWriteEmuTitleStaysOffScreen(t *testing.T) {
	for _, in := range titleCases {
		emu := vt.NewSafeEmulator(40, 3)
		writeEmu(emu, &vtirm.Filter{}, []byte(in))
		if row := emuRow0(emu); row != "AB" {
			t.Errorf("%q: row 0 = %q, want %q", in, row, "AB")
		}
		if pos := emu.CursorPosition(); pos.X != 2 || pos.Y != 0 {
			t.Errorf("%q: cursor at %d,%d, want 2,0", in, pos.X, pos.Y)
		}
	}
}

// TestWriteEmuTitleSplitAnywhere feeds the same titles in two chunks, split at every
// offset — between ESC and ], inside the rune that carries the 0x9C — as a PTY read can.
func TestWriteEmuTitleSplitAnywhere(t *testing.T) {
	for _, in := range titleCases {
		for i := 0; i <= len(in); i++ {
			emu := vt.NewSafeEmulator(40, 3)
			irm := &vtirm.Filter{}
			writeEmu(emu, irm, []byte(in[:i]))
			writeEmu(emu, irm, []byte(in[i:]))
			row, pos := emuRow0(emu), emu.CursorPosition()
			if row != "AB" || pos.X != 2 || pos.Y != 0 {
				t.Errorf("%q split at %d: row 0 = %q, cursor %d,%d; want \"AB\" at 2,0", in, i, row, pos.X, pos.Y)
			}
		}
	}
}

// TestWriteEmuAbortedTitleKeepsText guards the other direction: once a CAN or SUB has
// aborted a string, the 服 after it is printed text and must reach the grid as written.
func TestWriteEmuAbortedTitleKeepsText(t *testing.T) {
	for _, in := range []string{"\x1b]0;t\x18服", "\x1b]0;t\x1a服", "\x1bPq\x18服", "\x1b_G\x18服"} {
		emu := vt.NewSafeEmulator(40, 3)
		writeEmu(emu, &vtirm.Filter{}, []byte(in))
		if row := emuRow0(emu); row != "服" {
			t.Errorf("%q: row 0 = %q, want %q", in, row, "服")
		}
	}
}
