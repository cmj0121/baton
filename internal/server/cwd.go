package server

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proctree"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/vtquery"
)

// osc7Carry is how many trailing bytes of a panel's output are kept to be re-read
// with the next chunk.
//
// A working-directory report can be split across two reads — the PTY hands over
// whatever has arrived, not whole sequences — and a report seen half at a time is
// a report missed. Carrying a short tail closes that without buffering the stream:
// it only has to span one sequence, and a path longer than this is past what any
// shell prompt carries.
const osc7Carry = 512

// noteOutputCwdLocked reads a panel's own working-directory report out of a chunk
// of its output, if it made one. It runs on the output hot path, so the cheap
// substring gate comes first and the parse only when it fires. Caller holds s.mu.
//
// A report from another host is discarded rather than believed. Inside an ssh
// session the shell that speaks is the remote one: it reports the remote host and
// a remote path, and taking that for a local directory would put a re-run in a
// same-named local directory — landing somewhere else silently, which is worse
// than not landing at all.
func (s *Server) noteOutputCwdLocked(id string, data []byte) {
	if !s.trackCwd.ReadsReport() {
		return
	}
	buf := data
	if carry := s.osc7Tail[id]; len(carry) > 0 {
		buf = append(append(make([]byte, 0, len(carry)+len(data)), carry...), data...)
	}
	if vtquery.HasOSC7(buf) {
		if path, host, ok := vtquery.OSC7Path(buf); ok {
			if isLocalHost(host) {
				s.setCwdLocked(id, path)
				// A shell that reports its own directory never needs to be asked, so
				// this panel is exempt from process-table sampling from here on.
				if s.reportedCwd == nil {
					s.reportedCwd = make(map[string]bool)
				}
				s.reportedCwd[id] = true
			} else {
				log.Debug().Str("panel", id).Str("host", host).Msg("ignoring a working directory reported by another host")
			}
		}
	}
	s.rememberTailLocked(id, buf)
}

// rememberTailLocked keeps the last osc7Carry bytes of what was just scanned, so
// a report split across two reads is whole the second time. Caller holds s.mu.
func (s *Server) rememberTailLocked(id string, buf []byte) {
	if s.osc7Tail == nil {
		s.osc7Tail = make(map[string][]byte)
	}
	if len(buf) > osc7Carry {
		buf = buf[len(buf)-osc7Carry:]
	}
	s.osc7Tail[id] = append(make([]byte, 0, len(buf)), buf...)
}

// setCwdLocked records a panel's live working directory, ignoring anything that
// is not an absolute path. Caller holds s.mu.
func (s *Server) setCwdLocked(id, dir string) {
	if !filepath.IsAbs(dir) {
		return
	}
	if i := s.indexLocked(id); i >= 0 {
		s.panels[i].Cwd = dir
	}
}

// isLocalHost reports whether a host named in a working-directory report is this
// machine. An empty host means "wherever this terminal is", which is here.
//
// The first label is compared as well as the whole name, because the two sides
// disagree about the suffix more often than they disagree about the machine: a
// shell reports "box.local" where the host calls itself "box", or the reverse.
func isLocalHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	self, err := os.Hostname()
	if err != nil || self == "" {
		return false // cannot tell; a foreign path is the costlier mistake
	}
	if strings.EqualFold(host, self) {
		return true
	}
	first := func(s string) string { h, _, _ := strings.Cut(s, "."); return h }
	return strings.EqualFold(first(host), first(self))
}

// livePid is a panel's process-group leader pid, read from the PTY manager. It is
// deliberately not taken from Panel.Pid: that field is joined in only when a
// snapshot is rendered for the wire, so the fleet the server works with in memory
// carries a zero there and every reader of it would silently sample nothing.
func (s *Server) livePid(id string) int {
	if s.pidOf == nil {
		return s.pty.Pids()[id]
	}
	return s.pidOf(id)
}

// cwdSample is one panel to ask the process table about, carried out of the
// monitor tick so the syscall happens with the lock released.
type cwdSample struct {
	id  string
	pid int
}

// wantsCwdSampleLocked reports whether a panel that has just settled at a prompt
// should have its working directory read from the process table.
//
// Settling is the moment to ask: the directory is stable, it is where the user is
// about to do something, and the transition is rare — a handful per panel per
// session, not one per monitor tick. A shell that reports its own directory is
// never asked at all, so the common case costs nothing. Caller holds s.mu.
func (s *Server) wantsCwdSampleLocked(id string, pid int) bool {
	return s.trackCwd.ReadsProcess() && pid > 0 && !s.reportedCwd[id]
}

// sampleCwdFromProcess reads a panel's directory from the process table and
// records it, whatever was known before — the shell may have moved since. It is
// the pull half of the tracking, and must run with s.mu released: it asks the OS.
func (s *Server) sampleCwdFromProcess(id string, pid int) {
	dir, err := proctree.Cwd(pid)
	if err != nil {
		log.Debug().Err(err).Str("panel", id).Msg("could not sample the working directory")
		return
	}
	s.mu.Lock()
	s.setCwdLocked(id, dir)
	s.mu.Unlock()
}

// panelCwd is a panel's best-known working directory, sampled from the process
// table when the shell has not reported one and the mode allows it.
//
// The sample is taken here — when something is about to use the path — rather
// than on a tick. Keeping fifty panels' directories fresh for a string nobody is
// looking at is the cost this project avoids elsewhere. It returns "" when the
// directory is not known, which callers read as "fall back to where it started"
// rather than as a directory.
//
// The pid is resolved only after the early return, not before it. livePid reads
// the PTY manager's whole table under that manager's lock to take one entry out
// of it, and this now runs on every delivery and every fan-out member — the
// answered-from-the-row case, which is the common one, must not pay for a map
// sized to the fleet.
func (s *Server) panelCwd(id string) string {
	s.mu.Lock()
	i := s.indexLocked(id)
	if i < 0 {
		s.mu.Unlock()
		return ""
	}
	known, track := s.panels[i].Cwd, s.trackCwd
	s.mu.Unlock()
	if known != "" || !track.ReadsProcess() {
		return known
	}
	pid := s.livePid(id)
	if pid <= 0 {
		return ""
	}
	dir, err := proctree.Cwd(pid)
	if err != nil {
		log.Debug().Err(err).Str("panel", id).Msg("could not sample the working directory")
		return ""
	}
	s.mu.Lock()
	s.setCwdLocked(id, dir)
	s.mu.Unlock()
	return dir
}

// respawnDir is the directory a re-run should start in, and the notice to show
// when that is not the one the panel was last in.
//
// The fallback is deliberately visible. A panel that comes back somewhere other
// than where it was, without saying so, is the failure this feature exists to
// avoid: the directory may have been a worktree that has since been removed, and
// silently landing in the original directory looks like the restore worked.
func (s *Server) respawnDir(id string, spec ptymgr.Spec, isAgent bool) (dir, notice string) {
	if !s.restoreCwd.Restores(isAgent) {
		return spec.Dir, ""
	}
	last := s.panelCwd(id)
	switch {
	case last == "" || last == spec.Dir:
		return spec.Dir, ""
	case dirExists(last):
		return last, ""
	default:
		return spec.Dir, "last directory is gone (" + last + "); starting where the panel was created"
	}
}

// targetDir is the directory a git or diff operation runs in for a panel: the
// directory the agent is in now when that is known, else the one it was launched
// in. It is what makes those operations follow an agent that has moved into a
// worktree instead of staying pinned to where it started.
func (s *Server) targetDir(id string, spec ptymgr.Spec) string {
	if live := s.panelCwd(id); live != "" && dirExists(live) {
		return live
	}
	return ptymgr.PanelDir(spec.Dir)
}

// forgetCwdLocked drops a panel's tracking state. Caller holds s.mu.
func (s *Server) forgetCwdLocked(id string) {
	delete(s.osc7Tail, id)
	delete(s.reportedCwd, id)
}

// isAgentPanelLocked reports whether a panel runs an agent rather than a shell.
// Caller holds s.mu.
func (s *Server) isAgentPanelLocked(id string) bool {
	i := s.indexLocked(id)
	return i >= 0 && s.panels[i].Kind == panel.Agent
}
