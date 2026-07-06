// Package proto defines the semantic, versioned wire protocol spoken between
// baton frontends (clients) and the baton server over a Unix domain socket.
//
// Framing is newline-delimited JSON: clients send Command values up, the server
// sends ServerMsg values down. This is the only formal entry into the core.
package proto

import (
	"encoding/json"
	"time"
)

// ProtocolVersion is negotiated on connect. Bump it on breaking wire changes.
const ProtocolVersion = "baton/1"

// IPC timing for the persistent, legitimately-idle Unix-socket connection. The
// steady-state command loop carries NO read deadline (a client may attach and
// send nothing for minutes); liveness is kept instead by a server→client
// heartbeat ping and an idle read deadline the client resets on any message.
const (
	// HeartbeatInterval is the server→client ping cadence. The server emits a
	// keepalive ping this often so an idle client's read deadline keeps resetting.
	HeartbeatInterval = 15 * time.Second

	// ClientReadTimeout is the client's idle read deadline, reset on every message
	// (incl. ping). It is ≥ 3× HeartbeatInterval so a single dropped ping never
	// disconnects a healthy client — only a genuinely dead peer trips it.
	ClientReadTimeout = 45 * time.Second

	// WriteTimeout is the per-encode write deadline on either side. It is generous
	// enough that a legitimate burst draining a full EventBufferSize buffer does not
	// trip it; it exists to tear down a peer that has stopped reading.
	WriteTimeout = 10 * time.Second

	// HandshakeTimeout is the server's read deadline for the initial hello only.
	// Once the first command is read the deadline is cleared, leaving the idle
	// command loop with no read deadline.
	HandshakeTimeout = 10 * time.Second
)

// Panel kinds carried on the wire.
const (
	KindShell = "shell" // a plain host shell (the default)
	KindAgent = "agent" // an agent CLI run as the panel process
)

// EventBufferSize is the per-client buffer of outbound server messages. It is
// generous so a burst of zoomed panel output is not dropped.
const EventBufferSize = 256

// Command is sent from a client to the server. Beyond the lifecycle actions, a
// zoomed client streams a panel with attach/input/resize/detach, and organises
// the fleet with panel.group / panel.rename.
type Command struct {
	Action string   `json:"action"`           // hello | panel.list | panel.create | panel.respawn | panel.close | panel.purge | panel.attach | panel.detach | panel.input | panel.dispatch | panel.dispatch-group | panel.resize | panel.group | panel.ungroup | panel.rename | panel.move | panel.pin | panel.unpin | panel.favourite | panel.unfavourite | panel.signal | panel.diff | panel.git | panel.scratch | group.show | group.layout | group.favourite | group.unfavourite | task.enqueue | task.list | task.cancel | task.drain | server.reload | config.get | command.run
	Kind   string   `json:"kind,omitempty"`   // panel kind for "panel.create" (default "shell")
	ID     string   `json:"id,omitempty"`     // target panel for close/attach/input/resize/diff, or the panel to rename
	Path   string   `json:"path,omitempty"`   // init command (binary path) for "panel.create"; empty = default shell
	Args   []string `json:"args,omitempty"`   // command arguments for "panel.create" (an agent profile's args)
	Dir    string   `json:"dir,omitempty"`    // working directory the new panel's process runs in ("panel.create")
	Data   []byte   `json:"data,omitempty"`   // input bytes for "panel.input"
	Prompt string   `json:"prompt,omitempty"` // the task brief for "panel.dispatch"/"panel.dispatch-group": recorded on the panel(s) and delivered to the process as a unit
	Submit string   `json:"submit,omitempty"` // optional submit sequence appended to a dispatched prompt (default newline)
	Rows   int      `json:"rows,omitempty"`   // window size for "panel.resize"
	Cols   int      `json:"cols,omitempty"`
	IDs    []string `json:"ids,omitempty"`    // panels to group ("panel.group"), remove ("panel.ungroup"), close ("panel.close"), or move as a block ("panel.move")
	Group  string   `json:"group,omitempty"`  // group name to assign ("panel.group"), or the group to rename ("panel.rename")
	Name   string   `json:"name,omitempty"`   // new name for "panel.rename" (a panel title or a group name)
	Index  int      `json:"index,omitempty"`  // destination index among the remaining panels for "panel.move"
	Signal string   `json:"signal,omitempty"` // signal name to deliver for "panel.signal", e.g. "SIGINT"
	Count  int      `json:"count,omitempty"`  // absolute visible count for "group.show": how many members stream as live tiles
	Git    string   `json:"git,omitempty"`    // git op for "panel.git", e.g. "log", "commit", "worktree-add"; Name carries a branch, Dir a worktree path
	Layout string   `json:"layout,omitempty"` // layout name for "group.layout": the named split arrangement the group opens with

	// Role and Self are declared on "hello" by a control client (the conductor
	// agent driving the fleet over the socket). Role "conductor" puts the
	// connection under a scoped policy — it cannot act on itself and cannot stop
	// the server (see the server's command dispatch); an empty Role is the
	// full-power cockpit, unchanged. Self is the conductor's OWN panel id, so the
	// server knows which panel to refuse self-targeted actions against. Both are
	// self-declared over a uid-private socket: they are a guardrail against agent
	// accidents, not a security boundary.
	Role string `json:"role,omitempty"`
	Self string `json:"self,omitempty"`

	// Conductor marks a "panel.create" as the singleton control agent. The server
	// enforces at most one, gives it a server-managed ephemeral workspace, and
	// injects the socket/identity env so the agent inside can drive the fleet.
	Conductor bool `json:"conductor,omitempty"`
}

// GroupView carries a group's view settings on a snapshot: Shown is how many
// members stream as live tiles before the rest collapse into the summary tile.
type GroupView struct {
	Group     string `json:"group"`
	Shown     int    `json:"shown,omitempty"`
	Layout    string `json:"layout,omitempty"`    // the named split arrangement the group opens with ("" = the default)
	Favourite bool   `json:"favourite,omitempty"` // a dashboard favourite: sorts the group's card to the front
}

// Panel is the server-side view of a single live terminal.
type Panel struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`                // "shell" | "agent"
	Title     string `json:"title"`               // human label shown on the dashboard
	State     string `json:"state,omitempty"`     // lifecycle: spawning|running|idle|attention|exited
	Group     string `json:"group,omitempty"`     // work item the panel belongs to, if any
	Task      string `json:"task,omitempty"`      // the brief the panel was last dispatched, if any
	Activity  string `json:"activity,omitempty"`  // short status line the Monitor keeps live
	Spark     string `json:"spark,omitempty"`     // output-rate sparkline over the recent window
	Pinned    bool   `json:"pinned,omitempty"`    // pinned to a live tile in its group's split view
	Favourite bool   `json:"favourite,omitempty"` // a dashboard favourite: sorts the card to the front
	Conductor bool   `json:"conductor,omitempty"` // the singleton control agent (server-managed workspace), so a frontend can badge it
}

// Task is the wire view of a backlog task: a prompt assigned (or waiting to be
// assigned) to a panel, with its lifecycle status. Frontends render the set as the
// queue/kanban; the status string matches task.Status.
type Task struct {
	ID       string `json:"id"`
	Prompt   string `json:"prompt"`
	Status   string `json:"status"`             // queued | dispatched | running | done | failed
	Panel    string `json:"panel,omitempty"`    // the panel executing it, if assigned
	Group    string `json:"group,omitempty"`    // the work item it belongs to, if any
	Result   string `json:"result,omitempty"`   // a terminal note (e.g. a failure reason)
	Attempts int    `json:"attempts,omitempty"` // how many times its prompt has been delivered
}

// DiffFile is one changed path in the structured "diff" reply: its staged and
// unstaged status letters (as `git status --porcelain` reports them, "?" for an
// untracked file) and the unified diff text for each side, either empty when that
// side is unchanged. The cockpit renders the set as a master-detail popup.
type DiffFile struct {
	Path     string `json:"path"`
	Index    string `json:"index,omitempty"`         // staged-side status: M, A, D, R, … or "" when unchanged
	Work     string `json:"work,omitempty"`          // unstaged-side status, or "?" for an untracked file
	Staged   string `json:"staged_diff,omitempty"`   // `git diff --cached` text for this file
	Unstaged string `json:"unstaged_diff,omitempty"` // `git diff` text for this file
}

// PluginCommand is one command a Lua plugin registered, surfaced to frontends so
// the cockpit's command picker can list it and invoke it with command.run.
type PluginCommand struct {
	Name string `json:"name"`           // stable name, the key command.run carries
	Desc string `json:"desc,omitempty"` // one-line description shown in the picker
}

// ServerMsg is broadcast or replied from the server to a client.
type ServerMsg struct {
	Type      string      `json:"type"`                 // "welcome" | "panels" | "telemetry" | "output" | "stats" | "error" | "ephemeral" | "scratch" | "diff" | "gitout" | "notice" | "config" | "footer" | "tasks" | "ping" (an additive, ignorable server→client keepalive that resets the client's idle read deadline)
	Version   string      `json:"version,omitempty"`    // protocol version, set on "welcome"
	ServerVer string      `json:"server_ver,omitempty"` // the server's build version, set on "welcome"
	Error     string      `json:"error,omitempty"`      // set on "error"
	Notice    string      `json:"notice,omitempty"`     // a plugin-originated transient notice, set on "notice"
	Footer    string      `json:"footer,omitempty"`     // a plugin-set persistent footer segment, set on "footer" and carried on "config"; empty clears it
	Panels    []Panel     `json:"panels,omitempty"`     // full snapshot on "panels"; live state/spark refresh on "telemetry"
	Groups    []GroupView `json:"groups,omitempty"`     // per-group view settings on the "panels" snapshot, alongside Panels
	Tasks     []Task      `json:"tasks,omitempty"`      // the backlog snapshot on "tasks" (reply to task.list)
	ID        string      `json:"id,omitempty"`         // panel id on "output"; the new transient panel id on "ephemeral" (a git op); the diffed agent panel id on "diff"
	Data      []byte      `json:"data,omitempty"`       // pty output bytes on "output"
	Files     []DiffFile  `json:"files,omitempty"`      // per-file staged/unstaged diffs on "diff"; ID carries the target panel
	Text      string      `json:"text,omitempty"`       // a non-interactive git op's captured output on "gitout"; ID carries the target panel
	Failed    bool        `json:"failed,omitempty"`     // on "gitout", the op exited non-zero (its message is in Text)

	// The merged effective client config, set on "config": defaults <- YAML <-
	// plugin. The cockpit applies it over its local config on attach and reload, so
	// a plugin can rebind keys and set toggles. Commands lists the plugin commands
	// for the command picker.
	Config   json.RawMessage `json:"config,omitempty"`
	Commands []PluginCommand `json:"commands,omitempty"`

	// Host resource sample on "stats", measured on the server so the footer
	// reflects the machine where the panels actually run.
	CPU      float64 `json:"cpu,omitempty"`       // system-wide CPU load %
	MemUsed  uint64  `json:"mem_used,omitempty"`  // system memory in use, bytes
	MemTotal uint64  `json:"mem_total,omitempty"` // total system memory, bytes
}
