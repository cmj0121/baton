package tui

import (
	"testing"

	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/proto"
)

// liveScratch builds a model with a shown, spawned scratch pane.
func liveScratch() model {
	m := baseModel()
	m.scratchID = "scratch:1"
	m.scratchEmu = vt.NewSafeEmulator(10, 4)
	m.scratchOpen = true
	return m
}

// fedScratch is a live pane whose emulator input is drained the way the cockpit's
// zoomReader drains it, so feeding a keystroke does not block on the pane's
// unbuffered input pipe. The drain is torn down when the test ends.
func fedScratch(t *testing.T) model {
	t.Helper()
	m := liveScratch()
	emu := m.scratchEmu
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := emu.Read(buf); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { closeZoom(emu) })
	return m
}

// TestToggleScratchHidesWhenOpen: toggling an open pane hides it.
func TestToggleScratchHidesWhenOpen(t *testing.T) {
	m := liveScratch()
	tm, _ := m.toggleScratch()
	m = tm.(model)
	if m.scratchOpen {
		t.Fatal("toggling an open pane should hide it")
	}
}

// TestToggleScratchReopensLive: a hidden-but-live pane shows again rather than
// spawning a fresh shell.
func TestToggleScratchReopensLive(t *testing.T) {
	m := liveScratch()
	m.scratchOpen = false // hidden but the PTY is still alive
	tm, _ := m.toggleScratch()
	m = tm.(model)
	if !m.scratchOpen {
		t.Fatal("toggling a hidden-but-live pane should show it again")
	}
}

// TestScratchKeyArmsPrefix: a bare prefix inside the pane arms it rather than
// feeding the shell.
func TestScratchKeyArmsPrefix(t *testing.T) {
	m := liveScratch()
	tm, _ := m.handleScratchKey(key(m.effPrefix()))
	m = tm.(model)
	if !m.scratchArmed {
		t.Fatal("a bare prefix should arm the pane")
	}
}

// TestScratchKeyFeedsShell: an ordinary key with no armed prefix is fed to the
// shell and the pane stays shown.
func TestScratchKeyFeedsShell(t *testing.T) {
	m := fedScratch(t)
	tm, _ := m.handleScratchKey(key("a"))
	m = tm.(model)
	if !m.scratchOpen || m.scratchArmed {
		t.Fatalf("an ordinary key should feed the shell and keep the pane shown, open=%v armed=%v", m.scratchOpen, m.scratchArmed)
	}
}

// TestScratchKeyLiteralPrefix: prefix+prefix feeds a literal prefix and disarms.
func TestScratchKeyLiteralPrefix(t *testing.T) {
	m := fedScratch(t)
	m.scratchArmed = true
	tm, _ := m.handleScratchKey(key(m.effPrefix()))
	m = tm.(model)
	if m.scratchArmed {
		t.Fatal("prefix + prefix should disarm after feeding a literal prefix")
	}
	if !m.scratchOpen {
		t.Fatal("a literal prefix should keep the pane shown")
	}
}

// TestScratchKeyClose: prefix + the close key reaps the pane for good.
func TestScratchKeyClose(t *testing.T) {
	m := liveScratch()
	m.scratchArmed = true
	tm, _ := m.handleScratchKey(key(m.bindingKey(actClose)))
	m = tm.(model)
	if m.scratchID != "" || m.scratchEmu != nil || m.scratchOpen {
		t.Fatalf("prefix + close should tear the pane down, got id=%q emu=%v open=%v", m.scratchID, m.scratchEmu, m.scratchOpen)
	}
}

// TestScratchKeyDetach: prefix + the detach key quits the cockpit.
func TestScratchKeyDetach(t *testing.T) {
	m := liveScratch()
	m.scratchArmed = true
	tm, cmd := m.handleScratchKey(key(m.bindingKey(actDetach)))
	m = tm.(model)
	if !m.quitting {
		t.Fatal("prefix + detach should mark the model quitting")
	}
	if cmd == nil {
		t.Fatal("detach should emit a quit command")
	}
}

// TestScratchKeyArmedStray: an armed key that is none of the escapes is swallowed
// and disarms.
func TestScratchKeyArmedStray(t *testing.T) {
	m := liveScratch()
	m.scratchArmed = true
	tm, _ := m.handleScratchKey(key("z"))
	m = tm.(model)
	if m.scratchArmed {
		t.Fatal("an armed stray key should disarm the pane")
	}
	if !m.scratchOpen {
		t.Fatal("an armed stray key should not close the pane")
	}
}

// TestFeedScratchNoEmu: feeding before the pane spawns is a safe no-op.
func TestFeedScratchNoEmu(t *testing.T) {
	m := baseModel() // no scratchEmu
	m.feedScratch(key("a"))
}

// TestScratchFracHonoursConfig: configured, in-range fractions win over the
// defaults; out-of-range values fall back.
func TestScratchFracHonoursConfig(t *testing.T) {
	m := baseModel()
	m.tuiCfg = config.TUIConfig{Scratch: config.ScratchConfig{Width: 0.5, Height: 0.4}}
	w, h := m.scratchFrac()
	if w != 0.5 || h != 0.4 {
		t.Fatalf("configured fractions should win, got w=%v h=%v", w, h)
	}

	m.tuiCfg = config.TUIConfig{Scratch: config.ScratchConfig{Width: 2, Height: -1}}
	w, h = m.scratchFrac()
	if w != scratchDefWFrac || h != scratchDefHFrac {
		t.Fatalf("out-of-range fractions should fall back to defaults, got w=%v h=%v", w, h)
	}
}

// TestResizeScratchNoop: resizing before the pane spawns does nothing.
func TestResizeScratchNoop(t *testing.T) {
	m := baseModel() // no emu / id
	m.resizeScratch()
	if m.scratchEmu != nil {
		t.Fatal("resize on an unspawned pane should stay a no-op")
	}
}

// TestResizeScratchResizesPane: with a live pane, resize refits the emulator and
// asks the server to match.
func TestResizeScratchResizesPane(t *testing.T) {
	c, cmds := recordingServer(t)
	m := liveScratch()
	m.client = c
	m.resizeScratch()
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.resize" })
	if got.ID != "scratch:1" {
		t.Fatalf("resize should target the scratch pane, got %+v", got)
	}
}

// TestScratchBodyDrawsCursor: with a spawned emulator the body reflects the shell
// screen and stamps a cursor, and is always exactly rows tall.
func TestScratchBodyDrawsCursor(t *testing.T) {
	m := liveScratch()
	cols, rows := m.scratchEmuSize()
	body := m.scratchBody(cols, rows)
	if len(body) != rows {
		t.Fatalf("scratchBody should be exactly %d rows, got %d", rows, len(body))
	}
}

// TestScratchBodyNoEmu: without an emulator the body is a blank block.
func TestScratchBodyNoEmu(t *testing.T) {
	m := baseModel()
	body := m.scratchBody(10, 4)
	if len(body) != 4 {
		t.Fatalf("a blank body should still be 4 rows, got %d", len(body))
	}
	for _, r := range body {
		if r != "" {
			t.Fatalf("a blank body row should be empty, got %q", r)
		}
	}
}
