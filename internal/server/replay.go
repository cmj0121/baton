package server

import (
	"regexp"

	"github.com/cmj0121/baton/internal/vtquery"
)

// stripReplayQueries removes terminal report-triggering query sequences from a replay
// snapshot, so a re-attaching emulator does not re-answer queries already answered when
// they first happened live. The same strip is applied client-side to live output fed to
// an emulator (see internal/tui writeEmu); the shared rule lives in internal/vtquery.
func stripReplayQueries(snap []byte) []byte {
	return vtquery.Strip(snap)
}

// screenReset matches the sequences that redefine the whole screen from scratch, so
// everything before the last one is overdrawn and irrelevant to the current view:
//   - CSI 2 J — erase the entire display (what `clear` emits).
//   - CSI ? 1049 h/l, and the older 1047 / 47 — switch to / from the alternate screen.
//   - ESC c — RIS, a full terminal reset.
var screenReset = regexp.MustCompile(`\x1b\[2J|\x1b\[\?(?:1049|1047|47)[hl]|\x1bc`)

// trimToLastScreenReset drops replay bytes before the last full screen-defining reset,
// so a fresh emulator reconstructs the view from a clean boundary instead of desyncing.
//
// The replay snapshot is a fixed-size raw byte ring (no server-side emulator). A long
// full-screen program — vim, a pager — overflows the ring, so its one ESC[?1049h
// (enter the alternate screen) is evicted from the front. A fresh emulator replaying
// the remainder never switches to the alternate screen, so that program's drawing lands
// on the PRIMARY grid and lingers there after it exits — visible as dirty data, worst
// in a group split where every tile attaches its own fresh emulator. The persistent
// zoom emulator never sees this: it was live throughout and kept the real screen state.
//
// Trimming to the last reset is exact for the visible screen (the cells only depend on
// output since the last clear / alt-screen switch) and a no-op when there is no reset,
// so ordinary scrollback is preserved. It is applied only to the replay path, alongside
// stripReplayQueries; the marker itself is kept so the emulator performs the reset.
func trimToLastScreenReset(snap []byte) []byte {
	locs := screenReset.FindAllIndex(snap, -1)
	if len(locs) == 0 {
		return snap
	}
	return snap[locs[len(locs)-1][0]:]
}
