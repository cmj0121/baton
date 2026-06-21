package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/panel"
)

// drainInput reads the emulator's input side (encoded keys / query replies) so a
// Write that generates a reply never blocks. Mirrors the product's zoomReader; it
// captures the stable emulator pointer, not the model.
func drainInput(emu *vt.SafeEmulator) {
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := emu.Read(buf); err != nil {
				return
			}
		}
	}()
}

// vimSeqs are sequences a real vim / full-screen program emits.
var vimSeqs = []string{
	"\x1b[?1049h", "\x1b[>4;2m", "\x1b[?2004h", "\x1b[?1006h\x1b[?1002h",
	"\x1b[1;24r", "\x1b[2J", "\x1b[?25l",
	"\x1b[1;1H~", "\x1b[24;1H\x1b[1m-- INSERT --\x1b[0m",
	"\x1b]11;rgb:2020/2020/2020\x07", "\x1b]10;?\x07",
	"\x1bP$qm\x1b\\", "\x1b[6n", "\x1b[?u", "\x1b[c",
	"\x1b[38;2;255;128;0mcolor\x1b[0m", "\x1b[12;200H世界 wide 你好",
	"\x1b[?2004l", "\x1b[?1049l", "\x1b[?25h", "\x1b[>4;0m",
	"\x1b[200~pasted\x1b[201~", "\x1b[8;50;200t",
}

// TestEmulatorSurvivesVimSequences feeds vim-style sequences (and a resize) at the
// emulator and renders, to catch a parser panic that would crash the client.
func TestEmulatorSurvivesVimSequences(t *testing.T) {
	emu := vt.NewSafeEmulator(80, 24)
	drainInput(emu)
	for _, s := range vimSeqs {
		writeEmu(emu, []byte(s))
		_ = emu.Render()
		_ = emu.CursorPosition()
	}
	emu.Resize(40, 10)
	c := emu.CursorPosition()
	_ = overlayCursor(strings.Split(emu.Render(), "\n")[0], c.X)
}

// TestZoomVimDetach exercises the full zoom→dashboard flow with vim sequences.
func TestZoomVimDetach(t *testing.T) {
	m := model{width: 80, height: 24, fleet: []panel.Panel{{ID: "1", Title: "vim"}},
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m = m.zoomInto(panel.Panel{ID: "1", Title: "vim"})
	emu := m.emu // stable pointer, like the product's zoomReader
	drainInput(emu)
	for _, s := range vimSeqs {
		writeEmu(emu, []byte(s))
		_ = m.View()
	}
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m = m2.(model)
	_ = m.View()
	next, _ := m.zoomDetach()
	_ = next.(model).View()
}

// TestZoomRealVimOutput feeds bytes captured from a real vim PTY session into the
// zoom, then resizes and detaches — the exact path the user hit.
func TestZoomRealVimOutput(t *testing.T) {
	raw, err := os.ReadFile("testdata/vimcap.bin")
	if err != nil {
		t.Skipf("no vim capture: %v", err)
	}
	m := model{width: 80, height: 24, fleet: []panel.Panel{{ID: "1", Title: "vim"}},
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m = m.zoomInto(panel.Panel{ID: "1", Title: "vim"})
	emu := m.emu
	drainInput(emu)
	for i := 0; i < len(raw); i += 37 {
		end := min(i+37, len(raw))
		writeEmu(emu, raw[i:end])
		_ = m.View()
	}
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m = m2.(model)
	_ = m.View()
	next, _ := m.zoomDetach()
	_ = next.(model).View()
}
