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
	"strconv"
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
// socket path comes from BATON_SOCK (else the session default); the self panel id
// comes from the environment baton injects into every agent panel, so an agent
// that runs `baton ctl` inside its own panel can speak about itself without ever
// being told which panel that is. The role comes from the same environment but is
// injected into the conductor alone, so a control client inside the conductor is
// fenced by the server, while an ordinary agent panel — and the same binary run
// from a plain shell — declares the unscoped, full-power cockpit role.
func Dial() (*Client, error) {
	return dial(sessionActor)
}

// DialAsProcess is Dial for a client that OUTLIVES its connections. `baton mcp`
// is one long-lived process serving many tool calls, each over a fresh dial, so
// what it should be identified as outside a panel is ITSELF — not the session it
// happens to have been started in, which is usually the operator's own terminal
// and would put the two of them back in one rate-cap slot.
//
// Every other client is a process per command and has no such identity to offer;
// see sessionActor for what they declare instead.
func DialAsProcess() (*Client, error) {
	return dial(processActor)
}

// dial is Dial and DialAsProcess with the identity rule as the only difference.
// The socket comes from BATON_SOCK (else the session default) and the role and
// panel from the environment baton injects; see Dial.
func dial(identify func() string) (*Client, error) {
	return DialSocket(paths.Socket(), os.Getenv(paths.EnvRole), os.Getenv(paths.EnvPanelID), identify())
}

// DialSocket connects to the server at socket and says hello with role, self and
// actor. An empty role is the unscoped cockpit role; a "conductor" role asks the
// server to fence the connection (see the server's guardConductor).
//
// IT TAKES THE ACTOR rather than choosing one, and that is the whole difference
// between it and Dial. There are two identity rules and they are not
// interchangeable — a per-command client declares its session, a client that
// outlives its connections declares itself — so an exported dial that silently
// applied one of them was a third policy site with no way to ask for the other.
// The two rules live on Dial and DialAsProcess, which is where a caller reading
// proto.Command.Actor is sent.
//
// A client that HAS a panel declares no actor whatever it passed, and that is
// enforced here rather than in each rule: a panel id is already an identity, and
// a second one beside it is just another slot the same client could spend from.
func DialSocket(socket, role, self, actor string) (*Client, error) {
	if self != "" {
		actor = ""
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connect to baton at %s: %w (is the server running?)", socket, err)
	}
	c := &Client{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}

	if err := c.send(proto.Command{Action: "hello", Role: role, Self: self, Actor: actor}); err != nil {
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

// sessionActor is what a PER-COMMAND client calls itself when it has no panel to
// be: its session. `baton ctl` is a whole process per command, so nothing about
// the process itself survives two turns of a loop — the identity has to be
// something outside it. The parent will not do: `out=$(baton ctl …)` forks a
// subshell, so a loop written that way would hand out a fresh identity per
// iteration and walk straight through the cap. Every process a shell spawns,
// subshells included, shares that shell's session; two terminals are two
// sessions, which is two operators or two scripts and is the split worth having.
//
// WHAT IT COVERS, and it is narrower than "outside a panel". A session is stable
// for a shell and everything under it — the shape the cap was measured failing
// on — and it is NOT stable under a launcher that starts each command in a
// session of its own. cron, `ssh host baton ctl …`, systemd-run, `docker exec
// -t` and agent runtimes all setsid(2) per invocation, which hands one actor a
// fresh sid every time, exactly as the parent would have. Reproduced: two
// invocations under one such runtime reported sid 7019 and then 7074.
//
// Who that actually leaves is a smaller set than it sounds. An AGENT panel never
// reaches here at all: the daemon injects BATON_PANEL_ID into its environment
// (server.panelEnv), so `baton ctl score submit` from its shell tool declares
// that panel id as self and returns empty below. `baton mcp` does not reach here
// either — it is long-lived and declares pid: (processActor). What is left is a
// SHELL panel, which carries no identity env by deliberate choice (see the
// argument on the server's spawn path), and a client one of those launchers
// started.
//
// For those, a per-invocation session is the self-rotation this identity is
// already documented as accepting: nothing here grants anything, so an actor
// that varies only picks a different rate-cap slot, and a client that wanted to
// could vary Self just as easily. There is no stable identity to reach for
// instead, and naming one that does not exist would be worse than saying so.
//
// Empty when the session cannot be read, which puts this client back in the
// shared slot — the safe direction, since a cap that over-groups refuses more
// rather than less. A client that HAS a panel declares nothing either, and that
// rule is DialSocket's rather than this function's: it holds for the actor an
// outside caller supplies as much as for the one computed here.
//
// The prefix keeps both forms out of the panel ids the same caps are keyed on,
// which are bare numbers.
//
// The body is per-OS: see session_unix.go and session_other.go.
// processActor is sessionActor for a client that outlives its connections: the
// process itself, which is exactly the thing a per-command client does not have.
// See DialAsProcess.
func processActor() string {
	return "pid:" + strconv.Itoa(os.Getpid())
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
//
// agent is a binary, not a profile name, so these spawns carry no profile and
// resolve to the fleet-wide resource limits alone. That is deliberate: this path
// is what the conductor drives, and an agent must not be able to name its way
// into a profile whose caps are wider than the fleet's.
func (c *Client) SpawnPanel(agent string, args []string, dir string) (string, error) {
	cmd := proto.Command{Action: "panel.create", Kind: proto.KindShell, Dir: dir}
	if agent != "" {
		cmd.Kind = proto.KindAgent
		cmd.Path = agent
		cmd.Args = args
	}
	return c.Spawn(cmd)
}

// SpawnWorktree branches repo at branch, opens a git worktree for it, and starts
// agent in THAT tree, filed under the branch as a work item. It returns the new
// panel's id, like SpawnPanel.
//
// repo is the REPOSITORY, not the directory the process runs in — the workdir is
// the tree the server makes. That inversion is why this is a second method rather
// than a flag on SpawnPanel: the same argument names two different places in the
// two calls, and a reader of one call site should not have to know which.
//
// The wire op is panel.git worktree-add in its targetless form — the one the
// dashboard's `n w` already sends — rather than panel.create with an extra field.
// panel.create's Dir means the workdir, so an older daemon handed a repo there
// would ignore the unknown flag and spawn straight INTO the repo: a misread, and
// the exact thing a worktree spawn exists to prevent. An older daemon handed this
// command finds an empty id and answers `no panel with id ""`, which is a refusal.
//
// All three arguments are required, and refused HERE, before the socket is dialled
// and long before git runs. agent is required because there is no default to fall
// back on: the server holds no agent commands, each client resolves what to run
// (the dashboard resolves the fleet default and refuses when it cannot), and an
// empty command would build the tree and then fail to fill it.
func (c *Client) SpawnWorktree(agent string, args []string, repo, branch string) (string, error) {
	switch {
	case repo == "":
		return "", fmt.Errorf("a worktree spawn needs the repository to branch from")
	case branch == "":
		return "", fmt.Errorf("a worktree spawn needs a branch name")
	case agent == "":
		return "", fmt.Errorf("a worktree spawn needs an agent command to run in the tree")
	}
	return c.Spawn(proto.Command{
		Action: "panel.git", Git: "worktree-add",
		Dir: repo, Name: branch,
		Path: agent, Args: args,
	})
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

// DeclareAttention raises a standing declaration on panel id: the agent saying,
// in its own words, that it needs a human before it can go on. It is the top of
// the server's detection precedence — above the quiet timer, above the tail
// heuristic — because it is the only signal that came from the panel itself
// rather than from baton guessing at it from the outside.
//
// An empty id means "this connection's own panel", the id declared on hello from
// the environment baton injects. That is the form an agent uses: it needs no
// argument and no way to learn its own id.
//
// reason is required by the server, and its whole point: a declaration displaces
// two guesses precisely because it can say why, and the person reading the queue
// sees that sentence rather than a scraped terminal line.
func (c *Client) DeclareAttention(id, reason string) error {
	return c.Do(proto.Command{Action: "panel.attention", ID: id, Reason: reason})
}

// ResolveAttention withdraws the declaration standing on panel id — the agent
// saying the need has passed — and hands the panel's state back to the lifecycle
// ladder. It is a no-op rather than an error when no declaration stands, so an
// agent can tidy up after itself without first checking whether its hand is
// still up. An empty id means this connection's own panel, as with
// DeclareAttention.
func (c *Client) ResolveAttention(id string) error {
	return c.Do(proto.Command{Action: "panel.resolve", ID: id})
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

// EnqueueSpawn adds a spawn-on-demand task: when no agent is free, the scheduler
// provisions one running command (with args, in dir) and dispatches the task there,
// closing that agent on done when closeOnDone is set.
func (c *Client) EnqueueSpawn(prompt, group, command string, args []string, dir string, closeOnDone bool) error {
	return c.Do(proto.Command{
		Action: "task.enqueue", Prompt: prompt, Group: group,
		Path: command, Args: args, Dir: dir, Ephemeral: closeOnDone,
	})
}

// CancelTask removes a queued backlog task by id.
func (c *Client) CancelTask(id string) error {
	return c.Do(proto.Command{Action: "task.cancel", ID: id})
}

// PromoteTask bumps a queued task to the head of the backlog, so the scheduler
// drains it next.
func (c *Client) PromoteTask(id string) error {
	return c.Do(proto.Command{Action: "task.promote", ID: id})
}

// DemoteTask drops a queued task to the tail of the backlog, so it drains last.
func (c *Client) DemoteTask(id string) error {
	return c.Do(proto.Command{Action: "task.demote", ID: id})
}

// DrainQueue clears every queued backlog task.
func (c *Client) DrainQueue() error {
	return c.Do(proto.Command{Action: "task.drain"})
}

// ResetConductor deletes the conductor's workspace, so the next conductor opens
// into an empty one. The server refuses while a conductor still exists in the
// fleet — close it first.
func (c *Client) ResetConductor() error {
	return c.Do(proto.Command{Action: "conductor.reset"})
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

// scoreExchange issues one score.* command and returns the raw payload of the
// "score" reply it produces. Like Tasks it trails the request with a config.get
// barrier and drains up to the "config" answer, tolerating any interleaved
// pushes ("panels", "stats", …) the server broadcasts in between — a score reply
// is a new message type, but the connection it arrives on is as chatty as ever.
func (c *Client) scoreExchange(cmd proto.Command) (json.RawMessage, error) {
	if err := c.send(cmd); err != nil {
		return nil, err
	}
	if err := c.send(proto.Command{Action: "config.get"}); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(ioTimeout)
	var payload json.RawMessage
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
		case "score":
			payload = msg.Score
		case "config":
			if firstErr != nil {
				return nil, firstErr
			}
			return payload, nil
		}
	}
}

// ScoreSubmit records text as a fleet-memory note (score.submit) and returns the
// id of the entry it landed in, and whether it FOLDED into one the store already
// held rather than starting a new one. Those two are the whole of what the
// caller can act on — #38's "new or folded into id" — extracted here so the CLI
// and the MCP tool share the presentation.
func (c *Client) ScoreSubmit(text string) (id string, folded bool, err error) {
	payload, err := c.scoreExchange(proto.Command{Action: "score.submit", Prompt: text})
	if err != nil {
		return "", false, err
	}
	var out struct {
		Id     string `json:"id"`
		Folded bool   `json:"folded"`
	}
	if err := json.Unmarshal(payload, &out); err != nil || out.Id == "" {
		return "", false, fmt.Errorf("malformed score.submit reply: %s", payload)
	}
	return out.Id, out.Folded, nil
}

// ScoreMerge folds entry from into entry id — the first of the conductor's
// three corrections to the fleet memory (#38 §1), beside ScoreReword and
// ScoreLower. The absorbed entry is retired and its wording is kept on the
// survivor, so a later repeat of it still folds.
//
// The daemon refuses all three to any connection that is not the conductor
// panel's, so a cockpit calling one gets an error rather than an effect.
//
// Three methods rather than one with an operation string, because the three take
// three different arguments and the mapping onto the wire is one line each; a
// shared verb would only have moved the switch here. Each returns the daemon's
// refusal and discards the reply payload, which names the entry the caller
// already named — the MCP tools are the presentation.
func (c *Client) ScoreMerge(id, from string) error {
	_, err := c.scoreExchange(proto.Command{Action: "score.merge", ID: id, From: from})
	return err
}

// ScoreReword replaces the wording of entry id. The prior wording is kept as an
// alias by the store, so a later repeat of it still folds into the same entry.
func (c *Client) ScoreReword(id, text string) error {
	_, err := c.scoreExchange(proto.Command{Action: "score.reword", ID: id, Prompt: text})
	return err
}

// ScoreLower pulls entry id down one rung. There is no target tier on the wire
// because there is none in the store: the verb steps down, and only down.
func (c *Client) ScoreLower(id string) error {
	_, err := c.scoreExchange(proto.Command{Action: "score.lower", ID: id})
	return err
}

// ScoreList returns every recorded fleet-memory entry as the server's raw JSON,
// each carrying the rank and the factor breakdown that placed it, whether the
// working set took it and — when it did not — which cap left it out, beside the
// context they were ranked against. The shared presentation for
// `baton ctl score list`.
//
// panel names the panel to rank for: its directory, profile and group are what
// the context dimensions are matched against, which is what makes the reply an
// answer about the brief THAT panel gets. Empty ranks against no context at all
// — the cockpit's own view, where every context factor is 1.0 — and the echoed
// context is what tells the two apart. A panel the fleet does not have is an
// error, never a silent fallback to the contextless answer.
func (c *Client) ScoreList(panel string) (string, error) {
	payload, err := c.scoreExchange(proto.Command{Action: "score.list", ID: panel})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// ScoreStatus returns the fleet-memory status object (enabled, entries, the
// tuning in force, dir) as the server's raw JSON — the shared presentation for
// `baton ctl score status`.
func (c *Client) ScoreStatus() (string, error) {
	payload, err := c.scoreExchange(proto.Command{Action: "score.status"})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (c *Client) send(cmd proto.Command) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(ioTimeout))
	if err := c.enc.Encode(cmd); err != nil {
		return fmt.Errorf("send to baton: %w", err)
	}
	return nil
}
