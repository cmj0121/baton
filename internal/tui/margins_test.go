package tui

import (
	"strings"
	"testing"

	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/vtirm"
)

// TestWriteEmuSurvivesTallScrollRegion pins #139 at the choke point: a program that
// still believes a taller PTY sets a scroll region past this emulator's last row,
// then deletes a line inside it. Unclamped, the emulator indexed past its buffer,
// the write panicked, and every byte after the delete in that chunk was lost —
// here, the prompt. Clamped, the chunk lands whole.
func TestWriteEmuSurvivesTallScrollRegion(t *testing.T) {
	for _, op := range []string{"\x1b[M", "\x1b[L", "\x1bM", "\x1b[2S", "\x1b[2T"} {
		emu := vt.NewSafeEmulator(80, 24)
		irm := &vtirm.Filter{}
		writeEmu(emu, irm, []byte("\x1b[1;40r\x1b[5;1Hold"))
		writeEmu(emu, irm, []byte("\x1b[5;1H"+op+"\x1b[24;1H$ prompt"))
		if !strings.Contains(emu.Render(), "$ prompt") {
			t.Errorf("%q after a too-tall margin: the rest of the chunk was dropped", op)
		}
	}
}

// TestWriteEmuKeepsScrollRegionInside proves the clamp is a no-op for a region that
// fits: the margins the program asked for are the ones the emulator scrolls within.
func TestWriteEmuKeepsScrollRegionInside(t *testing.T) {
	emu := vt.NewSafeEmulator(80, 24)
	irm := &vtirm.Filter{}
	writeEmu(emu, irm, []byte("\x1b[1;1Hkeep\x1b[2;24r\x1b[24;1Hbottom\n"))
	rows := strings.Split(emu.Render(), "\n")
	if !strings.HasPrefix(rows[0], "keep") {
		t.Fatalf("row 0 = %q: the line above the region must not scroll", rows[0])
	}
	if !strings.HasPrefix(rows[22], "bottom") {
		t.Fatalf("row 22 = %q: the region should have scrolled its bottom line up", rows[22])
	}
}
