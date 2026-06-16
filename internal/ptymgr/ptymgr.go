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

// StartShell launches the user's shell under a new PTY for the given panel id.
// Output is drained (the banner-only dashboard does not render it yet) so the
// process never blocks on a full buffer. When the shell exits it is reaped and
// forgotten.
func (m *Manager) StartShell(id string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
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

func (m *Manager) remove(id string) {
	m.mu.Lock()
	if f, ok := m.ptys[id]; ok {
		_ = f.Close()
		delete(m.ptys, id)
	}
	m.mu.Unlock()
}
