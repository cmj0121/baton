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

// TestZoomScrollKeys drives the zoom scrollback keys: shift+up/pgup walk back,
// shift+down comes forward, and any program key snaps back to the live bottom.
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

	step := func(k tea.KeyType) {
		next, _ := m.handleZoomKey(tea.KeyMsg{Type: k})
		m = next.(model)
	}

	step(tea.KeyShiftUp)
	if m.scrollOff != 1 {
		t.Fatalf("shift+up should scroll one line, off = %d", m.scrollOff)
	}
	page := m.scrollOff
	step(tea.KeyPgUp)
	if m.scrollOff <= page+1 {
		t.Fatalf("pgup should scroll a page, off = %d", m.scrollOff)
	}
	step(tea.KeyShiftDown)
	// A printable key returns to the live bottom and drives the program.
	next, _ := m.handleZoomKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = next.(model)
	if m.scrollOff != 0 {
		t.Fatalf("a program key should snap back to the bottom, off = %d", m.scrollOff)
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

	next, _ := m.handleGroupZoomKey(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(model)
	if m.scrollOff <= 0 {
		t.Fatalf("pgup should scroll the focused tile, off = %d", m.scrollOff)
	}

	// Moving the focus resets the scrollback to the new tile's live bottom.
	next, _ = m.handleGroupZoomKey(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.scrollOff != 0 {
		t.Fatalf("changing focus should reset scrollback, off = %d", m.scrollOff)
	}
}
