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
	Action string   `json:"action"`         // hello | panel.list | panel.create | panel.respawn | panel.close | panel.purge | panel.attach | panel.detach | panel.input | panel.resize | panel.group | panel.ungroup | panel.rename | panel.move | panel.pin | panel.unpin | panel.signal | panel.diff | panel.git | group.show | server.reload | config.get | command.run
	Kind   string   `json:"kind,omitempty"` // panel kind for "panel.create" (default "shell")
	ID     string   `json:"id,omitempty"`   // target panel for close/attach/input/resize/diff, or the panel to rename
	Path   string   `json:"path,omitempty"` // init command (binary path) for "panel.create"; empty = default shell
	Args   []string `json:"args,omitempty"` // command arguments for "panel.create" (an agent profile's args)
	Dir    string   `json:"dir,omitempty"`  // working directory the new panel's process runs in ("panel.create")
	Data   []byte   `json:"data,omitempty"` // input bytes for "panel.input"
	Rows   int      `json:"rows,omitempty"` // window size for "panel.resize"
	Cols   int      `json:"cols,omitempty"`
	IDs    []string `json:"ids,omitempty"`    // panels to group ("panel.group"), remove ("panel.ungroup"), close ("panel.close"), or move as a block ("panel.move")
	Group  string   `json:"group,omitempty"`  // group name to assign ("panel.group"), or the group to rename ("panel.rename")
	Name   string   `json:"name,omitempty"`   // new name for "panel.rename" (a panel title or a group name)
	Index  int      `json:"index,omitempty"`  // destination index among the remaining panels for "panel.move"
	Signal string   `json:"signal,omitempty"` // signal name to deliver for "panel.signal", e.g. "SIGINT"
	Count  int      `json:"count,omitempty"`  // absolute visible count for "group.show": how many members stream as live tiles
	Git    string   `json:"git,omitempty"`    // git op for "panel.git", e.g. "log", "commit", "worktree-add"; Name carries a branch, Dir a worktree path
}

// GroupView carries a group's view settings on a snapshot: Shown is how many
// members stream as live tiles before the rest collapse into the summary tile.
type GroupView struct {
	Group string `json:"group"`
	Shown int    `json:"shown,omitempty"`
}

// Panel is the server-side view of a single live terminal.
type Panel struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`               // "shell" | "agent"
	Title    string `json:"title"`              // human label shown on the dashboard
	State    string `json:"state,omitempty"`    // lifecycle: spawning|running|idle|attention|exited
	Group    string `json:"group,omitempty"`    // work item the panel belongs to, if any
	Activity string `json:"activity,omitempty"` // short status line the Monitor keeps live
	Spark    string `json:"spark,omitempty"`    // output-rate sparkline over the recent window
	Pinned   bool   `json:"pinned,omitempty"`   // pinned to a live tile in its group's split view
}

// PluginCommand is one command a Lua plugin registered, surfaced to frontends so
// the cockpit's command picker can list it and invoke it with command.run.
type PluginCommand struct {
	Name string `json:"name"`           // stable name, the key command.run carries
	Desc string `json:"desc,omitempty"` // one-line description shown in the picker
}

// ServerMsg is broadcast or replied from the server to a client.
type ServerMsg struct {
	Type      string      `json:"type"`                 // "welcome" | "panels" | "telemetry" | "output" | "stats" | "error" | "ephemeral" | "notice" | "config" | "footer" | "ping" (an additive, ignorable server→client keepalive that resets the client's idle read deadline)
	Version   string      `json:"version,omitempty"`    // protocol version, set on "welcome"
	ServerVer string      `json:"server_ver,omitempty"` // the server's build version, set on "welcome"
	Error     string      `json:"error,omitempty"`      // set on "error"
	Notice    string      `json:"notice,omitempty"`     // a plugin-originated transient notice, set on "notice"
	Footer    string      `json:"footer,omitempty"`     // a plugin-set persistent footer segment, set on "footer" and carried on "config"; empty clears it
	Panels    []Panel     `json:"panels,omitempty"`     // full snapshot on "panels"; live state/spark refresh on "telemetry"
	Groups    []GroupView `json:"groups,omitempty"`     // per-group view settings on the "panels" snapshot, alongside Panels
	ID        string      `json:"id,omitempty"`         // panel id on "output"; the new transient panel id on "ephemeral" (a diff or git op)
	Data      []byte      `json:"data,omitempty"`       // pty output bytes on "output"

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
