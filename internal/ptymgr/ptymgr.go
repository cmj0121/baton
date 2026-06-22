// Package ptymgr is the PTY MANAGER: it spawns and owns the real processes that
// sit behind each panel, streams their output, and forwards input back to them.
package ptymgr

import (
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

// DefaultRingCap is how much recent output is kept per panel for replay on
// attach when none is configured. It seeds the scrollback a frontend can page
// through, so it is generous; override it with WithRingCap.
const DefaultRingCap = 256 * 1024

// minRingCap floors the ring so replay and the Monitor's attention tail (which
// reads the last attnTailBytes) always have something to work with.
const minRingCap = 4 * 1024

// pane is one PTY plus a ring buffer of its recent output. After the process
// exits the pane is kept (dead) so its final output can still be replayed; it is
// freed only when the panel is closed or purged.
type pane struct {
	f    *os.File
	pid  int // child process id; with pty.Start it leads its own process group
	ring []byte
	dead bool // process exited: f is closed, ring retained for replay
}

// Manager tracks the live PTYs keyed by panel id and fans their output out
// through a single sink. ringCap is the per-panel replay buffer size, fixed at
// construction.
type Manager struct {
	mu       sync.Mutex
	ptys     map[string]*pane
	ringCap  int
	onOutput func(id string, data []byte)
	onClose  func(id string, exitCode int)
}

// Option tunes a Manager at construction.
type Option func(*Manager)

// WithRingCap sets the per-panel replay buffer to bytes (floored at minRingCap),
// the recent output replayed on attach to seed a frontend's scrollback.
func WithRingCap(bytes int) Option {
	return func(m *Manager) { m.ringCap = bytes }
}

// SetRingCap changes the per-panel replay buffer size for output kept from here
// on. A value at or below zero resets it to the built-in default; anything below
// minRingCap is floored. Existing rings are trimmed to the new cap on their next
// write, so a change takes hold under a running fleet without touching any live
// process — the hot-reload path. Safe for concurrent use.
func (m *Manager) SetRingCap(bytes int) {
	switch {
	case bytes <= 0:
		bytes = DefaultRingCap
	case bytes < minRingCap:
		bytes = minRingCap
	}
	m.mu.Lock()
	m.ringCap = bytes
	m.mu.Unlock()
}

// New returns an empty manager. Without options the replay ring is DefaultRingCap.
func New(opts ...Option) *Manager {
	m := &Manager{ptys: make(map[string]*pane), ringCap: DefaultRingCap}
	for _, opt := range opts {
		opt(m)
	}
	if m.ringCap < minRingCap {
		m.ringCap = minRingCap
	}
	return m
}

// OnOutput registers the sink that receives every panel's output. Set it once,
// before any panels are started.
func (m *Manager) OnOutput(fn func(id string, data []byte)) { m.onOutput = fn }

// OnClose registers a callback fired when a panel's process exits on its own. It
// carries the process exit code: 0 on a clean exit, the status code on a non-zero
// exit, and -1 when the wait failed for a reason other than an exit status.
func (m *Manager) OnClose(fn func(id string, exitCode int)) { m.onClose = fn }

// Spec describes the process behind a panel: the binary, its arguments, and the
// directory it runs in. The zero value (empty Command) launches the user's shell.
type Spec struct {
	Command string   // binary path; empty = the user's shell ($SHELL, then /bin/sh)
	Args    []string // arguments, e.g. an agent profile's flags
	Dir     string   // working directory; empty falls back to the user's home
}

// PanelDir is the directory a panel runs in: the requested dir, or the user's
// home when none is given. A panel never inherits the daemon's own working
// directory (where baton happened to be launched). It is exported so a caller
// that needs the same effective workdir before a spawn (e.g. the diff pop-up
// resolving an agent's git tree) resolves it identically to StartCmd.
func PanelDir(dir string) string {
	if dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return ""
}

// Start launches command (a binary path) under a new PTY for the given panel id.
// An empty command falls back to the user's shell. It is the simple shell-panel
// entry; StartCmd carries arguments and a working directory for agent panels.
func (m *Manager) Start(id, command string) error {
	return m.StartCmd(id, Spec{Command: command})
}

// StartCmd launches spec under a new PTY for the given panel id. Output is
// streamed to the sink and kept in a small ring for replay; when the process
// exits its PTY is reaped but the ring is retained, so the final result can
// still be shown until the panel is closed or purged.
func (m *Manager) StartCmd(id string, spec Spec) error {
	command := spec.Command
	if command == "" {
		command = DefaultShell()
	}

	cmd := exec.Command(command, spec.Args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.Dir = PanelDir(spec.Dir) // empty → home, never the daemon's cwd

	f, err := pty.Start(cmd)
	if err != nil {
		return err
	}

	p := &pane{f: f, pid: cmd.Process.Pid}
	m.mu.Lock()
	m.ptys[id] = p
	m.mu.Unlock()

	go m.pump(id, p, cmd)
	return nil
}

// pump streams the PTY's output to the sink and the ring until it closes.
func (m *Manager) pump(id string, p *pane, cmd *exec.Cmd) {
	buf := make([]byte, 4096)
	for {
		n, err := p.f.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			m.appendRing(p, chunk)
			if m.onOutput != nil {
				m.onOutput(id, chunk)
			}
		}
		if err != nil {
			break
		}
	}
	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1 // waited, but the failure was not an exit status
		}
	}
	m.markDead(id)
	if m.onClose != nil {
		m.onClose(id, exitCode)
	}
}

// markDead closes a panel's PTY but keeps its pane so the retained output ring
// can still be replayed. The pane is freed for real by Stop (close/purge).
func (m *Manager) markDead(id string) {
	m.mu.Lock()
	if p, ok := m.ptys[id]; ok {
		_ = p.f.Close()
		p.dead = true
	}
	m.mu.Unlock()
}

func (m *Manager) appendRing(p *pane, chunk []byte) {
	m.mu.Lock()
	p.ring = append(p.ring, chunk...)
	if len(p.ring) > m.ringCap {
		p.ring = append([]byte(nil), p.ring[len(p.ring)-m.ringCap:]...)
	}
	m.mu.Unlock()
}

// Snapshot returns a copy of a panel's recent output, for replay when a client
// attaches. Nil for an unknown id.
func (m *Manager) Snapshot(id string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.ptys[id]; ok {
		return append([]byte(nil), p.ring...)
	}
	return nil
}

// Tail returns up to the last n bytes of a panel's retained output, the cheap
// read the Monitor uses to sniff whether a quiet panel is waiting on you. Nil for
// an unknown id; the whole ring when it holds fewer than n bytes.
func (m *Manager) Tail(id string, n int) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.ptys[id]
	if !ok {
		return nil
	}
	if n >= len(p.ring) {
		return append([]byte(nil), p.ring...)
	}
	return append([]byte(nil), p.ring[len(p.ring)-n:]...)
}

// livePane returns a panel's pane if it exists and its process is still running.
// It is the shared guard for operations that must skip unknown or exited (dead)
// panels; the PTY is touched by the caller after the lock is released.
func (m *Manager) livePane(id string) (*pane, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.ptys[id]
	return p, ok && !p.dead
}

// Write forwards input bytes to a panel's process. A no-op for an unknown or
// exited (dead) panel.
func (m *Manager) Write(id string, data []byte) {
	if p, live := m.livePane(id); live {
		_, _ = p.f.Write(data)
	}
}

// Signal delivers sig to a panel's process group, so it reaches the foreground
// job inside the PTY the way a keyboard signal would (a Ctrl-C, a kill), not only
// the shell that launched it. pty.Start makes the child a session leader, so its
// pid is the group id and the negative-pid kill hits the whole group. A no-op for
// an unknown or exited (dead) panel, or one with no recorded pid.
func (m *Manager) Signal(id string, sig syscall.Signal) {
	if p, live := m.livePane(id); live && p.pid > 0 {
		_ = syscall.Kill(-p.pid, sig)
	}
}

// KillAll delivers sig to every live panel's process group — the daemon's
// shutdown sweep, so no child process outlives baton. Like Signal it targets the
// group (negative pid), so the foreground job dies with its shell, not just the
// shell; dead (already-exited) panes are skipped. The pids are collected under
// the lock and signalled after releasing it, so a slow kill never blocks the
// manager. Returns how many process groups were signalled.
func (m *Manager) KillAll(sig syscall.Signal) int {
	m.mu.Lock()
	pids := make([]int, 0, len(m.ptys))
	for _, p := range m.ptys {
		if !p.dead && p.pid > 0 {
			pids = append(pids, p.pid)
		}
	}
	m.mu.Unlock()
	for _, pid := range pids {
		_ = syscall.Kill(-pid, sig)
	}
	return len(pids)
}

// Resize sets a panel's window size (in cells). A no-op for an unknown or exited
// (dead) panel.
func (m *Manager) Resize(id string, rows, cols int) {
	if p, live := m.livePane(id); live {
		_ = pty.Setsize(p.f, &pty.Winsize{Rows: clampCell(rows), Cols: clampCell(cols)})
	}
}

// StartShell launches the user's default shell. Equivalent to Start(id, "").
func (m *Manager) StartShell(id string) error { return m.Start(id, "") }

// Stop terminates the PTY backing the given panel id, if any. Closing the
// master hangs up the child; the pump then reaps it. Safe to call for an unknown
// id (a panel with no live process).
func (m *Manager) Stop(id string) { m.remove(id) }

func (m *Manager) remove(id string) {
	m.mu.Lock()
	if p, ok := m.ptys[id]; ok {
		_ = p.f.Close()
		delete(m.ptys, id)
	}
	m.mu.Unlock()
}

// DefaultShell is the system shell: $SHELL, or /bin/sh when unset.
func DefaultShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// clampCell fits a terminal dimension into the uint16 the kernel expects.
func clampCell(n int) uint16 {
	switch {
	case n < 0:
		return 0
	case n > 0xffff:
		return 0xffff
	default:
		return uint16(n)
	}
}
