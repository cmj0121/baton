package server

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/cmj0121/baton/internal/ptymgr"
)

// Claude Code writes one JSONL transcript per session, named by the session id,
// and will use an id baton hands it — so giving every agent panel an id of its
// own is what turns the account-wide usage total into a per-panel one. Without
// it the only handle is the working directory, which several panels can share.
//
// Two properties of the flag shape everything below:
//
//   - The id must be unique per launch. Re-using one is a hard error ("Session ID
//     ... is already in use") that exits before the agent starts, and it stays an
//     error after the first session has ended. So the id is minted at each spawn
//     and never stored in the panel's frozen spawn spec, which respawn replays
//     verbatim.
//   - It is Claude Code's flag. Another agent CLI would reject an unknown option
//     and fail to start, so injection is limited to commands that are Claude Code,
//     and a panel running anything else simply has no per-panel usage.
const sessionIDFlag = "--session-id"

// sessionFlags are the options that already decide a session's identity. When the
// user has set one by hand, baton adds nothing: a second --session-id would be a
// conflicting argument, and resuming a session means deliberately re-entering an
// existing id.
var sessionFlags = []string{sessionIDFlag, "--resume", "-r", "--continue", "-c", "--fork-session"}

// newSessionID mints a random RFC 4122 version-4 UUID, the form the flag
// requires. crypto/rand.Read is documented never to fail.
func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// isClaudeCommand reports whether spec runs Claude Code, matched on the binary's
// name so an absolute path, a version-suffixed name or a Windows .exe all count.
func isClaudeCommand(command string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	base = strings.TrimSuffix(base, ".exe")
	return base == "claude" || strings.HasPrefix(base, "claude-")
}

// withSessionID returns a copy of spec that launches with a session id of its
// own, and the id it minted. It returns the spec untouched and an empty id when
// the panel is not one baton can identify this way: a non-Claude command, or one
// whose arguments already pin the session.
//
// The copy matters. The caller keeps the original spec to replay on respawn, and
// replaying an id would make the respawn fail outright.
func withSessionID(spec ptymgr.Spec) (ptymgr.Spec, string) {
	if !isClaudeCommand(spec.Command) {
		return spec, ""
	}
	for _, a := range spec.Args {
		flag, _, _ := strings.Cut(a, "=")
		for _, known := range sessionFlags {
			if flag == known {
				return spec, ""
			}
		}
	}
	id := newSessionID()
	spec.Args = append(append([]string(nil), spec.Args...), sessionIDFlag, id)
	return spec, id
}
