// Package paths resolves where baton keeps its runtime files (the control socket
// and the background server log).
package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/host"
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

// Socket returns the control socket path. It is one fixed path per user, so a
// machine has one and only one backend and every cockpit — whichever terminal it
// is launched from — attaches to the same fleet. Override with BATON_SOCK, which
// is also how the daemon child inherits the parent's choice, since it re-sessions
// itself and is never told the path any other way.
func Socket() string {
	if v := os.Getenv(EnvSocket); v != "" {
		return v
	}
	return filepath.Join(runtimeDir(), "baton.sock")
}

// PidFile returns the daemon PID file that pairs with the given socket. It is
// derived from the socket path so the daemon child — which re-sessions itself
// and cannot recompute Socket() — resolves the same path from BATON_SOCK.
func PidFile(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".pid"
}

// LockFile returns the daemon's exclusive lock that pairs with the given socket.
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
// given socket. Like PidFile it is derived from the socket path, so the one
// daemon owns one state file and the daemon child resolves the same path from
// BATON_SOCK after it re-sessions itself.
func StateFile(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".state.json"
}

// QueueDir returns the task-backlog directory that pairs with the given socket —
// one file per queued task. Like StateFile it is derived from the socket path, so
// the one daemon owns one backlog and the daemon child resolves the same path
// from BATON_SOCK after it re-sessions itself.
func QueueDir(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".queue"
}

// LogFile is the default log file ($HOME/.baton/baton.log), used when --log is
// not given. One server runs per user, so it needs no per-instance suffix.
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
// The rate limits belong to the account, not to a fleet: a reading harvested
// under one BATON_SOCK is equally true under another, and the file outlives the
// daemon that wrote it.
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

// ConductorWorkspace returns the conductor panel's workspace for the given
// control socket, creating it when it is not already there. The conductor agent
// runs here instead of in any source tree, so its only local surface is the baton
// control wiring the server drops in — not the user's code.
//
// It is one fixed directory per socket rather than a fresh one per open, so a
// conductor that is closed and opened again comes back to the settings it had
// collected (the permission grants an agent writes beside itself) instead of
// starting from nothing every time. Deriving the name from the socket keeps the
// promise the rest of this file makes — one daemon owns one of everything — so a
// second fleet under BATON_SOCK gets a workspace of its own rather than sharing,
// and deleting, this one's.
//
// The name is a hash of the socket path rather than the path itself, because the
// base moved: StateFile and QueueDir sit beside the socket and are unique by
// construction, while these all land in one shared temporary base, where two
// sockets named baton.sock in different directories would otherwise collide —
// exactly the shape a test that binds under t.TempDir() produces.
//
// The workspace is wiped and rebuilt when the host has rebooted since it was
// created (see clearIfRebooted); it is not removed when the conductor is closed.
func ConductorWorkspace(socket string) (string, error) {
	base := conductorBase()
	if err := ensurePrivateDir(base); err != nil {
		return "", err
	}
	ws := filepath.Join(base, conductorName(socket))
	if err := clearIfRebooted(ws); err != nil {
		return "", err
	}
	if err := os.MkdirAll(ws, 0o700); err != nil {
		return "", err
	}
	return ws, nil
}

// ConductorStampFile is the host-boot stamp that pairs with a conductor
// workspace. It sits beside the workspace rather than inside it: the workspace is
// the agent's own directory and the stamp decides whether that directory lives,
// so a conductor that rewrites its cwd cannot rewrite the record of which boot it
// belongs to.
func ConductorStampFile(workspace string) string {
	return workspace + ".boot"
}

// RemoveConductorWorkspace deletes a conductor workspace and its boot stamp. It
// is the escape hatch behind `baton ctl conductor reset`, for a workspace whose
// accumulated state has gone bad; the ordinary lifecycle never removes one.
func RemoveConductorWorkspace(workspace string) error {
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	return os.Remove(ConductorStampFile(workspace))
}

// LegacyConductorWorkspaces lists the throwaway conductor directories left behind
// by versions that made a fresh MkdirTemp workspace per open and removed it on
// close. That removal only ran if the daemon reached it, so a crash or a hard
// kill leaked one every time, and they accumulate for as long as baton has been
// installed. The current workspace is never included, whichever base it is in.
//
// It lists rather than deletes so the caller can log what it is dropping: these
// are directories the user never asked for, but they are still the user's.
func LegacyConductorWorkspaces(current string) []string {
	var found []string
	seen := map[string]bool{}
	for _, base := range []string{conductorBase(), runtimeDir()} {
		if seen[base] {
			continue
		}
		seen[base] = true
		matches, err := filepath.Glob(filepath.Join(base, "conductor-*"))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if m == current || m == ConductorStampFile(current) {
				continue
			}
			if fi, err := os.Lstat(m); err != nil || !fi.IsDir() {
				continue // a stamp file, or something that vanished under us
			}
			found = append(found, m)
		}
	}
	return found
}

// ensurePrivateDir creates a directory with owner-only permissions and refuses to
// hand it back unless it really is one: a directory, not a symlink, owned by this
// user, and unreachable by group or other. The conductor workspace used to be
// unguessable (MkdirTemp), and dropping that for a fixed path means the checks
// have to be made explicitly — under a shared /tmp the base is a name any other
// user can claim first.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Lstat, not Stat, and after MkdirAll rather than before: MkdirAll is happy to
	// find a symlink pointing at an existing directory, and that is the case worth
	// catching. Nothing has been written yet when this runs.
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink, refusing to use it", dir)
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s is reachable by group or other (%04o)", dir, fi.Mode().Perm())
	case !ownedByCaller(fi):
		return fmt.Errorf("%s is not owned by uid %d", dir, os.Getuid())
	}
	return nil
}

// conductorBase is the per-user temporary directory the conductor workspace lives
// in. It is deliberately temporary rather than beside the socket: the workspace
// holds an agent's accumulated state, and the contract is that a reboot clears
// it. $XDG_RUNTIME_DIR delivers that by itself (tmpfs, emptied on logout), which
// is why it comes first; os.TempDir() is the fallback everywhere else and needs
// the boot stamp to make the same promise.
//
// Note that both are read from the environment, so a daemon started from an ssh
// session and one started from a desktop terminal can resolve different bases and
// therefore different workspaces. That is a known and accepted limit: the socket
// is one fixed path per user, so in practice one daemon serves them all and the
// base is whatever the process that started it had.
func conductorBase() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "baton")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("baton-%d", os.Getuid()))
}

// conductorName is the workspace directory name for a socket: stable for the same
// socket, distinct for any other. Half a SHA-256 word is plenty — this separates
// a handful of fleets on one machine, it does not resist an adversary.
func conductorName(socket string) string {
	abs := socket
	if a, err := filepath.Abs(socket); err == nil {
		abs = a
	}
	sum := sha256.Sum256([]byte(abs))
	return "conductor-" + hex.EncodeToString(sum[:4])
}

// clearIfRebooted removes the workspace when the host has rebooted since the
// stamp beside it was written, and (re)writes the stamp. It is what makes "the
// conductor forgets across a reboot" true on every platform rather than only
// where the temporary base happens to be cleared for us: $XDG_RUNTIME_DIR is
// emptied on logout, but macOS keeps $TMPDIR across a reboot and sweeps
// /private/tmp on a three-day timer, so relying on the location alone would give
// three different behaviours and no way to test any of them.
//
// A host boot time we cannot read is treated as "same boot": failing to clear
// costs the user a stale workspace, while clearing on a bad reading throws away
// state they were promised would survive a restart. The comparison allows a small
// skew because the boot time is derived from the current clock on some platforms
// and drifts by a second or two when that clock is adjusted.
func clearIfRebooted(ws string) error {
	boot, err := hostBootTime()
	if err != nil {
		return nil
	}
	stamp := ConductorStampFile(ws)
	if raw, err := os.ReadFile(stamp); err == nil {
		if sec, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			if d := boot.Sub(time.Unix(sec, 0)); d < bootSkew && d > -bootSkew {
				return nil // same boot: the workspace and its memory stay
			}
		}
	}
	if err := os.RemoveAll(ws); err != nil {
		return err
	}
	return WriteFileAtomic(stamp, []byte(strconv.FormatInt(boot.Unix(), 10)+"\n"), 0o600)
}

// bootSkew is how far two readings of the host boot time may differ and still
// mean the same boot.
const bootSkew = 5 * time.Second

// hostBootTime reports when the host last booted. It is a variable so a test can
// pretend the machine rebooted without one.
var hostBootTime = func() (time.Time, error) {
	sec, err := host.BootTime()
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(int64(sec), 0), nil
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
