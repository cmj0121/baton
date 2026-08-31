// Package server is the headless baton core: the connection layer plus the
// single source of truth for panel state. Clients attach over the socket, send
// commands, and receive event broadcasts.
package server

import (
	"context"
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

	"github.com/cmj0121/baton/internal/attn"
	"github.com/cmj0121/baton/internal/cgroup"
	"github.com/cmj0121/baton/internal/cwd"
	"github.com/cmj0121/baton/internal/gitdiff"
	"github.com/cmj0121/baton/internal/gitops"
	"github.com/cmj0121/baton/internal/isolate"
	"github.com/cmj0121/baton/internal/limits"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/panellog"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/queue"
	"github.com/cmj0121/baton/internal/remote"
	"github.com/cmj0121/baton/internal/restart"
	"github.com/cmj0121/baton/internal/score"
	"github.com/cmj0121/baton/internal/signals"
	"github.com/cmj0121/baton/internal/state"
	"github.com/cmj0121/baton/internal/task"
	"github.com/cmj0121/baton/internal/usage"
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

	// id, source and since are this connection's row in the remote overlay: a
	// stable id remote.kick names it by, the label it called itself on hello
	// ("" for a local cockpit), and when it attached. id and since are set once
	// in handle(); source is set once in the hello handler, on the same
	// command-loop goroutine that reads them, so they need no lock beyond the
	// s.mu already held while the client set is walked.
	id     string
	source string
	since  time.Time

	// greeted is set once this connection's hello has been handled. Until then it
	// has no row worth listing — its role and source are still unknown — so the
	// remote overlay leaves it out rather than showing a nameless placeholder.
	// Written and read under Server.mu, unlike role/self, which the command loop
	// also reads on its own goroutine.
	greeted bool
}

// spawnSpec is what a panel was launched from: the exact process spec ptymgr
// ran, plus the agent profile it came from. The profile is kept as a name rather
// than as the resolved limits so a reload re-reads the policy for panels that are
// already running, instead of freezing it at spawn time.
type spawnSpec struct {
	ptymgr.Spec
	Profile string // agent profile name; empty for a shell or a profile-less spawn
}

// Settings are the server knobs a running daemon can adopt without a restart —
// what Reload swaps in, and what the construction options seed. They travel as
// one value so the reload path does not grow another positional parameter every
// time a knob lands.
type Settings struct {
	// Remote is `settings.remote`: whether the fleet accepts a cockpit attached
	// over the ssh bridge. Off by default. It is acted on as a TRANSITION rather
	// than as a value (see applyRemoteSetting), so a reload never undoes a `C-t @`
	// taken since the file was last read.
	Remote bool

	AllowNameConflict bool   // when false, panel titles and group names stay unique
	DefaultDir        string // workdir for a panel that asks for none; empty → the user's home
	ReplayBytes       int    // per-panel replay buffer; 0 keeps the ptymgr default
	DiffCommand       string // explicit diff command for the agent diff pop-up
	Editor            string // commit editor for the git menu (GIT_EDITOR)
	WorktreeDir       string // base dir for new git-menu worktrees

	// QueueMax is the most queued (unassigned) tasks the backlog holds and
	// QueueConcurrency the most tasks one work item runs at once; 0 means
	// unlimited for either, and a negative QueueMax restores the built-in default.
	// Both are read afresh on every enqueue and every scheduler pass, so they swap
	// under a running backlog like the rest of this struct: lowering the cap below
	// what is already queued refuses the next enqueue rather than dropping a task.
	QueueMax         int
	QueueConcurrency int

	// Limits is the fleet-wide resource cap and AgentLimits the per-profile caps
	// layered over it, keyed by profile name. Only the caps are passed, never the
	// profiles' commands: the server resolves policy, the client resolves what to run.
	Limits      limits.Limits
	AgentLimits map[string]limits.Limits

	// Restart is the fleet-wide policy for bringing a dead panel back and
	// AgentRestart the per-profile policies layered over it, keyed by profile
	// name — the same shape as the resource caps above, and resolved the same way.
	Restart      restart.Policy
	AgentRestart map[string]restart.Policy

	// Attention is the fleet-wide quiet ladder — how long silence means a turn is
	// over, and how much longer it means something is wrong — and AgentAttention
	// the per-profile ladders layered over it. Same shape and same resolution as
	// the caps and the restart policy, which is what makes all three hot-reload on
	// SIGHUP without touching a single live panel: a panel records its PROFILE
	// name, and every tick resolves the policy afresh from it.
	Attention      attn.Policy
	AgentAttention map[string]attn.Policy

	// AgentIsolate holds the per-profile container isolation, keyed by profile
	// name. There is no fleet-wide counterpart, unlike every other policy above:
	// an isolated panel needs an image with the right toolchain, and no single
	// image is right for a whole fleet. A profile absent from the table runs on
	// the host exactly as it does today.
	AgentIsolate map[string]isolate.Policy

	// TrackCwd is how a panel's live working directory is learned and RestoreCwd
	// which panels a re-run puts back in it.
	TrackCwd   cwd.Track
	RestoreCwd cwd.Restore

	// Output logging. LogDir is where a panel's transcript lands and AgentLogDir
	// the per-profile destinations layered over it — the same shape as the caps and
	// the restart policy, resolved the same way, so all three hot-reload together.
	// An empty LogDir with no override disables logging for that panel entirely.
	// AgentLog lists the profiles that log from the moment they spawn, and
	// LogMaxBytes is the size a log rolls at (0 = the built-in default).
	LogDir      string
	AgentLogDir map[string]string
	AgentLog    map[string]bool
	LogMaxBytes int64
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

	// Resource limits. limits is the fleet-wide cap every panel runs under and
	// agentLimits holds the per-profile caps layered over it (see Settings). The
	// server keeps the policy rather than the client, so a connection can never
	// spawn itself a wider one; it is resolved on demand from the profile name
	// recorded with each panel, so a reload takes hold without touching the fleet.
	limits      limits.Limits
	agentLimits map[string]limits.Limits

	// cg is the enforcement backend the resolved limits are handed to: it makes
	// each panel's process tree actually run under its caps, or reports that this
	// host cannot and leaves every panel uncapped. Built once at construction and
	// thereafter read without the lock; it guards its own state.
	cg *cgroup.Manager

	// agentIsolate is the per-profile container isolation, resolved at spawn from
	// the profile name the same way the caps are. containers maps a live panel to
	// the container name it was launched under, which is what teardown needs back:
	// with a TTY attached the runtime does not proxy signals, so killing the client
	// leaves the container running and only the recorded name can find it again.
	agentIsolate map[string]isolate.Policy
	containers   map[string]string

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
	// delivered: it may rewrite the prompt or the score, or veto the task. It blocks
	// on the plugin worker, so — unlike eventSink — it must be called WITHOUT s.mu
	// held. A nil filter (no plugin) passes every brief through unchanged.
	onFilterTask func(b TaskBrief) (TaskBrief, bool)
	clientConfig json.RawMessage
	pluginCmds   []proto.PluginCommand
	footerText   string // a plugin-set persistent footer segment (baton.footer); carried on config + pushed live

	// agents is the set of agent backends this host can actually run, detected by
	// the daemon at boot and on every reload. It lives on the server rather than
	// being resolved by each frontend because the PATH that matters is the one the
	// PANELS are spawned from: a cockpit attached over --remote would otherwise
	// offer a list of the binaries on its own machine, which is worse than no list.
	agents []proto.AgentBackend

	// Account usage footer. usageProvider polls the current account's token/cost
	// usage over the billing window (internal/usage); a nil provider disables the
	// segment. usageInterval is the poll cadence; usageText and usageInfo are the
	// last rendered segment and its structured form, held so a freshly attaching
	// client is seeded on hello. usageWarn/usageAlarm/usageFormat are the
	// presentation settings the frontends need to render the countdown on their
	// own clock. Guarded by mu.
	usageProvider usage.Provider
	usageInterval time.Duration
	usageText     string
	usageInfo     *proto.UsageInfo
	usageWarn     float64
	usageAlarm    float64

	// The account's rate-limit standing (internal/usage). limitsProvider is the
	// source and limitsInfo the last reading it gave, held so a reading survives a
	// poll that had nothing new — the statusline source is a push, and an idle
	// fleet stops reporting without the last quota becoming untrue. limitsSelf is
	// the path to baton's own binary, which every Claude Code panel is launched
	// pointing its status line at; empty disables the injection. Guarded by mu,
	// except limitsSelf and limitsProvider, which are set once before the loops
	// start.
	limitsProvider usage.LimitsProvider
	limitsInfo     *proto.LimitsInfo
	limitsSelf     string

	// Remote access (remote.go). remoteOn and remoteKey are the live switch and
	// the in-memory 8-character passkey — never persisted, so a restart always
	// means a new one. remoteCfg is the last value the CONFIG asked for, kept so
	// a reload acts on a change to the file rather than undoing a `C-t @` that
	// was made since. remoteLim rate-limits failed attaches. connSeq mints the
	// per-connection ids the overlay lists and remote.kick names. Guarded by mu,
	// bar remoteLim, which locks internally.
	remoteOn  bool
	remoteKey string
	remoteCfg bool
	remoteLim *remote.Limiter
	connSeq   int

	mu      sync.Mutex
	seq     int
	panels  []panel.Panel
	clients map[*clientConn]struct{}
	mon     *monitor             // lifecycle + telemetry bookkeeping, guarded by mu
	specs   map[string]spawnSpec // immutable spawn spec per panel id, for persistence + respawn (guarded by mu)

	// sessions maps a panel id to the Claude Code session ids it has run under,
	// oldest first — a list rather than one id because re-using an id is a hard
	// error, so every spawn and respawn mints a fresh one and the panel's spend is
	// the sum over all of them. Guarded by mu.
	//
	// It is deliberately not persisted: a restored panel comes back as an exited
	// slot with no live process, so carrying its old ids across a daemon restart
	// would attribute a window's spend to something that is not running.
	sessions map[string][]string

	// Restart supervision. restart is the fleet-wide policy and agentRestart the
	// per-profile ones layered over it (see Settings); restarts holds the live
	// bookkeeping per panel — failure count, run clock, armed timer. shuttingDown
	// is set before the daemon kills the fleet, so the kills it does on purpose
	// are not mistaken for crashes worth undoing. Guarded by mu.
	restart      restart.Policy
	agentRestart map[string]restart.Policy
	restarts     map[string]*restartState
	shuttingDown bool

	// Output logging. logDir/agentLogDir resolve where a panel's transcript lands,
	// agentLog names the profiles that log from spawn, and logMaxBytes is the roll
	// size (see Settings). logs holds the open sink per panel id — a panel that
	// exits keeps its sink so a re-run appends rather than truncating. All guarded
	// by mu.
	//
	// logMu is a SECOND lock, and only for the enable/disable pair: opening a log
	// snapshots the replay ring, creates a file and flushes a buffer into it, which
	// is disk work that must not be done under the lock the output path contends.
	// It is never held while taking mu for anything but a short read or write.
	logDir      string
	agentLogDir map[string]string
	agentLog    map[string]bool
	logMaxBytes int64
	logs        map[string]*panellog.Sink
	logMu       sync.Mutex

	// The quiet ladder. attention is the fleet-wide policy and agentAttention the
	// per-profile ones layered over it (see Settings), resolved per tick from the
	// profile each panel records rather than frozen at spawn — which is what lets
	// a SIGHUP change the thresholds under a running fleet. Guarded by mu.
	attention      attn.Policy
	agentAttention map[string]attn.Policy

	// declared is the top of the detection precedence: what each agent has said
	// about ITSELF, keyed by panel id, populated lazily and dropped when the panel
	// exits, closes, or is pruned.
	//
	// taskSettled is the other half — the set of panels whose in-flight task went
	// terminal-done since the last tick. It is an EDGE rather than a state: only
	// the tick that saw it may promote the panel to done, and a byte of output
	// clears it, or a panel that woke back to running would be dragged straight
	// back by work that finished minutes ago. Both guarded by mu.
	declared    map[string]*declaration
	taskSettled map[string]bool

	// acked records that a human has dealt with a panel from the inbox:
	// dismissed it, snoozed it, or replied to it. It is fleet state rather than
	// per-cockpit state because the queue's promise is "the fleet needs a human",
	// and a second cockpit re-offering work the first just cleared is exactly the
	// untrustworthy queue this feature exists to fix. A zero value means "until
	// the panel speaks again"; a set one is a snooze's expiry, evaluated where it
	// is read (ackedLocked) rather than by a sweeper. Guarded by mu; see ack.go.
	acked map[string]time.Time

	// exitedAt is when each dead panel's process ended. It exists because the
	// Monitor forgets a panel the moment it exits, which takes its state clock
	// with it — and an exited panel still needs one, since a queue that lists
	// failures has to sort them oldest-first like everything else. Written once on
	// exit, read by wirePanel as the fallback, dropped with every other per-panel
	// map when the panel is closed, pruned, or re-run. Guarded by mu.
	exitedAt map[string]time.Time

	// Working-directory tracking. trackCwd is how a panel's live directory is
	// learned and restoreCwd which panels are re-run in it; osc7Tail carries the
	// last few bytes of each panel's output so a report split across two reads is
	// still read (see cwd.go). Guarded by mu.
	trackCwd    cwd.Track
	restoreCwd  cwd.Restore
	osc7Tail    map[string][]byte
	reportedCwd map[string]bool

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

	// spawning marks the ids of spawn-on-demand tasks with a panel currently being
	// provisioned for them, so the scheduler asks for one panel per such task
	// rather than a fresh one every tick until it settles. Guarded by mu.
	spawning map[string]bool

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

	// pidOf resolves a panel's process-group leader pid. It is the PTY manager's
	// own map in production; a test swaps it to stand in for a live process. Set
	// once in New, then read without a lock — like writeInput above.
	pidOf func(id string) int

	// scoreState is the fleet memory (internal/score): the store whose entries are
	// rendered into directly dispatched briefs and served over the score.* verbs,
	// what the config asked for, why no store is running when none is, and whether
	// score.md has become unreadable. Held as one value because those are four
	// readings of one subsystem — see scoreState.
	scoreState scoreState

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

	// groupFavourite is the set of groups marked a dashboard favourite — their
	// cards sort to the front of the dashboard. Keyed by group name; an absent
	// entry means "not a favourite". Entirely separate from groupShown/groupLayout
	// and from a panel's Pinned flag. Guarded by mu.
	groupFavourite map[string]bool

	// conductorPending reserves the conductor singleton across the unlocked spawn
	// in createPanel, so two near-simultaneous conductor.create calls cannot both
	// pass the "no conductor exists yet" check. Guarded by mu.
	conductorPending bool

	// globalShellPending reserves the global-shell singleton across the unlocked
	// spawn in createPanel, the same way conductorPending guards the conductor, so
	// two near-simultaneous global-shell creates cannot both pass the check.
	// Guarded by mu.
	globalShellPending bool

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

// WithLimits seeds the resource-limit policy: the fleet-wide caps and the
// per-agent-profile caps layered over them. Reload swaps both together, so this
// only sets the policy the daemon boots with.
func WithLimits(limits limits.Limits, agents map[string]limits.Limits) Option {
	return func(s *Server) { s.limits, s.agentLimits = limits, agents }
}

// WithLogging configures panel output logging: the fleet-wide destination, the
// per-profile destinations layered over it, the profiles that log from spawn, and
// the size a log rolls at. An empty dir with no per-profile override leaves
// logging off, which is the default.
func WithLogging(dir string, agentDirs map[string]string, agentLog map[string]bool, maxBytes int64) Option {
	return func(s *Server) {
		s.logDir, s.agentLogDir, s.agentLog, s.logMaxBytes = dir, agentDirs, agentLog, maxBytes
	}
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

// ScoreState is everything the server is TOLD about the fleet memory: the store
// itself, what the config asked for, and why no store is running when none is.
//
// The three travel together in one value on purpose. They were briefly two
// options, and every caller then had to remember to pass both — a server given
// a live store and no state answered score.status with `enabled:false,
// available:true`, which is not a state that exists. One option makes the
// contradiction unrepresentable rather than merely discouraged, which matters
// because #41-#46 all wire this.
type ScoreState struct {
	Store   *score.Store // nil when no store is running
	Enabled bool         // the score.enabled config knob
	Reason  string       // why Store is nil; empty when it opened normally
}

// scoreState is what the server HOLDS: the option's value, kept whole rather
// than dissolved into loose fields two readers re-correlate by hand, plus the
// fourth reading of the same subsystem — whether score.md has stopped being
// readable. That one lives here because it belongs to the same answer: a store
// that opened fine and whose file has since become a directory is `available`
// with no reason to report, while the operator's edits sit inert. Invariant I8
// is precisely that they must not have to read the daemon log to find out.
//
// It is a distinct type from ScoreState because the latch is an atomic — every
// connection's command loop reads it — and an atomic cannot ride a value
// callers construct and copy.
type scoreState struct {
	ScoreState
	failing atomic.Bool // score.md could not be read on the last attempt
}

// available reports that a store is running. It is not the same question as
// "is the fleet memory healthy" — see reason.
func (st *scoreState) available() bool { return st.Store != nil }

// reason is why the fleet memory is not doing its job, phrased for whoever
// asked, and empty when it is. It answers for both the store that never opened
// and the score.md that has stopped being readable, because to the operator
// those are one question.
func (st *scoreState) reason() string {
	switch {
	case st.Store == nil && st.Reason != "":
		return st.Reason
	case st.Store == nil:
		return "score is disabled"
	case st.failing.Load():
		return "score.md cannot be read; serving the last view the store did read"
	default:
		return ""
	}
}

// WithScore hands the server the fleet-memory store whose entries are injected
// into directly dispatched briefs and served over the score.* verbs, together
// with why it is absent when it is. A nil Store is the subsystem not running;
// Enabled and Reason are what let score.status and score.submit tell "switched
// off" apart from "unavailable" instead of answering both with a bare
// "disabled". The server never re-asks the config.
func WithScore(st ScoreState) Option {
	return func(s *Server) {
		s.scoreState.ScoreState = st
	}
}

// UsageDisplay is how the frontends should present the window: the fractions of
// it spent at which the footer segment turns amber and then red. These live on
// the daemon because that is where the usage.* config is read, and they ride the
// usage message because the countdown ticks on the frontend's clock, not the poll
// interval.
type UsageDisplay struct {
	WarnAt  float64
	AlarmAt float64
}

// WithUsage wires the account usage/cost footer: p polls the current usage,
// interval is the poll cadence, and display carries the presentation settings on
// to the frontends. A nil provider (or non-positive interval) leaves the segment
// off, so usage is opt-in and costs nothing when unconfigured.
func WithUsage(p usage.Provider, interval time.Duration, display UsageDisplay) Option {
	return func(s *Server) {
		s.usageProvider = p
		s.usageInterval = interval
		s.usageWarn = display.WarnAt
		s.usageAlarm = display.AlarmAt
	}
}

// WithUsageLimits wires the account's rate-limit bars: p reads the current standing,
// and self is the path to baton's own binary, which the Claude Code panels the
// daemon launches are pointed at as their status line (see withStatusLine).
//
// The two arguments are separate because the reading and the harvesting are
// separate concerns. The oauth source fetches on its own and needs no injection,
// while the statusline source needs nothing but the injection — it only reads a
// file the panels fill in. Passing an empty self is how a source that harvests
// for itself says so.
func WithUsageLimits(p usage.LimitsProvider, self string) Option {
	return func(s *Server) {
		s.limitsProvider = p
		s.limitsSelf = self
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
		specs:           make(map[string]spawnSpec),
		sessions:        make(map[string][]string),
		restarts:        make(map[string]*restartState),
		osc7Tail:        make(map[string][]byte),
		reportedCwd:     make(map[string]bool),
		trackCwd:        cwd.Auto,
		restoreCwd:      cwd.Shells,
		ephemeral:       make(map[string]struct{}),
		logs:            make(map[string]*panellog.Sink),
		groupShown:      make(map[string]int),
		groupLayout:     make(map[string]string),
		groupFavourite:  make(map[string]bool),
		pendingDispatch: make(map[string][]byte),
		declared:        make(map[string]*declaration),
		taskSettled:     make(map[string]bool),
		acked:           make(map[string]time.Time),
		exitedAt:        make(map[string]time.Time),
		tasks:           make(map[string]*task.Task),
		panelTask:       make(map[string]string),
		spawning:        make(map[string]bool),
		taskDirty:       make(chan string, 256),
		queueMax:        defaultQueueMax,
		dirty:           make(chan struct{}, 1),
		heartbeat:       proto.HeartbeatInterval,
		cg:              cgroup.New(),
		containers:      make(map[string]string),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.probeEnforcement()

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
	if s.pidOf == nil {
		s.pidOf = func(id string) int { return s.pty.Pids()[id] }
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
// name-conflict policy, the default workdir, the per-panel replay buffer, and the
// resource limits can all change under a running fleet; settings fixed at
// construction (the listener, the build version) are left alone. A ReplayBytes of
// zero resets the buffer to its built-in default.
//
// The limits swap is the whole policy at once: because a panel records the agent
// profile it came from rather than the caps that profile resolved to, every live
// panel re-reads the new policy from here on with nothing to migrate.
func (s *Server) Reload(set Settings) {
	s.mu.Lock()
	s.allowNameConflict = set.AllowNameConflict
	s.defaultDir = set.DefaultDir
	s.diffCommand = set.DiffCommand
	s.editor = set.Editor
	s.worktreeDir = set.WorktreeDir
	s.limits, s.agentLimits = set.Limits, set.AgentLimits
	s.restart, s.agentRestart = set.Restart, set.AgentRestart
	s.attention, s.agentAttention = set.Attention, set.AgentAttention
	s.agentIsolate = set.AgentIsolate // resolved per spawn, so live panels keep the runtime they started under
	s.trackCwd, s.restoreCwd = set.TrackCwd, set.RestoreCwd
	// The logging policy swaps whole, like the caps: a panel records its PROFILE,
	// so every resolution from here on reads the new destination. Files ALREADY
	// open keep the path they were opened with — a log that moved mid-run would
	// leave half a transcript in each of two places.
	s.logDir, s.agentLogDir, s.agentLog, s.logMaxBytes = set.LogDir, set.AgentLogDir, set.AgentLog, set.LogMaxBytes
	// The backlog caps, on the same terms WithQueue sets them at construction —
	// except that a config which no longer names queue.max restores the built-in
	// default rather than keeping the old number, so removing the key from the file
	// is as much an edit as changing it.
	if set.QueueMax >= 0 {
		s.queueMax = set.QueueMax
	} else {
		s.queueMax = defaultQueueMax
	}
	if set.QueueConcurrency >= 0 {
		s.queueConcurrency = set.QueueConcurrency
	}
	s.mu.Unlock()
	s.applyRemoteSetting(set.Remote) // acts only on a change to `settings.remote` in the file
	s.refreshConductorWiring()       // an edited operator brief lands without reopening the conductor
	s.pty.SetRingCap(set.ReplayBytes)
	s.probeEnforcement() // a reload may be the first thing to configure a cap

	// Push the new caps onto the live cgroups. This is the half a reload cannot do
	// by re-resolving alone: the kernel holds the old numbers until they are
	// rewritten. Anything that cannot take hold under a running process comes back
	// as needing a respawn, so it can be reported rather than quietly missed.
	s.mu.Lock()
	resolved := make(map[string]limits.Limits, len(s.specs))
	for id, spec := range s.specs {
		resolved[id] = s.effectiveLimitsLocked(spec.Profile)
	}
	fleetWide := s.effectiveLimitsLocked("")
	s.mu.Unlock()
	updated, deferred := s.cg.Update(func(id string) limits.Limits {
		if caps, ok := resolved[id]; ok {
			return caps
		}
		return fleetWide // an ephemeral: it holds no spec, so it takes the fleet's
	})
	log.Info().Bool("allow_name_conflict", set.AllowNameConflict).Str("default_dir", set.DefaultDir).
		Int("replay_bytes", set.ReplayBytes).Str("diff_command", set.DiffCommand).Str("editor", set.Editor).
		Str("worktree_dir", set.WorktreeDir).Interface("limits", set.Limits.Fields()).Int("agent_limits", len(set.AgentLimits)).
		Dur("done_after", set.Attention.Done()).Dur("stuck_after", set.Attention.Stuck()).Int("agent_attention", len(set.AgentAttention)).
		Int("panels_recapped", updated).Strs("respawn_to_apply", deferred).
		Msg("settings reloaded")
}

// startPanel is the daemon's one fork point: it resolves the caps the panel is
// to run under, places it inside them, and starts it. Every spawn — a fleet
// panel, a re-run, a diff pop-up, the scratch shell — goes through here, so a
// panel cannot be added later that quietly escapes the policy.
//
// profile names the agent profile the caps resolve through; empty resolves to
// the fleet-wide caps alone, which is what a shell or a profile-less spawn gets.
//
// A policy the backend cannot express fails the START. A panel that was asked to
// be capped and silently is not is the one outcome worse than not starting it,
// because it reads as protection that is not there. A host with no backend at all
// is the exception: that degradation is reported once at startup, and failing
// every spawn on a machine that simply cannot enforce would make the setting
// unusable rather than safe.
func (s *Server) startPanel(id, profile string, spec ptymgr.Spec) error {
	s.mu.Lock()
	caps := s.effectiveLimitsLocked(profile)
	iso := s.agentIsolate[profile]
	s.mu.Unlock()

	// Hand an agent panel a session of its own, on the launched copy only — the
	// spec the caller retains for respawn must stay free of the id, because
	// re-using one fails the next launch outright (see session.go).
	spec, session := withSessionID(spec)
	if session != "" {
		s.mu.Lock()
		s.sessions[id] = append(s.sessions[id], session)
		s.mu.Unlock()
	}

	// …and a status line that harvests the account's rate limits on the way past.
	// Same rule as the session id: the launched copy only, never the spec kept for
	// respawn, so a re-run re-resolves whatever status line the user has by then.
	spec, _ = withStatusLine(spec, s.limitsSelf)

	if iso.Enabled() {
		// No cgroup here, and that is deliberate: it would confine the runtime
		// CLIENT, leaving the container — the daemon's child, not ours — entirely
		// uncapped. The caps ride the runtime's own flags instead, so an isolated
		// panel is held to the same policy by a different enforcer.
		if err := s.isolateSpec(id, iso, &spec, caps); err != nil {
			return err
		}
	} else {
		h, err := s.cg.Prepare(id, caps)
		if err != nil {
			return fmt.Errorf("resource limits for panel %s: %w", id, err)
		}
		if h != nil {
			if skipped := append(s.cg.Unenforced(caps), h.Skipped()...); len(skipped) > 0 {
				log.Warn().Str("panel", id).Strs("limits", skipped).Msg("limits this backend cannot enforce")
			}
			// Hooked onto the copy that is launched, never onto the spec the server
			// retains: a stored hook would pin the handle long after Release drops it.
			spec.Confine = h.Confine
		}
	}
	if err := s.pty.StartCmd(id, spec); err != nil {
		s.cg.Release(id)       // the cgroup outlived the process it was made for
		s.releaseContainer(id) // and so did the container name, if this was an isolated spawn
		return err
	}
	// The run's clock starts here, at the one fork point, so every way a panel can
	// come up — a fresh spawn, a manual re-run, a supervised restart — feeds the
	// same "was this run healthy" question.
	s.mu.Lock()
	s.noteSpawnLocked(id, time.Now())
	s.mu.Unlock()
	return nil
}

// probeEnforcement commits to a backend once there is a policy to enforce, and
// reports what the host can actually hold a panel to. Probing creates cgroups and
// moves the daemon into one, so a fleet with no caps configured never pays for it
// — and a reload that introduces the first cap calls this, not just startup.
func (s *Server) probeEnforcement() {
	s.mu.Lock()
	configured := !s.limits.IsZero() || len(s.agentLimits) > 0
	s.mu.Unlock()
	if configured {
		s.cg.Probe()
	}
	log.Info().Str("enforcement", s.cg.Describe()).Msg("resource limits")
}

// EffectiveLimits resolves the resource caps a live panel runs under: the
// fleet-wide limits with its agent profile's own layered over them. It reads the
// policy as it stands now, not as it stood when the panel spawned, so a reload is
// visible here immediately. An unknown panel resolves to the fleet-wide limits.
func (s *Server) EffectiveLimits(id string) limits.Limits {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effectiveLimitsLocked(s.specs[id].Profile)
}

// effectiveLimitsLocked layers the named agent profile's caps over the fleet-wide
// ones. An empty or unknown profile — a shell panel, or an agent spawned without
// one — resolves to the fleet-wide limits alone. Caller holds s.mu.
func (s *Server) effectiveLimitsLocked(profile string) limits.Limits {
	return s.limits.Merge(s.agentLimits[profile])
}

// effectiveAttentionLocked layers the named agent profile's quiet ladder over
// the fleet-wide one, on the same terms as the caps above. It is resolved on
// every tick rather than cached because that is what makes the thresholds
// hot-reloadable: a SIGHUP swaps the policy and the very next tick reads it.
// Caller holds s.mu.
func (s *Server) effectiveAttentionLocked(profile string) attn.Policy {
	return s.attention.Merge(s.agentAttention[profile])
}

// onPanelExit marks a panel exited when its process ends on its own, notifies
// and detaches any client zoomed into it, and broadcasts the change. It is a
// no-op for a panel already gone (e.g. an explicit panel.close).
func (s *Server) onPanelExit(id string, exitCode int) {
	// The runtime client has exited, so an isolated panel's container is either
	// already gone (it exited on its own, and --rm took it) or orphaned (baton
	// killed the client and the container never heard). Removing by name settles
	// both; a respawn mints a fresh name, so nothing here is reused.
	s.releaseContainer(id)

	s.mu.Lock()
	found := false
	var fields map[string]any
	var notice string
	for i := range s.panels {
		if s.panels[i].ID == id {
			s.panels[i].State = panel.Exited
			s.panels[i].ExitCode = exitCode // the daemon reports it; the cockpit renders a non-zero one as failed
			s.panels[i].Reason = ""         // a dead process is not asking for anything
			s.panels[i].Activity = "exited"
			s.exitedAt[id] = time.Now()   // the Monitor is about to forget it; keep the instant the queue sorts on
			s.mon.forget(id)              // a dead panel no longer ticks
			delete(s.declared, id)        // …and its raised hand goes with it
			delete(s.acked, id)           // …and any acknowledgement of it
			delete(s.taskSettled, id)     // …as does any pending done edge
			delete(s.pendingDispatch, id) // a held dispatch dies with the process
			// A task in flight died with its panel — fail it with the exit code.
			s.advanceTaskLocked(id, task.Failed, fmt.Sprintf("panel exited (code %d)", exitCode))
			// The restart policy decides whether this is the end of the panel or a
			// pause in it, and says which on the card.
			if notice = s.superviseExitLocked(id, exitCode, time.Now()); notice != "" {
				s.panels[i].Activity = notice
			}
			fields = panelFields(s.panels[i])
			fields["exit_code"] = exitCode
			found = true
			break
		}
	}
	line := "\r\n[process exited]\r\n"
	if notice != "" {
		line = "\r\n[" + notice + "]\r\n"
	}
	for cc := range s.clients {
		if cc.attached[id] {
			send(cc, protoOutput(id, line))
			delete(cc.attached, id)
		}
	}

	var stop []string // PTYs to reap after the lock: pruned dead slots + a self-exited ephemeral
	if found {
		s.emit("panel.exit", fields)
		stop = s.pruneExitedLocked()
	} else if _, ok := s.ephemeral[id]; ok {
		// A diff/scratch ephemeral panel exited on its own (it never lives in
		// s.panels). Drop it from the ephemeral set and every conn's, so it stops
		// counting against maxEphemeralPerConn and its dead pane is freed, rather
		// than lingering until the client explicitly closes it or disconnects.
		delete(s.ephemeral, id)
		for cc := range s.clients {
			delete(cc.ephemeral, id)
		}
		stop = append(stop, id)
	}
	s.mu.Unlock()

	for _, sid := range stop {
		s.pty.Stop(sid)
		s.cg.Release(sid)                 // the panel is gone for good; drop its cgroup with it
		s.releaseContainer(sid)           // and the container it was launched in, if it had one
		s.stopLogging(sid, logMarkClosed) // …and its transcript is finished rather than abandoned
	}

	if found {
		// Flush and close the file the process was writing to, keeping the sink so a
		// re-run appends under a new session marker instead of truncating it.
		s.suspendLog(id, exitCode)
		log.Info().Str("panel", id).Int("exit_code", exitCode).Msg("panel process exited")
		s.broadcast(s.panelsMsg())
	}
}

// protoOutput is a server-authored line addressed to a panel's viewers — the
// "[process exited]" notice and its restart siblings. It is not the panel's own
// output and never reaches the replay buffer: it tells whoever is watching what
// just happened to the process behind the screen they are looking at.
func protoOutput(id, text string) proto.ServerMsg {
	return proto.ServerMsg{Type: "output", ID: id, Data: []byte(text)}
}

// routeOutput is the daemon's whole output path: it fans a panel's bytes out to
// every client zoomed into it, feeds the Monitor, and appends them to the panel's
// log when one is open.
//
// The two halves are split because only the first needs the server lock. Waking a
// quiet panel and demuxing to clients is bookkeeping; writing the log is disk I/O,
// and the whole fleet's fan-out must never queue behind one panel's disk.
func (s *Server) routeOutput(id string, data []byte) {
	s.fanOutput(id, data)
	// The log is written with s.mu RELEASED. It is a file write on the hot output
	// path, and the whole fleet's fan-out must never queue behind one panel's disk;
	// per-panel ordering is preserved because this runs on that panel's own pump.
	s.writeLog(id, data)
}

// fanOutput is routeOutput's locked half: the Monitor bookkeeping and the fan-out
// to attached clients. Output is the signal that wakes a quiet (or just-spawned)
// panel back to running; the wake is in-memory only — the next monitor tick
// carries it to clients — so the hot output path never triggers a broadcast of
// its own.
func (s *Server) fanOutput(id string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mon.observed(id, len(data))
	// An acknowledgement stands only until the panel next SPEAKS, and this is that
	// edge — the literal one, not the quiet→noisy wake below. A panel whose own
	// declaration stands never takes the wake branch, so clearing the ack there
	// would leave a dismissed row suppressed for as long as the declaration held.
	// The length check keeps the steady output path free of a map hash: the map is
	// empty whenever nobody has triaged anything.
	if len(s.acked) > 0 {
		delete(s.acked, id)
	}
	s.noteOutputCwdLocked(id, data)
	if i := s.indexLocked(id); i >= 0 {
		// A byte of output wakes every resting state back to running — but NOT
		// while the agent's own declaration stands. An agent that prints a spinner
		// while waiting on you would otherwise lose its raised hand on the next
		// byte, which defeats the whole point of a declaration outranking the
		// timers: only panel.resolve, or the process ending, withdraws one.
		if from := s.panels[i].State; wakesOnOutput(from) && !s.declaredLocked(id) {
			s.panels[i].State = panel.Running
			s.mon.entered(id)
			// A byte of output invalidates any pending "the turn is over" event: the
			// panel is demonstrably working, so a task that finished a moment ago must
			// not drag it to done on the next tick.
			delete(s.taskSettled, id)
			f := panelFields(s.panels[i])
			f["from"], f["to"] = from.String(), panel.Running.String()
			s.emit("panel.state", f)
			s.advanceTaskLocked(id, task.Running, "") // output means the agent is working its task
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
	go s.usageLoop(stop)
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

// usageLoop polls the account's usage/cost on a fixed interval and broadcasts the
// formatted footer segment when it changes. It seeds the held value once at boot
// (so hello can serve it) and thereafter fetches only while a client is attached,
// since the footer is the only consumer. A nil provider disables it entirely.
func (s *Server) usageLoop(stop <-chan struct{}) {
	// No source at all, or a non-positive interval, disables the loop: guard both so
	// a hand-wired interval of zero cannot panic time.NewTicker and take the loop's
	// goroutine down with it (the doc promises a non-positive interval is a no-op).
	// Either source alone is enough to keep it running — the quota bars stand on
	// their own, and an account can be deep into its window with nothing in the
	// transcripts baton can see.
	if (s.usageProvider == nil && s.limitsProvider == nil) || s.usageInterval <= 0 {
		return
	}
	s.refreshUsage() // seed the held value before the first client attaches
	t := time.NewTicker(s.usageInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.mu.Lock()
			n := len(s.clients)
			s.mu.Unlock()
			if n == 0 {
				continue // nobody watching — skip the fetch until someone attaches
			}
			s.refreshUsage()
		}
	}
}

// refreshUsage fetches a fresh snapshot, formats it, and broadcasts it when the
// value moved. On a fetch error it keeps the last good value if the new one is
// empty (a transient failure should not blank the footer), but still shows a
// partial snapshot that carries data (e.g. the api source got tokens but not cost).
func (s *Server) refreshUsage() {
	ctx, cancel := context.WithTimeout(context.Background(), s.usageInterval)
	defer cancel()

	s.refreshLimits(ctx)

	// The token/cost source is optional now that the quota bars can stand alone:
	// a fleet configured for limits only still runs this loop, and there is no
	// snapshot to fetch.
	var snap usage.Snapshot
	var err error
	if s.usageProvider != nil {
		snap, err = s.usageProvider.Fetch(ctx)
	}
	text := usage.Format(snap)
	// A failed scan holds the last totals rather than blanking them. It no longer
	// returns early, though: the rate limits came from a different source that did
	// not fail, and a transcript scan going wrong is no reason to take down the one
	// reading that says whether the next turn will be refused.
	hold := err != nil && text == ""
	if err != nil {
		if hold {
			log.Warn().Err(err).Msg("usage fetch failed; keeping the last value")
		} else {
			log.Warn().Err(err).Msg("usage fetch partial; showing what returned")
		}
	}

	s.mu.Lock()
	info := s.usageInfoLocked(snap)
	if hold {
		text, info = s.usageText, attachLimits(s.usageInfo, s.limitsInfo)
	}
	changed := s.usageText != text || !sameUsageInfo(s.usageInfo, info)
	s.usageText, s.usageInfo = text, info
	s.mu.Unlock()
	if changed {
		s.broadcast(proto.ServerMsg{Type: "usage", Usage: text, UsageInfo: info})
	}
}

// refreshLimits takes a fresh rate-limit reading and holds it.
//
// A source with nothing to report leaves the previous reading in place rather
// than clearing it. That is not caching around a failure — it is what the
// statusline source is: a push, written by whichever panel last rendered. A fleet
// that has gone quiet stops writing, and a quota nobody has restated has not
// thereby become untrue. The reading carries its own age, and the cockpit decides
// when that age makes it worth marking.
func (s *Server) refreshLimits(ctx context.Context) {
	if s.limitsProvider == nil {
		return
	}
	l, ok := s.limitsProvider.Limits(ctx)
	if !ok {
		return
	}
	info := limitsInfo(l)
	s.mu.Lock()
	s.limitsInfo = info
	s.mu.Unlock()
}

// limitsInfo turns a reading into its wire form. Every window stays a pointer all
// the way through: a window the source did not report must reach the cockpit as
// absent, not as one sitting at zero.
func limitsInfo(l usage.Limits) *proto.LimitsInfo {
	if l.Empty() {
		return nil
	}
	info := &proto.LimitsInfo{
		FiveHour:       limitWindow(l.FiveHour),
		SevenDay:       limitWindow(l.SevenDay),
		SevenDayOpus:   limitWindow(l.SevenDayOpus),
		SevenDaySonnet: limitWindow(l.SevenDaySonnet),
		Source:         l.Source,
	}
	if !l.At.IsZero() {
		info.At = l.At.UTC().Format(time.RFC3339)
	}
	if l.Credit != nil && l.Credit.Enabled {
		info.Credit = &proto.LimitCredit{
			Enabled:     true,
			MonthlyUSD:  l.Credit.MonthlyUSD,
			UsedUSD:     l.Credit.UsedUSD,
			UsedPercent: l.Credit.UsedPercent,
		}
	}
	return info
}

// limitWindow converts one window, keeping an absent reset absent.
func limitWindow(w *usage.Window) *proto.LimitWindow {
	if w == nil {
		return nil
	}
	out := &proto.LimitWindow{UsedPercent: w.UsedPercent}
	if !w.ResetsAt.IsZero() {
		out.ResetsAt = w.ResetsAt.UTC().Format(time.RFC3339)
	}
	return out
}

// attachLimits returns info carrying lim, without mutating what the caller held.
// The two halves of the payload are refreshed by different sources on different
// terms, so one is routinely swapped onto the other; doing it in place would edit
// the value a client was already handed.
func attachLimits(info *proto.UsageInfo, lim *proto.LimitsInfo) *proto.UsageInfo {
	if info == nil {
		if lim == nil {
			return nil
		}
		return &proto.UsageInfo{Limits: lim}
	}
	out := *info
	out.Limits = lim
	return &out
}

// usageInfoLocked turns a snapshot into the wire form, resolving the per-session
// breakdown into a per-panel one. Frontends address panels, not Claude Code
// sessions — and the mapping is the server's to know, since it is the server that
// handed each panel its session. Callers must hold mu.
//
// A panel is listed only when at least one of its sessions was seen in the window,
// so an absent entry reads as "nothing attributed", never as a zero the user might
// take for "this panel is free".
func (s *Server) usageInfoLocked(snap usage.Snapshot) *proto.UsageInfo {
	if snap.Empty() {
		// No tokens to report is not no payload to send: the quota bars come from a
		// different source and stand on their own, and an account can be well into
		// its window with nothing in the transcripts baton can see.
		return attachLimits(nil, s.limitsInfo)
	}
	info := &proto.UsageInfo{
		Tokens:  snap.TotalTokens(),
		CostUSD: snap.CostUSD,
		Source:  snap.Source,
		Resets:  snap.Resets,
		WarnAt:  s.usageWarn,
		AlarmAt: s.usageAlarm,
	}
	if !snap.Since.IsZero() {
		info.Since = snap.Since.Format(time.RFC3339)
	}
	if !snap.Until.IsZero() {
		info.Until = snap.Until.Format(time.RFC3339)
	}
	for id, sessions := range s.sessions {
		var pu proto.PanelUsage
		var seen bool
		for _, sid := range sessions {
			su, ok := snap.Sessions[sid]
			if !ok {
				continue
			}
			pu.Tokens += su.Tokens
			pu.CostUSD += su.CostUSD
			seen = true
		}
		if !seen {
			continue
		}
		if info.Panels == nil {
			info.Panels = make(map[string]proto.PanelUsage, len(s.sessions))
		}
		info.Panels[id] = pu
	}
	return attachLimits(info, s.limitsInfo)
}

// sameUsageInfo reports whether two usage payloads carry the same numbers, so an
// unchanged poll does not wake every attached frontend. The window bounds are
// compared too: the totals can sit still across a poll while the window rolls
// forward underneath them, and the countdown has to follow that.
func sameUsageInfo(a, b *proto.UsageInfo) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	if a.Tokens != b.Tokens || a.CostUSD != b.CostUSD || a.Since != b.Since || a.Until != b.Until ||
		a.Resets != b.Resets || len(a.Panels) != len(b.Panels) {
		return false
	}
	for id, pa := range a.Panels {
		if pb, ok := b.Panels[id]; !ok || pa != pb {
			return false
		}
	}
	return sameLimits(a.Limits, b.Limits)
}

// sameLimits reports whether two rate-limit payloads say the same thing. The
// reading's own timestamp is compared along with the numbers: a restated but
// unchanged quota is still news, because it is what tells a cockpit the reading
// has not gone stale.
func sameLimits(a, b *proto.LimitsInfo) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	if a.Source != b.Source || a.At != b.At {
		return false
	}
	if !sameLimitWindow(a.FiveHour, b.FiveHour) || !sameLimitWindow(a.SevenDay, b.SevenDay) ||
		!sameLimitWindow(a.SevenDayOpus, b.SevenDayOpus) || !sameLimitWindow(a.SevenDaySonnet, b.SevenDaySonnet) {
		return false
	}
	return sameLimitCredit(a.Credit, b.Credit)
}

// sameLimitWindow compares two windows, treating absence as a value of its own —
// a window that has gone away is a change, not a match.
func sameLimitWindow(a, b *proto.LimitWindow) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// sameLimitCredit compares two credit balances through their pointers, since a
// null amount ("uncapped") and a zero one are different readings.
func sameLimitCredit(a, b *proto.LimitCredit) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Enabled == b.Enabled && sameFloatPtr(a.MonthlyUSD, b.MonthlyUSD) &&
		sameFloatPtr(a.UsedUSD, b.UsedUSD) && sameFloatPtr(a.UsedPercent, b.UsedPercent)
}

// sameFloatPtr compares two optional amounts, absence included.
func sameFloatPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// usageMsg is the held usage segment as a wire message, used to seed a freshly
// attaching client on hello (before its first poll tick).
func (s *Server) usageMsg() proto.ServerMsg {
	s.mu.Lock()
	text, info := s.usageText, s.usageInfo
	s.mu.Unlock()
	return proto.ServerMsg{Type: "usage", Usage: text, UsageInfo: info}
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
	var sampleCwd []cwdSample   // panels that settled and have no directory of their own to report
	var closeAfter []string     // spawn-on-demand panels to reap now their task is done
	for i := range s.panels {
		p := &s.panels[i]
		if p.State == panel.Exited {
			continue
		}
		// The task event is consumed, not read: the tick that sees a task go
		// terminal-done is the only one entitled to promote the panel to done.
		taskDone := s.taskSettled[p.ID]
		delete(s.taskSettled, p.ID)
		if ns, ok := nextState(s.signalsLocked(*p, taskDone)); ok {
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
			// A panel that has settled is sitting at a prompt: the moment its
			// working directory is both stable and about to matter. Collected here
			// and read after the lock, since it asks the OS.
			//
			// The pid comes from the PTY manager, not the panel: Panel.Pid is joined
			// in only when a snapshot is built for the wire, so the in-memory fleet
			// carries a zero there.
			if ns == panel.Idle || ns == panel.Attention {
				if pid := s.livePid(p.ID); s.wantsCwdSampleLocked(p.ID, pid) {
					sampleCwd = append(sampleCwd, cwdSample{id: p.ID, pid: pid})
				}
			}
			// The panel just settled. If a dispatch was held for it, deliver it and
			// move its task to dispatched; otherwise a running task whose agent has
			// gone quiet is finished — mark it done.
			if dispatchReady(ns) {
				if data, held := s.pendingDispatch[p.ID]; held {
					delete(s.pendingDispatch, p.ID)
					deliver = append(deliver, readyDispatch{id: p.ID, data: data})
					s.advanceTaskLocked(p.ID, task.Dispatched, "")
				} else {
					s.advanceTaskLocked(p.ID, task.Done, "")
					// A spawn-on-demand worker asked to be closed when its task
					// settles — reap it after the lock so the fleet frees up.
					if tid, ok := s.panelTask[p.ID]; ok {
						if t := s.tasks[tid]; t != nil && t.Status == task.Done && t.Spawn != nil && t.Spawn.CloseOnDone {
							closeAfter = append(closeAfter, p.ID)
						}
					}
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
		// A new look is a change worth broadcasting in its own right: the summary
		// fold reads Sig, so a cockpit holding a stale one folds by stale data.
		if s.refreshSigLocked(p.ID, p.State) {
			changed = true
		}
	}

	// Drain the queued backlog onto any free idle agents this tick; assignments
	// also produce deliveries and change panels, so they refresh the dashboard.
	// spawns are spawn-on-demand tasks with no free agent — provisioned below,
	// off the lock.
	assigned, spawns := s.scheduleLocked()
	if len(assigned) > 0 {
		deliver = append(deliver, assigned...)
		changed = true
	}

	var out []proto.Panel
	if changed && len(s.clients) > 0 {
		pids := s.pty.Pids()
		out = make([]proto.Panel, len(s.panels))
		for i, p := range s.panels {
			out[i] = s.wirePanel(p, pids)
		}
	}
	s.mu.Unlock()

	// Deliver held dispatches outside the lock — a PTY write must not block under
	// mu, and a panel that just settled is waiting for input, so the write lands.
	for _, d := range deliver {
		s.writeInput(d.id, d.data)
	}

	// Read the settled panels' directories off the lock: the process table is a
	// syscall away, which is cheap but not something to hold the fleet for.
	for _, c := range sampleCwd {
		s.sampleCwdFromProcess(c.id, c.pid)
	}

	// Reap finished ephemeral workers and provision new ones for the backlog, both
	// off the lock (close/create take it themselves). Either reshapes the fleet, so
	// a fresh authoritative snapshot supersedes this tick's stale telemetry.
	structural := false
	for _, id := range closeAfter {
		if s.closePanel(id) == nil {
			structural = true
		}
	}
	if len(spawns) > 0 && s.applyScheduledSpawns(spawns) {
		structural = true
	}
	if structural {
		return s.panelsMsg(), true
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
	// Enforce the uid-private socket invariant before this connection can send a
	// single command. A stranger who reaches the socket could otherwise spawn any
	// process (panel.create) as this user; the conductor fence is explicitly a
	// guardrail, not this boundary. A peer whose uid cannot be confirmed to match
	// ours is dropped — the check fails closed on a real unix peer.
	if ok, err := sameUserPeer(conn); err != nil || !ok {
		if err != nil {
			log.Warn().Err(err).Msg("rejecting control connection: cannot verify peer identity")
		} else {
			log.Warn().Msg("rejecting control connection from a different user")
		}
		_ = conn.Close()
		return
	}

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
		since:     time.Now(),
	}
	cc.id = s.nextConnID()
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
				continue
			}
			// A "goodbye" is the server dropping this connection on purpose — a
			// refused remote attach, a kick, remote being switched off. The teardown
			// rides the message rather than racing it: only once the reason is on the
			// wire is the socket broken, so the far cockpit can always say why it
			// went rather than just finding the pipe gone.
			if msg.Type == "goodbye" {
				broken = true
				closeConn()
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
	case "remote.status", "remote.enable", "remote.disable", "remote.rotate", "remote.kick":
		// Remote access is an operator surface, and the sharpest one there is: the
		// verbs behind it open the machine to another host, mint the code that lets
		// a cockpit in, and drop the operator's own attachment. An agent driving the
		// fleet has no business reading the passkey, let alone rotating it.
		return "conductor role: remote access is an operator surface"
	case "task.drain":
		return "conductor role: draining the backlog is an operator action"
	case "conductor.reset":
		// Resetting the workspace deletes the conductor's own accumulated state
		// while it is the thing running in it. It is an operator's escape hatch for
		// a workspace that has gone bad, and an agent that has gone bad is exactly
		// the one that must not be able to reach for it.
		return "conductor role: resetting the workspace is an operator action"
	case "panel.close", "panel.signal", "panel.input", "panel.dispatch":
		// Self-targeted control is forbidden: closing/signalling itself kills the
		// agent mid-command, feeding itself input loops, or dispatching a task onto
		// its own panel. targetIDs folds in cmd.ID, so it covers panel.input and
		// panel.dispatch (which address a single panel) too.
		if cc.self != "" && slices.Contains(targetIDs(cmd), cc.self) {
			return "conductor role: cannot act on its own panel"
		}
	case "panel.attention", "panel.resolve":
		// The one pair that is fenced the other way round. Raising a hand is not
		// destructive, so a conductor is allowed to do it — but only ABOUT ITSELF. A
		// declaration takes its panel out of the scheduler's free pool until
		// something withdraws it, so a conductor free to raise hands across the fleet
		// is a conductor that can freeze the backlog for every other agent, one
		// looping call at a time. An empty id already means "my own panel".
		if cc.self != "" && cmd.ID != "" && cmd.ID != cc.self {
			return "conductor role: may only raise its own hand"
		}
	case "panel.log", "panel.logview":
		// Logging is an operator surface for the same reason the inbox is, and with an
		// edge the inbox does not have: panel.log asks the DAEMON to write files, on
		// the daemon's host, as the user. The remote actions are already fenced away
		// from a conductor, and this is the same shape of question. Reading a log back
		// is fenced with it rather than separately — a viewer the agent can open is a
		// transcript of another panel it can read at leisure, which is the surface the
		// panel.tail fence exists to keep shut.
		return "conductor role: panel logging is an operator surface"
	case "panel.tail", "panel.ack":
		// The inbox verbs, and the inbox is an operator surface. There is no conductor
		// queue — an agent triaging the fleet's attention is a design this round
		// deliberately did not build (DESIGN §12) — and the fence is where that
		// decision is ENFORCED rather than merely intended. Reading another panel's
		// trailing output is not destructive, so this is a boundary rather than a
		// guardrail; opening it later is deleting one line, and interface room is
		// left for exactly that.
		return "conductor role: the inbox is an operator surface"
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
	// A scoped connection does not get to choose its own policy. The profile name
	// is what a panel's resource caps resolve through, so an agent free to name one
	// is an agent free to name its way into caps wider than the fleet's; dropping
	// it here makes that a property of the server rather than a convention the
	// clients happen to keep.
	if cc.role == roleConductor {
		cmd.Profile = ""
	}
	switch cmd.Action {
	case "hello":
		// A cockpit that reached this daemon over the ssh bridge declares
		// roleRemote and carries the passkey. Refuse it here, before it can send a
		// single other command, and tell it why on the way out.
		if cmd.Role == roleRemote {
			if reason := s.admitRemote(cmd, cmd.Source); reason != "" {
				send(cc, goodbye(reason))
				return
			}
		}
		// Written under mu because the remote overlay's list reads them from ANOTHER
		// goroutine (the pushing one). Every other read is on this command loop, so
		// the lock is held for exactly as long as the assignment.
		s.mu.Lock()
		cc.role, cc.self, cc.source, cc.greeted = cmd.Role, cmd.Self, cmd.Source, true
		s.mu.Unlock()
		send(cc, proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion, ServerVer: s.version,
			Enforce: string(s.cg.Mode()), EnforceWhy: s.cg.Reason()})
		send(cc, s.panelsMsg())
		send(cc, statsMsg()) // seed the footer immediately, before the first tick
		if s.usageProvider != nil {
			send(cc, s.usageMsg()) // seed the account usage segment from the held value
		}
		// The connection list gained a row, and this connection has not seen one at
		// all yet — push to everyone so an open overlay on either side is current.
		s.pushRemote()
	case "remote.status":
		send(cc, proto.ServerMsg{Type: "remote", Remote: s.remoteInfoFor(cc)})
	case "remote.enable", "remote.rotate", "remote.disable":
		s.onRemoteControl(cc, cmd.Action)
	case "remote.kick":
		if !s.kickConn(cmd.Conn, connSource(cc)) {
			send(cc, proto.ServerMsg{Type: "error", Error: "no such connection: " + cmd.Conn})
		}
	case "panel.list":
		send(cc, s.panelsMsg())
	case "panel.create":
		if _, err := s.createPanel(cmd.Kind, cmd.Path, cmd.Args, cmd.Dir, cmd.Profile, cmd.Conductor, cmd.GlobalShell); err != nil {
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
	case "conductor.reset":
		if err := s.resetConductorWorkspace(); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
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
	case "panel.favourite", "panel.unfavourite":
		if err := s.setFavourite(targetIDs(cmd), cmd.Action == "panel.favourite"); err != nil {
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
	case "panel.log":
		// Start or stop writing the panel's output to a file on the machine the fleet
		// runs on. It broadcasts on its own (the card badges), so there is nothing to
		// broadcast here; the reply is a notice naming the file.
		if err := s.toggleLog(cc, cmd.ID); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
	case "panel.logview":
		// Open the panel's log in a transient, auto-zoomed panel that FOLLOWS the file
		// — the same ephemeral mechanism the git menu uses, so it is reaped on exit and
		// on the way back to the dashboard, and never becomes a card in the fleet.
		if err := s.openLogView(cc, cmd.ID); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
	case "fleet.search":
		// Scan every panel's retained output for the term and reply "search" with the
		// matching lines; the cockpit renders them grouped by panel. Read-only — it
		// touches no panel state and spawns nothing — so a failure (only an empty term)
		// just surfaces as an error, like panel.diff.
		if err := s.sendSearch(cc, cmd.Query); err != nil {
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
	case "group.favourite", "group.unfavourite":
		if err := s.setGroupFavourite(cmd.Group, cmd.Action == "group.favourite"); err != nil {
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
		cfg, cmds, footer, backends := s.clientConfig, s.pluginCmds, s.footerText, s.agents
		s.mu.Unlock()
		send(cc, proto.ServerMsg{Type: "config", Config: cfg, Commands: cmds, Footer: footer, Agents: backends})
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
		// Nudge the size so the program repaints a full frame over the replay: a fresh
		// emulator cannot losslessly reconstruct a differential renderer (claude) from
		// the bounded ring, leaving ghost cells until a real SIGWINCH forces a clean paint.
		s.pty.ForceRepaint(cmd.ID)
	case "panel.detach":
		s.detach(cc, cmd.ID)
	case "panel.input":
		s.pty.Write(cmd.ID, cmd.Data)
	case "panel.dispatch":
		// Assign a task to a panel: record the brief and deliver it to the process as
		// a unit. Unlike panel.input (raw keystrokes), the server knows the objective,
		// so it reaches every frontend's card and the snapshot. The target panel is
		// known here, so the brief carries its context and the rendered score block
		// — the one path that injects fleet memory (#39).
		s.dispatchFiltered(cc, s.dispatchBrief(cmd.ID, cmd.Prompt), func(b TaskBrief) error {
			return s.dispatchScored(cmd.ID, b.Prompt, b.Score, cmd.Submit)
		})
	case "panel.dispatch-group":
		// Fan one task to every member of a work item — the mechanic behind racing N
		// agents on the same prompt. The reply error names an empty/unknown group.
		// No score block on fan-out in S0: per-member delivery is R5's problem (#39).
		s.dispatchFiltered(cc, TaskBrief{Prompt: cmd.Prompt, Group: cmd.Group}, func(b TaskBrief) error {
			_, err := s.dispatchGroup(cmd.Group, b.Prompt, cmd.Submit)
			return err
		})
	case "task.enqueue":
		// Add a task to the backlog; the scheduler drains it onto a free agent. When
		// the command carries a spawn command (Path), the task provisions its own
		// agent if none is free — Args/Dir shape it, Ephemeral closes it on done. The
		// reply error names a full queue.
		var spawn *task.SpawnSpec
		if cmd.Path != "" {
			spawn = &task.SpawnSpec{Command: cmd.Path, Profile: cmd.Profile, Args: cmd.Args, Dir: cmd.Dir, CloseOnDone: cmd.Ephemeral}
		}
		s.dispatchFiltered(cc, TaskBrief{Prompt: cmd.Prompt, Group: cmd.Group}, func(b TaskBrief) error {
			_, err := s.enqueueTask(b.Prompt, cmd.Group, spawn)
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
	case "task.promote", "task.demote":
		// Reorder a queued task within the backlog: promote bumps it to the head,
		// demote drops it to the tail. Only a waiting task can be reordered.
		if err := s.reprioritizeTask(cmd.ID, cmd.Action == "task.promote"); err != nil {
			send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
			return
		}
		send(cc, s.tasksMsg())
	case "task.drain":
		s.drainQueued()
		send(cc, s.tasksMsg())
	case "panel.attention":
		// An agent raising its own hand, with a reason — the top of the detection
		// precedence. Reply and re-derive both happen in attention.go.
		s.declareAttention(cc, cmd)
	case "panel.resolve":
		// …and putting it back down, without waiting for its next byte of output to
		// be downgraded by a timer.
		s.resolveAttention(cc, cmd)
	case "panel.tail":
		// The inbox pulling one row's trailing output, so its detail pane shows the
		// same bytes the Monitor sniffed. The clamp and the reply live in tail.go.
		s.sendTail(cc, cmd.ID, cmd.Count)
	case "panel.ack":
		// A human dealt with a panel from the inbox — dismissed, snoozed, or
		// answered it. The record is fleet state; the reasoning is in ack.go.
		s.ackPanel(cc, cmd)
	case "panel.resize":
		s.pty.Resize(cmd.ID, cmd.Rows, cmd.Cols)
	case "score.submit":
		// Record a fleet-memory note. Deliberately absent from guardConductor's
		// deny list: submission is open to every panel by design (#38) — the
		// memory is fed by agents and operators alike.
		s.scoreSubmit(cc, cmd)
	case "score.list":
		send(cc, proto.ServerMsg{Type: "score", Score: s.scoreList()})
	case "score.status":
		send(cc, proto.ServerMsg{Type: "score", Score: s.scoreStatus()})
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
// profile names the agent profile the spawn came from. It is recorded with the
// panel and is what its resource limits resolve through, so the caps follow the
// config rather than being frozen here; an empty name resolves to the fleet-wide
// limits alone.
//
// A conductor panel is a special agent: the server enforces at most one, runs it
// in the socket's managed workspace (not any source tree) instead of dir, and
// injects the socket + identity env so the agent inside can drive the fleet under
// the scoped conductor role.
func (s *Server) createPanel(kind, path string, args []string, dir, profile string, conductor, globalShell bool) (string, error) {
	if kind == "" {
		kind = proto.KindShell
	}
	if conductor {
		kind = proto.KindAgent // a conductor is always an agent
	}
	if globalShell {
		kind = proto.KindShell // a global shell is always a plain host shell
	}

	s.mu.Lock()
	if conductor && s.hasConductorLocked() {
		s.mu.Unlock()
		return "", fmt.Errorf("a conductor already exists")
	}
	if globalShell && s.hasGlobalShellLocked() {
		s.mu.Unlock()
		return "", fmt.Errorf("a global shell already exists")
	}
	if conductor {
		s.conductorPending = true // reserve the singleton across the unlocked spawn below
	}
	if globalShell {
		s.globalShellPending = true // reserve the singleton across the unlocked spawn below
	}
	s.seq++
	id := fmt.Sprintf("%d", s.seq)
	if dir == "" {
		dir = s.defaultDir // read under the lock so a SIGHUP reload cannot race it; empty still falls back to home
	}
	s.mu.Unlock()

	// A conductor runs in the server-managed workspace for this socket, never dir, and
	// carries the identity env. Build them before the spec so a failure cleans up
	// the reservation and any half-made workspace. Every other agent panel gets
	// the same identity minus the scoped role: it is told which panel it is, and
	// is granted nothing by being told (see panelEnv).
	var env []string
	switch {
	case conductor:
		ws, err := s.makeConductorWorkspace(id)
		if err != nil {
			s.clearConductorPending()
			return "", err
		}
		dir, env = ws, s.conductorEnv(id)
	case kind == proto.KindAgent:
		env = s.panelEnv(id)
	}
	// A global shell always opens in $HOME — a stable "home base", never dir or the
	// configured default. No workspace, no identity env: it drives nothing.
	if globalShell {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		}
	}

	// Build the spawn spec once, then use the same value to start the PTY and to
	// stash for respawn — so a restored panel re-runs with exactly what launched it
	// (a shell carries no args; an agent does).
	var spec ptymgr.Spec
	switch kind {
	case proto.KindShell:
		// A shell panel carries no identity env, and that is a decision rather than
		// an oversight. The case for giving it one is real enough: a person who
		// types `echo $BATON_PANEL_ID` would get an answer. It loses on two counts.
		// A shell is a launcher — every program a person starts in it inherits the
		// marking, so the id would trail tools that have nothing to do with the
		// panel, where an agent panel runs the single long-lived process that IS the
		// panel. And the human already has what the agent lacks: the cockpit shows
		// the panel, `baton ctl list` names the ids, and `--id` is one flag away;
		// the reason an agent must be told is precisely that it can see neither.
		// The socket needs no injecting: the daemon pins BATON_SOCK into its own
		// environment when it re-execs (daemonEnviron in cmd/baton), and ptymgr
		// starts every panel from os.Environ(), so a shell panel inherits it and
		// `baton ctl` there already reaches this server at full cockpit power. Note
		// that it is the inherited variable doing the work, not paths.Socket()'s
		// fallback: a panel is Setsid'd into its own session, so recomputing the
		// session-scoped default inside one yields a socket that does not exist.
		// The global shell has been held to this exact contract since it landed,
		// and both shell kinds stay identical here.
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

	if err := s.startPanel(id, profile, spec); err != nil {
		if conductor {
			// The workspace stays: it is this socket's one conductor workspace, and it
			// may already hold the settings an earlier conductor collected. A failed
			// spawn is no reason to throw those away — the next attempt reuses it.
			s.clearConductorPending()
		}
		if globalShell {
			s.clearGlobalShellPending()
		}
		return "", err
	}

	p := panel.Panel{
		ID:          id,
		Kind:        panel.ParseKind(kind),
		Title:       panelTitle(kind, path, dir, id),
		State:       panel.Spawning,
		Activity:    activityText(panel.Spawning, 0), // the Monitor keeps it live from here
		Conductor:   conductor,
		GlobalShell: globalShell,
	}
	if conductor {
		p.Title = "conductor · " + id
	}
	if globalShell {
		p.Title = "shell · " + id
	}
	s.mu.Lock()
	s.panels = append(s.panels, p)
	s.specs[id] = spawnSpec{Spec: spec, Profile: profile} // the exact spec StartCmd launched, so respawn reproduces it
	s.mon.spawned(id)                                     // start the Monitor's clock; first output wakes it to running
	if conductor {
		s.conductorPending = false // the singleton is now a real panel
	}
	if globalShell {
		s.globalShellPending = false // the singleton is now a real panel
	}
	fields := panelFields(p)
	caps := s.effectiveLimitsLocked(profile).Fields()
	if caps != nil {
		fields["limits"] = caps // the caps this panel resolved to, so a plugin sees the policy it spawned under
	}
	s.emit("panel.spawn", fields)
	s.mu.Unlock()

	log.Info().Str("panel", p.Title).Str("profile", profile).Interface("limits", caps).Msg("panel created")
	// A profile configured to log does so from the moment it spawns. It is done
	// here rather than in startPanel because startPanel is also the fork point for
	// the transient panels — a diff pop-up, the scratch shell, a log viewer — and
	// those are not the fleet, so they are not the transcript either.
	s.autoLog(id, profile)
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

// hasGlobalShellLocked reports whether a global shell already exists or is mid-
// spawn. It holds the singleton invariant exactly like hasConductorLocked: a
// second global-shell create is refused while the first is live (running or an
// exited dead slot) or being created. Caller holds s.mu.
func (s *Server) hasGlobalShellLocked() bool {
	if s.globalShellPending {
		return true
	}
	for _, p := range s.panels {
		if p.GlobalShell {
			return true
		}
	}
	return false
}

func (s *Server) clearGlobalShellPending() {
	s.mu.Lock()
	s.globalShellPending = false
	s.mu.Unlock()
}

// panelEnv is the identity baton injects into an agent panel's process: the
// control socket to dial, and the panel's own id. Together they are the answer to
// a question an agent could not previously ask — "which panel am I?" — and the
// whole reason it matters is that a control client which cannot name itself
// cannot say anything about itself. `baton ctl` reads both on Dial, so the agent
// inside the panel reaches this server and declares a self without anyone having
// to tell it. Until this existed only the conductor was told, which meant a
// fleet of fifty agents had exactly one member able to speak about itself.
//
// The role is left empty, and what that means should not be overstated. It means
// only that an ordinary agent must not be handed the CONDUCTOR's role: that role
// carries a fence built for the one panel that drives the fleet (no self-close,
// no self-dispatch, a spawn rate cap, a fleet ceiling) plus the policy stripping
// that travels with it, none of which describes a worker. An empty role is the
// plain, unscoped connection, so a worker keeps exactly the reach it already had
// before this: full cockpit power over the whole fleet, including closing its
// neighbours. That is the status quo, not a decision — an agent panel could do
// all of it before an id was ever injected, and injecting one widens nothing.
//
// It is also not the end state. A worker-scoped role — open for the verbs an
// agent needs about ITSELF (attention, resolve, rename) and closed against
// panel.close, panel.signal, panel.input, panel.dispatch and panel.create on
// anyone else — is a known follow-up, and the identity injected here is the
// prerequisite it was waiting on. This unit neither opens that gap nor closes
// it; it should not be read as having settled the question.
//
// A panel that runs baton itself inherits these variables and nothing needs
// guarding. The cockpit (internal/client) says hello with no role and no self at
// all, so a nested TUI attaches at full power no matter what it inherited. A
// nested *server* is covered too: ptymgr appends a spec's env after os.Environ()
// and os/exec keeps the last occurrence of a duplicate key, so an inner daemon's
// injection overrides the outer one and each panel is told the id its own server
// gave it. Only `baton ctl` and `baton mcp` read these at all, and for them
// inheritance is the right answer — a helper the agent shells out to is still
// acting on that panel's behalf.
//
// None of this is a security boundary, and the comment should not be read as
// claiming otherwise. The socket is uid-private and both role and self are
// self-declared, so any local process of your user can dial it and claim to be
// any panel it likes. Injecting the id makes the honest case work; it does not
// make the dishonest one impossible. The server's fence (see guardConductor) is
// documented under exactly the same limit.
func (s *Server) panelEnv(id string) []string {
	return []string{
		paths.EnvSocket + "=" + s.socketPath(),
		paths.EnvPanelID + "=" + id,
	}
}

// conductorEnv is panelEnv plus the one thing that makes a conductor a conductor:
// the scoped role its control client declares on hello, which the server fences.
// The rest of its identity — the socket to dial, and the own panel id it is
// fenced from acting on — is the same identity every agent panel now carries.
func (s *Server) conductorEnv(id string) []string {
	return append(s.panelEnv(id), paths.EnvRole+"="+roleConductor)
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

// makeConductorWorkspace resolves this socket's one conductor workspace — creating
// it, or reusing what a previous conductor left there — and seeds it with the
// control wiring (see writeConductorFiles).
//
// The path is logged because it is no longer guessable: the directory name is a
// hash of the socket, and a user who wants to look inside it (or delete it by
// hand) has no other way to find out where it went.
func (s *Server) makeConductorWorkspace(id string) (string, error) {
	ws, err := paths.ConductorWorkspace(s.socketPath())
	if err != nil {
		return "", fmt.Errorf("create conductor workspace: %w", err)
	}
	writeConductorFiles(ws, id)
	log.Info().Str("panel", id).Str("workspace", ws).Msg("conductor workspace ready")
	return ws, nil
}

// refreshConductorWiring rewrites the conductor's workspace wiring on a reload, so
// an edited operator brief ($HOME/.baton/CONDUCTOR.md) reaches the conductor
// without closing and reopening it. Before v1.4.0 there was nowhere to write it:
// the workspace was a throwaway directory that existed only while the panel did.
// A workspace that outlives the panel is what makes a reload able to touch it.
//
// It refreshes the files and says so; it does not pretend to have changed the
// agent's mind. A running agent reads its project instructions when its session
// starts — Claude Code reads CLAUDE.md once — so the new brief is what it will
// see the next time it looks, and the notice tells whoever is watching that there
// is something new to look at. Feeding the agent a prompt instead would interrupt
// whatever it is doing mid-turn, which is a worse trade for a config reload.
//
// A conductor that has exited still gets its files rewritten (the workspace is
// there and a re-run should not need a second reload) but no notice: there is
// nothing running to tell.
func (s *Server) refreshConductorWiring() {
	s.mu.Lock()
	var id, dir string
	live := false
	for _, p := range s.panels {
		if p.Conductor {
			id, dir, live = p.ID, s.specs[p.ID].Dir, p.State != panel.Exited
			break
		}
	}
	s.mu.Unlock()
	if id == "" || dir == "" || !dirExists(dir) {
		return
	}

	writeConductorFiles(dir, id)
	log.Info().Str("panel", id).Str("workspace", dir).Msg("conductor brief refreshed")
	if live {
		s.notifyAttached(id, "\r\n[conductor brief updated — re-read BATON.md]\r\n")
	}
}

// resetConductorWorkspace deletes this socket's conductor workspace, so the next
// conductor starts from nothing. It is the way out of a workspace whose collected
// state has gone bad — the ordinary lifecycle keeps it, and a reboot is otherwise
// the only thing that clears it.
//
// It refuses while a conductor exists in the fleet, exited slot included. Deleting
// the directory under a live agent would leave it running in an unlinked cwd,
// which fails in ways that look like anything but a reset; and a panel that is
// merely exited can still be re-run, which would silently recreate the workspace
// straight after. Close it first — the message says so, because a wedged conductor
// is precisely when someone reaches for this.
func (s *Server) resetConductorWorkspace() error {
	s.mu.Lock()
	live := s.hasConductorLocked()
	s.mu.Unlock()
	if live {
		return fmt.Errorf("close the conductor first, then reset its workspace")
	}

	ws, err := paths.ConductorWorkspace(s.socketPath())
	if err != nil {
		return fmt.Errorf("resolve conductor workspace: %w", err)
	}
	if err := paths.RemoveConductorWorkspace(ws); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset conductor workspace: %w", err)
	}
	log.Info().Str("workspace", ws).Msg("conductor workspace reset")
	return nil
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
	// Mark the intent before the kills land, so the exits they cause are read as
	// the daemon going down rather than as a fleet-wide crash to be undone.
	s.mu.Lock()
	s.shuttingDown = true
	for _, st := range s.restarts {
		if st.timer != nil {
			st.timer.Stop()
		}
	}
	s.mu.Unlock()

	n := s.pty.KillAll(syscall.SIGKILL)
	if n > 0 {
		log.Info().Int("panels", n).Msg("killed live panels on shutdown")
	}
	// The kills above ended the runtime CLIENTS of any isolated panels; the
	// containers they were attached to are the daemon's children, not ours, and
	// survive unless they are removed by name.
	s.sweepContainers()
	// Every open transcript is flushed and closed with a marker saying why, so a
	// log never simply stops mid-line with nothing to say what happened.
	s.closeAllLogs(logMarkShutdown)
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
			ID:          p.ID,
			Kind:        p.Kind.String(),
			Title:       p.Title,
			Group:       p.Group,
			Task:        p.Task,
			Pinned:      p.Pinned,
			Favourite:   p.Favourite,
			Conductor:   p.Conductor,
			GlobalShell: p.GlobalShell,
			Spec:        state.Spec{Command: spec.Command, Args: spec.Args, Dir: spec.Dir, Profile: spec.Profile},
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
	for g := range s.groupFavourite { // the map only ever stores true — a false deletes the key
		gview(g).Favourite = true
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
			ID:          ps.ID,
			Kind:        panel.ParseKind(ps.Kind),
			Title:       ps.Title,
			Group:       ps.Group,
			Task:        ps.Task,
			Pinned:      ps.Pinned,
			Favourite:   ps.Favourite,
			Conductor:   ps.Conductor,
			GlobalShell: ps.GlobalShell,
			State:       panel.Exited,
			Activity:    "restored · press r to re-run",
		})
		s.specs[ps.ID] = spawnSpec{
			Spec:    ptymgr.Spec{Command: ps.Spec.Command, Args: ps.Spec.Args, Dir: ps.Spec.Dir},
			Profile: ps.Spec.Profile,
		}
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
		if g.Favourite {
			s.groupFavourite[g.Group] = true
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
			// A terminal task should not have a live file — its removal nudge was
			// dropped under a full taskDirty channel when it finished. Delete the
			// orphan now so it cannot accumulate across restarts.
			_ = s.qstore.Remove(tk.ID)
			continue
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
	return s.dispatchScored(id, prompt, "", submit)
}

// dispatchScored is dispatchPanel with a score block riding the DELIVERED bytes
// only: the block precedes the prompt in what the process receives, while the
// recorded brief — the card, the snapshot, a restart's restore — stays the bare
// prompt, because the block is advice for this one delivery, not part of the
// objective. An empty scoreBlock is the plain dispatch.
func (s *Server) dispatchScored(id, prompt, scoreBlock, submit string) error {
	if id == "" {
		return fmt.Errorf("panel.dispatch needs an id")
	}
	if prompt == "" {
		return fmt.Errorf("panel.dispatch needs a prompt")
	}
	delivered := prompt
	if scoreBlock != "" {
		delivered = scoreBlock + "\n\n" + prompt
	}
	data := dispatchData(delivered, submit)

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
		"result":   t.Result,
		"priority": t.Priority,
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
// one place the panel lifecycle drives the task lifecycle. A non-empty result is
// recorded as the task's terminal note (why it failed), so a finished task carries
// a reason rather than a blank. A move into a terminal state prunes the finished
// history so it stays bounded. Caller holds s.mu.
func (s *Server) advanceTaskLocked(panelID string, status task.Status, result string) {
	tid, ok := s.panelTask[panelID]
	if !ok {
		return
	}
	t := s.tasks[tid]
	if t == nil || !task.CanAdvance(t.Status, status) {
		return
	}
	t.Status = status
	if result != "" {
		t.Result = result
	}
	if status == task.Done {
		// The server SAW the work finish rather than inferring it from silence, so
		// the panel's turn is over now. The tick that reads this edge clears it.
		s.taskSettled[panelID] = true
	}
	t.Updated = s.mon.now()
	s.emit("task.change", taskFields(t))
	s.markTaskDirtyLocked(t.ID)
	if status.Terminal() {
		s.pruneHistoryLocked()
	}
}

// maxTaskHistory caps how many finished (done/failed) tasks linger in memory as
// history — enough to review a recent burst without the task table (and the queue
// view) growing without bound over a long session.
const maxTaskHistory = 50

// pruneHistoryLocked trims finished tasks to the most recently updated
// maxTaskHistory, dropping the oldest beyond it. Live tasks (queued, dispatched,
// running) are never touched — only terminal history is bounded. A dropped task's
// file is already gone (removed when it went terminal); the dirty nudge reconciles
// any stragglers. Caller holds s.mu.
func (s *Server) pruneHistoryLocked() {
	var terminal []*task.Task
	for _, t := range s.tasks {
		if t.Status.Terminal() {
			terminal = append(terminal, t)
		}
	}
	if len(terminal) <= maxTaskHistory {
		return
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].Updated.After(terminal[j].Updated) })
	for _, t := range terminal[maxTaskHistory:] {
		delete(s.tasks, t.ID)
		if t.Panel != "" && s.panelTask[t.Panel] == t.ID {
			delete(s.panelTask, t.Panel) // drop a mapping left dangling by the pruned task
		}
		s.markTaskDirtyLocked(t.ID)
	}
}

// maxExitedPanels caps how many exited panels linger as dead slots in the fleet
// (and therefore in the persisted snapshot). Exited panels are kept so their final
// output can still be reviewed, but a long-lived daemon that spawns and reaps many
// panels would otherwise grow s.panels — and state.json, which reloads them as
// dead slots — without bound until a manual purge. Like maxTaskHistory it bounds
// retained history rather than live state.
const maxExitedPanels = 128

// pruneExitedLocked drops the oldest exited panels beyond maxExitedPanels, freeing
// their retained spec and monitor state so a busy daemon's fleet cannot grow
// without bound. Live panels are never touched, and the newest exited slots (the
// ones a user is most likely reviewing) are kept. It returns the ids whose PTY the
// caller must Stop AFTER releasing s.mu, since that touches the PTY manager. A
// pruned conductor leaves its workspace on disk: the workspace belongs to the
// socket rather than to the panel, and the next conductor opens straight back
// into it. Caller holds s.mu.
func (s *Server) pruneExitedLocked() (stop []string) {
	exited := 0
	for i := range s.panels {
		if s.panels[i].State == panel.Exited {
			exited++
		}
	}
	drop := exited - maxExitedPanels
	if drop <= 0 {
		return nil
	}
	kept := make([]panel.Panel, 0, len(s.panels))
	for _, p := range s.panels {
		if drop > 0 && p.State == panel.Exited {
			drop--
			stop = append(stop, p.ID)
			s.mon.forget(p.ID)
			delete(s.specs, p.ID)
			delete(s.sessions, p.ID)
			s.forgetRestartLocked(p.ID)
			s.forgetCwdLocked(p.ID)
			delete(s.declared, p.ID)
			delete(s.acked, p.ID)
			delete(s.taskSettled, p.ID)
			delete(s.exitedAt, p.ID)
			delete(s.pendingDispatch, p.ID)
			delete(s.panelTask, p.ID) // the panel is gone; its task history is bounded separately
			continue
		}
		kept = append(kept, p)
	}
	s.panels = kept
	return stop
}

// dispatchReady reports whether a panel in this state can receive a dispatched
// prompt now: a settled agent is ready; one still spawning or actively producing
// output is not, so the dispatch is held until it settles.
//
// done and stuck are settled by definition — both describe a panel that has
// stopped producing output — and both MUST be listed here. A held dispatch is
// only ever released by a transition, and neither state moves again without new
// output, so leaving them out would strand a prompt sent to a panel that had
// simply been quiet for a minute.
func dispatchReady(st panel.State) bool {
	switch st {
	case panel.Idle, panel.Attention, panel.Done, panel.Stuck:
		return true
	}
	return false
}

// freeForWork reports whether an agent in this state may be handed a task off
// the backlog: quiet, and not asking for anything.
//
// done and stuck join idle because all three describe an agent that has stopped
// producing output — which is the whole of what idle meant before the ladder
// split it in three. Omitting them would silently shrink the scheduler's pool to
// nothing on any fleet left alone for a minute. attention is excluded on
// purpose: a panel waiting on a human is not waiting for more work.
//
// Including stuck is a deliberate tension, not an oversight: the dashboard is
// telling a human "this agent looks wedged" while the scheduler hands it more
// work. It is resolved in favour of behaviour preservation, because stuck is a
// SUSPICION drawn from silence alone and the panel it describes was plain idle
// before this ladder existed — one an operator would have dispatched to without
// a second thought. A wedged agent that takes a prompt and does nothing with it
// shows up as a task stuck in `dispatched`, which is visible; an agent wrongly
// withheld from the queue shows up as nothing at all, which is not. If that ever
// stops being the right trade, this is the one line to change.
func freeForWork(st panel.State) bool {
	switch st {
	case panel.Idle, panel.Done, panel.Stuck:
		return true
	}
	return false
}

// wakesOnOutput reports whether a byte of output brings a panel back to running.
// Every resting state does; running is already there and exited is terminal.
func wakesOnOutput(st panel.State) bool {
	return st != panel.Running && st != panel.Exited
}

// declaredLocked reports whether an agent's own panel.attention declaration
// currently stands for this panel. Caller holds s.mu.
func (s *Server) declaredLocked(id string) bool {
	d := s.declared[id]
	return d != nil && d.Reason != ""
}

// enqueueTask adds an unassigned task to the backlog for the scheduler to drain
// onto a free agent. It errors when the queued backlog is at queueMax — the cap is
// backpressure on a runaway producer, counting only unassigned tasks, so a busy
// fleet never blocks new work from being queued.
func (s *Server) enqueueTask(prompt, group string, spawn *task.SpawnSpec) (string, error) {
	if prompt == "" {
		return "", fmt.Errorf("task.enqueue needs a prompt")
	}
	if spawn != nil && spawn.Command == "" {
		return "", fmt.Errorf("a spawn-on-demand task needs a command to run")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queueMax > 0 && s.queuedBacklogLenLocked() >= s.queueMax {
		return "", fmt.Errorf("queue is full (%d queued); raise queue.max or let it drain", s.queueMax)
	}
	t := s.upsertTaskLocked("", prompt, group, task.Queued)
	if spawn != nil {
		t.Spawn = spawn
		s.markTaskDirtyLocked(t.ID) // persist the spawn spec alongside the task
	}
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
		if p.Kind != panel.Agent || p.Conductor || !freeForWork(p.State) {
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

// spawnRequest is a queued spawn-on-demand task with no free agent: the panel to
// provision for it, executed once the monitor lock is released.
type spawnRequest struct {
	taskID string
	spec   task.SpawnSpec
}

// scheduleLocked drains the queued backlog, highest priority (then oldest) first,
// honouring the per-group concurrency cap. It assigns each task to a free idle
// agent — recording the brief, moving the task to dispatched — and returns the
// prompts to deliver once the lock is released. A queued task that carries a spawn
// spec and finds no free agent instead yields a spawnRequest: the scheduler
// provisions a fresh agent for it (below the fleet ceiling) rather than leaving it
// to wait on the standing fleet. Caller holds s.mu.
func (s *Server) scheduleLocked() ([]readyDispatch, []spawnRequest) {
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
		return nil, nil
	}
	sort.Slice(queued, func(i, j int) bool {
		if queued[i].Priority != queued[j].Priority {
			return queued[i].Priority > queued[j].Priority // higher priority drains first
		}
		return queued[i].Created.Before(queued[j].Created) // then oldest-first
	})

	var deliver []readyDispatch
	var spawns []spawnRequest
	for _, t := range queued {
		if s.queueConcurrency > 0 && groupRunning[t.Group] >= s.queueConcurrency {
			continue
		}
		pid, ok := s.freeIdleAgentLocked(t.Group)
		if !ok {
			// No standing agent is free. If the task provisions its own, ask for one
			// panel per task (spawning marks it in flight so a later tick does not
			// double-spawn), staying below the fleet ceiling.
			if t.Spawn != nil && !s.spawning[t.ID] && len(s.panels) < maxConductorFleet {
				s.spawning[t.ID] = true
				groupRunning[t.Group]++ // the pending worker counts against the cap for later tasks
				spawns = append(spawns, spawnRequest{taskID: t.ID, spec: *t.Spawn})
			}
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
	return deliver, spawns
}

// applyScheduledSpawns provisions an agent for each spawn-on-demand task the
// scheduler could not place, then assigns the task to it: the dispatch is held in
// pendingDispatch so the monitor delivers the prompt once the new panel settles. A
// spawn failure fails the task with the reason; a task that vanished mid-spawn
// (cancelled/drained) leaves an orphan panel, which is closed. It runs with s.mu
// released — createPanel and closePanel take the lock themselves. Returns whether
// the fleet changed, so the caller can broadcast a fresh snapshot.
func (s *Server) applyScheduledSpawns(spawns []spawnRequest) bool {
	changed := false
	var orphans []string
	for _, req := range spawns {
		pid, err := s.createPanel(proto.KindAgent, req.spec.Command, req.spec.Args, req.spec.Dir, req.spec.Profile, false, false)
		s.mu.Lock()
		delete(s.spawning, req.taskID)
		t := s.tasks[req.taskID]
		if err != nil {
			if t != nil && !t.Status.Terminal() {
				t.Status, t.Result, t.Updated = task.Failed, "spawn failed: "+err.Error(), s.mon.now()
				s.emit("task.change", taskFields(t))
				s.markTaskDirtyLocked(t.ID)
				s.pruneHistoryLocked()
			}
			s.mu.Unlock()
			continue
		}
		changed = true
		if t == nil || t.Status.Terminal() {
			orphans = append(orphans, pid) // the task went away mid-spawn — reclaim the panel
			s.mu.Unlock()
			continue
		}
		if idx := s.indexLocked(pid); idx >= 0 {
			s.panels[idx].Task = t.Prompt
		}
		t.Panel, t.Status, t.Attempts, t.Updated = pid, task.Dispatched, t.Attempts+1, s.mon.now()
		s.panelTask[pid] = t.ID
		s.pendingDispatch[pid] = dispatchData(t.Prompt, "") // deliver when the fresh panel settles
		s.emit("task.change", taskFields(t))
		s.markTaskDirtyLocked(t.ID)
		s.mu.Unlock()
	}
	for _, id := range orphans {
		_ = s.closePanel(id)
	}
	return changed
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

// reprioritizeTask moves a queued task to the head (up) or tail (down) of the
// backlog by setting its priority just past the current extreme among the other
// queued tasks — a single, predictable "bump to next" / "drop to last" gesture.
// Only a waiting task has a queue position: a task already in flight or finished is
// refused, like cancel.
func (s *Server) reprioritizeTask(id string, up bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("no task %q", id)
	}
	if t.Panel != "" || t.Status != task.Queued {
		return fmt.Errorf("task %q is not queued; only a waiting task can be reordered", id)
	}
	hi, lo, seen := 0, 0, false
	for _, o := range s.tasks {
		if o.ID == id || o.Panel != "" || o.Status != task.Queued {
			continue
		}
		if !seen {
			hi, lo, seen = o.Priority, o.Priority, true
			continue
		}
		hi, lo = max(hi, o.Priority), min(lo, o.Priority)
	}
	if !seen { // the only queued task — nothing to reorder against
		return nil
	}
	if up {
		t.Priority = hi + 1
	} else {
		t.Priority = lo - 1
	}
	t.Updated = s.mon.now()
	s.emit("task.change", taskFields(t))
	s.markTaskDirtyLocked(t.ID)
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

// tasksMsg builds the backlog snapshot reply so a frontend can render the
// queue/kanban. The order mirrors what will happen: live tasks come first in
// scheduler order (higher priority, then oldest), so the pending backlog reads
// top-to-bottom in the sequence it will drain and a reorder visibly moves a row;
// finished history follows, most recent first.
func (s *Server) tasksMsg() proto.ServerMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks := make([]*task.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		if a.Status.Terminal() != b.Status.Terminal() {
			return !a.Status.Terminal() // live tasks before finished history
		}
		if a.Status.Terminal() {
			return a.Updated.After(b.Updated) // history: most recent first
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority // backlog: higher priority first
		}
		return a.Created.Before(b.Created) // then oldest-first, matching the scheduler
	})
	wire := make([]proto.Task, len(tasks))
	for i, t := range tasks {
		wire[i] = proto.Task{
			ID: t.ID, Prompt: t.Prompt, Status: string(t.Status), Panel: t.Panel,
			Group: t.Group, Result: t.Result, Priority: t.Priority, Attempts: t.Attempts,
			Spawn: t.Spawn != nil,
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
		if panel.GroupIsUnder(group, p.Group) && !p.Conductor { // the whole subtree, nested groups included
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
	isAgent := s.isAgentPanelLocked(id) // still under the lock taken above
	spec, ok := s.specs[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("nothing to re-run")
	}

	// Every agent panel has its identity rebuilt on a re-run, and rebuilt rather
	// than replayed. The id is the panel's own and never changes, so it comes back
	// as the same self it was; what is not stable is everything around it. The
	// socket path can move across a daemon restart, and a panel restored from disk
	// carries no env at all — the snapshot persists command, args, dir and profile,
	// never the environment. Taking the values from the live server is what makes a
	// re-run agent know itself as surely as a freshly spawned one, instead of coming
	// back mute or pointed at a socket that is gone. A conductor needs one thing
	// more: a live workspace. Reuse the retained one if it still exists (the common
	// exit→respawn), make a new one if it is gone (e.g. a reboot cleared the runtime
	// dir), and rewrite its wiring either way, so an edited operator brief
	// ($HOME/.baton/CONDUCTOR.md) is picked up on every re-run.
	//
	// Assigning Env replaces it rather than merging into it. That is correct only
	// because an agent spec's Env holds nothing but the identity this server put
	// there — if a profile ever carries custom environment onto a spawn, this line
	// would silently swallow it on the first re-run and must merge instead.
	// A conductor is always an agent, so the second half of the condition never
	// decides anything today; it is there so a conductor's workspace rebuild does
	// not quietly depend on that invariant holding in a restored state file.
	if isAgent || isConductor {
		if isConductor {
			ws, err := s.makeConductorWorkspace(id)
			if err != nil {
				return err
			}
			spec.Dir = ws
			spec.Env = s.conductorEnv(id)
		} else {
			spec.Env = s.panelEnv(id)
		}
		s.mu.Lock()
		s.specs[id] = spec
		s.mu.Unlock()
	}

	// Put the panel back where it was, not merely where it started — the half of
	// the promise that only exists because the directory is tracked. A directory
	// that has since been removed falls back to the spawn directory and says so:
	// coming back somewhere else in silence is the outcome worth avoiding.
	launch := spec.Spec
	dir, notice := s.respawnDir(id, launch, isAgent)
	launch.Dir = dir
	if notice != "" {
		log.Warn().Str("panel", id).Str("dir", dir).Msg(notice)
		s.notifyAttached(id, "\r\n["+notice+"]\r\n")
	}

	// Re-resolve on every re-run rather than reusing what the first spawn landed
	// on, so a respawn is also how a deferred cap (a lowered memory ceiling)
	// finally takes hold.
	if err := s.startPanel(id, spec.Profile, launch); err != nil {
		return err
	}

	s.mu.Lock()
	if i := s.indexLocked(id); i >= 0 {
		s.panels[i].State = panel.Spawning
		s.panels[i].ExitCode = 0 // the old process's status says nothing about the new one
		delete(s.exitedAt, id)   // …nor does when it died
		delete(s.acked, id)      // …nor does a human having dealt with the run that ended
		s.panels[i].Activity = activityText(panel.Spawning, 0)
		// The new process starts in the directory just resolved, whatever the old
		// one had wandered to; the tracker re-learns from there.
		s.panels[i].Cwd = dir
		s.mon.spawned(id) // restart the Monitor's clock; first output wakes it to running
	}
	delete(s.osc7Tail, id) // the old process's output tail says nothing about the new one
	s.mu.Unlock()

	s.resumeLog(id) // a logged panel comes back into the same file, under a new session marker
	log.Info().Str("panel", id).Str("dir", dir).Msg("panel re-run")
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
			s.cg.Release(id)       // the ephemeral is gone; drop its cgroup with it
			s.releaseContainer(id) // and the container it was launched in, if it had one
			log.Info().Str("panel", id).Msg("ephemeral diff panel closed")
			return nil
		}
		s.mu.Unlock()
		return fmt.Errorf("no panel with id %q", id)
	}
	title := s.panels[idx].Title
	s.advanceTaskLocked(id, task.Failed, "panel closed") // closing a panel mid-task abandons it
	s.panels = slices.Delete(s.panels, idx, idx+1)
	s.mon.forget(id)
	delete(s.specs, id)           // the panel is gone for good; drop its retained spawn spec
	delete(s.sessions, id)        // …and the session ids its usage was attributed through
	s.forgetRestartLocked(id)     // …and any restart armed for it: it must not come back
	s.forgetCwdLocked(id)         // …and the output tail kept to read its directory reports
	delete(s.declared, id)        // …and whatever it had said about needing a human
	delete(s.acked, id)           // …and any acknowledgement a human left on it
	delete(s.taskSettled, id)     // …and any pending done edge
	delete(s.exitedAt, id)        // …and the instant it died, if it had
	delete(s.pendingDispatch, id) // and any dispatch held for it
	delete(s.panelTask, id)       // and its task mapping (the task record stays as history)
	s.emit("panel.close", map[string]any{"id": id, "title": title})
	s.mu.Unlock()

	s.pty.Stop(id)                   // no-op for a panel with no live process
	s.cg.Release(id)                 // the panel is gone for good; drop its cgroup with it
	s.releaseContainer(id)           // and the container it was launched in, if it had one
	s.stopLogging(id, logMarkClosed) // …and its transcript is finished rather than abandoned
	// A conductor's workspace stays on disk. It is keyed on the control socket, not
	// on this panel, and the settings the agent collected there are the whole point
	// of it surviving: the next conductor opens into the same directory. Only a
	// reboot (paths.ConductorWorkspace) or an explicit reset clears it.
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
	dir := s.targetDir(targetID, spec.Spec)
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
	dir := s.targetDir(targetID, spec.Spec)
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

	// Resolve the workdir the same way every other target does: where the agent is
	// now when that is known, else where it was launched.
	dir := s.targetDir(targetID, spec.Spec)
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
	// An ephemeral runs in the agent's workdir on the agent's behalf, so it belongs
	// under the agent's caps rather than beside them.
	if err := s.startPanel(ephID, spec.Profile, ptymgr.Spec{Command: name, Args: args, Env: env, Dir: dir}); err != nil {
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
	// The scratch shell belongs to no agent, so it takes the fleet-wide caps. It is
	// the panel a user types arbitrary commands into, which makes leaving it
	// uncapped the least defensible exemption of the four.
	if err := s.startPanel(ephID, "", ptymgr.Spec{Command: cmd, Dir: dir}); err != nil {
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
func (s *Server) agentTargetSpec(targetID, label string) (spawnSpec, error) {
	s.mu.Lock()
	idx := s.indexLocked(targetID)
	if idx < 0 {
		s.mu.Unlock()
		return spawnSpec{}, fmt.Errorf("no panel with id %q", targetID)
	}
	kind := s.panels[idx].Kind
	spec := s.specs[targetID]
	s.mu.Unlock()

	if kind != panel.Agent {
		return spawnSpec{}, fmt.Errorf("%s is available on agent panels", label)
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

	repo := s.targetDir(targetID, spec.Spec)
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
	id, err := s.createPanel(proto.KindAgent, spec.Command, spec.Args, path, spec.Profile, false, false)
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
	repo := s.targetDir(targetID, spec.Spec)
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
		s.cg.Release(id)       // the ephemeral is gone; drop its cgroup with it
		s.releaseContainer(id) // and the container it was launched in, if it had one
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
			delete(s.specs, p.ID)    // purged for good; drop its retained spawn spec
			delete(s.sessions, p.ID) // …and the session ids its usage was attributed through
			s.forgetRestartLocked(p.ID)
			s.forgetCwdLocked(p.ID)
			continue
		}
		kept = append(kept, p)
	}
	s.panels = kept
	s.mu.Unlock()

	for _, id := range gone {
		s.pty.Stop(id)
		s.cg.Release(id)                 // purged for good; drop its cgroup with it
		s.releaseContainer(id)           // and the container it was launched in, if it had one
		s.stopLogging(id, logMarkClosed) // …and its transcript is finished rather than abandoned
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
	if !panel.GroupValid(name) {
		return fmt.Errorf("invalid group path %q", name)
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

// swapGroupPrefix rewrites a group path that lies under oldPrefix so it sits under
// newPrefix instead, keeping the suffix below the prefix. It is the primitive behind
// both re-parenting (rename to a nested path) and dissolving a level (promote to the
// parent, newPrefix == the parent). A newPrefix of "" strips the prefix entirely,
// returning the bare suffix — a top-level path, or "" for the prefix itself.
func swapGroupPrefix(path, oldPrefix, newPrefix string) string {
	suffix := path[len(oldPrefix):] // "" when path == oldPrefix, else "/rest"
	if newPrefix == "" {
		return strings.TrimPrefix(suffix, panel.GroupSep)
	}
	return newPrefix + suffix
}

// remapGroupKeys re-keys the per-group view maps when a subtree moves: every key
// under oldPrefix is swapped onto newPrefix (dropping any that collapse to "", i.e.
// the dissolved root's own settings). Keys are collected before any write so a moved
// key is never re-read. A generic over the two map value types (int count, string
// layout).
func remapGroupKeys[V any](m map[string]V, oldPrefix, newPrefix string) {
	type kv struct {
		k string
		v V
	}
	var moves []kv
	for k, v := range m {
		if panel.GroupIsUnder(oldPrefix, k) {
			moves = append(moves, kv{k, v})
		}
	}
	for _, mv := range moves {
		delete(m, mv.k)
	}
	for _, mv := range moves {
		if nk := swapGroupPrefix(mv.k, oldPrefix, newPrefix); nk != "" {
			m[nk] = mv.v
		}
	}
}

// moveGroupSubtreeLocked re-parents the whole subtree rooted at oldPrefix onto
// newPrefix: every panel whose Group is under oldPrefix has its path prefix swapped,
// and the per-group view settings (visible count, layout) are re-keyed to follow. It
// is the shared core of a group rename/move and a dissolve (newPrefix == the group's
// parent). Returns how many panels moved. The caller holds s.mu.
func (s *Server) moveGroupSubtreeLocked(oldPrefix, newPrefix string) int {
	moved := 0
	for i := range s.panels {
		if panel.GroupIsUnder(oldPrefix, s.panels[i].Group) {
			s.panels[i].Group = swapGroupPrefix(s.panels[i].Group, oldPrefix, newPrefix)
			moved++
		}
	}
	remapGroupKeys(s.groupShown, oldPrefix, newPrefix)
	remapGroupKeys(s.groupLayout, oldPrefix, newPrefix)
	remapGroupKeys(s.groupFavourite, oldPrefix, newPrefix)
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
	// Dissolve the whole group: promote its subtree one level (drop this path
	// segment), so nested sub-groups survive as children of the parent — "backend"
	// dissolves to lone panels + top-level sub-groups, "backend/api" folds into
	// "backend". moveGroupSubtreeLocked also re-keys the dissolved level's view
	// settings (dropping the root's own).
	s.mu.Lock()
	moved := s.moveGroupSubtreeLocked(name, panel.GroupParent(name))
	if moved > 0 {
		s.emit("group.change", map[string]any{"group": name})
	}
	s.mu.Unlock()
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
	if !panel.GroupValid(name) {
		return fmt.Errorf("invalid group path %q", name)
	}
	if panel.GroupIsUnder(old, name) {
		return fmt.Errorf("cannot nest group %q inside itself", old)
	}
	if !s.allowNameConflict && s.nameTakenLocked(name, "", old) {
		return fmt.Errorf("the name %q is already taken", name)
	}
	// A group rename is a subtree prefix move: it rewrites old → name across every
	// descendant path (so renaming "db" → "backend/db" nests the whole subtree) and
	// re-keys the view settings to follow. Renaming onto an existing path merges the
	// two, as before — group identity is the path.
	moved := s.moveGroupSubtreeLocked(old, name)
	if moved == 0 {
		return fmt.Errorf("no panels in group %q", old)
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

	// Record the intent before delivering: an exit this causes must not be read as
	// a crash the supervisor should undo.
	s.noteStopRequested(targets)

	for _, id := range targets {
		s.signalPanel(id, name, sig)
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

// setFavourite marks every listed panel a dashboard favourite (or not), the
// server-owned flag the dashboard reads to sort a card to the front. Favourites
// live with the panel here — the single source of truth — so they survive a
// frontend restart and are shared across clients. It is entirely separate from
// Pinned. Ids that match no panel are skipped; it errors only when none matched.
func (s *Server) setFavourite(ids []string, fav bool) error {
	if len(ids) == 0 {
		return fmt.Errorf("panel.favourite needs at least one panel")
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
			s.panels[i].Favourite = fav
			n++
		}
	}
	if n == 0 {
		return fmt.Errorf("no panel matched the given ids")
	}
	log.Info().Int("panels", n).Bool("favourite", fav).Msg("panels favourited")
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

// setGroupFavourite records whether a group is a dashboard favourite — its card
// sorts to the front of the dashboard. An empty group name is rejected. Like
// setGroupShown the group need not currently exist, and lifecycle cleanup keeps
// the map tidy on dissolve/rename. It is entirely separate from a panel's Pinned
// flag and from the per-group view settings.
func (s *Server) setGroupFavourite(group string, fav bool) error {
	group = strings.TrimSpace(group)
	if group == "" {
		return fmt.Errorf("group.favourite needs a group")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if fav {
		s.groupFavourite[group] = true
	} else {
		delete(s.groupFavourite, group)
	}
	s.emit("group.change", map[string]any{"group": group, "favourite": fav})
	log.Info().Str("group", group).Bool("favourite", fav).Msg("group favourite set")
	return nil
}

// wirePanel encodes one panel for the wire and joins in everything the domain
// model deliberately does not carry: the live group-leader pid and the state
// clock. Both fleet messages a client sees — the "panels" snapshot and the
// "telemetry" refresh — are built through here, so the two can never disagree:
// before this helper existed the snapshot joined the pid and the telemetry frame
// did not, which is exactly the drift two hand-written builders of the same
// message accumulate.
//
// Since travels as an instant rather than as the rendered Activity string
// because a queue has to SORT on it: a rendered "3m" cannot be ordered, and a
// client's own clock cannot be trusted across the --remote ssh hop. It keeps
// nanoseconds so panels that settled in the same tick still order stably. For a
// dead panel it falls back to when the process ended, because the Monitor forgets
// a panel on exit and a queue listing failures still has to order them.
// Caller holds s.mu.
func (s *Server) wirePanel(p panel.Panel, pids map[string]int) proto.Panel {
	out := p.ToProto()
	out.Pid = pids[p.ID]
	// The profile joins the snapshot here rather than living on the panel record,
	// for the reason the pid does: it belongs to the spawn spec, and a frontend
	// that wants to group a fleet by what KIND of agent is running has no other
	// way to ask. Caller holds s.mu.
	out.Profile = s.specs[p.ID].Profile
	since := s.mon.enteredAt(p.ID)
	if since.IsZero() {
		since = s.exitedAt[p.ID]
	}
	if !since.IsZero() {
		out.Since = since.Format(time.RFC3339Nano)
	}
	out.Sig = s.mon.sig(p.ID)
	out.Acked = s.ackedLocked(p.ID)
	if sink := s.logs[p.ID]; sink != nil {
		out.Logging, out.LogPath = true, sink.Path()
	}
	return out
}

// panelsMsg builds the full "panels" snapshot broadcast to clients: every panel
// in wire form plus each group's non-default view settings, sorted by name for a
// deterministic frame.
func (s *Server) panelsMsg() proto.ServerMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]proto.Panel, len(s.panels))
	pids := s.pty.Pids() // one lock acquisition, then a map lookup per panel — the ptymgr lock is contended by the output pump
	for i, p := range s.panels {
		out[i] = s.wirePanel(p, pids)
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
	for g := range s.groupFavourite { // the map only ever stores true — a false deletes the key
		view(g).Favourite = true
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
	s.pushRemote() // the connection list lost a row; refresh any open overlay
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
