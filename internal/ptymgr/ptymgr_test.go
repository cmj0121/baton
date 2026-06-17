package ptymgr

import (
	"testing"
	"time"
)

func TestStartAndStopShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := New()

	if err := m.StartShell("1"); err != nil {
		t.Fatalf("StartShell: %v", err)
	}
	// Give the PTY a moment to be tracked, then stop it.
	time.Sleep(20 * time.Millisecond)
	m.Stop("1")

	// Stopping again, or stopping an unknown id, must be safe no-ops.
	m.Stop("1")
	m.Stop("does-not-exist")
}

func TestStartWithExplicitCommand(t *testing.T) {
	m := New()
	if err := m.Start("c", "/bin/sh"); err != nil {
		t.Fatalf("Start with explicit command: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	m.Stop("c")
}

func TestDefaultShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	if DefaultShell() != "/bin/zsh" {
		t.Fatalf("DefaultShell() = %q, want $SHELL", DefaultShell())
	}
	t.Setenv("SHELL", "")
	if DefaultShell() != "/bin/sh" {
		t.Fatalf("DefaultShell() = %q, want /bin/sh fallback", DefaultShell())
	}
}

func TestStartShellBadShellErrors(t *testing.T) {
	t.Setenv("SHELL", "/definitely/not/a/real/shell")
	m := New()
	if err := m.StartShell("x"); err == nil {
		m.Stop("x")
		t.Fatal("StartShell with a missing shell should error")
	}
}
