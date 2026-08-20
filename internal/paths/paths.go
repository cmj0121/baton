// Package paths resolves where baton keeps its runtime files (the control socket
// and the background server log).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// Environment variables baton reads and injects. EnvSocket points a client at
// the control socket (and lets the daemon child inherit the parent's choice
// across its re-session, which is also how every panel it spawns finds the
// socket). EnvPanelID is injected into every agent panel's process, so the
// control client inside it can name itself to the server without being told
// which panel it is. EnvRole is injected into the conductor panel alone: it
// names the scoped role that panel runs under, and the fence that comes with it.
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

// Sockets lists every baton control socket in the runtime directory, newest
// first. It exists for the remote bridge (`baton --stdio`), which is run by
// sshd in a session of its OWN — so Socket() would resolve to a path no fleet
// has ever bound, and a remote cockpit would attach to a daemon it just started
// rather than to the fleet the person actually has running on that machine.
//
// Newest first because that is the fleet most likely meant: the sockets are
// per login session, and the one bound most recently is the session still in
// use. The caller filters the list for one that answers.
func Sockets() []string {
	matches, err := filepath.Glob(filepath.Join(runtimeDir(), "baton-*.sock"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	type entry struct {
		path string
		mod  int64
	}
	entries := make([]entry, 0, len(matches))
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		entries = append(entries, entry{m, fi.ModTime().UnixNano()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod > entries[j].mod })
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.path
	}
	return out
}

// PidFile returns the daemon PID file that pairs with the given socket. It is
// derived from the socket path so the daemon child — which re-sessions itself
// and cannot recompute Socket() — resolves the same path from BATON_SOCK.
func PidFile(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".pid"
}

// LockFile returns the daemon's session lock that pairs with the given socket.
// Like PidFile it is derived from the socket path, so the daemon child resolves
// the same path from BATON_SOCK after it re-sessions itself.
//
// It is a lock only — never read, never removed. The PID file cannot serve the
// purpose because starting a daemon unlinks a stale one, and unlinking the file
// a lock is held on lets the next process lock a fresh inode and believe it won.
func LockFile(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".lock"
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

// UsageLimitsFile is where the status-line sink drops the account's latest
// rate-limit reading ($HOME/.baton/usage-limits.json), and where the daemon picks
// it up.
//
// It is deliberately not scoped to a socket, unlike the state and queue files.
// The rate limits belong to the account, not to a fleet: two baton sessions on
// the same machine are measured against one quota, and a reading either of them
// harvests is equally true for the other. Scoping it per session would have each
// fleet blind to what the other had already learned.
//
// A file rather than a socket message because the writer is a status line. Claude
// Code re-runs it on every render — several times a second while an agent works —
// and a control-socket handshake at that rate would cost more than the reading is
// worth. A small atomic write costs nothing and survives a daemon restart.
func UsageLimitsFile() string {
	return filepath.Join(home(), ".baton", "usage-limits.json")
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

// Expand resolves a hand-written path from the config file to an absolute one:
// "~" and "~/…" expand to the home directory, and a relative path resolves
// against the process's working directory. An empty (or blank) value stays
// empty, since every caller reads that as "unset" rather than as a directory.
//
// It lives here rather than beside each reader because the daemon and the
// cockpit both read the same file and must land on the same directory: a log
// written to ~/.baton/logs and one read from ./~/.baton/logs are not a feature.
func Expand(p string) string {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return ""
	case p == "~":
		return home()
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home(), p[2:])
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
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
