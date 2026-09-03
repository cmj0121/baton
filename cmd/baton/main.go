// Command baton is an agent-friendly terminal multiplexer.
//
// Running `baton` starts the background server for this user (if one is not
// already running) and attaches a cockpit to it. There is one backend per user on
// a machine, so a second `baton` — from any terminal — attaches to the same fleet.
//
//	-l, --log FILE  write logs to FILE (default: $HOME/.baton/baton.log)
//	-v, -vv         increase log verbosity
//	-h, --help      show help and exit
//	-V, --version   show the version and exit
//
// Two subcommands drive the fleet over the control socket without the cockpit:
// `baton ctl` is a thin control client for a human or a script (see ctl.go), and
// `baton mcp` is a Model Context Protocol server an agent's .mcp.json launches so
// it can spawn, group, signal, and send prompts to panels (see mcp.go).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/attn"
	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/cwd"
	"github.com/cmj0121/baton/internal/isolate"
	"github.com/cmj0121/baton/internal/limits"
	"github.com/cmj0121/baton/internal/panellog"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/plugin"
	"github.com/cmj0121/baton/internal/restart"
	"github.com/cmj0121/baton/internal/score"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/tui"
	"github.com/cmj0121/baton/internal/usage"
)

const version = "1.6.0"

// daemonEnv marks the re-executed child that should run the server loop instead
// of attaching a cockpit.
const daemonEnv = "BATON_DAEMON"

// CLI is the entire baton command-line surface: a few flags, no commands.
type CLI struct {
	Log     string           `short:"l" name:"log" placeholder:"FILE" help:"Write logs to FILE (default: $HOME/.baton/baton.log)."`
	Plugin  string           `short:"p" name:"plugin" placeholder:"FILE" help:"Load the Lua plugin from FILE (default: $HOME/.baton/plug-in.lua)."`
	Verbose int              `short:"v" type:"counter" help:"Increase log verbosity (-v debug, -vv trace)."`
	Force   bool             `short:"f" name:"force" help:"Force-stop any running server for this session and start a fresh one before attaching."`
	Remote  bool             `name:"remote" help:"Attach to a fleet on another machine over SSH: opens a form for the address and the passkey."`
	Stdio   bool             `name:"stdio" hidden:"" help:"Bridge stdin/stdout to this machine's fleet. Run by --remote over ssh, not by a human."`
	Version kong.VersionFlag `short:"V" help:"Print the version and quit."`
}

func main() {
	// `baton ctl …` and `baton mcp` are the control surfaces, handled before the
	// cockpit's flag parsing so the default `baton` (attach) path stays a flag-only
	// CLI. ctl is the human/script CLI; mcp is the agent-facing MCP server.
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		os.Exit(ctlMain(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(mcpMain())
	}
	// `baton usage-sink` is the status line baton hands to the Claude Code panels
	// it launches, so it runs once per render of every panel in the fleet. It is
	// dispatched here, ahead of everything the cockpit path sets up — a logger, a
	// config read, a socket — none of which a command that reads one pipe and
	// writes one line has any use for.
	if len(os.Args) > 1 && os.Args[1] == "usage-sink" {
		os.Exit(usageSinkMain(os.Args[2:]))
	}

	var cli CLI
	kctx := kong.Parse(&cli,
		kong.Name("baton"),
		kong.Description("an agent-friendly terminal multiplexer"),
		kong.UsageOnError(),
		kong.Vars{"version": version},
	)

	logPath := resolveLogPath(cli.Log)
	sink, err := setupLogger(cli.Verbose, logPath)
	kctx.FatalIfErrorf(err)

	// The daemon child re-executes this same binary with daemonEnv set.
	if claimDaemonRole() {
		// Its std streams are the log file its parent opened for it, and that
		// descriptor is never reopened; re-point them through the sink so a panic
		// follows the rotations instead of landing in an unlinked inode. Done here
		// and not in runServer because runServer is called in-process by tests,
		// whose stdout is the test report.
		kctx.FatalIfErrorf(sink.mirrorStdio())
		kctx.FatalIfErrorf(runServer())
		return
	}
	// The far side of a remote attach. It is checked before anything that could
	// write to stdout, because from here on stdout IS the wire.
	if cli.Stdio {
		kctx.FatalIfErrorf(runStdio(cli.Verbose, logPath, cli.Plugin))
		return
	}
	if cli.Remote {
		cfg, err := config.Load()
		if err != nil {
			log.Warn().Err(err).Msg("config load failed; dialling with the defaults")
		}
		kctx.FatalIfErrorf(attachRemote(cfg))
		return
	}
	kctx.FatalIfErrorf(attach(cli.Verbose, logPath, cli.Plugin, cli.Force))
}

// resolveLogPath picks the log file: the explicit --log flag when set, otherwise
// the shared default. An empty flag means "use the default".
func resolveLogPath(flag string) string {
	if flag != "" {
		return flag
	}
	return paths.LogFile()
}

// claimDaemonRole reports whether this process is the re-executed daemon child
// (marked by daemonEnv=1) that runs the server loop rather than attaching a
// cockpit — and takes the marker as it reads it.
//
// Clearing it is the point, not tidiness. The daemon's environment is the one
// every panel it spawns inherits, and everything run inside a panel inherits it
// in turn. A marker left in place therefore reaches the user's own shell, where
// the next `baton` reads it, decides it is the daemon child, finds the socket
// already bound and exits with an error — so a cockpit could not be opened from
// inside baton at all, which is the most natural place to want a second one.
func claimDaemonRole() bool {
	if os.Getenv(daemonEnv) != "1" {
		return false
	}
	_ = os.Unsetenv(daemonEnv)
	return true
}

// attach starts the user's server if needed, then runs the cockpit. With force,
// any running server is stopped first so the fleet comes up fresh. An
// explicit plugin path is handed to the daemon child through BATON_PLUGIN.
func attach(verbose int, logPath, pluginPath string, force bool) error {
	if force {
		if err := stopDaemon(paths.Socket()); err != nil {
			return err
		}
	}
	if err := startDaemon(verbose, logPath, pluginPath, force); err != nil {
		return err
	}
	return runClient(verbose, logPath, pluginPath)
}

// stopDaemon force-stops the running daemon, if any, and waits for it to release
// the socket. A daemon that never got as far as a socket is stopped by
// stopUnboundDaemon instead; when nothing is running at all this is a no-op, bar
// tidying what a crash left behind.
//
// WHY THERE ARE TWO, since a predicate on the session claim alone would answer
// for both cases and would be the better oracle in each: sessionClaimed is
// hardcoded false on non-unix (see session_other.go, where claimSession grants
// every claim without recording one). A single claim-based stop would leave
// those platforms unable to stop a daemon that HAD bound its socket, which is
// every ordinary stop. The socket is the thing every build can ask about; the
// claim is the sharper question only some can. That portability stub is the
// whole reason, and the two paths must not be merged on the strength of the unix
// build alone.
func stopDaemon(sock string) error {
	if !alive(sock) {
		return stopUnboundDaemon(sock)
	}

	pid, err := readPidFile(paths.PidFile(sock))
	if err != nil {
		return err
	}
	if err := signalAndWait(pid, func() bool { return !alive(sock) }); err != nil {
		return err
	}
	log.Info().Int("pid", pid).Msg("daemon stopped")
	return nil
}

// signalAndWait asks pid to stop and waits until gone says it has, within the
// same budget startDaemon gives a daemon to come up.
//
// gone is the caller's, because the two stops watch different things: a daemon
// that was serving is gone when its socket stops answering, and one that never
// bound has no socket to watch, only the session claim the kernel drops as it
// dies.
func signalAndWait(pid int, gone func() bool) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal daemon %d: %w", pid, err)
	}
	if !waitFor(gone, daemonPollTries, daemonPollGap) {
		return fmt.Errorf("daemon %d did not stop in time", pid)
	}
	return nil
}

// stopUnboundDaemon is stopDaemon for the case where dialling the socket found
// nothing: either nothing is running, or a daemon is alive and has not bound yet
// — stuck in the filesystem reads that now happen above net.Listen. That daemon
// holds the session claim, so every `baton` after it loses claimSession and
// exits, and until this existed the only way out was finding its pid by hand.
//
// The session claim, not the PID file, is what decides. A PID file is a number
// on disk that outlives the process it named — a SIGKILLed daemon leaves one —
// and the operating system reuses pids, so signalling one on the strength of the
// file alone can deliver a SIGTERM to whatever unrelated program now holds that
// number. That is a worse failure than the wedge this fixes. sessionClaimed
// answers a different question: is a daemon for THIS socket alive right now. The
// kernel drops the flock when its holder dies, so a false there means the pid
// file is garbage no matter how live the process it names is, and the file is
// tidied rather than signalled.
//
// WHAT IS STILL OPEN, because the claim is one file and the pid is another. A
// daemon takes the claim and then tidies a predecessor's PID file (runServer,
// through clearStaleSocket); in between, this can see the claim held and a pid
// that is not the holder's. Only the kernel knows who holds an flock and it will
// not say, so no ordering of two files closes that — what the ordering does buy
// is that the gap is a stat and an unlink rather than the daemon's whole life,
// and that the state a --force is most likely to land in is "claim held, no PID
// file", which is refused below rather than guessed at.
//
// The probe has a cost of its own: it decides by taking the flock, so it
// conflicts with a claimSession running at that instant, and the daemon making
// that claim will read the conflict as another daemon owning the session and
// leave quietly. Its launcher then spends daemonPollTries waiting for a socket
// nothing is bringing up and reports that the server did not come up. It needs
// two batons inside the same few microseconds, and the next one run succeeds.
//
// POLLING DOES NOT MULTIPLY THAT, which is worth saying because the wait below
// asks the same question up to daemonPollTries times over five seconds and the
// arithmetic looks like a hundred of these windows. It is not, and the reason is
// which branch of the probe touches the lock. A claim somebody holds is answered
// by the FAILURE to take it, so every question asked while the target daemon is
// still alive costs one failed flock and opens nothing. The probe takes the lock
// only when the session is already free — and that answer is what ENDS the wait,
// so it happens once. Two instants per stop, not a hundred: the opening check,
// and the question that finds the daemon gone.
//
// So no backoff is owed here. What a backoff would buy is fewer conflicting
// instants, and the count is already two; what it would cost is a slower stop on
// the path an operator is running because their fleet is wedged.
func stopUnboundDaemon(sock string) error {
	probe := openSessionProbe(paths.LockFile(sock))
	defer probe.close()
	if !probe.claimed() {
		return clearStaleSocket(sock)
	}

	pid, err := readPidFile(paths.PidFile(sock))
	if err != nil {
		return fmt.Errorf("a daemon holds the session for %s but named no pid to stop it with: %w", sock, err)
	}
	// The claim is what is watched, not the socket: there is no socket, which is
	// the whole shape of this case. The daemon is stuck in a call it will not
	// return from, so nothing of its own runs on the way out; what ends the wait is
	// the kernel dropping the flock as the process dies.
	if err := signalAndWait(pid, func() bool { return !probe.claimed() }); err != nil {
		return err
	}
	log.Info().Int("pid", pid).Msg("stopped a daemon that had not bound its socket")
	return clearStaleSocket(sock)
}

// setupLogger points the global zerolog logger at the log file, creating it (and
// its directory) as needed, and returns the sink underneath it.
//
// The sink is returned rather than kept here because the daemon has one more
// thing to do with it (mirrorStdio) and no other caller does. It sits UNDER
// zerolog.ConsoleWriter — the console writer formats a line and hands it on as
// one Write — which is what lets a rotation reach the file every process is
// really writing to without the logger above knowing anything happened.
func setupLogger(verbosity int, logPath string) (*logRotator, error) {
	level := zerolog.InfoLevel
	switch {
	case verbosity >= 2:
		level = zerolog.TraceLevel
	case verbosity == 1:
		level = zerolog.DebugLevel
	}
	zerolog.SetGlobalLevel(level)

	if err := paths.EnsureDir(logPath); err != nil {
		return nil, fmt.Errorf("prepare log dir: %w", err)
	}
	sink, err := openLogRotator(logPath, logRotateAtBytes)
	if err != nil {
		return nil, err
	}
	writer := zerolog.ConsoleWriter{Out: sink, NoColor: true, TimeFormat: "2006-01-02 15:04:05"}
	log.Logger = zerolog.New(writer).With().Timestamp().Logger()
	return sink, nil
}

// startDaemon ensures this user's server is running, launching it in the
// background if not. Exactly one server runs per user (one socket).
func startDaemon(verbose int, logPath, pluginPath string, forced bool) error {
	sock := paths.Socket()
	if alive(sock) {
		log.Debug().Str("socket", sock).Msg("daemon already running")
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate baton binary: %w", err)
	}
	if err := paths.EnsureDir(sock); err != nil {
		return fmt.Errorf("prepare runtime dir: %w", err)
	}

	// The child logs through zerolog; redirect its std streams to the same file
	// so panics and other non-logger output are captured too.
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", logPath, err)
	}
	defer func() { _ = logf.Close() }()

	// Re-exec ourselves as the daemon, pinned to the control socket and log file
	// and carrying the same verbosity.
	proc := exec.Command(exe, daemonArgs(logPath, verbose)...)
	proc.Env = daemonEnviron(os.Environ(), sock, pluginPath)
	proc.Stdout = logf
	proc.Stderr = logf
	proc.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach into its own session
	if err := proc.Start(); err != nil {
		return fmt.Errorf("start baton server: %w", err)
	}

	if !waitFor(func() bool { return alive(sock) }, daemonPollTries, daemonPollGap) {
		return errors.New(didNotComeUpReason(logPath, forced))
	}
	log.Debug().Str("socket", sock).Msg("daemon started")
	return nil
}

// didNotComeUpReason is what an operator is told when the daemon they just
// started never bound its socket. It is its own function for the reason
// timedOutReason is: the wording is the whole of it, so a test pins it.
//
// IT CARRIES THE RECOVERY, because nothing else does — a grep over docs/ and the
// README finds neither this sentence nor the flag that clears it. And the state
// it names does not clear by itself: a daemon wedged in a filesystem read above
// the bind is still alive and still holding the session claim, so every later
// `baton` loses claimSession to it and exits quietly. Without the flag an
// operator's only route out is finding a pid by hand.
//
// It points at the LAST LINE rather than at the log, because the log now has a
// last line worth reading: the boot says which file it is about to read before
// it reads it (see loadServerBoot), so the answer is the thing the operator's eye
// lands on first.
//
// FORCED changes the advice, because the advice is otherwise circular. Reached
// from a plain `baton`, "try --force" is new information. Reached from --force
// ITSELF it is the operator being told to do the thing they just did — and the
// flag had worked: it stopped the old daemon and started a fresh one, which then
// walked into the same read. What is left at that point is not another --force,
// it is the path.
func didNotComeUpReason(logPath string, forced bool) string {
	advice := "If it is still wedged there, `baton --force` stops it and starts again"
	if forced {
		advice = "--force stopped the old server and started a fresh one, and it stopped in the " +
			"same place, so repeating it will not help: restore or unmount that path first"
	}
	return fmt.Sprintf("baton server did not come up; see %s — its last line names what the daemon "+
		"was reading. %s", logPath, advice)
}

// runServer is the long-lived server loop (the daemon child).
func runServer() error {
	sock := paths.Socket()
	if err := paths.EnsureDir(sock); err != nil {
		return fmt.Errorf("prepare socket dir: %w", err)
	}

	// One backend per user, decided here rather than by who reaches bind first
	// (see claimSession). Losing is ordinary — every cockpit starting at once
	// tries to bring a backend up — so it is not reported as a failure: the
	// winner's socket is what they all attach to.
	release, held, err := claimSession(paths.LockFile(sock))
	if err != nil {
		return err
	}
	if !held {
		log.Debug().Str("socket", sock).Msg("another daemon owns this session; leaving it to run")
		return nil
	}
	defer release()

	if err := clearStaleSocket(sock); err != nil {
		return err
	}

	// The daemon's own reading of the operator's filesystem happens here, on this
	// side of the bind. It used to happen on the other one: config.Load, the
	// legacy sweep and score.Open all ran after net.Listen and before Serve, so a
	// score.dir or a $HOME that does not answer left a socket bound and never
	// served — a client connects, is accepted, and waits forever, with nothing in
	// the log because the line that would explain it comes after the call that is
	// stuck. Above the bind the same hang leaves no socket at all: the client gets
	// a connection error and `baton` reports that the server did not come up (#60).
	//
	// It is NOT yet every read below Serve, and saying otherwise here would be the
	// defect this fix is about. applyConfig's first pass still runs after the bind
	// and re-reads this same config file, plus $HOME/.baton/TUI.yaml and the Lua
	// plugin — which the daemon EXECUTES, bounded by nothing. All three need the
	// server that does not exist until the listener does, so hoisting them is its
	// own change. What is closed here is the steady state: a path that is already
	// dead when the daemon starts, which is every case #60 was filed on.
	boot := loadServerBoot(sock)
	// The boot's own acquisitions come back through the one function that made
	// them, on every path out of this one — including the TWO WAYS THE BIND CAN
	// FAIL below, which are why the defer is here rather than only in runServerOn.
	// A store exists before either of them and it holds the score directory's
	// single-writer claim; returning without giving that back would leave it held
	// by a process nothing can reach it through, which is the failure that makes
	// the NEXT daemon refuse the same directory. See serverBoot.release, which is
	// idempotent, so runServerOn's cleanup calling it first costs nothing here.
	defer boot.release()

	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sock, err)
	}
	// Clamp the socket to owner-only so no other user on the host can connect and
	// drive the fleet — the socket carries full spawn-any-process power.
	if err := paths.SecureSocket(sock); err != nil {
		_ = ln.Close()
		return fmt.Errorf("secure socket %s: %w", sock, err)
	}
	return runServerOn(ln, sock, boot)
}

// buildServerOptions projects the hot-reloadable settings and the persisted
// state file onto the server's construction options. The replay-buffer option is
// added only when the config sets a positive size, leaving the server's built-in
// default in place otherwise.
func buildServerOptions(rc reloadable, stateF string) []server.Option {
	opts := []server.Option{
		server.WithVersion(version),
		server.WithAllowNameConflict(rc.settings.AllowNameConflict),
		server.WithDefaultDir(rc.settings.DefaultDir),
		server.WithDiffCommand(rc.settings.DiffCommand),
		server.WithEditor(rc.settings.Editor),
		server.WithWorktreeDir(rc.settings.WorktreeDir),
		server.WithLimits(rc.settings.Limits, rc.settings.AgentLimits),
		server.WithLogging(rc.settings.LogDir, rc.settings.AgentLogDir, rc.settings.AgentLog, rc.settings.LogMaxBytes),
		server.WithStateFile(stateF),
		server.WithQueue(rc.settings.QueueMax, rc.settings.QueueConcurrency),
	}
	if rc.settings.ReplayBytes > 0 {
		opts = append(opts, server.WithReplayBytes(rc.settings.ReplayBytes))
	}
	return opts
}

// usageOption builds the account-usage footer option from the config: it picks the
// data source (see usage.NewProvider) and the poll cadence, defaulting per source
// and clamping to a floor so a hand-edited interval can never hammer the poller.
// Usage source/interval are construction-time (a restart picks up a change); the
// show/hide toggle is client-side and live.
func usageOption(cfg config.Config) server.Option {
	p := usage.NewProvider(cfg.Usage.Source, cfg.Usage.WindowDuration())
	interval := time.Duration(cfg.Usage.Interval) * time.Second
	if interval <= 0 {
		interval = usage.DefaultInterval(p)
	}
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	warn, alarm := cfg.Usage.Thresholds()
	return server.WithUsage(p, interval, server.UsageDisplay{WarnAt: warn, AlarmAt: alarm})
}

// limitsOption wires the account's rate-limit bars from the config.
//
// The status-line source needs baton's own path and nothing else: the reading is
// harvested by the Claude Code panels themselves, which are launched with `baton
// usage-sink` as their status line and drop what they see in a file the daemon
// reads. When os.Executable cannot say where this binary is, the source is left
// off rather than pointed at a guess — a status line aimed at the wrong path
// would fail once per render inside the user's panel.
func limitsOption(cfg config.Config) server.Option {
	switch cfg.Usage.LimitsSource() {
	case usage.LimitsStatusline:
		self, err := os.Executable()
		if err != nil {
			log.Warn().Err(err).Msg("cannot locate the baton binary; usage limits are off")
			return func(*server.Server) {}
		}
		return server.WithUsageLimits(usage.NewStatuslineLimits(paths.UsageLimitsFile()), self)
	case usage.LimitsOAuth:
		// The oauth source fetches for itself, so it needs no status line injected —
		// and gets an empty self, which is how a source says exactly that. It is also
		// the only source that can see the per-model weekly ceilings and the
		// extra-usage credit balance, which is the whole reason to reach for it.
		return server.WithUsageLimits(usage.NewOAuthLimits(), "")
	default:
		return func(*server.Server) {}
	}
}

// sweepLegacyConductorWorkspaces drops the throwaway conductor directories older
// versions of baton left behind. Each open used to make a fresh MkdirTemp
// workspace and delete it on close, so every crash or hard kill leaked one, and
// they pile up for as long as baton has been installed. There is one conductor
// workspace per socket now, and it is not one of these.
//
// It runs once per daemon start, from here rather than from the server, so no test
// that builds a Server can reach a real user's runtime directory. Each removal is
// logged: this is the user's disk, and a directory disappearing silently is worse
// than the clutter.
//
// BELOW THE BIND, deliberately, and it is the one boot-time filesystem walk that
// stayed there when #60 hoisted the rest. What it does is two globs, a stat per
// match and a recursive RemoveAll per leak — unbounded in the number of leaks and
// in the size of each — and nothing between net.Listen and Serve reads a legacy
// workspace, so putting it in front of the socket would have bought a slower
// start for no answer anybody waits on.
func sweepLegacyConductorWorkspaces(sock string) {
	current, err := paths.ConductorWorkspace(sock)
	if err != nil {
		log.Warn().Err(err).Msg("could not resolve the conductor workspace; skipping the legacy sweep")
		return
	}
	for _, ws := range paths.LegacyConductorWorkspaces(current) {
		if err := os.RemoveAll(ws); err != nil {
			log.Warn().Err(err).Str("workspace", ws).Msg("could not remove a legacy conductor workspace")
			continue
		}
		log.Info().Str("workspace", ws).Msg("removed a legacy conductor workspace")
	}
}

// scorePolicy is the store's tuning as the config file spells it — the
// recurrence threshold, the working-set budget, and the ranking weights — and
// the GATE on a file that would not parse. ok false means this file chooses no
// policy at all.
//
// It is the one translation between the two shapes AND the one gate, because
// both halves have to be the same at boot and on reload or one file produces two
// live policies. They did not used to be: boot handed openScore the config
// struct whatever config.Load's error said, while the reload path gated
// SetPolicy on that error being nil. One mistyped weight was then enough for a
// restart to apply `working-set: 9` while a SIGHUP applied nothing — two
// policies from one file, chosen by whether the operator reloaded or restarted,
// with nothing said about either. A mistyped number fails the whole decode (see
// config.badNumbers, which is how the key gets named), so this is not a corner.
//
// A failed load is not a file saying "use the defaults". At boot there is no
// running policy to keep, so the store is built on its own defaults; on a reload
// the running one stands. Both are the same rule — a broken file never chooses —
// and the difference is only in what there is to fall back to.
//
// Nothing is clamped here. The store clamps every field on the way in (see
// score.Policy.clamp), so the defaults and the floors are stated once, in the
// package that acts on them; warnScorePolicy is what says when a clamp moved a
// number the operator wrote.
func scorePolicy(cfg config.ScoreConfig, err error) (p score.Policy, ok bool) {
	if err != nil {
		return score.Policy{}, false
	}
	return score.Policy{
		PromoteAt:     cfg.PromoteAt,
		UserSignalsAt: cfg.UserSignalsAt,
		WorkingSet:    cfg.WorkingSet,
		Rank: score.Rank{
			Recency: cfg.Rank.Recency,
			Cwd:     cfg.Rank.Cwd,
			Profile: cfg.Rank.Profile,
			Group:   cfg.Rank.Group,
		},
	}, true
}

// implausibleUserSignalsAt is where a user-signal threshold stops looking like a
// choice and starts looking like a typo.
//
// It is NOT a clamp and NOT an arithmetic ceiling, which is what separates it
// from score.MaxReachableWorkingSet: there the rune backstop makes a wide budget
// provably unspendable, whereas any user-signal threshold is reachable by an
// operator willing to say a thing that many times. #37 leaves the number to
// them, so the store honours whatever it is given and this only says so out
// loud.
//
// Twenty, because the knob counts the USER's own repetitions of ONE entry, by
// hand, over the life of a fleet. Past twenty the top tier is unreachable in
// practice rather than in theory, and `user-signals-at: 999999` — a fat finger,
// or a number meant for another key — would otherwise switch the whole rung off
// in silence, which is the failure invariant I8 exists to prevent.
const implausibleUserSignalsAt = 20

// warnScorePolicy says what the file asked for that the daemon is not doing. It
// runs at boot and on every reload, like the stale-key warning it sits beside,
// because a config mistake persists until it is fixed and the reload is exactly
// when it was made.
//
// Three things, each invisible otherwise (invariant I8):
//
//   - a key whose value is not a number. That failed the whole decode, so NO
//     part of this file's score policy is in force — including the keys that
//     were fine — and the generic load warning names the plugin rather than the
//     section. Named per key, so the operator has something to fix.
//   - a value the store CLAMPED. `rank.cwd: 0.5` runs as 1.0, which switches the
//     dimension off rather than penalising a miss, and `1e300` runs as 1e6.
//     Reported whether or not the reload changed anything, since a clamp that
//     lands on the value already in force changes nothing and is the case an
//     operator is most likely to be confused by. An UNSET key is not a clamp: it
//     asked for nothing and got the default.
//   - a cwd weight that cannot ever match, because panel.track-cwd is off. The
//     weight is then dead config: no panel reports a directory, so no entry
//     records one and no dispatch has one to compare.
func warnScorePolicy(cfg config.Config, want score.Policy, st *score.Store) {
	for _, key := range cfg.Score.BadNumbers {
		log.Warn().Str("key", key).
			Msg("config value is not a number; no score policy from this file is in force")
	}
	// The rest compares what was asked against what is IN FORCE, so it needs a
	// store to ask. With none — switched off, or a directory another daemon holds
	// — there is no in-force policy, and comparing against the zero one would
	// report every key the operator set as clamped to nothing. The store's
	// absence is already reported through score.status and every refusal.
	if st == nil {
		return
	}
	got := st.Policy()
	clamped := func(key string, asked, force float64) {
		if asked != 0 && asked != force {
			log.Warn().Str("key", key).Float64("configured", asked).Float64("in_force", force).
				Msg("score weight is out of range and was clamped")
		}
	}
	if want.PromoteAt != 0 && want.PromoteAt != got.PromoteAt {
		log.Warn().Int("configured", want.PromoteAt).Int("in_force", got.PromoteAt).
			Msg("score.promote-at is out of range and was clamped")
	}
	if want.UserSignalsAt != 0 && want.UserSignalsAt != got.UserSignalsAt {
		log.Warn().Int("configured", want.UserSignalsAt).Int("in_force", got.UserSignalsAt).
			Msg("score.user-signals-at is out of range and was clamped")
	}
	if got.UserSignalsAt > implausibleUserSignalsAt {
		log.Warn().Int("score.user-signals-at", got.UserSignalsAt).
			Msg("score.user-signals-at is high enough that the top tier is effectively unreachable; " +
				"it counts the user's own repetitions of one entry, not an agent's")
	}
	if want.WorkingSet != 0 && want.WorkingSet != got.WorkingSet {
		log.Warn().Int("configured", want.WorkingSet).Int("in_force", got.WorkingSet).
			Msg("score.working-set is out of range and was clamped")
	}
	clamped("score.rank.recency", want.Rank.Recency, got.Rank.Recency)
	clamped("score.rank.cwd", want.Rank.Cwd, got.Rank.Cwd)
	clamped("score.rank.profile", want.Rank.Profile, got.Rank.Profile)
	clamped("score.rank.group", want.Rank.Group, got.Rank.Group)

	if track, terr := cfg.Panel.CwdTracking(); terr == nil && track == cwd.Off && got.Rank.Cwd > 1 {
		log.Warn().Float64("score.rank.cwd", got.Rank.Cwd).
			Msg("score.rank.cwd cannot match while panel.track-cwd is off; no panel reports a directory to record or compare")
	}
	// A budget the block's rune backstop can never spend is dead config in the
	// same sense that cwd weight is: the number was written down, the daemon
	// accepted it, and no dispatch will ever reach it. Said here rather than left
	// as an unexplained gap between working_set and rendered on score.status —
	// which is where the operator would otherwise meet it, one brief at a time.
	// Not clamped: #37 leaves the count to the operator, and a budget that never
	// binds still behaves. See score.MaxReachableWorkingSet.
	if got.WorkingSet > score.MaxReachableWorkingSet {
		log.Warn().Int("score.working-set", got.WorkingSet).
			Int("reachable", score.MaxReachableWorkingSet).
			Msg("score.working-set is larger than the injected block can ever carry; the rune cap will bind first")
	}
}

// warnScoreKeysAReloadCannotApply says which score keys the operator has just
// edited that a reload will not act on. booted is the config the daemon opened
// its store from; now is the file the reload has read.
//
// The reload's own line says `config reloaded on SIGHUP` and nothing else, and
// every other score key really does take effect — promote-at, working-set,
// user-signals-at and all four rank weights reload live and announce themselves
// with `score policy changed`. So an operator who moves score.dir, sends a HUP,
// sees a success line and no complaint has every reason to believe their edit
// took. It did not, and there is no other symptom: the fleet goes on using the
// old directory, and the new one simply stays empty.
//
// WHICH keys those are is config.ScoreConfig.UnappliedOnReload's to say, beside
// the doc that claims everything else reloads. This is only the saying of it,
// which is where a log line belongs.
func warnScoreKeysAReloadCannotApply(booted, now config.ScoreConfig) {
	for _, k := range now.UnappliedOnReload(booted) {
		log.Warn().Str("key", k.Key).Str("in_force", k.InForce).Str("configured", k.Configured).
			Msg(k.Key + " changed but a reload cannot apply it; the store opens once at boot, " +
				"so restart the daemon for it to take effect")
	}
}

// scoreOpenTimeout bounds how long the daemon waits for the fleet memory before
// it gives up and serves the fleet without one.
//
// It is NOT what keeps the daemon reachable, and it has not been since the open
// moved above net.Listen — runServer says what that ordering buys (#60).
//
// What the bound still buys is the difference between "the daemon starts
// without its memory" and "the daemon never starts". score.dir is a path the
// operator chooses and $HOME is where it defaults, so a network home directory
// whose server has gone away puts a hard-mount stat inside score.Open — and a
// hard mount does not fail, it waits. Unbounded, one directory that stopped
// answering is the whole fleet, indefinitely. Bounded, the fleet runs and the
// three score surfaces say why it has no memory (see timedOutReason).
//
// Ten seconds, and the number is chosen from the other side of the trade rather
// than from this one. A store opening slowly is NORMAL — the boot replays the
// whole event log, measured at 461-512 ms for a 51.9 MB one — and giving up on a
// store that was merely slow costs the fleet its memory until someone restarts
// the daemon. The log IS bounded while the daemon runs now (#56), so it no
// longer grows without limit between restarts — but what that bounds is GROWTH,
// not size: a store whose compacted form is already large stays large, and its
// replay stays as slow as it was. A big log is still not a pathology this may
// fire on. So the bound has to sit far above a healthy boot on a large store,
// and still short enough that a person waiting on a dead mount gets an answer
// rather than a hang.
//
// A boot that spends the WHOLE bound now outlasts startDaemon's own patience —
// daemonPollTries × daemonPollGap, five seconds — because the socket is not
// bound until this returns. `baton` says the server did not come up, and the
// daemon comes up behind it, memoryless; the next invocation attaches to a
// running fleet. That is the ordering fix working, not a defect: a wrong answer
// in five seconds beats an accepted socket that never replies. The two numbers
// are independent of each other, so the arithmetic in this paragraph is a test
// rather than a sentence: TestABootThatSpendsTheWholeBoundOutlastsTheLauncher.
//
// It is a WAIT, not a cancellation: nothing in userspace can interrupt a stat
// inside the kernel. What it buys is that the daemon proceeds. The goroutine
// holding the open stays where it is until the filesystem answers it, and closes
// whatever it is handed, so a store that arrives too late does not sit on the
// directory's single-writer claim where nothing can reach it.
const scoreOpenTimeout = 10 * time.Second

// errScoreOpenTimeout is what waitForStore returns when the deadline passed
// before the open answered. It is a sentinel rather than a second result,
// because "which kind of failure is this" is a question this codebase already
// answers one way: score.ErrSubmissionText tells the store's refusal of a text
// apart from a durable write that did not land, and the server reads it with
// errors.Is. An extra boolean beside the error is a second failure channel for
// one more case, and it puts the error somewhere other than last.
//
// The MESSAGE is deliberately bare. What an operator reads is timedOutReason,
// which names the directory and the bound; this is the wire between two
// functions in one file.
var errScoreOpenTimeout = errors.New("score: the store did not open within the deadline")

// waitForStore runs open in a goroutine of its own and gives up on it after
// within, returning errScoreOpenTimeout when the deadline passes first.
//
// The channel is UNBUFFERED and the abandonment is its own channel, because the
// two have to be decided together. Buffer the send and BOTH select arms are
// ready once the caller has gone, and a select over ready arms picks at random:
// the store would be leaked with the directory's claim still on it about half
// the time, which is the failure that makes the next restart refuse to open the
// same directory, arriving as a flake rather than as a bug. Unbuffered, the send
// cannot complete without a receiver, so once gaveUp is closed the goroutine has
// exactly one case it can take.
func waitForStore(within time.Duration, open func() (*score.Store, error)) (*score.Store, error) {
	type opened struct {
		store *score.Store
		err   error
	}
	done := make(chan opened)
	gaveUp := make(chan struct{})
	go func() {
		st, err := open()
		select {
		case done <- opened{st, err}:
		case <-gaveUp:
			// Nobody is waiting any more. Close is nil-safe, and this is the only
			// place the abandoned store can be reached from.
			st.Close()
		}
	}()

	timer := time.NewTimer(within)
	defer timer.Stop()
	select {
	case got := <-done:
		return got.store, got.err
	case <-timer.C:
		close(gaveUp)
		return nil, errScoreOpenTimeout
	}
}

// timedOutReason is what a store that never opened tells whoever asks — the
// string score.status reports and a refused score.submit is answered with.
//
// It NAMES THE DIRECTORY, which is the whole reason it is a sentence rather than
// a constant: BATON_SOCK is the documented way to run a second fleet and both
// fleets default to $HOME/.baton, so an operator reading "the memory is not
// running" has two daemons and possibly two mounts to guess between. It is its
// own function so the wording is pinned by a test; the branch that uses it needs
// a filesystem that hangs, which no test can conjure.
func timedOutReason(within time.Duration, dir string) string {
	return fmt.Sprintf("score did not open within %s; %s may be on a filesystem that is not answering", within, dir)
}

// openScore opens the fleet memory (#39) under the policy p — scorePolicy's, so
// a file that would not parse hands it the zero policy and the store boots on
// its own defaults rather than on half a broken file — and reports why no store
// is running when none is.
//
// The policy goes in at construction, because Open's
// own recovery pass promotes: it folds and rewords whatever the operator edited
// into score.md while the daemon was down, and those `raised` events are
// durable and never demoted. applyConfig re-tunes the same policy on every
// reload; this is the one place it is CHOSEN. A failed Open logs and boots
// WITHOUT a store — corrupt score files never block the fleet (#38 lifecycle),
// and a second daemon pointed at the same score directory is refused there
// rather than clobbering the first one's files. The reason travels to the server
// so score.status and score.submit can say WHICH of "switched off",
// "unavailable", and "running" this daemon is in; a boot log line alone would
// leave the operator of a second BATON_SOCK fleet guessing.
//
// The open is BOUNDED within, which the daemon passes as scoreOpenTimeout —
// see there for what a directory that never answers does without one, and why
// the bound is a parameter: its branch needs a filesystem that hangs, and a
// deadline a caller chooses is the only way a test reaches it at all. A timeout
// lands in the same place a failure does — no store, a reason the three score
// surfaces report — because to whoever asked they are one condition: the memory
// is not running, and here is why.
func openScore(cfg config.ScoreConfig, p score.Policy, within time.Duration) (*score.Store, string) {
	if !cfg.IsEnabled() {
		return nil, "score is switched off in the config (score.enabled: false)"
	}
	dir := cfg.Directory()
	// The same before-the-read line loadServerBoot writes for the config, for the
	// same reason. This branch DID name its directory — in the reason string, and
	// in the warning the timeout leaves — but both of those arrive when the wait
	// gives up, which is five seconds after `baton` has stopped waiting and told the
	// operator to read this file. A store that is merely slow, or one that never
	// answers, is the same line here either way.
	log.Info().Str("dir", dir).Str("within", within.String()).Msg("boot: opening the fleet memory")
	st, err := waitForStore(within, func() (*score.Store, error) { return score.Open(dir, p) })
	switch {
	case errors.Is(err, errScoreOpenTimeout):
		// Str rather than Dur, which renders a duration in milliseconds: the line
		// said `waited=10000` beside a reason string saying `10s`, which is one
		// number reaching the operator as two.
		log.Warn().Str("dir", dir).Str("waited", within.String()).
			Msg("score store did not open in time; serving the fleet without its memory")
		return nil, timedOutReason(within, dir)
	case err != nil:
		log.Warn().Err(err).Str("dir", dir).Msg("score store open failed; running without fleet memory")
		return nil, err.Error()
	}
	if st.Unlocked() {
		log.Warn().Str("dir", dir).
			Msg("score directory cannot be locked on this filesystem; a second daemon here would corrupt it")
	}
	logScoreBoot(dir, st.Len(), st.Boot(), st.Health())
	return st, ""
}

// logScoreBoot says what the boot pass did to the operator's files. It is a
// function of its arguments and nothing else, which is what lets every branch
// below be exercised — two of the three need a filesystem failing in a
// particular way, and neither had ever been reached.
//
// THE COUNTERS are #38's lifecycle asking for one line per recovery action, so
// what the boot did is visible without reading the event log by hand.
//
// THE FAILED REWRITE is said separately and at Warn, because the counters carry
// it as the number 1. A full-disk boot logged `compaction_failures=1` and never
// "no space left on device" — and the words are the half that says what to do.
//
// THE REWRITE THAT SUCCEEDED is said at Warn too, and its sentence is no longer
// written here: #56 gave the store a compactor that runs while the daemon is up,
// so the same warning now has a second site, and two copies of a warning this
// specific drift. server.ScoreCompaction is the one producer for both, and why a
// rewrite nobody asked for is worth a Warn at all is stated there.
//
// The condition asks Compactions rather than Compacted so that this door and the
// running daemon's ask the same question — "has a rewrite happened" — of the
// same field, instead of two sites each inferring it from a size.
func logScoreBoot(dir string, entries int, d score.Delta, h score.Health) {
	if d != (score.Delta{}) || h != (score.Health{}) {
		server.ScoreCounters(log.Info(), d, h).
			Str("dir", dir).Msg("score recovered")
	}
	if h.CompactionError != "" {
		log.Warn().Str("dir", dir).Str("error", h.CompactionError).
			Msg("score could not rewrite its event log; the old log is intact, but every boot from here is slower than this one")
	}
	if h.Compactions > 0 {
		server.ScoreCompaction(dir, entries, h)
	}
}

// serverBoot is what the daemon read off the operator's filesystem BEFORE it
// bound its socket: the effective config, and the fleet memory opened from it.
//
// It is a struct rather than three more parameters because its whole reason to
// exist is that these travel together — one boot's reading of one filesystem —
// so the next thing hoisted above the bind belongs beside them rather than in a
// fourth argument.
type serverBoot struct {
	cfg         config.Config
	scoreStore  *score.Store // nil when score is switched off, or did not open
	scoreReason string       // why no store is running; empty when one is
	// release gives back everything the boot took: the PID file this daemon is
	// reachable through and the score directory's single-writer claim, with the
	// folds the store is still holding said on the way past.
	//
	// It exists because acquisition and release had drifted to different depths.
	// The PID file was written in runServer and removed at four sites — two bind
	// failures, runServerOn's cleanup, and clearStaleSocket — one of which
	// recomputed its path in a function that had been told it publishes nothing;
	// the store was closed at two, safe only because Close happens to be
	// idempotent. claimSession already owns the shape this borrows: whoever takes
	// a thing hands back the one function that gives it up.
	//
	// It is idempotent, because two callers must both be able to reach for it:
	// runServer defers it for the paths that never get as far as serving, and
	// runServerOn's cleanup calls it on the way out — including from the signal
	// handler, which os.Exits and runs no defer at all.
	//
	// clearStaleSocket's own removal of the PID file is NOT this and must not be
	// folded into it: that one tidies a PREDECESSOR's file, before this daemon
	// has published anything of its own.
	release func()
}

// loadServerBoot is everything the daemon acquires above the bind: the PID file
// it can be stopped through, the user's config, and the fleet memory — plus the
// one function that gives all of it back. runServer calls it BEFORE net.Listen
// — see there for what these used to do on the other side of the bind.
//
// NOTHING ELSE BELONGS HERE unless the fleet cannot be served without it. What
// this side of the bind buys is that a dead path is a daemon that never comes
// up rather than a socket nobody answers on; what it costs is that every
// millisecond spent here is a millisecond `baton` spends waiting, against a
// budget of five seconds. The legacy conductor sweep was hoisted here with the
// config and the store and has since gone back down: it is a recursive
// RemoveAll per leaked directory, of directories that pile up for as long as
// baton has been installed, and nothing between the bind and Serve reads one.
//
// It cannot fail. Every read here degrades to a warning and a default, because
// neither is a reason to refuse the fleet: the config falls back to the strict
// defaults, and a store that did not open is a first-class answer the three
// score surfaces already report.
func loadServerBoot(sock string) serverBoot {
	// The pid goes on disk HERE, above the bind, and no longer where the daemon
	// starts serving. Everything below this line can hang forever on a filesystem
	// that does not answer, and a daemon hung there is holding the session claim
	// runServer took a few lines up: every later `baton` loses claimSession to it
	// and exits quietly, so the wedge does not clear by itself. Without a pid file
	// above the bind there is nothing to stop it WITH — `baton --force` dials a
	// socket that was never created, finds nothing alive, and signals nobody.
	//
	// The cost is real and is the reason this used to sit below the listener: a
	// bind that then fails leaves the file behind. release covers those paths.
	// What that tidying does NOT cover is a SIGKILL, which leaves a PID file no
	// code runs to remove — so the file is never trusted on its own:
	// stopUnboundDaemon signals what it names only while the session claim is
	// still held, and the kernel drops that claim when its holder dies.
	//
	// It matters that clearStaleSocket ran first and that it removes the PID file
	// unconditionally. A failed write here is only a warning, so without that a
	// predecessor's pid would stay readable for this daemon's whole life, under a
	// claim this process holds — the one state where a stale pid gets signalled.
	// The write cannot leave a wrong pid behind; at worst it leaves none.
	pidPath := paths.PidFile(sock)
	if err := writePidFile(pidPath, os.Getpid()); err != nil {
		log.Warn().Err(err).Str("pid_file", pidPath).Msg("could not write pid file")
	}

	// SAID BEFORE THE READ, and the ordering is the whole of what this line is
	// worth. Every read below can hang forever on a filesystem that does not
	// answer, and a daemon hung there holds the session claim: `baton` waits out
	// its budget, reports that the server did not come up and points at this log
	// — which, until this line, was ZERO BYTES, at the default level and at -vv
	// alike. Nothing at all was logged between claiming the session and the first
	// unbounded read, so the operator was sent to a file that named neither the
	// file the daemon stopped on nor the fact that it had started at all.
	//
	// Info, not Debug, because the reader is an operator holding an error message
	// rather than someone debugging baton, and they will not have set a flag
	// before the boot that failed. It costs two lines per daemon start.
	//
	// It names the PATH rather than saying "the config": there are two ways to
	// point a daemon at a different one ($HOME and BATON_SOCK's second fleet), and
	// "which file" is the entire question this answers.
	log.Info().Str("path", paths.ConfigFile()).Msg("boot: reading the config")
	// Honour the user's settings from the shared config file; a missing or
	// unreadable config keeps the strict defaults (unique names, home workdir).
	cfg, err := config.Load()
	if err != nil {
		log.Warn().Err(err).Msg("config load failed, building the server on defaults")
	}
	// Note what is NOT gated on the load error: score.dir and score.enabled are
	// taken from cfg whatever it says, while the policy is not. That asymmetry is
	// deliberate, and it is the safe direction of each.
	//
	// A policy read from a half-parsed file is a wrong POLICY — entries climb at
	// the wrong threshold, briefs carry the wrong few — and every one of those is
	// recoverable by fixing the file, so refusing to guess costs nothing. A
	// directory read from a half-parsed file is a wrong STORE: the daemon would
	// open a different score.md, and the fleet's memory silently splits in two
	// with each half durable and neither wrong-looking. Falling back to the
	// default directory over a typo in a weight is the one mistake here that the
	// operator cannot see and the log cannot undo. The same holds for enabled:
	// switching the memory off over an unrelated typo is a bigger surprise than
	// running it on the numbers the daemon can still read.
	scorePol, _ := scorePolicy(cfg.Score, err)
	store, reason := openScore(cfg.Score, scorePol, scoreOpenTimeout)

	var once sync.Once
	return serverBoot{cfg: cfg, scoreStore: store, scoreReason: reason, release: func() {
		once.Do(func() {
			_ = os.Remove(pidPath)
			// Any fold the store is still holding gets said before the daemon
			// stops. Records are buffered for the next read to drain, and on the
			// way out there is no next read — so a repeat counted seconds before a
			// SIGTERM was durable in the log and named in no line anywhere, which
			// is #38's one-line-per-fold quietly not happening in the case an
			// operator is most likely to be investigating. Each record carries its
			// own timestamp, so these are stamped when they happened.
			server.ScoreFolds(store.DrainFolds())
			// Nil-safe and idempotent, so the no-store cases cost nothing here.
			store.Close()
		})
	}}
}

// runServerOn runs the long-lived server loop on an already-bound listener for
// the given socket path, on the config and store loadServerBoot already read. It
// is the body of runServer, split out so the loop can be driven without
// re-binding the socket: it builds the server, wires the plugin and the
// signal-driven shutdown/reload, and serves until the listener closes. It
// returns when Serve returns on its own; a SIGINT/SIGTERM instead tidies up and
// exits the process.
//
// It acquires nothing of its OWN: the config, the store and the PID file all
// arrive already taken, and it gives them back through the one function that
// took them (serverBoot.release) rather than by naming any of them again. The
// reads still below the bind belong to applyConfig — the same config file again,
// the TUI theme, and the Lua plugin — which need the server they configure and
// so could not move with the rest.
func runServerOn(ln net.Listener, sock string, boot serverBoot) error {
	// Here rather than beside the config and the store, on this side of the bind:
	// see sweepLegacyConductorWorkspaces for why the one unbounded walk left over
	// from older versions is the one that did not move.
	sweepLegacyConductorWorkspaces(sock)

	// Build the server before the cleanup/signal wiring, so the shutdown handler
	// can flush the final fleet/layout snapshot through it.
	cfg := boot.cfg
	rc := reloadableSettings(cfg)
	stateF := paths.StateFile(sock)
	scoreStore := boot.scoreStore
	// The two score keys a reload cannot apply, remembered as they were AT BOOT
	// so a later reload can tell whether the operator has since changed one. See
	// warnScoreKeysAReloadCannotApply.
	bootedScore := cfg.Score
	srv := server.New(ln, append(buildServerOptions(rc, stateF), usageOption(cfg), limitsOption(cfg),
		// The fan-out's ceiling is the plugin's own per-hook allowance, spent once
		// for a whole group. It is handed over rather than restated in the server
		// because this is the only place both numbers are visible, and the server's
		// must never be the shorter of the two.
		server.WithFanoutFilterBudget(plugin.FilterTimeout),
		server.WithScore(server.ScoreState{
			Store: scoreStore, Enabled: cfg.Score.IsEnabled(), Reason: boot.scoreReason,
		}))...)
	srv.Restore() // seed the fleet from the last snapshot (all as exited dead slots) before serving

	// The Lua plugin subsystem (docs/PLUGIN.md). Wire the server's event sink and
	// command runner to the plugin's worker before the first load, so a hook a
	// load-time action triggers is delivered and a command the picker invokes runs.
	plug := plugin.New(srv)
	defer plug.Close()
	srv.SetEventSink(plug.Dispatch)
	srv.SetRunCommand(plug.RunCommand)
	// server.TaskBrief and plugin.Brief mirror each other field-for-field; the
	// adapter keeps internal/server free of an internal/plugin import.
	srv.SetTaskFilter(func(b server.TaskBrief) (server.TaskBrief, bool) {
		out, ok := plug.FilterTask(plugin.Brief{
			Prompt: b.Prompt, Group: b.Group, Score: b.Score,
			Cwd: b.Cwd, Profile: b.Profile, Panel: b.Panel,
		})
		return server.TaskBrief{
			Prompt: out.Prompt, Group: out.Group, Score: out.Score,
			Cwd: out.Cwd, Profile: out.Profile, Panel: out.Panel,
		}, ok
	})
	pluginPath := paths.PluginFile()

	// applyConfig re-reads the YAML config, (re)runs the plugin on top of it, and
	// applies the merged effective config: the hot-reloadable server settings, the
	// output-event gate, the config/commands served to frontends. broadcast pushes
	// the refreshed config to open cockpits — set on a reload, skipped on first boot
	// when no client is attached yet.
	applyConfig := func(broadcast bool) {
		cfg, err := config.Load()
		if err != nil {
			log.Warn().Err(err).Msg("config load failed, using defaults as the plugin base")
		}
		res, perr := plug.Load(pluginPath, cfg)
		if perr != nil {
			log.Warn().Err(perr).Msg("plugin load error, continuing with what loaded")
		}
		// The cockpit appearance lives in its own file ($HOME/.baton/TUI.yaml). Read
		// it and attach onto the merged config so it rides the same broadcast to every
		// frontend; a read error is non-fatal — the frontends keep the built-in look.
		if tcfg, tErr := config.LoadTUI(); tErr != nil {
			log.Warn().Err(tErr).Msg("TUI config load failed, using the built-in theme and layouts")
		} else {
			res.Config.TUI = tcfg
		}
		rc := reloadableSettings(res.Config)
		srv.Reload(rc.settings)
		// score.promote-at, score.user-signals-at, score.rank and score.working-set
		// DO reload — each is a number the live store compares, so a fleet whose
		// entries are climbing too eagerly, or whose briefs are carrying the wrong
		// few, is retuned with C-t R rather than by restarting and returning every
		// panel as Exited.
		// Tiers already earned are replayed from the log and never move, and the
		// ranking half changes order and no tier at all (see Store.SetPolicy).
		//
		// score.dir / score.enabled deliberately do NOT: the store is opened once
		// at boot (above), because swapping a live store under in-flight
		// dispatches is R7's lifecycle work (#39). A SIGHUP leaves the fleet
		// memory itself exactly as booted — and SAYS SO when the file it just read
		// asks for something else, because the reload's own line says only that a
		// reload happened.
		//
		// A config that would not PARSE is not a config that says "use the
		// default": `cfg` is the zero value then, and retuning the store from it
		// would quietly undo an operator's whole policy on the reload that told
		// them their file has a typo in it. The running values stand until a file
		// the daemon could actually read asks for others — and the load failure is
		// already warned about above, so this branch stays silent.
		// A file still spelling the key `promote_at` is not a parse error — the
		// YAML decoder is not strict, so the key is dropped and the threshold
		// silently falls back to the default. Say so: the operator wrote a number
		// down and is running on another one, which is the surprise this whole
		// knob exists to avoid (invariant I8). Warned on the reload as well as at
		// boot, because retuning the threshold is exactly when the typo is made.
		if err == nil {
			if res.Config.Score.StalePromoteAt {
				log.Warn().Msg("config key score.promote_at is ignored; the key is score.promote-at")
			}
			warnScoreKeysAReloadCannotApply(bootedScore, res.Config.Score)
		}
		// The SAME gate boot uses, and the same warnings, so one file cannot
		// produce one live policy on a restart and another on a reload. This runs
		// on the first pass too — applyConfig(false), before Serve — which is why
		// the boot path above only OPENS the store and says nothing about it: two
		// call sites logged the same clamp twice on every start.
		if want, ok := scorePolicy(res.Config.Score, err); ok {
			if scoreStore.SetPolicy(want) {
				p := scoreStore.Policy()
				log.Info().Int("promote_at", p.PromoteAt).Int("user_signals_at", p.UserSignalsAt).
					Int("working_set", p.WorkingSet).
					Float64("rank_recency", p.Rank.Recency).Float64("rank_cwd", p.Rank.Cwd).
					Float64("rank_profile", p.Rank.Profile).Float64("rank_group", p.Rank.Group).
					Msg("score policy changed")
			}
			warnScorePolicy(res.Config, want, scoreStore)
		} else {
			warnScorePolicy(cfg, score.Policy{}, scoreStore)
		}
		srv.SetOutputEvents(res.WantOutput)
		srv.SetTitleHook(res.WantTitle)
		if data, mErr := json.Marshal(res.Config); mErr == nil {
			srv.SetClientConfig(data)
		}
		srv.SetPluginCommands(res.Commands)
		// Re-detect the agent backends on every config load, so installing an agent
		// CLI and pressing C-t R is the whole re-detect story — no new key, no new
		// command, and the same action that already means "re-read what you were
		// configured with, the fleet keeps running".
		srv.SetAgents(detectAgents(res.Config))
		if broadcast {
			srv.PushConfig()
		}
	}
	applyConfig(false) // before Serve: settle settings, config, and commands from the plugin

	// Tidy the socket and everything the boot took, whichever path gets us out:
	// a SIGINT/SIGTERM (the usual stop, and what baton --force / restart send) or
	// the server loop returning on its own. Both halves are idempotent — the once
	// in serverBoot.release, and os.Remove on a file that is already gone — so the
	// signal handler and the defer can both call this safely.
	//
	// Remove the files *before* closing the listener. A force-restart waits only
	// for the socket to become unreachable, so unlinking them first guarantees
	// both are gone before this daemon returns — otherwise a lagging removal here
	// could race a replacement daemon and delete its fresh socket/PID.
	//
	// The release runs HERE rather than on a defer, because the signal path — the
	// daemon's ordinary exit — calls os.Exit and runs no defer at all.
	cleanup := func() {
		_ = os.Remove(sock)
		boot.release()
		_ = ln.Close()
	}
	defer cleanup()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Info().Msg("shutting down")
		srv.SaveNow()  // flush the last layout before os.Exit skips the saverLoop and the defers
		srv.Shutdown() // SIGKILL every live panel's process group so no child outlives the daemon
		cleanup()
		os.Exit(0)
	}()

	// reload re-reads the config, re-runs the plugin, and applies the hot-reloadable
	// settings to the live server, leaving the fleet running — then pushes the
	// refreshed config and commands to open cockpits. Both reload paths share it: a
	// cockpit server.reload command and an external SIGHUP do the same thing.
	reload := func() { applyConfig(true) }
	srv.OnReload(reload)

	// SIGHUP is the conventional reload signal, so `kill -HUP $(cat pidfile)`
	// picks up an edited config without a restart, just like the cockpit action.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			reload()
			log.Info().Msg("config reloaded on SIGHUP")
		}
	}()

	log.Info().Str("socket", sock).Int("pid", os.Getpid()).Msgf("baton %s listening", version)
	if err := srv.Serve(); err != nil {
		log.Info().Err(err).Msg("server stopped")
	}
	srv.Shutdown() // if Serve returns on its own (listener closed), still reap the panels
	return nil
}

// reloadable holds the server settings that can change on a SIGHUP without
// restarting the daemon: the only knobs both the initial options and the reload
// path derive from the config, so the two can never drift. settings is what
// Reload swaps in wholesale, and the backlog caps ride along in it — WithQueue
// seeds the same two values at construction, so there is one source for both.
type reloadable struct {
	settings server.Settings
}

// reloadableSettings projects a config onto the hot-reloadable settings, applying
// the same defaults the server expects: strict names, the home workdir, and the
// built-in replay buffer when the config leaves a field unset.
func reloadableSettings(cfg config.Config) reloadable {
	rc := reloadable{settings: server.Settings{
		DefaultDir:  cfg.Panel.Workdir,
		DiffCommand: cfg.Panel.DiffCommand,
		Editor:      cfg.Panel.Editor,
		WorktreeDir: cfg.Panel.WorktreeDir,
		Limits:      cfg.Panel.Limits,
		AgentLimits: agentLimits(cfg.Panel.Agents),
	}}
	rc.settings.LogDir, rc.settings.AgentLogDir, rc.settings.AgentLog = logPolicy(cfg)
	rc.settings.LogMaxBytes = panellog.MaxBytes(cfg.Panel.LogMaxMB)
	rc.settings.Restart, rc.settings.AgentRestart = restartPolicies(cfg)
	rc.settings.Attention, rc.settings.AgentAttention = attentionPolicies(cfg)
	rc.settings.AgentIsolate = isolationPolicies(cfg)
	rc.settings.TrackCwd, rc.settings.RestoreCwd = cwdPolicy(cfg)
	if cfg.Settings.AllowNameConflict != nil {
		rc.settings.AllowNameConflict = *cfg.Settings.AllowNameConflict
	}
	if cfg.Settings.Remote != nil {
		rc.settings.Remote = *cfg.Settings.Remote
	}
	if cfg.Panel.ReplayKB > 0 {
		rc.settings.ReplayBytes = cfg.Panel.ReplayKB * 1024
	}
	// queueMax -1 keeps the server's built-in default; a positive config caps the
	// backlog. Concurrency passes straight through (0 = unlimited).
	rc.settings.QueueMax = -1
	if cfg.Queue.Max > 0 {
		rc.settings.QueueMax = cfg.Queue.Max
	}
	rc.settings.QueueConcurrency = cfg.Queue.Concurrency
	return rc
}

// logPolicy projects the config's logging keys onto the destination the daemon
// resolves per panel: the fleet-wide directory, the per-profile directories
// layered over it, and the profiles that log from the moment they spawn.
//
// The paths are expanded HERE, once, because both halves of the pair are
// hand-written ("~/.baton/logs") and the daemon must never resolve a relative one
// against whatever directory it happens to have been launched from. A profile
// that asks to be logged but names no directory of its own is left out of the
// override table entirely, so it inherits the fleet-wide one rather than being
// stored empty — the same rule agentLimits keeps.
func logPolicy(cfg config.Config) (dir string, agentDirs map[string]string, agentLog map[string]bool) {
	dir = paths.Expand(cfg.Panel.LogDir)
	for name, prof := range cfg.Panel.Agents {
		if d := paths.Expand(prof.LogDir); d != "" {
			if agentDirs == nil {
				agentDirs = make(map[string]string, len(cfg.Panel.Agents))
			}
			agentDirs[name] = d
		}
		if prof.Log {
			if agentLog == nil {
				agentLog = make(map[string]bool, len(cfg.Panel.Agents))
			}
			agentLog[name] = true
		}
	}
	return dir, agentDirs, agentLog
}

// agentLimits projects the configured agent profiles onto the caps-only table the
// server keeps. The server is handed the limits and never the commands: it
// resolves policy, the cockpit resolves what to run — which is also why a profile
// with no caps of its own is left out entirely rather than stored empty.
func agentLimits(profiles map[string]config.AgentProfile) map[string]limits.Limits {
	out := make(map[string]limits.Limits, len(profiles))
	for name, prof := range profiles {
		if !prof.Limits.IsZero() {
			out[name] = prof.Limits
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// restartPolicies projects the config's restart blocks onto the fleet-wide policy
// and the per-profile ones layered over it.
//
// A malformed policy is reported and dropped rather than guessed at. The failure
// mode matters: a policy baton half-understood would start processes on the
// user's behalf on a schedule they did not write, which is worse than a fleet
// that does not restart at all and says why in the log.
func restartPolicies(cfg config.Config) (restart.Policy, map[string]restart.Policy) {
	fleet, err := cfg.Panel.Restart.Policy()
	if err != nil {
		log.Warn().Err(err).Msg("restart policy ignored; panels will not be restarted")
		fleet = restart.Policy{}
	}
	var perAgent map[string]restart.Policy
	for name, prof := range cfg.Panel.Agents {
		p, err := prof.Restart.Policy()
		if err != nil {
			log.Warn().Err(err).Str("agent", name).Msg("agent restart policy ignored; the fleet policy applies")
			continue
		}
		if p.IsZero() {
			continue
		}
		if perAgent == nil {
			perAgent = make(map[string]restart.Policy, len(cfg.Panel.Agents))
		}
		perAgent[name] = p
	}
	return fleet, perAgent
}

// isolationPolicies projects the config's per-profile container isolation.
//
// It is the one policy here that does NOT drop what it cannot read. A malformed
// restart block simply restarts nothing and a bad track-cwd falls back to the
// default, because the forgiving direction costs a convenience. Isolation's
// forgiving direction is a panel running unconfined on the host — the outcome the
// user wrote the setting to prevent — so a broken policy is kept, poisoned, and
// fails every spawn of that profile with the reason until the file is fixed.
func isolationPolicies(cfg config.Config) map[string]isolate.Policy {
	var out map[string]isolate.Policy
	for name, prof := range cfg.Panel.Agents {
		p, err := prof.Isolation()
		if err != nil {
			log.Warn().Err(err).Str("agent", name).Msg("agent isolation is not usable; spawns of this profile will fail")
		}
		if !p.Enabled() {
			continue
		}
		if out == nil {
			out = make(map[string]isolate.Policy, len(cfg.Panel.Agents))
		}
		out[name] = p
	}
	return out
}

// cwdPolicy projects the config's working-directory settings, reporting anything
// the file got wrong and falling back to the defaults. The forgiving direction is
// deliberate here, unlike the restart policy: failing to learn a directory costs
// a convenience, while refusing to start processes is a safety property.
func cwdPolicy(cfg config.Config) (cwd.Track, cwd.Restore) {
	track, err := cfg.Panel.CwdTracking()
	if err != nil {
		log.Warn().Err(err).Msg("track-cwd ignored; using the default")
	}
	restore, err := cfg.Panel.CwdRestore()
	if err != nil {
		log.Warn().Err(err).Msg("restore-cwd ignored; using the default")
	}
	return track, restore
}

// attentionPolicies projects the config's quiet ladder onto the fleet-wide
// policy and the per-profile overrides layered on it — the same shape and the
// same "report and drop" discipline as restartPolicies above, so a hand-edited
// file can never silently promote panels into a state the user did not write.
//
// The ladder is also checked for order on the MERGED policy, not on each block
// in isolation, because that is where the mistake actually lives: a profile that
// only says `done-after: 30m` inherits a 10m stuck-after and inverts the rungs
// without either block being wrong on its own. An inverted ladder disables stuck
// for that scope and says which scope it was.
func attentionPolicies(cfg config.Config) (attn.Policy, map[string]attn.Policy) {
	fleet, err := cfg.Panel.Attention.Policy()
	if err != nil {
		log.Warn().Err(err).Msg("attention thresholds ignored; the built-in defaults apply")
		fleet = attn.Policy{}
	}
	if !fleet.Ordered() {
		log.Warn().Dur("done_after", fleet.Done()).Dur("stuck_after", fleet.Stuck()).
			Msg("panel.stuck-after is not above panel.done-after; stuck is disabled fleet-wide")
		fleet.StuckAfter = attn.Never
	}

	var perAgent map[string]attn.Policy
	for name, prof := range cfg.Panel.Agents {
		p, err := prof.Attention.ProfilePolicy(name)
		if err != nil {
			log.Warn().Err(err).Str("agent", name).Msg("agent attention thresholds ignored; the fleet ladder applies")
			continue
		}
		if p.IsZero() {
			continue
		}
		if merged := fleet.Merge(p); !merged.Ordered() {
			log.Warn().Str("agent", name).Dur("done_after", merged.Done()).Dur("stuck_after", merged.Stuck()).
				Msg("stuck-after is not above done-after for this profile; stuck is disabled for it")
			p.StuckAfter = attn.Never
		}
		if perAgent == nil {
			perAgent = make(map[string]attn.Policy, len(cfg.Panel.Agents))
		}
		perAgent[name] = p
	}
	return fleet, perAgent
}

// runClient attaches a TUI cockpit to this session's server. If the cockpit
// exits asking for a restart (the prefix+S binding), it force-stops the daemon,
// starts a fresh one, and re-attaches.
func runClient(verbose int, logPath, pluginPath string) error {
	sock := paths.Socket()
	for {
		c, err := client.Dial(sock)
		if err != nil {
			return fmt.Errorf("attach to baton server at %s: %w", sock, err)
		}

		final, runErr := tea.NewProgram(tui.New(c, version), tea.WithAltScreen()).Run()
		_ = c.Close()
		if runErr != nil {
			return fmt.Errorf("tui: %w", runErr)
		}

		if !restartRequested(final) {
			return nil
		}
		if err := stopDaemon(sock); err != nil {
			return err
		}
		if err := startDaemon(verbose, logPath, pluginPath, false); err != nil {
			return err
		}
	}
}

// alive reports whether a server is accepting connections on sock.
func alive(sock string) bool {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// clearStaleSocket removes what a crashed server left behind — a leftover socket
// and an orphaned PID file — but refuses to clobber a live one, enforcing one
// server per socket. A SIGKILLed daemon never runs its own cleanup, so nothing
// of its own removes either file.
//
// The PID file goes whether or not there was a socket to remove, and this is the
// one place that tidies it. Since the pid is published from above the bind, a
// daemon that died up there leaves a PID file and no socket at all — and a
// leftover pid outlives the process it named, so leaving one for the next reader
// to find is how a stale number gets signalled. The socket is what is guarded
// here, not the pid: a live one means a running daemon owns both files, so that
// case returns before anything is unlinked.
func clearStaleSocket(sock string) error {
	if _, err := os.Stat(sock); err == nil {
		if alive(sock) {
			return fmt.Errorf("baton server already running on %s", sock)
		}
		if err := os.Remove(sock); err != nil {
			return err
		}
	}
	_ = os.Remove(paths.PidFile(sock))
	return nil
}

// daemonPollTries and daemonPollGap bound how long start/stop waits for the
// socket to come up or be released — generous enough (5s) to ride out a loaded
// host binding or releasing the socket, short enough to fail visibly.
const (
	daemonPollTries = 100
	daemonPollGap   = 50 * time.Millisecond
)

// waitFor polls cond up to tries times, sleeping gap between attempts.
func waitFor(cond func() bool, tries int, gap time.Duration) bool {
	for range tries {
		if cond() {
			return true
		}
		time.Sleep(gap)
	}
	return cond()
}

// parsePid parses a PID written to a PID file, accepting only a positive decimal
// integer. It rejects empty/blank input, non-numeric garbage, and non-positive
// values (zero or negative), which never name a real process and would otherwise
// be passed to syscall.Kill — where 0 and negatives address process groups, not
// the daemon we mean to stop.
func parsePid(s string) (int, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty pid")
	}
	pid, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("parse pid %q: %w", trimmed, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d: must be positive", pid)
	}
	return pid, nil
}

// readPidFile reads and validates the daemon PID recorded at path. It fails if
// the file is missing/unreadable or holds anything other than a positive integer
// (see parsePid).
func readPidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("find daemon pid (%s): %w", path, err)
	}
	pid, err := parsePid(string(data))
	if err != nil {
		return 0, fmt.Errorf("parse daemon pid from %s: %w", path, err)
	}
	return pid, nil
}

// writePidFile records pid at path with owner-only permissions, the way the
// daemon advertises itself for force-stop and reload.
func writePidFile(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600)
}

// daemonArgs builds the command-line arguments for the re-executed daemon child:
// the pinned --log file and the same -v verbosity the parent was given.
func daemonArgs(logPath string, verbose int) []string {
	args := []string{"--log", logPath}
	for range verbose {
		args = append(args, "-v")
	}
	return args
}

// daemonEnviron builds the environment for the re-executed daemon child from a
// base environment (normally os.Environ()): it marks the child with daemonEnv=1
// and pins it to the control socket. A non-empty pluginPath is carried across
// the re-exec via BATON_PLUGIN, because the re-sessioned child cannot see the
// parent's --plugin flag.
func daemonEnviron(base []string, sock, pluginPath string) []string {
	env := append(append([]string{}, base...), daemonEnv+"=1", "BATON_SOCK="+sock)
	if pluginPath != "" {
		env = append(env, "BATON_PLUGIN="+pluginPath)
	}
	return env
}

// restartRequested reports whether the cockpit's final model asked for a daemon
// restart (the prefix+S binding). A model that does not expose RestartRequested,
// or returns false, means a normal exit.
func restartRequested(final tea.Model) bool {
	r, ok := final.(interface{ RestartRequested() bool })
	return ok && r.RestartRequested()
}
