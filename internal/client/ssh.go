package client

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/remote"
)

// The remote transport: `ssh [-p port] <target> baton --stdio`.
//
// baton opens no listening port, ships no TLS and invents no key exchange. The
// stream carries every panel's full terminal input and output, and it is
// protected by exactly the thing that already protects the user's shell — their
// own ssh keys and their own known_hosts. The far side's `--stdio` bridges this
// pipe to that machine's session socket, finding or starting the daemon the same
// way a local attach does.

// DefaultRemoteCommand is what the far side is asked to run. It is overridable
// (see DialSSH) because `ssh host cmd` runs a NON-interactive login shell, whose
// PATH often misses the directory baton was installed into — the single most
// likely reason a first remote attach fails, and not one worth making a person
// guess at.
const DefaultRemoteCommand = "baton --stdio"

// remoteHandshakeTimeout bounds the wait for the fleet's answer to the hello.
// It is generous next to a LAN round-trip because the far side may be STARTING a
// daemon rather than finding one, and that costs a process spawn plus the socket
// coming up.
const remoteHandshakeTimeout = 20 * time.Second

// SSHOptions is what a remote attach needs beyond the address: the passkey the
// fleet was enabled with, the label this cockpit is listed under, and an
// override for the far-side command.
type SSHOptions struct {
	Passkey string // the 8-character code; the attach is refused without the current one
	Source  string // how this connection is labelled in the remote overlay, e.g. "cmj@laptop.lan"
	Command string // far-side command; empty means DefaultRemoteCommand
	SSHArgs []string
}

// DialSSH attaches to a fleet on another machine. It starts ssh, says hello as
// a remote cockpit, and WAITS for the fleet's answer — so a wrong passkey, a
// fleet with remote switched off, or a host that cannot run baton is reported
// here, by the connection form, rather than by a cockpit that opens and dies.
//
// It must be called with the terminal in its ordinary state, not inside an
// alt-screen program: ssh asks for a passphrase or a host-key confirmation on
// /dev/tty, and the person has to be able to see and answer it.
func DialSSH(addr remote.Address, opts SSHOptions) (*Client, error) {
	conn, err := startSSH(addr, opts)
	if err != nil {
		return nil, err
	}

	c := newClient(conn, addr.String())
	go c.readLoop()

	// Both failures below go through explain, and the FIRST one is the reason why:
	// when ssh dies at once — a bad host, a refused key, no baton over there — the
	// pipe is already shut by the time the hello is written, so this Send fails
	// with "broken pipe". That is a true statement about a file descriptor and
	// tells a person nothing. ssh's own last line is what they need, and it is
	// sitting in the tap either way.
	hello := proto.Command{Action: "hello", Role: "remote", Passkey: opts.Passkey, Source: opts.Source}
	if err := c.Send(hello); err != nil {
		reason := conn.explain(err)
		_ = conn.Close()
		return nil, fmt.Errorf("%s: %w", addr.String(), reason)
	}

	if err := c.Wait(remoteHandshakeTimeout); err != nil {
		reason := conn.explain(err)
		_ = conn.Close()
		return nil, fmt.Errorf("%s: %w", addr.String(), reason)
	}
	// Only past the gate is the plugin config worth asking for; a refused attach
	// would otherwise leave a stray command in a pipe nobody reads.
	_ = c.Send(proto.Command{Action: "config.get"})
	return c, nil
}

// startSSH launches the ssh child and wires the pipe.
//
// The pipe ends are made with os.Pipe rather than taken from exec.Cmd's own
// helpers because a *os.File carries read and write deadlines, and the client's
// idle read deadline is how a dead peer is noticed at all. A transport without
// them would leave a cockpit attached to a host that stopped answering.
func startSSH(addr remote.Address, opts SSHOptions) (*sshConn, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("open ssh input pipe: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_, _ = inR.Close(), inW.Close()
		return nil, fmt.Errorf("open ssh output pipe: %w", err)
	}

	tap := &stderrTap{passthrough: true}
	cmd := exec.Command("ssh", sshArgs(addr, opts)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, tap
	if err := cmd.Start(); err != nil {
		_, _ = inR.Close(), inW.Close()
		_, _ = outR.Close(), outW.Close()
		return nil, fmt.Errorf("start ssh: %w", err)
	}
	// The child holds its own descriptors now; ours would otherwise keep the pipe
	// open past the child's exit and hide the EOF the read loop waits for.
	_, _ = inR.Close(), outW.Close()

	// Reap the child in the background and close `done` when it has gone. Waiting
	// here rather than only in Close is what makes ssh's last words readable: the
	// stderr copy is a goroutine exec owns, and it is joined by Wait — so a failure
	// explained before Wait returns would quote a buffer that had not been filled
	// in yet, which is exactly the race that shows up as an empty reason.
	conn := &sshConn{r: outR, w: inW, cmd: cmd, tap: tap, done: make(chan struct{})}
	go func() { defer close(conn.done); _ = cmd.Wait() }()
	return conn, nil
}

// sshArgs builds the ssh command line. It stays deliberately short: everything
// about how to reach a host — the key, the jump host, the alias — belongs in the
// user's own ~/.ssh/config, which ssh reads for us and baton must not re-invent.
func sshArgs(addr remote.Address, opts SSHOptions) []string {
	args := make([]string, 0, 8)
	if addr.Port != 0 && addr.Port != remote.DefaultPort {
		args = append(args, "-p", strconv.Itoa(addr.Port))
	}
	args = append(args, opts.SSHArgs...)
	command := opts.Command
	if strings.TrimSpace(command) == "" {
		command = DefaultRemoteCommand
	}
	return append(args, addr.Target(), command)
}

// sshConn is the protocol connection over an ssh child's stdin/stdout.
type sshConn struct {
	r   *os.File
	w   *os.File
	cmd *exec.Cmd
	tap *stderrTap

	// done is closed once the ssh child has been reaped, which is also when its
	// stderr has been fully copied into the tap.
	done chan struct{}
	once sync.Once
}

func (c *sshConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *sshConn) Write(p []byte) (int, error) { return c.w.Write(p) }

func (c *sshConn) SetReadDeadline(t time.Time) error  { return c.r.SetReadDeadline(t) }
func (c *sshConn) SetWriteDeadline(t time.Time) error { return c.w.SetWriteDeadline(t) }

// Close ends the ssh session: the pipes go first so the far side sees EOF and
// exits on its own, and the child is only killed if it does not take the hint.
func (c *sshConn) Close() error {
	c.once.Do(func() {
		_, _ = c.w.Close(), c.r.Close()
		if !c.reaped(2 * time.Second) {
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
			<-c.done
		}
	})
	return nil
}

// reaped waits up to d for the ssh child to exit, reporting whether it did.
func (c *sshConn) reaped(d time.Duration) bool {
	select {
	case <-c.done:
		return true
	case <-time.After(d):
		return false
	}
}

// quiet stops ssh's stderr from reaching the terminal, keeping only the copy in
// the tap. It is called once the cockpit owns the screen: a late "Connection to
// host closed" printed over an alt-screen TUI is corruption, not information.
func (c *sshConn) quiet() { c.tap.quiet() }

// explain turns a failed handshake into something a person can act on. The
// server's own refusal ("wrong passkey") already reads well and is passed
// through; a silent death instead gets ssh's last words, which is where "No
// route to host", "Permission denied" and "baton: command not found" live.
func (c *sshConn) explain(err error) error {
	// Give the child a moment to be reaped first: until Wait returns, the stderr
	// copy may still be in flight and the tap would read empty.
	c.reaped(explainGrace)
	if tail := c.tap.tail(); tail != "" {
		if strings.Contains(tail, "command not found") || strings.Contains(tail, "not found") {
			return fmt.Errorf("%s (is baton on the remote PATH? set settings.remote-command)", tail)
		}
		return fmt.Errorf("%s", tail)
	}
	return err
}

// explainGrace is how long a failure waits for ssh to finish dying before it
// quotes what ssh said. It is short: the process is already on its way out, and
// a reason a second late is worse than the generic one.
const explainGrace = 2 * time.Second

// maxStderrTap bounds what is kept from ssh's stderr: enough for the handful of
// lines that explain a failure, never enough for a chatty banner to grow without
// limit in a long session.
const maxStderrTap = 4096

// stderrTap is ssh's stderr: passed through to the real terminal while the
// connection is being made, so a passphrase prompt's explanation is visible, and
// kept in a bounded buffer so a failure can be quoted back afterwards.
type stderrTap struct {
	mu          sync.Mutex
	buf         []byte
	passthrough bool
}

func (t *stderrTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	pass := t.passthrough
	t.buf = append(t.buf, p...)
	if len(t.buf) > maxStderrTap {
		t.buf = t.buf[len(t.buf)-maxStderrTap:]
	}
	t.mu.Unlock()
	if pass {
		_, _ = os.Stderr.Write(p)
	}
	return len(p), nil
}

func (t *stderrTap) quiet() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.passthrough = false
}

// tail is the last non-empty line ssh wrote, which is the one that says what
// went wrong. Anything before it is context the caller did not ask for.
func (t *stderrTap) tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}

// Quiet silences the remote transport's pass-through of ssh's stderr. The
// runner calls it just before the cockpit takes the screen.
func (c *Client) Quiet() {
	if sc, ok := c.conn.(*sshConn); ok {
		sc.quiet()
	}
}
