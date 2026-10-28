package vtquery

import (
	"bytes"
	"regexp"
)

// privateMode matches a DEC private mode set or reset:
//
//	CSI ? Pm h     (DECSET)
//	CSI ? Pm l     (DECRST)
//
// Pm is a semicolon-separated list, so one sequence can toggle several modes at
// once — a full-screen program entering the alternate screen commonly sends
// CSI ? 1049 ; 2004 h in a single write. Like the OSC 7 report this is a mode
// change the program announces, not a query, so Strip leaves it on the output
// path where it can be read.
var privateMode = regexp.MustCompile(`\x1b\[\?([0-9;]*)([hl])`)

// pasteMode is the parameter for bracketed paste, compared against each whole
// field of the list so a neighbouring number such as 12004 or 20045 that merely
// contains it is never mistaken for it.
var pasteMode = []byte("2004")

// BracketedPaste reads the bracketed-paste state a chunk of terminal output
// leaves behind: on when the program asked for pasted text to arrive wrapped in
// ESC[200~ … ESC[201~, off when it asked for raw pastes again.
//
// seen reports whether the chunk touched the mode at all, and it is what makes
// the answer usable. Most chunks say nothing about paste mode, and treating
// those as "off" would undo a program's earlier request the moment it printed
// anything else; a caller keeps its previous state until seen is true.
//
// The last toggle wins, as it does for the terminal itself: a shell turns the
// mode on before each prompt and off before running a command, and a chunk that
// spans both describes only where it ended. This is a scan, not an emulator — it
// knows nothing of the other modes in a combined list, nor of a sequence split
// across two chunks; joining a carried tail to the next chunk is the caller's job.
func BracketedPaste(buf []byte) (on, seen bool) {
	// The cheap gate first, as HasOSC7 is for the cwd report: almost no chunk of
	// a panel's output mentions the mode, and those skip the regex entirely.
	if !bytes.Contains(buf, pasteMode) {
		return false, false
	}
	for _, m := range privateMode.FindAllSubmatch(buf, -1) {
		for field := range bytes.SplitSeq(m[1], []byte{';'}) {
			if bytes.Equal(field, pasteMode) {
				on, seen = m[2][0] == 'h', true
				break
			}
		}
	}
	return on, seen
}
