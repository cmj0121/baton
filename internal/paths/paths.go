// Package paths resolves where baton keeps its runtime files (the control socket
// and the background server log).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// PidFile returns the daemon PID file that pairs with the given socket. It is
// derived from the socket path so the daemon child — which re-sessions itself
// and cannot recompute Socket() — resolves the same path from BATON_SOCK.
func PidFile(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".pid"
}

// StateFile returns the persisted fleet/layout snapshot that pairs with the
// given socket. Like PidFile it is derived from the socket path, so one
// daemon-per-session owns one state file and the daemon child resolves the same
// path from BATON_SOCK after it re-sessions itself.
func StateFile(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".state.json"
}

// LogFile is the default log file ($HOME/.baton/baton.log), used when --log is
// not given. One server runs per login session, so it needs no per-instance
// suffix.
func LogFile() string {
	return filepath.Join(os.Getenv("HOME"), ".baton", "baton.log")
}

// ConfigFile is the user's persistent client configuration ($HOME/.baton/config,
// YAML). It holds settings such as the key-binding overrides.
func ConfigFile() string {
	return filepath.Join(os.Getenv("HOME"), ".baton", "config")
}

// PluginFile is the user's Lua plugin ($HOME/.baton/plug-in.lua). BATON_PLUGIN
// overrides it (and is how the daemon child inherits an explicit --plugin choice
// across the re-exec, since it re-sessions itself).
func PluginFile() string {
	if v := os.Getenv("BATON_PLUGIN"); v != "" {
		return v
	}
	return filepath.Join(os.Getenv("HOME"), ".baton", "plug-in.lua")
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
