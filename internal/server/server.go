// Package server is the headless baton core: the connection layer plus the
// single source of truth for panel state. Clients attach over the socket, send
// commands, and receive event broadcasts.
package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
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
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/queue"
	"github.com/cmj0121/baton/internal/signals"
	"github.com/cmj0121/baton/internal/state"
	"github.com/cmj0121/baton/internal/task"
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

	// role and self are declared on hello. role "conductor" fences this
	// connection under guardConductor; self is the conductor's own panel id, the
	// panel it is forbidden to close/signal/feed input to. They are written once
	// in the hello handler and thereafter only read, all on this connection's
	// single command-loop goroutine, so they need no lock. lastSpawn is the last
	// time this connection's panel.create was admitted, for the spawn-rate cap.
	role      string
	self      string
	lastSpawn time.Time
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
	// onFilterTask runs the synchronous task.pre hooks over a brief before it is
	// delivered: it may rewrite the prompt or veto the task. It blocks on the plugin
	// worker, so — unlike eventSink — it must be called WITHOUT s.mu held. A nil
	// filter (no plugin) passes every brief through unchanged.
	onFilterTask func(prompt, group string) (string, bool)
	clientConfig json.RawMessage
	pluginCmds   []proto.PluginCommand
	footerText   string // a plugin-set persistent footer segment (baton.footer); carried on config + pushed live

	mu      sync.Mutex
	seq     int
	panels  []panel.Panel
	clients map[*clientConn]struct{}
	mon     *monitor               // lifecycle + telemetry bookkeeping, guarded by mu
	specs   map[string]ptymgr.Spec // immutable spawn spec per panel id, for persistence + respawn (guarded by mu)

	// pendingDispatch holds a dispatch whose panel was not yet ready to receive it
	// (still spawning or mid-output): the bytes to write once the panel settles to
	// idle/attention. Keyed by panel id, guarded by mu; the monitor tick drains it.
	pendingDispatch map[string][]byte

	// Tasks. A dispatched prompt is promoted to a task.Task tracked through its
	// lifecycle; tasks holds them by id and panelTask maps a panel to its current
	// task, so a re-dispatch updates the same task (bumping Attempts). taskSeq names
	// tasks "t<n>" from a private counter. All guarded by mu.
	tasks     map[string]*task.Task
	panelTask map[string]string
	taskSeq   int

	// Queue. qstore is the on-disk backlog mirror ("" / nil when persistence is
	// off). queueMax caps the queued (unassigned) backlog; queueConcurrency caps
	// how many of a group's tasks run at once (0 = unlimited). taskDirty carries
	// task ids whose disk file the saver must refresh or remove.
	qstore           *queue.Store
	queueMax         int
	queueConcurrency int
	taskDirty        chan string

	// writeInput delivers input bytes to a panel's PTY. It is s.pty.Write in
	// production; a test swaps it (SetInputWriter) to record dispatched bytes
	// without a live process. Set once in New, then read without a lock.
	writeInput func(id string, data []byte)

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

	// groupLayout is the per-group split arrangement — the named layout (a preset
	// or a custom TUI.yaml layout) a group opens with. Keyed by group name; an
	// absent or empty entry means "use the client default" (tiled). Guarded by mu.
	groupLayout map[string]string

	// conductorPending reserves the conductor singleton across the unlocked spawn
	// in createPanel, so two near-simultaneous conductor.create calls cannot both
	// pass the "no conductor exists yet" check. Guarded by mu.
	conductorPending bool

	// Persistence. stateF is the snapshot path ("" disables persistence); dirty is
	// a 1-deep "save pending" nudge the saverLoop drains; saveMu serializes the
	// disk writes; bootTime is when this server (re)booted, persisted as LastBoot.
	stateF   string
	dirty    chan struct{}
	saveMu   sync.Mutex
	bootTime time.Time

	// heartbeat is the server→client ping cadence for each connection's keepalive
	// ticker. It defaults to proto.HeartbeatInterval; tests set it to milliseconds
	// so the heartbeat fires fast. Set before Serve; read once per handle().
	heartbeat time.Duration
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

// WithClock overrides the monitor's clock so a test can advance time without
// sleeping — the lifecycle transitions (idle/attention) and the dispatch gating
// they drive then become deterministic.
func WithClock(now func() time.Time) Option {
	return func(s *Server) { s.mon.now = now }
}

// defaultQueueMax is the built-in cap on the queued (unassigned) backlog when the
// config sets none: enough headroom for real fan-out, low enough to rein in a
// runaway producer.
const defaultQueueMax = 128

// WithQueue sets the backlog caps: max is the most queued tasks the backlog holds
// (0 = unlimited), concurrency is the most tasks one work item runs at once (0 =
// unlimited). A negative value is ignored, keeping the default.
func WithQueue(max, concurrency int) Option {
	return func(s *Server) {
		if max >= 0 {
			s.queueMax = max
		}
		if concurrency >= 0 {
			s.queueConcurrency = concurrency
		}
	}
}

// New builds a server bound to ln. The fleet starts empty — panels appear only
// when the user spawns a real one. Options are applied before the PTY manager is
// built, so settings like the replay size reach it.
func New(ln net.Listener, opts ...Option) *Server {
	s := &Server{
		ln:              ln,
		clients:         make(map[*clientConn]struct{}),
		mon:             newMonitor(),
		specs:           make(map[string]ptymgr.Spec),
		ephemeral:       make(map[string]struct{}),
		groupShown:      make(map[string]int),
		groupLayout:     make(map[string]string),
		pendingDispatch: make(map[string][]byte),
		tasks:           make(map[string]*task.Task),
		panelTask:       make(map[string]string),
		taskDirty:       make(chan string, 256),
		queueMax:        defaultQueueMax,
		dirty:           make(chan struct{}, 1),
		heartbeat:       proto.HeartbeatInterval,
	}
	for _, opt := range opts {
		opt(s)
	}

	// The task backlog mirrors to disk alongside the fleet snapshot, so it shares
	// the same on/off switch: a state file implies a sibling queue directory.
	if s.stateF != "" {
		s.qstore = queue.New(strings.TrimSuffix(s.stateF, ".state.json")+".queue", time.Now)
	}

	var pmOpts []ptymgr.Option
	if s.replayBytes > 0 {
		pmOpts = append(pmOpts, ptymgr.WithRingCap(s.replayBytes))
	}
	s.pty = ptymgr.New(pmOpts...)
	if s.writeInput == nil {
		s.writeInput = s.pty.Write
	}
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
			s.mon.forget(id)                     // a dead panel no longer ticks
			delete(s.pendingDispatch, id)        // a held dispatch dies with the process
			s.advanceTaskLocked(id, task.Failed) // a task in flight died with its panel
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
			s.advanceTaskLocked(id, task.Running) // output means the agent is working its task
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
	go s.taskSaverLoop(stop)

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

	changed := false
	var deliver []readyDispatch // pending dispatches whose panel settled this tick
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
			// The panel just settled. If a dispatch was held for it, deliver it and
			// move its task to dispatched; otherwise a running task whose agent has
			// gone quiet is finished — mark it done.
			if dispatchReady(ns) {
				if data, held := s.pendingDispatch[p.ID]; held {
					delete(s.pendingDispatch, p.ID)
					deliver = append(deliver, readyDispatch{id: p.ID, data: data})
					s.advanceTaskLocked(p.ID, task.Dispatched)
				} else {
					s.advanceTaskLocked(p.ID, task.Done)
				}
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

	// Drain the queued backlog onto any free idle agents this tick assignments
	// also produce deliveries and change panels, so they refresh the dashboard.
	if assigned := s.scheduleLocked(); len(assigned) > 0 {
		deliver = append(deliver, assigned...)
		changed = true
	}

	var out []proto.Panel
	if changed && len(s.clients) > 0 {
		out = make([]proto.Panel, len(s.panels))
		for i, p := range s.panels {
			out[i] = p.ToProto()
		}
	}
	s.mu.Unlock()

	// Deliver held dispatches outside the lock — a PTY write must not block under
	// mu, and a panel that just settled is waiting for input, so the write lands.
	for _, d := range deliver {
		s.writeInput(d.id, d.data)
	}

	if out == nil {
		return proto.ServerMsg{}, false
	}
	return proto.ServerMsg{Type: "telemetry", Panels: out}, true
}

// readyDispatch is a held dispatch whose panel settled this tick: the bytes to
// deliver once the monitor lock is released.
type readyDispatch struct {
	id   string
	data []byte
}

// handle serves one accepted client connection for its lifetime: it runs the
// handshake, then fans the connection into a reader (client commands), a writer
// (outbound broadcasts), and a heartbeat, tearing all three down together when
// any one fails.
func (s *Server) handle(conn net.Conn) {
	// closeOnce makes conn.Close idempotent across the reader, writer, and
	// heartbeat paths: whichever side fails first tears the connection down, and
	// the others observe the broken conn rather than racing a second Close.
	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { _ = conn.Close() }) }

	// done is closed exactly once, when the reader (this function) returns. The
	// writer and heartbeat goroutines watch it to stop. It couples the goroutines:
	// reader returns → done closes + conn closes → writer's Encode fails and the
	// heartbeat stops; conversely a writer failure closes the conn → the reader's
	// Decode fails → handle returns.
	done := make(chan struct{})

	cc := &clientConn{
		out:       make(chan proto.ServerMsg, proto.EventBufferSize),
		attached:  make(map[string]bool),
		ephemeral: make(map[string]bool),
	}
	s.addClient(cc)

	// hbDone signals the heartbeat goroutine has fully stopped. Teardown joins it
	// BEFORE removeClient closes cc.out, so the heartbeat — the one sender not
	// serialised by s.mu — can never send on a closed channel. removeClient stays
	// the sole closer of cc.out; closing it then unblocks the writer's range.
	hbDone := make(chan struct{})

	// Teardown runs in a fixed order on return: signal both goroutines (done) and
	// break the conn (closeConn) so the writer's Encode fails; JOIN the heartbeat;
	// then reap the client (closes cc.out, ending the writer's range) and any
	// ephemeral diff panels left open.
	defer func() {
		close(done)
		closeConn()
		<-hbDone
		s.removeClient(cc)
		s.closeEphemeral(cc)
	}()

	// Writer goroutine: the ONLY place this connection is encoded to. A single
	// json.Encoder lives here, so every server→client message — broadcasts and the
	// heartbeat ping alike — is serialised through one writer (the single-writer
	// invariant). On any encode error (incl. a write-deadline timeout) it tears the
	// conn down so the reader's Decode fails and handle() returns. It ranges over
	// cc.out until removeClient closes it, so a broadcast mid-teardown never blocks.
	go func() {
		enc := json.NewEncoder(conn)
		broken := false
		for msg := range cc.out {
			if broken {
				continue // conn is gone; drain queued messages until removeClient closes cc.out
			}
			_ = conn.SetWriteDeadline(time.Now().Add(proto.WriteTimeout))
			if err := enc.Encode(msg); err != nil {
				broken = true
				closeConn() // unblock the reader's Decode so handle() returns
			}
		}
	}()

	// Heartbeat ticker: every interval it queues a ping through the normal
	// send(cc, …) → cc.out path, so the writer goroutine remains the only thing
	// that ever encodes to this conn. It stops on done — and teardown waits for
	// hbDone before cc.out is closed — so it never sends on a closed channel.
	go func() {
		defer close(hbDone)
		t := time.NewTicker(s.heartbeat)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				send(cc, proto.ServerMsg{Type: "ping"})
			}
		}
	}()

	// Command loop. The initial hello carries a handshake read deadline so a
	// connect-but-never-speak peer is dropped; once the first command is read the
	// deadline is cleared, leaving the steady-state loop with no read deadline (a
	// client may legitimately stay idle for minutes).
	dec := json.NewDecoder(conn)
	_ = conn.SetReadDeadline(time.Now().Add(proto.HandshakeTimeout))
	first := true
	for {
		var cmd proto.Command
		if err := dec.Decode(&cmd); err != nil {
			return // client detached, timed out on the handshake, or the conn broke
		}
		if first {
			_ = conn.SetReadDeadline(time.Time{}) // idle command loop has no read deadline
			first = false
		}
		s.onCommand(cc, cmd)
	}
}

// roleConductor is the scoped role a control agent declares on hello.
const roleConductor = "conductor"

const (
	// maxConductorFleet caps how many panels may exist while a conductor is
	// driving, so a looping agent cannot fork-bomb the host. The conductor's own
	// panel counts toward it.
	maxConductorFleet = 64

	// minConductorSpawnGap throttles a conductor's panel.create rate: a tight
	// loop cannot spray panels faster than a person ever would.
	minConductorSpawnGap = 250 * time.Millisecond
)

// guardConductor returns a denial reason when cmd is forbidden for a scoped
// conductor connection, or "" when it is allowed. A non-conductor connection
// (the full-power cockpit) is never fenced. The conductor may arrange and drive
// the rest of the fleet, but not: stop the server; close, signal, or feed input
// to its OWN panel (the self id it declared on hello — closing it would kill the
// agent mid-command, an input loop would feed itself); or spawn faster than the
// rate cap / past the fleet ceiling. The fence is a guardrail against agent
// accidents over a uid-private socket, not a security boundary.
func (s *Server) guardConductor(cc *clientConn, cmd proto.Command) string {
	if cc.role != roleConductor {
		return ""
	}
	switch cmd.Action {
	case "server.reload":
		return "conductor role: reloading the server is not permitted"
	case "task.drain":
		return "conductor role: draining the backlog is an operator action"
	case "panel.close", "panel.signal", "panel.input", "panel.dispatch":
		// Self-targeted control is forbidden: closing/signalling itself kills the
		// agent mid-command, feeding itself input loops, or dispatching a task onto
		// its own panel. targetIDs folds in cmd.ID, so it covers panel.input and
		// panel.dispatch (which address a single panel) too.
		if cc.self != "" && slices.Contains(targetIDs(cmd), cc.self) {
			return "conductor role: cannot act on its own panel"
		}
	case "panel.create":
		now := time.Now()
		if !cc.lastSpawn.IsZero() && now.Sub(cc.lastSpawn) < minConductorSpawnGap {
			return "conductor role: spawning too fast, slow down"
		}
		s.mu.Lock()
		n := len(s.panels)
		s.mu.Unlock()
		if n >= maxConductorFleet {
			return fmt.Sprintf("conductor role: fleet at capacity (%d panels)", maxConductorFleet)
		}
		cc.lastSpawn = now
	}
	return ""
}

// onCommand maps a wire command onto a core action.
func (s *Server) onCommand(cc *clientConn, cmd proto.Command) {
	// A conductor connection is fenced: it may drive the fleet but not act on
	// itself, stop the server, or fork-bomb the host. Reject a forbidden command
	// before it reaches the action; everything else falls through unchanged.
	if reason := s.guardConductor(cc, cmd); reason != "" {
		send(cc, proto.ServerMsg{Type: "error", Error: reason})
		return
	}
	switch cmd.Action {
	case "hello":
		cc.role, cc.self = cmd.Role, cmd.Self
		send(cc, proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion, ServerVer: s.version})
		send(cc, s.panelsMsg())
		send(cc, statsMsg()) // seed the footer immediately, before the first tick
	case "panel.list":
		send(cc, s.panelsMsg())
	case "panel.create":
		if _, err := s.createPanel(cmd.Kind, cmd.Path, cmd.Args, cmd.Dir, cmd.Conductor); err != nil {
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
		// Compute the target agent's structured work-tree diff and reply with it; the
		// cockpit renders it as a master-detail popup. The git commands are one-shot
		// (run and reaped by sendDiff), so nothing lingers — no ephemeral panel reaches
		// the dashboard or the persisted state. A failure surfaces as an error.
		if err := s.sendDiff(cc, cmd.ID); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
	case "panel.git":
		// Run a git-menu op for the target agent. The non-interactive output ops reply
		// "gitout" with captured text (a popup); commit spawns a transient PTY panel
		// ("ephemeral", auto-zoomed) for its editor; worktree-add is a real fleet change
		// (broadcasts); worktree-remove confirms with a notice. Any failure surfaces as
		// an error, like panel.diff.
		if err := s.runGit(cc, cmd); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
	case "panel.scratch":
		// Spawn the cockpit's floating scratch shell as a transient ephemeral PTY —
		// off the fleet snapshot and the persisted state, reaped on close/disconnect —
		// and reply "scratch" with its id so the client attaches and floats it.
		if err := s.openScratch(cc, cmd.Path, cmd.Dir); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
	case "group.show":
		if err := s.setGroupShown(cmd.Group, cmd.Count); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		s.broadcastFleet()
	case "group.layout":
		if err := s.setGroupLayout(cmd.Group, cmd.Layout); err != nil {
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
	case "panel.dispatch":
		// Assign a task to a panel: record the brief and deliver it to the process as
		// a unit. Unlike panel.input (raw keystrokes), the server knows the objective,
		// so it reaches every frontend's card and the snapshot.
		s.dispatchFiltered(cc, cmd.Prompt, "", func(p string) error {
			return s.dispatchPanel(cmd.ID, p, cmd.Submit)
		})
	case "panel.dispatch-group":
		// Fan one task to every member of a work item — the mechanic behind racing N
		// agents on the same prompt. The reply error names an empty/unknown group.
		s.dispatchFiltered(cc, cmd.Prompt, cmd.Group, func(p string) error {
			_, err := s.dispatchGroup(cmd.Group, p, cmd.Submit)
			return err
		})
	case "task.enqueue":
		// Add a task to the backlog; the scheduler drains it onto a free agent. The
		// reply error names a full queue.
		s.dispatchFiltered(cc, cmd.Prompt, cmd.Group, func(p string) error {
			_, err := s.enqueueTask(p, cmd.Group)
			return err
		})
	case "task.list":
		send(cc, s.tasksMsg())
	case "task.cancel":
		if err := s.cancelTask(cmd.ID); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		send(cc, s.tasksMsg())
	case "task.drain":
		s.drainQueued()
		send(cc, s.tasksMsg())
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
//
// A conductor panel is a special agent: the server enforces at most one, runs it
// in a fresh ephemeral workspace (not any source tree) instead of dir, and
// injects the socket + identity env so the agent inside can drive the fleet under
// the scoped conductor role.
func (s *Server) createPanel(kind, path string, args []string, dir string, conductor bool) (string, error) {
	if kind == "" {
		kind = proto.KindShell
	}
	if conductor {
		kind = proto.KindAgent // a conductor is always an agent
	}

	s.mu.Lock()
	if conductor && s.hasConductorLocked() {
		s.mu.Unlock()
		return "", fmt.Errorf("a conductor already exists")
	}
	if conductor {
		s.conductorPending = true // reserve the singleton across the unlocked spawn below
	}
	s.seq++
	id := fmt.Sprintf("%d", s.seq)
	if dir == "" {
		dir = s.defaultDir // read under the lock so a SIGHUP reload cannot race it; empty still falls back to home
	}
	s.mu.Unlock()

	// A conductor runs in a server-managed ephemeral workspace, never dir, and
	// carries the identity env. Build them before the spec so a failure cleans up
	// the reservation and any half-made workspace.
	var env []string
	if conductor {
		ws, err := s.makeConductorWorkspace(id)
		if err != nil {
			s.clearConductorPending()
			return "", err
		}
		dir, env = ws, s.conductorEnv(id)
	}

	// Build the spawn spec once, then use the same value to start the PTY and to
	// stash for respawn — so a restored panel re-runs with exactly what launched it
	// (a shell carries no args; an agent does).
	var spec ptymgr.Spec
	switch kind {
	case proto.KindShell:
		spec = ptymgr.Spec{Command: path, Dir: dir}
	case proto.KindAgent:
		if path == "" {
			s.clearConductorPending()
			return "", fmt.Errorf("an agent panel needs a command")
		}
		spec = ptymgr.Spec{Command: path, Args: args, Dir: dir, Env: env}
	default:
		return "", fmt.Errorf("unknown panel kind %q", kind)
	}
	if err := s.pty.StartCmd(id, spec); err != nil {
		if conductor {
			_ = os.RemoveAll(dir) // drop the workspace we just made
			s.clearConductorPending()
		}
		return "", err
	}

	p := panel.Panel{
		ID:        id,
		Kind:      panel.ParseKind(kind),
		Title:     panelTitle(kind, path, dir, id),
		State:     panel.Spawning,
		Activity:  activityText(panel.Spawning, 0), // the Monitor keeps it live from here
		Conductor: conductor,
	}
	if conductor {
		p.Title = "conductor · " + id
	}
	s.mu.Lock()
	s.panels = append(s.panels, p)
	s.specs[id] = spec // the exact spec StartCmd launched, so respawn reproduces it
	s.mon.spawned(id)  // start the Monitor's clock; first output wakes it to running
	if conductor {
		s.conductorPending = false // the singleton is now a real panel
	}
	s.emit("panel.spawn", panelFields(p))
	s.mu.Unlock()

	log.Info().Str("panel", p.Title).Msg("panel created")
	return id, nil
}

// hasConductorLocked reports whether a conductor panel already exists or is mid-
// spawn. It holds the singleton invariant: a second conductor.create is refused
// while the first is live (running or an exited dead slot) or being created.
// Caller holds s.mu.
func (s *Server) hasConductorLocked() bool {
	if s.conductorPending {
		return true
	}
	for _, p := range s.panels {
		if p.Conductor {
			return true
		}
	}
	return false
}

func (s *Server) clearConductorPending() {
	s.mu.Lock()
	s.conductorPending = false
	s.mu.Unlock()
}

// conductorEnv is the identity baton injects into the conductor panel's process:
// the control socket to dial and the scoped role + own panel id the control
// client declares on hello, so `baton ctl` inside the panel is fenced to the
// conductor policy and knows which panel not to act on.
func (s *Server) conductorEnv(id string) []string {
	return []string{
		paths.EnvSocket + "=" + s.socketPath(),
		paths.EnvRole + "=" + roleConductor,
		paths.EnvPanelID + "=" + id,
	}
}

// socketPath is the control socket this server listens on, taken from the live
// listener so it is correct even in tests that bind an explicit path.
func (s *Server) socketPath() string {
	if s.ln != nil {
		if addr := s.ln.Addr(); addr != nil {
			return addr.String()
		}
	}
	return paths.Socket()
}

// makeConductorWorkspace creates a fresh conductor workspace and seeds it with the
// control wiring (see writeConductorFiles).
func (s *Server) makeConductorWorkspace(id string) (string, error) {
	ws, err := paths.NewConductorWorkspace()
	if err != nil {
		return "", fmt.Errorf("create conductor workspace: %w", err)
	}
	writeConductorFiles(ws, id)
	return ws, nil
}

// writeConductorFiles (re)writes the conductor's workspace wiring, so the agent's
// only local surface is how to drive baton: the briefing and a .mcp.json pointing
// an MCP-speaking agent at `baton mcp`. The briefing is written to BATON.md (the
// canonical, agent-agnostic name) and to CLAUDE.md, which the default Claude
// conductor auto-reads as its project instructions, so it ingests the mission with
// no extra wiring; the CLAUDE.md copy is harmless for other agents. It is called on
// every spawn and respawn, so an edited operator brief ($HOME/.baton/CONDUCTOR.md)
// is re-read each time the conductor is opened. All writes are best-effort — a
// missing file just costs a hint or the auto-loaded tools, not correctness.
func writeConductorFiles(ws, id string) {
	briefing := conductorBriefing(id)
	_ = os.WriteFile(filepath.Join(ws, "BATON.md"), briefing, 0o600)
	_ = os.WriteFile(filepath.Join(ws, "CLAUDE.md"), briefing, 0o600)
	_ = os.WriteFile(filepath.Join(ws, ".mcp.json"), conductorMCPConfig(), 0o600)
}

// conductorBriefing is the full BATON.md: the built-in control primer, plus the
// operator's own goal and guide from $HOME/.baton/CONDUCTOR.md when it is present
// and non-empty. The operator brief is appended (never replaces the primer), so
// the agent always keeps the mechanics and forbidden actions.
func conductorBriefing(id string) []byte {
	b := conductorPrimer(id)
	guide, err := os.ReadFile(paths.ConductorFile())
	if err != nil || strings.TrimSpace(string(guide)) == "" {
		return b
	}
	b = append(b, []byte("\n---\n\n# Operator's brief\n\nYour operator wrote this in "+
		paths.ConductorFile()+" to set your goal — follow it:\n\n")...)
	b = append(b, guide...)
	if !strings.HasSuffix(string(guide), "\n") {
		b = append(b, '\n')
	}
	return b
}

// conductorMCPConfig is the .mcp.json dropped into the conductor workspace so an
// MCP-aware agent (Claude Code) auto-loads baton's fleet-control tools. It points
// at this very baton binary, run as `baton mcp`; the MCP subprocess inherits the
// conductor panel's env (BATON_SOCK/role/self), so it is fenced like the CLI.
func conductorMCPConfig() []byte {
	bin := "baton"
	if exe, err := os.Executable(); err == nil && exe != "" {
		bin = exe
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"baton": map[string]any{
				"command": bin,
				"args":    []string{"mcp"},
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return []byte(`{"mcpServers":{"baton":{"command":"baton","args":["mcp"]}}}`)
	}
	return data
}

// conductorPrimer is the control crib sheet dropped into the conductor's
// workspace. It tells the agent it drives the fleet through `baton ctl` and what
// it may and may not do under the scoped role.
func conductorPrimer(id string) []byte {
	return []byte(`# You are the baton conductor

You are an AI agent running inside baton — a terminal multiplexer for AI coding
agents. You are the **conductor**: you orchestrate the other panels (agents and
shells) in the fleet. You have no source code here; this workspace exists only so
you can drive baton.

If you speak MCP, baton's tools are auto-loaded from .mcp.json: ` +
		"`baton_list`, `baton_spawn`, `baton_send`, `baton_group`, `baton_rename`, " +
		"`baton_pin`, `baton_unpin`, `baton_signal`, `baton_close`" + `. Prefer them.

Either way, the same verbs are available as the ` + "`baton ctl`" + ` command:

    baton ctl list                       # the fleet, as JSON (ids, titles, state, group)
    baton ctl spawn --agent claude --dir /path/to/repo   # start an agent; prints its id
    baton ctl spawn --dir /path/to/repo  # start a shell panel
    baton ctl send <id> "a prompt"       # type a prompt into a panel and submit it
    baton ctl group <name> <id> <id>     # file panels under a work item
    baton ctl rename --id <id> <name>    # rename a panel
    baton ctl pin <id>                   # pin a panel to a live tile
    baton ctl signal SIGINT <id>         # signal a panel
    baton ctl close <id>                 # close a panel

You may arrange and drive every other panel. You may NOT act on your own panel
(id ` + id + `), reload the server, or spawn faster than the rate cap — the
server will refuse these.

If an "Operator's brief" section follows below, your operator wrote it to set
your goal — treat it as your standing instructions.
`)
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
			ID:        p.ID,
			Kind:      p.Kind.String(),
			Title:     p.Title,
			Group:     p.Group,
			Task:      p.Task,
			Pinned:    p.Pinned,
			Conductor: p.Conductor,
			Spec:      state.Spec{Command: spec.Command, Args: spec.Args, Dir: spec.Dir},
		}
	}
	// Per-group view settings (the visible counts and the chosen layout), keyed by
	// name like the group, so a restart restores how each group was arranged.
	gviews := make(map[string]*state.GroupLayout)
	gview := func(g string) *state.GroupLayout {
		v, ok := gviews[g]
		if !ok {
			v = &state.GroupLayout{Group: g}
			gviews[g] = v
		}
		return v
	}
	for g, shown := range s.groupShown {
		if shown != 0 {
			gview(g).Shown = shown
		}
	}
	for g, layout := range s.groupLayout {
		if layout != "" {
			gview(g).Layout = layout
		}
	}
	groups := make([]state.GroupLayout, 0, len(gviews))
	for _, v := range gviews {
		groups = append(groups, *v)
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
			ID:        ps.ID,
			Kind:      panel.ParseKind(ps.Kind),
			Title:     ps.Title,
			Group:     ps.Group,
			Task:      ps.Task,
			Pinned:    ps.Pinned,
			Conductor: ps.Conductor,
			State:     panel.Exited,
			Activity:  "restored · press r to re-run",
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
		if g.Layout != "" {
			s.groupLayout[g.Group] = g.Layout
		}
	}
	s.restoreTasksLocked()
	log.Info().Int("panels", len(st.Panels)).Int("seq", s.seq).Int("tasks", len(s.tasks)).Msg("state restored")
}

// restoreTasksLocked reloads the on-disk backlog into the task table. Every
// restored panel comes back exited, so a task that was in flight on one is
// orphaned: it is re-queued (unassigned, kept id and attempts) for the scheduler
// to redrive once agents are running again. A malformed file is quarantined aside.
// taskSeq is bumped past the highest restored id so a new task never collides.
// Caller holds s.mu; a no-op when persistence is off.
func (s *Server) restoreTasksLocked() {
	if s.qstore == nil {
		return
	}
	tasks, bad, err := s.qstore.LoadAll()
	if err != nil {
		log.Warn().Err(err).Msg("could not load task backlog")
		return
	}
	for _, id := range bad {
		_ = s.qstore.Quarantine(id)
	}
	for _, t := range tasks {
		tk := t
		if tk.Status.Terminal() {
			continue // a terminal task should not have a live file; drop it
		}
		if tk.Panel != "" { // was in flight on a now-dead panel — orphaned, re-queue it
			tk.Panel = ""
			tk.Status = task.Queued
		}
		s.tasks[tk.ID] = &tk
		_ = s.qstore.Save(tk) // rewrite the re-queued shape
		if n := taskSeqNum(tk.ID); n > s.taskSeq {
			s.taskSeq = n
		}
	}
}

// taskSeqNum parses the numeric part of a "t<n>" task id, or -1 if it does not fit
// the shape, so the restored counter can clear the highest seen id.
func taskSeqNum(id string) int {
	if len(id) < 2 || id[0] != 't' {
		return -1
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil {
		return -1
	}
	return n
}

// defaultSubmit is the bytes appended to a dispatched prompt to submit it — a
// newline, the same rule control.SendText uses. A dispatch may override it (some
// REPLs want a different sequence), but the hard default lives here, not inline.
const defaultSubmit = "\n"

// dispatchData is the bytes a dispatch delivers: the prompt followed by its submit
// sequence (the default when the caller gives none).
func dispatchData(prompt, submit string) []byte {
	if submit == "" {
		submit = defaultSubmit
	}
	return append([]byte(prompt), submit...)
}

// dispatchPanel is a core action: it records prompt as the panel's task brief and
// delivers it to the panel's process as a unit — the prompt text followed by a
// submit sequence. Unlike raw panel.input, the server keeps the brief on the
// panel, so it reaches every frontend's card and is persisted to survive a
// restart. An empty id, unknown panel, or empty prompt errors — dispatch is
// "assign a task", not "clear it".
//
// Delivery is gated on readiness: a panel still spawning or mid-output is not
// ready to receive a prompt, so the bytes are held in pendingDispatch and the
// monitor tick delivers them once the panel settles to idle/attention. A panel
// already settled is written immediately. The brief is recorded either way.
func (s *Server) dispatchPanel(id, prompt, submit string) error {
	if id == "" {
		return fmt.Errorf("panel.dispatch needs an id")
	}
	if prompt == "" {
		return fmt.Errorf("panel.dispatch needs a prompt")
	}
	data := dispatchData(prompt, submit)

	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no panel with id %q", id)
	}
	s.panels[idx].Task = prompt
	ready := dispatchReady(s.panels[idx].State)
	status := task.Queued
	if ready {
		delete(s.pendingDispatch, id) // a fresh immediate dispatch supersedes a held one
		status = task.Dispatched
	} else {
		s.pendingDispatch[id] = data // deliver when the panel next settles
	}
	s.upsertTaskLocked(id, prompt, s.panels[idx].Group, status)
	s.mu.Unlock()

	if ready {
		s.writeInput(id, data)
	}
	s.markDirty() // persist the brief so a restart restores it
	return nil
}

// markTaskDirtyLocked nudges the task saver to refresh (or remove) a task's disk
// file. It is a non-blocking hand-off — a full channel drops the nudge, and the
// next change re-sends it — so it is safe to call under mu. A no-op when there is
// no backlog store. Caller holds s.mu.
func (s *Server) markTaskDirtyLocked(id string) {
	if s.qstore == nil {
		return
	}
	select {
	case s.taskDirty <- id:
	default:
	}
}

// taskSaverLoop mirrors task changes to the on-disk backlog: it saves a live task
// and removes a terminal or vanished one, serialising the disk I/O off the command
// path. It stops when Serve returns.
func (s *Server) taskSaverLoop(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case id := <-s.taskDirty:
			s.mu.Lock()
			t, ok := s.tasks[id]
			var snapshot task.Task
			remove := !ok
			if ok {
				snapshot = *t
				remove = t.Status.Terminal()
			}
			s.mu.Unlock()
			var err error
			if remove {
				err = s.qstore.Remove(id)
			} else {
				err = s.qstore.Save(snapshot)
			}
			if err != nil {
				log.Warn().Str("task", id).Bool("remove", remove).Err(err).Msg("could not persist task")
			}
		}
	}
}

// taskFields is the event payload for a task — the shape the Lua worker turns
// into the table a task.change handler receives.
func taskFields(t *task.Task) map[string]any {
	return map[string]any{
		"id":       t.ID,
		"prompt":   t.Prompt,
		"status":   string(t.Status),
		"panel":    t.Panel,
		"group":    t.Group,
		"attempts": t.Attempts,
	}
}

// upsertTaskLocked records a dispatch as a task and emits task.change. A panel
// whose current task is still live is re-dispatched in place — same id, a bumped
// Attempts — so iterating on a busy agent keeps one task; otherwise a new task is
// created. Caller holds s.mu.
func (s *Server) upsertTaskLocked(panelID, prompt, group string, status task.Status) *task.Task {
	now := s.mon.now()
	if tid, ok := s.panelTask[panelID]; ok {
		if t := s.tasks[tid]; t != nil && !t.Status.Terminal() {
			t.Prompt, t.Group, t.Status = prompt, group, status
			t.Attempts++
			t.Updated = now
			s.emit("task.change", taskFields(t))
			s.markTaskDirtyLocked(t.ID)
			return t
		}
	}
	s.taskSeq++
	t := &task.Task{
		ID: fmt.Sprintf("t%d", s.taskSeq), Prompt: prompt, Status: status,
		Panel: panelID, Group: group, Attempts: 1, Created: now, Updated: now,
	}
	s.tasks[t.ID] = t
	if panelID != "" {
		s.panelTask[panelID] = t.ID
	}
	s.emit("task.change", taskFields(t))
	s.markTaskDirtyLocked(t.ID)
	return t
}

// advanceTaskLocked moves a panel's current task to status when the lifecycle
// permits it (see task.CanAdvance), emitting task.change on a real move. It is the
// one place the panel lifecycle drives the task lifecycle. Caller holds s.mu.
func (s *Server) advanceTaskLocked(panelID string, status task.Status) {
	tid, ok := s.panelTask[panelID]
	if !ok {
		return
	}
	t := s.tasks[tid]
	if t == nil || !task.CanAdvance(t.Status, status) {
		return
	}
	t.Status = status
	t.Updated = s.mon.now()
	s.emit("task.change", taskFields(t))
	s.markTaskDirtyLocked(t.ID)
}

// dispatchReady reports whether a panel in this state can receive a dispatched
// prompt now: a settled agent (idle or waiting for input) is ready; one still
// spawning or actively producing output is not, so the dispatch is held.
func dispatchReady(st panel.State) bool {
	return st == panel.Idle || st == panel.Attention
}

// enqueueTask adds an unassigned task to the backlog for the scheduler to drain
// onto a free agent. It errors when the queued backlog is at queueMax — the cap is
// backpressure on a runaway producer, counting only unassigned tasks, so a busy
// fleet never blocks new work from being queued.
func (s *Server) enqueueTask(prompt, group string) (string, error) {
	if prompt == "" {
		return "", fmt.Errorf("task.enqueue needs a prompt")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queueMax > 0 && s.queuedBacklogLenLocked() >= s.queueMax {
		return "", fmt.Errorf("queue is full (%d queued); raise queue.max or let it drain", s.queueMax)
	}
	t := s.upsertTaskLocked("", prompt, group, task.Queued)
	return t.ID, nil
}

// queuedBacklogLenLocked counts the unassigned queued tasks — the backlog depth
// queueMax caps. Caller holds s.mu.
func (s *Server) queuedBacklogLenLocked() int {
	n := 0
	for _, t := range s.tasks {
		if t.Panel == "" && t.Status == task.Queued {
			n++
		}
	}
	return n
}

// freeIdleAgentLocked finds an idle agent panel that can take a task in group: an
// agent (never the conductor) sitting idle, in the matching group when one is
// named, with no live task of its own. Caller holds s.mu.
func (s *Server) freeIdleAgentLocked(group string) (string, bool) {
	for i := range s.panels {
		p := &s.panels[i]
		if p.Kind != panel.Agent || p.Conductor || p.State != panel.Idle {
			continue
		}
		if group != "" && p.Group != group {
			continue
		}
		if tid, ok := s.panelTask[p.ID]; ok {
			if t := s.tasks[tid]; t != nil && !t.Status.Terminal() {
				continue // already running a task
			}
		}
		return p.ID, true
	}
	return "", false
}

// scheduleLocked drains the queued backlog onto free idle agents, oldest task
// first, honouring the per-group concurrency cap. It assigns the task to the panel
// (recording the brief, moving the task to dispatched) and returns the prompts to
// deliver once the lock is released. The scheduler never spawns a panel — it
// distributes work across the agents already in the fleet. Caller holds s.mu.
func (s *Server) scheduleLocked() []readyDispatch {
	// One pass over the task table: collect the unassigned backlog and tally each
	// group's in-flight (dispatched/running) count, so the per-group cap is a map
	// lookup per candidate rather than a full rescan.
	var queued []*task.Task
	groupRunning := map[string]int{}
	for _, t := range s.tasks {
		switch {
		case t.Panel == "" && t.Status == task.Queued:
			queued = append(queued, t)
		case t.Status == task.Dispatched || t.Status == task.Running:
			groupRunning[t.Group]++
		}
	}
	if len(queued) == 0 {
		return nil
	}
	sort.Slice(queued, func(i, j int) bool { return queued[i].Created.Before(queued[j].Created) })

	var deliver []readyDispatch
	for _, t := range queued {
		if s.queueConcurrency > 0 && groupRunning[t.Group] >= s.queueConcurrency {
			continue
		}
		pid, ok := s.freeIdleAgentLocked(t.Group)
		if !ok {
			continue
		}
		if idx := s.indexLocked(pid); idx >= 0 {
			s.panels[idx].Task = t.Prompt
		}
		t.Panel = pid
		t.Status = task.Dispatched
		t.Attempts++
		t.Updated = s.mon.now()
		s.panelTask[pid] = t.ID
		groupRunning[t.Group]++ // the fresh dispatch counts against the cap for later tasks
		s.emit("task.change", taskFields(t))
		s.markTaskDirtyLocked(t.ID)
		deliver = append(deliver, readyDispatch{id: pid, data: dispatchData(t.Prompt, "")})
	}
	return deliver
}

// cancelTask removes a queued, unassigned task from the backlog. A task already
// dispatched or running is in flight on a panel — cancel that by closing or
// signalling the panel — so only a waiting task can be cancelled here.
func (s *Server) cancelTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("no task %q", id)
	}
	if t.Panel != "" || t.Status != task.Queued {
		return fmt.Errorf("task %q is already in flight; close its panel instead", id)
	}
	delete(s.tasks, id)
	s.markTaskDirtyLocked(id)
	return nil
}

// drainQueued clears every unassigned queued task, returning how many it dropped.
// In-flight tasks (dispatched/running on a panel) are left to finish — draining the
// backlog is not the same as stopping the fleet.
func (s *Server) drainQueued() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, t := range s.tasks {
		if t.Panel == "" && t.Status == task.Queued {
			delete(s.tasks, id)
			s.markTaskDirtyLocked(id)
			n++
		}
	}
	return n
}

// tasksMsg builds the backlog snapshot reply, newest activity first, so a frontend
// can render the queue/kanban.
func (s *Server) tasksMsg() proto.ServerMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks := make([]*task.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Updated.After(tasks[j].Updated) })
	wire := make([]proto.Task, len(tasks))
	for i, t := range tasks {
		wire[i] = proto.Task{
			ID: t.ID, Prompt: t.Prompt, Status: string(t.Status), Panel: t.Panel,
			Group: t.Group, Result: t.Result, Attempts: t.Attempts,
		}
	}
	return proto.ServerMsg{Type: "tasks", Tasks: wire}
}

// dispatchGroup dispatches one prompt to every member of a named group, returning
// how many it reached. The conductor panel is never a target — a group dispatch
// cannot loop the control agent back onto itself. An unknown or empty group, or
// one with no dispatchable member, errors.
func (s *Server) dispatchGroup(group, prompt, submit string) (int, error) {
	if group == "" {
		return 0, fmt.Errorf("panel.dispatch-group needs a group")
	}
	if prompt == "" {
		return 0, fmt.Errorf("panel.dispatch-group needs a prompt")
	}
	s.mu.Lock()
	var ids []string
	for _, p := range s.panels {
		if p.Group == group && !p.Conductor {
			ids = append(ids, p.ID)
		}
	}
	s.mu.Unlock()
	if len(ids) == 0 {
		return 0, fmt.Errorf("no panel in group %q", group)
	}
	for _, id := range ids {
		_ = s.dispatchPanel(id, prompt, submit) // each id came from the fleet a moment ago
	}
	return len(ids), nil
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
	isConductor := s.panels[idx].Conductor
	spec, ok := s.specs[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("nothing to re-run")
	}

	// A conductor re-run needs a live workspace and fresh identity env: reuse the
	// retained workspace if it still exists (the common exit→respawn), make a new
	// one if it is gone (e.g. after a reboot cleared the runtime dir), and always
	// refresh the env since the socket path can change across a daemon restart.
	// Rewrite the workspace wiring either way, so an edited operator brief
	// ($HOME/.baton/CONDUCTOR.md) is picked up on every re-run.
	if isConductor {
		if spec.Dir == "" || !dirExists(spec.Dir) {
			ws, err := paths.NewConductorWorkspace()
			if err != nil {
				return err
			}
			spec.Dir = ws
		}
		writeConductorFiles(spec.Dir, id)
		spec.Env = s.conductorEnv(id)
		s.mu.Lock()
		s.specs[id] = spec
		s.mu.Unlock()
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
	workspace := "" // a conductor's ephemeral workspace, removed once the panel is gone
	if s.panels[idx].Conductor {
		workspace = s.specs[id].Dir
	}
	s.advanceTaskLocked(id, task.Failed) // closing a panel mid-task abandons it
	s.panels = slices.Delete(s.panels, idx, idx+1)
	s.mon.forget(id)
	delete(s.specs, id)           // the panel is gone for good; drop its retained spawn spec
	delete(s.pendingDispatch, id) // and any dispatch held for it
	delete(s.panelTask, id)       // and its task mapping (the task record stays as history)
	s.emit("panel.close", map[string]any{"id": id, "title": title})
	s.mu.Unlock()

	s.pty.Stop(id) // no-op for a panel with no live process
	if workspace != "" {
		_ = os.RemoveAll(workspace)
	}
	log.Info().Str("panel", title).Msg("panel closed")
	return nil
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// sendDiff replies with the agent panel targetID's work-tree diff. The default is
// a structured {type:"diff", id:targetID, files:[…]} reply — one entry per changed
// path with its staged and unstaged diff text — which the cockpit renders as a
// master-detail popup. A user-configured explicit diff-command cannot be split
// per-file, so it keeps the old behaviour: a transient, auto-zoomed PTY via
// openEphemeral (which replies "ephemeral"). Either way nothing structured is
// persisted. The git probes run with s.mu released — they shell out and must never
// hold the server lock.
func (s *Server) sendDiff(cc *clientConn, targetID string) error {
	spec, err := s.agentTargetSpec(targetID, "diff")
	if err != nil {
		log.Warn().Str("target", targetID).Str("action", "diff").Err(err).Msg("diff rejected")
		return err
	}
	dir := ptymgr.PanelDir(spec.Dir)
	if !gitdiff.IsWorkTree(dir) {
		return fmt.Errorf("not a git repository: %s", dir)
	}
	if !gitdiff.HasChanges(dir) {
		return fmt.Errorf("no uncommitted changes")
	}

	// A user-configured explicit diff-command can't be split per file, so it keeps
	// the old behaviour: a transient, auto-zoomed PTY. openEphemeral re-resolves the
	// same target and replies "ephemeral".
	if diffCommand := s.snapDiffCommand(); diffCommand != "" {
		return s.openEphemeral(cc, targetID, "diff", func(dir string) (string, []string, []string, error) {
			name, args := gitdiff.ResolveCommand(dir, diffCommand)
			return name, args, nil, nil
		})
	}

	changes, err := gitdiff.Collect(dir)
	if err != nil {
		return fmt.Errorf("could not read diff: %w", err)
	}
	files := make([]proto.DiffFile, len(changes))
	for i, c := range changes {
		files[i] = proto.DiffFile{Path: c.Path, Index: c.Index, Work: c.Work, Staged: c.Staged, Unstaged: c.Unstaged}
	}
	log.Info().Str("target", targetID).Str("dir", dir).Int("files", len(files)).Msg("diff sent")
	send(cc, proto.ServerMsg{Type: "diff", ID: targetID, Files: files})
	return nil
}

// runGit dispatches a panel.git op for the target agent. The non-interactive output
// ops (status/log/add/push/branch/worktree-list) run synchronously and reply
// "gitout" with their captured text, which the cockpit shows in a scrollable popup;
// commit needs an editor, so it alone keeps the transient-PTY path via openGit;
// worktree-add creates a tree and spawns an agent in it (a fleet change, so it
// broadcasts); worktree-remove runs synchronously and confirms with a notice.
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
	case gitops.OpCommit:
		// commit opens $EDITOR, which needs a real terminal, so it keeps the
		// transient, auto-zoomed PTY rather than a captured popup.
		return s.openGit(cc, cmd.ID, op, cmd.Name)
	default:
		return s.captureGit(cc, cmd.ID, op, cmd.Name)
	}
}

// captureGit runs a non-interactive output op for the target agent and replies with
// a structured {type:"gitout", id, text} the cockpit shows in a scrollable popup —
// the text sibling of the diff popup. Like the diff probes it spawns and persists
// nothing, and runs with s.mu released since it shells out to git. A non-zero exit
// still opens the popup (the failed flag tints it) so the user sees git's message;
// only a pre-flight failure (not a repo, nothing to do) surfaces as an error.
func (s *Server) captureGit(cc *clientConn, targetID string, op gitops.Op, arg string) error {
	spec, err := s.agentTargetSpec(targetID, "git")
	if err != nil {
		log.Warn().Str("target", targetID).Str("op", string(op)).Err(err).Msg("git rejected")
		return err
	}
	dir := ptymgr.PanelDir(spec.Dir)
	res, err := gitops.Capture(op, dir, arg, s.snapEditor())
	if err != nil {
		log.Warn().Str("target", targetID).Str("dir", dir).Str("op", string(op)).Err(err).Msg("git rejected")
		return err
	}
	log.Info().Str("target", targetID).Str("dir", dir).Str("op", string(op)).Bool("failed", res.Failed).Msg("git captured")
	send(cc, proto.ServerMsg{Type: "gitout", ID: targetID, Text: res.Output, Failed: res.Failed})
	return nil
}

// openGit spawns a transient panel running commit in the target agent's workdir,
// resolved by the gitops layer with the configured commit editor. The other output
// ops capture to a popup via captureGit; only commit, which drives $EDITOR, still
// needs a live PTY.
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
	spec, err := s.agentTargetSpec(targetID, label)
	if err != nil {
		log.Warn().Str("target", targetID).Str("action", label).Err(err).Msg("ephemeral rejected")
		return err
	}

	// Resolve the effective workdir exactly as a spawn would (empty → home), then
	// let the caller resolve the command (and its git-specific gates) against it.
	dir := ptymgr.PanelDir(spec.Dir)
	name, args, env, err := resolve(dir)
	if err != nil {
		log.Warn().Str("target", targetID).Str("dir", dir).Str("action", label).Err(err).Msg("ephemeral rejected")
		return err
	}

	ephID, unwind, err := s.registerEphemeral(cc, label)
	if err != nil {
		log.Warn().Str("target", targetID).Str("action", label).Err(err).Msg("ephemeral rejected")
		return err
	}
	if err := s.pty.StartCmd(ephID, ptymgr.Spec{Command: name, Args: args, Env: env, Dir: dir}); err != nil {
		unwind()
		err = fmt.Errorf("could not open %s: %w", label, err)
		log.Warn().Str("target", targetID).Str("dir", dir).Str("action", label).Err(err).Msg("ephemeral spawn failed")
		return err
	}

	log.Info().Str("panel", ephID).Str("target", targetID).Str("dir", dir).Str("action", label).Msg("ephemeral panel opened")
	send(cc, proto.ServerMsg{Type: "ephemeral", ID: ephID})
	return nil
}

// registerEphemeral allocates and registers a transient panel id for a connection,
// enforcing the per-connection cap — the shared bookkeeping behind openEphemeral and
// openScratch. It bumps ephSeq and records the id in both s.ephemeral and
// cc.ephemeral under one lock, so a concurrent disconnect cleanup sees a consistent
// set and two opens cannot slip past the cap. It returns the id and an unwind func
// the caller must invoke if the spawn then fails, to drop the reservation. label
// prefixes the id ("diff:3", "scratch:7").
func (s *Server) registerEphemeral(cc *clientConn, label string) (string, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(cc.ephemeral) >= maxEphemeralPerConn {
		return "", nil, fmt.Errorf("too many open panels (max %d) — close one first", maxEphemeralPerConn)
	}
	s.ephSeq++
	ephID := fmt.Sprintf("%s:%d", label, s.ephSeq)
	s.ephemeral[ephID] = struct{}{}
	cc.ephemeral[ephID] = true
	unwind := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.ephemeral, ephID)
		delete(cc.ephemeral, ephID)
	}
	return ephID, unwind, nil
}

// openScratch spawns a transient shell PTY for the cockpit's floating scratch pane
// — the standalone sibling of openEphemeral, with no agent target. Like a diff
// panel it is registered only in s.ephemeral (and the owning conn), so it never
// reaches the fleet snapshot (panelsMsg) or the persisted state (snapshotState),
// and it is reaped when the client closes it (panel.close on an ephemeral id) or
// disconnects. cmd is the program to run (empty = the default shell) in dir. On
// success it replies {type:"scratch", id:"scratch:<n>"} so the client attaches and
// floats it, rather than the auto-zoom an "ephemeral" reply drives.
func (s *Server) openScratch(cc *clientConn, cmd, dir string) error {
	dir = ptymgr.PanelDir(dir)

	ephID, unwind, err := s.registerEphemeral(cc, "scratch")
	if err != nil {
		return err
	}
	if err := s.pty.StartCmd(ephID, ptymgr.Spec{Command: cmd, Dir: dir}); err != nil {
		unwind()
		return fmt.Errorf("could not open the scratch shell: %w", err)
	}
	log.Info().Str("panel", ephID).Str("dir", dir).Msg("scratch panel opened")
	send(cc, proto.ServerMsg{Type: "scratch", ID: ephID})
	return nil
}

// agentTargetSpec resolves a panel.git / diff target to its spawn spec, enforcing
// the authoritative agent-only gate in one place — the client gates too for UX, but
// the server is the source of truth, so every target-taking op (the ephemeral ops,
// worktree add and remove) routes through here. label names the action in the gate
// error ("diff"/"git"). Returns the panel's immutable spec, or an error for an
// unknown id or a non-agent target.
func (s *Server) agentTargetSpec(targetID, label string) (ptymgr.Spec, error) {
	s.mu.Lock()
	idx := s.indexLocked(targetID)
	if idx < 0 {
		s.mu.Unlock()
		return ptymgr.Spec{}, fmt.Errorf("no panel with id %q", targetID)
	}
	kind := s.panels[idx].Kind
	spec := s.specs[targetID]
	s.mu.Unlock()

	if kind != panel.Agent {
		return ptymgr.Spec{}, fmt.Errorf("%s is available on agent panels", label)
	}
	return spec, nil
}

// gitWorktreeAdd creates a worktree on a new branch off the target agent's repo and
// spawns an agent panel rooted in it, grouped under the branch name — the isolation
// bridge. It reuses the source agent's command and args, so the new tree gets the
// same kind of agent. A real fleet change, so the caller broadcasts.
func (s *Server) gitWorktreeAdd(targetID, branch string) error {
	spec, err := s.agentTargetSpec(targetID, "git")
	if err != nil {
		return err
	}
	s.mu.Lock()
	base := s.worktreeDir
	s.mu.Unlock()

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
	id, err := s.createPanel(proto.KindAgent, spec.Command, spec.Args, path, false)
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
	spec, err := s.agentTargetSpec(targetID, "git")
	if err != nil {
		return err
	}
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
	var workspaces []string // ephemeral conductor workspaces to remove once purged
	for _, p := range s.panels {
		if p.State == panel.Exited {
			gone = append(gone, p.ID)
			if p.Conductor {
				if ws := s.specs[p.ID].Dir; ws != "" {
					workspaces = append(workspaces, ws)
				}
			}
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
	for _, ws := range workspaces {
		_ = os.RemoveAll(ws)
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
	// The whole group is gone; drop its view settings so the maps stay tidy.
	s.mu.Lock()
	delete(s.groupShown, name)
	delete(s.groupLayout, name)
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
	// Carry the view settings to the new name, keyed by name like the group itself.
	if shown, ok := s.groupShown[old]; ok {
		s.groupShown[name] = shown
		delete(s.groupShown, old)
	}
	if layout, ok := s.groupLayout[old]; ok {
		s.groupLayout[name] = layout
		delete(s.groupLayout, old)
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

// setGroupLayout records a group's split arrangement — the named layout (a preset
// or a custom TUI.yaml layout) the group opens with. The name is stored verbatim;
// the client resolves an unknown name to the default, so a layout that only exists
// in one frontend's config never wedges another. An empty group name is rejected;
// an empty layout clears the override back to the default. Like setGroupShown the
// group need not currently exist, and lifecycle cleanup keeps the map tidy.
func (s *Server) setGroupLayout(group, layout string) error {
	group = strings.TrimSpace(group)
	if group == "" {
		return fmt.Errorf("group.layout needs a group")
	}
	layout = strings.TrimSpace(layout)

	s.mu.Lock()
	defer s.mu.Unlock()
	if layout == "" {
		delete(s.groupLayout, group)
	} else {
		s.groupLayout[group] = layout
	}
	s.emit("group.change", map[string]any{"group": group, "layout": layout})
	log.Info().Str("group", group).Str("layout", layout).Msg("group layout set")
	return nil
}

// panelsMsg builds the full "panels" snapshot broadcast to clients: every panel
// in wire form plus each group's non-default view settings, sorted by name for a
// deterministic frame.
func (s *Server) panelsMsg() proto.ServerMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]proto.Panel, len(s.panels))
	for i, p := range s.panels {
		out[i] = p.ToProto()
	}
	// Per-group view settings ride the snapshot, sorted by name for determinism.
	// A group appears when it carries a non-default visible count, a non-default
	// layout, or both, so the two settings travel on one row per group.
	views := make(map[string]*proto.GroupView)
	view := func(g string) *proto.GroupView {
		v, ok := views[g]
		if !ok {
			v = &proto.GroupView{Group: g}
			views[g] = v
		}
		return v
	}
	for g, shown := range s.groupShown {
		if shown != 0 {
			view(g).Shown = shown
		}
	}
	for g, layout := range s.groupLayout {
		if layout != "" {
			view(g).Layout = layout
		}
	}
	groups := make([]proto.GroupView, 0, len(views))
	for _, v := range views {
		groups = append(groups, *v)
	}
	slices.SortFunc(groups, func(a, b proto.GroupView) int { return strings.Compare(a.Group, b.Group) })
	return proto.ServerMsg{Type: "panels", Panels: out, Groups: groups}
}

// addClient registers an attached client connection so it receives broadcasts.
func (s *Server) addClient(cc *clientConn) {
	s.mu.Lock()
	s.clients[cc] = struct{}{}
	n := len(s.clients)
	s.mu.Unlock()
	log.Info().Int("clients", n).Msg("client attached")
}

// removeClient detaches a client and closes its outbound queue. It is idempotent:
// a connection already gone is a no-op, so a double detach cannot double-close.
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
