package tui

import (
	"math"
	"testing"

	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// scrollback_test.go covers #165: every emulator the cockpit opens keeps a
// scrollback sized from the replay budget, not the library's 10000 lines.

// TestScrollbackLinesFromReplayKB pins the sizing table: the default budget,
// the floor a tiny budget is raised to, the ceiling a big one is held to, and a
// budget large enough to overflow the byte count if it were multiplied first.
func TestScrollbackLinesFromReplayKB(t *testing.T) {
	cases := []struct {
		name     string
		replayKB int
		want     int
	}{
		{"unset means the daemon default", 0, ptymgr.DefaultRingCap / scrollbackBytesPerLine},
		{"negative means the daemon default", -1, 2048},
		{"tiny budget is floored", 1, minScrollbackLines},
		{"just above the floor", 384, 3072},
		{"exactly the ceiling", maxScrollbackLines * scrollbackBytesPerLine / 1024, maxScrollbackLines},
		{"huge budget is capped", 1 << 20, maxScrollbackLines},
		{"overflow-sized budget is capped", math.MaxInt, maxScrollbackLines},
	}
	for _, c := range cases {
		if got := scrollbackLines(c.replayKB); got != c.want {
			t.Errorf("%s: scrollbackLines(%d) = %d, want %d", c.name, c.replayKB, got, c.want)
		}
	}
	if maxScrollbackLines != vt.DefaultScrollbackSize {
		t.Errorf("the ceiling %d must stay the library default %d", maxScrollbackLines, vt.DefaultScrollbackSize)
	}
}

// TestTileScrollbackLines is a quarter of the zoom's cap, never under its floor.
func TestTileScrollbackLines(t *testing.T) {
	cases := map[int]int{
		0:           2048 / tileScrollbackDivisor, // the default zoom cap, quartered
		1:           minTileScrollbackLines,       // 2000/4 = 500, exactly the floor
		1 << 20:     maxScrollbackLines / tileScrollbackDivisor,
		math.MaxInt: maxScrollbackLines / tileScrollbackDivisor,
	}
	for kb, want := range cases {
		if got := tileScrollbackLines(kb); got != want {
			t.Errorf("tileScrollbackLines(%d) = %d, want %d", kb, got, want)
		}
	}
}

// overfill writes half again as many lines as limit into emu, so a scrollback that was not
// capped would hold far more than cap.
func overfill(emu *vt.SafeEmulator, limit int) {
	fillLines(emu, limit+limit/2+emu.Height())
}

// TestZoomScrollbackIsCapped opens a real zoom and proves its emulator stops
// growing at the derived cap, for the default budget and a configured one.
func TestZoomScrollbackIsCapped(t *testing.T) {
	for _, kb := range []int{0, 384} {
		m := baseModel()
		m.replayKB = kb
		m = m.zoomInto(panel.Panel{ID: "z1", Title: "shell", State: panel.Running})
		if m.emu == nil {
			t.Fatal("precondition: zoomInto should open an emulator")
		}
		want := scrollbackLines(kb)
		overfill(m.emu, want)
		got := m.emu.ScrollbackLen()
		closeZoom(m.emu)
		if got != want {
			t.Errorf("replayKB=%d: zoom scrollback holds %d lines, want the cap %d", kb, got, want)
		}
	}
}

// TestTileScrollbackIsCapped is the same promise for a group-split tile.
func TestTileScrollbackIsCapped(t *testing.T) {
	m := baseModel()
	emu := m.attachEmu("t1", 40, 6)
	t.Cleanup(func() { closeZoom(emu) })
	want := tileScrollbackLines(m.replayKB)
	overfill(emu, want)
	if got := emu.ScrollbackLen(); got != want {
		t.Errorf("tile scrollback holds %d lines, want the cap %d", got, want)
	}
}
