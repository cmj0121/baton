package ptymgr

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestSignalKillsProcessGroup checks Signal reaches a panel's process: a SIGKILL
// ends the shell, and the close callback fires.
func TestSignalKillsProcessGroup(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := New()
	closed := make(chan string, 1)
	m.OnClose(func(id string, _ int) { closed <- id })
	if err := m.Start("1", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop("1")

	m.Signal("1", syscall.SIGKILL)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("SIGKILL should end the panel's process")
	}
}

// TestSignalUnknownIDSafe confirms signalling an unknown or exited panel is a
// harmless no-op rather than a panic.
func TestSignalUnknownIDSafe(t *testing.T) {
	m := New()
	m.Signal("nope", syscall.SIGTERM) // must not panic
}

// TestKillAllKillsEveryLivePanel is the daemon's shutdown sweep: every live
// panel's process group gets the signal, the count reflects only the live ones,
// and each process ends.
func TestKillAllKillsEveryLivePanel(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := New()
	closed := make(chan string, 3)
	m.OnClose(func(id string, _ int) { closed <- id })
	for _, id := range []string{"1", "2", "3"} {
		if err := m.Start(id, ""); err != nil {
			t.Fatalf("Start %s: %v", id, err)
		}
		defer m.Stop(id)
	}

	if n := m.KillAll(syscall.SIGKILL); n != 3 {
		t.Fatalf("KillAll should report 3 panels killed, got %d", n)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatalf("KillAll should end every panel's process (only %d exited)", i)
		}
	}
	// A second sweep finds nothing live to kill — dead panes are skipped.
	if n := m.KillAll(syscall.SIGKILL); n != 0 {
		t.Fatalf("KillAll over already-dead panels should kill 0, got %d", n)
	}
}

// TestKillAllEmptySafe confirms the shutdown sweep over an empty manager is a
// harmless no-op.
func TestKillAllEmptySafe(t *testing.T) {
	m := New()
	if n := m.KillAll(syscall.SIGKILL); n != 0 {
		t.Fatalf("KillAll on an empty manager should kill 0, got %d", n)
	}
}

// TestStartAfterKillAllKillsItself guards the spawn-vs-shutdown race: a panel
// started after the shutdown sweep has run must not outlive the daemon. Once
// KillAll marks the manager closed, StartCmd kills the freshly forked group
// itself, so the child exits promptly rather than leaking as an orphan.
func TestStartAfterKillAllKillsItself(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := New()
	closed := make(chan string, 1)
	m.OnClose(func(id string, _ int) { closed <- id })

	m.KillAll(syscall.SIGKILL) // the sweep runs first; the manager is now closing
	if err := m.Start("late", ""); err != nil {
		t.Fatalf("Start after KillAll: %v", err)
	}
	defer m.Stop("late")

	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("a panel spawned after the shutdown sweep should be killed, not orphaned")
	}
}

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

// TestSetRingCapTrimsLiveRing checks SetRingCap shrinks the replay buffer under a
// running fleet: a process keeps streaming, and once its retained output crosses
// the new, smaller cap the snapshot is trimmed to it — the hot-reload path, no
// restart.
func TestSetRingCapTrimsLiveRing(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := New() // starts at DefaultRingCap
	if err := m.Start("1", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop("1")

	// Shrink the buffer well below the default, then drive enough output past it.
	m.SetRingCap(minRingCap)
	m.Resize("1", 24, 80)
	m.Write("1", []byte("for i in $(seq 1 5000); do echo ring-line-$i; done\n"))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n := len(m.Snapshot("1")); n > minRingCap*2 {
			t.Fatalf("ring should be trimmed to the new cap, held %d bytes", n)
		} else if strings.Contains(string(m.Snapshot("1")), "ring-line-5000") {
			return // streamed under the smaller cap, trimmed to it
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("did not see the tail output under the shrunken ring")
}

// TestSetRingCapResetsToDefault checks a non-positive cap restores the built-in
// default and a tiny cap is floored at minRingCap.
func TestSetRingCapResetsToDefault(t *testing.T) {
	m := New()
	m.SetRingCap(8)
	if m.ringCap != minRingCap {
		t.Fatalf("a tiny cap should floor at minRingCap, got %d", m.ringCap)
	}
	m.SetRingCap(0)
	if m.ringCap != DefaultRingCap {
		t.Fatalf("a zero cap should reset to DefaultRingCap, got %d", m.ringCap)
	}
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
	m.OnClose(func(id string, _ int) { closed <- id })

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
	m.OnClose(func(id string, _ int) { closed <- id })

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

// TestWriteResizeStopOnClosedFDSafe drives the error branches now that the
// manager logs instead of discarding: a pane whose master fd is already closed
// makes Write/Resize/Stop's Close fail, yet each must still return normally and
// stay a no-op for the caller (best-effort, logged-only). We register the pane on
// a live (not dead) manager so livePane lets Write/Resize through to the closed
// fd, which is exactly the failure they must swallow.
func TestWriteResizeStopOnClosedFDSafe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	_ = r.Close()
	if err := w.Close(); err != nil {
		t.Fatalf("closing write end: %v", err)
	}

	m := New()
	// A live pane backed by an already-closed file: livePane returns it (not
	// dead), so the PTY ops run and hit the closed-fd error path.
	m.ptys["x"] = &pane{f: w, pid: 0}

	m.Write("x", []byte("data")) // Write on closed fd → logged, no panic
	m.Resize("x", 24, 80)        // Setsize on closed fd → logged, no panic
	m.Stop("x")                  // remove's Close on closed fd → logged, no panic

	if _, ok := m.livePane("x"); ok {
		t.Fatal("Stop should have removed the pane even when Close failed")
	}
}

// TestMarkDeadCloseErrorSafe confirms markDead survives a Close failure: the
// pane is still flagged dead (output retained for replay) and no panic escapes.
func TestMarkDeadCloseErrorSafe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	_ = r.Close()
	if err := w.Close(); err != nil {
		t.Fatalf("closing write end: %v", err)
	}

	m := New()
	m.ptys["x"] = &pane{f: w, pid: 0}
	m.markDead("x") // Close on closed fd → logged, pane marked dead

	m.mu.Lock()
	dead := m.ptys["x"].dead
	m.mu.Unlock()
	if !dead {
		t.Fatal("markDead should flag the pane dead even when Close failed")
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

	// A custom cap above the floor exposes only the most recent bytes (the tail),
	// which is exactly what replay-on-attach hands a frontend — even though the
	// backing slice is allowed to run up to 2*cap before it is trimmed.
	const cap = 8 * 1024
	m := New(WithRingCap(cap))
	p := &pane{}
	for i := 0; i < 10; i++ {
		m.appendRing(p, make([]byte, 1000)) // 10000 bytes total, cap is 8192
	}
	if got := len(m.ringView(p)); got != cap {
		t.Fatalf("ringView should expose exactly the cap, len = %d want %d", got, cap)
	}
	// Keep writing well past 2*cap and confirm the backing slice is trimmed back.
	for i := 0; i < 10; i++ {
		m.appendRing(p, make([]byte, 1000)) // 20000 bytes total now
	}
	if len(p.ring) > 2*cap {
		t.Fatalf("backing ring should stay <= 2*cap, len = %d want <= %d", len(p.ring), 2*cap)
	}
	if got := len(m.ringView(p)); got != cap {
		t.Fatalf("ringView should still expose exactly the cap, len = %d want %d", got, cap)
	}
}

// TestPanelDir defaults an empty working directory to the user's home — never
// the daemon's own cwd — and passes a given directory through unchanged.
func TestPanelDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if got := PanelDir(""); got != home {
		t.Fatalf("empty dir should default to home %q, got %q", home, got)
	}
	if got := PanelDir("/tmp/x"); got != "/tmp/x" {
		t.Fatalf("a given dir should pass through, got %q", got)
	}
}

// TestAppendRingAmortizedTrim checks the replay ring exposes at most ringCap
// bytes while letting its backing slice grow to 2*ringCap before trimming, so the
// O(ringCap) trim runs at most once per ringCap bytes written rather than on
// every chunk.
func TestAppendRingAmortizedTrim(t *testing.T) {
	m := New()
	m.ringCap = 64 // white-box: small cap so the trim boundary is easy to hit
	p := &pane{}

	const writes = 1000
	for i := 0; i < writes; i++ {
		m.appendRing(p, []byte{byte('a' + i%26)})
		if len(p.ring) > 2*m.ringCap {
			t.Fatalf("write %d: backing ring grew to %d, want <= %d", i, len(p.ring), 2*m.ringCap)
		}
		if v := m.ringView(p); len(v) > m.ringCap {
			t.Fatalf("write %d: ringView exposed %d bytes, want <= %d", i, len(v), m.ringCap)
		}
	}

	// The visible window must be exactly the last ringCap bytes written.
	want := make([]byte, m.ringCap)
	for i := range want {
		idx := writes - m.ringCap + i
		want[i] = byte('a' + idx%26)
	}
	if got := m.ringView(p); string(got) != string(want) {
		t.Fatalf("ringView tail mismatch:\n got %q\nwant %q", got, want)
	}
}
