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
	return filepath.Join(home(), ".baton", "baton.log")
}

// ConfigFile is the user's persistent client configuration ($HOME/.baton/config,
// YAML). It holds settings such as the key-binding overrides.
func ConfigFile() string {
	return filepath.Join(home(), ".baton", "config")
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
