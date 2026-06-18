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
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// statsInterval is how often the server samples host CPU/memory for the footer.
const statsInterval = 2 * time.Second

// clientConn is one attached frontend. Outbound messages go through its buffered
// channel so a slow client never stalls a broadcast. attached is the set of panel
// ids this client is streaming (guarded by Server.mu) — one for a single zoom,
// several for a group split, empty for none.
type clientConn struct {
	out      chan proto.ServerMsg
	attached map[string]bool
}

// Server owns all state and every PTY. It is safe for concurrent use.
type Server struct {
	ln  net.Listener
	pty *ptymgr.Manager

	allowNameConflict bool // when false, panel titles and group names stay unique

	mu      sync.Mutex
	seq     int
	panels  []panel.Panel
	clients map[*clientConn]struct{}
}

// Option tunes a Server at construction. Options keep New's signature stable as
// settings accrue.
type Option func(*Server)

// WithAllowNameConflict lets two work items share a name, disabling the default
// uniqueness check on panel titles and group names.
func WithAllowNameConflict(allow bool) Option {
	return func(s *Server) { s.allowNameConflict = allow }
}

// New builds a server bound to ln. The fleet starts empty — panels appear only
// when the user spawns a real one.
func New(ln net.Listener, opts ...Option) *Server {
	s := &Server{
		ln:      ln,
		pty:     ptymgr.New(),
		clients: make(map[*clientConn]struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.pty.OnOutput(s.routeOutput)
	s.pty.OnClose(s.onPanelExit)
	return s
}

// onPanelExit marks a panel exited when its process ends on its own, notifies
// and detaches any client zoomed into it, and broadcasts the change. It is a
// no-op for a panel already gone (e.g. an explicit panel.close).
func (s *Server) onPanelExit(id string) {
	s.mu.Lock()
	found := false
	for i := range s.panels {
		if s.panels[i].ID == id {
			s.panels[i].State = panel.Exited
			s.panels[i].Activity = "exited"
			found = true
			break
		}
	}
	for cc := range s.clients {
		if cc.attached[id] {
			send(cc, proto.ServerMsg{Type: "output", ID: id, Data: []byte("\r\n[process exited]\r\n")})
			delete(cc.attached, id)
		}
	}
	s.mu.Unlock()

	if found {
		log.Info().Str("panel", id).Msg("panel process exited")
		s.broadcast(s.panelsMsg())
	}
}

// routeOutput fans a panel's output out to every client zoomed into it.
func (s *Server) routeOutput(id string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for cc := range s.clients {
		if cc.attached[id] {
			send(cc, proto.ServerMsg{Type: "output", ID: id, Data: data})
		}
	}
}

// attach adds panel id to a client's stream set. The recent output is replayed
// before live output starts, so the screen is not blank and stays in order —
// both happen under the lock that gates routeOutput. Attaching is additive, so a
// group split can stream every member at once; each message is tagged with its
// panel id, so the client demuxes. Detaching is detach's job.
func (s *Server) attach(cc *clientConn, id string) {
	if id == "" {
		return // detaching is detach's job; attaching nothing is a no-op
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap := s.pty.Snapshot(id); len(snap) > 0 {
		send(cc, proto.ServerMsg{Type: "output", ID: id, Data: snap})
	}
	cc.attached[id] = true
}

// detach removes panel id from a client's stream set, or all of them when id is
// empty (the back-compatible "detach everything" a single zoom sends).
func (s *Server) detach(cc *clientConn, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		cc.attached = make(map[string]bool)
		return
	}
	delete(cc.attached, id)
}

// Serve accepts connections until the listener closes.
func (s *Server) Serve() error {
	stop := make(chan struct{})
	defer close(stop)
	go s.statsLoop(stop)

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(conn)
	}
}

// statsLoop samples host CPU/memory on a fixed interval and broadcasts it to
// attached clients, so the footer reflects the server's machine. It stops when
// Serve returns (the listener closed).
func (s *Server) statsLoop(stop <-chan struct{}) {
	_, _ = cpu.Percent(0, false) // prime the rolling CPU delta
	t := time.NewTicker(statsInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.mu.Lock()
			n := len(s.clients)
			s.mu.Unlock()
			if n > 0 {
				s.broadcast(statsMsg())
			}
		}
	}
}

// statsMsg samples the host's CPU load and memory for the footer. cpu.Percent
// with a zero interval is non-blocking, reporting load since the previous call.
func statsMsg() proto.ServerMsg {
	msg := proto.ServerMsg{Type: "stats"}
	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		msg.CPU = pct[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		msg.MemUsed, msg.MemTotal = vm.Used, vm.Total
	}
	return msg
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	cc := &clientConn{out: make(chan proto.ServerMsg, proto.EventBufferSize), attached: make(map[string]bool)}
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
		send(cc, statsMsg()) // seed the footer immediately, before the first tick
	case "panel.list":
		send(cc, s.panelsMsg())
	case "panel.create":
		if err := s.createPanel(cmd.Kind, cmd.Path, cmd.Args, cmd.Dir); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcast(s.panelsMsg())
	case "panel.close":
		ids := cmd.IDs
		if len(ids) == 0 && cmd.ID != "" {
			ids = []string{cmd.ID} // back-compat: a single id still closes one panel
		}
		if err := s.closePanels(ids); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcast(s.panelsMsg())
	case "panel.purge":
		if s.purgeExited() > 0 {
			s.broadcast(s.panelsMsg())
		}
	case "panel.group":
		if err := s.groupPanels(cmd.IDs, cmd.Group); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcast(s.panelsMsg())
	case "panel.ungroup":
		if err := s.ungroup(cmd.IDs, cmd.Group); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcast(s.panelsMsg())
	case "panel.rename":
		if err := s.rename(cmd.ID, cmd.Group, cmd.Name); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcast(s.panelsMsg())
	case "panel.attach":
		s.attach(cc, cmd.ID)
	case "panel.detach":
		s.detach(cc, cmd.ID)
	case "panel.input":
		s.pty.Write(cmd.ID, cmd.Data)
	case "panel.resize":
		s.pty.Resize(cmd.ID, cmd.Rows, cmd.Cols)
	default:
		send(cc, proto.ServerMsg{Type: "error", Error: fmt.Sprintf("unknown action %q", cmd.Action)})
	}
}

// createPanel is a core action: it spawns the backing process and records the new
// panel in the fleet. A shell panel runs path (or the default shell when empty);
// an agent panel runs its profile command with args in dir, the working directory
// the agent operates on.
func (s *Server) createPanel(kind, path string, args []string, dir string) error {
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
	case proto.KindAgent:
		if path == "" {
			return fmt.Errorf("an agent panel needs a command")
		}
		if err := s.pty.StartCmd(id, ptymgr.Spec{Command: path, Args: args, Dir: dir}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown panel kind %q", kind)
	}

	p := panel.Panel{
		ID:       id,
		Kind:     panel.ParseKind(kind),
		Title:    panelTitle(kind, path, dir, id),
		State:    panel.Running,
		Activity: panelActivity(kind, dir),
	}
	s.mu.Lock()
	s.panels = append(s.panels, p)
	s.mu.Unlock()

	log.Info().Str("panel", p.Title).Msg("panel created")
	return nil
}

// panelTitle is the human label for a new panel. An agent reads as
// "<command> · <workdir>", e.g. "claude · baton", so its task and where it runs
// are visible at a glance; a shell falls back to "<name> #<id>".
func panelTitle(kind, path, dir, id string) string {
	if kind == proto.KindAgent {
		name := filepath.Base(path)
		if dir != "" {
			return fmt.Sprintf("%s · %s", name, filepath.Base(dir))
		}
		return fmt.Sprintf("%s #%s", name, id)
	}
	name := "shell"
	if path != "" {
		name = filepath.Base(path)
	}
	return fmt.Sprintf("%s #%s", name, id)
}

// panelActivity is the short status line a new panel starts with: an agent shows
// its working directory, a shell just notes the spawn.
func panelActivity(kind, dir string) string {
	if kind == proto.KindAgent && dir != "" {
		return "in " + dir
	}
	return "spawned"
}

// closePanels closes every listed panel and broadcasts once for the whole batch
// — closing a work item is one command, not one round-trip per member. Ids that
// match no panel are skipped; it errors only when none matched, so closing a
// group another client already thinned still retires the rest.
func (s *Server) closePanels(ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("panel.close needs an id")
	}
	closed := 0
	for _, id := range ids {
		if err := s.closePanel(id); err == nil {
			closed++
		}
	}
	if closed == 0 {
		return fmt.Errorf("no panel matched the given ids")
	}
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

// purgeExited drops every exited panel from the fleet and frees its retained PTY
// resources, leaving live panels untouched. Returns how many were removed.
func (s *Server) purgeExited() int {
	s.mu.Lock()
	kept := make([]panel.Panel, 0, len(s.panels))
	var gone []string
	for _, p := range s.panels {
		if p.State == panel.Exited {
			gone = append(gone, p.ID)
			continue
		}
		kept = append(kept, p)
	}
	s.panels = kept
	s.mu.Unlock()

	for _, id := range gone {
		s.pty.Stop(id)
	}
	if len(gone) > 0 {
		log.Info().Int("count", len(gone)).Msg("purged exited panels")
	}
	return len(gone)
}

// groupPanels is a core action: it files the given panels under one work-item
// name, the shared identity every group view keys on. An empty name is rejected
// (the empty string means "ungrouped"); ids that match no panel are skipped, and
// if none match at all it errors.
func (s *Server) groupPanels(ids []string, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("panel.group needs a name")
	}
	if len(ids) == 0 {
		return fmt.Errorf("panel.group needs at least one panel")
	}

	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}

	// Check the name and file the panels under one lock, so two clients racing the
	// same name cannot both pass the uniqueness test before either writes. Skipping
	// the group of this same name lets the "add" action merge into an existing work
	// item, which is intentional rather than a conflict.
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.allowNameConflict && s.nameTakenLocked(name, "", name) {
		return fmt.Errorf("the name %q is already taken", name)
	}
	moved := s.setGroupLocked(func(p panel.Panel) bool { _, ok := want[p.ID]; return ok }, name)
	if moved == 0 {
		return fmt.Errorf("no panel matched the given ids")
	}
	log.Info().Str("group", name).Int("panels", moved).Msg("panels grouped")
	return nil
}

// nameTakenLocked reports whether name already identifies a different work item —
// a panel title (other than skipID) or a group name (other than skipGroup). It is
// the basis of the no-duplicate-names policy. An empty name never collides. The
// caller must hold s.mu, so the check and the write it guards stay atomic.
func (s *Server) nameTakenLocked(name, skipID, skipGroup string) bool {
	if name == "" {
		return false
	}
	for _, p := range s.panels {
		if p.ID != skipID && p.Title == name {
			return true
		}
		if p.Group != "" && p.Group != skipGroup && p.Group == name {
			return true
		}
	}
	return false
}

// setGroup files every panel matching match under name, returning how many moved.
// It takes the lock itself, for callers (ungroup) that have no name to check.
func (s *Server) setGroup(match func(panel.Panel) bool, name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setGroupLocked(match, name)
}

// setGroupLocked is the lock-free core of setGroup; the caller must hold s.mu so
// a name check and the move it gates run as one atomic step.
func (s *Server) setGroupLocked(match func(panel.Panel) bool, name string) int {
	moved := 0
	for i := range s.panels {
		if match(s.panels[i]) {
			s.panels[i].Group = name
			moved++
		}
	}
	return moved
}

// ungroup is a core action that clears the Group on its targets, returning them
// to the dashboard as lone panels. Given ids it removes just those members from
// whatever group they sit in; otherwise it dissolves the whole named group.
func (s *Server) ungroup(ids []string, name string) error {
	if len(ids) > 0 {
		want := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			want[id] = struct{}{}
		}
		moved := s.setGroup(func(p panel.Panel) bool {
			_, ok := want[p.ID]
			return ok && p.Group != ""
		}, "")
		if moved == 0 {
			return fmt.Errorf("no grouped panel matched the given ids")
		}
		log.Info().Int("panels", moved).Msg("panels removed from group")
		return nil
	}
	if name == "" {
		return fmt.Errorf("panel.ungroup needs a group or panel ids")
	}
	moved := s.setGroup(func(p panel.Panel) bool { return p.Group == name }, "")
	if moved == 0 {
		return fmt.Errorf("no panels in group %q", name)
	}
	log.Info().Str("group", name).Int("panels", moved).Msg("group dissolved")
	return nil
}

// rename is a core action that renames either one panel (by id) or a whole group
// (by its current name). A panel rename changes its title; a group rename rewrites
// the Group on every member. Exactly one target must be given, and the new name
// must be non-empty.
func (s *Server) rename(id, group, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("panel.rename needs a name")
	}
	switch {
	case id != "" && group != "":
		return fmt.Errorf("panel.rename takes a panel id or a group, not both")
	case id != "":
		return s.renamePanel(id, name)
	case group != "":
		return s.renameGroup(group, name)
	default:
		return fmt.Errorf("panel.rename needs a panel id or a group")
	}
}

// renamePanel sets the title of the panel with the given id. The uniqueness
// check and the write happen under one lock so a racing rename cannot slip a
// duplicate title past the test.
func (s *Server) renamePanel(id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.allowNameConflict && s.nameTakenLocked(title, id, "") {
		return fmt.Errorf("the name %q is already taken", title)
	}
	for i := range s.panels {
		if s.panels[i].ID == id {
			s.panels[i].Title = title
			log.Info().Str("panel", id).Str("title", title).Msg("panel renamed")
			return nil
		}
	}
	return fmt.Errorf("no panel with id %q", id)
}

// renameGroup rewrites the Group of every panel currently filed under old to the
// new name. Renaming onto an existing group name merges the two — group identity
// is the name itself. The check and the rewrite share one lock so the merge
// decision cannot race another rename.
func (s *Server) renameGroup(old, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.allowNameConflict && s.nameTakenLocked(name, "", old) {
		return fmt.Errorf("the name %q is already taken", name)
	}
	moved := s.setGroupLocked(func(p panel.Panel) bool { return p.Group == old }, name)
	if moved == 0 {
		return fmt.Errorf("no panels in group %q", old)
	}
	log.Info().Str("from", old).Str("to", name).Int("panels", moved).Msg("group renamed")
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
