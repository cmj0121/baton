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

	allowNameConflict bool   // when false, panel titles and group names stay unique
	replayBytes       int    // per-panel replay buffer; 0 keeps the ptymgr default
	defaultDir        string // workdir for a panel that asks for none; empty → the user's home
	version           string // the server's build version, reported in the welcome

	onReload func() // invoked on a server.reload command; re-reads config and Reloads

	mu      sync.Mutex
	seq     int
	panels  []panel.Panel
	clients map[*clientConn]struct{}
	mon     *monitor // lifecycle + telemetry bookkeeping, guarded by mu
}

// Option tunes a Server at construction. Options keep New's signature stable as
// settings accrue.
type Option func(*Server)

// WithAllowNameConflict lets two work items share a name, disabling the default
// uniqueness check on panel titles and group names.
func WithAllowNameConflict(allow bool) Option {
	return func(s *Server) { s.allowNameConflict = allow }
}

// WithReplayBytes sets the per-panel replay buffer the server keeps and replays
// to an attaching frontend, seeding the scrollback it can page through. Zero
// keeps the ptymgr default.
func WithReplayBytes(bytes int) Option {
	return func(s *Server) { s.replayBytes = bytes }
}

// WithDefaultDir sets the working directory new panels run in when the request
// names none. Empty keeps the fallback (the user's home), so a panel never
// inherits the directory the daemon was launched from.
func WithDefaultDir(dir string) Option {
	return func(s *Server) { s.defaultDir = dir }
}

// WithVersion sets the server's build version, reported to a frontend in the
// welcome so it can show the backend version and flag a mismatch.
func WithVersion(v string) Option {
	return func(s *Server) { s.version = v }
}

// New builds a server bound to ln. The fleet starts empty — panels appear only
// when the user spawns a real one. Options are applied before the PTY manager is
// built, so settings like the replay size reach it.
func New(ln net.Listener, opts ...Option) *Server {
	s := &Server{
		ln:      ln,
		clients: make(map[*clientConn]struct{}),
		mon:     newMonitor(),
	}
	for _, opt := range opts {
		opt(s)
	}

	var pmOpts []ptymgr.Option
	if s.replayBytes > 0 {
		pmOpts = append(pmOpts, ptymgr.WithRingCap(s.replayBytes))
	}
	s.pty = ptymgr.New(pmOpts...)
	s.pty.OnOutput(s.routeOutput)
	s.pty.OnClose(s.onPanelExit)
	return s
}

// OnReload registers the handler a server.reload command runs — the in-cockpit
// reload, which re-reads the config and calls Reload. It shares the routine the
// SIGHUP path uses, so a cockpit reload and an external `kill -HUP` do the same
// thing. Set it once, before Serve.
func (s *Server) OnReload(fn func()) { s.onReload = fn }

// Reload applies the hot-reloadable settings from a freshly read config without
// restarting the daemon or disturbing a single live panel — the SIGHUP path. The
// name-conflict policy, the default workdir, and the per-panel replay buffer can
// all change under a running fleet; settings fixed at construction (the listener,
// the build version) are left alone. A replayBytes of zero resets the buffer to
// its built-in default.
func (s *Server) Reload(allowNameConflict bool, defaultDir string, replayBytes int) {
	s.mu.Lock()
	s.allowNameConflict = allowNameConflict
	s.defaultDir = defaultDir
	s.mu.Unlock()
	s.pty.SetRingCap(replayBytes)
	log.Info().Bool("allow_name_conflict", allowNameConflict).Str("default_dir", defaultDir).Int("replay_bytes", replayBytes).Msg("settings reloaded")
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
			s.mon.forget(id) // a dead panel no longer ticks
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

// routeOutput fans a panel's output out to every client zoomed into it, and feeds
// the Monitor: output is the signal that wakes a quiet (or just-spawned) panel
// back to running. The wake is in-memory only — the next monitor tick carries it
// to clients — so the hot output path never triggers a broadcast of its own.
func (s *Server) routeOutput(id string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mon.observed(id, len(data))
	if i := s.indexLocked(id); i >= 0 {
		switch s.panels[i].State {
		case panel.Spawning, panel.Idle, panel.Attention:
			s.panels[i].State = panel.Running
			s.mon.entered(id)
		}
	}
	for cc := range s.clients {
		if cc.attached[id] {
			send(cc, proto.ServerMsg{Type: "output", ID: id, Data: data})
		}
	}
}

// indexLocked returns the index of the panel with the given id, or -1. The caller
// must hold s.mu.
func (s *Server) indexLocked(id string) int {
	for i := range s.panels {
		if s.panels[i].ID == id {
			return i
		}
	}
	return -1
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
	go s.monitorLoop(stop)

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

// monitorLoop is the Monitor's heartbeat: on each tick it advances every panel's
// lifecycle and telemetry, and broadcasts the refresh when something moved. It
// stops when Serve returns.
func (s *Server) monitorLoop(stop <-chan struct{}) {
	t := time.NewTicker(monitorInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if msg, ok := s.monitorTick(); ok {
				s.broadcast(msg)
			}
		}
	}
}

// monitorTick re-evaluates every live panel: it settles a quiet one to idle or
// attention (wakes are handled on the output path), rolls each sparkline, and
// refreshes the activity line. It returns a "telemetry" snapshot and true when any
// panel moved and there is a client to tell; telemetry rides its own message type
// so it never disturbs a frontend's structural panel stream.
func (s *Server) monitorTick() (proto.ServerMsg, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for i := range s.panels {
		p := &s.panels[i]
		if p.State == panel.Exited {
			continue
		}
		quiet := s.mon.quiet(p.ID)
		attention := quiet && p.State == panel.Running && looksLikeAttention(s.pty.Tail(p.ID, attnTailBytes))
		if ns, ok := nextState(p.State, quiet, attention); ok {
			p.State = ns
			s.mon.entered(p.ID)
			changed = true
		}
		if spark := s.mon.roll(p.ID); spark != p.Spark {
			p.Spark = spark
			changed = true
		}
		if act := activityText(p.State, s.mon.since(p.ID)); act != p.Activity {
			p.Activity = act
			changed = true
		}
	}

	if !changed || len(s.clients) == 0 {
		return proto.ServerMsg{}, false
	}
	out := make([]proto.Panel, len(s.panels))
	for i, p := range s.panels {
		out[i] = p.ToProto()
	}
	return proto.ServerMsg{Type: "telemetry", Panels: out}, true
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
		send(cc, proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion, ServerVer: s.version})
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
	case "panel.move":
		if err := s.movePanels(cmd.IDs, cmd.Index); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcast(s.panelsMsg())
	case "panel.pin", "panel.unpin":
		if err := s.setPinned(pinTargets(cmd), cmd.Action == "panel.pin"); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcast(s.panelsMsg())
	case "server.reload":
		// Re-read the config and apply it in place — the cockpit's reload action.
		// The fleet keeps running; only the tunable settings change.
		if s.onReload != nil {
			s.onReload()
		}
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
// an agent panel runs its profile command with args. Both run in dir, the working
// directory; an empty dir falls back to the configured default (then the user's
// home), so a panel never inherits the directory the daemon was launched from.
func (s *Server) createPanel(kind, path string, args []string, dir string) error {
	if kind == "" {
		kind = proto.KindShell
	}

	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("%d", s.seq)
	if dir == "" {
		dir = s.defaultDir // read under the lock so a SIGHUP reload cannot race it; empty still falls back to home
	}
	s.mu.Unlock()

	switch kind {
	case proto.KindShell:
		if err := s.pty.StartCmd(id, ptymgr.Spec{Command: path, Dir: dir}); err != nil {
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
		State:    panel.Spawning,
		Activity: activityText(panel.Spawning, 0), // the Monitor keeps it live from here
	}
	s.mu.Lock()
	s.panels = append(s.panels, p)
	s.mon.spawned(id) // start the Monitor's clock; first output wakes it to running
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
	s.mon.forget(id)
	s.mu.Unlock()

	s.pty.Stop(id) // no-op for a panel with no live process
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
			s.mon.forget(p.ID)
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

// movePanels is a core action that reorders the fleet: it lifts the panels named
// in ids out as a block (keeping their current relative order) and reinserts them
// at index among the remaining panels. Fleet order is the single source of truth
// every frontend renders from — the dashboard's item order and a group's member
// order both follow it — so reordering here moves items in every view at once and
// for every attached client. The index is clamped into range; ids that match no
// panel are ignored, and it errors only when none match. A moved group's members
// land contiguously, which is a tidy side effect rather than a requirement.
func (s *Server) movePanels(ids []string, index int) error {
	if len(ids) == 0 {
		return fmt.Errorf("panel.move needs at least one panel")
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	block := make([]panel.Panel, 0, len(ids))
	rest := make([]panel.Panel, 0, len(s.panels))
	for _, p := range s.panels {
		if _, ok := want[p.ID]; ok {
			block = append(block, p)
		} else {
			rest = append(rest, p)
		}
	}
	if len(block) == 0 {
		return fmt.Errorf("no panel matched the given ids")
	}
	if index < 0 {
		index = 0
	}
	if index > len(rest) {
		index = len(rest)
	}

	out := make([]panel.Panel, 0, len(s.panels))
	out = append(out, rest[:index]...)
	out = append(out, block...)
	out = append(out, rest[index:]...)
	s.panels = out

	log.Info().Int("panels", len(block)).Int("index", index).Msg("panels reordered")
	return nil
}

// pinTargets is the panels a pin command addresses: the IDs list, falling back
// to the single ID for a one-panel toggle.
func pinTargets(cmd proto.Command) []string {
	if len(cmd.IDs) > 0 {
		return cmd.IDs
	}
	if cmd.ID != "" {
		return []string{cmd.ID}
	}
	return nil
}

// setPinned marks every listed panel pinned (or not), the server-owned flag the
// group split reads to promote a member to a live tile. Pins live with the panel
// here — the single source of truth — so they survive a frontend restart and are
// shared across clients. Ids that match no panel are skipped; it errors only when
// none matched.
func (s *Server) setPinned(ids []string, pinned bool) error {
	if len(ids) == 0 {
		return fmt.Errorf("panel.pin needs at least one panel")
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i := range s.panels {
		if _, ok := want[s.panels[i].ID]; ok {
			s.panels[i].Pinned = pinned
			n++
		}
	}
	if n == 0 {
		return fmt.Errorf("no panel matched the given ids")
	}
	log.Info().Int("panels", n).Bool("pinned", pinned).Msg("panels pinned")
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
