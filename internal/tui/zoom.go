package tui

import (
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	vt "github.com/charmbracelet/x/vt"
	"github.com/mattn/go-runewidth"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
)

// writeEmu feeds program output into the emulator, isolating the cockpit from a
// panic in the terminal parser: a single misbehaving program (or a parser edge
// case) degrades its own panel rather than taking down the whole TUI. The stack
// is logged so the underlying fault can be pinned down.
func writeEmu(emu *vt.SafeEmulator, data []byte) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Bytes("stack", debug.Stack()).Msg("recovered an emulator write panic")
		}
	}()
	_, _ = emu.Write(data)
}

// cellCond gives wcwidth-style cell widths — 2 for wide (CJK) glyphs, 1 for the
// rest — independent of the user's locale, matching the emulator's own cell math.
var cellCond = &runewidth.Condition{}

// cellWidth is how many terminal cells a rune occupies (0 for a combining mark).
func cellWidth(r rune) int {
	if w := cellCond.RuneWidth(r); w >= 0 {
		return w
	}
	return 0
}

// zoomFooter builds the coloured strip below the emulated panel: a brand cap, a
// state cap (green live ZOOM, or grey EXITED for a finished program), a scrollback
// marker when the view is scrolled off the live bottom, the panel title, the C-t ?
// help hint, and — like every view — the host stats, clock, and connection status.
func (m model) zoomFooter() string {
	if m.input == inputSearch { // typing a find term: show it in the footer, screen stays visible
		return m.searchPromptFooter()
	}
	state := seg("◉ ZOOM", colInk, colGreen)
	switch {
	case m.searchActive():
		state = m.searchSeg()
	case m.scrolling:
		state = seg("↕ SCROLL", colDark, colCyan)
	case m.zoomExited:
		state = seg("◼ EXITED", colDark, colMuted)
	}
	left := seg("◈ BATON", colDark, colBrand) + state + scrollSeg(m.scrollOff) + barBold.Render(" "+m.zoomTitle+" ")
	return m.statusBar(left, m.helpHint())
}

// scrollSeg is the footer marker shown while a view is scrolled back through the
// scrollback buffer — how many lines above the live bottom the window sits. Empty
// at the bottom, so the live view carries no marker.
func scrollSeg(off int) string {
	if off <= 0 {
		return ""
	}
	return seg(fmt.Sprintf("⮝ %d", off), colDark, colCyan)
}

// scrollEmu moves the scrollback viewport by delta lines for the given emulator:
// positive scrolls toward older output, negative back toward the live bottom. It
// clamps to the live bottom (0) and to the buffer's depth, so holding the key at
// either end simply rests there.
func (m *model) scrollEmu(emu *vt.SafeEmulator, delta int) {
	if emu == nil {
		return
	}
	off := m.scrollOff + delta
	if off < 0 {
		off = 0
	}
	if depth := emu.ScrollbackLen(); off > depth {
		off = depth
	}
	m.scrollOff = off
}

// emuWindow renders a rows-tall window of an emulator's content, scrolled off
// lines up from the live bottom. The lines that have rolled off the top live in
// the emulator's scrollback buffer; this stitches the needed slice of them above
// the current screen so scrolling reveals earlier output. Each line is clipped to
// cols, so a line captured while the panel was wider cannot spill past its tile.
// off is clamped to the buffer depth, and only the visible scrollback lines are
// rendered, so a deep buffer costs nothing while sitting at the bottom.
func emuWindow(emu *vt.SafeEmulator, cols, rows, off int) []string {
	out := make([]string, rows)
	if emu == nil || rows < 1 {
		return out
	}
	screen := strings.Split(emu.Render(), "\n")
	sbLen := 0
	var sb *vt.Scrollback
	if off > 0 {
		sb = emu.Scrollback()
		sbLen = sb.Len()
		if off > sbLen {
			off = sbLen
		}
	}
	start := sbLen - off // top of the window, in the combined scrollback+screen space
	for i := range out {
		idx := start + i
		switch {
		case idx < 0:
			// above the oldest line: leave blank
		case idx < sbLen:
			if ln := sb.Line(idx); ln != nil {
				out[i] = clipVisible(ln.Render(), cols)
			}
		default:
			if si := idx - sbLen; si >= 0 && si < len(screen) {
				out[i] = clipVisible(screen[si], cols)
			}
		}
	}
	return out
}

// clipVisible returns the prefix of s holding at most width visible columns,
// copying escape sequences verbatim since they cost no columns. A scrollback line
// captured at a wider size is thus trimmed to fit, and a trailing reset is added
// when the clip lands mid-styling so a colour cannot bleed past the cut.
func clipVisible(s string, width int) string {
	if width < 1 {
		return ""
	}
	var out strings.Builder
	vis, clipped := 0, false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			n := escLen(s[i:])
			out.WriteString(s[i : i+n])
			i += n
			continue
		}
		if vis >= width {
			clipped = true
			break
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		out.WriteString(s[i : i+size])
		vis++
		i += size
	}
	if clipped {
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

// overlayCursor inserts a reverse-video cell at visible column col of an
// ANSI-styled line. Escape sequences are copied verbatim and don't count as
// columns, and columns are measured in display cells — so a wide (CJK) glyph
// advances two — keeping the cursor aligned on a line of full-width text (a
// Chinese BBS like ptt.cc) rather than drifting left of where it belongs. If the
// cursor sits past the line's content the line is space-padded.
func overlayCursor(line string, col int) string {
	if col < 0 {
		return line
	}
	var out strings.Builder
	vis := 0
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			n := escLen(line[i:])
			out.WriteString(line[i : i+n])
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		w := cellWidth(r)
		// The cursor lands on the glyph whose cell span covers col, so either cell
		// of a wide glyph highlights the whole glyph.
		if w > 0 && vis <= col && col < vis+w {
			out.WriteString("\x1b[7m" + line[i:i+size] + "\x1b[27m")
		} else {
			out.WriteString(line[i : i+size])
		}
		vis += w
		i += size
	}
	if vis <= col {
		out.WriteString(strings.Repeat(" ", col-vis) + "\x1b[7m \x1b[27m")
	}
	return out.String()
}

// escLen returns the byte length of the escape sequence at the start of s.
func escLen(s string) int {
	if len(s) < 2 || s[0] != 0x1b {
		return 1
	}
	switch s[1] {
	case '[': // CSI: ESC [ … final byte (0x40–0x7e)
		i := 2
		for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
			i++
		}
		if i < len(s) {
			i++
		}
		return i
	case ']': // OSC: ESC ] … BEL or ST (ESC \)
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	default:
		return 2 // two-byte escape, e.g. ESC 7
	}
}

// zoomReader bridges the emulator's input side to the panel's PTY. The emulator
// encodes keystrokes (queued by feedKey) and the replies a program generates for
// terminal queries onto an internal pipe; this drains that pipe and forwards the
// bytes to the server. It runs until the emulator is closed on detach, when Read
// returns io.EOF. Routing keys through the emulator (rather than encoding them
// ourselves) is what makes the arrows honour the program's DECCKM mode, and
// draining the pipe is what keeps a query-happy program (htop) from blocking.
func zoomReader(emu *vt.SafeEmulator, c *client.Client, id string) {
	buf := make([]byte, 4096)
	for {
		n, err := emu.Read(buf)
		if n > 0 && c != nil {
			_ = c.Send(proto.Command{Action: "panel.input", ID: id, Data: append([]byte(nil), buf[:n]...)})
		}
		if err != nil {
			return
		}
	}
}

// closeZoom stops the zoom reader by closing the emulator's input pipe, which
// makes the goroutine's blocked Read return EOF. We close the pipe writer
// directly rather than calling emu.Close(): Close mutates an unsynchronised
// "closed" flag that the still-running Read also reads, which the race detector
// flags. Closing the pipe is the real, memory-safe unblock.
func closeZoom(emu *vt.SafeEmulator) {
	if emu == nil {
		return
	}
	if pw, ok := emu.InputPipe().(*io.PipeWriter); ok {
		_ = pw.Close()
	}
}

// feedKey encodes a bubbletea key event into the zoomed emulator. Printable runes
// (and pastes) go through as text; everything else is sent as a key event so the
// emulator emits the mode-correct bytes — notably application-cursor-key (DECCKM)
// sequences for the arrows when a full-screen program asks for them. Alt prefixes
// the Meta modifier (an ESC lead-in).
func feedKey(emu *vt.SafeEmulator, k tea.KeyMsg) {
	if k.Type == tea.KeyRunes {
		if k.Alt {
			for _, r := range k.Runes {
				emu.SendKey(vt.KeyPressEvent{Code: r, Mod: vt.ModAlt})
			}
			return
		}
		emu.SendText(string(k.Runes))
		return
	}
	ev, ok := keyEvent(k)
	if !ok {
		return
	}
	if k.Alt {
		ev.Mod |= vt.ModAlt
	}
	emu.SendKey(ev)
}

// specialKey maps bubbletea's named (negative) key types to the ultraviolet key
// code the emulator understands, so it can encode them in the program's mode.
var specialKey = map[tea.KeyType]rune{
	tea.KeyUp:     vt.KeyUp,
	tea.KeyDown:   vt.KeyDown,
	tea.KeyRight:  vt.KeyRight,
	tea.KeyLeft:   vt.KeyLeft,
	tea.KeyHome:   vt.KeyHome,
	tea.KeyEnd:    vt.KeyEnd,
	tea.KeyPgUp:   vt.KeyPgUp,
	tea.KeyPgDown: vt.KeyPgDown,
	tea.KeyInsert: vt.KeyInsert,
	tea.KeyDelete: vt.KeyDelete,
	tea.KeyF1:     vt.KeyF1,
	tea.KeyF2:     vt.KeyF2,
	tea.KeyF3:     vt.KeyF3,
	tea.KeyF4:     vt.KeyF4,
	tea.KeyF5:     vt.KeyF5,
	tea.KeyF6:     vt.KeyF6,
	tea.KeyF7:     vt.KeyF7,
	tea.KeyF8:     vt.KeyF8,
	tea.KeyF9:     vt.KeyF9,
	tea.KeyF10:    vt.KeyF10,
	tea.KeyF11:    vt.KeyF11,
	tea.KeyF12:    vt.KeyF12,
}

// keyEvent converts a bubbletea key event into an ultraviolet key-press event,
// reporting false for keys with no emulator equivalent. Enter, tab, esc, and
// backspace share ASCII codes with control keys, so they are matched by name
// first; the remaining 1..26 range encodes Ctrl-A..Ctrl-Z.
func keyEvent(k tea.KeyMsg) (vt.KeyPressEvent, bool) {
	switch k.Type {
	case tea.KeySpace:
		return vt.KeyPressEvent{Code: vt.KeySpace}, true
	case tea.KeyEnter:
		return vt.KeyPressEvent{Code: vt.KeyEnter}, true
	case tea.KeyTab:
		return vt.KeyPressEvent{Code: vt.KeyTab}, true
	case tea.KeyEsc:
		return vt.KeyPressEvent{Code: vt.KeyEscape}, true
	case tea.KeyBackspace:
		return vt.KeyPressEvent{Code: vt.KeyBackspace}, true
	case tea.KeyShiftTab:
		return vt.KeyPressEvent{Code: vt.KeyTab, Mod: vt.ModShift}, true
	}
	if r, ok := specialKey[k.Type]; ok {
		return vt.KeyPressEvent{Code: r}, true
	}
	if k.Type >= tea.KeyCtrlA && k.Type <= tea.KeyCtrlZ {
		return vt.KeyPressEvent{Code: 'a' + rune(k.Type-tea.KeyCtrlA), Mod: vt.ModCtrl}, true
	}
	return vt.KeyPressEvent{}, false
}
