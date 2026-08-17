package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/vtirm"
)

// shellInsertBurst is what a real bash panel puts on the wire for the steps in
// github.com/cmj0121/baton#10 — captured from a $SHELL driven under a PTY sized the way
// ptymgr sizes a panel. The prompt and the line are drawn, C-a walks the cursor back with
// backspaces, and the character typed there arrives wrapped in IRM rather than as an ICH.
const shellInsertBurst = "$ " + // the prompt
	"ls a/b/c/d" + // the line, as typed
	"\b\b\b\b\b\b\b\b\b\b" + // C-a: back to the front of the line
	"\x1b[4ha\x1b[4l" // the character, printed under insert mode

// TestZoomHonoursShellInsertMode is the regression guard for #10: a character typed into
// the middle of a line has to push its neighbours right, not land on top of one. Before
// the insert-mode rewrite the emulator drew "as a/b/c/d", because x/vt overwrites the cell
// under the cursor whatever mode 4 says.
func TestZoomHonoursShellInsertMode(t *testing.T) {
	emu := vt.NewSafeEmulator(40, 4)
	drainInput(emu)
	writeEmu(emu, &vtirm.Filter{}, []byte(shellInsertBurst))

	const want = "$ als a/b/c/d"
	if got := firstRow(emu); got != want {
		t.Errorf("zoom rendered %q, want %q", got, want)
	}
}

// TestZoomHonoursShellInsertModeSplit feeds the same burst a byte at a time. The bytes
// reach a panel in whatever sizes the PTY read and the socket produced, so the rewrite has
// to survive a boundary anywhere — including inside the escape sequence that turns insert
// mode on, and inside the one that turns it off again.
func TestZoomHonoursShellInsertModeSplit(t *testing.T) {
	emu := vt.NewSafeEmulator(40, 4)
	drainInput(emu)
	irm := &vtirm.Filter{}
	for i := range len(shellInsertBurst) {
		writeEmu(emu, irm, []byte(shellInsertBurst[i:i+1]))
	}

	const want = "$ als a/b/c/d"
	if got := firstRow(emu); got != want {
		t.Errorf("zoom rendered %q from single-byte chunks, want %q", got, want)
	}
}

// TestZoomKeepsOverwriteOutsideInsertMode is the other half of the guard: without mode 4
// a printed character still overwrites, so the rewrite cannot be inserting everywhere.
func TestZoomKeepsOverwriteOutsideInsertMode(t *testing.T) {
	emu := vt.NewSafeEmulator(40, 4)
	drainInput(emu)
	writeEmu(emu, &vtirm.Filter{}, []byte("$ ls a/b/c/d\b\b\b\b\b\b\b\b\b\ba"))

	const want = "$ as a/b/c/d"
	if got := firstRow(emu); got != want {
		t.Errorf("zoom rendered %q, want %q", got, want)
	}
}

// TestInsertModeLeavesOtherOutputAlone is the no-regression guard for the rewrite sitting
// on every emulator-bound byte. It replays bytes captured from a real vim PTY session —
// escape-heavy output that never asks for insert mode — through two emulators, one fed
// through the filter and one not, at several chunk sizes, and requires the two screens to
// come out identical cell for cell.
func TestInsertModeLeavesOtherOutputAlone(t *testing.T) {
	raw, err := os.ReadFile("testdata/vimcap.bin")
	if err != nil {
		t.Skipf("no vim capture: %v", err)
	}

	for _, size := range []int{1, 7, 37, 512} {
		t.Run(fmt.Sprintf("chunks of %d", size), func(t *testing.T) {
			plain, filtered := vt.NewSafeEmulator(80, 24), vt.NewSafeEmulator(80, 24)
			drainInput(plain)
			drainInput(filtered)
			irm := &vtirm.Filter{}

			for i := 0; i < len(raw); i += size {
				chunk := raw[i:min(i+size, len(raw))]
				writeEmu(plain, nil, chunk)
				writeEmu(filtered, irm, chunk)
			}
			if got, want := filtered.Render(), plain.Render(); got != want {
				t.Errorf("the filter changed a stream that never sets insert mode:\ngot  %q\nwant %q", got, want)
			}
		})
	}
}

// firstRow is the emulator's top line with the trailing blanks cut off.
func firstRow(emu *vt.SafeEmulator) string {
	return strings.TrimRight(strings.Split(emu.Render(), "\n")[0], " ")
}

func benchStream(b *testing.B) []byte {
	raw, err := os.ReadFile("testdata/vimcap.bin")
	if err != nil {
		b.Skip(err)
	}
	return raw
}

func BenchmarkWriteEmuNoFilter(b *testing.B) {
	raw := benchStream(b)
	emu := vt.NewSafeEmulator(80, 24)
	drainInput(emu)
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for b.Loop() {
		for i := 0; i < len(raw); i += 512 {
			writeEmu(emu, nil, raw[i:min(i+512, len(raw))])
		}
	}
}

func BenchmarkWriteEmuFilter(b *testing.B) {
	raw := benchStream(b)
	emu := vt.NewSafeEmulator(80, 24)
	drainInput(emu)
	irm := &vtirm.Filter{}
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for b.Loop() {
		for i := 0; i < len(raw); i += 512 {
			writeEmu(emu, irm, raw[i:min(i+512, len(raw))])
		}
	}
}

func BenchmarkWriteEmuFilterPlainText(b *testing.B) {
	raw := make([]byte, 4096)
	for i := range raw {
		raw[i] = byte('a' + i%26)
	}
	emu := vt.NewSafeEmulator(80, 24)
	drainInput(emu)
	irm := &vtirm.Filter{}
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for b.Loop() {
		writeEmu(emu, irm, raw)
	}
}
