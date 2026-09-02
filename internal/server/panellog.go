package server

// Panel output logging: the daemon's half of docs/LOGGING.md.
//
// baton keeps a bounded in-memory replay ring per panel, and that ring dies with
// the panel. What you actually want when an agent finishes a long run is its full
// transcript — to grep, to paste into an issue, to hand to another agent, to keep
// for an audit — so this file writes one to disk.
//
// Three properties are worth stating once, here, because everything below follows
// from them:
//
//   - The DAEMON owns the PTY, so the file lands on the machine the FLEET runs on.
//     Over --remote that is not the machine the cockpit is on, and the cockpit
//     therefore never opens the file itself: it asks for a panel that reads it.
//   - The sink OUTLIVES the process it logs. A panel that exits suspends its sink
//     and a re-run resumes it, appending under a new session marker — the previous
//     run is usually why you are reading the file.
//   - The write happens with s.mu RELEASED. It is on the hot output path, and the
//     whole fleet's fan-out must never queue behind one panel's disk.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/panellog"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// The markers written into a log at each lifecycle edge. They are constants
// rather than literals at the call sites so the file reads consistently and a
// test can assert on the same strings the daemon writes.
const (
	logMarkStopped  = "logging stopped"
	logMarkExited   = "process exited"
	logMarkRestart  = "session restarted"
	logMarkShutdown = "daemon shutting down"
	logMarkClosed   = "panel closed"
	logMarkFailed   = "logging stopped: the file could not be written"
)

// effectiveLogDirLocked resolves where a panel's log lands: the agent profile's
// own log-dir when it names one, else the fleet-wide panel.log-dir. An empty
// result means logging is off, which is the default — a feature that writes
// terminals to disk is one a user opts into by naming a directory. Caller holds
// s.mu.
func (s *Server) effectiveLogDirLocked(profile string) string {
	if dir := s.agentLogDir[profile]; dir != "" {
		return dir
	}
	return s.logDir
}

// logSink returns a panel's open sink, or nil when it is not being logged.
func (s *Server) logSink(id string) *panellog.Sink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logs[id]
}

// LogPath is the file a panel's output is being written to, or "" when it is not
// being logged. Exported for the cockpit's "open my log" and for tests.
func (s *Server) LogPath(id string) string {
	if sink := s.logSink(id); sink != nil {
		return sink.Path()
	}
	return ""
}

// writeLog appends a chunk of a panel's output to its log, if it has one. It runs
// with s.mu released, on the panel's own output pump — so per-panel ordering is
// the pump's ordering, and a slow disk slows one panel rather than the fleet.
//
// A failed write switches the panel's logging OFF rather than retrying it every
// chunk. A full disk is not a transient, and a badge that keeps claiming a log is
// being written when it is not is the failure this whole feature is supposed to
// prevent.
func (s *Server) writeLog(id string, data []byte) {
	sink := s.logSink(id)
	if sink == nil {
		return
	}
	if err := sink.Write(data); err != nil {
		log.Warn().Str("panel", id).Err(err).Msg("panel log write failed; logging switched off for this panel")
		s.stopLogging(id, logMarkFailed)
		s.broadcastFleet()
	}
}

// startLogging opens a panel's log and flushes its replay buffer into it,
// returning the file's path.
//
// The replay flush is the point of starting this way: you reach for logging
// BECAUSE something interesting just happened, so a log that began at the
// keypress would miss the thing that made you press it.
//
// The file work — the ring snapshot, the open, the flush — is done with s.mu
// released, since it is disk I/O on a lock the output path contends. s.logMu
// serialises the enable/disable pair instead, so two toggles cannot both win.
func (s *Server) startLogging(id string) (string, error) {
	s.logMu.Lock()
	defer s.logMu.Unlock()

	s.mu.Lock()
	if s.logs[id] != nil {
		path := s.logs[id].Path()
		s.mu.Unlock()
		return path, nil // already logging: enabling twice is not an error
	}
	i := s.indexLocked(id)
	if i < 0 {
		s.mu.Unlock()
		return "", fmt.Errorf("no panel with id %q", id)
	}
	title := s.panels[i].Title
	spec := s.specs[id]
	dir := s.effectiveLogDirLocked(spec.Profile)
	maxBytes := s.logMaxBytes
	s.mu.Unlock()

	// targetDir takes s.mu for itself, so it is resolved out here rather than
	// alongside the reads above.
	workdir := s.targetDir(id, spec.Spec)

	if dir == "" {
		return "", fmt.Errorf("logging is off — set panel.log-dir in $HOME/.baton/config")
	}
	now := time.Now()
	sink, err := panellog.Open(dir, title, id, maxBytes, now)
	if err != nil {
		return "", err
	}
	if err := sink.Start(title, workdir, s.pty.Snapshot(id), now); err != nil {
		_ = sink.Close(logMarkFailed, now)
		return "", err
	}

	s.mu.Lock()
	if existing := s.logs[id]; existing != nil { // a race we cannot have, guarded anyway
		s.mu.Unlock()
		_ = sink.Close(logMarkStopped, now)
		return existing.Path(), nil
	}
	s.logs[id] = sink
	s.mu.Unlock()

	log.Info().Str("panel", id).Str("file", sink.Path()).Msg("panel logging started")
	return sink.Path(), nil
}

// stopLogging closes a panel's log for good and forgets it, writing reason as the
// closing marker. A panel that was not being logged is a no-op, so every teardown
// path — switching off, closing the panel, purging it, shutting down — may call it
// unconditionally.
func (s *Server) stopLogging(id, reason string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.mu.Lock()
	sink := s.logs[id]
	delete(s.logs, id)
	s.mu.Unlock()
	if sink == nil {
		return
	}
	if err := sink.Close(reason, time.Now()); err != nil {
		log.Warn().Str("panel", id).Err(err).Msg("closing a panel log failed")
	}
	log.Info().Str("panel", id).Str("file", sink.Path()).Str("reason", reason).Msg("panel logging stopped")
}

// suspendLog closes a logged panel's file when its process exits, KEEPING the
// sink so a re-run resumes into the same file. It is the half of the respawn
// contract that lives on the exit side.
func (s *Server) suspendLog(id string, exitCode int) {
	sink := s.logSink(id)
	if sink == nil {
		return
	}
	if err := sink.Suspend(fmt.Sprintf("%s (code %d)", logMarkExited, exitCode), time.Now()); err != nil {
		log.Warn().Str("panel", id).Err(err).Msg("flushing a panel log on exit failed")
	}
}

// resumeLog reopens a logged panel's file after a re-run, under a new session
// marker rather than truncating: the previous run is usually why you are reading
// the file.
func (s *Server) resumeLog(id string) {
	sink := s.logSink(id)
	if sink == nil {
		return
	}
	if err := sink.Resume(logMarkRestart, time.Now()); err != nil {
		log.Warn().Str("panel", id).Err(err).Msg("reopening a panel log on re-run failed; logging switched off for this panel")
		s.stopLogging(id, logMarkFailed)
	}
}

// autoLog starts logging a freshly created panel when its agent profile asks to
// be logged from the moment it spawns (panel.agents.<name>.log: true).
//
// A destination that cannot be written does NOT fail the spawn. The agent is the
// point and the log is the accessory: a typo'd path must not make a profile
// unspawnable. The panel comes up unlogged, carries no logging badge, and the
// reason is reported — which is the friendlier half of an honest pair.
func (s *Server) autoLog(id, profile string) {
	s.mu.Lock()
	want := s.agentLog[profile]
	s.mu.Unlock()
	if !want {
		return
	}
	if _, err := s.startLogging(id); err != nil {
		log.Warn().Str("panel", id).Str("profile", profile).Err(err).Msg("auto-logging is configured for this profile but could not start; the panel runs unlogged")
	}
}

// closeAllLogs flushes and closes every open log — the daemon's shutdown sweep,
// so no transcript ends mid-line with no explanation of why.
func (s *Server) closeAllLogs(reason string) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.logs))
	for id := range s.logs {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.stopLogging(id, reason)
	}
}

// toggleLog is the panel.log action: it starts logging the panel, or stops it if
// it is already on, and reports which happened. The fleet is broadcast either way
// so the card's badge and the footer follow immediately — a feature that writes
// your terminal to disk has to be visible while it does it.
func (s *Server) toggleLog(cc *clientConn, id string) error {
	if id == "" {
		return fmt.Errorf("panel.log needs an id")
	}
	if path := s.LogPath(id); path != "" {
		s.stopLogging(id, logMarkStopped)
		s.broadcastFleet()
		send(cc, proto.ServerMsg{Type: "notice", Notice: "logging stopped: " + path})
		return nil
	}
	path, err := s.startLogging(id)
	if err != nil {
		return err
	}
	s.broadcastFleet()
	send(cc, proto.ServerMsg{Type: "notice", Notice: "logging to " + path})
	return nil
}

// openLogView is the panel.logview action: it opens the panel's log in a
// TRANSIENT panel that follows the file as it grows.
//
// It reuses the ephemeral mechanism the git and diff menus already use, so the
// panel closes on exit and on the way back to the dashboard or the group view and
// never becomes a card in the fleet. Unlike those two it takes no agent target: a
// shell's log is as readable as an agent's, and the agent-only gate they carry is
// about running git in a work tree, which this does not do.
func (s *Server) openLogView(cc *clientConn, id string) error {
	if id == "" {
		return fmt.Errorf("panel.logview needs an id")
	}
	path := s.LogPath(id)
	if path == "" {
		return fmt.Errorf("this panel is not being logged — press the logging key first")
	}
	name, args := followCommand(path)

	ephID, unwind, err := s.registerEphemeral(cc, "log")
	if err != nil {
		return err
	}
	// The viewer belongs to nobody's agent, so it runs under the fleet-wide caps
	// rather than under a profile's.
	if err := s.startPanel(ephID, "", ptymgr.Spec{Command: name, Args: args, Dir: dirOf(path)}); err != nil {
		unwind()
		return fmt.Errorf("could not open the log: %w", err)
	}
	log.Info().Str("panel", ephID).Str("target", id).Str("file", path).Msg("log view opened")
	send(cc, proto.ServerMsg{Type: "ephemeral", ID: ephID})
	return nil
}

// followCommand is how a log is read back: `less +F`, which follows the file the
// way tail does but leaves you able to stop following and page through what came
// before — the panel it belongs to is usually still running, so both halves get
// used. It falls back to `tail -f` on a host with no less.
func followCommand(path string) (string, []string) {
	if less, err := exec.LookPath("less"); err == nil {
		return less, []string{"+F", path}
	}
	return "tail", []string{"-f", path}
}

// dirOf is the directory a log viewer runs in — the log's own directory, so a
// pager that shells out lands somewhere that exists. A directory that has since
// been removed falls back to home, which ptymgr would have chosen anyway.
func dirOf(path string) string {
	if dir := filepath.Dir(path); dirExists(dir) {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
