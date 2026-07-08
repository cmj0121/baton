package server

import (
	"fmt"
	"net"
	"path/filepath"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// TestPruneExitedCapsFleet proves exited panels cannot grow without bound: past
// maxExitedPanels the oldest dead slots are dropped (freeing their spec), while
// live panels and the newest exited slots are kept. Without the cap a long-lived
// daemon that spawns and reaps many panels grows s.panels and the persisted
// snapshot forever until a manual purge.
func TestPruneExitedCapsFleet(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	s := New(ln)

	const over = 50
	s.panels = append(s.panels, panel.Panel{ID: "live1", State: panel.Running})
	for i := 0; i < maxExitedPanels+over; i++ {
		id := fmt.Sprintf("e%d", i)
		s.panels = append(s.panels, panel.Panel{ID: id, State: panel.Exited})
		s.specs[id] = ptymgr.Spec{}
	}
	s.panels = append(s.panels, panel.Panel{ID: "live2", State: panel.Running})

	s.mu.Lock()
	stop, _ := s.pruneExitedLocked()
	s.mu.Unlock()

	if len(stop) != over {
		t.Fatalf("prune should have dropped %d exited panels, dropped %d", over, len(stop))
	}
	if len(s.panels) != maxExitedPanels+2 {
		t.Fatalf("fleet = %d panels, want %d live+capped-exited", len(s.panels), maxExitedPanels+2)
	}

	ids := map[string]panel.State{}
	for _, p := range s.panels {
		ids[p.ID] = p.State
	}
	if ids["live1"] != panel.Running || ids["live2"] != panel.Running {
		t.Fatalf("live panels must survive the prune, got %+v", ids)
	}
	if _, ok := ids["e0"]; ok {
		t.Fatalf("the oldest exited panel should have been dropped")
	}
	if _, ok := ids[fmt.Sprintf("e%d", maxExitedPanels+over-1)]; !ok {
		t.Fatalf("the newest exited panel should have been kept")
	}
	// A dropped panel's spec is freed; a kept one's is retained.
	if _, ok := s.specs["e0"]; ok {
		t.Fatalf("a pruned panel's spec should be dropped")
	}
	if _, ok := s.specs[fmt.Sprintf("e%d", maxExitedPanels+over-1)]; !ok {
		t.Fatalf("a kept panel's spec should be retained")
	}
}
