package tui

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TestRainStepDeterministic: same seed, same size, same number of frames → an
// identical render. The injected RNG is what keeps the animation testable.
func TestRainStepDeterministic(t *testing.T) {
	a := newRain(40, 12, rand.New(rand.NewSource(7)))
	b := newRain(40, 12, rand.New(rand.NewSource(7)))
	for i := 0; i < 60; i++ {
		a.step()
		b.step()
	}
	if !reflect.DeepEqual(a.render(), b.render()) {
		t.Fatal("same-seed rains diverged — the RNG is not the only source of randomness")
	}
}

// TestRainRenderDimensions: render always yields an h×w grid of single-cell
// strings, whatever the drops are doing.
func TestRainRenderDimensions(t *testing.T) {
	r := newRain(30, 8, rand.New(rand.NewSource(1)))
	for i := 0; i < 100; i++ {
		r.step()
	}
	grid := r.render()
	if len(grid) != 8 {
		t.Fatalf("rows = %d, want 8", len(grid))
	}
	for y, row := range grid {
		if len(row) != 30 {
			t.Fatalf("row %d has %d cells, want 30", y, len(row))
		}
		for x, cell := range row {
			if w := lipgloss.Width(cell); w != 1 {
				t.Fatalf("cell (%d,%d) visible width = %d, want 1", y, x, w)
			}
		}
	}
}

// TestRainResizeReflows: a resize reallocates the grid and never panics when the
// drops keep falling into the new bounds.
func TestRainResizeReflows(t *testing.T) {
	r := newRain(20, 10, rand.New(rand.NewSource(3)))
	for i := 0; i < 30; i++ {
		r.step()
	}
	r.resize(60, 24)
	if r.w != 60 || r.h != 24 {
		t.Fatalf("after resize w,h = %d,%d want 60,24", r.w, r.h)
	}
	for i := 0; i < 30; i++ {
		r.step() // must not index a stale column
	}
	if grid := r.render(); len(grid) != 24 || len(grid[0]) != 60 {
		t.Fatalf("post-resize grid = %dx%d, want 24x60", len(grid), len(grid[0]))
	}
	// A degenerate 0×0 resize floors to 1×1 rather than blowing up.
	r.resize(0, 0)
	if r.w != 1 || r.h != 1 {
		t.Fatalf("zero resize = %dx%d, want 1x1", r.w, r.h)
	}
}

// TestBigClockShape: given room, the clock renders in the bold 6-row font, all rows
// the same width, in the block glyph.
func TestBigClockShape(t *testing.T) {
	rows := bigClock(time.Date(2026, 7, 6, 13, 4, 5, 0, time.UTC), 200)
	if len(rows) != 6 {
		t.Fatalf("bold clock rows = %d, want 6", len(rows))
	}
	w := len([]rune(rows[0]))
	for i, r := range rows {
		if len([]rune(r)) != w {
			t.Fatalf("row %d width = %d, want %d (uneven clock)", i, len([]rune(r)), w)
		}
	}
	if !strings.Contains(rows[0], "█") {
		t.Error("clock should be drawn in the block glyph")
	}
}

// TestBigClockFallback: too narrow for the bold font, the clock drops to the compact
// 5-row fallback rather than overflowing.
func TestBigClockFallback(t *testing.T) {
	now := time.Date(2026, 7, 6, 13, 4, 5, 0, time.UTC)
	bold := bigClock(now, 200)
	small := bigClock(now, 10) // no bold clock fits in 10 cells
	if len(small) != 5 {
		t.Fatalf("fallback clock rows = %d, want the compact 5", len(small))
	}
	if len([]rune(small[0])) >= len([]rune(bold[0])) {
		t.Error("the fallback should be narrower than the bold clock")
	}
}

// TestEnterExitScreensaver: enter remembers the covered view and builds the rain;
// exit restores that view and drops the rain.
func TestEnterExitScreensaver(t *testing.T) {
	m := baseModel()
	m.mode = modeGroupZoom
	m.now = time.Unix(100, 0)

	m = m.enterScreensaver()
	if m.mode != modeScreensaver {
		t.Fatalf("mode = %v, want modeScreensaver", m.mode)
	}
	if m.saver == nil {
		t.Fatal("enter should build the rain")
	}
	if m.saverReturn != modeGroupZoom {
		t.Fatalf("saverReturn = %v, want modeGroupZoom", m.saverReturn)
	}

	m = m.exitScreensaver()
	if m.mode != modeGroupZoom {
		t.Fatalf("exit mode = %v, want the restored modeGroupZoom", m.mode)
	}
	if m.saver != nil {
		t.Fatal("exit should drop the rain")
	}
}

// TestScreensaverDismissAnyKeySwallowed: any key exits the saver and is swallowed
// whole — even the detach key does not act (the cockpit must not quit).
func TestScreensaverDismissAnyKeySwallowed(t *testing.T) {
	m := baseModel()
	m = m.enterScreensaver() // from the dashboard
	tm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(keyDetach)})
	nm := tm.(model)
	if nm.mode != modeDashboard {
		t.Fatalf("mode = %v, want the dashboard restored", nm.mode)
	}
	if nm.quitting {
		t.Fatal("the dismissing key must be swallowed, not acted on (no detach)")
	}
	if cmd != nil {
		t.Fatal("dismiss should not re-arm any command")
	}
}

// TestScreensaverDismissMouse: a click dismisses the saver; motion is ignored so
// cell-motion noise neither dismisses nor leaks into the covered view.
func TestScreensaverDismissMouse(t *testing.T) {
	m := baseModel()
	m = m.enterScreensaver()

	tm, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionMotion})
	if tm.(model).mode != modeScreensaver {
		t.Fatal("mouse motion should not dismiss the saver")
	}

	tm, _ = tm.(model).Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if tm.(model).mode != modeDashboard {
		t.Fatal("a click should dismiss the saver")
	}
}

// TestIdleAutoStart: after saverIdle of no input, the 1 s tick enters the saver.
func TestIdleAutoStart(t *testing.T) {
	m := baseModel()
	m.lastInput = time.Unix(0, 0)
	tm, cmd := m.Update(tickMsg(time.Unix(0, 0).Add(saverIdle + time.Second)))
	nm := tm.(model)
	if nm.mode != modeScreensaver {
		t.Fatalf("mode = %v, want the saver to auto-start", nm.mode)
	}
	if cmd == nil {
		t.Fatal("auto-start should return the animation + clock ticks")
	}
}

// TestIdleDoesNotStartBeforeThreshold: a tick just short of saverIdle leaves the
// dashboard alone.
func TestIdleDoesNotStartBeforeThreshold(t *testing.T) {
	m := baseModel()
	m.lastInput = time.Unix(0, 0)
	tm, _ := m.Update(tickMsg(time.Unix(0, 0).Add(saverIdle - time.Second)))
	if tm.(model).mode != modeDashboard {
		t.Fatal("saver must not start before the idle threshold")
	}
}

// TestCanAutoSaverGuards: the auto-start refuses in every view where keystrokes are
// flowing to a program or an overlay, and while the backend is down.
func TestCanAutoSaverGuards(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*model)
		want bool
	}{
		{"dashboard", func(*model) {}, true},
		{"zoom", func(m *model) { m.mode = modeZoom }, false},
		{"group-split", func(m *model) { m.mode = modeGroupZoom }, false},
		{"already-saving", func(m *model) { m.mode = modeScreensaver }, false},
		{"scratch-open", func(m *model) { m.scratchOpen = true }, false},
		{"input-overlay", func(m *model) { m.input = inputFilter }, false},
		{"scroll-mode", func(m *model) { m.scrolling = true }, false},
		{"leader-armed", func(m *model) { m.prefix = true }, false},
		{"rebinding", func(m *model) { m.editing = true }, false},
		{"backend-down", func(m *model) { m.backendDown = true }, false},
		{"pending-close", func(m *model) { m.pendingClose = true }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseModel()
			tc.mut(&m)
			if got := m.canAutoSaver(); got != tc.want {
				t.Fatalf("canAutoSaver() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConnClosedExitsSaver: a backend drop force-exits the saver so the outage
// alert is never masked behind the rain.
func TestConnClosedExitsSaver(t *testing.T) {
	m := baseModel()
	m = m.enterScreensaver()
	tm, _ := m.Update(connClosedMsg{})
	nm := tm.(model)
	if nm.mode == modeScreensaver {
		t.Fatal("a backend drop should exit the saver")
	}
	if !nm.backendDown {
		t.Fatal("backendDown should be set")
	}
}

// TestSaverTickReArmsOnlyWhenActive: the fast tick re-arms while the saver runs and
// stops the moment it is dismissed.
func TestSaverTickReArmsOnlyWhenActive(t *testing.T) {
	m := baseModel()
	m = m.enterScreensaver()
	if _, cmd := m.Update(saverTickMsg(time.Unix(0, 0))); cmd == nil {
		t.Fatal("an active saver should re-arm the animation tick")
	}
	m = m.exitScreensaver()
	if _, cmd := m.Update(saverTickMsg(time.Unix(0, 0))); cmd != nil {
		t.Fatal("a dismissed saver must not re-arm the animation tick")
	}
}

// TestSummonFromDashboard: C-t E (the hidden binding) enters the saver from the
// dashboard, and it is absent from the editable key map so it stays a secret.
func TestSummonFromDashboard(t *testing.T) {
	m := baseModel()
	m.prefix = true // leader already pressed
	tm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(keyScreensaver)})
	if tm.(model).mode != modeScreensaver {
		t.Fatal("C-t E should summon the saver")
	}
	if cmd == nil {
		t.Fatal("summon should start the animation tick")
	}
	for _, b := range bindings {
		if b.key == keyScreensaver && b.act != actDetach {
			t.Errorf("the screensaver key %q leaked into the editable bindings", keyScreensaver)
		}
	}
}

// TestSummonFromZoom: C-t E summons the saver from a zoom (the armed handler), and
// exit restores the zoom it covered.
func TestSummonFromZoom(t *testing.T) {
	m := baseModel()
	m.mode = modeZoom
	m.zoomArmed = true // leader already pressed inside the zoom
	tm, cmd := m.handleZoomKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(keyScreensaver)})
	nm := tm.(model)
	if nm.mode != modeScreensaver {
		t.Fatal("C-t E should summon the saver from a zoom")
	}
	if nm.saverReturn != modeZoom {
		t.Fatalf("saverReturn = %v, want modeZoom", nm.saverReturn)
	}
	if cmd == nil {
		t.Fatal("summon should start the animation tick")
	}
	if nm.exitScreensaver().mode != modeZoom {
		t.Fatal("exit should restore the covered zoom")
	}
}

// TestSummonFromGroupSplit: C-t E summons the saver from the group split (its armed
// handler), restoring the split on exit so its live tiles are untouched.
func TestSummonFromGroupSplit(t *testing.T) {
	m := baseModel()
	m.mode = modeGroupZoom
	m.groupArmed = true // leader already pressed inside the split
	tm, cmd := m.handleGroupZoomKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(keyScreensaver)})
	nm := tm.(model)
	if nm.mode != modeScreensaver {
		t.Fatal("C-t E should summon the saver from the group split")
	}
	if nm.saverReturn != modeGroupZoom {
		t.Fatalf("saverReturn = %v, want modeGroupZoom", nm.saverReturn)
	}
	if cmd == nil {
		t.Fatal("summon should start the animation tick")
	}
}

// TestScreensaverViewFullScreen: the view owns exactly height rows, each width
// cells wide, and paints the clock somewhere in the frame.
func TestScreensaverViewFullScreen(t *testing.T) {
	m := baseModel()
	m.now = time.Date(2026, 7, 6, 13, 4, 5, 0, time.UTC)
	m = m.enterScreensaver()
	view := m.screensaverView()
	lines := strings.Split(view, "\n")
	if len(lines) != m.height {
		t.Fatalf("lines = %d, want %d", len(lines), m.height)
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != m.width {
			t.Fatalf("line %d visible width = %d, want %d", i, w, m.width)
		}
	}
	if !strings.Contains(view, "█") {
		t.Error("the frame should carry the block-glyph wordmark / clock")
	}
}
