package server

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestSendSearchCapsAndDisplayTitle drives sendSearch over a panel that produced far
// more matches than the per-panel cap, alongside a ghost panel with no output. It
// exercises three branches the end-to-end search test does not: the per-panel hit
// cap (which trims and flags the result), a hit carrying a panel's DisplayTitle
// override rather than its base Title, and the empty-snapshot skip for a panel that
// has emitted nothing.
func TestSendSearchCapsAndDisplayTitle(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := New(ln)

	id, err := s.createPanel("shell", "", nil, dir, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Give the panel a display-title override so its hits report that, not the base.
	s.mu.Lock()
	if i := s.indexLocked(id); i >= 0 {
		s.panels[i].DisplayTitle = "OVERRIDE-TITLE"
	}
	// A second panel that never runs a process: its snapshot is empty, so the scan
	// must skip it via the len(raw)==0 continue.
	s.panels = append(s.panels, panel.Panel{ID: "ghost", State: panel.Running})
	s.mu.Unlock()

	// Emit well over the per-panel cap of matching lines, then wait for the ring to
	// hold them before searching.
	s.pty.Write(id, []byte("i=0; while [ $i -lt 140 ]; do echo ZMATCHLINE; i=$((i+1)); done\n"))

	deadline := time.After(15 * time.Second)
	for strings.Count(string(s.pty.Snapshot(id)), "ZMATCHLINE") <= maxHitsPerPanel+5 {
		select {
		case <-deadline:
			t.Fatalf("panel never produced enough output to hit the per-panel cap")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	cc := &clientConn{out: make(chan proto.ServerMsg, 4)}
	if err := s.sendSearch(cc, "ZMATCHLINE"); err != nil {
		t.Fatalf("sendSearch: %v", err)
	}

	var msg proto.ServerMsg
	select {
	case msg = <-cc.out:
	case <-time.After(2 * time.Second):
		t.Fatal("sendSearch did not reply")
	}
	if msg.Type != "search" {
		t.Fatalf("reply type = %q, want search", msg.Type)
	}
	if len(msg.Hits) != maxHitsPerPanel {
		t.Fatalf("hits = %d, want the per-panel cap %d (result should be trimmed)", len(msg.Hits), maxHitsPerPanel)
	}
	for _, h := range msg.Hits {
		if h.Panel != id {
			t.Fatalf("hit in unexpected panel %q, want %q", h.Panel, id)
		}
		if h.Title != "OVERRIDE-TITLE" {
			t.Fatalf("hit title = %q, want the DisplayTitle override", h.Title)
		}
	}

	// Tidy up the live process.
	_ = s.closePanel(id)
}
