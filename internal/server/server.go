// Package server is the headless baton core: the connection layer plus the
// single source of truth for panel state. Clients attach over the socket, send
// commands, and receive event broadcasts.
package server

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// clientConn is one attached frontend. Outbound messages go through its buffered
// channel so a slow client never stalls a broadcast.
type clientConn struct {
	out chan proto.ServerMsg
}

// Server owns all state and every PTY. It is safe for concurrent use.
type Server struct {
	ln  net.Listener
	pty *ptymgr.Manager

	mu      sync.Mutex
	seq     int
	panels  []panel.Panel
	clients map[*clientConn]struct{}
}

// New builds a server bound to ln. The fleet starts empty — panels appear only
// when the user spawns a real one.
func New(ln net.Listener) *Server {
	return &Server{
		ln:      ln,
		pty:     ptymgr.New(),
		clients: make(map[*clientConn]struct{}),
	}
}

// Serve accepts connections until the listener closes.
func (s *Server) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	cc := &clientConn{out: make(chan proto.ServerMsg, proto.EventBufferSize)}
	s.addClient(cc)
	defer s.removeClient(cc)

	// Writer goroutine: the only place this connection is encoded to.
	go func() {
		enc := json.NewEncoder(conn)
		for msg := range cc.out {
			if err := enc.Encode(msg); err != nil {
				return
			}
		}
	}()

	// Command loop.
	dec := json.NewDecoder(conn)
	for {
		var cmd proto.Command
		if err := dec.Decode(&cmd); err != nil {
			return // client detached
		}
		s.onCommand(cc, cmd)
	}
}

// onCommand maps a wire command onto a core action.
func (s *Server) onCommand(cc *clientConn, cmd proto.Command) {
	switch cmd.Action {
	case "hello":
		send(cc, proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion})
		send(cc, s.panelsMsg())
	case "panel.list":
		send(cc, s.panelsMsg())
	case "panel.create":
		if err := s.createPanel(cmd.Kind, cmd.Path); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcast(s.panelsMsg())
	case "panel.close":
		if err := s.closePanel(cmd.ID); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcast(s.panelsMsg())
	default:
		send(cc, proto.ServerMsg{Type: "error", Error: fmt.Sprintf("unknown action %q", cmd.Action)})
	}
}

// createPanel is a core action: it spawns the backing process (running path, or
// the default shell when empty) and records the new panel in the fleet.
func (s *Server) createPanel(kind, path string) error {
	if kind == "" {
		kind = proto.KindShell
	}

	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("%d", s.seq)
	s.mu.Unlock()

	switch kind {
	case proto.KindShell:
		if err := s.pty.Start(id, path); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown panel kind %q", kind)
	}

	name := "shell"
	if path != "" {
		name = filepath.Base(path)
	}
	p := panel.Panel{
		ID:       id,
		Kind:     panel.ParseKind(kind),
		Title:    fmt.Sprintf("%s #%s", name, id),
		State:    panel.Running,
		Activity: "spawned",
	}
	s.mu.Lock()
	s.panels = append(s.panels, p)
	s.mu.Unlock()

	log.Info().Str("panel", p.Title).Msg("panel created")
	return nil
}

// closePanel is a core action: it removes the panel with the given id from the
// fleet and stops its backing process, if any.
func (s *Server) closePanel(id string) error {
	if id == "" {
		return fmt.Errorf("panel.close needs an id")
	}

	s.mu.Lock()
	idx := -1
	for i, p := range s.panels {
		if p.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no panel with id %q", id)
	}
	title := s.panels[idx].Title
	s.panels = slices.Delete(s.panels, idx, idx+1)
	s.mu.Unlock()

	s.pty.Stop(id) // no-op for mock panels with no real process
	log.Info().Str("panel", title).Msg("panel closed")
	return nil
}

func (s *Server) panelsMsg() proto.ServerMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]proto.Panel, len(s.panels))
	for i, p := range s.panels {
		out[i] = p.ToProto()
	}
	return proto.ServerMsg{Type: "panels", Panels: out}
}

func (s *Server) addClient(cc *clientConn) {
	s.mu.Lock()
	s.clients[cc] = struct{}{}
	n := len(s.clients)
	s.mu.Unlock()
	log.Info().Int("clients", n).Msg("client attached")
}

func (s *Server) removeClient(cc *clientConn) {
	s.mu.Lock()
	if _, ok := s.clients[cc]; ok {
		delete(s.clients, cc)
		close(cc.out)
	}
	n := len(s.clients)
	s.mu.Unlock()
	log.Info().Int("clients", n).Msg("client detached")
}

// broadcast fans a message out to every attached client.
func (s *Server) broadcast(msg proto.ServerMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for cc := range s.clients {
		send(cc, msg)
	}
}

// send queues a message to one client. It never blocks; if the client's buffer
// is full the message is dropped.
func send(cc *clientConn, msg proto.ServerMsg) {
	select {
	case cc.out <- msg:
	default:
	}
}
