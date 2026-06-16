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
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/client"
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
	Log     string           `short:"l" name:"log" placeholder:"FILE" help:"Write logs to FILE, e.g. /var/log/baton.log (default: a per-session file under the runtime dir)."`
	Verbose int              `short:"v" type:"counter" help:"Increase log verbosity (-v debug, -vv trace)."`
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
	kctx.FatalIfErrorf(attach(cli.Verbose, logPath))
}

// attach starts the session's server if needed, then runs the cockpit.
func attach(verbose int, logPath string) error {
	if err := startDaemon(verbose, logPath); err != nil {
		return err
	}
	return runClient()
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
	cleanup := func() {
		_ = ln.Close()
		_ = os.Remove(sock)
	}
	defer cleanup()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Info().Msg("shutting down")
		cleanup()
		os.Exit(0)
	}()

	log.Info().Str("socket", sock).Int("pid", os.Getpid()).Msgf("baton %s listening", version)
	if err := server.New(ln).Serve(); err != nil {
		log.Info().Err(err).Msg("server stopped")
	}
	return nil
}

// runClient attaches a TUI cockpit to this session's server.
func runClient() error {
	sock := paths.Socket()
	c, err := client.Dial(sock)
	if err != nil {
		return fmt.Errorf("attach to baton server at %s: %w", sock, err)
	}
	defer func() { _ = c.Close() }()

	if _, err := tea.NewProgram(tui.New(c), tea.WithAltScreen()).Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
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

// clearStaleSocket removes a leftover socket from a crashed server, but refuses
// to clobber a live one — enforcing one server per session.
func clearStaleSocket(sock string) error {
	if _, err := os.Stat(sock); err != nil {
		return nil
	}
	if alive(sock) {
		return fmt.Errorf("baton server already running on %s", sock)
	}
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
