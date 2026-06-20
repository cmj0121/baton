package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/panel"
)

// fillLines writes n numbered lines into an emulator so its scrollback fills.
func fillLines(emu *vt.SafeEmulator, n int) {
	for i := 0; i < n; i++ {
		_, _ = fmt.Fprintf(emu, "line%d\r\n", i)
	}
}

func TestClipVisible(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"abc", 9, "abc"},           // fits: unchanged
		{"", 4, ""},                 // empty: unchanged
		{"abc", 0, ""},              // zero width
		{"abc", 3, "abc"},           // exact fit, nothing dropped
		{"abcdef", 3, "abc\x1b[0m"}, // clipped: a reset closes any open styling
		// Escapes cost no columns; a clip that lands mid-style still resets.
		{"\x1b[31mabcdef", 3, "\x1b[31mabc\x1b[0m"},
		{"\x1b[31mab\x1b[0m", 5, "\x1b[31mab\x1b[0m"}, // fits, escapes preserved verbatim
	}
	for _, c := range cases {
		if got := clipVisible(c.in, c.width); got != c.want {
			t.Errorf("clipVisible(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

// TestEmuWindowScrollback proves the window shows the live tail at the bottom and
// reveals earlier output once scrolled up into the scrollback buffer.
func TestEmuWindowScrollback(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	fillLines(emu, 12)
	if emu.ScrollbackLen() == 0 {
		t.Fatal("expected scrollback to capture lines that rolled off")
	}

	bottom := strings.Join(emuWindow(emu, 20, 4, 0), "\n")
	if !strings.Contains(bottom, "line11") {
		t.Fatalf("the live bottom should show the latest output, got:\n%s", bottom)
	}
	if strings.Contains(bottom, "line0") {
		t.Fatalf("the live bottom should not show the oldest output, got:\n%s", bottom)
	}

	// Scroll past the top: off is clamped to the buffer depth, so the window rests
	// on the oldest captured line.
	top := strings.Join(emuWindow(emu, 20, 4, 999), "\n")
	if !strings.Contains(top, "line0") {
		t.Fatalf("scrolling to the top should reveal the oldest output, got:\n%s", top)
	}
}

// TestZoomScrollKeys drives the zoom scroll mode: C-t [ enters it, then the
// arrows and page keys navigate the scrollback and esc returns to the bottom.
func TestZoomScrollKeys(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	fillLines(emu, 30)
	// Drain the emulator's input side so feeding a program key never blocks.
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := emu.Read(buf); err != nil {
				return
			}
		}
	}()

	m := model{emu: emu, mode: modeZoom, zoomID: "1", width: 20, height: 5,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	// Enter scroll mode with the leader: C-t [.
	next, _ := m.handleZoomKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = next.(model)
	next, _ = m.handleZoomKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	m = next.(model)
	if !m.scrolling {
		t.Fatal("C-t [ should enter scroll mode")
	}

	// In scroll mode the keys navigate history (routed through handleScrollKey).
	scroll := func(k tea.KeyMsg) {
		next, _ := m.handleScrollKey(k)
		m = next.(model)
	}
	scroll(tea.KeyMsg{Type: tea.KeyUp})
	if m.scrollOff != 1 {
		t.Fatalf("↑ should scroll one line, off = %d", m.scrollOff)
	}
	page := m.scrollOff
	scroll(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if m.scrollOff <= page+1 {
		t.Fatalf("b should page up, off = %d", m.scrollOff)
	}
	scroll(tea.KeyMsg{Type: tea.KeyDown})

	// esc leaves scroll mode and returns to the live bottom.
	scroll(tea.KeyMsg{Type: tea.KeyEsc})
	if m.scrolling || m.scrollOff != 0 {
		t.Fatalf("esc should exit scroll mode at the bottom, scrolling=%v off=%d", m.scrolling, m.scrollOff)
	}
}

// TestGroupScrollKeys scrolls the focused tile's history in the split view.
func TestGroupScrollKeys(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	fillLines(emu, 30)
	m := model{mode: modeGroupZoom, groupName: "g", width: 80, height: 24,
		fleet:      []panel.Panel{{ID: "A", Group: "g", Title: "A"}},
		groupEmus:  map[string]*vt.SafeEmulator{"A": emu},
		groupFocus: 0, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	// Enter scroll mode (C-t [), then b pages the focused tile.
	next, _ := m.handleGroupZoomKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = next.(model)
	next, _ = m.handleGroupZoomKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	m = next.(model)
	if !m.scrolling {
		t.Fatal("C-t [ should enter scroll mode in the group split")
	}
	next, _ = m.handleScrollKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m = next.(model)
	if m.scrollOff <= 0 {
		t.Fatalf("b should scroll the focused tile, off = %d", m.scrollOff)
	}

	// esc leaves scroll mode at the live bottom.
	next, _ = m.handleScrollKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.scrolling || m.scrollOff != 0 {
		t.Fatalf("esc should exit scroll mode, scrolling=%v off=%d", m.scrolling, m.scrollOff)
	}
}

// TestScrollModeKeys covers in-mode navigation: g/G jump to the top and bottom,
// and a stray key is ignored so a fat-finger never drops you out mid-scroll.
func TestScrollModeKeys(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 6)
	fillLines(emu, 40)
	m := model{emu: emu, mode: modeZoom, width: 20, height: 8, scrolling: true,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	scroll := func(r string) {
		next, _ := m.handleScrollKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)})
		m = next.(model)
	}

	scroll("g") // jump to the oldest line
	top := m.scrollOff
	if top == 0 {
		t.Fatal("g should jump to the oldest line")
	}
	scroll("z") // a stray key is ignored — scroll mode holds, offset unchanged
	if !m.scrolling || m.scrollOff != top {
		t.Fatalf("a stray key should be ignored, scrolling=%v off=%d", m.scrolling, m.scrollOff)
	}
	scroll("G") // back to the live bottom
	if m.scrollOff != 0 {
		t.Fatalf("G should jump to the live bottom, off = %d", m.scrollOff)
	}
}
