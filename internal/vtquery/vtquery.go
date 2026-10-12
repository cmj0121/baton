// Package vtquery strips terminal query / report-triggering escape sequences from a
// byte stream before it is fed to a client-side emulator, and answers the queries
// that do not need screen state.
//
// baton has no server-side emulator. Left alone, the CLIENT's emulator answers a
// program's terminal queries (device attributes, cursor position, background colour)
// and those replies go back to the program as panel.input.
//
// That answering is the problem in two places baton feeds an emulator:
//
//   - REPLAY (server.attach): replaying a program's *old* queries through a fresh
//     emulator makes it re-answer queries answered live long ago. Those late replies are
//     injected as input to a program now at a prompt, where they echo as literal garbage
//     (e.g. "62;1;6;22c", "rgb:0000/0000/0000") that lingers even after `clear`.
//   - LIVE (a zoomed / group-tile emulator): a running program re-emits its
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

// answered matches the queries the server can answer without an emulator, in the
// order they appear in the chunk:
//
//	CSI 0* 5 n      device-status DSR. Ps is exactly 5; leading zeros are the same
//	                parameter. No private marker, no second parameter.
//	CSI [>=]? … c   DA1 (marker ""), DA2 (marker ">"), DA3 (marker "=").
//
// The DSR alternative is tried first so CSI 5 n is not read as a DA. The marker set
// is exactly [>=], so a CSI ending in c with another private marker or an
// intermediate byte (CSI > 4 ; 0 m) is not a DA. CSI 6 n, CSI ? 5 n, CSI ? 6 n,
// CSI 15 n and CSI 5 ; 6 n do not match.
var answered = regexp.MustCompile(`\x1b\[(?:0*5n|([>=]?)[0-9;]*c)`)

// Canned answers. The DA bytes match the client-side x/vt emulator, so the server
// responder is a drop-in for the emulator that answered these live before the
// live-strip landed. Both replies are state-independent, which is why a server
// that keeps no emulator can write them.
const (
	da1Reply = "\x1b[?62;1;6;22c" // VT220 + 132-col + selective-erase + ANSI colour
	da2Reply = "\x1b[>1;10;0c"    // VT220, firmware 10, no cartridge
	// Operating status: ready, no malfunction (ECMA-48 / VT510 DSR). This is CSI 0 n.
	// x/vt emits CSI ? 0 n here because it passes a DEC status type; that is not the
	// reply a terminal gives, and a binding on CSI 0 n would never see it.
	dsrReady = "\x1b[0n"
)

// Reply returns canned answers for the queries in b that do not need screen state,
// in the order they appear, so pump can write them on the PTY the way a real
// terminal does. The write happens on the read that carried the query, before a
// round-tripped emulator reply could arrive late at the prompt as garbage.
//
// Answered:
//   - DA1 and DA2. A full-screen program blocks on DA during its handshake,
//     including around suspend, so a missing answer wedges it (nvim Ctrl-Z).
//     DA3 gets nothing, matching the emulator.
//   - Device-status DSR, CSI 5 n. The ready reply is always CSI 0 n. A readline
//     macro bound to that sequence never runs while the PTY stays silent.
//
// Not answered, because the bytes depend on state this server does not keep:
// cursor-position DSR (CSI 6 n, CSI ? 6 n), DECRQM, colour queries, XTVERSION,
// and the in-band-resize report. Strip removes those before the client emulator,
// so nothing answers them. A canned row, mode bit, or colour would be a lie.
//
// Returns nil when b holds no answerable query, so the common no-query chunk is
// allocation-free. A chunk whose only query is DA3 also returns nil.
func Reply(b []byte) []byte {
	ms := answered.FindAllSubmatch(b, -1)
	if ms == nil {
		return nil
	}
	var out []byte
	for _, m := range ms {
		if m[0][len(m[0])-1] == 'n' { // device-status DSR; group 1 did not participate
			out = append(out, dsrReady...)
			continue
		}
		switch string(m[1]) {
		case "": // DA1
			out = append(out, da1Reply...)
		case ">": // DA2
			out = append(out, da2Reply...)
			// "=" (DA3): the emulator returns nothing, so neither do we.
		}
	}
	return out
}
