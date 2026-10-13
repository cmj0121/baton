package server

import (
	"strconv"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// TestPeersFlagPrunedOnUnwindAndCap covers the two paths that take a panel out
// of the fleet without a command a test client can send: the unwind of a spawn
// whose fork failed, and the cap on retained exited panels. Either can remove
// a group's last member, and the peers flag must go with it exactly as it does
// on a close.
func TestPeersFlagPrunedOnUnwindAndCap(t *testing.T) {
	t.Run("withdrawn spawn", func(t *testing.T) {
		s, _, _ := gateServer(
			panel.Panel{ID: "1", Group: "work"},
			panel.Panel{ID: "2", Group: "kept"},
		)
		s.groupPeers = map[string]bool{"work": true, "kept": true}

		s.withdrawPanel("1")
		if s.groupPeers["work"] {
			t.Fatalf("withdrawing a group's last panel must drop its peers flag")
		}
		// The other half: a prune that dropped everything would pass the line above.
		if !s.groupPeers["kept"] {
			t.Fatalf("a group that still has a member must keep its peers flag")
		}
	})

	t.Run("exited cap", func(t *testing.T) {
		// The oldest exited panel is the only member of "work"; enough newer
		// exited ones follow it to push exactly that one over the cap.
		panels := []panel.Panel{{ID: "old", Group: "work", State: panel.Exited}}
		for i := range maxExitedPanels {
			panels = append(panels, panel.Panel{ID: strconv.Itoa(i), Group: "kept", State: panel.Exited})
		}
		s, _, _ := gateServer(panels...)
		s.groupPeers = map[string]bool{"work": true, "kept": true}

		s.mu.Lock()
		stop := s.pruneExitedLocked()
		s.mu.Unlock()
		if len(stop) != 1 || stop[0] != "old" {
			t.Fatalf("setup: the cap should have dropped only the oldest panel, dropped %v", stop)
		}
		if s.groupPeers["work"] {
			t.Fatalf("the cap removing a group's last panel must drop its peers flag")
		}
		if !s.groupPeers["kept"] {
			t.Fatalf("a group that still has a member must keep its peers flag")
		}
	})
}
