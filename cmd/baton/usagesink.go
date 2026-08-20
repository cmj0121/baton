package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/usage"
)

// `baton usage-sink` is the status line baton hands to the Claude Code panels it
// launches. Claude Code pipes the whole session state to it on every render, and
// that payload carries the account's rate-limit windows — the one thing baton
// cannot work out from the transcripts, because a quota is the vendor's opinion
// and not an arithmetic fact about tokens.
//
// It is a status line first and a sink second, and the order matters. Whatever
// the user already had configured is run with the same input and its output
// printed verbatim, so a panel inside baton looks exactly like a panel outside
// it. The harvesting is a side effect the user never sees.
//
// Three rules follow from where it runs:
//
//   - It never writes to stdout except as the status line. A diagnostic printed
//     here does not go to a log, it goes into the user's panel, once every render,
//     until they work out what changed.
//   - It never fails visibly. A sink that cannot write its file has lost a number
//     that will be along again in a second; a status line that exits non-zero
//     after eating the wrapped command's output has cost the user their status
//     line. Errors are dropped on purpose.
//   - It keeps only the four numbers it came for. The payload also carries the
//     transcript path, the working directory and the session cost; a sink that
//     forwards only what it needs is a sink that cannot leak the rest.
//
// Usage:
//
//	baton usage-sink [--wrap <shell command>]
//
// --wrap is the status line to defer to, as the user configured it. With none,
// the sink prints baton's own quota line instead of nothing, so injecting it into
// a panel that had no status line adds a row worth having rather than a blank one.

// sinkBarWidth is the bar width in the sink's own fallback line. It is narrower
// than the cockpit's because it shares a row with whatever else Claude Code puts
// there, and two ten-cell bars already spell out the reading.
const sinkBarWidth = 10

// usageSinkMain runs `baton usage-sink`. It returns a process exit code, which is
// the wrapped status line's own or 0; a failure of the sink's own work never
// shows up here.
func usageSinkMain(args []string) int {
	wrapped := parseWrap(args)

	// Read the payload whole before anything else. The wrapped command needs the
	// same bytes, and stdin can only be drained once.
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		payload = nil
	}

	harvest(payload)

	if wrapped == "" {
		printOwnLine()
		return 0
	}
	return runWrapped(wrapped, payload)
}

// parseWrap pulls the --wrap value out of the argument list. It is hand-parsed
// rather than handed to kong because kong's failure mode is to print usage, and
// this command's stdout is a status line: a usage block would be rendered into
// the user's panel on every frame. An argument it does not understand is ignored
// for the same reason.
func parseWrap(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--wrap" && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(args[i], "--wrap="):
			return strings.TrimPrefix(args[i], "--wrap=")
		}
	}
	return ""
}

// harvest parses the payload's rate limits and drops them in the sink file for
// the daemon to pick up. Every failure below is silent and survivable: a payload
// with no limits in it is the ordinary state of a session before its first API
// response, and a write that fails has lost a reading the next render will bring
// again.
func harvest(payload []byte) {
	if len(payload) == 0 {
		return
	}
	l, ok := usage.ParseStatusline(payload, time.Now())
	if !ok {
		return
	}
	_, _ = usage.WriteLimitsIfChanged(paths.UsageLimitsFile(), l)
}

// printOwnLine renders baton's own quota line, for a panel whose user had no
// status line of their own to wrap.
//
// It reads back what harvest just wrote rather than using the parsed reading
// directly, so the line shows the account's best-known standing rather than this
// panel's slice of it — which for a window the panel has not seen yet is the
// difference between a real number and a blank row.
func printOwnLine() {
	l, ok := usage.ReadLimits(paths.UsageLimitsFile())
	if !ok {
		return
	}
	if s := usage.FormatLimits(l, time.Now(), sinkBarWidth, usage.CountdownAuto); s != "" {
		_, _ = os.Stdout.WriteString(s)
	}
}

// runWrapped executes the user's own status line with the same payload on stdin
// and its output passed straight through. It goes via the shell because that is
// how Claude Code runs a statusLine command, and a wrapper that parsed the string
// itself would break the quoting the user wrote against.
func runWrapped(command string, payload []byte) int {
	cmd := exec.Command(shell(), "-c", command)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		// The status line could not be started at all — a command that has since been
		// renamed, a shell that is missing. There is nothing useful to say into a
		// status line about it, and saying nothing leaves the row blank exactly as it
		// would have been without baton in the way.
		return 0
	}
	return 0
}

// shell is the interpreter the wrapped status line runs under. SHELL is honoured
// so a user whose status line relies on their own shell's syntax keeps it, with
// /bin/sh as the fallback every platform baton runs on has.
func shell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}
