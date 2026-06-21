package ptymgr

import (
	"os"
	"path/filepath"
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

// TestStartCmdRunsArgsInDir checks StartCmd honours the working directory and the
// arguments — the agent-panel path: a command run in dir prints that dir.
func TestStartCmdRunsArgsInDir(t *testing.T) {
	dir := t.TempDir()
	m := New()

	var mu sync.Mutex
	var got []byte
	m.OnOutput(func(_ string, data []byte) {
		mu.Lock()
		got = append(got, data...)
		mu.Unlock()
	})

	// `sh -c pwd` writes the working directory it was launched in, then exits.
	if err := m.StartCmd("1", Spec{Command: "/bin/sh", Args: []string{"-c", "pwd"}, Dir: dir}); err != nil {
		t.Fatalf("StartCmd: %v", err)
	}
	defer m.Stop("1")

	// TempDir may live under a symlinked prefix (e.g. /var -> /private/var on
	// macOS), so match on the unique leaf rather than the whole path.
	leaf := filepath.Base(dir)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		seen := strings.Contains(string(got), leaf)
		mu.Unlock()
		if seen {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("output never showed the workdir leaf %q; got %q", leaf, string(got))
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

// TestRingCap covers the configurable replay buffer: the default, a custom cap
// that trims the ring to its tail, and the floor on absurdly small values.
func TestRingCap(t *testing.T) {
	if New().ringCap != DefaultRingCap {
		t.Fatalf("default ring cap = %d, want %d", New().ringCap, DefaultRingCap)
	}
	if got := New(WithRingCap(10)).ringCap; got != minRingCap {
		t.Fatalf("a tiny cap should floor at %d, got %d", minRingCap, got)
	}

	// A custom cap above the floor keeps only the most recent bytes (the tail),
	// which is exactly what replay-on-attach should hand a frontend.
	const cap = 8 * 1024
	m := New(WithRingCap(cap))
	p := &pane{}
	for i := 0; i < 10; i++ {
		m.appendRing(p, make([]byte, 1000)) // 10000 bytes total, cap is 8192
	}
	if len(p.ring) != cap {
		t.Fatalf("ring should be trimmed to the cap, len = %d want %d", len(p.ring), cap)
	}
}

// TestPanelDir defaults an empty working directory to the user's home — never
// the daemon's own cwd — and passes a given directory through unchanged.
func TestPanelDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if got := panelDir(""); got != home {
		t.Fatalf("empty dir should default to home %q, got %q", home, got)
	}
	if got := panelDir("/tmp/x"); got != "/tmp/x" {
		t.Fatalf("a given dir should pass through, got %q", got)
	}
}
