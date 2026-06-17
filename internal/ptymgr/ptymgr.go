// Package ptymgr is the PTY MANAGER: it spawns and owns the real processes that
// sit behind each panel, streams their output, and forwards input back to them.
package ptymgr

import (
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// ringCap is how much recent output is kept per panel for replay on attach.
const ringCap = 32 * 1024

// pane is one live PTY plus a ring buffer of its recent output.
type pane struct {
	f    *os.File
	ring []byte
}

// Manager tracks the live PTYs keyed by panel id and fans their output out
// through a single sink.
type Manager struct {
	mu       sync.Mutex
	ptys     map[string]*pane
	onOutput func(id string, data []byte)
}

// New returns an empty manager.
func New() *Manager {
	return &Manager{ptys: make(map[string]*pane)}
}

// OnOutput registers the sink that receives every panel's output. Set it once,
// before any panels are started.
func (m *Manager) OnOutput(fn func(id string, data []byte)) { m.onOutput = fn }

// Start launches command (a binary path) under a new PTY for the given panel id.
// An empty command falls back to the user's shell ($SHELL, then /bin/sh). Output
// is streamed to the sink and kept in a small ring for replay; when the process
// exits it is reaped and forgotten.
func (m *Manager) Start(id, command string) error {
	if command == "" {
		command = DefaultShell()
	}

	cmd := exec.Command(command)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

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
	m.remove(id)
}

func (m *Manager) appendRing(p *pane, chunk []byte) {
	m.mu.Lock()
	p.ring = append(p.ring, chunk...)
	if len(p.ring) > ringCap {
		p.ring = append([]byte(nil), p.ring[len(p.ring)-ringCap:]...)
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

// Write forwards input bytes to a panel's process. A no-op for an unknown id.
func (m *Manager) Write(id string, data []byte) {
	m.mu.Lock()
	p, ok := m.ptys[id]
	m.mu.Unlock()
	if ok {
		_, _ = p.f.Write(data)
	}
}

// Resize sets a panel's window size (in cells). A no-op for an unknown id.
func (m *Manager) Resize(id string, rows, cols int) {
	m.mu.Lock()
	p, ok := m.ptys[id]
	m.mu.Unlock()
	if ok {
		_ = pty.Setsize(p.f, &pty.Winsize{Rows: clampCell(rows), Cols: clampCell(cols)})
	}
}

// StartShell launches the user's default shell. Equivalent to Start(id, "").
func (m *Manager) StartShell(id string) error { return m.Start(id, "") }

// Stop terminates the PTY backing the given panel id, if any. Closing the
// master hangs up the child; the pump then reaps it. Safe to call for an unknown
// id (e.g. a mock panel with no real process).
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
