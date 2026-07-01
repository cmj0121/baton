package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/config"
)

// TestScratchToggleDefersOpen: the first toggle (no live pane) asks the server to
// spawn the shell and waits — it does not show the box until the "scratch" reply
// lands, so the overlay never renders against a nil emulator.
func TestScratchToggleDefersOpen(t *testing.T) {
	m := baseModel()
	tm, _ := m.toggleScratch()
	m = tm.(model)
	if m.scratchOpen {
		t.Error("scratch must not open until the server returns its id")
	}
	if !strings.Contains(m.status, "opening") {
		t.Errorf("status = %q, want an opening notice", m.status)
	}
}

// TestScratchShowHideKill: show floats the pane, hide keeps the PTY alive for reuse,
// and kill tears everything down.
func TestScratchShowHideKill(t *testing.T) {
	m := baseModel()
	m.scratchID = "scratch:1"
	m.scratchEmu = vt.NewSafeEmulator(10, 4)

	m = m.showScratch()
	if !m.scratchOpen {
		t.Fatal("showScratch should float the pane")
	}
	m = m.hideScratch()
	if m.scratchOpen {
		t.Fatal("hideScratch should hide the pane")
	}
	if m.scratchID == "" || m.scratchEmu == nil {
		t.Fatal("hide must keep the PTY and emulator alive for reuse")
	}
	m = m.killScratch()
	if m.scratchOpen || m.scratchID != "" || m.scratchEmu != nil {
		t.Fatalf("kill should tear down: open=%v id=%q emu=%v", m.scratchOpen, m.scratchID, m.scratchEmu)
	}
}

// TestScratchKeyToggleHides: the prefix then the toggle key hides the pane from
// inside it, rather than feeding the shell.
func TestScratchKeyToggleHides(t *testing.T) {
	m := baseModel()
	m.scratchOpen = true
	m.scratchArmed = true // prefix already pressed inside the pane
	tm, _ := m.handleScratchKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(keyScratch)})
	if tm.(model).scratchOpen {
		t.Error("prefix + the toggle key should hide the scratch pane")
	}
}

// TestScratchRectCentersAndFloors: the box centers in the terminal, respects the
// configured fraction, and never shrinks below the legible minimum.
func TestScratchRectCentersAndFloors(t *testing.T) {
	m := baseModel() // 120x40
	x, y, w, h := m.scratchRect()
	if w != int(120*scratchDefWFrac) {
		t.Errorf("default width = %d, want %d", w, int(120*scratchDefWFrac))
	}
	if x != (120-w)/2 || y != (39-h)/2 {
		t.Errorf("box not centered: x=%d y=%d for w=%d h=%d", x, y, w, h)
	}

	// A tiny terminal floors the box at the minimum rather than going to zero.
	small := baseModel()
	small.width, small.height = 20, 8
	_, _, sw, sh := small.scratchRect()
	if sw < scratchMinW-1 && sw != small.width {
		t.Errorf("width should floor near %d or cap at the screen: got %d", scratchMinW, sw)
	}
	if sh < 1 {
		t.Errorf("height should stay positive: %d", sh)
	}
}

// TestOverlayScratchKeepsFrameShape: floating the pane over a frame leaves its line
// count intact and stamps the titled box into it.
func TestOverlayScratchKeepsFrameShape(t *testing.T) {
	m := baseModel()
	c, r := m.scratchEmuSize()
	m.scratchEmu = vt.NewSafeEmulator(c, r)
	m.scratchOpen = true

	rows := make([]string, m.height-1)
	for i := range rows {
		rows[i] = strings.Repeat(".", m.width)
	}
	out := m.overlayScratch(strings.Join(rows, "\n"))
	if n := len(strings.Split(out, "\n")); n != m.height-1 {
		t.Fatalf("overlay changed the frame line count: %d, want %d", n, m.height-1)
	}
	if !strings.Contains(out, "scratch") {
		t.Error("the floated frame should carry the scratch title")
	}
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w != m.width {
			t.Fatalf("a row's visible width drifted to %d, want %d", w, m.width)
		}
	}
}

// TestScratchCommandPrefersConfig: the pane runs the configured command when set,
// else falls back to the cockpit's default shell.
func TestScratchCommandPrefersConfig(t *testing.T) {
	m := baseModel()
	m.shellPath = "/bin/zsh"
	if got := m.scratchCommand(); got != "/bin/zsh" {
		t.Errorf("default = %q, want the shell", got)
	}
	m.tuiCfg = config.TUIConfig{Scratch: config.ScratchConfig{Command: "btop"}}
	if got := m.scratchCommand(); got != "btop" {
		t.Errorf("configured = %q, want btop", got)
	}
}
