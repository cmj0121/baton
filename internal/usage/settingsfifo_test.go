package usage

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestAFifoSettingsFileDoesNotStallTheSpawn is the same peer-chosen path
// TestOversizedSettingsAreNotRead covers, with the other way to abuse it. Size was
// bounded; the KIND of file was not, and open(2) on a FIFO with no writer does not
// return. StatusLine runs inside panel.create, so a directory carrying one parks
// the handler goroutine of the connection that asked for the spawn — for good, not
// for a while.
//
// The second file is what makes this an assertion rather than a smoke test: the
// FIFO has to be SKIPPED, so the user's own status line is what comes back.
func TestAFifoSettingsFileDoesNotStallTheSpawn(t *testing.T) {
	cfg := claudeHome(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, ".claude", "settings.json"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	writeSettings(t, filepath.Join(cfg, "settings.json"),
		`{"statusLine":{"type":"command","command":"user-line"}}`)

	type result struct {
		cmd        string
		configured bool
	}
	got := make(chan result, 1)
	go func() {
		cmd, configured := StatusLine(dir)
		got <- result{cmd, configured}
	}()
	select {
	case r := <-got:
		if !r.configured || r.cmd != "user-line" {
			t.Fatalf("StatusLine = (%q, %v), want the FIFO skipped and the user's line used", r.cmd, r.configured)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StatusLine did not return: the spawn is blocked on a FIFO in the peer's directory")
	}
}
