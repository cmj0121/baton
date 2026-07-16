package ptymgr

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPumpAnswersDeviceAttributes is the end-to-end regression guard for the DA
// responder: a program that writes a DA1 query to its PTY must read the terminal's
// reply back on its own stdin, exactly as it would under a real terminal. Without
// this, a full-screen program (nvim) blocks on the handshake reply — the concrete bug
// being Ctrl-Z suspend never taking effect. The client strips DA queries, so pump is
// the only place left to answer them.
//
// The probe puts its tty in raw mode (no line buffering / echo) so the un-terminated
// reply is readable, and select-times-out so a broken responder fails fast, never hangs.
func TestPumpAnswersDeviceAttributes(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	const prog = `
import sys, os, tty, termios, select
fd = 0
old = termios.tcgetattr(fd)
tty.setraw(fd)
sys.stdout.write("\x1b[c"); sys.stdout.flush()      # DA1 query
r, _, _ = select.select([fd], [], [], 2.0)          # wait for the terminal's reply
data = os.read(fd, 64) if r else b""
termios.tcsetattr(fd, termios.TCSADRAIN, old)
sys.stdout.write("REPLY:" + data.hex() + ":END\r\n"); sys.stdout.flush()
`
	var mu sync.Mutex
	var out strings.Builder
	m := New()
	m.OnOutput(func(_ string, d []byte) { mu.Lock(); out.Write(d); mu.Unlock() })
	if err := m.StartCmd("p", Spec{Command: "python3", Args: []string{"-c", prog}}); err != nil {
		t.Skipf("python3 start: %v", err)
	}
	defer m.Stop("p")

	deadline := time.Now().Add(4 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		mu.Lock()
		got = out.String()
		mu.Unlock()
		if strings.Contains(got, ":END") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// hex of the DA1 reply the responder writes: ESC [ ? 6 2 ; 1 ; 6 ; 2 2 c
	const wantHex = "1b5b3f36323b313b363b323263"
	if !strings.Contains(got, "REPLY:"+wantHex+":END") {
		t.Fatalf("program did not receive the DA reply on stdin.\ngot: %q\nwant REPLY to contain %q", got, wantHex)
	}
}
