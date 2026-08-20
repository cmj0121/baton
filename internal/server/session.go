package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/usage"
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

// settingsFlag loads an extra settings source into Claude Code. It merges with
// the user's own settings files rather than replacing them and outranks them, so
// injecting one key leaves every other setting they wrote exactly as it was.
const settingsFlag = "--settings"

// withStatusLine returns a copy of spec that launches with baton's usage sink as
// the panel's status line, and whether it injected one.
//
// This is how the account's rate limits reach the cockpit. Claude Code hands its
// whole session state to the status-line command on every render, and that state
// carries the two subscription windows — a reading no amount of transcript
// arithmetic can produce, because a quota is the vendor's judgement rather than a
// sum of tokens. Baton is already launching the process, so it is already in a
// position to be that command.
//
// It is a wrapper, never a replacement. Whatever status line the user configured
// is resolved here and handed to the sink to run, so the panel renders exactly
// what it would have without baton in the way. Three cases end in no injection at
// all, and each of them is a case where injecting would change something that is
// not baton's to change:
//
//   - The panel is not Claude Code. No other agent CLI has this flag, and one
//     handed an unknown option fails to start.
//   - The spec already carries --settings. The user has said what settings this
//     panel runs under, and a second source arguing with the first is worse than
//     no reading.
//   - A status line is configured in a form baton cannot reproduce. Running
//     something else in its place would trade the user's setup for a number.
//
// self is the path to baton's own binary; with none, there is nothing to point
// the status line at.
func withStatusLine(spec ptymgr.Spec, self string) (ptymgr.Spec, bool) {
	if strings.TrimSpace(self) == "" || !isClaudeCommand(spec.Command) {
		return spec, false
	}
	for _, a := range spec.Args {
		if flag, _, _ := strings.Cut(a, "="); flag == settingsFlag {
			return spec, false
		}
	}

	wrapped, configured := usage.StatusLine(ptymgr.PanelDir(spec.Dir))
	if configured && wrapped == "" {
		return spec, false
	}

	// The command is a string Claude Code hands to a shell, so both halves are
	// quoted: baton's own path may sit under a directory with a space in it, and
	// the wrapped command is arbitrary shell the user wrote.
	command := shellQuote(self) + " usage-sink"
	if wrapped != "" {
		command += " --wrap " + shellQuote(wrapped)
	}
	settings, err := json.Marshal(map[string]any{
		"statusLine": map[string]string{"type": "command", "command": command},
	})
	if err != nil {
		return spec, false
	}

	spec.Args = append(append([]string(nil), spec.Args...), settingsFlag, string(settings))
	return spec, true
}

// shellQuote wraps s in single quotes so a shell reads it as one literal word,
// splicing around any single quote it contains. It is the POSIX form rather than
// anything cleverer because the string is going to whichever shell Claude Code
// runs a status line under, and this quoting means the same thing in all of them.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
