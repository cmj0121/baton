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
//
// It stays "baton/1" through the attention work, and the reasoning is worth
// keeping here because the temptation to bump recurs. Every change that landed
// is additive in the two directions that matter: new optional fields (an old
// peer's encoding/json ignores unknown keys, and omitempty elides absent ones,
// so both ends read what they know and zero for the rest) and new actions (an
// old daemon answers a new client's panel.attention with its existing `unknown
// action` error, which the cockpit already surfaces). The one non-additive
// change — the "done" and "stuck" state strings — is handled where it belongs,
// in panel.ParseState, which maps anything it does not know to idle so an older
// cockpit under-claims rather than lies.
//
// Bump to "baton/2" when a field's MEANING changes or a required field is added,
// not when fields are appended: a version bump forces every frontend to be
// rebuilt in lockstep, which is the opposite of what a negotiated version is for
// when the change already degrades correctly on its own.
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
	Action    string   `json:"action"`              // hello | panel.list | panel.create | panel.respawn | panel.close | panel.purge | panel.attach | panel.detach | panel.input | panel.dispatch | panel.dispatch-group | panel.resize | panel.group | panel.ungroup | panel.rename | panel.move | panel.pin | panel.unpin | panel.favourite | panel.unfavourite | panel.signal | panel.attention | panel.resolve | panel.ack | panel.tail | panel.diff | panel.git | panel.scratch | fleet.search | group.show | group.layout | group.favourite | group.unfavourite | task.enqueue | task.list | task.cancel | task.promote | task.demote | task.drain | server.reload | config.get | command.run
	Kind      string   `json:"kind,omitempty"`      // panel kind for "panel.create" (default "shell")
	ID        string   `json:"id,omitempty"`        // target panel for close/attach/input/resize/diff, or the panel to rename
	Path      string   `json:"path,omitempty"`      // init command (binary path) for "panel.create"; empty = default shell
	Args      []string `json:"args,omitempty"`      // command arguments for "panel.create" (an agent profile's args)
	Profile   string   `json:"profile,omitempty"`   // the agent profile the spawn came from; the server resolves THAT profile's resource limits from its own config, so a client never carries a policy it could widen
	Dir       string   `json:"dir,omitempty"`       // working directory the new panel's process runs in ("panel.create")
	Data      []byte   `json:"data,omitempty"`      // input bytes for "panel.input"
	Prompt    string   `json:"prompt,omitempty"`    // the task brief for "panel.dispatch"/"panel.dispatch-group": recorded on the panel(s) and delivered to the process as a unit
	Submit    string   `json:"submit,omitempty"`    // optional submit sequence appended to a dispatched prompt (default newline)
	Ephemeral bool     `json:"ephemeral,omitempty"` // for "task.enqueue" with a spawn spec (Path/Args/Dir): close the provisioned agent once the task finishes
	Rows      int      `json:"rows,omitempty"`      // window size for "panel.resize"
	Cols      int      `json:"cols,omitempty"`
	IDs       []string `json:"ids,omitempty"`    // panels to group ("panel.group"), remove ("panel.ungroup"), close ("panel.close"), or move as a block ("panel.move")
	Group     string   `json:"group,omitempty"`  // group name to assign ("panel.group"), or the group to rename ("panel.rename")
	Name      string   `json:"name,omitempty"`   // new name for "panel.rename" (a panel title or a group name)
	Index     int      `json:"index,omitempty"`  // destination index among the remaining panels for "panel.move"
	Signal    string   `json:"signal,omitempty"` // signal name to deliver for "panel.signal", e.g. "SIGINT"
	Count     int      `json:"count,omitempty"`  // absolute visible count for "group.show"; also how many trailing bytes "panel.tail" returns (0 = the Monitor's own attention-sniff window)
	Git       string   `json:"git,omitempty"`    // git op for "panel.git", e.g. "log", "commit", "worktree-add"; Name carries a branch, Dir a worktree path
	Layout    string   `json:"layout,omitempty"` // layout name for "group.layout": the named split arrangement the group opens with
	Query     string   `json:"query,omitempty"`  // the search term for "fleet.search": a case-insensitive regexp matched against every panel's retained output

	// Reason is why an agent says it needs a human, carried by "panel.attention".
	// It is required there rather than optional: a declaration outranks both the
	// quiet timer and the tail heuristic precisely because it can say what it
	// wants, and one that cannot is worth no more than the heuristic it displaces.
	Reason string `json:"reason,omitempty"`

	// Until is how long an acknowledgement holds, as an RFC 3339 instant, for
	// "panel.ack". Empty is the ordinary dismissal — it holds until the panel next
	// produces output, i.e. until the panel itself does something — while a value
	// is the snooze, which additionally expires on the clock.
	Until string `json:"until,omitempty"`

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

	// GlobalShell marks a "panel.create" as the singleton global shell. The server
	// enforces at most one, runs it as a plain host shell in $HOME, and injects no
	// scoped-role env — unlike the conductor it does not drive the fleet.
	GlobalShell bool `json:"global_shell,omitempty"`
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
	ID          string `json:"id"`
	Kind        string `json:"kind"`                   // "shell" | "agent"
	Title       string `json:"title"`                  // human label shown on the dashboard
	State       string `json:"state,omitempty"`        // lifecycle: spawning|running|idle|attention|done|stuck|exited
	Group       string `json:"group,omitempty"`        // work item the panel belongs to, if any
	Task        string `json:"task,omitempty"`         // the brief the panel was last dispatched, if any
	Activity    string `json:"activity,omitempty"`     // short status line the Monitor keeps live
	Spark       string `json:"spark,omitempty"`        // output-rate sparkline over the recent window
	Pinned      bool   `json:"pinned,omitempty"`       // pinned to a live tile in its group's split view
	Favourite   bool   `json:"favourite,omitempty"`    // a dashboard favourite: sorts the card to the front
	Conductor   bool   `json:"conductor,omitempty"`    // the singleton control agent (server-managed workspace), so a frontend can badge it
	GlobalShell bool   `json:"global_shell,omitempty"` // the singleton global shell (plain host shell in $HOME), so a frontend can badge it
	Cwd         string `json:"cwd,omitempty"`          // the directory the panel's process is in now (not the one it was launched in); empty when unknown
	Pid         int    `json:"pid,omitempty"`          // OS pid of the panel's process-group leader; 0 once the process has exited. Roots the panel's OS descendant subtree (baton ctl tree).

	// ExitCode is the panel process's exit status. It is meaningful ONLY when
	// State is "exited"; a zero value on a live panel means "not applicable",
	// never "succeeded". A non-zero code on an exited panel is what the cockpit
	// renders as `failed` — `failed` is deliberately not a state (see
	// docs/SPEC.md), so the daemon reports the fact and the frontend draws the
	// conclusion.
	ExitCode int `json:"exit_code,omitempty"`

	// Reason is why the panel says it needs a human, as the AGENT stated it via
	// panel.attention. Empty when no declaration stands — a heuristic or a timer
	// raises the state without a reason, and the inbox shows the tail instead.
	Reason string `json:"reason,omitempty"`

	// Since is when the panel entered its current state, as an RFC 3339 instant.
	// It is joined in when a snapshot is built (like Pid), never carried on the
	// in-memory fleet. The inbox sorts on it — oldest first — because a rendered
	// Activity string cannot be ordered and a client's own clock cannot be trusted
	// across the --remote ssh hop.
	Since string `json:"since,omitempty"`

	// Sig is a short hash of what this panel currently looks like: its state and
	// the shape of its last output line, with digits collapsed so "[3/12]
	// building" and "[7/12] building" hash alike. The group summary tile folds the
	// members whose Sig matches the majority. It is 8 hex characters rather than
	// the text itself because shipping 50 tails on every snapshot is exactly what
	// the pull-on-demand tail (panel.tail) exists to avoid.
	Sig string `json:"sig,omitempty"`

	// Acked marks a panel a human has already dealt with from the inbox —
	// dismissed, snoozed, or replied to. It is fleet state, not per-cockpit state,
	// so a second cockpit does not re-offer work the first one just cleared. It
	// falls away when the panel next produces output.
	Acked bool `json:"acked,omitempty"`
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
	Priority int    `json:"priority,omitempty"` // scheduler order among queued tasks: higher drains first
	Attempts int    `json:"attempts,omitempty"` // how many times its prompt has been delivered
	Spawn    bool   `json:"spawn,omitempty"`    // the task provisions its own agent when none is free
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

// SearchHit is one matching line found by a fleet-wide search (fleet.search): the
// panel it is in and the matched line's plain text, carried with the panel's title
// and group so the results list needs no join back to the fleet snapshot. The
// cockpit renders the set grouped by panel; selecting a hit zooms that panel and
// re-runs the same term as a scrollback search there.
type SearchHit struct {
	Panel string `json:"panel"`           // panel id the match is in
	Title string `json:"title"`           // panel title, so the results list stands alone
	Group string `json:"group,omitempty"` // work item the panel belongs to, for grouping the hits
	Text  string `json:"text"`            // the matched line, plain (escape sequences stripped)
}

// PluginCommand is one command a Lua plugin registered, surfaced to frontends so
// the cockpit's command picker can list it and invoke it with command.run.
type PluginCommand struct {
	Name string `json:"name"`           // stable name, the key command.run carries
	Desc string `json:"desc,omitempty"` // one-line description shown in the picker
}

// PanelUsage is one panel's share of the billing window: what the agent running
// in it has spent since the window opened, subagents included.
type PanelUsage struct {
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// UsageInfo is the account's usage over the current billing window, sent
// alongside the pre-rendered Usage string so a frontend can show more than one
// view of it without a round-trip.
//
// The countdown is deliberately not pre-rendered: it has to tick every second,
// while the daemon polls once every 30. The frontend is given the reset instant
// and the presentation settings and does the arithmetic on its own clock.
type UsageInfo struct {
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	Source  string  `json:"source,omitempty"` // "local" | "api"

	// Since and Until bound the window, as RFC 3339 instants. Resets marks Until
	// as a real reset to count down to rather than the edge of the period the
	// source happened to query; a frontend shows no countdown without it, because
	// a wrong one is worse than none.
	Since  string `json:"since,omitempty"`
	Until  string `json:"until,omitempty"`
	Resets bool   `json:"resets,omitempty"`

	// WarnAt and AlarmAt are the fractions of the window spent at which the
	// segment should turn amber and then red. They ride the message because the
	// thresholds are configured on the daemon, where usage.* lives.
	WarnAt  float64 `json:"warn_at,omitempty"`
	AlarmAt float64 `json:"alarm_at,omitempty"`

	// CountdownFormat is "auto" or "dd:hh:mm"; see usage.FormatCountdown.
	CountdownFormat string `json:"countdown_format,omitempty"`

	// Panels is the per-panel breakdown, keyed by panel id. Only a source that can
	// attribute spend fills it in, and only for panels the daemon launched with a
	// session of their own — so a missing entry means "not known", never "zero".
	Panels map[string]PanelUsage `json:"panels,omitempty"`
}

// ServerMsg is broadcast or replied from the server to a client.
type ServerMsg struct {
	Type       string      `json:"type"`                  // "welcome" | "panels" | "telemetry" | "output" | "stats" | "error" | "ephemeral" | "scratch" | "diff" | "gitout" | "search" | "notice" | "config" | "footer" | "usage" | "tasks" | "tail" (the pulled trailing output of one panel: ID names it, Data carries the bytes) | "ping" (an additive, ignorable server→client keepalive that resets the client's idle read deadline)
	Version    string      `json:"version,omitempty"`     // protocol version, set on "welcome"
	ServerVer  string      `json:"server_ver,omitempty"`  // the server's build version, set on "welcome"
	Enforce    string      `json:"enforce,omitempty"`     // the resource-limit backend in force on the host the panels run on ("cgroup", "none"), set on "welcome" and "config" so a frontend offering to edit limits can say whether they bite
	EnforceWhy string      `json:"enforce_why,omitempty"` // why Enforce is "none", e.g. "cgroup v2 is Linux-only"; empty when enforcing
	Error      string      `json:"error,omitempty"`       // set on "error"
	Notice     string      `json:"notice,omitempty"`      // a plugin-originated transient notice, set on "notice"
	Footer     string      `json:"footer,omitempty"`      // a plugin-set persistent footer segment, set on "footer" and carried on "config"; empty clears it
	Usage      string      `json:"usage,omitempty"`       // the account's usage/cost footer segment (internal/usage), set on "usage" and seeded on "hello"; empty means nothing to show
	UsageInfo  *UsageInfo  `json:"usage_info,omitempty"`  // the same usage as structured data, so a frontend can render its own view (the window countdown, a per-panel breakdown) instead of only the pre-rendered Usage string; nil when there is nothing to report
	Panels     []Panel     `json:"panels,omitempty"`      // full snapshot on "panels"; live state/spark refresh on "telemetry"
	Groups     []GroupView `json:"groups,omitempty"`      // per-group view settings on the "panels" snapshot, alongside Panels
	Tasks      []Task      `json:"tasks,omitempty"`       // the backlog snapshot on "tasks" (reply to task.list)
	ID         string      `json:"id,omitempty"`          // panel id on "output" and on "tail"; the new transient panel id on "ephemeral" (a git op); the diffed agent panel id on "diff"
	Data       []byte      `json:"data,omitempty"`        // pty output bytes on "output"; the pulled trailing output on "tail"
	Files      []DiffFile  `json:"files,omitempty"`       // per-file staged/unstaged diffs on "diff"; ID carries the target panel
	Hits       []SearchHit `json:"hits,omitempty"`        // matching lines on "search" (reply to fleet.search), grouped by panel on the frontend
	Text       string      `json:"text,omitempty"`        // a non-interactive git op's captured output on "gitout"; ID carries the target panel
	Failed     bool        `json:"failed,omitempty"`      // on "gitout", the op exited non-zero (its message is in Text)

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
