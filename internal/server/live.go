package server

import (
	"maps"
	"path/filepath"
	"slices"

	"github.com/cmj0121/baton/internal/usage"
)

// followLiveSessions moves each Claude Code panel onto the session its status
// line last reported, when that is not the one baton holds as current. /clear and
// /resume change a panel's session from inside the agent, and without this the
// panel's opening cost and its share of the window stay keyed on a session it has
// left — one that, after a /clear before the first turn, never had a transcript
// at all.
//
// The records are read outside mu, like the opening costs they feed; a panel
// respawned while they were read is skipped, because its record belongs to a run
// that is over. Records of launches no panel is running any more are swept.
func (s *Server) followLiveSessions() {
	s.mu.Lock()
	dir, read := s.liveDir, s.liveRead
	launches := maps.Clone(s.launched)
	s.mu.Unlock()

	if read == nil {
		if dir == "" {
			return
		}
		read = func(launch string) (string, bool) {
			return usage.ReadLiveSession(filepath.Join(dir, launch))
		}
	}
	got := make(map[string]string, len(launches))
	for id, launch := range launches {
		if sid, ok := read(launch); ok {
			got[id] = sid
		}
	}

	s.mu.Lock()
	for id, sid := range got {
		if s.launched[id] == launches[id] {
			s.makeCurrentLocked(id, sid)
		}
	}
	keep := make(map[string]bool, len(s.launched))
	for _, launch := range s.launched {
		keep[launch] = true
	}
	s.mu.Unlock()

	if dir != "" {
		usage.SweepLiveSessions(dir, keep)
	}
}

// makeCurrentLocked makes sid the panel's current session, the last in its list.
// A session the panel ran under before — /resume back to it — is moved rather
// than listed twice, so the panel's spend never counts one transcript twice.
// Callers must hold mu.
func (s *Server) makeCurrentLocked(id, sid string) {
	list := slices.DeleteFunc(slices.Clone(s.sessions[id]), func(x string) bool { return x == sid })
	s.sessions[id] = append(list, sid)
}
