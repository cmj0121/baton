package tui

import (
	vt "github.com/charmbracelet/x/vt"

	"github.com/cmj0121/baton/internal/ptymgr"
)

// Scrollback sizing (#165). The vt library keeps DefaultScrollbackSize (10000)
// lines unless told otherwise, and every line holds a full row of cells — at 200
// columns that is ~200 MB for one emulator, and a group split can open sixteen.
// Most of it is never reachable: the daemon replays only the panel's ring
// (panel.replay-kb, else ptymgr.DefaultRingCap) on attach, so history older than
// that is gone after a detach anyway. The cap is sized from that budget instead.

const (
	// scrollbackBytesPerLine is the average line length assumed when turning the
	// replay budget into lines; agent output runs well under a full row.
	scrollbackBytesPerLine = 128
	// minScrollbackLines keeps a small budget from leaving a zoom with too little
	// history to search or copy from.
	minScrollbackLines = 2000
	// maxScrollbackLines is never more than the library default, which is what
	// every emulator kept before the cap.
	maxScrollbackLines = vt.DefaultScrollbackSize
	// minTileScrollbackLines and tileScrollbackDivisor size a split tile: a tile
	// is a glance, not the place to dig through history, and there can be many.
	minTileScrollbackLines = 500
	tileScrollbackDivisor  = 4
)

// scrollbackLines is the zoom emulator's scrollback for a replay budget in KiB,
// where zero or less means the daemon's default ring. The budget is clamped
// before it is turned into bytes, so a huge configured value cannot overflow.
func scrollbackLines(replayKB int) int {
	bytes := ptymgr.DefaultRingCap
	if replayKB > 0 {
		bytes = min(replayKB, maxScrollbackLines*scrollbackBytesPerLine/1024) * 1024
	}
	return max(minScrollbackLines, min(bytes/scrollbackBytesPerLine, maxScrollbackLines))
}

// tileScrollbackLines is a group-split tile's scrollback: a fraction of the
// zoom's, floored so a tile still holds a few screens.
func tileScrollbackLines(replayKB int) int {
	return max(minTileScrollbackLines, scrollbackLines(replayKB)/tileScrollbackDivisor)
}
