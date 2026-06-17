// Package client is the frontend side of the socket: it dials the server,
// performs the handshake, and exposes a stream of server events.
package client

import (
	"encoding/json"
	"net"
	"sync"

	"github.com/cmj0121/baton/internal/proto"
)

// Client is a live attachment to the baton server.
type Client struct {
	conn net.Conn

	sendMu sync.Mutex // serialises Send; the zoom reader and the UI both write
	enc    *json.Encoder

	// Events delivers control messages; Output delivers PTY data from a zoomed
	// panel. Splitting them keeps a burst of output from starving the cockpit.
	// Both are closed on disconnect.
	Events chan proto.ServerMsg
	Output chan proto.ServerMsg
}

// Dial connects to the server at socket, says hello, and starts reading events.
func Dial(socket string) (*Client, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, err
	}

	c := &Client{
		conn:   conn,
		enc:    json.NewEncoder(conn),
		Events: make(chan proto.ServerMsg, proto.EventBufferSize),
		Output: make(chan proto.ServerMsg, proto.EventBufferSize),
	}
	go c.readLoop()

	if err := c.Send(proto.Command{Action: "hello"}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// Send writes a command to the server. It is safe for concurrent use: the
// cockpit's event loop and the zoom reader goroutine both send.
func (c *Client) Send(cmd proto.Command) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
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
	dec := json.NewDecoder(c.conn)
	for {
		var msg proto.ServerMsg
		if err := dec.Decode(&msg); err != nil {
			return
		}
		if msg.Type == "output" {
			c.Output <- msg
		} else {
			c.Events <- msg
		}
	}
}
