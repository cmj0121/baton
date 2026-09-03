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
	dial    func() (*control.Client, error) // how a tool call reaches the socket; control.DialAsProcess by default
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
	// DialAsProcess rather than Dial: outside a panel this server is identified
	// as itself, so a fleet-wide rate cap tells it from the operator's own shell —
	// which, when an agent runtime was started from that shell, is the same
	// session. See control.DialAsProcess.
	s := &Server{version: version, dial: control.DialAsProcess}
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
			desc: "Spawn a panel and return its id. Give 'agent' to run an agent CLI (e.g. claude); omit it for a shell. Set 'worktree' with a 'branch' to spawn into a fresh git worktree of 'dir' instead — prefer that when the workers would otherwise share one checkout.",
			// worktree/branch are extra fields on this tool rather than a sibling
			// tool, so a conductor that already knows how to spawn does not have to
			// discover a second one. They also re-point 'dir': with worktree it names
			// the repository to branch from, not the directory the process runs in.
			schema: obj(map[string]any{
				"agent":    str("agent CLI command to run; omit for a shell panel"),
				"args":     strList("arguments passed to the agent command"),
				"dir":      str("working directory the panel runs in; with worktree, the repository to branch from"),
				"worktree": map[string]any{"type": "boolean", "description": "spawn into a fresh git worktree of dir; requires branch and agent"},
				"branch":   str("branch the new worktree is created on; required with worktree"),
			}),
			run: func(c *control.Client, a args) (string, error) {
				var id string
				var err error
				switch worktree := a.boolDefault("worktree", false); {
				case worktree:
					id, err = c.SpawnWorktree(a.str("agent"), a.strSlice("args"), a.str("dir"), a.str("branch"))
				case a.str("branch") != "":
					// A branch with no worktree is refused rather than dropped, the same as
					// `ctl spawn --branch` without `--worktree`. Silently ignoring it would
					// spawn into `dir` itself — for a conductor, the shared checkout this
					// whole verb exists to get its workers out of — and setting one field
					// of a pair is exactly the slip a model makes.
					return "", fmt.Errorf("branch names the worktree to spawn into; set worktree: true")
				default:
					id, err = c.SpawnPanel(a.str("agent"), a.strSlice("args"), a.str("dir"))
				}
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
			name: "baton_attention",
			desc: "Say that you need a human before you can go on, and why. Use this the moment you are blocked on a decision, an approval, or a credential — it puts your panel at the top of the human's queue with your reason on it, which no timer or output-sniffing guess can do. Omit 'id' to mean your own panel. Call baton_resolve once the need has passed.",
			schema: obj(map[string]any{
				"why": str("why you need a human — one sentence, shown to the person as-is"),
				"id":  str("panel id to raise the hand for; omit for your own panel"),
			}, "why"),
			run: func(c *control.Client, a args) (string, error) {
				why := a.str("why")
				if why == "" {
					return "", fmt.Errorf("why is required: a declaration is only worth more than a guess because it says why")
				}
				if err := c.DeclareAttention(a.str("id"), why); err != nil {
					return "", err
				}
				return "raised a hand: " + why, nil
			},
		},
		{
			name:   "baton_resolve",
			desc:   "Say the reason you needed a human has passed, so your panel leaves the queue without waiting to be noticed. Omit 'id' to mean your own panel. Safe to call when nothing is standing.",
			schema: obj(map[string]any{"id": str("panel id to stand down; omit for your own panel")}),
			run: func(c *control.Client, a args) (string, error) {
				if err := c.ResolveAttention(a.str("id")); err != nil {
					return "", err
				}
				return "stood down", nil
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
		{
			name:   "score_submit",
			desc:   "After a round of work, record one short observation about how this fleet behaves — a habit of its agents or its workflow, not a fact about the code. It lands at the lowest tier immediately and earns importance only by recurring, so submit freely and briefly.",
			schema: obj(map[string]any{"text": str("the observation to record — one short sentence")}, "text"),
			run: func(c *control.Client, a args) (string, error) {
				text := a.str("text")
				if text == "" {
					return "", fmt.Errorf("text is required")
				}
				id, folded, err := c.ScoreSubmit(text)
				if err != nil {
					return "", err
				}
				// A fold is worth saying out loud: it tells the agent the fleet
				// already knew this, which is the one thing that makes submitting
				// freely cheap rather than noisy.
				if folded {
					return "folded into " + id + ", which the fleet already remembers", nil
				}
				return "recorded as " + id, nil
			},
		},
		// The conductor's three corrections. The daemon refuses them to any
		// connection that is not the conductor panel's, so on an ordinary agent
		// panel these tools exist and answer with a refusal.
		//
		// They are listed unconditionally rather than filtered, and it is worth
		// saying that this is a CHOICE and not an inability: control.Dial resolves
		// the role from paths.EnvRole in this same process, so New could drop the
		// three from a table built outside a conductor panel. It is not worth the
		// branch — the .mcp.json that loads this server is written into the
		// conductor's workspace and nowhere else, so a non-conductor reaching the
		// table at all is already the unusual case — and one refusal a model can
		// read beats a tool that is silently absent.
		//
		// Each description says what the tool does AND what it is not, because
		// the boundary is the part an agent can talk itself across. Correcting the
		// memory is not executing it: the server records, folds, counts, ranks and
		// injects on its own, in Go, whether or not a conductor is running (#38 §1,
		// invariant I1). None of these three counts as anything, so none of them
		// can raise an entry — say so, or a conductor asked to "make sure the fleet
		// takes this seriously" will reach for reword.
		{
			name: "score_merge",
			desc: "Fleet memory upkeep: join two entries that say the same thing in different words, which baton's folding could not join for itself because it matches on text. Keep the better-worded entry and give this the id of the other; that one is retired and its wording is kept, so a later repeat of it still folds into the survivor. Nothing is counted and no entry is promoted — this tidies the memory, it does not decide what the memory is worth.",
			schema: obj(map[string]any{
				"id":   str("the entry to keep"),
				"from": str("the entry to fold into it; it is retired"),
			}, "id", "from"),
			run: func(c *control.Client, a args) (string, error) {
				id, from := a.str("id"), a.str("from")
				if id == "" || from == "" {
					return "", fmt.Errorf("id and from are both required: a merge names the entry to keep and the entry to fold into it")
				}
				if err := c.ScoreMerge(id, from); err != nil {
					return "", err
				}
				return "merged " + from + " into " + id, nil
			},
		},
		{
			name: "score_reword",
			desc: "Fleet memory upkeep: fix the wording of one entry — a typo, an ambiguity, a sentence that reads as a fact about a codebase rather than about how this fleet behaves. The old wording is kept, so repeats of it still fold into this entry. A reword counts as nothing at all: correcting a statement is not the fleet saying it again, so it cannot make an entry more important. Do not use it to emphasise an entry.",
			schema: obj(map[string]any{
				"id":   str("the entry to reword"),
				"text": str("the corrected wording — one short sentence"),
			}, "id", "text"),
			run: func(c *control.Client, a args) (string, error) {
				id, text := a.str("id"), a.str("text")
				if id == "" || text == "" {
					return "", fmt.Errorf("id and text are both required")
				}
				if err := c.ScoreReword(id, text); err != nil {
					return "", err
				}
				return "reworded " + id, nil
			},
		},
		{
			name:   "score_lower",
			desc:   "Fleet memory upkeep: pull one entry down a single rung when it was raised in error — a brief that coincidentally repeated it, or an observation that turned out to be about one repository rather than about this fleet. It moves DOWN only, one rung per call, and refuses at the bottom; there is no tool that raises an entry, because importance is earned by recurrence and the top rung is the operator's alone. Nothing is destroyed — the entry, its wording and its whole history stay — but a rung is not handed back by editing a file: the entry climbs again only by being said again. Lower an entry you are confident about, not one you are unsure of.",
			schema: obj(map[string]any{"id": str("the entry to pull down one rung")}, "id"),
			run: func(c *control.Client, a args) (string, error) {
				id := a.str("id")
				if id == "" {
					return "", fmt.Errorf("id is required")
				}
				if err := c.ScoreLower(id); err != nil {
					return "", err
				}
				return "lowered " + id + " by one rung", nil
			},
		},
	}
}
