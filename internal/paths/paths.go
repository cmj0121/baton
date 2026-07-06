// Package paths resolves where baton keeps its runtime files (the control socket
// and the background server log).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Environment variables baton reads and injects. EnvSocket points a client at
// the control socket (and lets the daemon child inherit the parent's choice
// across its re-session). EnvRole and EnvPanelID are injected into a conductor
// panel's process so the control client inside it identifies itself to the
// server: the scoped role it runs under, and its own panel id (the panel it is
// fenced from acting on).
const (
	EnvSocket  = "BATON_SOCK"
	EnvRole    = "BATON_ROLE"
	EnvPanelID = "BATON_PANEL_ID"
)

// Socket returns the control socket path. It is scoped to the caller's login
// session, so there is one and only one server per session. Override with
// BATON_SOCK — which is also how the daemon child inherits the parent's choice,
// since it re-sessions itself and could not otherwise recompute the same path.
func Socket() string {
	if v := os.Getenv(EnvSocket); v != "" {
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

// QueueDir returns the task-backlog directory that pairs with the given socket —
// one file per queued task. Like StateFile it is derived from the socket path, so
// one daemon-per-session owns one backlog and the daemon child resolves the same
// path from BATON_SOCK after it re-sessions itself.
func QueueDir(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".queue"
}

// LogFile is the default log file ($HOME/.baton/baton.log), used when --log is
// not given. One server runs per login session, so it needs no per-instance
// suffix.
func LogFile() string {
	return filepath.Join(home(), ".baton", "baton.log")
}

// ConfigFile is the user's persistent client configuration ($HOME/.baton/config,
// YAML). It holds settings such as the key-binding overrides.
func ConfigFile() string {
	return filepath.Join(home(), ".baton", "config")
}

// TUIConfigFile is the user's cockpit appearance configuration
// ($HOME/.baton/TUI.yaml): the colour theme and the group-split layouts. It is a
// separate file from ConfigFile so a user can reshape the look without touching
// their bindings and behaviour settings. The server reads it, merges it into the
// effective config, and broadcasts it to every frontend.
func TUIConfigFile() string {
	return filepath.Join(home(), ".baton", "TUI.yaml")
}

// ConductorFile is the operator's conductor brief ($HOME/.baton/CONDUCTOR.md): a
// goal and guide the user writes for the conductor agent. It is optional — when
// absent or empty the conductor gets only the built-in control primer. The server
// reads it each time it builds a conductor workspace, so edits take effect on the
// next time the conductor is opened or re-run.
func ConductorFile() string {
	return filepath.Join(home(), ".baton", "CONDUCTOR.md")
}

// PluginFile is the user's Lua plugin ($HOME/.baton/plug-in.lua). BATON_PLUGIN
// overrides it (and is how the daemon child inherits an explicit --plugin choice
// across the re-exec, since it re-sessions itself).
func PluginFile() string {
	if v := os.Getenv("BATON_PLUGIN"); v != "" {
		return v
	}
	return filepath.Join(home(), ".baton", "plug-in.lua")
}

// EnsureDir creates the directory that holds the given file, with private perms.
func EnsureDir(file string) error {
	return os.MkdirAll(filepath.Dir(file), 0o700)
}

// SecureSocket tightens a freshly bound control socket to owner-only (0600). The
// socket is the one uid-private channel that drives the whole fleet — anyone who
// can connect can spawn processes as this user — so it must not be reachable by
// group or other. net.Listen creates the socket under the process umask, which on
// a permissive umask leaves group/other bits set; on Linux those bits gate
// connect(2), so clamping them here is a real barrier (and defence in depth
// behind the 0700 runtime dir, which is the platform-independent gate). The
// server additionally verifies each peer's uid, so this is the outer of two
// layers. A missing socket (already unlinked) is not an error.
func SecureSocket(socket string) error {
	if err := os.Chmod(socket, 0o600); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// NewConductorWorkspace creates a fresh, private, ephemeral directory for a
// conductor panel under baton's runtime dir and returns its path. The conductor
// agent runs here instead of in any source tree, so its only local surface is
// the baton control wiring the server drops in — not the user's code. The caller
// removes it when the conductor panel is closed.
func NewConductorWorkspace() (string, error) {
	base := runtimeDir()
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, "conductor-")
}

// WriteFileAtomic writes data to path atomically and durably: it writes a sibling
// temp file, fsyncs it, renames it into place, then fsyncs the parent directory so
// the rename itself survives a crash. A reader therefore sees either the old file
// or the whole new one — never a truncated mix. The caller is responsible for
// ensuring the parent directory exists (see EnsureDir).
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	// Any failure from here on leaves a stale temp file behind; drop it on the way
	// out, so a half-written ".tmp" never lingers.
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}

	// Fsync the parent directory so the rename is durable. Not every platform can
	// open a directory for sync; that is not fatal to the write.
	if dir, derr := os.Open(filepath.Dir(path)); derr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// runtimeDir is the per-user base for baton's runtime files.
func runtimeDir() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "baton")
	}
	return filepath.Join(home(), ".baton")
}

// home resolves the user's home directory. It prefers the OS resolution
// (os.UserHomeDir, which reads $HOME on Unix) and falls back to a literal $HOME,
// so a caller never silently anchors baton's files to a relative ".baton" built
// from an empty string when the environment is unusual.
func home() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// sessionID identifies the caller's login session by its process session id, so
// each session maps to its own socket. Falls back to the parent PID.
func sessionID() int {
	if sid, err := unix.Getsid(0); err == nil && sid > 0 {
		return sid
	}
	return os.Getppid()
}
