package ptymgr

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPanelInheritsNoExtraDescriptors is the descriptor half of the file-mode
// question: a mode on baton's own files is worth nothing if the agent CLI in a
// panel is handed an open descriptor to one. The daemon holds several while it
// spawns — the log it writes every line to, the score store, the control socket,
// the flock'd lock files — and a panel runs a third-party binary with the user's
// full environment.
//
// Go opens every file O_CLOEXEC and passes a child only fds 0, 1 and 2 plus the
// extra descriptors a caller asks os/exec to hand over -- which nothing in this
// tree asks for -- so the expectation is that nothing leaks. That is a claim about the runtime rather than about baton, and
// this asserts it against the real spawn path instead of trusting it: the parent
// opens a decoy, starts a panel, and the panel reports which descriptors above
// stderr it can see. Its three fds are the PTY, so anything else is a leak.
func TestPanelInheritsNoExtraDescriptors(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")

	// The decoy stands in for the daemon log and the score store: opened before the
	// spawn and deliberately left open across it, which is exactly how the daemon
	// holds its own.
	decoy := filepath.Join(t.TempDir(), "secret.log")
	f, err := os.OpenFile(decoy, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open decoy: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("a score entry the panel must not reach\n"); err != nil {
		t.Fatalf("write decoy: %v", err)
	}

	var mu sync.Mutex
	var out strings.Builder
	m := New()
	m.OnOutput(func(_ string, data []byte) {
		mu.Lock()
		out.Write(data)
		mu.Unlock()
	})

	// /dev/fd is present on both darwin and linux. Descriptors 0-2 are the PTY; the
	// loop reports anything above them, and the sentinel proves the command itself
	// ran rather than the test reading an empty buffer as success.
	script := `for fd in 3 4 5 6 7 8 9 10 11 12; do
		if [ -e /dev/fd/$fd ]; then echo "LEAKED:$fd"; fi
	done; echo "SCAN-COMPLETE"`
	if err := m.StartCmd("1", Spec{Command: "/bin/sh", Args: []string{"-c", script}}); err != nil {
		t.Fatalf("StartCmd: %v", err)
	}
	defer m.Stop("1")

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := strings.Contains(out.String(), "SCAN-COMPLETE")
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	got := out.String()
	mu.Unlock()

	if !strings.Contains(got, "SCAN-COMPLETE") {
		t.Fatalf("the panel's scan never finished; got %q", got)
	}
	if strings.Contains(got, "LEAKED:") {
		t.Errorf("a panel inherited a descriptor above stderr, so a file's mode does "+
			"not bound who reads it; scan said:\n%s", got)
	}
}
