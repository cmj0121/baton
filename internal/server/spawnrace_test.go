package server

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// spawnrace_test.go covers #102: a panel whose process exits before
// createPanel registers it was shown as alive forever.
//
// The exit was observed, delivered, and dropped — onPanelExit scans s.panels,
// did not find the id, and returned having done nothing, because both halves of
// that function are inside `if found`. The panel was then appended as Spawning
// for a process that was already dead, and nothing would ever move it: the
// Monitor's first-output wake is not coming from a dead child.
//
// It cannot be tested by racing for it. On any machine fast enough the parent
// wins, which is exactly how this survived three CI investigations that each
// concluded "flaky test". afterSpawnForTest widens the window to a certainty.

// afterSpawn installs a pause between the fork and the panel's first broadcast,
// long enough that an instantly-exiting child is certainly gone, and removes it
// when the test ends.
func afterSpawn(t *testing.T, d time.Duration) {
	t.Helper()
	afterSpawnForTest = func() { time.Sleep(d) }
	t.Cleanup(func() { afterSpawnForTest = nil })
}

// waitState polls the fleet for a panel's state, reporting what it last saw.
func waitState(t *testing.T, s *Server, id string, want panel.State) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		s.mu.Lock()
		var got panel.State
		found := false
		if i := s.indexLocked(id); i >= 0 {
			got, found = s.panels[i].State, true
		}
		s.mu.Unlock()
		if found && got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("panel %s is %v, want %v (present=%v)", id, got, want, found)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestAPanelThatExitsBeforeItIsRegisteredStillExits is the bug. The child is
// gone before createPanel's next line, and the fleet must still learn it.
//
// KindAgent rather than KindShell: a shell panel drops the args and runs the
// shell INTERACTIVELY, which never exits and prints a prompt — the first draft
// of this test did that and watched the panel go to running, proving nothing
// about the exit path.
func TestAPanelThatExitsBeforeItIsRegisteredStillExits(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	s := New(nil)
	s.pty.OnOutput(s.routeOutput)
	s.pty.OnClose(s.onPanelExit)
	t.Cleanup(func() { s.pty.KillAll(0) })

	// The exit lands while createPanel is still inside itself.
	afterSpawn(t, 300*time.Millisecond)

	id, err := s.createPanel(originOperator, proto.KindAgent, "/bin/sh", []string{"-c", "exit 0"}, t.TempDir(), "", false, false)
	if err != nil {
		t.Fatalf("createPanel: %v", err)
	}
	waitState(t, s, id, panel.Exited)
}

// TestAFailedSpawnLeavesNothingBehind is the cost of registering first, and the
// mutation that kills the test above: an unwind that forgets the spec or the
// Monitor leaves a card for a process that never existed — the same bug wearing
// the other face.
func TestAFailedSpawnLeavesNothingBehind(t *testing.T) {
	s := New(nil)
	before := len(s.panels)

	_, err := s.createPanel(originOperator, proto.KindAgent, "/nonexistent/binary", nil, t.TempDir(), "", false, false)
	if err == nil {
		t.Fatal("spawning a binary that does not exist should fail")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if got := len(s.panels); got != before {
		t.Errorf("the fleet holds %d panels after a failed spawn, want %d", got, before)
	}
	if len(s.specs) != 0 {
		t.Errorf("a failed spawn left %d spec(s) behind", len(s.specs))
	}
}

// TestTheSpawnBroadcastCarriesTheStateItReallyHas pins the second half of the
// reorder. The panel is built as Spawning before the fork; by the time the
// plugin event is emitted, an instantly-dead child may already have moved it.
// Announcing the value built earlier would tell a plugin something that stopped
// being true before the message was assembled.
func TestTheSpawnBroadcastCarriesTheStateItReallyHas(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	s := New(nil)
	s.pty.OnOutput(s.routeOutput)
	s.pty.OnClose(s.onPanelExit)
	t.Cleanup(func() { s.pty.KillAll(0) })

	seen := make(chan string, 4)
	s.SetEventSink(func(event string, fields map[string]any) {
		if event == "panel.spawn" {
			if st, ok := fields["state"].(string); ok {
				seen <- st
			}
		}
	})
	afterSpawn(t, 300*time.Millisecond)

	if _, err := s.createPanel(originOperator, proto.KindAgent, "/bin/sh", []string{"-c", "exit 0"}, t.TempDir(), "", false, false); err != nil {
		t.Fatalf("createPanel: %v", err)
	}
	select {
	case st := <-seen:
		if st == panel.Spawning.String() {
			t.Errorf("panel.spawn announced %q for a child that had already exited", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no panel.spawn event")
	}
}
