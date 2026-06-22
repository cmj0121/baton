// Package client is the frontend side of the socket: it dials the server,
// performs the handshake, and exposes a stream of server events.
package client

import (
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// readTimeout is the client's idle read deadline, reset on every successfully
// decoded message. It defaults to proto.ClientReadTimeout; tests override it to a
// few milliseconds so readLoop's timeout path runs fast. Set before Dial.
var readTimeout = proto.ClientReadTimeout

// Client is a live attachment to the baton server.
type Client struct {
	conn net.Conn

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
}

// Dial connects to the server at socket, says hello, and starts reading events.
func Dial(socket string) (*Client, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, err
	}

	c := &Client{
		conn:      conn,
		enc:       json.NewEncoder(conn),
		Events:    make(chan proto.ServerMsg, proto.EventBufferSize),
		Output:    make(chan proto.ServerMsg, proto.EventBufferSize),
		Stats:     make(chan proto.ServerMsg, proto.EventBufferSize),
		Telemetry: make(chan proto.ServerMsg, proto.EventBufferSize),
		Config:    make(chan proto.ServerMsg, proto.EventBufferSize),
		Footer:    make(chan proto.ServerMsg, proto.EventBufferSize),
	}
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
	addr := c.conn.RemoteAddr()
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
	defer close(c.Events)
	defer close(c.Output)
	defer close(c.Stats)
	defer close(c.Telemetry)
	defer close(c.Config)
	defer close(c.Footer)
	dec := json.NewDecoder(c.conn)
	// The connection is persistent but may be legitimately idle, so liveness rides
	// on the server's heartbeat: set an idle read deadline up front and reset it on
	// every successful decode (any message, ping included, proves the peer alive).
	// When no message arrives within the window the Decode errors and we tear down.
	_ = c.conn.SetReadDeadline(time.Now().Add(readTimeout))
	for {
		var msg proto.ServerMsg
		if err := dec.Decode(&msg); err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(readTimeout)) // a message arrived; reset the idle timer
		switch msg.Type {
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
		default:
			c.Events <- msg
		}
	}
}
