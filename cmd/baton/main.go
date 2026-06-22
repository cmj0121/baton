// Command baton is an agent-friendly terminal multiplexer.
//
// Running `baton` starts the background server for this login session (if one is
// not already running) and attaches a cockpit to it. There are no subcommands.
//
//	-l, --log FILE  write logs to FILE (default: a per-session file)
//	-v, -vv         increase log verbosity
//	-h, --help      show help and exit
//	-V, --version   show the version and exit
package main

import (
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
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/tui"
)

const version = "0.0.0-dev"

// daemonEnv marks the re-executed child that should run the server loop instead
// of attaching a cockpit.
const daemonEnv = "BATON_DAEMON"

// CLI is the entire baton command-line surface: a few flags, no commands.
type CLI struct {
	Log     string           `short:"l" name:"log" placeholder:"FILE" help:"Write logs to FILE (default: $HOME/.baton/baton.log)."`
	Verbose int              `short:"v" type:"counter" help:"Increase log verbosity (-v debug, -vv trace)."`
	Force   bool             `short:"f" name:"force" help:"Force-stop any running server for this session and start a fresh one before attaching."`
	Version kong.VersionFlag `short:"V" help:"Print the version and quit."`
}

func main() {
	var cli CLI
	kctx := kong.Parse(&cli,
		kong.Name("baton"),
		kong.Description("an agent-friendly terminal multiplexer"),
		kong.UsageOnError(),
		kong.Vars{"version": version},
	)

	logPath := cli.Log
	if logPath == "" {
		logPath = paths.LogFile()
	}
	kctx.FatalIfErrorf(setupLogger(cli.Verbose, logPath))

	// The daemon child re-executes this same binary with daemonEnv set.
	if os.Getenv(daemonEnv) == "1" {
		kctx.FatalIfErrorf(runServer())
		return
	}
	kctx.FatalIfErrorf(attach(cli.Verbose, logPath, cli.Force))
}

// attach starts the session's server if needed, then runs the cockpit. With
// force, any running server is stopped first so the session comes up fresh.
func attach(verbose int, logPath string, force bool) error {
	if force {
		if err := stopDaemon(paths.Socket()); err != nil {
			return err
		}
	}
	if err := startDaemon(verbose, logPath); err != nil {
		return err
	}
	return runClient(verbose, logPath)
}

// stopDaemon force-stops this session's running daemon, if any, and waits for it
// to release the socket. It is a no-op (bar tidying a stale socket) when no
// server is alive.
func stopDaemon(sock string) error {
	if !alive(sock) {
		return clearStaleSocket(sock)
	}

	pidPath := paths.PidFile(sock)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return fmt.Errorf("find daemon pid (%s): %w", pidPath, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("parse daemon pid from %s: %w", pidPath, err)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal daemon %d: %w", pid, err)
	}
	if !waitFor(func() bool { return !alive(sock) }, 50, 50*time.Millisecond) {
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
func startDaemon(verbose int, logPath string) error {
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
	args := []string{"--log", logPath}
	for range verbose {
		args = append(args, "-v")
	}
	proc := exec.Command(exe, args...)
	proc.Env = append(os.Environ(), daemonEnv+"=1", "BATON_SOCK="+sock)
	proc.Stdout = logf
	proc.Stderr = logf
	proc.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach into its own session
	if err := proc.Start(); err != nil {
		return fmt.Errorf("start baton server: %w", err)
	}

	if !waitFor(func() bool { return alive(sock) }, 50, 50*time.Millisecond) {
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

	// Record the PID so clients can force-stop this daemon (baton --force / the
	// in-TUI restart). Non-fatal if it cannot be written.
	pidPath := paths.PidFile(sock)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		log.Warn().Err(err).Str("pid_file", pidPath).Msg("could not write pid file")
	}

	// Honour the user's settings from the shared config file; a missing or
	// unreadable config keeps the strict defaults (unique names, home workdir).
	// Build the server before the cleanup/signal wiring, so the shutdown handler
	// can flush the final fleet/layout snapshot through it.
	cfg, _ := config.Load()
	rc := reloadableSettings(cfg)
	stateF := paths.StateFile(sock)
	opts := []server.Option{
		server.WithVersion(version),
		server.WithAllowNameConflict(rc.allowNameConflict),
		server.WithDefaultDir(rc.defaultDir),
		server.WithDiffCommand(rc.diffCommand),
		server.WithStateFile(stateF),
	}
	if rc.replayBytes > 0 {
		opts = append(opts, server.WithReplayBytes(rc.replayBytes))
	}
	srv := server.New(ln, opts...)
	srv.Restore() // seed the fleet from the last snapshot (all as exited dead slots) before serving

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

	// reload re-reads the config and applies the hot-reloadable settings to the
	// live server, leaving the fleet running. Both reload paths share it: a
	// cockpit server.reload command and an external SIGHUP do the same thing.
	reload := func() {
		cfg, err := config.Load()
		if err != nil {
			log.Warn().Err(err).Msg("config reload failed, keeping current settings")
			return
		}
		rc := reloadableSettings(cfg)
		srv.Reload(rc.allowNameConflict, rc.defaultDir, rc.replayBytes, rc.diffCommand)
	}
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
}

// reloadableSettings projects a config onto the hot-reloadable settings, applying
// the same defaults the server expects: strict names, the home workdir, and the
// built-in replay buffer when the config leaves a field unset.
func reloadableSettings(cfg config.Config) reloadable {
	rc := reloadable{defaultDir: cfg.Panel.Workdir, diffCommand: cfg.Panel.DiffCommand}
	if cfg.Settings.AllowNameConflict != nil {
		rc.allowNameConflict = *cfg.Settings.AllowNameConflict
	}
	if cfg.Panel.ReplayKB > 0 {
		rc.replayBytes = cfg.Panel.ReplayKB * 1024
	}
	return rc
}

// runClient attaches a TUI cockpit to this session's server. If the cockpit
// exits asking for a restart (the prefix+S binding), it force-stops the daemon,
// starts a fresh one, and re-attaches.
func runClient(verbose int, logPath string) error {
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

		r, ok := final.(interface{ RestartRequested() bool })
		if !ok || !r.RestartRequested() {
			return nil
		}
		if err := stopDaemon(sock); err != nil {
			return err
		}
		if err := startDaemon(verbose, logPath); err != nil {
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
