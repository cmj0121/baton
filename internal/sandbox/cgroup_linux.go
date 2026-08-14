//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// cgroupMount is where the unified (v2) hierarchy lives on every current distro.
const cgroupMount = "/sys/fs/cgroup"

// batonSlice is the cgroup baton creates for itself under whichever ancestor
// turns out to be delegated; panel cgroups are its children, and the daemon's own
// leaf (daemonLeaf) sits beside them when a migration was needed.
const (
	batonSlice = "baton.slice"
	daemonLeaf = "daemon"
)

// wantControllers are the controllers a panel cgroup needs. They must be enabled
// in every ancestor's subtree_control before a leaf can use them.
var wantControllers = []string{"cpu", "memory", "pids"}

// detectRoot finds — and prepares — the directory panel cgroups are created
// under, or explains why this host cannot enforce.
//
// What makes this more than a mkdir is cgroup v2's "no internal processes" rule:
// a cgroup may hold processes OR enable controllers for its children, never both.
// So baton.slice cannot go just anywhere — it has to hang off an ancestor that is
// both delegated to this user and free of processes. On a systemd user session
// that is the delegated user@<uid>.service, whose children are all slices; the
// daemon's own scope, further down, holds the daemon and is therefore skipped.
//
// detectRoot walks up from the daemon's own cgroup taking the first ancestor that
// works, so it lands as deep as the host allows rather than assuming a layout.
func detectRoot() (string, error) {
	if _, err := os.Stat(filepath.Join(cgroupMount, "cgroup.controllers")); err != nil {
		return "", fmt.Errorf("no cgroup v2 at %s", cgroupMount)
	}
	own, err := ownCgroup()
	if err != nil {
		return "", err
	}

	var errs []error
	for dir := filepath.Join(cgroupMount, own); ; dir = filepath.Dir(dir) {
		root, err := prepareUnder(dir)
		if err == nil {
			return root, nil
		}
		errs = append(errs, err)
		if dir == cgroupMount {
			break
		}
	}
	return "", fmt.Errorf("no usable cgroup subtree: %w", errors.Join(errs...))
}

// prepareUnder tries to make dir the parent of baton's own slice: it must be
// delegated (we can create a child there) and free of processes (or freeable —
// see below) so it can enable the controllers the panels need.
//
// When dir holds processes, the one case worth rescuing is dir holding only US:
// a daemon started directly in a delegated cgroup, which is what a container
// looks like. Moving the daemon down into its own leaf empties dir and the rest
// proceeds. If anything else lives there, dir belongs to someone else and the
// walk moves on to its parent.
func prepareUnder(dir string) (string, error) {
	root := filepath.Join(dir, batonSlice)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("%s is not delegated: %w", dir, err)
	}
	if hasProcs(dir) {
		if err := migrateSelf(root); err != nil {
			return "", fmt.Errorf("%s holds processes and the daemon could not move out: %w", dir, err)
		}
		if hasProcs(dir) {
			// The daemon now lives in root's daemon leaf, so root cannot be removed
			// on the way out — leaving it is the price of having tried.
			return "", fmt.Errorf("%s holds other processes", dir)
		}
	}
	// The panels need the controllers enabled at both levels: in dir, so
	// baton.slice is offered them, and in baton.slice, so its panel leaves are.
	// A level that cannot offer them is not ours to keep a cgroup at, so the walk
	// tidies up before moving on to the parent.
	for _, level := range []string{dir, root} {
		if err := enableControllers(level); err != nil {
			_ = os.Remove(root)
			return "", err
		}
	}
	return root, nil
}

// ownCgroup reads the daemon's own cgroup path from /proc/self/cgroup, whose v2
// line is always "0::<path>" relative to the mount point.
//
// A path that already ends in baton.slice/daemon is walked back off first: that
// is where a previous detectRoot migrated this daemon to, and starting the search
// from there would nest a second baton.slice inside the first on every re-probe.
func ownCgroup() (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		return stripDaemonLeaf(strings.TrimSpace(rest)), nil
	}
	return "", fmt.Errorf("no cgroup v2 membership in /proc/self/cgroup")
}

// stripDaemonLeaf walks a cgroup path back off baton's own daemon leaf. That is
// where a previous probe migrated this daemon to, and searching from there would
// nest a second baton.slice inside the first on every re-probe.
func stripDaemonLeaf(own string) string {
	if filepath.Base(own) == daemonLeaf && filepath.Base(filepath.Dir(own)) == batonSlice {
		return filepath.Dir(filepath.Dir(own))
	}
	return own
}

// hasProcs reports whether a cgroup holds any process directly. A cgroup.procs
// that cannot be read is treated as occupied: the safe reading, since acting on
// a cgroup we cannot even inspect is how the rule gets violated.
func hasProcs(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return true
	}
	return len(strings.Fields(string(raw))) > 0
}

// migrateSelf moves the daemon into its own leaf beside the panel cgroups.
// Writing "0" to cgroup.procs moves the writing process; its threads follow, and
// its existing children stay where they are — which is fine, since this runs
// before any panel exists.
func migrateSelf(root string) error {
	leaf := filepath.Join(root, daemonLeaf)
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte("0"), 0o644)
}

// enableControllers turns on the controllers a panel cgroup needs in dir, asking
// for each one separately so a host that delegates only some still gets those.
// It fails only when none could be enabled, which is the case where claiming to
// enforce would be a lie.
func enableControllers(dir string) error {
	path := filepath.Join(dir, "cgroup.subtree_control")
	available, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Join(dir, "cgroup.controllers"), err)
	}
	// cgroup.controllers is a space-separated list, matched field by field: a
	// substring test would read "cpuset" as offering "cpu".
	offered := strings.Fields(string(available))
	enabled := strings.Fields(readFile(path))

	got := 0
	for _, c := range wantControllers {
		if slices.Contains(enabled, c) {
			got++ // an ancestor already enabled it for us; nothing to write
			continue
		}
		if !slices.Contains(offered, c) {
			continue
		}
		if err := os.WriteFile(path, []byte("+"+c), 0o644); err == nil {
			got++
		}
	}
	if got == 0 {
		return fmt.Errorf("no cpu/memory/pids controller usable at %s", dir)
	}
	return nil
}

// readFile is a best-effort read for the cgroup pseudo-files whose absence is not
// interesting: an unreadable one reads as empty.
func readFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// open takes an O_PATH descriptor on the panel's cgroup directory. That
// descriptor is the whole point of the handle: passed to clone3 as
// CLONE_INTO_CGROUP, it makes the child's very first instruction run inside the
// cgroup, so there is no window in which it could fork a descendant that escapes
// the cap.
func (h *Handle) open() error {
	fd, err := unix.Open(h.dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open cgroup %s: %w", h.dir, err)
	}
	h.fd = fd
	return nil
}

// closeFD drops the descriptor, leaving the directory. Caller holds h.mu.
func (h *Handle) closeFD() error {
	if h.fd < 0 {
		return nil
	}
	err := unix.Close(h.fd)
	h.fd = -1
	return err
}

// Confine places the child being started into this panel's cgroup, by asking
// clone3 to put it there at birth. It is handed to ptymgr as Spec.Confine, which
// is what keeps ptymgr free of any policy of its own.
func (h *Handle) Confine(cmd *exec.Cmd) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fd < 0 {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD, cmd.SysProcAttr.CgroupFD = true, h.fd
	return nil
}
