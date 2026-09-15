package ptymgr

import (
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestPanelIsBornWithAUsableSize pins the size a panel's PTY has BEFORE anyone
// attaches to it.
//
// pty.Start sets no winsize, so the kernel's default for a fresh pty is 0x0 —
// and a panel keeps that until a cockpit attaches and sends panel.resize. A
// shell never notices. A full-screen program does: it asks the terminal how big
// it is, is told nothing, and draws a zero-row screen. The score editor (#93)
// hits this every time because it execs the editor immediately, where the git
// menu's commit spends `git add -A && git commit` first and usually loses the
// race to the resize by accident rather than by design.
//
// The number does not matter and is not asserted; that it is not zero does.
func TestPanelIsBornWithAUsableSize(t *testing.T) {
	m := New()
	t.Cleanup(func() { m.KillAll(syscall.SIGKILL) })

	var mu sync.Mutex
	var got strings.Builder
	m.OnOutput(func(_ string, b []byte) {
		mu.Lock()
		defer mu.Unlock()
		got.Write(b)
	})
	// stty reads the terminal on stdin, which IS the pty, before anything could
	// have resized it.
	if err := m.StartCmd("p1", Spec{Command: "sh", Args: []string{"-c", "stty size"}}); err != nil {
		t.Fatalf("StartCmd: %v", err)
	}

	read := func() string {
		mu.Lock()
		defer mu.Unlock()
		return got.String()
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(read(), "\n") {
		time.Sleep(20 * time.Millisecond)
	}
	out := strings.TrimSpace(read())
	rows, cols, ok := strings.Cut(strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]), " ")
	if !ok {
		t.Fatalf("stty size said %q, which is not a size", out)
	}
	if rows == "0" || cols == "0" {
		t.Errorf("a panel is born into a %s x %s terminal; a full-screen program draws nothing in it", rows, cols)
	}
}
