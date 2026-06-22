package server

// EphemeralCount returns how many ephemeral diff panels the server currently
// tracks. It exists only for tests, which assert the set is empty after a diff
// panel is closed or its owning client disconnects (no orphan PTYs).
func (s *Server) EphemeralCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ephemeral)
}
