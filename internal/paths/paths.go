// Package paths resolves where baton keeps its runtime files (the control socket
// and the background server log).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Socket returns the control socket path. It is scoped to the caller's login
// session, so there is one and only one server per session. Override with
// BATON_SOCK — which is also how the daemon child inherits the parent's choice,
// since it re-sessions itself and could not otherwise recompute the same path.
func Socket() string {
	if v := os.Getenv("BATON_SOCK"); v != "" {
		return v
	}
	return filepath.Join(runtimeDir(), fmt.Sprintf("baton-%d.sock", sessionID()))
}

// LogFile is the default log file, used when --log is not given. One server
// runs per login session, so it needs no per-instance suffix.
func LogFile() string {
	return filepath.Join(os.Getenv("HOME"), ".baton", "log")
}

// EnsureDir creates the directory that holds the given file, with private perms.
func EnsureDir(file string) error {
	return os.MkdirAll(filepath.Dir(file), 0o700)
}

// runtimeDir is the per-user base for baton's runtime files.
func runtimeDir() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "baton")
	}
	return filepath.Join(os.Getenv("HOME"), ".baton")
}

// sessionID identifies the caller's login session by its process session id, so
// each session maps to its own socket. Falls back to the parent PID.
func sessionID() int {
	if sid, err := syscall.Getsid(0); err == nil && sid > 0 {
		return sid
	}
	return os.Getppid()
}
