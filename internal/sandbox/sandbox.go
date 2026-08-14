// Package sandbox enforces the resource limits a panel runs under. It is the
// backend behind limits.Limits: the config layer decides WHAT the caps are, this
// one makes the kernel hold a panel to them.
//
// The only backend today is cgroup v2, which is Linux-only and needs a delegated
// subtree (the usual systemd user session provides one). Everywhere else — macOS,
// a Linux without delegation — the manager reports ModeNone with the reason why,
// and every panel runs uncapped. That degradation is deliberately loud: a
// silently ignored limit is worse than no limit, because it reads as protection
// that is not there.
//
// cgroups are the backend rather than setrlimit because the unit of enforcement
// has to be the whole process tree. An agent forks node, git, and test runners;
// an rlimit is per-process and inherited, so a 4Gi rlimit means every descendant
// may take 4Gi. A cgroup caps the tree as one.
package sandbox

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/cmj0121/baton/internal/limits"
)

// Mode is which enforcement backend is actually in force.
type Mode string

// The enforcement modes.
const (
	ModeNone   Mode = "none"   // nothing is enforced; Reason says why
	ModeCgroup Mode = "cgroup" // cgroup v2, capping each panel's whole process tree
)

// cpuPeriod is the cpu.max accounting window in microseconds. It is the kernel's
// own default; the quota is the core allowance times this, so "2" cores becomes
// "200000 100000" — two full periods of CPU per period of wall clock.
const cpuPeriod = 100000

// Manager owns the enforcement backend for a daemon: the mode in force and, on a
// working cgroup setup, the parent cgroup each panel's own is created under. The
// zero value is unusable — build one with New.
type Manager struct {
	mu      sync.Mutex
	mode    Mode
	root    string // parent directory panel cgroups are created under (cgroup mode)
	reason  string // why the mode is none; empty when enforcing
	probed  bool   // the host probe has run
	handles map[string]*Handle
}

// New returns an unprobed manager. It touches nothing: probing the host is not
// free — it creates cgroups and moves the daemon process into one — and a baton
// with no caps configured has no business doing either. Probe is what commits to
// that, and the daemon calls it only once caps exist.
func New() *Manager {
	return &Manager{mode: ModeNone, reason: "no limits configured", handles: make(map[string]*Handle)}
}

// Probe looks for a usable backend, at most once per daemon. The caller invokes
// it when a policy exists — at startup if the config carries caps, and on the
// reload that first introduces them — so the filesystem work and the daemon's own
// cgroup migration happen only for a fleet that is actually going to be capped.
//
// It never fails: a host that cannot enforce leaves the manager at ModeNone with
// a Reason explaining what was missing.
func (m *Manager) Probe() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.probed {
		return
	}
	m.probed = true
	if root, err := detectRoot(); err != nil {
		m.reason = err.Error()
	} else {
		m.mode, m.root, m.reason = ModeCgroup, root, ""
	}
}

// Mode reports the backend in force. A nil manager enforces nothing, so a caller
// that never built one (a bare test server) behaves like an unsupported host
// rather than panicking.
func (m *Manager) Mode() Mode {
	if m == nil {
		return ModeNone
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

// Reason explains why the mode is ModeNone. It is empty when enforcing, and is
// what the daemon logs at startup so a degraded host is visible rather than
// silently uncapped.
func (m *Manager) Reason() string {
	if m == nil {
		return "no enforcement backend"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reason
}

// Describe renders the mode for a LOG LINE: "cgroup", or "none (cgroup v2 is
// Linux-only)". It is prose — anything that has to act on the answer should read
// Mode and Reason instead, so a reworded message here cannot change a decision
// somewhere else.
func (m *Manager) Describe() string {
	if m == nil {
		return string(ModeNone) + " (no enforcement backend)"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reason == "" {
		return string(m.mode)
	}
	return fmt.Sprintf("%s (%s)", m.mode, m.reason)
}

// Unenforced lists the caps that are set but that this backend cannot hold a
// process to, so the caller can report them instead of letting them look
// applied. nofile is the only one: an open-file cap is per-process rlimit
// territory (RLIMIT_NOFILE), not a cgroup knob.
func (m *Manager) Unenforced(l limits.Limits) []string {
	if m.Mode() == ModeNone || l.NOFile == "" {
		return nil
	}
	return []string{"nofile"}
}

// Prepare creates the cgroup a panel will be born into and writes its caps. The
// returned handle is what the spawn applies (Confine) and what a reload updates;
// it is nil when there is nothing to enforce — no backend, or a policy that caps
// nothing — and every Handle method is safe on a nil receiver.
//
// An error here means the backend is present but refused the policy, and the
// caller should fail the spawn rather than start an uncapped process that was
// asked to be capped.
func (m *Manager) Prepare(id string, caps limits.Limits) (*Handle, error) {
	if m == nil || caps.IsZero() {
		return nil, nil
	}
	m.mu.Lock()
	mode, root := m.mode, m.root
	m.mu.Unlock()
	if mode == ModeNone {
		return nil, nil
	}
	dir := filepath.Join(root, "panel-"+id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cgroup %s: %w", dir, err)
	}
	skipped, err := writeControls(dir, caps)
	if err != nil {
		_ = os.Remove(dir)
		return nil, err
	}
	h := &Handle{dir: dir, fd: -1, skipped: skipped}
	if err := h.open(); err != nil {
		_ = os.Remove(dir)
		return nil, err
	}

	m.mu.Lock()
	if old := m.handles[id]; old != nil && old != h {
		_ = old.closeFD() // a respawn re-prepares the same panel: drop the stale fd, keep the dir
	}
	m.handles[id] = h
	m.mu.Unlock()
	return h, nil
}

// Update rewrites the caps of every live panel from resolve, which maps a panel
// id to its freshly resolved limits — the hot-reload path. It returns how many
// panels were updated and the ids whose new caps cannot take hold until they
// respawn (see Handle.Update). A ModeNone manager updates nothing.
func (m *Manager) Update(resolve func(id string) limits.Limits) (updated int, deferred []string) {
	if m == nil {
		return 0, nil
	}
	m.mu.Lock()
	live := make([]panelHandle, 0, len(m.handles))
	for id, h := range m.handles {
		live = append(live, panelHandle{id, h})
	}
	m.mu.Unlock()

	for _, p := range live {
		defers, err := p.h.Update(resolve(p.id))
		if err != nil {
			continue
		}
		updated++
		if len(defers) > 0 {
			deferred = append(deferred, p.id)
		}
	}
	return updated, deferred
}

// Release tears down a panel's cgroup once the panel is gone for good. It is a
// no-op for a panel that never had one. A cgroup with live processes cannot be
// removed, so a failed removal is not an error worth surfacing — the daemon has
// already killed the process by the time this runs, and a leftover empty
// directory costs nothing.
func (m *Manager) Release(id string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	h := m.handles[id]
	delete(m.handles, id)
	m.mu.Unlock()
	h.Close()
}

// panelHandle pairs a panel with its cgroup for the snapshot a reload walks —
// taken as a slice so the update runs outside the manager's lock without
// rebuilding a map to do it.
type panelHandle struct {
	id string
	h  *Handle
}

// Handle is one panel's cgroup: the directory holding its control files and an
// open descriptor to it, which is how the child is placed INTO the cgroup at
// fork time rather than moved in afterwards. The difference matters — a process
// moved in after the fact can fork children in the gap that escape the cap.
type Handle struct {
	mu      sync.Mutex
	dir     string
	fd      int      // O_PATH descriptor on dir, or -1
	skipped []string // caps the kernel exposed no control file for, e.g. an undelegated controller
}

// Skipped names the caps this cgroup could not take because the kernel exposed
// no control file for them — a controller the host did not delegate. They are
// reported rather than swallowed, for the same reason the mode is: a cap that
// looks applied and is not is worse than one that was never asked for.
func (h *Handle) Skipped() []string {
	if h == nil {
		return nil
	}
	return h.skipped
}

// Dir is the panel's cgroup directory, for logging and tests.
func (h *Handle) Dir() string {
	if h == nil {
		return ""
	}
	return h.dir
}

// Update rewrites the panel's caps in place, so a reload takes hold under a
// running process with nothing to restart. It returns the caps that could NOT be
// applied to the live tree and need a respawn.
//
// Only one cap is ever deferred: a memory.max being lowered. Writing a smaller
// hard limit than the tree is currently using makes the kernel reclaim against
// it immediately and kill the agent mid-task, which is a far worse outcome than
// the cap taking hold a moment later. Raising it is applied at once, and
// memory.high — which throttles rather than kills — follows the config in both
// directions, so a lowered ceiling still slows the tree down straight away.
func (h *Handle) Update(next limits.Limits) (deferred []string, err error) {
	if h == nil {
		return nil, nil
	}
	ctrls, err := controls(next) // parsed outside the lock; it guards the writes, not the reading
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range ctrls {
		if c.file == "memory.max" && lowers(h.dir, c) {
			deferred = append(deferred, "memory")
			continue
		}
		if _, err := writeControl(h.dir, c); err != nil {
			return deferred, err
		}
	}
	return deferred, nil
}

// Close releases the panel's cgroup: the descriptor first, then the directory.
// Safe on a nil handle and safe to call twice.
func (h *Handle) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = h.closeFD()
	_ = os.Remove(h.dir) // fails while processes remain; harmless, see Manager.Release
}

// control is one cgroup control file and the value to write into it. capped
// records whether the policy asked for a cap here at all, so the writers never
// have to recover that from the rendered string — "max" and "max 100000" are
// both "no cap", and a prefix test for them would be load-bearing formatting.
type control struct {
	file   string
	value  string
	capped bool
}

// controls renders a policy as the cgroup v2 control files that express it. Every
// field is rendered on every call, including the ones the policy leaves unset —
// they become "max", which is what actually lifts a cap that a previous write put
// in place. Rendering only the set fields would make an update unable to remove
// anything.
func controls(l limits.Limits) ([]control, error) {
	cores, capped, err := limits.ParseCPUs(l.CPUs)
	if err != nil {
		return nil, fmt.Errorf("cpus: %w", err)
	}
	cpu := control{"cpu.max", "max " + strconv.Itoa(cpuPeriod), capped}
	if capped {
		cpu.value = fmt.Sprintf("%d %d", int64(math.Round(cores*cpuPeriod)), cpuPeriod)
	}

	mem, err := quantityControl("memory.max", "memory", l.Memory, limits.ParseBytes)
	if err != nil {
		return nil, err
	}
	high, err := quantityControl("memory.high", "memory-high", l.MemoryHigh, limits.ParseBytes)
	if err != nil {
		return nil, err
	}
	pids, err := quantityControl("pids.max", "pids", l.Pids, limits.ParseCount)
	if err != nil {
		return nil, err
	}
	return []control{cpu, mem, high, pids}, nil
}

// quantityControl renders one whole-number control. field names the config key
// so a bad value is reported the way the user spelled it, not the way cgroup
// does; parse is whichever reader that field's units call for.
func quantityControl(file, field, value string, parse func(string) (int64, bool, error)) (control, error) {
	n, capped, err := parse(value)
	if err != nil {
		return control{}, fmt.Errorf("%s: %w", field, err)
	}
	return control{file, quantity(n, capped), capped}, nil
}

// quantity renders a cap as cgroup v2 spells it: the number, or "max" for no cap.
func quantity(n int64, capped bool) string {
	if !capped {
		return "max"
	}
	return strconv.FormatInt(n, 10)
}

// writeControls writes a whole policy into a cgroup directory, returning the
// control files the kernel did not expose. Only a cap the policy actually sets
// counts as skipped: a "max" write to a missing file was asking for nothing.
func writeControls(dir string, l limits.Limits) (skipped []string, err error) {
	ctrls, err := controls(l)
	if err != nil {
		return nil, err
	}
	for _, c := range ctrls {
		wrote, err := writeControl(dir, c)
		if err != nil {
			return skipped, err
		}
		if !wrote && c.capped {
			skipped = append(skipped, c.file)
		}
	}
	return skipped, nil
}

// writeControl writes one control file, reporting whether the file was there to
// write at all. A control the kernel does not expose — memory.max without the
// memory controller delegated, say — is skipped rather than failed: the caps that
// CAN be applied still should be, and the caller reports the rest. Opening
// directly (rather than stat-then-write) both halves the syscalls and closes the
// window in which the answer could change between the two.
func writeControl(dir string, c control) (wrote bool, err error) {
	path := filepath.Join(dir, c.file)
	f, err := os.OpenFile(path, os.O_WRONLY, 0) // never O_CREATE: cgroupfs rejects it
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	_, err = f.WriteString(c.value)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// lowers reports whether a control would move an existing numeric cap DOWN. A
// control that removes the cap never counts, and neither does an existing value
// that is not a number ("max", or a file that is not there): there is nothing
// below to fall from.
func lowers(dir string, c control) bool {
	if !c.capped {
		return false
	}
	next, err := strconv.ParseInt(c.value, 10, 64)
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(dir, c.file))
	if err != nil {
		return false
	}
	cur, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return false
	}
	return next < cur
}
