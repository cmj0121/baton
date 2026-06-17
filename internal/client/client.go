// Package client is the frontend side of the socket: it dials the server,
// performs the handshake, and exposes a stream of server events.
package client

import (
	"encoding/json"
	"net"

	"github.com/cmj0121/baton/internal/proto"
)

// Client is a live attachment to the baton server.
type Client struct {
	conn net.Conn
	enc  *json.Encoder

	// Events delivers messages pushed by the server; it is closed on disconnect.
	Events chan proto.ServerMsg
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
	}
	go c.readLoop()

	if err := c.Send(proto.Command{Action: "hello"}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// Send writes a command to the server.
func (c *Client) Send(cmd proto.Command) error {
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
	dec := json.NewDecoder(c.conn)
	for {
		var msg proto.ServerMsg
		if err := dec.Decode(&msg); err != nil {
			return
		}
		c.Events <- msg
	}
}
