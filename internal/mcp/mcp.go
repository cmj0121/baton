// Package mcp is a minimal Model Context Protocol server over stdio: it exposes
// baton's fleet-control verbs as MCP tools so an MCP-speaking agent (a Claude
// conductor) drives the fleet through structured, discoverable tool calls instead
// of shelling out to `baton ctl`. Every tool is a thin wrapper over the same
// internal/control client the CLI uses, so it grants no power the socket did not
// already expose — and inside a conductor panel it inherits the injected env, so
// the server fences it under the conductor role.
//
// The transport is the MCP stdio transport: newline-delimited JSON-RPC 2.0 on
// stdin/stdout. Only protocol messages go to stdout; logs (if any) go to stderr,
// so the stream stays clean.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
)

// protocolVersion is the MCP revision baton implements; it is echoed back to a
// client that requests a version, and used as the default otherwise.
const protocolVersion = "2024-11-05"

// Server is an MCP stdio server bound to a set of fleet-control tools.
type Server struct {
	version string                          // baton's build version, reported in serverInfo
	dial    func() (*control.Client, error) // how a tool call reaches the socket; control.Dial by default
	tools   []tool
}

// tool is one MCP tool: its name, one-line description, JSON-Schema input shape,
// and the handler that runs it against a live control connection.
type tool struct {
	name   string
	desc   string
	schema map[string]any
	run    func(c *control.Client, a args) (string, error)
}

// New builds a server reporting version, dialing the session socket per tool call
// (so a dropped connection never wedges the long-lived server, and the per-call
// hello re-reads the injected conductor identity each time).
func New(version string) *Server {
	s := &Server{version: version, dial: control.Dial}
	s.tools = defaultTools()
	return s
}

// Serve runs the JSON-RPC loop until in reaches EOF. Requests get a response;
// notifications (no id) are handled for their side effects and answered with
// nothing, per JSON-RPC.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	// Frame the stream as newline-delimited JSON-RPC (the MCP stdio transport), so a
	// single malformed line is isolated to its own frame. A json.Decoder cannot
	// resync after a syntax error mid-stream, so one bad byte from a misbehaving
	// client — or a truncated write — would otherwise tear down the whole server and
	// silently drop every later tool call, stranding the conductor with no recovery.
	r := bufio.NewReader(in)
	enc := json.NewEncoder(out)
	for {
		line, err := r.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			if resp, reply := s.handleLine(trimmed); reply {
				if encErr := enc.Encode(resp); encErr != nil {
					return encErr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// handleLine parses one framed message and dispatches it. A frame that is not
// valid JSON is answered with a JSON-RPC parse error (null id, since the id
// cannot be recovered from unparseable bytes) and the loop continues, so one bad
// frame never stops the server.
func (s *Server) handleLine(line []byte) (rpcResponse, bool) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: -32700, Message: "parse error"},
		}, true
	}
	return s.handle(req)
}

// handle dispatches one message. The second return is false for a notification,
// which carries no id and must not be answered.
func (s *Server) handle(req rpcRequest) (rpcResponse, bool) {
	if len(req.ID) == 0 {
		return rpcResponse{}, false // a notification (e.g. notifications/initialized)
	}
	switch req.Method {
	case "initialize":
		return ok(req.ID, s.initializeResult(req.Params)), true
	case "tools/list":
		return ok(req.ID, map[string]any{"tools": s.toolList()}), true
	case "tools/call":
		return ok(req.ID, s.callTool(req.Params)), true
	case "ping":
		return ok(req.ID, map[string]any{}), true
	default:
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}, true
	}
}

// initializeResult answers the handshake. It echoes the client's requested
// protocol version when given (so negotiation never fails on a version baton
// would also accept), advertises the tools capability, and names the server.
func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	version := protocolVersion
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
			version = p.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "baton", "version": s.version},
	}
}

// toolList renders the tool definitions for tools/list.
func (s *Server) toolList() []map[string]any {
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, map[string]any{
			"name":        t.name,
			"description": t.desc,
			"inputSchema": t.schema,
		})
	}
	return out
}

// callTool runs the named tool. A tool failure (bad args, server rejection,
// socket down) comes back as an MCP tool result with isError set, so the model
// sees it and can adjust — only a malformed request is a JSON-RPC-level error.
func (s *Server) callTool(params json.RawMessage) map[string]any {
	var call struct {
		Name      string `json:"name"`
		Arguments args   `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return errorResult(fmt.Sprintf("invalid tool call: %v", err))
	}
	t, ok := s.lookup(call.Name)
	if !ok {
		return errorResult(fmt.Sprintf("unknown tool: %s", call.Name))
	}

	c, err := s.dial()
	if err != nil {
		return errorResult(err.Error())
	}
	defer func() { _ = c.Close() }()

	text, err := t.run(c, call.Arguments)
	if err != nil {
		return errorResult(err.Error())
	}
	return textResult(text)
}

func (s *Server) lookup(name string) (tool, bool) {
	for _, t := range s.tools {
		if t.name == name {
			return t, true
		}
	}
	return tool{}, false
}

// defaultTools is the fleet-control tool set, mirroring `baton ctl`.
func defaultTools() []tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	strList := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}

	return []tool{
		{
			name:   "baton_list",
			desc:   "List the fleet: every panel with its id, title, state, group, and whether it is the conductor.",
			schema: obj(map[string]any{}),
			run: func(c *control.Client, _ args) (string, error) {
				return c.ListJSON()
			},
		},
		{
			name: "baton_spawn",
			desc: "Spawn a panel and return its id. Give 'agent' to run an agent CLI (e.g. claude); omit it for a shell.",
			schema: obj(map[string]any{
				"agent": str("agent CLI command to run; omit for a shell panel"),
				"args":  strList("arguments passed to the agent command"),
				"dir":   str("working directory the panel runs in"),
			}),
			run: func(c *control.Client, a args) (string, error) {
				id, err := c.SpawnPanel(a.str("agent"), a.strSlice("args"), a.str("dir"))
				if err != nil {
					return "", err
				}
				return "spawned panel " + id, nil
			},
		},
		{
			name: "baton_send",
			desc: "Type text into a panel — a prompt for an agent, a command for a shell. Submits with a newline unless submit is false.",
			schema: obj(map[string]any{
				"id":     str("target panel id"),
				"text":   str("text to type into the panel"),
				"submit": map[string]any{"type": "boolean", "description": "append a newline to submit (default true)"},
			}, "id", "text"),
			run: func(c *control.Client, a args) (string, error) {
				id := a.str("id")
				if id == "" {
					return "", fmt.Errorf("id is required")
				}
				if err := c.SendText(id, a.str("text"), a.boolDefault("submit", true)); err != nil {
					return "", err
				}
				return "sent to panel " + id, nil
			},
		},
		{
			name: "baton_dispatch",
			desc: "Assign a task to a panel: record the brief and deliver the prompt to the agent as a unit. Prefer this over baton_send for handing an agent work — the brief shows on its card and survives a restart.",
			schema: obj(map[string]any{
				"id":     str("target panel id"),
				"prompt": str("the task brief to assign and deliver"),
			}, "id", "prompt"),
			run: func(c *control.Client, a args) (string, error) {
				id := a.str("id")
				if id == "" {
					return "", fmt.Errorf("id is required")
				}
				if err := c.Dispatch(id, a.str("prompt")); err != nil {
					return "", err
				}
				return "dispatched to panel " + id, nil
			},
		},
		{
			name: "baton_enqueue",
			desc: "Add a task to the backlog. The scheduler drains it onto a free idle agent (in the given work item, if any) — use this to hand off work without picking a panel yourself. Give 'command' to make it spawn-on-demand: when no agent is free the scheduler provisions one running that command, and 'close' reaps it once the task is done — a way to burst a fresh worker fleet through the backlog.",
			schema: obj(map[string]any{
				"prompt":  str("the task brief to enqueue"),
				"group":   str("restrict the task to agents in this work item (optional)"),
				"command": str("spawn-on-demand: agent command to provision when none is free (optional)"),
				"args":    strList("arguments for the spawned command (optional)"),
				"dir":     str("working directory for the spawned agent (optional)"),
				"close":   map[string]any{"type": "boolean", "description": "close the spawned agent once the task finishes (optional)"},
			}, "prompt"),
			run: func(c *control.Client, a args) (string, error) {
				if cmd := a.str("command"); cmd != "" {
					if err := c.EnqueueSpawn(a.str("prompt"), a.str("group"), cmd, a.strSlice("args"), a.str("dir"), a.boolDefault("close", false)); err != nil {
						return "", err
					}
					return "enqueued (spawn-on-demand)", nil
				}
				if err := c.Enqueue(a.str("prompt"), a.str("group")); err != nil {
					return "", err
				}
				return "enqueued", nil
			},
		},
		{
			name:   "baton_queue",
			desc:   "List the task backlog: every task with its id, prompt, status, panel, and group.",
			schema: obj(map[string]any{}),
			run: func(c *control.Client, _ args) (string, error) {
				return c.TasksJSON()
			},
		},
		{
			name: "baton_reorder",
			desc: "Reorder a queued task in the backlog: 'head' drains it next, 'tail' drains it last. Only a task still waiting (not yet on an agent) can be reordered.",
			schema: obj(map[string]any{
				"id": str("queued task id to move"),
				"to": str("where to move it: 'head' or 'tail'"),
			}, "id", "to"),
			run: func(c *control.Client, a args) (string, error) {
				id, to := a.str("id"), a.str("to")
				if id == "" {
					return "", fmt.Errorf("id is required")
				}
				switch to {
				case "head":
					if err := c.PromoteTask(id); err != nil {
						return "", err
					}
					return "promoted " + id + " to the head", nil
				case "tail":
					if err := c.DemoteTask(id); err != nil {
						return "", err
					}
					return "demoted " + id + " to the tail", nil
				default:
					return "", fmt.Errorf("to must be 'head' or 'tail'")
				}
			},
		},
		{
			name: "baton_dispatch_group",
			desc: "Fan one task to every member of a work item — the way to race N agents on the same prompt. Group them first with baton_group.",
			schema: obj(map[string]any{
				"group":  str("the work-item name whose members receive the task"),
				"prompt": str("the task brief to dispatch to every member"),
			}, "group", "prompt"),
			run: func(c *control.Client, a args) (string, error) {
				group := a.str("group")
				if group == "" {
					return "", fmt.Errorf("group is required")
				}
				if err := c.DispatchGroup(group, a.str("prompt")); err != nil {
					return "", err
				}
				return "dispatched to group " + group, nil
			},
		},
		{
			name: "baton_group",
			desc: "File panels under a work-item name, grouping them in the dashboard and split view.",
			schema: obj(map[string]any{
				"name": str("work-item name"),
				"ids":  strList("panel ids to group"),
			}, "name", "ids"),
			run: func(c *control.Client, a args) (string, error) {
				if err := c.Do(proto.Command{Action: "panel.group", Group: a.str("name"), IDs: a.strSlice("ids")}); err != nil {
					return "", err
				}
				return "grouped under " + a.str("name"), nil
			},
		},
		{
			name: "baton_rename",
			desc: "Rename a panel (give id) or a group (give group). 'name' is the new name.",
			schema: obj(map[string]any{
				"id":    str("panel id to rename"),
				"group": str("existing group name to rename"),
				"name":  str("the new name"),
			}, "name"),
			run: func(c *control.Client, a args) (string, error) {
				if err := c.Do(proto.Command{Action: "panel.rename", ID: a.str("id"), Group: a.str("group"), Name: a.str("name")}); err != nil {
					return "", err
				}
				return "renamed to " + a.str("name"), nil
			},
		},
		{
			name:   "baton_pin",
			desc:   "Pin panels to live tiles in their group split.",
			schema: obj(map[string]any{"ids": strList("panel ids to pin")}, "ids"),
			run: func(c *control.Client, a args) (string, error) {
				if err := c.Do(proto.Command{Action: "panel.pin", IDs: a.strSlice("ids")}); err != nil {
					return "", err
				}
				return "pinned", nil
			},
		},
		{
			name:   "baton_unpin",
			desc:   "Unpin panels.",
			schema: obj(map[string]any{"ids": strList("panel ids to unpin")}, "ids"),
			run: func(c *control.Client, a args) (string, error) {
				if err := c.Do(proto.Command{Action: "panel.unpin", IDs: a.strSlice("ids")}); err != nil {
					return "", err
				}
				return "unpinned", nil
			},
		},
		{
			name: "baton_signal",
			desc: "Send a signal (e.g. SIGINT) to panels.",
			schema: obj(map[string]any{
				"signal": str("signal name or number, e.g. SIGINT or 2"),
				"ids":    strList("panel ids to signal"),
			}, "signal", "ids"),
			run: func(c *control.Client, a args) (string, error) {
				if err := c.Do(proto.Command{Action: "panel.signal", Signal: a.str("signal"), IDs: a.strSlice("ids")}); err != nil {
					return "", err
				}
				return "signalled " + a.str("signal"), nil
			},
		},
		{
			name:   "baton_close",
			desc:   "Close panels by id.",
			schema: obj(map[string]any{"ids": strList("panel ids to close")}, "ids"),
			run: func(c *control.Client, a args) (string, error) {
				if err := c.Do(proto.Command{Action: "panel.close", IDs: a.strSlice("ids")}); err != nil {
					return "", err
				}
				return "closed", nil
			},
		},
	}
}
