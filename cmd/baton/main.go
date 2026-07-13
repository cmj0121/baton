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

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/plugin"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/tui"
	"github.com/cmj0121/baton/internal/usage"
)

const version = "0.5.0"

// daemonEnv marks the re-executed child that should run the server loop instead
// of attaching a cockpit.
const daemonEnv = "BATON_DAEMON"

// CLI is the entire baton command-line surface: a few flags, no commands.
type CLI struct {
	Log     string           `short:"l" name:"log" placeholder:"FILE" help:"Write logs to FILE (default: $HOME/.baton/baton.log)."`
	Plugin  string           `short:"p" name:"plugin" placeholder:"FILE" help:"Load the Lua plugin from FILE (default: $HOME/.baton/plug-in.lua)."`
	Verbose int              `short:"v" type:"counter" help:"Increase log verbosity (-v debug, -vv trace)."`
	Force   bool             `short:"f" name:"force" help:"Force-stop any running server for this session and start a fresh one before attaching."`
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
	if isDaemonChild() {
		kctx.FatalIfErrorf(runServer())
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

// isDaemonChild reports whether this process is the re-executed daemon child
// (marked by daemonEnv=1) that runs the server loop rather than attaching a
// cockpit.
func isDaemonChild() bool {
	return os.Getenv(daemonEnv) == "1"
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
		server.WithAllowNameConflict(rc.allowNameConflict),
		server.WithDefaultDir(rc.defaultDir),
		server.WithDiffCommand(rc.diffCommand),
		server.WithEditor(rc.editor),
		server.WithWorktreeDir(rc.worktreeDir),
		server.WithStateFile(stateF),
		server.WithQueue(rc.queueMax, rc.queueConcurrency),
	}
	if rc.replayBytes > 0 {
		opts = append(opts, server.WithReplayBytes(rc.replayBytes))
	}
	return opts
}

// usageOption builds the account-usage footer option from the config: it picks the
// data source (see usage.NewProvider) and the poll cadence, defaulting per source
// and clamping to a floor so a hand-edited interval can never hammer the poller.
// Usage source/interval are construction-time (a restart picks up a change); the
// show/hide toggle is client-side and live.
func usageOption(cfg config.Config) server.Option {
	p := usage.NewProvider(cfg.Usage.Source)
	interval := time.Duration(cfg.Usage.Interval) * time.Second
	if interval <= 0 {
		interval = usage.DefaultInterval(p)
	}
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	return server.WithUsage(p, interval)
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
	srv := server.New(ln, append(buildServerOptions(rc, stateF), usageOption(cfg))...)
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
		srv.Reload(rc.allowNameConflict, rc.defaultDir, rc.replayBytes, rc.diffCommand, rc.editor, rc.worktreeDir)
		srv.SetOutputEvents(res.WantOutput)
		srv.SetTitleHook(res.WantTitle)
		if data, mErr := json.Marshal(res.Config); mErr == nil {
			srv.SetClientConfig(data)
		}
		srv.SetPluginCommands(res.Commands)
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
// path derive from the config, so the two can never drift.
type reloadable struct {
	allowNameConflict bool
	defaultDir        string
	replayBytes       int    // 0 keeps the server's built-in replay default
	diffCommand       string // explicit diff command for the agent diff pop-up; empty falls back to git diff.tool then a built-in diff
	editor            string // commit editor for the git menu (GIT_EDITOR); empty falls back to git's own editor chain
	worktreeDir       string // base dir for new git-menu worktrees; empty falls back to a sibling of the agent's repo
	queueMax          int    // most queued tasks the backlog holds; -1 keeps the server default
	queueConcurrency  int    // most tasks one work item runs at once; 0 = unlimited
}

// reloadableSettings projects a config onto the hot-reloadable settings, applying
// the same defaults the server expects: strict names, the home workdir, and the
// built-in replay buffer when the config leaves a field unset.
func reloadableSettings(cfg config.Config) reloadable {
	rc := reloadable{defaultDir: cfg.Panel.Workdir, diffCommand: cfg.Panel.DiffCommand, editor: cfg.Panel.Editor, worktreeDir: cfg.Panel.WorktreeDir}
	if cfg.Settings.AllowNameConflict != nil {
		rc.allowNameConflict = *cfg.Settings.AllowNameConflict
	}
	if cfg.Panel.ReplayKB > 0 {
		rc.replayBytes = cfg.Panel.ReplayKB * 1024
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
