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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/cmj0121/baton/internal/gitdiff"
	"github.com/cmj0121/baton/internal/gitops"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/signals"
	"github.com/cmj0121/baton/internal/state"
)

// statsInterval is how often the server samples host CPU/memory for the footer.
const statsInterval = 2 * time.Second

// minVisible and maxVisible bound a group's visible count — how many members
// stream as live tiles before the rest collapse into the summary tile. maxVisible
// mirrors the TUI's old maxGroupTiles, the live-tile cap.
const (
	minVisible = 1
	maxVisible = 16
)

// maxEphemeralPerConn caps how many diff panels a single connection may hold
// open at once. It bounds a scripted or runaway client's blast radius — each
// open diff costs a PTY, a throwaway git index, and gc-able loose objects — so a
// client must close one before opening another past the cap.
const maxEphemeralPerConn = 8

// clientConn is one attached frontend. Outbound messages go through its buffered
// channel so a slow client never stalls a broadcast. attached is the set of panel
// ids this client is streaming (guarded by Server.mu) — one for a single zoom,
// several for a group split, empty for none.
type clientConn struct {
	out      chan proto.ServerMsg
	attached map[string]bool
	// ephemeral is the set of ephemeral diff-panel ids this client opened. They
	// live only as PTYs (never in s.panels), so the owning conn tracks them to
	// reap any still-open one when it disconnects. Guarded by Server.mu.
	ephemeral map[string]bool
}

// Server owns all state and every PTY. It is safe for concurrent use.
type Server struct {
	ln  net.Listener
	pty *ptymgr.Manager

	allowNameConflict bool   // when false, panel titles and group names stay unique
	replayBytes       int    // per-panel replay buffer; 0 keeps the ptymgr default
	defaultDir        string // workdir for a panel that asks for none; empty → the user's home
	diffCommand       string // explicit diff command for the agent diff pop-up; empty → git diff.tool then a built-in diff
	editor            string // commit editor for the git menu (GIT_EDITOR); empty → git's own editor chain
	worktreeDir       string // base dir for new git-menu worktrees; empty → a sibling of the agent's repo
	version           string // the server's build version, reported in the welcome

	onReload func() // invoked on a server.reload command; re-reads config and Reloads

	// Plugin wiring. eventSink receives every lifecycle event (a non-blocking post
	// to the plugin's worker, safe to call under mu); outputEvents gates the
	// high-volume panel.output emit so it costs nothing until a plugin asks for it.
	// onRunCommand invokes a plugin command by name. clientConfig is the merged
	// effective config (defaults <- YAML <- plugin) served to frontends, and
	// pluginCmds is the plugin command list the picker shows. Set before Serve or
	// under mu; read under mu.
	eventSink    func(name string, fields map[string]any)
	outputEvents atomic.Bool
	onRunCommand func(name string) error
	clientConfig json.RawMessage
	pluginCmds   []proto.PluginCommand
	footerText   string // a plugin-set persistent footer segment (baton.footer); carried on config + pushed live

	mu      sync.Mutex
	seq     int
	panels  []panel.Panel
	clients map[*clientConn]struct{}
	mon     *monitor               // lifecycle + telemetry bookkeeping, guarded by mu
	specs   map[string]ptymgr.Spec // immutable spawn spec per panel id, for persistence + respawn (guarded by mu)

	// Ephemeral diff panels. ephemeral is the set of live "diff:<n>" ids spawned
	// as PTYs but deliberately kept out of s.panels/s.specs, so persistence
	// (snapshotState) and the dashboard (panelsMsg) never see them. ephSeq numbers
	// them from a private counter, so a "diff:" id can never collide with or
	// perturb the decimal panel ids drawn from s.seq. Both guarded by mu.
	ephemeral map[string]struct{}
	ephSeq    int

	// groupShown is the per-group visible count — how many members stream as live
	// tiles before the rest collapse into the summary tile. Keyed by group name;
	// an absent or zero entry means "use the client default". Guarded by mu.
	groupShown map[string]int

	// Persistence. stateF is the snapshot path ("" disables persistence); dirty is
	// a 1-deep "save pending" nudge the saverLoop drains; saveMu serializes the
	// disk writes; bootTime is when this server (re)booted, persisted as LastBoot.
	stateF   string
	dirty    chan struct{}
	saveMu   sync.Mutex
	bootTime time.Time
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

// WithDiffCommand sets the explicit diff command the agent diff pop-up runs.
// Empty falls back to the repo's git diff.tool, then a built-in untracked-
// inclusive diff — the resolution gitdiff.ResolveCommand performs.
func WithDiffCommand(cmd string) Option {
	return func(s *Server) { s.diffCommand = cmd }
}

// WithEditor sets the commit editor the git menu's commit op opens (injected as
// GIT_EDITOR). Empty lets git use its own GIT_EDITOR / core.editor / EDITOR / vi
// chain.
func WithEditor(cmd string) Option {
	return func(s *Server) { s.editor = cmd }
}

// WithWorktreeDir sets the base directory new git-menu worktrees are created
// under. Empty defaults to a sibling "<repo>-worktrees/<branch>" of the agent's
// repo.
func WithWorktreeDir(dir string) Option {
	return func(s *Server) { s.worktreeDir = dir }
}

// WithVersion sets the server's build version, reported to a frontend in the
// welcome so it can show the backend version and flag a mismatch.
func WithVersion(v string) Option {
	return func(s *Server) { s.version = v }
}

// WithStateFile points the server at the snapshot it persists the fleet/layout
// to and restores from on boot. An empty path disables persistence entirely.
func WithStateFile(path string) Option {
	return func(s *Server) { s.stateF = path }
}

// New builds a server bound to ln. The fleet starts empty — panels appear only
// when the user spawns a real one. Options are applied before the PTY manager is
// built, so settings like the replay size reach it.
func New(ln net.Listener, opts ...Option) *Server {
	s := &Server{
		ln:         ln,
		clients:    make(map[*clientConn]struct{}),
		mon:        newMonitor(),
		specs:      make(map[string]ptymgr.Spec),
		ephemeral:  make(map[string]struct{}),
		groupShown: make(map[string]int),
		dirty:      make(chan struct{}, 1),
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
func (s *Server) Reload(allowNameConflict bool, defaultDir string, replayBytes int, diffCommand, editor, worktreeDir string) {
	s.mu.Lock()
	s.allowNameConflict = allowNameConflict
	s.defaultDir = defaultDir
	s.diffCommand = diffCommand
	s.editor = editor
	s.worktreeDir = worktreeDir
	s.mu.Unlock()
	s.pty.SetRingCap(replayBytes)
	log.Info().Bool("allow_name_conflict", allowNameConflict).Str("default_dir", defaultDir).Int("replay_bytes", replayBytes).Str("diff_command", diffCommand).Str("editor", editor).Str("worktree_dir", worktreeDir).Msg("settings reloaded")
}

// onPanelExit marks a panel exited when its process ends on its own, notifies
// and detaches any client zoomed into it, and broadcasts the change. It is a
// no-op for a panel already gone (e.g. an explicit panel.close).
func (s *Server) onPanelExit(id string, exitCode int) {
	s.mu.Lock()
	found := false
	var fields map[string]any
	for i := range s.panels {
		if s.panels[i].ID == id {
			s.panels[i].State = panel.Exited
			s.panels[i].Activity = "exited"
			s.mon.forget(id) // a dead panel no longer ticks
			fields = panelFields(s.panels[i])
			fields["exit_code"] = exitCode
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
	if found {
		s.emit("panel.exit", fields)
	}
	s.mu.Unlock()

	if found {
		log.Info().Str("panel", id).Int("exit_code", exitCode).Msg("panel process exited")
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
		switch from := s.panels[i].State; from {
		case panel.Spawning, panel.Idle, panel.Attention:
			s.panels[i].State = panel.Running
			s.mon.entered(id)
			f := panelFields(s.panels[i])
			f["from"], f["to"] = from.String(), panel.Running.String()
			s.emit("panel.state", f)
		}
		// panel.output is opt-in: emitted only when a plugin registered a handler,
		// so the hot output path costs nothing otherwise. The byte slice is copied
		// since the caller (pump) reuses its buffer after this returns.
		if s.outputEvents.Load() {
			f := panelFields(s.panels[i])
			f["data"] = string(data)
			s.emit("panel.output", f)
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
		// Condition the raw replay ring before a fresh emulator reconstructs from it:
		// trim to the last full screen reset so a ring that evicted a program's
		// alt-screen enter (a long vim/pager) cannot leave its drawing on the primary
		// grid as dirty data — worst in a group split, where every tile attaches its
		// own emulator. Then strip query sequences so the emulator does not re-answer
		// the program's old terminal queries (their late replies echo as garbage at a
		// prompt). Live output is untouched by both.
		send(cc, proto.ServerMsg{Type: "output", ID: id, Data: stripReplayQueries(trimToLastScreenReset(snap))})
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
	go s.saverLoop(stop)

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
			from := p.State
			p.State = ns
			s.mon.entered(p.ID)
			changed = true
			f := panelFields(*p)
			f["from"], f["to"] = from.String(), ns.String()
			s.emit("panel.state", f)
			switch ns {
			case panel.Attention:
				s.emit("panel.attention", panelFields(*p))
			case panel.Idle:
				s.emit("panel.idle", panelFields(*p))
			}
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

	cc := &clientConn{
		out:       make(chan proto.ServerMsg, proto.EventBufferSize),
		attached:  make(map[string]bool),
		ephemeral: make(map[string]bool),
	}
	s.addClient(cc)
	defer s.removeClient(cc)
	defer s.closeEphemeral(cc) // reap any diff panel this conn left open on the way out

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
		if _, err := s.createPanel(cmd.Kind, cmd.Path, cmd.Args, cmd.Dir); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcastFleet()
	case "panel.respawn":
		if err := s.respawnPanel(cmd.ID); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcastFleet()
	case "panel.close":
		ids := cmd.IDs
		if len(ids) == 0 && cmd.ID != "" {
			ids = []string{cmd.ID} // back-compat: a single id still closes one panel
		}
		if err := s.closePanels(ids); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcastFleet()
	case "panel.purge":
		if s.purgeExited() > 0 {
			s.broadcastFleet()
		}
	case "panel.group":
		if err := s.groupPanels(cmd.IDs, cmd.Group); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcastFleet()
	case "panel.ungroup":
		if err := s.ungroup(cmd.IDs, cmd.Group); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcastFleet()
	case "panel.rename":
		if err := s.rename(cmd.ID, cmd.Group, cmd.Name); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcastFleet()
	case "panel.move":
		if err := s.movePanels(cmd.IDs, cmd.Index); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcastFleet()
	case "panel.pin", "panel.unpin":
		if err := s.setPinned(targetIDs(cmd), cmd.Action == "panel.pin"); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcastFleet()
	case "panel.signal":
		// Delivering a signal does not change any panel struct; an exit it triggers
		// flows back through onPanelExit, so there is nothing to broadcast here.
		if err := s.signalPanels(targetIDs(cmd), cmd.Signal); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
	case "panel.diff":
		// Open a transient diff panel for the target agent. openDiff sends its own
		// "ephemeral" reply on success; on failure we surface the reason as an error. It
		// is deliberately NOT broadcastFleet'd — the diff panel is ephemeral and must
		// never reach the dashboard or the persisted state.
		if err := s.openDiff(cc, cmd.ID); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
	case "panel.git":
		// Run a git-menu op for the target agent. The output ops spawn a transient
		// panel (openGit replies "ephemeral" so the client auto-zooms it); worktree-add is
		// a real fleet change (broadcasts); worktree-remove confirms with a notice.
		// Any failure surfaces as an error, like panel.diff.
		if err := s.runGit(cc, cmd); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
	case "group.show":
		if err := s.setGroupShown(cmd.Group, cmd.Count); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcastFleet()
	case "server.reload":
		// Re-read the config and apply it in place — the cockpit's reload action.
		// The fleet keeps running; only the tunable settings change.
		if s.onReload != nil {
			s.onReload()
		}
	case "config.get":
		// Hand the frontend the merged effective config (defaults <- YAML <- plugin)
		// and the plugin command list, so the cockpit can apply keymaps/toggles and
		// fill its command picker. Empty until a plugin sets them — the client then
		// just keeps its local config.
		s.mu.Lock()
		cfg, cmds, footer := s.clientConfig, s.pluginCmds, s.footerText
		s.mu.Unlock()
		send(cc, proto.ServerMsg{Type: "config", Config: cfg, Commands: cmds, Footer: footer})
	case "command.run":
		// Invoke a plugin-registered command by name on the Lua worker. The run is
		// fire-and-forget from the wire's view; any fleet change it makes broadcasts
		// through the normal core-action path.
		s.mu.Lock()
		run := s.onRunCommand
		s.mu.Unlock()
		if run == nil {
			send(cc, proto.ServerMsg{Type: "error", Error: "no plugin commands are registered"})
			return
		}
		if err := run(cmd.Name); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
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
func (s *Server) createPanel(kind, path string, args []string, dir string) (string, error) {
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

	// Build the spawn spec once, then use the same value to start the PTY and to
	// stash for respawn — so a restored panel re-runs with exactly what launched it
	// (a shell carries no args; an agent does).
	var spec ptymgr.Spec
	switch kind {
	case proto.KindShell:
		spec = ptymgr.Spec{Command: path, Dir: dir}
	case proto.KindAgent:
		if path == "" {
			return "", fmt.Errorf("an agent panel needs a command")
		}
		spec = ptymgr.Spec{Command: path, Args: args, Dir: dir}
	default:
		return "", fmt.Errorf("unknown panel kind %q", kind)
	}
	if err := s.pty.StartCmd(id, spec); err != nil {
		return "", err
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
	s.specs[id] = spec // the exact spec StartCmd launched, so respawn reproduces it
	s.mon.spawned(id)  // start the Monitor's clock; first output wakes it to running
	s.emit("panel.spawn", panelFields(p))
	s.mu.Unlock()

	log.Info().Str("panel", p.Title).Msg("panel created")
	return id, nil
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

// broadcastFleet pushes the current fleet snapshot to every client and marks the
// persisted state dirty — the two halves of "the fleet structurally changed":
// tell clients now, flush to disk soon. Every structural mutation ends here, so a
// new mutation path cannot announce a change yet silently forget to persist it.
// Non-structural live updates (a panel's exit, telemetry) call broadcast directly,
// since they restore identically and need no save.
func (s *Server) broadcastFleet() {
	s.broadcast(s.panelsMsg())
	s.markDirty()
}

// Shutdown sends SIGKILL to every live panel's process group, so no child
// process outlives the daemon when it stops. The signal handler calls this on the
// way out (after SaveNow has flushed the layout); a process group escapes only if
// a child daemonised into its own session, the same caveat panel signals carry.
// Returns the number of panels killed.
func (s *Server) Shutdown() int {
	n := s.pty.KillAll(syscall.SIGKILL)
	if n > 0 {
		log.Info().Int("panels", n).Msg("killed live panels on shutdown")
	}
	return n
}

// markDirty nudges the saverLoop to flush the current fleet/layout to disk. It is
// called after each successful structural mutation. The dirty channel is 1-deep, so
// a burst of mutations coalesces into a single save; a no-op when persistence is off.
func (s *Server) markDirty() {
	if s.stateF == "" {
		return
	}
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

// saverLoop persists the fleet/layout whenever a mutation marks the state dirty.
// It stops when Serve returns. The shutdown path flushes a final snapshot
// synchronously (SaveNow), since os.Exit kills this loop before it can drain.
func (s *Server) saverLoop(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-s.dirty:
			s.saveNow()
		}
	}
}

// snapshotState builds the persisted snapshot from the live fleet. It briefly
// acquires s.mu just to read; the caller must not hold it. The disk write is the
// caller's job (saveNow), kept off the lock.
func (s *Server) snapshotState() state.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	panels := make([]state.PanelState, len(s.panels))
	for i, p := range s.panels {
		spec := s.specs[p.ID]
		panels[i] = state.PanelState{
			ID:     p.ID,
			Kind:   p.Kind.String(),
			Title:  p.Title,
			Group:  p.Group,
			Pinned: p.Pinned,
			Spec:   state.Spec{Command: spec.Command, Args: spec.Args, Dir: spec.Dir},
		}
	}
	// Per-group view settings (the visible counts), keyed by name like the group.
	groups := make([]state.GroupLayout, 0, len(s.groupShown))
	for g, shown := range s.groupShown {
		if shown != 0 {
			groups = append(groups, state.GroupLayout{Group: g, Shown: shown})
		}
	}
	return state.State{Seq: s.seq, LastBoot: s.bootTime, Panels: panels, Groups: groups}
}

// saveNow writes the current snapshot to disk now. saveMu serializes writers so two
// saves never interleave; the snapshot is built under s.mu, then released before the
// disk I/O so a slow write never stalls a command. A write error is logged, never fatal.
func (s *Server) saveNow() {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	st := s.snapshotState() // builds under s.mu, then releases it
	if err := st.Save(s.stateF); err != nil {
		log.Warn().Err(err).Str("state_file", s.stateF).Msg("could not save state")
	}
}

// SaveNow flushes the current fleet/layout to disk synchronously. The daemon's
// shutdown path calls it before os.Exit, which would otherwise skip the final save.
// A no-op when persistence is off.
func (s *Server) SaveNow() {
	if s.stateF == "" {
		return
	}
	s.saveNow()
}

// Restore loads the persisted fleet/layout and seeds the server with it before
// Serve. Every restored panel comes back as an Exited dead-slot placeholder: no
// process is auto-respawned, for shells or agents alike — a manual panel.respawn
// re-runs one on demand. The id counter is restored (and bumped past the highest
// restored id) so a new panel can never collide with a dead slot. Call it once,
// before Serve; a no-op when persistence is off.
func (s *Server) Restore() {
	if s.stateF == "" {
		return
	}
	st, _ := state.Load(s.stateF) // Load never hard-fails: a bad file yields an empty State

	s.mu.Lock()
	defer s.mu.Unlock()
	s.bootTime = time.Now()
	s.seq = st.Seq
	max := s.seq
	for _, ps := range st.Panels {
		s.panels = append(s.panels, panel.Panel{
			ID:       ps.ID,
			Kind:     panel.ParseKind(ps.Kind),
			Title:    ps.Title,
			Group:    ps.Group,
			Pinned:   ps.Pinned,
			State:    panel.Exited,
			Activity: "restored · press r to re-run",
		})
		s.specs[ps.ID] = ptymgr.Spec{Command: ps.Spec.Command, Args: ps.Spec.Args, Dir: ps.Spec.Dir}
		if n, err := strconv.Atoi(ps.ID); err == nil && n > max {
			max = n
		}
	}
	if max > s.seq {
		s.seq = max // a new panel's id picks up past the highest restored one
	}
	for _, g := range st.Groups {
		if g.Shown > 0 {
			s.groupShown[g.Group] = g.Shown
		}
	}
	log.Info().Int("panels", len(st.Panels)).Int("seq", s.seq).Msg("state restored")
}

// respawnPanel re-runs the backing process of an exited panel from its frozen spawn
// spec. It is the manual counterpart to the no-auto-respawn restore: only an Exited
// panel with a recorded spec can be re-run. The lock is dropped around StartCmd (which
// may block), mirroring createPanel, then re-taken to flip the panel back to Spawning.
func (s *Server) respawnPanel(id string) error {
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no panel with id %q", id)
	}
	if s.panels[idx].State != panel.Exited {
		s.mu.Unlock()
		return fmt.Errorf("panel is still running")
	}
	spec, ok := s.specs[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("nothing to re-run")
	}

	if err := s.pty.StartCmd(id, spec); err != nil {
		return err
	}

	s.mu.Lock()
	if i := s.indexLocked(id); i >= 0 {
		s.panels[i].State = panel.Spawning
		s.panels[i].Activity = activityText(panel.Spawning, 0)
		s.mon.spawned(id) // restart the Monitor's clock; first output wakes it to running
	}
	s.mu.Unlock()

	log.Info().Str("panel", id).Msg("panel re-run")
	return nil
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
		// Not a real panel: it may be an ephemeral diff panel. Closing one stops its
		// PTY and drops it from the ephemeral set (the owning conn's cc.ephemeral is
		// cleared by the disconnect path; an explicit close here just needs the PTY
		// gone and the server set tidy). The client sends this on leaving the diff
		// zoom, so the transient panel does not outlive the view.
		if _, ok := s.ephemeral[id]; ok {
			delete(s.ephemeral, id)
			for cc := range s.clients {
				delete(cc.ephemeral, id)
			}
			s.mu.Unlock()
			// A plain Stop only closes the PTY master (SIGHUP); a GUI difftool or a
			// backgrounded child can survive that. An ephemeral panel is transient and
			// safe to hard-kill, so SIGKILL its whole process group first, then stop —
			// nothing the diff launched lingers. Scoped strictly to ephemeral ids;
			// normal panel close keeps its SIGHUP-via-close semantics.
			s.pty.Signal(id, syscall.SIGKILL)
			s.pty.Stop(id)
			log.Info().Str("panel", id).Msg("ephemeral diff panel closed")
			return nil
		}
		s.mu.Unlock()
		return fmt.Errorf("no panel with id %q", id)
	}
	title := s.panels[idx].Title
	s.panels = slices.Delete(s.panels, idx, idx+1)
	s.mon.forget(id)
	delete(s.specs, id) // the panel is gone for good; drop its retained spawn spec
	s.emit("panel.close", map[string]any{"id": id, "title": title})
	s.mu.Unlock()

	s.pty.Stop(id) // no-op for a panel with no live process
	log.Info().Str("panel", title).Msg("panel closed")
	return nil
}

// openDiff spawns a transient diff panel for the agent panel targetID: an
// ephemeral PTY, running the resolved diff command in that agent's workdir. The
// panel is never appended to s.panels and never written to s.specs, so it stays
// out of both the dashboard snapshot (panelsMsg) and the persisted state
// (snapshotState) for free — it is tracked only in s.ephemeral (server-wide) and
// cc.ephemeral (the owning conn, for disconnect cleanup). On success it replies
// {type:"ephemeral", id:"diff:<n>"} so the client can auto-zoom it.
//
// The git probes (work-tree check, change check, command resolution) run with
// s.mu released — they shell out and must never hold the server lock.
func (s *Server) openDiff(cc *clientConn, targetID string) error {
	diffCommand := s.snapDiffCommand()
	return s.openEphemeral(cc, targetID, "diff", func(dir string) (string, []string, []string, error) {
		if !gitdiff.IsWorkTree(dir) {
			return "", nil, nil, fmt.Errorf("not a git repository: %s", dir)
		}
		if !gitdiff.HasChanges(dir) {
			return "", nil, nil, fmt.Errorf("no uncommitted changes")
		}
		name, args := gitdiff.ResolveCommand(dir, diffCommand)
		return name, args, nil, nil
	})
}

// runGit dispatches a panel.git op for the target agent. The output-producing ops
// (status/log/add/commit/push/branch/worktree-list) spawn a transient panel via
// openGit; worktree-add creates a tree and spawns an agent in it (a fleet change,
// so it broadcasts); worktree-remove runs synchronously and confirms with a notice.
func (s *Server) runGit(cc *clientConn, cmd proto.Command) error {
	switch op := gitops.Op(cmd.Git); op {
	case gitops.OpWorktreeAdd:
		if err := s.gitWorktreeAdd(cmd.ID, cmd.Name); err != nil {
			return err
		}
		s.broadcastFleet()
		return nil
	case gitops.OpWorktreeRemove:
		if err := s.gitWorktreeRemove(cmd.ID, cmd.Dir); err != nil {
			return err
		}
		send(cc, proto.ServerMsg{Type: "notice", Notice: "worktree removed: " + cmd.Dir})
		return nil
	default:
		return s.openGit(cc, cmd.ID, op, cmd.Name)
	}
}

// openGit spawns a transient panel running an output-producing git op in the target
// agent's workdir, resolved by the gitops layer with the configured commit editor.
func (s *Server) openGit(cc *clientConn, targetID string, op gitops.Op, arg string) error {
	editor := s.snapEditor()
	return s.openEphemeral(cc, targetID, "git", func(dir string) (string, []string, []string, error) {
		return gitops.Resolve(op, dir, arg, editor)
	})
}

// ephemeralResolver produces the command (executable, args, extra env) a transient
// panel runs in the agent's resolved workdir, or an error explaining why not.
type ephemeralResolver func(dir string) (name string, args, env []string, err error)

// openEphemeral spawns a transient, auto-zoomed PTY for the agent panel targetID,
// running the command resolve produces in the agent's workdir. It is the shared
// engine behind the diff and git menus: the panel is never appended to s.panels or
// s.specs, so it stays out of the dashboard snapshot (panelsMsg) and the persisted
// state (snapshotState), tracked only in s.ephemeral (and the owning conn, for
// disconnect cleanup). label names the action for the log and the ephemeral id
// prefix. On success it replies {type:"ephemeral", id:"<label>:<n>"} so the client
// auto-zooms it. The git probes run with s.mu released — they shell out and must
// never hold the server lock.
func (s *Server) openEphemeral(cc *clientConn, targetID, label string, resolve ephemeralResolver) error {
	s.mu.Lock()
	idx := s.indexLocked(targetID)
	if idx < 0 {
		s.mu.Unlock()
		err := fmt.Errorf("no panel with id %q", targetID)
		log.Warn().Str("target", targetID).Str("action", label).Err(err).Msg("ephemeral rejected")
		return err
	}
	kind := s.panels[idx].Kind
	dir := s.specs[targetID].Dir
	s.mu.Unlock()

	// Authoritative agent-only gate. The client gates this too for UX, but the
	// server is the source of truth; relaxing the feature to shells is this one line.
	if kind != panel.Agent {
		err := fmt.Errorf("%s is available on agent panels", label)
		log.Warn().Str("target", targetID).Str("action", label).Err(err).Msg("ephemeral rejected")
		return err
	}

	// Resolve the effective workdir exactly as a spawn would (empty → home), then
	// let the caller resolve the command (and its git-specific gates) against it.
	dir = ptymgr.PanelDir(dir)
	name, args, env, err := resolve(dir)
	if err != nil {
		log.Warn().Str("target", targetID).Str("dir", dir).Str("action", label).Err(err).Msg("ephemeral rejected")
		return err
	}

	// Bound a scripted or runaway client: cap how many transient panels one
	// connection may hold open at once. The check reads cc.ephemeral under the same
	// lock the allocation below writes it, so two concurrent opens cannot both slip
	// past N.
	s.mu.Lock()
	if len(cc.ephemeral) >= maxEphemeralPerConn {
		s.mu.Unlock()
		err := fmt.Errorf("too many open panels (max %d) — close one first", maxEphemeralPerConn)
		log.Warn().Str("target", targetID).Str("action", label).Err(err).Msg("ephemeral rejected")
		return err
	}
	// Allocate the ephemeral id and register it before the spawn, so a concurrent
	// disconnect cleanup sees a consistent set; unregister if the spawn fails.
	s.ephSeq++
	ephID := fmt.Sprintf("%s:%d", label, s.ephSeq)
	s.ephemeral[ephID] = struct{}{}
	cc.ephemeral[ephID] = true
	s.mu.Unlock()

	if err := s.pty.StartCmd(ephID, ptymgr.Spec{Command: name, Args: args, Env: env, Dir: dir}); err != nil {
		s.mu.Lock()
		delete(s.ephemeral, ephID)
		delete(cc.ephemeral, ephID)
		s.mu.Unlock()
		err = fmt.Errorf("could not open %s: %w", label, err)
		log.Warn().Str("target", targetID).Str("dir", dir).Str("action", label).Err(err).Msg("ephemeral spawn failed")
		return err
	}

	log.Info().Str("panel", ephID).Str("target", targetID).Str("dir", dir).Str("action", label).Msg("ephemeral panel opened")
	send(cc, proto.ServerMsg{Type: "ephemeral", ID: ephID})
	return nil
}

// gitWorktreeAdd creates a worktree on a new branch off the target agent's repo and
// spawns an agent panel rooted in it, grouped under the branch name — the isolation
// bridge. It reuses the source agent's command and args, so the new tree gets the
// same kind of agent. A real fleet change, so the caller broadcasts.
func (s *Server) gitWorktreeAdd(targetID, branch string) error {
	s.mu.Lock()
	idx := s.indexLocked(targetID)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no panel with id %q", targetID)
	}
	kind := s.panels[idx].Kind
	spec := s.specs[targetID]
	base := s.worktreeDir
	s.mu.Unlock()

	if kind != panel.Agent {
		return fmt.Errorf("git is available on agent panels")
	}
	repo := ptymgr.PanelDir(spec.Dir)
	if !gitdiff.IsWorkTree(repo) {
		return fmt.Errorf("not a git repository: %s", repo)
	}

	path := worktreePath(base, repo, branch)
	if err := gitops.WorktreeAdd(repo, branch, path); err != nil {
		return err
	}

	// Spawn the agent in the new worktree and file it under the branch, so it lands
	// as a work item immediately. A spawn failure leaves the worktree in place — the
	// user can retire it with worktree-remove rather than us guessing.
	id, err := s.createPanel(proto.KindAgent, spec.Command, spec.Args, path)
	if err != nil {
		return fmt.Errorf("worktree created at %s, but the agent did not start: %w", path, err)
	}
	if err := s.groupPanels([]string{id}, branch); err != nil {
		log.Warn().Str("panel", id).Str("group", branch).Err(err).Msg("worktree agent spawned but not grouped")
	}
	log.Info().Str("repo", repo).Str("branch", branch).Str("path", path).Str("panel", id).Msg("worktree agent spawned")
	return nil
}

// gitWorktreeRemove removes the worktree at path from the target agent's repo. It
// runs plain (no --force), so git refuses a dirty or locked tree — surfaced as the
// error. It does not touch any panel; a removed tree's agent, if still open, is the
// user's to close.
func (s *Server) gitWorktreeRemove(targetID, path string) error {
	s.mu.Lock()
	idx := s.indexLocked(targetID)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no panel with id %q", targetID)
	}
	spec := s.specs[targetID]
	s.mu.Unlock()

	repo := ptymgr.PanelDir(spec.Dir)
	if err := gitops.WorktreeRemove(repo, path); err != nil {
		return err
	}
	log.Info().Str("repo", repo).Str("path", path).Msg("worktree removed")
	return nil
}

// worktreePath is where a new worktree for branch goes: under the configured base
// dir when set, else a sibling "<repo>-worktrees" of the repo. The branch's slashes
// become dashes so "feature/x" is a single path segment.
func worktreePath(base, repo, branch string) string {
	leaf := strings.ReplaceAll(branch, "/", "-")
	if base != "" {
		return filepath.Join(base, leaf)
	}
	return filepath.Join(repo+"-worktrees", leaf)
}

// snapDiffCommand / snapEditor read a hot-reloadable setting under the lock, so a
// concurrent SIGHUP Reload cannot race the read the ephemeral resolvers make.
func (s *Server) snapDiffCommand() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.diffCommand
}

func (s *Server) snapEditor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.editor
}

// closeEphemeral reaps every diff panel a client still has open, called when its
// connection drops so a client that vanishes mid-diff leaves no orphan PTY.
func (s *Server) closeEphemeral(cc *clientConn) {
	s.mu.Lock()
	ids := make([]string, 0, len(cc.ephemeral))
	for id := range cc.ephemeral {
		ids = append(ids, id)
		delete(s.ephemeral, id)
	}
	cc.ephemeral = make(map[string]bool)
	s.mu.Unlock()

	for _, id := range ids {
		// Hard-kill the process group before stopping, as the explicit-close path
		// does: a plain Stop is only SIGHUP, so a GUI difftool or backgrounded child
		// could outlive the dropped client. Ephemeral panels are safe to SIGKILL.
		s.pty.Signal(id, syscall.SIGKILL)
		s.pty.Stop(id)
	}
	if len(ids) > 0 {
		log.Info().Int("count", len(ids)).Msg("reaped ephemeral diff panels on disconnect")
	}
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
			delete(s.specs, p.ID) // purged for good; drop its retained spawn spec
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
	s.emit("group.change", map[string]any{"group": name})
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
		s.emit("group.change", map[string]any{})
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
	// The whole group is gone; drop its visible count so the map stays tidy.
	s.mu.Lock()
	delete(s.groupShown, name)
	s.emit("group.change", map[string]any{"group": name})
	s.mu.Unlock()
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
	// Carry the visible count to the new name, keyed by name like the group itself.
	if shown, ok := s.groupShown[old]; ok {
		s.groupShown[name] = shown
		delete(s.groupShown, old)
	}
	s.emit("group.change", map[string]any{"group": name, "from": old})
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

// targetIDs is the panels a command addresses: the IDs list, falling back to the
// single ID for a one-panel action. Shared by pin/unpin and signal.
func targetIDs(cmd proto.Command) []string {
	if len(cmd.IDs) > 0 {
		return cmd.IDs
	}
	if cmd.ID != "" {
		return []string{cmd.ID}
	}
	return nil
}

// signalPanels delivers the named signal to every listed panel's process group —
// one command signals a whole group at once. The name (or number) must be one the
// shared signals table resolves. Targets are validated against the fleet under the
// lock; an exited panel is skipped — its process is gone, so signalling it would
// be a silent no-op that still counted toward "sent". It errors only when no live
// panel matched, so the cockpit's reported count is the count actually delivered.
func (s *Server) signalPanels(ids []string, name string) error {
	sig, ok := signals.Lookup(name)
	if !ok {
		return fmt.Errorf("unknown signal %q", name)
	}
	if len(ids) == 0 {
		return fmt.Errorf("panel.signal needs at least one panel")
	}

	s.mu.Lock()
	var targets []string
	for _, id := range ids {
		if i := s.indexLocked(id); i >= 0 && s.panels[i].State != panel.Exited {
			targets = append(targets, id)
		}
	}
	s.mu.Unlock()
	if len(targets) == 0 {
		return fmt.Errorf("no live panel matched the given ids")
	}

	for _, id := range targets {
		s.pty.Signal(id, sig)
	}
	log.Info().Str("signal", name).Int("panels", len(targets)).Msg("signal sent")
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

// setGroupShown records a group's visible count — how many members stream as
// live tiles before the rest collapse into the summary tile. The count is clamped
// to [minVisible, maxVisible]; an empty group name is rejected. The group need not
// currently exist: a count may be set as the user curates, and a lingering entry is
// harmless (lifecycle cleanup keeps the map tidy on dissolve/rename).
func (s *Server) setGroupShown(group string, count int) error {
	group = strings.TrimSpace(group)
	if group == "" {
		return fmt.Errorf("group.show needs a group")
	}
	count = max(minVisible, min(count, maxVisible))

	s.mu.Lock()
	defer s.mu.Unlock()
	s.groupShown[group] = count
	s.emit("group.change", map[string]any{"group": group, "shown": count})
	log.Info().Str("group", group).Int("shown", count).Msg("group visible count set")
	return nil
}

func (s *Server) panelsMsg() proto.ServerMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]proto.Panel, len(s.panels))
	for i, p := range s.panels {
		out[i] = p.ToProto()
	}
	// Per-group view settings ride the snapshot, sorted by name for determinism.
	groups := make([]proto.GroupView, 0, len(s.groupShown))
	for g, shown := range s.groupShown {
		if shown != 0 {
			groups = append(groups, proto.GroupView{Group: g, Shown: shown})
		}
	}
	slices.SortFunc(groups, func(a, b proto.GroupView) int { return strings.Compare(a.Group, b.Group) })
	return proto.ServerMsg{Type: "panels", Panels: out, Groups: groups}
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
