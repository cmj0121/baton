package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/remote"
	"github.com/cmj0121/baton/internal/tui"
)

// The two halves of a remote attach (docs/REMOTE.md).
//
//	baton --remote   the near side: a connection form, then ssh, then the cockpit
//	baton --stdio    the far side: a bridge from this pipe to the fleet's socket
//
// Between them there is no listening port, no TLS and no key exchange of
// baton's own — the transport is ssh(1), running as the user, with the user's
// keys and the user's known_hosts.

// promptFunc asks for a target. ok is false when the person quit the form.
type promptFunc func(address, problem string) (tui.RemoteTarget, bool, error)

// cockpitFunc runs the cockpit against an attached remote client.
type cockpitFunc func(*client.Client, remote.Address) error

// attachRemote runs the connection form, dials, and hands the client to the
// ordinary cockpit.
func attachRemote(cfg config.Config) error {
	return attachRemoteWith(cfg, promptRemoteTarget, runRemoteCockpit)
}

// promptRemoteTarget runs the connection form as its own program. It is split
// out so attachRemoteWith can be driven without a terminal.
func promptRemoteTarget(address, problem string) (tui.RemoteTarget, bool, error) {
	final, err := tea.NewProgram(tui.NewRemoteForm(address, problem), tea.WithAltScreen()).Run()
	if err != nil {
		return tui.RemoteTarget{}, false, fmt.Errorf("remote form: %w", err)
	}
	target, ok := tui.RemoteResult(final)
	return target, ok, nil
}

// attachRemoteWith is the ask-dial-attach loop. A failure comes back INTO the
// form with the address kept, so a mistyped passkey is one keystroke from being
// retyped rather than a restart of the whole command.
func attachRemoteWith(cfg config.Config, prompt promptFunc, cockpit cockpitFunc) error {
	address, problem := "", ""
	for {
		target, ok, err := prompt(address, problem)
		if err != nil {
			return err
		}
		if !ok {
			return nil // esc — the person changed their mind, which is not an error
		}

		addr, err := remote.ParseAddress(target.Address)
		if err != nil {
			address, problem = target.Address, err.Error()
			continue
		}

		// The form's program has exited, so the terminal is back in its ordinary
		// state — which it has to be, because ssh may ask for a passphrase or a
		// host-key confirmation on /dev/tty before it gets anywhere.
		c, err := client.DialSSH(addr, client.SSHOptions{
			Passkey: target.Passkey,
			Source:  localSourceLabel(),
			Command: strings.TrimSpace(cfg.Settings.RemoteCommand),
		})
		if err != nil {
			address, problem = target.Address, err.Error()
			continue
		}

		c.Quiet() // the cockpit owns the screen now; ssh's stderr must not paint over it
		return cockpit(c, addr)
	}
}

// runRemoteCockpit runs the cockpit against a remote client and reports how it
// ended. A deliberate drop — a kick, remote switched off — carries a reason the
// server sent, and it is printed once the alt screen is down: a cockpit that
// vanishes should always say why.
func runRemoteCockpit(c *client.Client, addr remote.Address) error {
	_, runErr := tea.NewProgram(tui.New(c, version), tea.WithAltScreen()).Run()
	bye := c.Bye()
	_ = c.Close()
	if runErr != nil {
		return fmt.Errorf("tui: %w", runErr)
	}
	if bye != "" {
		return fmt.Errorf("%s: %s", addr.String(), bye)
	}
	return nil
}

// localSourceLabel is how this cockpit names itself in the fleet's remote
// overlay: `user@hostname` of the machine it is running ON. It is a label to
// recognise a connection by, self-declared exactly as the role is — the server
// is never asked to trust it.
func localSourceLabel() string {
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	host, _ := os.Hostname()
	return tui.RemoteSourceLabel(name, host)
}

// stdioBridgeTimeout bounds how long the bridge waits for a socket to answer
// before deciding it is stale. It is short because both ends are on this host.
const stdioBridgeTimeout = 2 * time.Second

// runStdio is the far side of a remote attach: it finds (or starts) this
// machine's fleet and copies bytes between the ssh pipe and its socket. It is
// not something a human types — `baton --remote` is what runs it.
//
// Nothing may be written to stdout but protocol: stdout IS the wire. The logger
// is already pointed at a file by the time this runs, and every diagnostic here
// goes through it.
func runStdio(verbose int, logPath, pluginPath string) error {
	sock, err := bridgeSocket(verbose, logPath, pluginPath)
	if err != nil {
		return err
	}
	return bridge(os.Stdin, os.Stdout, sock)
}

// bridge copies between one end of the ssh pipe and the fleet's socket. The
// streams are parameters rather than os.Stdin/os.Stdout directly so a test can
// drive it over a pair of pipes — swapping the process-wide ones would race
// every goroutine that logs.
func bridge(in io.Reader, out io.Writer, sock string) error {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return fmt.Errorf("attach to baton server at %s: %w", sock, err)
	}
	defer func() { _ = conn.Close() }()
	log.Info().Str("socket", sock).Msg("stdio bridge attached")

	// Copy both ways and return as soon as EITHER direction ends: the far cockpit
	// hanging up and the daemon closing the connection are both reasons to stop,
	// and whichever happens first tears the other down through the deferred Close.
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, in)
		if uc, ok := conn.(*net.UnixConn); ok {
			_ = uc.CloseWrite() // let the server see a clean EOF rather than a reset
		}
		done <- err
	}()
	go func() {
		_, err := io.Copy(out, conn)
		done <- err
	}()
	if err := <-done; err != nil {
		log.Info().Err(err).Msg("stdio bridge ended")
	}
	return nil
}

// bridgeSocket picks the fleet the bridge attaches to.
//
// The control socket is one fixed path per user, so the bridge lands on the same
// fleet the person has running on that machine even though sshd runs `baton
// --stdio` in a session of its own — and an explicit BATON_SOCK still wins,
// since Socket() reads it. It probes with a deadline rather than trusting the
// socket file, which may have been left behind by a crashed daemon on a host it
// cannot ask about, and only when nothing answers does it start a fleet —
// exactly as a local attach would.
func bridgeSocket(verbose int, logPath, pluginPath string) (string, error) {
	sock := paths.Socket()
	if aliveWithin(sock, stdioBridgeTimeout) {
		return sock, nil
	}
	log.Info().Msg("no fleet is running here; starting one for the remote attach")
	if err := startDaemon(verbose, logPath, pluginPath); err != nil {
		return "", err
	}
	return sock, nil
}

// aliveWithin reports whether a server answers on sock inside d. It is alive()
// with a deadline, because the bridge may meet a socket file left behind by a
// crashed daemon on a host it cannot ask about.
func aliveWithin(sock string, d time.Duration) bool {
	conn, err := net.DialTimeout("unix", sock, d)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
