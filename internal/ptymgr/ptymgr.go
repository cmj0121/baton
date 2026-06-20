// Package ptymgr is the PTY MANAGER: it spawns and owns the real processes that
// sit behind each panel, streams their output, and forwards input back to them.
package ptymgr

import (
	"os"
	"os/exec"
	"sync"

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
	onClose  func(id string)
}

// Option tunes a Manager at construction.
type Option func(*Manager)

// WithRingCap sets the per-panel replay buffer to bytes (floored at minRingCap),
// the recent output replayed on attach to seed a frontend's scrollback.
func WithRingCap(bytes int) Option {
	return func(m *Manager) { m.ringCap = bytes }
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

// OnClose registers a callback fired when a panel's process exits on its own.
func (m *Manager) OnClose(fn func(id string)) { m.onClose = fn }

// Spec describes the process behind a panel: the binary, its arguments, and the
// directory it runs in. The zero value (empty Command) launches the user's shell
// in the inherited directory — the plain shell-panel case.
type Spec struct {
	Command string   // binary path; empty = the user's shell ($SHELL, then /bin/sh)
	Args    []string // arguments, e.g. an agent profile's flags
	Dir     string   // working directory; empty inherits the server's
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
	cmd.Dir = spec.Dir // empty inherits the server's working directory

	f, err := pty.Start(cmd)
	if err != nil {
		return err
	}

	p := &pane{f: f}
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
	_ = cmd.Wait()
	m.markDead(id)
	if m.onClose != nil {
		m.onClose(id)
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
