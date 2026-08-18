package server

import (
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/restart"
)

// userStopGrace is how long after a signal an exit still counts as one the user
// asked for.
//
// A signal cannot be assumed to kill: SIGINT to an agent interrupts a task the
// process survives, and a flag set forever by that would silently disable restart
// for the rest of the panel's life. Bounding it keeps the deliberate case —
// signal, process dies moments later — while letting a genuine crash an hour
// afterwards still bring the panel back.
const userStopGrace = 10 * time.Second

// restartState is one panel's supervision bookkeeping: how many consecutive
// failures it has had, when the current run began (a run that lasts long enough
// clears the count), the pending restart timer if one is armed, and when the user
// last asked it to stop.
type restartState struct {
	failures  int
	startedAt time.Time
	timer     *time.Timer
	stopAsked time.Time
}

// effectiveRestartLocked resolves the restart policy a panel of the given agent
// profile runs under: the fleet-wide policy with the profile's own layered over
// it. An empty or unknown profile — a shell panel, or an agent spawned without
// one — resolves to the fleet-wide policy alone. Caller holds s.mu.
func (s *Server) effectiveRestartLocked(profile string) restart.Policy {
	return s.restart.Merge(s.agentRestart[profile]).WithDefaults()
}

// restartStateLocked returns a panel's supervision bookkeeping, creating it — and
// the map holding it — on first use. The map is built lazily rather than only in
// the constructor so a Server assembled field-by-field (as the tests do) is as
// safe to supervise as one New built. Caller holds s.mu.
func (s *Server) restartStateLocked(id string) *restartState {
	if s.restarts == nil {
		s.restarts = make(map[string]*restartState)
	}
	st := s.restarts[id]
	if st == nil {
		st = &restartState{}
		s.restarts[id] = st
	}
	return st
}

// noteSpawnLocked records that a panel's process has just started: the run's
// clock begins, and any armed restart is disarmed because the thing it was
// waiting to do has happened. Caller holds s.mu.
func (s *Server) noteSpawnLocked(id string, now time.Time) {
	st := s.restartStateLocked(id)
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.startedAt = now
}

// noteStopRequested records that the user asked these panels to stop, so the exit
// it causes is not mistaken for a failure worth undoing. Closing a panel needs no
// such note — that path removes the panel before its process is reaped, so the
// exit never reaches the supervisor — but signalling one does: the panel stays in
// the fleet and its exit arrives looking exactly like a crash.
func (s *Server) noteStopRequested(ids []string) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.restartStateLocked(id).stopAsked = now
	}
}

// forgetRestartLocked drops a panel's supervision state, disarming any pending
// restart. Called when the panel is closed or purged — a panel that no longer
// exists must not come back. Caller holds s.mu.
func (s *Server) forgetRestartLocked(id string) {
	if st := s.restarts[id]; st != nil && st.timer != nil {
		st.timer.Stop()
	}
	delete(s.restarts, id)
}

// superviseExitLocked decides what happens to a panel whose process just exited,
// and returns the activity line to show for it. It arms the restart timer as a
// side effect; the empty string means "nothing to add", leaving the plain exited
// activity in place. Caller holds s.mu.
//
// The order of the checks is the policy, written out:
//
//   - a daemon shutting down is killing everything on purpose
//   - a panel with no recorded spec has nothing to re-run
//   - an exit the user asked for is not a failure
//   - a clean exit is a finished job, not a crash
//   - a run that lasted long enough earns its failure budget back
//   - and giving up is loud, carrying the reason
func (s *Server) superviseExitLocked(id string, exitCode int, now time.Time) string {
	if s.shuttingDown {
		return ""
	}
	spec, ok := s.specs[id]
	if !ok {
		return ""
	}
	policy := s.effectiveRestartLocked(spec.Profile)
	if policy.Mode != restart.OnFailure {
		return ""
	}

	st := s.restartStateLocked(id)
	if !st.stopAsked.IsZero() && now.Sub(st.stopAsked) < userStopGrace {
		log.Info().Str("panel", id).Msg("exit followed a signal the user sent; not restarting")
		return "exited · stopped on request"
	}
	if !policy.Restarts(exitCode) {
		st.failures = 0
		return ""
	}
	// A run that stayed up long enough was healthy: whatever crash loop the
	// counter was tracking is over, and this failure starts a fresh budget.
	if !st.startedAt.IsZero() && now.Sub(st.startedAt) >= policy.Healthy {
		st.failures = 0
	}
	st.failures++
	if st.failures > policy.Max {
		log.Warn().Str("panel", id).Int("failures", st.failures-1).Int("exit_code", exitCode).
			Msg("restart limit reached; leaving the panel exited")
		return fmt.Sprintf("exited · restart limit reached after %d failures", st.failures-1)
	}

	delay := policy.Delay(st.failures - 1)
	st.timer = time.AfterFunc(delay, func() { s.restartPanel(id) })
	log.Info().Str("panel", id).Int("exit_code", exitCode).Int("attempt", st.failures).
		Int("max", policy.Max).Dur("in", delay).Msg("restarting panel")
	return fmt.Sprintf("exited · restarting in %s (%d/%d)", delay, st.failures, policy.Max)
}

// restartPanel re-runs a panel the supervisor decided to bring back. It is the
// timer's callback, so it runs without the lock and re-checks the world it was
// armed in: the panel may have been closed, re-run by hand, or the daemon may be
// going down in the seconds it waited.
//
// A restart that fails to start is itself a failure — it counts against the
// budget and arms the next attempt — rather than a silent end to the supervision.
func (s *Server) restartPanel(id string) {
	s.mu.Lock()
	st := s.restarts[id]
	if st != nil {
		st.timer = nil
	}
	idx := s.indexLocked(id)
	stale := s.shuttingDown || idx < 0 || s.panels[idx].State != panel.Exited
	s.mu.Unlock()
	if stale {
		return
	}

	if err := s.respawnPanel(id); err != nil {
		log.Warn().Err(err).Str("panel", id).Msg("restart failed")
		s.mu.Lock()
		activity := s.superviseExitLocked(id, -1, time.Now())
		if i := s.indexLocked(id); i >= 0 && activity != "" {
			s.panels[i].Activity = activity
		}
		s.mu.Unlock()
		s.broadcast(s.panelsMsg())
		return
	}
	s.notifyAttached(id, "\r\n[restarted]\r\n")
}

// notifyAttached writes a server-authored line to every client watching a panel.
// It rides the same path as the "[process exited]" notice, so a restart is
// visible where the exit that caused it was — the one place a viewer can tell a
// fresh process apart from a program that cleared its own screen.
func (s *Server) notifyAttached(id, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for cc := range s.clients {
		if cc.attached[id] {
			send(cc, protoOutput(id, text))
		}
	}
}
