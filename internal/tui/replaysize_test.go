package tui

import (
	"strings"
	"testing"

	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/vtirm"
)

// replaysize_test.go covers the cockpit half of #133: a replay is reconstructed
// at the size it was painted at, and only then resized to the view.
//
// A program like Claude repaints RELATIVELY — it erases the rows it believes
// its last frame took, moving the cursor up one row at a time, and draws the new
// frame from there. How many rows that frame took depends on where the terminal
// wrapped it. Replayed into an emulator of a different width, the wraps land
// elsewhere, the cursor-ups overshoot or fall short, and the screen keeps cells
// the program believes it erased (or loses ones it never touched).

// claudeRepaint is a frame painted at 80 columns and then repainted relatively:
// a header, an input line of 100 cells that the terminal wraps onto two rows,
// and a repaint that erases those two rows bottom-up and draws a short line in
// their place. At 80 columns the header survives; at any wider size the input
// sits on one row, the second cursor-up reaches the header and erases it, and
// the new frame lands a row too high.
var claudeRepaint = "header\r\n> " + strings.Repeat("x", 98) +
	"\x1b[2K\x1b[1A\x1b[2K\x1b[G> short"

// paintedAt is what a real terminal of cols x rows shows after claudeRepaint,
// and then after being resized to the view — the screen the replay must rebuild.
func paintedAt(cols, rows, viewCols, viewRows int) string {
	ref := vt.NewSafeEmulator(cols, rows)
	go zoomReader(ref, nil, "ref")
	defer closeZoom(ref)
	writeEmu(ref, &vtirm.Filter{}, []byte(claudeRepaint))
	ref.Resize(viewCols, viewRows)
	return ref.String()
}

// TestAZoomReplaysAtThePaintedSize is the zoom's half: a replay tagged 80x24
// lands in a 120-column emulator exactly as a real 80-column terminal resized
// to 120 would show it, and the emulator is left at the view's size.
func TestAZoomReplaysAtThePaintedSize(t *testing.T) {
	c, _ := recordingServer(t)
	m := baseModel()
	m.client = c
	p := panel.Panel{ID: "a1", Kind: panel.Agent, Title: "claude", State: panel.Running}
	m.fleet = []panel.Panel{p}
	m = m.zoomInto(p)
	t.Cleanup(func() { closeZoom(m.emu) })
	w, h := m.emu.Width(), m.emu.Height()

	nm, _ := m.Update(panelOutputMsg{Type: "output", ID: "a1", Data: []byte(claudeRepaint), Rows: 24, Cols: 80})
	m = nm.(model)

	if got, want := m.emu.String(), paintedAt(80, 24, w, h); got != want {
		t.Errorf("the replay was rebuilt at the wrong size:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(m.emu.String(), "header") {
		t.Errorf("the relative repaint erased a line it never painted:\n%s", m.emu.String())
	}
	if m.emu.Width() != w || m.emu.Height() != h {
		t.Errorf("the emulator was left at %dx%d, want the view's %dx%d", m.emu.Width(), m.emu.Height(), w, h)
	}
}

// TestAGroupTileReplaysAtThePaintedSize is the same promise for a split tile,
// which opens its own emulator per member and is fed by the same message.
func TestAGroupTileReplaysAtThePaintedSize(t *testing.T) {
	c, _ := recordingServer(t)
	m := baseModel()
	m.client = c
	m.mode = modeGroupZoom
	emu := vt.NewSafeEmulator(110, 12)
	go zoomReader(emu, nil, "a1")
	t.Cleanup(func() { closeZoom(emu) })
	m.groupEmus = map[string]*vt.SafeEmulator{"a1": emu}
	m.groupIRMs = map[string]*vtirm.Filter{"a1": {}}

	nm, _ := m.Update(panelOutputMsg{Type: "output", ID: "a1", Data: []byte(claudeRepaint), Rows: 24, Cols: 80})
	_ = nm.(model)

	if got, want := emu.String(), paintedAt(80, 24, 110, 12); got != want {
		t.Errorf("the tile's replay was rebuilt at the wrong size:\n got %q\nwant %q", got, want)
	}
	if emu.Width() != 110 || emu.Height() != 12 {
		t.Errorf("the tile was left at %dx%d, want its own 110x12", emu.Width(), emu.Height())
	}
}

// TestUnsizedOutputIsWrittenAsIs keeps live output — and every replay from a
// daemon that predates the size tag — on the path it always took: straight into
// the emulator at the emulator's own size.
func TestUnsizedOutputIsWrittenAsIs(t *testing.T) {
	c, _ := recordingServer(t)
	m := baseModel()
	m.client = c
	p := panel.Panel{ID: "a1", Kind: panel.Agent, Title: "claude", State: panel.Running}
	m.fleet = []panel.Panel{p}
	m = m.zoomInto(p)
	t.Cleanup(func() { closeZoom(m.emu) })
	w, h := m.emu.Width(), m.emu.Height()

	nm, _ := m.Update(panelOutputMsg{Type: "output", ID: "a1", Data: []byte(claudeRepaint)})
	m = nm.(model)

	if got, want := m.emu.String(), paintedAt(w, h, w, h); got != want {
		t.Errorf("unsized output must be painted at the emulator's size:\n got %q\nwant %q", got, want)
	}
}

// TestAttachIsSentBeforeResize pins the order both attach paths speak in. The
// replay's size tag is the PTY's size when the daemon snapshots it; resizing
// first would tag the old ring tail with the NEW size — the width it was not
// painted at — and the reconstruction above would be done at the wrong one.
func TestAttachIsSentBeforeResize(t *testing.T) {
	var got []string
	sendHook = func(c proto.Command) {
		if c.Action == "panel.attach" || c.Action == "panel.resize" {
			got = append(got, c.Action)
		}
	}
	t.Cleanup(func() { sendHook = nil })

	m := baseModel()
	p := panel.Panel{ID: "a1", Kind: panel.Agent, Title: "claude", State: panel.Running}
	m.fleet = []panel.Panel{p}
	m = m.zoomInto(p)
	closeZoom(m.emu)
	closeZoom(m.attachEmu("a1", 40, 10))

	want := []string{"panel.attach", "panel.resize", "panel.attach", "panel.resize"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("zoomInto then attachEmu sent %v, want %v", got, want)
	}
}
