package tui

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// TestFeedKey drives keys through a real emulator and reads the bytes it queues
// on its input side, which is exactly what the zoom reader forwards to the PTY.
// This proves the encoding is mode-aware: the arrows switch to application-cursor
// (DECCKM) sequences once the program enables that mode.
func TestFeedKey(t *testing.T) {
	read := func(emu *vt.SafeEmulator) string {
		buf := make([]byte, 64)
		n, _ := emu.Read(buf)
		return string(buf[:n])
	}

	cases := []struct {
		k    tea.KeyMsg
		want string
	}{
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}, "a"},
		{tea.KeyMsg{Type: tea.KeySpace}, " "},
		{tea.KeyMsg{Type: tea.KeyEnter}, "\r"},
		{tea.KeyMsg{Type: tea.KeyTab}, "\t"},
		{tea.KeyMsg{Type: tea.KeyEsc}, "\x1b"},
		{tea.KeyMsg{Type: tea.KeyBackspace}, "\x7f"},
		{tea.KeyMsg{Type: tea.KeyCtrlC}, "\x03"},
		{tea.KeyMsg{Type: tea.KeyUp}, "\x1b[A"}, // normal cursor-key mode
		{tea.KeyMsg{Type: tea.KeyDelete}, "\x1b[3~"},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x"), Alt: true}, "\x1bx"},
	}
	for _, c := range cases {
		emu := vt.NewSafeEmulator(20, 5)
		// SendKey blocks until the reader drains the pipe, so read concurrently.
		got := make(chan string, 1)
		go func() { got <- read(emu) }()
		feedKey(emu, c.k)
		if g := <-got; g != c.want {
			t.Errorf("feedKey(%v) = %q, want %q", c.k, g, c.want)
		}
	}

	// With application-cursor-key mode on (DECCKM, the byte htop sends), the same
	// up arrow must encode as ESC O A rather than ESC [ A.
	emu := vt.NewSafeEmulator(20, 5)
	_, _ = emu.Write([]byte("\x1b[?1h")) // DECCKM set
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := emu.Read(buf)
		got <- string(buf[:n])
	}()
	feedKey(emu, tea.KeyMsg{Type: tea.KeyUp})
	if g := <-got; g != "\x1bOA" {
		t.Errorf("up arrow in DECCKM = %q, want %q", g, "\x1bOA")
	}
}

func TestOverlayCursor(t *testing.T) {
	cases := []struct {
		line string
		col  int
		want string
	}{
		{"abc", 1, "a\x1b[7mb\x1b[27mc"},                             // mid-line
		{"ab", 4, "ab  \x1b[7m \x1b[27m"},                            // past the end → padded
		{"\x1b[31mX\x1b[0mY", 1, "\x1b[31mX\x1b[0m\x1b[7mY\x1b[27m"}, // escapes don't count as columns
		{"中b", 0, "\x1b[7m中\x1b[27mb"},                               // cursor on a wide (2-cell) glyph
		{"中b", 2, "中\x1b[7mb\x1b[27m"},                               // lands after the wide glyph, not drifted left
		{"中b", 1, "\x1b[7m中\x1b[27mb"},                               // either cell of the wide glyph highlights it
	}
	for _, c := range cases {
		if got := overlayCursor(c.line, c.col); got != c.want {
			t.Errorf("overlayCursor(%q, %d) = %q, want %q", c.line, c.col, got, c.want)
		}
	}
}

// zoomServer spins up a server, dials a client, and spawns one shell panel.
func zoomServer(t *testing.T) (*client.Client, string) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "z.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	<-c.Events // welcome
	<-c.Events // empty panels
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	return c, (<-c.Events).Panels[0].ID
}

func TestZoomEmulatesShell(t *testing.T) {
	c, id := zoomServer(t)

	m := model{client: c, width: 80, height: 24, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m = m.zoomInto(panel.Panel{ID: id, Title: "sh #" + id})
	if m.mode != modeZoom || m.emu == nil {
		t.Fatalf("zoomInto should enter modeZoom with an emulator, mode=%v emu=%v", m.mode, m.emu)
	}

	// Type a command through the zoom key path.
	for _, r := range "echo zoomemu" {
		next, _ := m.handleZoomKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(string(r))})
		m = next.(model)
	}
	next, _ := m.handleZoomKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	// Pump the shell's output into the emulator until it renders the result.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-c.Output:
			if msg.ID == id {
				_, _ = m.emu.Write(msg.Data)
			}
			if strings.Contains(m.emu.Render(), "zoomemu") {
				goto detach
			}
		case <-deadline:
			t.Fatalf("emulator never rendered the command output:\n%s", m.emu.Render())
		}
	}

detach:
	// The view renders the screen plus a footer.
	if v := m.View(); !strings.Contains(v, "ZOOM") {
		t.Fatal("zoom view should include the footer")
	}

	next, _ = m.zoomDetach()
	m = next.(model)
	if m.mode != modeDashboard || m.emu != nil {
		t.Fatalf("detach should return to the dashboard, mode=%v emu=%v", m.mode, m.emu)
	}
}

func TestZoomDetachKey(t *testing.T) {
	c, id := zoomServer(t)
	m := model{client: c, width: 80, height: 24, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m = m.zoomInto(panel.Panel{ID: id, Title: "sh"})

	// prefix arms; the dashboard key then detaches.
	next, _ := m.handleZoomKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = next.(model)
	if !m.zoomArmed {
		t.Fatal("prefix should arm inside a zoom")
	}
	next, _ = m.handleZoomKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = next.(model)
	if m.mode != modeDashboard {
		t.Fatalf("prefix+d should detach, mode=%v", m.mode)
	}
}

// TestZoomTracksCursorVisibility checks the DECTCEM callback wired up by zoomInto
// flips the model's cursor-hidden flag as the program shows and hides its cursor.
func TestZoomTracksCursorVisibility(t *testing.T) {
	m := model{width: 20, height: 6, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	m = m.zoomInto(panel.Panel{ID: "1", Title: "x"})
	if m.emu == nil {
		t.Fatal("zoomInto should create an emulator")
	}
	if m.cursorHiddenNow() {
		t.Fatal("the cursor should start visible")
	}
	_, _ = m.emu.Write([]byte("\x1b[?25l")) // program hides the cursor
	if !m.cursorHiddenNow() {
		t.Fatal("DECTCEM hide should flip the cursor-hidden flag")
	}
	_, _ = m.emu.Write([]byte("\x1b[?25h")) // program shows it again
	if m.cursorHiddenNow() {
		t.Fatal("DECTCEM show should clear the cursor-hidden flag")
	}
}

// TestZoomViewHidesCursor proves the zoom view draws the cursor only while the
// program keeps it visible.
func TestZoomViewHidesCursor(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 5)
	hidden := false
	m := model{emu: emu, mode: modeZoom, width: 20, height: 6, cursorHidden: &hidden}
	if !strings.Contains(m.zoomView(), "\x1b[7m") {
		t.Fatal("a visible cursor should be drawn")
	}
	hidden = true
	if strings.Contains(m.zoomView(), "\x1b[7m") {
		t.Fatal("a hidden cursor should not be drawn")
	}
}

// TestZoomBareKeysGoToProgram proves no bare key is stolen for scrollback — PgUp
// and shift+up both reach the program (a BBS like ptt.cc or vim pages itself) —
// while the leader (C-t b) drives baton's own scrollback.
func TestZoomBareKeysGoToProgram(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 5)
	fillLines(emu, 30) // scrollback exists
	// Drain the input side so a passed-through key never blocks.
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := emu.Read(buf); err != nil {
				return
			}
		}
	}()
	m := model{emu: emu, mode: modeZoom, zoomID: "1", width: 20, height: 6,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	// Bare PgUp and bare shift+up both belong to the program — baton never scrolls
	// on them (the Mac terminal that collapses shift+up to a plain up is harmless),
	// and stays out of scroll mode.
	for _, k := range []tea.KeyType{tea.KeyPgUp, tea.KeyShiftUp} {
		n, _ := m.handleZoomKey(tea.KeyMsg{Type: k})
		m = n.(model)
		if m.scrollOff != 0 || m.scrolling {
			t.Fatalf("%v should pass through to the program, not scroll baton", k)
		}
	}

	// Scrollback is reached only through the leader's scroll mode: C-t [.
	n, _ := m.handleZoomKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = n.(model)
	n, _ = m.handleZoomKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	m = n.(model)
	if !m.scrolling {
		t.Fatal("C-t [ should enter scroll mode")
	}
}
