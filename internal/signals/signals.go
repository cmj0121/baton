// Package signals is the single source of truth for the signals baton can send
// to a panel's process. The cockpit's picker and the server's delivery both read
// it, so the menu the user sees and the set the server accepts can never drift.
package signals

import (
	"strconv"
	"strings"
	"syscall"
)

// Choice is one entry in the send-signal picker: a single hotkey, the wire name,
// a short gloss, and the OS signal it maps to.
type Choice struct {
	Key  string // picker hotkey
	Name string // wire name, e.g. "SIGINT"
	Desc string // short gloss
	Sig  syscall.Signal
}

// Choices is the picker menu, in display order — the everyday "Common 7". The
// picker also offers an "other…" entry that accepts any name or number Lookup
// resolves, so this list is the shortcut set, not the limit.
var Choices = []Choice{
	{"c", "SIGINT", "interrupt (Ctrl-C)", syscall.SIGINT},
	{"t", "SIGTERM", "terminate", syscall.SIGTERM},
	{"k", "SIGKILL", "force kill", syscall.SIGKILL},
	{"h", "SIGHUP", "hangup", syscall.SIGHUP},
	{"q", "SIGQUIT", "quit + core", syscall.SIGQUIT},
	{"1", "SIGUSR1", "user-defined 1", syscall.SIGUSR1},
	{"2", "SIGUSR2", "user-defined 2", syscall.SIGUSR2},
}

// byName is the full set of names Lookup accepts — the portable POSIX signals
// defined on every Unix Go targets, so a name resolves the same on Linux and
// macOS. It is a superset of Choices, giving the picker's "other…" entry reach
// beyond the seven shortcuts (SIGWINCH, SIGTSTP, SIGCONT, …).
var byName = map[string]syscall.Signal{
	"SIGABRT":   syscall.SIGABRT,
	"SIGALRM":   syscall.SIGALRM,
	"SIGBUS":    syscall.SIGBUS,
	"SIGCHLD":   syscall.SIGCHLD,
	"SIGCONT":   syscall.SIGCONT,
	"SIGFPE":    syscall.SIGFPE,
	"SIGHUP":    syscall.SIGHUP,
	"SIGILL":    syscall.SIGILL,
	"SIGINT":    syscall.SIGINT,
	"SIGIO":     syscall.SIGIO,
	"SIGKILL":   syscall.SIGKILL,
	"SIGPIPE":   syscall.SIGPIPE,
	"SIGPROF":   syscall.SIGPROF,
	"SIGQUIT":   syscall.SIGQUIT,
	"SIGSEGV":   syscall.SIGSEGV,
	"SIGSTOP":   syscall.SIGSTOP,
	"SIGSYS":    syscall.SIGSYS,
	"SIGTERM":   syscall.SIGTERM,
	"SIGTRAP":   syscall.SIGTRAP,
	"SIGTSTP":   syscall.SIGTSTP,
	"SIGTTIN":   syscall.SIGTTIN,
	"SIGTTOU":   syscall.SIGTTOU,
	"SIGURG":    syscall.SIGURG,
	"SIGUSR1":   syscall.SIGUSR1,
	"SIGUSR2":   syscall.SIGUSR2,
	"SIGVTALRM": syscall.SIGVTALRM,
	"SIGWINCH":  syscall.SIGWINCH,
	"SIGXCPU":   syscall.SIGXCPU,
	"SIGXFSZ":   syscall.SIGXFSZ,
}

// Lookup resolves a signal token to an OS signal. It accepts a known name, with
// or without the SIG prefix and in any case ("int", "SIGINT", "Term"), or a raw
// signal number ("9", "15"). It reports false for anything it does not know, so
// a typo is rejected rather than guessed.
func Lookup(token string) (syscall.Signal, bool) {
	token = strings.ToUpper(strings.TrimSpace(token))
	if token == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(token); err == nil {
		if n > 0 && n < 64 { // the POSIX real-time range covers everything we deliver
			return syscall.Signal(n), true
		}
		return 0, false
	}
	if !strings.HasPrefix(token, "SIG") {
		token = "SIG" + token
	}
	sig, ok := byName[token]
	return sig, ok
}

// Valid reports whether Lookup would resolve the token — the cheap check the
// cockpit runs before sending a hand-typed "other…" signal.
func Valid(token string) bool {
	_, ok := Lookup(token)
	return ok
}
