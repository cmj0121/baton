package server

import "regexp"

// The replay snapshot is the program's raw output ring, fed to a fresh client-side
// emulator on attach to reconstruct the screen. baton has no server-side emulator, so
// the CLIENT's emulator is what answers a program's terminal queries (device
// attributes, cursor position, background colour) — its replies go back as panel.input
// so the program gets its answer.
//
// That is fine for LIVE output, but replaying the program's *old* queries through a
// fresh emulator makes it re-answer queries the program was already answered for, long
// ago. Those late replies are injected as input to a program now sitting at a prompt,
// where a shell echoes them as literal garbage (e.g. "62;1;6;22c", "rgb:0000/0000/0000")
// that lingers in the input line even after `clear`. Re-entering a group view, where
// each tile attaches its own emulator, makes it worse.
//
// Query sequences draw nothing, so dropping them from the replay leaves the
// reconstructed screen identical while stopping the spurious replies. Live output is
// never filtered, so a program's real-time query is still answered exactly once.
//
// One report-*triggering* sequence is not a query: the in-band-resize enable
// CSI ? 2048 h (SetInBandResizeMode). A TUI (claude, anything on bubbletea) writes it
// once at startup to subscribe to resize notifications; the emulator answers the enable
// with an immediate size report CSI 48 ; rows ; cols … t. Replayed through a fresh
// emulator, that report is re-emitted as input — a bogus resize (at the replay
// emulator's transient size, not the panel's) delivered to the program every re-attach,
// which reflows its UI and garbles the prompt. Dropping the enable from the replay costs
// nothing (it subscribes the throwaway replay emulator to nothing and draws nothing);
// the live enable is untouched, so the real subscription still happens exactly once.
var replayQueries = regexp.MustCompile(
	// CSI device-attributes / device-status reports: CSI … c (DA1/2/3), CSI … n (DSR,
	// incl. cursor-position request CSI 6 n) — and the XTVERSION query CSI > … q.
	`\x1b\[[0-9;>=?]*[cn]` + `|` + `\x1b\[>[0-9;]*q` +
		// DECRQM mode query: CSI ? … $ p.
		`|` + `\x1b\[\?[0-9;]*\$p` +
		// OSC colour / palette queries: OSC … ; ? terminated by BEL or ST.
		`|` + `\x1b\][0-9;]*;\?(?:\x07|\x1b\\)` +
		// In-band-resize enable: CSI ? 2048 h — answered with an immediate size report.
		`|` + `\x1b\[\?2048h`,
)

// stripReplayQueries removes terminal report-triggering query sequences from a replay
// snapshot, so a re-attaching emulator does not re-answer queries already answered when
// they first happened live. It is applied only to the replay path (server.attach),
// never to live output.
func stripReplayQueries(snap []byte) []byte {
	return replayQueries.ReplaceAll(snap, nil)
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
