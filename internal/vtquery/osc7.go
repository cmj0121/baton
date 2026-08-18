package vtquery

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"
)

// osc7 matches the shell's working-directory report:
//
//	OSC 7 ; file://HOST/PATH BEL     (or terminated by ST, ESC \)
//
// It is a report, not a query, so Strip leaves it alone — the emulator answers
// nothing and the sequence rides the normal output path where it can be read.
var osc7 = regexp.MustCompile(`\x1b\]7;([^\x07\x1b]*)(?:\x07|\x1b\\)`)

// OSC7Path reads the last working-directory report in a chunk of terminal output,
// returning the host the shell claims to be on and the directory it is in.
//
// The host is returned rather than discarded because it decides whether the path
// means anything locally. A shell inside an ssh session reports the *remote*
// host and a *remote* path; treating that as a local directory would land a
// respawn in a same-named local directory, which is the failure worth avoiding
// most — silently landing somewhere else is worse than not landing at all.
// Deciding what counts as "this host" needs the machine's own name, so it is left
// to the caller and kept out of this package.
//
// The last report wins: a chunk can carry several if the shell printed a few
// prompts at once, and only the newest describes where it is now.
func OSC7Path(b []byte) (path, host string, ok bool) {
	m := osc7.FindAllSubmatch(b, -1)
	if len(m) == 0 {
		return "", "", false
	}
	raw := string(m[len(m)-1][1])
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "file") || u.Path == "" {
		return "", "", false
	}
	// url.Parse has already percent-decoded the path; a shell reporting a
	// directory with a space or a non-ASCII name arrives usable.
	return u.Path, u.Hostname(), true
}

// osc7Prefix is the gate's needle, kept as bytes so the check over a panel's
// output allocates nothing.
var osc7Prefix = []byte("\x1b]7;")

// HasOSC7 is the cheap gate for the output hot path: a substring scan that tells
// the expensive parse it is worth running. Almost every chunk of a panel's output
// carries no report at all, and this keeps those chunks free.
func HasOSC7(b []byte) bool { return bytes.Contains(b, osc7Prefix) }
