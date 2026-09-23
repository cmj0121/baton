package server

import (
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// An agent panel's opening cost is the SYS figure in the cockpit footer: what its
// current session paid before the conversation said anything (see usage.Opening).
// It is a fact about a session, fixed from the first turn on, so each session is
// read exactly once — and only the CURRENT session of each panel is read or kept.
// A respawn mints a new session whose opening can differ (a memory file edited in
// between), and showing the old one until the new one lands would be showing a
// cost the panel is no longer paying.

// refreshOpening reads the opening cost of every panel's current session that has
// none held yet, and lets go of the readings no panel's current session needs.
//
// The reads happen outside mu: each is a glob and a file read, and the lock guards
// every panel event. A session that has not had its first turn yet reads nothing
// and is simply asked again next poll.
func (s *Server) refreshOpening() {
	s.mu.Lock()
	current := s.currentSessionsLocked()
	var want []string
	for _, sid := range current {
		if _, held := s.opening[sid]; !held {
			want = append(want, sid)
		}
	}
	read := s.openingRead
	s.mu.Unlock()

	if read == nil {
		root := usage.ClaudeProjectsDir()
		read = func(sid string) (int64, bool) { return usage.Opening(root, sid) }
	}
	got := make(map[string]int64, len(want))
	for _, sid := range want {
		if n, ok := read(sid); ok {
			got[sid] = n
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-derived under the lock: a panel may have been respawned or closed while
	// the reads ran, and a reading for a session that is no longer anyone's
	// current one must not be kept.
	live := make(map[string]bool)
	for _, sid := range s.currentSessionsLocked() {
		live[sid] = true
	}
	for sid := range s.opening {
		if !live[sid] {
			delete(s.opening, sid)
		}
	}
	for sid, n := range got {
		if !live[sid] {
			continue
		}
		if s.opening == nil {
			s.opening = make(map[string]int64)
		}
		s.opening[sid] = n
	}
}

// currentSessionsLocked is each panel's current session id, keyed by panel id:
// the last one it was launched with. Callers must hold mu.
func (s *Server) currentSessionsLocked() map[string]string {
	out := make(map[string]string, len(s.sessions))
	for id, sessions := range s.sessions {
		if len(sessions) > 0 {
			out[id] = sessions[len(sessions)-1]
		}
	}
	return out
}

// openingLocked is the held opening cost of each panel whose current session has
// one, keyed by panel id, or nil when none has. Callers must hold mu.
func (s *Server) openingLocked() map[string]int64 {
	var out map[string]int64
	for id, sid := range s.currentSessionsLocked() {
		n, ok := s.opening[sid]
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]int64)
		}
		out[id] = n
	}
	return out
}

// attachOpening returns info carrying the per-panel opening costs, without
// mutating what the caller held.
func attachOpening(info *proto.UsageInfo, opening map[string]int64) *proto.UsageInfo {
	if opening == nil && (info == nil || info.Opening == nil) {
		return info
	}
	if info == nil {
		return &proto.UsageInfo{Opening: opening}
	}
	out := *info
	out.Opening = opening
	return &out
}
