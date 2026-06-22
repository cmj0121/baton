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
var replayQueries = regexp.MustCompile(
	// CSI device-attributes / device-status reports: CSI … c (DA1/2/3), CSI … n (DSR,
	// incl. cursor-position request CSI 6 n) — and the XTVERSION query CSI > … q.
	`\x1b\[[0-9;>=?]*[cn]` + `|` + `\x1b\[>[0-9;]*q` +
		// DECRQM mode query: CSI ? … $ p.
		`|` + `\x1b\[\?[0-9;]*\$p` +
		// OSC colour / palette queries: OSC … ; ? terminated by BEL or ST.
		`|` + `\x1b\][0-9;]*;\?(?:\x07|\x1b\\)`,
)

// stripReplayQueries removes terminal report-triggering query sequences from a replay
// snapshot, so a re-attaching emulator does not re-answer queries already answered when
// they first happened live. It is applied only to the replay path (server.attach),
// never to live output.
func stripReplayQueries(snap []byte) []byte {
	return replayQueries.ReplaceAll(snap, nil)
}
