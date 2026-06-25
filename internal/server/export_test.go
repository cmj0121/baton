package server

import (
	"net"
	"time"
)

// Handle runs the connection handler on conn directly, bypassing the listener.
// It exists only for tests that need to interpose an instrumented net.Conn (e.g.
// one flagging concurrent writes) on the server's side of the wire.
func (s *Server) Handle(conn net.Conn) { s.handle(conn) }

// EphemeralCount returns how many ephemeral diff panels the server currently
// tracks. It exists only for tests, which assert the set is empty after a diff
// panel is closed or its owning client disconnects (no orphan PTYs).
func (s *Server) EphemeralCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ephemeral)
}

// SetHeartbeat overrides the per-connection heartbeat ping cadence. It exists
// only for tests, so the heartbeat fires in milliseconds rather than seconds.
// Call it before Serve so every handle() reads the test value.
func (s *Server) SetHeartbeat(d time.Duration) { s.heartbeat = d }

// ClientCount returns how many clients are currently attached. It exists only
// for tests, which assert a dropped connection is reaped (the writer/heartbeat
// teardown removed the client).
func (s *Server) ClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// PanelDir and PanelEnv expose a panel's retained spawn spec, so tests can assert
// the conductor runs in a server-managed workspace (not the requested dir) with
// the injected identity env.
func (s *Server) PanelDir(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.specs[id].Dir
}

func (s *Server) PanelEnv(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.specs[id].Env
}
