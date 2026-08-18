package server

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/restart"
	"github.com/cmj0121/baton/internal/task"
)

// supervisorServer is a server holding one exited panel with a recorded spawn
// spec, under the given fleet-wide policy — the smallest world in which the
// restart decision is a decision at all.
func supervisorServer(policy restart.Policy) *Server {
	mo, _ := newTestMonitor()
	s := &Server{
		pty:             ptymgr.New(),
		clients:         map[*clientConn]struct{}{},
		mon:             mo,
		panels:          []panel.Panel{{ID: "p1", Kind: panel.Shell, Title: "shell #1", State: panel.Exited}},
		pendingDispatch: map[string][]byte{},
		tasks:           map[string]*task.Task{},
		panelTask:       map[string]string{},
		spawning:        map[string]bool{},
		specs:           map[string]spawnSpec{"p1": {Spec: ptymgr.Spec{Command: "/bin/sh"}}},
		restart:         policy,
	}
	return s
}

// supervise runs the decision under the lock, the way onPanelExit does, and
// disarms whatever it armed so a test never leaves a timer running.
func supervise(s *Server, exitCode int, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.superviseExitLocked("p1", exitCode, now)
	if st := s.restarts["p1"]; st != nil && st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	return out
}

// TestNoPolicyRestartsNothing: a fleet that has not asked for restarts must
// behave exactly as it did before the policy existed.
func TestNoPolicyRestartsNothing(t *testing.T) {
	s := supervisorServer(restart.Policy{})
	if got := supervise(s, 1, time.Now()); got != "" {
		t.Fatalf("an unconfigured fleet restarted a panel: %q", got)
	}
}

// TestRestartsOnlyOnAbnormalExit: a clean exit is a finished job. Restarting it
// would turn every completed agent run into a loop.
func TestRestartsOnlyOnAbnormalExit(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure})
	if got := supervise(s, 0, time.Now()); got != "" {
		t.Errorf("a clean exit was restarted: %q", got)
	}
	if got := supervise(s, 1, time.Now()); got == "" {
		t.Error("an abnormal exit should schedule a restart")
	}
}

// TestSignalledPanelIsNotRestarted: you asked it to stop. Bringing it back is the
// single most infuriating thing a supervisor can do, and the exit code cannot
// tell this apart from a crash — both arrive as -1.
func TestSignalledPanelIsNotRestarted(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure})
	now := time.Now()
	s.noteStopRequested([]string{"p1"})

	got := supervise(s, -1, now)
	if got != "exited · stopped on request" {
		t.Fatalf("activity = %q, want the deliberate-stop reason", got)
	}
	if st := s.restarts["p1"]; st.timer != nil {
		t.Error("a deliberate stop must not arm a restart")
	}
}

// TestSignalSuppressionExpires: a signal is not proof of intent to kill — SIGINT
// to an agent interrupts a task it survives. A crash long afterwards is still a
// crash, so the suppression is bounded rather than permanent.
func TestSignalSuppressionExpires(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure})
	s.noteStopRequested([]string{"p1"})
	s.mu.Lock()
	s.restarts["p1"].stopAsked = time.Now().Add(-userStopGrace - time.Second)
	s.mu.Unlock()

	if got := supervise(s, -1, time.Now()); got == "exited · stopped on request" {
		t.Fatal("a stale signal should not suppress a later crash")
	}
}

// TestShutdownDoesNotRestart: the daemon kills the whole fleet on the way down.
// Reading that as a fleet-wide crash would fight the shutdown it is part of.
func TestShutdownDoesNotRestart(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure})
	s.mu.Lock()
	s.shuttingDown = true
	s.mu.Unlock()

	if got := supervise(s, -1, time.Now()); got != "" {
		t.Fatalf("a shutdown kill was treated as a crash: %q", got)
	}
}

// TestNoSpecNoRestart: a panel with nothing recorded to re-run cannot be brought
// back, and must not pretend it is about to be.
func TestNoSpecNoRestart(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure})
	s.specs = map[string]spawnSpec{}
	if got := supervise(s, 1, time.Now()); got != "" {
		t.Fatalf("a specless panel scheduled a restart: %q", got)
	}
}

// TestBackoffGrowsAndGivingUpIsLoud: each consecutive failure waits longer, and
// the limit ends the loop with the reason on the card rather than quietly.
func TestBackoffGrowsAndGivingUpIsLoud(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure, Max: 3, Backoff: time.Second, Healthy: time.Hour})
	now := time.Now()

	want := []string{
		"exited · restarting in 1s (1/3)",
		"exited · restarting in 2s (2/3)",
		"exited · restarting in 4s (3/3)",
		"exited · restart limit reached after 3 failures",
	}
	for i, w := range want {
		if got := supervise(s, 1, now); got != w {
			t.Fatalf("attempt %d activity = %q, want %q", i+1, got, w)
		}
	}
	// Past the limit it stays given up rather than starting the sequence again.
	if got := supervise(s, 1, now); got != "exited · restart limit reached after 4 failures" {
		t.Errorf("after giving up = %q", got)
	}
}

// TestHealthyRunResetsTheCounter: a panel that has been up for a day gets the
// full budget again, rather than the tail of a crash loop from last week.
func TestHealthyRunResetsTheCounter(t *testing.T) {
	policy := restart.Policy{Mode: restart.OnFailure, Max: 2, Backoff: time.Second, Healthy: 30 * time.Second}
	s := supervisorServer(policy)
	now := time.Now()

	supervise(s, 1, now) // failure 1
	supervise(s, 1, now) // failure 2 — the budget is spent

	// A run that lasted past the healthy mark clears the count.
	s.mu.Lock()
	s.restarts["p1"].startedAt = now.Add(-time.Minute)
	s.mu.Unlock()
	if got := supervise(s, 1, now); got != "exited · restarting in 1s (1/2)" {
		t.Fatalf("a healthy run did not reset the counter: %q", got)
	}
}

// TestCleanExitClearsFailures: a process that finally exits cleanly has stopped
// failing, so the next crash starts from the beginning.
func TestCleanExitClearsFailures(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure, Max: 3, Backoff: time.Second, Healthy: time.Hour})
	now := time.Now()
	supervise(s, 1, now)
	supervise(s, 1, now)
	supervise(s, 0, now) // a clean exit

	if got := supervise(s, 1, now); got != "exited · restarting in 1s (1/3)" {
		t.Fatalf("a clean exit did not clear the failure count: %q", got)
	}
}

// TestPerProfilePolicyWins: "if this one dies I want to look at it myself" is the
// whole point of the per-agent override.
func TestPerProfilePolicyWins(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure, Max: 3, Backoff: time.Second})
	s.agentRestart = map[string]restart.Policy{"claude": {Mode: restart.Never}}
	s.specs = map[string]spawnSpec{"p1": {Spec: ptymgr.Spec{Command: "claude"}, Profile: "claude"}}

	if got := supervise(s, 1, time.Now()); got != "" {
		t.Fatalf("a profile that opted out was restarted: %q", got)
	}

	// A profile with no policy of its own still inherits the fleet's.
	s.specs = map[string]spawnSpec{"p1": {Spec: ptymgr.Spec{Command: "codex"}, Profile: "codex"}}
	if got := supervise(s, 1, time.Now()); got == "" {
		t.Error("a profile with no override should inherit the fleet policy")
	}
}

// TestForgetRestartDisarms: a panel that is closed must not come back, even if a
// restart was already ticking down for it.
func TestForgetRestartDisarms(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure, Max: 3, Backoff: time.Hour})
	s.mu.Lock()
	s.superviseExitLocked("p1", 1, time.Now())
	armed := s.restarts["p1"].timer != nil
	s.forgetRestartLocked("p1")
	_, still := s.restarts["p1"]
	s.mu.Unlock()

	if !armed {
		t.Fatal("the test needs an armed timer to prove it is disarmed")
	}
	if still {
		t.Error("closing a panel should drop its supervision state")
	}
}

// TestNoteSpawnDisarmsAndStartsTheClock: re-running a panel by hand while a
// restart is pending must not leave the timer to fire a second process.
func TestNoteSpawnDisarmsAndStartsTheClock(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure, Max: 3, Backoff: time.Hour})
	s.mu.Lock()
	s.superviseExitLocked("p1", 1, time.Now())
	now := time.Now()
	s.noteSpawnLocked("p1", now)
	st := s.restarts["p1"]
	s.mu.Unlock()

	if st.timer != nil {
		t.Error("a spawn should disarm the pending restart")
	}
	if !st.startedAt.Equal(now) {
		t.Errorf("startedAt = %v, want the spawn time %v", st.startedAt, now)
	}
}

// TestEffectiveRestartFillsDefaults: an operator who writes only `restart:
// on-failure` still gets a bounded, backed-off supervisor.
func TestEffectiveRestartFillsDefaults(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure})
	s.mu.Lock()
	got := s.effectiveRestartLocked("")
	s.mu.Unlock()

	if got.Max != restart.DefaultMax || got.Backoff != restart.DefaultBackoff || got.Healthy != restart.DefaultHealthy {
		t.Fatalf("policy = %+v, want the defaults filled in", got)
	}
}
