// Command baton is an agent-friendly terminal multiplexer.
//
// Running `baton` starts the background server for this login session (if one is
// not already running) and attaches a cockpit to it.
//
//	-l, --log FILE  write logs to FILE (default: a per-session file)
//	-v, -vv         increase log verbosity
//	-h, --help      show help and exit
//	-V, --version   show the version and exit
//
// Two subcommands drive the fleet over the session's socket without the cockpit:
// `baton ctl` is a thin control client for a human or a script (see ctl.go), and
// `baton mcp` is a Model Context Protocol server an agent's .mcp.json launches so
// it can spawn, group, signal, and send prompts to panels (see mcp.go).
package main

import (
	"encoding/json"
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
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/tui"
	"github.com/cmj0121/baton/internal/usage"
)

const version = "1.2.1"

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
	kctx.FatalIfErrorf(setupLogger(cli.Verbose, logPath))

	// The daemon child re-executes this same binary with daemonEnv set.
	if claimDaemonRole() {
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
// the per-session default. An empty flag means "use the default".
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

// attach starts the session's server if needed, then runs the cockpit. With
// force, any running server is stopped first so the session comes up fresh. An
// explicit plugin path is handed to the daemon child through BATON_PLUGIN.
func attach(verbose int, logPath, pluginPath string, force bool) error {
	if force {
		if err := stopDaemon(paths.Socket()); err != nil {
			return err
		}
	}
	if err := startDaemon(verbose, logPath, pluginPath); err != nil {
		return err
	}
	return runClient(verbose, logPath, pluginPath)
}

// stopDaemon force-stops this session's running daemon, if any, and waits for it
// to release the socket. It is a no-op (bar tidying a stale socket) when no
// server is alive.
func stopDaemon(sock string) error {
	if !alive(sock) {
		return clearStaleSocket(sock)
	}

	pidPath := paths.PidFile(sock)
	pid, err := readPidFile(pidPath)
	if err != nil {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal daemon %d: %w", pid, err)
	}
	if !waitFor(func() bool { return !alive(sock) }, daemonPollTries, daemonPollGap) {
		return fmt.Errorf("daemon %d did not stop in time", pid)
	}
	log.Info().Int("pid", pid).Msg("daemon stopped")
	return nil
}

// setupLogger points the global zerolog logger at the log file, creating it (and
// its directory) as needed.
func setupLogger(verbosity int, logPath string) error {
	level := zerolog.InfoLevel
	switch {
	case verbosity >= 2:
		level = zerolog.TraceLevel
	case verbosity == 1:
		level = zerolog.DebugLevel
	}
	zerolog.SetGlobalLevel(level)

	if err := paths.EnsureDir(logPath); err != nil {
		return fmt.Errorf("prepare log dir: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", logPath, err)
	}
	writer := zerolog.ConsoleWriter{Out: f, NoColor: true, TimeFormat: "2006-01-02 15:04:05"}
	log.Logger = zerolog.New(writer).With().Timestamp().Logger()
	return nil
}

// startDaemon ensures this session's server is running, launching it in the
// background if not. Exactly one server runs per login session (one socket).
func startDaemon(verbose int, logPath, pluginPath string) error {
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

	// Re-exec ourselves as the daemon, pinned to this session's socket and log
	// file and carrying the same verbosity.
	proc := exec.Command(exe, daemonArgs(logPath, verbose)...)
	proc.Env = daemonEnviron(os.Environ(), sock, pluginPath)
	proc.Stdout = logf
	proc.Stderr = logf
	proc.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach into its own session
	if err := proc.Start(); err != nil {
		return fmt.Errorf("start baton server: %w", err)
	}

	if !waitFor(func() bool { return alive(sock) }, daemonPollTries, daemonPollGap) {
		return fmt.Errorf("baton server did not come up; see %s", logPath)
	}
	log.Debug().Str("socket", sock).Msg("daemon started")
	return nil
}

// runServer is the long-lived server loop (the daemon child).
func runServer() error {
	sock := paths.Socket()
	if err := paths.EnsureDir(sock); err != nil {
		return fmt.Errorf("prepare socket dir: %w", err)
	}

	// One backend per session, decided here rather than by who reaches bind first
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
	return runServerOn(ln, sock)
}

// buildServerOptions projects the hot-reloadable settings and the per-session
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
		server.WithQueue(rc.queueMax, rc.queueConcurrency),
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
	return server.WithUsage(p, interval, server.UsageDisplay{
		WarnAt:          warn,
		AlarmAt:         alarm,
		CountdownFormat: cfg.Usage.CountdownFormat,
	})
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

// runServerOn runs the long-lived server loop on an already-bound listener for
// the given socket path. It is the body of runServer, split out so the loop can
// be driven without re-binding the socket: it records the PID, builds the server
// from the effective config, wires the plugin and the signal-driven
// shutdown/reload, and serves until the listener closes. It returns when Serve
// returns on its own; a SIGINT/SIGTERM instead tidies up and exits the process.
func runServerOn(ln net.Listener, sock string) error {
	// Record the PID so clients can force-stop this daemon (baton --force / the
	// in-TUI restart). Non-fatal if it cannot be written.
	pidPath := paths.PidFile(sock)
	if err := writePidFile(pidPath, os.Getpid()); err != nil {
		log.Warn().Err(err).Str("pid_file", pidPath).Msg("could not write pid file")
	}

	// Honour the user's settings from the shared config file; a missing or
	// unreadable config keeps the strict defaults (unique names, home workdir).
	// Build the server before the cleanup/signal wiring, so the shutdown handler
	// can flush the final fleet/layout snapshot through it.
	cfg, err := config.Load()
	if err != nil {
		log.Warn().Err(err).Msg("config load failed, building the server on defaults")
	}
	rc := reloadableSettings(cfg)
	stateF := paths.StateFile(sock)
	srv := server.New(ln, append(buildServerOptions(rc, stateF), usageOption(cfg), limitsOption(cfg))...)
	srv.Restore() // seed the fleet from the last snapshot (all as exited dead slots) before serving

	// The Lua plugin subsystem (docs/PLUGIN.md). Wire the server's event sink and
	// command runner to the plugin's worker before the first load, so a hook a
	// load-time action triggers is delivered and a command the picker invokes runs.
	plug := plugin.New(srv)
	defer plug.Close()
	srv.SetEventSink(plug.Dispatch)
	srv.SetRunCommand(plug.RunCommand)
	srv.SetTaskFilter(plug.FilterTask)
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

	// Tidy the socket and PID file on the way out, whichever path gets us there:
	// a SIGINT/SIGTERM (the usual stop, and what baton --force / restart send) or
	// the server loop returning on its own. sync.Once keeps it to exactly one run
	// so the signal handler and the defer can both call it safely.
	//
	// Remove the files *before* closing the listener. A force-restart waits only
	// for the socket to become unreachable, so unlinking it first guarantees both
	// files are gone before this daemon returns — otherwise a lagging removal here
	// could race a replacement daemon and delete its fresh socket/PID.
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = os.Remove(sock)
			_ = os.Remove(pidPath)
			_ = ln.Close()
		})
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
// Reload swaps in wholesale; the queue caps are seeded at construction only.
type reloadable struct {
	settings         server.Settings
	queueMax         int // most queued tasks the backlog holds; -1 keeps the server default
	queueConcurrency int // most tasks one work item runs at once; 0 = unlimited
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
	rc.queueMax = -1
	if cfg.Queue.Max > 0 {
		rc.queueMax = cfg.Queue.Max
	}
	rc.queueConcurrency = cfg.Queue.Concurrency
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
		if err := startDaemon(verbose, logPath, pluginPath); err != nil {
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

// clearStaleSocket removes a leftover socket (and its orphaned PID file) from a
// crashed server, but refuses to clobber a live one — enforcing one server per
// session. A SIGKILLed daemon never runs its own cleanup, so we tidy both files
// here before a fresh daemon takes the session.
func clearStaleSocket(sock string) error {
	if _, err := os.Stat(sock); err != nil {
		return nil
	}
	if alive(sock) {
		return fmt.Errorf("baton server already running on %s", sock)
	}
	_ = os.Remove(paths.PidFile(sock))
	return os.Remove(sock)
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
// and pins it to this session's socket. A non-empty pluginPath is carried across
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
