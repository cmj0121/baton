// Package ptymgr is the PTY MANAGER: it spawns and owns the real processes that
// sit behind each panel. For now it knows how to start shell panels.
package ptymgr

import (
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// Manager tracks the live PTYs keyed by panel id.
type Manager struct {
	mu   sync.Mutex
	ptys map[string]*os.File
}

// New returns an empty manager.
func New() *Manager {
	return &Manager{ptys: make(map[string]*os.File)}
}

// Start launches command (a binary path) under a new PTY for the given panel id.
// An empty command falls back to the user's shell ($SHELL, then /bin/sh). Output
// is drained (the dashboard does not render it yet) so the process never blocks
// on a full buffer. When the process exits it is reaped and forgotten.
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

	m.mu.Lock()
	m.ptys[id] = f
	m.mu.Unlock()

	go func() {
		_, _ = io.Copy(io.Discard, f)
		_ = cmd.Wait()
		m.remove(id)
	}()

	return nil
}

// StartShell launches the user's default shell. Equivalent to Start(id, "").
func (m *Manager) StartShell(id string) error { return m.Start(id, "") }

// DefaultShell is the system shell: $SHELL, or /bin/sh when unset.
func DefaultShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// Stop terminates the PTY backing the given panel id, if any. Closing the
// master hangs up the child; the drain goroutine then reaps it. Safe to call for
// an unknown id (e.g. a mock panel with no real process).
func (m *Manager) Stop(id string) { m.remove(id) }

func (m *Manager) remove(id string) {
	m.mu.Lock()
	if f, ok := m.ptys[id]; ok {
		_ = f.Close()
		delete(m.ptys, id)
	}
	m.mu.Unlock()
}
