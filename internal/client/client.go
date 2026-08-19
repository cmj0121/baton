// Package client is the frontend side of the socket: it dials the server,
// performs the handshake, and exposes a stream of server events.
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// Conn is what a Client speaks the protocol over. A local attach hands it a
// *net.UnixConn; a remote one hands it the ssh pipe (see ssh.go), whose ends are
// *os.File precisely so the deadlines below still work — the idle read deadline
// is how a dead peer is noticed, and a transport that cannot carry one would
// leave a cockpit staring at a corpse.
type Conn interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// readTimeout is the client's idle read deadline (nanoseconds), reset on every
// successfully decoded message. It defaults to proto.ClientReadTimeout; tests
// override it (via SetReadTimeout) to a few milliseconds so readLoop's timeout
// path runs fast. It is atomic so a test adjusting it never races a still-draining
// readLoop goroutine that reads it — in production it is set once and only read.
var readTimeout atomic.Int64

func init() { readTimeout.Store(int64(proto.ClientReadTimeout)) }

// Client is a live attachment to the baton server.
type Client struct {
	conn Conn

	// endpoint is the label the cockpit's footer shows. A remote attach sets it
	// to the address dialled; a local one leaves it empty and Endpoint falls back
	// to what the socket itself says.
	endpoint string

	// handshake carries the outcome of the FIRST server message: nil once
	// anything but a refusal arrives, or the refusal's reason. A remote attach
	// waits on it (see Wait) so a wrong passkey is reported by the connection
	// form rather than by a cockpit that opens and instantly dies. bye holds the
	// reason from a "goodbye" so the caller can print it after the TUI is down.
	handshake chan error
	settled   sync.Once
	bye       atomic.Value // string

	sendMu sync.Mutex // serialises Send; the zoom reader and the UI both write
	enc    *json.Encoder

	// Events delivers control messages; Output delivers PTY data from a zoomed
	// panel; Stats delivers the server's host telemetry; Telemetry delivers the
	// Monitor's live panel refreshes (state, activity, sparkline). Splitting them
	// keeps a burst of output, a stale stat, or a telemetry tick from starving the
	// cockpit's structural events. All are closed on disconnect.
	Events    chan proto.ServerMsg
	Output    chan proto.ServerMsg
	Stats     chan proto.ServerMsg
	Telemetry chan proto.ServerMsg
	// Config delivers the merged effective config + plugin commands (config.get and
	// reload pushes). It rides its own channel so it never interleaves with the
	// structural panel stream a frontend counts on.
	Config chan proto.ServerMsg
	// Footer delivers a plugin's persistent footer segment. It is latest-wins like
	// telemetry, since a plugin may refresh it rapidly (e.g. a live token counter).
	Footer chan proto.ServerMsg
	// Usage delivers the account usage/cost footer segment. Latest-wins like Footer:
	// only the freshest sample matters, and it must never stall structural events.
	Usage chan proto.ServerMsg
	// Remote delivers the remote-access status and connection list. It rides its
	// own channel for the same reason Config does: it is pushed whenever ANY
	// client attaches, detaches or is kicked, and a status snapshot has no
	// business landing in the middle of the structural panel stream a frontend
	// counts. Latest-wins — each message is a complete picture.
	Remote chan proto.ServerMsg
}

// Dial connects to the server at socket, says hello, and starts reading events.
func Dial(socket string) (*Client, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, err
	}

	c := newClient(conn, "")
	go c.readLoop()

	if err := c.Send(proto.Command{Action: "hello"}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// Ask for the merged effective config (defaults <- YAML <- plugin) and the plugin
	// command list, so the cockpit applies any plugin keymaps/toggles and fills its
	// command picker. A failure here is non-fatal — the cockpit keeps its local
	// config and just misses plugin overrides.
	_ = c.Send(proto.Command{Action: "config.get"})
	return c, nil
}

// newClient builds the channel set around an open connection. Both dial paths go
// through it so the local and the remote attach can never drift in what they
// buffer or what they close.
func newClient(conn Conn, endpoint string) *Client {
	return &Client{
		conn:      conn,
		endpoint:  endpoint,
		handshake: make(chan error, 1),
		enc:       json.NewEncoder(conn),
		Events:    make(chan proto.ServerMsg, proto.EventBufferSize),
		Output:    make(chan proto.ServerMsg, proto.EventBufferSize),
		Stats:     make(chan proto.ServerMsg, proto.EventBufferSize),
		Telemetry: make(chan proto.ServerMsg, proto.EventBufferSize),
		Config:    make(chan proto.ServerMsg, proto.EventBufferSize),
		Footer:    make(chan proto.ServerMsg, proto.EventBufferSize),
		Usage:     make(chan proto.ServerMsg, proto.EventBufferSize),
		Remote:    make(chan proto.ServerMsg, proto.EventBufferSize),
	}
}

// Wait blocks until the server has answered the hello, or d elapses. It returns
// the server's refusal when the answer was a "goodbye" — a wrong passkey, remote
// switched off, too many attempts — and nil once an ordinary message has landed.
//
// A local attach has no use for it: the socket is uid-private and the hello is
// never refused. A remote one waits before opening a cockpit at all.
func (c *Client) Wait(d time.Duration) error {
	select {
	case err := <-c.handshake:
		return err
	case <-time.After(d):
		return errors.New("the fleet did not answer in time")
	}
}

// Bye is the reason the server gave for dropping this connection on purpose — a
// kick, remote being switched off, a refused attach — or "" for an ordinary
// disconnect. The runner prints it once the cockpit is off the screen, so a
// cockpit that vanishes always says why.
func (c *Client) Bye() string {
	s, _ := c.bye.Load().(string)
	return s
}

// settle reports the handshake outcome exactly once; later calls are no-ops.
func (c *Client) settle(err error) {
	c.settled.Do(func() { c.handshake <- err })
}

// Send writes a command to the server. It is safe for concurrent use: the
// cockpit's event loop and the zoom reader goroutine both send.
func (c *Client) Send(cmd proto.Command) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(proto.WriteTimeout))
	return c.enc.Encode(cmd)
}

// Endpoint is a short, human label for where this client is attached: "local"
// for a Unix-domain (same-host) server, or the host/IP for a future remote (TCP)
// server. It is what the cockpit shows in the footer.
func (c *Client) Endpoint() string {
	if c.endpoint != "" {
		return c.endpoint
	}
	nc, ok := c.conn.(net.Conn)
	if !ok {
		return "local"
	}
	addr := nc.RemoteAddr()
	if addr == nil || addr.Network() == "unix" {
		return "local"
	}
	if host, _, err := net.SplitHostPort(addr.String()); err == nil {
		return host
	}
	return addr.String()
}

// Close detaches from the server. The server keeps running.
func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) readLoop() {
	// Whatever ends this loop, the handshake must not be left hanging: a peer that
	// dies before answering is a failed attach, not an attach that never resolves.
	defer c.settle(errors.New("the fleet closed the connection before answering"))
	defer close(c.Events)
	defer close(c.Output)
	defer close(c.Stats)
	defer close(c.Telemetry)
	defer close(c.Config)
	defer close(c.Footer)
	defer close(c.Usage)
	defer close(c.Remote)
	dec := json.NewDecoder(c.conn)
	// The connection is persistent but may be legitimately idle, so liveness rides
	// on the server's heartbeat: set an idle read deadline up front and reset it on
	// every successful decode (any message, ping included, proves the peer alive).
	// When no message arrives within the window the Decode errors and we tear down.
	_ = c.conn.SetReadDeadline(time.Now().Add(time.Duration(readTimeout.Load())))
	for {
		var msg proto.ServerMsg
		if err := dec.Decode(&msg); err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(time.Duration(readTimeout.Load()))) // a message arrived; reset the idle timer
		if msg.Type == "welcome" {
			// Only the welcome settles the handshake, not merely "some message
			// arrived": a status push can be queued to this connection before its
			// hello has even been read, and settling on that would let a refused
			// remote attach open a cockpit that instantly dies.
			c.settle(nil)
		}
		switch msg.Type {
		case "goodbye":
			// The server is dropping us on purpose and said why. Record the reason for
			// the runner to print, resolve the handshake with it (so a refused remote
			// attach surfaces in the connection form), and pass it on so a cockpit
			// already on screen can show it before it goes.
			c.bye.Store(msg.Error)
			c.settle(fmt.Errorf("%s", msg.Error))
			c.Events <- msg
			return
		case "ping":
			// An ignorable keepalive: the successful decode above already reset the
			// read deadline, so a ping is a pure no-op. It must not reach Events.
		case "output":
			c.Output <- msg
		case "stats":
			// Host telemetry is latest-wins; drop a stale sample rather than let a
			// full buffer stall control messages.
			select {
			case c.Stats <- msg:
			default:
			}
		case "telemetry":
			// Panel telemetry is latest-wins too: a dropped refresh is corrected by
			// the next tick, and must never stall structural events.
			select {
			case c.Telemetry <- msg:
			default:
			}
		case "config":
			// Config/commands ride their own channel so they never interleave with the
			// structural panel snapshots a frontend counts.
			c.Config <- msg
		case "footer":
			// Latest-wins: a rapidly refreshed footer (a live counter) must never stall
			// structural events; the freshest value is the only one that matters.
			select {
			case c.Footer <- msg:
			default:
			}
		case "usage":
			// Latest-wins like the footer: only the freshest usage sample matters.
			select {
			case c.Usage <- msg:
			default:
			}
		case "remote":
			// Latest-wins: every message is the whole status, so a dropped one is
			// corrected by the next, and it must never stall structural events.
			select {
			case c.Remote <- msg:
			default:
			}
		case "tail":
			// A pulled panel tail (the inbox's detail pane). It is stated here rather
			// than left to the default below because it is the one message type that
			// LOOKS like bulk PTY data and is not: it is a low-rate request/response
			// control message, one in flight at a time, and it belongs on the same
			// channel as "diff", "search", and "gitout". Putting it on Output would
			// feed raw bytes to a zoom's emulator, which is exactly the attach the
			// inbox exists to avoid.
			c.Events <- msg
		default:
			c.Events <- msg
		}
	}
}
