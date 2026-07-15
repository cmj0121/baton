// Package vtquery strips terminal query / report-triggering escape sequences from a
// byte stream before it is fed to a client-side emulator.
//
// baton has no server-side emulator, so the CLIENT's emulator is what answers a
// program's terminal queries (device attributes, cursor position, background colour) —
// its replies go back to the program as panel.input so the program gets its answer.
//
// That answering is the problem in two places baton feeds an emulator:
//
//   - REPLAY (server.attach): replaying a program's *old* queries through a fresh
//     emulator makes it re-answer queries answered live long ago. Those late replies are
//     injected as input to a program now at a prompt, where they echo as literal garbage
//     (e.g. "62;1;6;22c", "rgb:0000/0000/0000") that lingers even after `clear`.
//   - LIVE (a zoomed / group-tile / scratch emulator): a running program re-emits its
//     terminal probes — every bubbletea TUI (claude included) does so on /clear — and the
//     live emulator answers them, injecting the reply back into the program's input line.
//     The reply arrives late, at the input box, so it lands as typed characters.
//
// Query sequences draw nothing, so dropping them leaves the reconstructed screen
// identical while stopping the spurious replies; mode-sets that DO draw (alt-screen) are
// left untouched.
//
// One report-*triggering* sequence is not a query: the in-band-resize enable
// CSI ? 2048 h (SetInBandResizeMode). A TUI writes it once at startup (and again on
// /clear) to subscribe to resize notifications; the emulator answers the enable with an
// immediate size report CSI 48 ; rows ; cols … t, and thereafter emits that report on
// every emulator resize. Both reports are injected as input — a bogus resize at the
// emulator's transient size — which reflows the program's UI and garbles the prompt.
// Dropping the enable costs nothing here (it subscribes a throwaway/secondary emulator to
// nothing and draws nothing); the program itself still enabled the mode on its real PTY,
// so its own resize handling is untouched.
package vtquery

import "regexp"

var queries = regexp.MustCompile(
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

// Strip removes terminal report-triggering query sequences from a byte stream, so the
// emulator it is fed to does not answer them (its answers would be injected as input to
// the program). It is applied to the replay snapshot (server.attach) and to the live
// output fed to a client emulator; it must never touch what is stored or displayed.
//
// The fast path returns the input slice unchanged when nothing matches, so the common
// case (a stream chunk with no query) allocates nothing.
func Strip(b []byte) []byte {
	if !queries.Match(b) {
		return b
	}
	return queries.ReplaceAll(b, nil)
}
