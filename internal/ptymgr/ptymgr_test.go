package ptymgr

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamsOutputAndForwardsInput(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := New()

	var mu sync.Mutex
	var got []byte
	m.OnOutput(func(_ string, data []byte) {
		mu.Lock()
		got = append(got, data...)
		mu.Unlock()
	})

	if err := m.Start("1", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop("1")

	m.Resize("1", 24, 80)
	m.Write("1", []byte("echo baton-ok\n"))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		seen := strings.Contains(string(got), "baton-ok")
		mu.Unlock()
		if seen {
			if !strings.Contains(string(m.Snapshot("1")), "baton-ok") {
				t.Fatal("snapshot should hold the recent output")
			}
			return // input forwarded, output streamed and buffered
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("did not see the echoed output")
}

func TestOnCloseFiresOnExit(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := New()
	closed := make(chan string, 1)
	m.OnClose(func(id string) { closed <- id })

	if err := m.Start("1", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.Write("1", []byte("exit\n")) // make the shell exit on its own

	select {
	case id := <-closed:
		if id != "1" {
			t.Fatalf("OnClose got %q, want 1", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnClose did not fire after the shell exited")
	}
}

func TestSnapshotSurvivesExit(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := New()
	closed := make(chan string, 1)
	m.OnClose(func(id string) { closed <- id })

	if err := m.Start("1", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Print a marker, then exit; the marker must survive in the ring afterwards.
	m.Write("1", []byte("echo result-kept; exit\n"))

	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("process did not exit")
	}

	// The result is still replayable after the process is gone …
	if !strings.Contains(string(m.Snapshot("1")), "result-kept") {
		t.Fatalf("snapshot should retain output after exit, got %q", m.Snapshot("1"))
	}
	// … writes to the dead panel are safe no-ops …
	m.Write("1", []byte("ignored"))
	m.Resize("1", 10, 10)
	// … and an explicit Stop finally frees it.
	m.Stop("1")
	if m.Snapshot("1") != nil {
		t.Fatal("snapshot should be gone after Stop")
	}
}

func TestWriteResizeSnapshotUnknownIDSafe(t *testing.T) {
	m := New()
	m.Write("nope", []byte("x")) // no panic
	m.Resize("nope", 10, 10)
	if m.Snapshot("nope") != nil {
		t.Fatal("snapshot of an unknown id should be nil")
	}
}

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
