// Package control is the agent-facing client of the socket: a thin, short-lived
// connection an external process (the conductor agent, a script, or a human at a
// shell) uses to drive the fleet. It speaks the same proto the cockpit does, so
// it grants no power the socket did not already expose — it just makes that power
// reachable from a command line or an MCP tool.
//
// A control connection is synchronous and one-shot in spirit: Dial, issue one or
// more commands, Close. Each mutating command is followed by a panel.list barrier
// so the call returns only once the server has processed it — the reply is either
// the server's error or the fleet snapshot that resulted.
package control

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
)

// ioTimeout bounds every read/write so a wedged server can never hang the CLI.
const ioTimeout = 5 * time.Second

// Client is a live control connection to the baton server.
type Client struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
}

// Dial connects to the session's server and identifies this client to it. The
// socket path comes from BATON_SOCK (else the session default); the role and
// self panel id come from the environment baton injects into a conductor panel,
// so a control client run inside the conductor is fenced by the server while the
// same binary run from a plain shell is the unscoped, full-power cockpit role.
func Dial() (*Client, error) {
	return DialSocket(paths.Socket(), os.Getenv(paths.EnvRole), os.Getenv(paths.EnvPanelID))
}

// DialSocket connects to the server at socket and says hello with role and self.
// An empty role is the unscoped cockpit role; a "conductor" role asks the server
// to fence the connection (see the server's guardConductor).
func DialSocket(socket, role, self string) (*Client, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connect to baton at %s: %w (is the server running?)", socket, err)
	}
	c := &Client{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}

	if err := c.send(proto.Command{Action: "hello", Role: role, Self: self}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// Drain the handshake up to and including the initial panels snapshot, so a
	// later command's reply is not confused with it.
	if err := c.readUntilPanels(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// Close drops the connection. The server keeps running.
func (c *Client) Close() error { return c.conn.Close() }

// List returns the current fleet snapshot.
func (c *Client) List() ([]proto.Panel, error) {
	return c.exchange(proto.Command{Action: "panel.list"})
}

// Do issues a command and waits for the server to process it. It returns the
// server's error if the command was rejected, and nil once it took effect.
func (c *Client) Do(cmd proto.Command) error {
	_, err := c.exchange(cmd)
	return err
}

// Spawn issues a panel.create and returns the new panel's id. The id is found by
// diffing the fleet before and after — robust to ordering and to the server not
// echoing the id — so the caller can immediately group, rename, or drive it.
func (c *Client) Spawn(cmd proto.Command) (string, error) {
	before, err := c.List()
	if err != nil {
		return "", err
	}
	seen := make(map[string]bool, len(before))
	for _, p := range before {
		seen[p.ID] = true
	}
	after, err := c.exchange(cmd)
	if err != nil {
		return "", err
	}
	for _, p := range after {
		if !seen[p.ID] {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("panel created but its id could not be determined")
}

// ListJSON returns the fleet as indented JSON — the shared presentation both the
// CLI (`baton ctl list`) and the MCP `baton_list` tool hand back.
func (c *Client) ListJSON() (string, error) {
	panels, err := c.List()
	if err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(panels, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SpawnPanel spawns a panel and returns its id: an agent panel running agent with
// args when agent is non-empty, otherwise a shell. dir is the working directory
// (empty falls back to the server default). It is the one place the agent-vs-shell
// spawn shape lives, shared by the CLI and the MCP tool.
func (c *Client) SpawnPanel(agent string, args []string, dir string) (string, error) {
	cmd := proto.Command{Action: "panel.create", Kind: proto.KindShell, Dir: dir}
	if agent != "" {
		cmd.Kind = proto.KindAgent
		cmd.Path = agent
		cmd.Args = args
	}
	return c.Spawn(cmd)
}

// SendText types text into panel id, appending a newline to submit it unless
// submit is false. It is the one place the submit-newline rule lives, shared by
// the CLI and the MCP tool.
func (c *Client) SendText(id, text string, submit bool) error {
	data := []byte(text)
	if submit {
		data = append(data, '\n')
	}
	return c.Do(proto.Command{Action: "panel.input", ID: id, Data: data})
}

// Dispatch assigns prompt to panel id as a task brief: the server records it on
// the panel and delivers it to the process as a unit. Unlike SendText (raw
// keystrokes), the brief reaches every frontend's card and the snapshot.
func (c *Client) Dispatch(id, prompt string) error {
	return c.Do(proto.Command{Action: "panel.dispatch", ID: id, Prompt: prompt})
}

// DispatchGroup fans prompt to every member of a work item — one task delivered
// to N agents, the mechanic behind racing them on the same prompt.
func (c *Client) DispatchGroup(group, prompt string) error {
	return c.Do(proto.Command{Action: "panel.dispatch-group", Group: group, Prompt: prompt})
}

// Enqueue adds a task to the backlog for the scheduler to drain onto a free agent
// in the given group (empty = any agent).
func (c *Client) Enqueue(prompt, group string) error {
	return c.Do(proto.Command{Action: "task.enqueue", Prompt: prompt, Group: group})
}

// CancelTask removes a queued backlog task by id.
func (c *Client) CancelTask(id string) error {
	return c.Do(proto.Command{Action: "task.cancel", ID: id})
}

// DrainQueue clears every queued backlog task.
func (c *Client) DrainQueue() error {
	return c.Do(proto.Command{Action: "task.drain"})
}

// Tasks returns the current backlog snapshot. Like exchange it trails the request
// with a config.get barrier, capturing the "tasks" reply before the barrier.
func (c *Client) Tasks() ([]proto.Task, error) {
	if err := c.send(proto.Command{Action: "task.list"}); err != nil {
		return nil, err
	}
	if err := c.send(proto.Command{Action: "config.get"}); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(ioTimeout)
	var tasks []proto.Task
	var firstErr error
	for {
		_ = c.conn.SetReadDeadline(deadline)
		var msg proto.ServerMsg
		if err := c.dec.Decode(&msg); err != nil {
			return nil, fmt.Errorf("read from baton: %w", err)
		}
		switch msg.Type {
		case "error":
			if firstErr == nil {
				firstErr = fmt.Errorf("%s", msg.Error)
			}
		case "tasks":
			tasks = msg.Tasks
		case "config":
			if firstErr != nil {
				return nil, firstErr
			}
			return tasks, nil
		}
	}
}

// TasksJSON returns the backlog as indented JSON, the shared presentation for
// `baton ctl queue list` and the MCP queue tool.
func (c *Client) TasksJSON() (string, error) {
	tasks, err := c.Tasks()
	if err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// exchange sends cmd followed by a config.get as a sync barrier, and reads up to
// and including the barrier's "config" reply — so the connection buffer is left
// clean for the next exchange. It returns the latest fleet snapshot seen before
// the barrier (the snapshot cmd produced, if it broadcast one; otherwise the
// fleet as it stood), or the server's error if cmd was rejected.
//
// The config.get barrier works because the server processes commands strictly in
// order: cmd's reply (an error, or a panels broadcast, or nothing) is always
// enqueued before the "config" that answers the trailing config.get. Draining
// through that "config" — even on the error path — guarantees no stray reply is
// left to desync the next exchange. config.get is chosen over a second panel.list
// precisely because its reply type ("config") is distinct from a command's panels
// broadcast, so the barrier is unambiguous.
func (c *Client) exchange(cmd proto.Command) ([]proto.Panel, error) {
	if err := c.send(cmd); err != nil {
		return nil, err
	}
	if err := c.send(proto.Command{Action: "config.get"}); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(ioTimeout)
	var latest []proto.Panel
	var firstErr error
	for {
		_ = c.conn.SetReadDeadline(deadline)
		var msg proto.ServerMsg
		if err := c.dec.Decode(&msg); err != nil {
			return nil, fmt.Errorf("read from baton: %w", err)
		}
		switch msg.Type {
		case "error":
			if firstErr == nil {
				firstErr = fmt.Errorf("%s", msg.Error)
			}
		case "panels":
			latest = msg.Panels
		case "config":
			// Barrier reached: cmd is fully processed and its reply already read.
			if firstErr != nil {
				return nil, firstErr
			}
			return latest, nil
		default:
			// welcome, stats, telemetry, ping, footer, output: ignore and keep reading.
		}
	}
}

// readUntilPanels drains the connect handshake up to and including the initial
// fleet snapshot (welcome precedes it), bounded by a read deadline. The seed
// stats that follow are harmlessly skipped by the next exchange.
func (c *Client) readUntilPanels() error {
	deadline := time.Now().Add(ioTimeout)
	for {
		_ = c.conn.SetReadDeadline(deadline)
		var msg proto.ServerMsg
		if err := c.dec.Decode(&msg); err != nil {
			return fmt.Errorf("read from baton: %w", err)
		}
		if msg.Type == "panels" {
			return nil
		}
	}
}

func (c *Client) send(cmd proto.Command) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(ioTimeout))
	if err := c.enc.Encode(cmd); err != nil {
		return fmt.Errorf("send to baton: %w", err)
	}
	return nil
}
