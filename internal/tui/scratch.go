package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
)

// The floating scratch pane: a transient shell (or any command) the cockpit floats
// over whatever view is active — tmux's display-popup, for a quick command without
// leaving the fleet. C-t ~ toggles it in every view. It is a server-side ephemeral
// PTY (panel.scratch), so it never joins the fleet, the snapshot, or the persisted
// state, and it is reaped when closed or when the cockpit disconnects. Hiding keeps
// it alive for reuse; C-t w inside it closes it for good.

const (
	scratchMinW = 24 // smallest the floating box tracks the window down to
	scratchMinH = 6

	scratchDefWFrac = 0.8 // default box size as a fraction of the terminal
	scratchDefHFrac = 0.6
)

// toggleScratch opens the scratch pane, or hides it when already shown. The first
// open asks the server for an ephemeral shell (the "scratch" reply then attaches
// and floats it); later opens reuse the live PTY, so its scrollback and running
// program persist across hide/show.
func (m model) toggleScratch() (tea.Model, tea.Cmd) {
	if m.scratchOpen {
		return m.hideScratch(), nil
	}
	if m.scratchID != "" { // a hidden-but-live pane: just show it again
		return m.showScratch(), nil
	}
	m.sendf(proto.Command{Action: "panel.scratch", Path: m.scratchCommand(), Dir: m.workdir})
	m.status = "opening scratch…"
	return m, nil
}

// scratchCommand is the program the pane runs: the configured scratch command, else
// the cockpit's default shell (empty lets the server pick the login shell).
func (m model) scratchCommand() string {
	if c := m.tuiCfg.Scratch.Command; c != "" {
		return c
	}
	return m.shellPath
}

// showScratch floats the pane and gives it the keyboard, refitting it to the
// current window first.
func (m model) showScratch() model {
	m.scratchOpen = true
	m.scratchArmed = false
	m.resizeScratch()
	m.status = fmt.Sprintf("scratch · %s %s hides · %s %s closes",
		keyLabel(m.effPrefix()), keyLabel(keyScratch),
		keyLabel(m.effPrefix()), keyLabel(m.bindingKey(actClose)))
	return m
}

// hideScratch tucks the pane away without killing it: the PTY keeps running (its
// output still streams into the emulator), so re-opening resumes where it left off.
func (m model) hideScratch() model {
	m.scratchOpen = false
	m.scratchArmed = false
	m.status = "scratch hidden · " + keyLabel(m.effPrefix()) + " " + keyLabel(keyScratch) + " reopens"
	return m
}

// killScratch closes the pane for good: it reaps the ephemeral PTY server-side and
// tears down the emulator, so the next open spawns a fresh shell.
func (m model) killScratch() model {
	if m.scratchID != "" {
		m.sendf(proto.Command{Action: "panel.close", ID: m.scratchID})
		closeZoom(m.scratchEmu)
	}
	m.scratchID = ""
	m.scratchEmu = nil
	m.scratchOpen = false
	m.scratchArmed = false
	m.status = "scratch closed"
	return m
}

// handleScratchKey drives the floating pane while it is shown: every bare key is fed
// to its shell, and the prefix is the only escape — C-t ~ hides it, C-t w closes it,
// C-t q detaches the cockpit, C-t C-t sends a literal prefix. This mirrors interact
// mode, but on a pane floating over the current view rather than a tile of the split.
func (m model) handleScratchKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.scratchArmed {
		m.scratchArmed = false
		switch {
		case key == m.effPrefix():
			m.feedScratch(k) // prefix+prefix → a literal prefix to the shell
		case key == keyScratch:
			return m.hideScratch(), nil
		case key == m.bindingKey(actClose): // C-t w closes the scratch for good
			return m.killScratch(), nil
		case key == m.bindingKey(actDetach): // C-t q detaches the whole cockpit
			return m.runAction(actDetach)
		}
		return m, nil
	}
	if key == m.effPrefix() {
		m.scratchArmed = true
		return m, nil
	}
	m.feedScratch(k)
	return m, nil
}

// feedScratch routes a keystroke to the pane's emulator, whose reader forwards the
// bytes to the shell's PTY. A no-op before the pane has spawned.
func (m model) feedScratch(k tea.KeyMsg) {
	if m.scratchEmu != nil {
		feedKey(m.scratchEmu, k)
	}
}

// scratchFrac is the box size as a fraction of the terminal — the configured values
// when sane (0 < f ≤ 1), else the built-in defaults.
func (m model) scratchFrac() (w, h float64) {
	w, h = scratchDefWFrac, scratchDefHFrac
	if v := m.tuiCfg.Scratch.Width; v > 0 && v <= 1 {
		w = v
	}
	if v := m.tuiCfg.Scratch.Height; v > 0 && v <= 1 {
		h = v
	}
	return w, h
}

// scratchRect is the floating box's outer placement: centered over the view, sized
// to the configured fraction of the terminal (net of the footer row), floored at a
// legible minimum and capped at the screen.
func (m model) scratchRect() (x, y, w, h int) {
	fw, fh := m.scratchFrac()
	avail := m.height - 1 // leave the footer row uncovered
	w = min(max(int(float64(m.width)*fw), scratchMinW), m.width)
	h = min(max(int(float64(avail)*fh), scratchMinH), avail)
	x = (m.width - w) / 2
	y = (avail - h) / 2
	return x, y, w, h
}

// scratchEmuSize is the pane's inner emulator size, derived from the outer box like
// a tile: the border (2) and padding (2) trim the width, the border (2) and the head
// line (1) the height. Floored at 1 so a tiny window never yields a zero emulator.
func (m model) scratchEmuSize() (cols, rows int) {
	_, _, w, h := m.scratchRect()
	return max(1, w-4), max(1, h-3)
}

// resizeScratch refits the emulator and the PTY to the current window, so the
// floating pane reflows when the terminal changes size while it is open.
func (m *model) resizeScratch() {
	if m.scratchEmu == nil || m.scratchID == "" {
		return
	}
	cols, rows := m.scratchEmuSize()
	m.scratchEmu.Resize(cols, rows)
	m.sendf(proto.Command{Action: "panel.resize", ID: m.scratchID, Rows: rows, Cols: cols})
}

// overlayScratch composites the floating pane over an already-rendered frame — the
// last step of View when the pane is shown.
func (m model) overlayScratch(frame string) string {
	x, y, _, _ := m.scratchRect()
	return overlayBox(frame, m.scratchView(), x, y)
}

// scratchView renders the floating box: a titled, brand-bordered pane wrapping the
// shell's live screen, with the cursor drawn where typing lands.
func (m model) scratchView() string {
	cols, rows := m.scratchEmuSize()
	badge := lipgloss.NewStyle().Foreground(colDark).Background(colBrand).Bold(true).Render(" ~ ")
	title := lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render("scratch")
	head := badge + " " + title
	return paneBox(cols, 0, colBrand).Render(lipgloss.JoinVertical(lipgloss.Left, head,
		lipgloss.JoinVertical(lipgloss.Left, m.scratchBody(cols, rows)...)))
}

// scratchBody is the pane's content rows, always exactly rows tall: the shell's live
// screen with a reverse-video cursor at the emulator's cursor cell, so you can see
// where typing lands. A blank block before the shell's first output (and in a test
// without a client).
func (m model) scratchBody(cols, rows int) []string {
	if m.scratchEmu == nil {
		return make([]string, rows)
	}
	out := emuWindow(m.scratchEmu, cols, rows, 0)
	cur := m.scratchEmu.CursorPosition()
	if cur.Y >= 0 && cur.Y < len(out) {
		out[cur.Y] = overlayCursor(out[cur.Y], cur.X)
	}
	return out
}
