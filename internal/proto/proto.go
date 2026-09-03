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
//
// REMOVING an action degrades the same way, in both directions, which is why
// dropping panel.scratch (#61) did not bump it either. Up: an old cockpit that
// still sends panel.scratch lands on the same `unknown action` error a new
// action gets from an old daemon — a refusal the cockpit already surfaces, not a
// misread. Down: the "scratch" reply is one the new daemon simply never sends,
// and a message type nobody emits costs an old cockpit nothing. A removal that
// does NOT degrade this way — a field the peer must read, a type still on the
// wire under a new meaning — is a bump, like any other change of meaning.
// TestScratchActionIsRefused in internal/server holds the up direction to that.
//
// GIVING A FIELD A MEANING IT DID NOT HAVE for one op is the third shape, and
// the dashboard's worktree verb (#66) is the case that settled it. `panel.git`
// with git "worktree-add" grew a second form: an EMPTY ID, a Dir naming the
// repo, and the Path/Args/Profile triple panel.create already carries. None of
// those four fields meant anything for this op before, so none of them changed
// meaning; ID went from required to optional, which is a widening rather than a
// new requirement. Both directions still degrade. Up: an old daemon ignores the
// four unknown-to-it fields, finds ID empty, and answers `no panel with id ""` —
// a refusal the cockpit surfaces, not a misread; TestWTAddNoDir is the same
// shape of refusal held on the current daemon. Down: an old cockpit only ever
// sends worktree-add with a real id, which is the form that did not change.
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
	Action    string   `json:"action"`              // hello | panel.list | panel.create | panel.respawn | panel.close | panel.purge | panel.attach | panel.detach | panel.input | panel.dispatch | panel.dispatch-group | panel.resize | panel.group | panel.ungroup | panel.rename | panel.move | panel.pin | panel.unpin | panel.favourite | panel.unfavourite | panel.signal | panel.attention | panel.resolve | panel.ack | panel.tail | panel.diff | panel.git | panel.log | panel.logview | fleet.search | group.show | group.layout | group.favourite | group.unfavourite | task.enqueue | task.list | task.cancel | task.promote | task.demote | task.drain | server.reload | config.get | command.run | remote.status | remote.enable | remote.disable | remote.rotate | remote.kick | score.submit | score.list | score.status | score.merge | score.reword | score.lower | worktree.list | worktree.sweep
	Kind      string   `json:"kind,omitempty"`      // panel kind for "panel.create" (default "shell")
	ID        string   `json:"id,omitempty"`        // target panel for close/attach/input/resize/diff, the panel to rename, the panel "score.list" ranks its entries for (empty = rank against no context), the score entry a refine verb corrects, or — empty on "panel.git" worktree-add — the spawn that has no source panel at all
	Path      string   `json:"path,omitempty"`      // init command (binary path) for "panel.create"; empty = default shell. Also the agent command for a targetless "panel.git" worktree-add, which spawns one
	Args      []string `json:"args,omitempty"`      // command arguments for "panel.create" (an agent profile's args), and for a targetless "panel.git" worktree-add
	Profile   string   `json:"profile,omitempty"`   // the agent profile the spawn came from; the server resolves THAT profile's resource limits from its own config, so a client never carries a policy it could widen. Same for a targetless "panel.git" worktree-add
	Dir       string   `json:"dir,omitempty"`       // working directory the new panel's process runs in ("panel.create"); the worktree path for "panel.git" worktree-remove, and the repo a targetless worktree-add branches from
	Data      []byte   `json:"data,omitempty"`      // input bytes for "panel.input"
	Prompt    string   `json:"prompt,omitempty"`    // the task brief for "panel.dispatch"/"panel.dispatch-group": recorded on the panel(s) and delivered to the process as a unit; the note text for "score.submit"; the new wording for "score.reword"
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
	Git       string   `json:"git,omitempty"`    // git op for "panel.git", e.g. "log", "commit", "worktree-add"; Name carries a branch, Dir a worktree path — or, for a targetless "worktree-add" (empty ID), the repo to branch from, with Path/Args/Profile carrying the agent spec
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

	// Actor is who a client OUTSIDE a panel is, declared on "hello" alongside the
	// two above. Inside a panel, Self already answers it — the daemon injects the
	// panel id and the client passes it back. Outside one, Self is empty for
	// everybody: the operator's own `baton ctl score submit` and every MCP server
	// started outside the fleet arrive indistinguishable, and the server's
	// per-actor rate caps then hand a single slot round between them, refusing one
	// client's work because another was busy.
	//
	// WHY THE CLIENT SAYS IT AT ALL, which is the question every comment around
	// this field answers a different half of. The server cannot work it out. The
	// connection is not the caller: `baton ctl` is a whole process per command and
	// `baton mcp` dials once per tool call, so cc.id is fresh every time and a cap
	// keyed on it fences nothing an agent drives — measured at fifty admitted
	// calls a second through twenty one-shot connections against a cap of four.
	// The peer credentials on the unix socket do not close it either: SO_PEERCRED
	// gives a pid, a uid and a session id on Linux and LOCAL_PEERPID gives a pid
	// and nothing else on darwin, so the identity the cap wants — one that
	// outlives the process for a per-command client, and does not for a
	// long-lived one — exists on one of baton's two platforms and cannot be asked
	// for on the other. Every party on this socket already runs as the fleet
	// owner's uid, so nothing is being defended here that a self-declared string
	// weakens.
	//
	// WHAT it is depends on how long the client lives, and that is not a hedge —
	// the two shapes have genuinely different stable identities. `baton ctl` is a
	// whole process per command, so nothing about the process survives two turns
	// of a loop and it declares its SESSION, which a shell and everything under
	// it share. `baton mcp` is one long-lived process dialling per tool call, so
	// it declares ITSELF; declaring the session would put it back in one slot with
	// the operator's own shell, which is usually the session that started the
	// agent runtime. See control.Dial and control.DialAsProcess.
	//
	// The session is stable for a shell and not for a launcher above one: cron,
	// ssh, systemd-run and agent runtimes start each command in a session of
	// their own, so an actor arriving that way is fresh every invocation.
	// control.sessionActor is where that is argued — including who it leaves
	// uncovered, which is a shell panel and those launchers, and not the agent
	// panels that carry a panel id nor `baton mcp`.
	//
	// Self-declared, exactly as Role and Self are, and for once that is not even a
	// weakening: it grants nothing, names no fence, and picks only which rate-cap
	// slot this client spends. A client that varied it would be evading a cap it
	// could already evade by varying Self.
	Actor string `json:"actor,omitempty"`

	// Passkey and Source are declared on "hello" by a REMOTE cockpit — one that
	// reached this daemon through `ssh <host> baton --stdio` rather than through
	// the session's own socket. Passkey is the 8-character code the fleet owner
	// enabled remote with; without the current one the attach is refused, and the
	// attempt is rate-limited and logged. Source is the label the connection
	// shows up as in the remote overlay, e.g. "cmj@laptop.lan".
	//
	// Both are self-declared, exactly as Role and Self are. That is not an
	// oversight: the far side of the ssh pipe already runs as the fleet owner's
	// uid, so the passkey is a deliberate-enable proof and a revocation handle,
	// never a boundary against someone who can already ssh in as that user.
	Passkey string `json:"passkey,omitempty"`
	Source  string `json:"source,omitempty"`

	// Conn is the connection the "remote.kick" acts on, as listed in the remote
	// overlay. It is its own field rather than ID because a connection is not a
	// panel and must never be resolvable as one.
	Conn string `json:"conn,omitempty"`

	// From is the score entry "score.merge" folds INTO the entry named by ID:
	// that one is retired and its wording is kept on the survivor as an alias, so
	// a later repeat of it still folds. It is its own field for the reason Conn
	// is: a score entry is not a panel, and IDs already means "several panels".
	//
	// Appended and optional, so an old daemon reads a new client's frame with it
	// zero — and answers the action it does not know with its existing `unknown
	// action` error, which is why no version moves for this. See ProtocolVersion.
	From string `json:"from,omitempty"`

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

	// Profile is the agent profile the panel was spawned from, joined in when a
	// snapshot is built (like Pid). The server has always recorded it — it is what
	// the caps, the restart policy, the log destination and the isolation all
	// resolve through — but it never travelled, so a frontend could not group or
	// filter by it. Empty for a shell, and for an agent spawned without a profile.
	Profile string `json:"profile,omitempty"`

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

	// Logging marks a panel whose output the daemon is writing to a file, so a
	// frontend can badge it. A feature that silently writes your terminal to disk
	// has to be visible while it does it — which is why this rides the ordinary
	// fleet snapshot rather than being something a cockpit has to ask about.
	Logging bool `json:"logging,omitempty"`

	// LogPath is the file that log is being written to, on the machine the FLEET
	// runs on — not the one the cockpit runs on, which is a distinction --remote
	// makes real. Empty whenever Logging is false.
	LogPath string `json:"log_path,omitempty"`

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

	// Panels is the per-panel breakdown, keyed by panel id. Only a source that can
	// attribute spend fills it in, and only for panels the daemon launched with a
	// session of their own — so a missing entry means "not known", never "zero".
	Panels map[string]PanelUsage `json:"panels,omitempty"`

	// Limits is the account's standing against its subscription quotas, when a
	// limits source is configured and has a reading. It rides the same message as
	// the token totals but answers a different question — the totals say who is
	// burning it, the limits say whether there is anything left to burn — and it
	// is nil, not zeroed, whenever there is no reading: a quota bar resting at 0%
	// asserts a full tank.
	Limits *LimitsInfo `json:"limits,omitempty"`
}

// LimitsInfo is the account's rate-limit standing on the wire. Every window is a
// pointer for the same reason it is in usage.Limits: absent is a state the
// sources genuinely report, and it must not decode as a window at zero.
//
// The resets are instants rather than durations, and deliberately so: the daemon
// polls on its own cadence while the cockpit ticks the countdown once a second,
// exactly as it already does for the usage window.
type LimitsInfo struct {
	FiveHour       *LimitWindow `json:"five_hour,omitempty"`
	SevenDay       *LimitWindow `json:"seven_day,omitempty"`
	SevenDayOpus   *LimitWindow `json:"seven_day_opus,omitempty"`
	SevenDaySonnet *LimitWindow `json:"seven_day_sonnet,omitempty"`

	// Credit is the extra-usage balance; nil when the account has none, or the
	// source cannot see one.
	Credit *LimitCredit `json:"credit,omitempty"`

	// Source is "statusline" or "oauth". At is when the reading was taken, as an
	// RFC 3339 instant — carried because the statusline source is a push and a
	// reading can sit unchanged for minutes, so its age is a fact the cockpit needs
	// in order to say whether it is still current.
	Source string `json:"source,omitempty"`
	At     string `json:"at,omitempty"`
}

// LimitWindow is one rate-limit window on the wire: how much of it is spent, as a
// percentage 0–100, and when it resets.
type LimitWindow struct {
	UsedPercent float64 `json:"used_percentage"`
	ResetsAt    string  `json:"resets_at,omitempty"`
}

// LimitCredit is the extra-usage balance on the wire. The amounts are pointers
// because a null monthly limit means uncapped, which is the opposite reading from
// a limit of zero.
type LimitCredit struct {
	Enabled     bool     `json:"enabled"`
	MonthlyUSD  *float64 `json:"monthly_usd,omitempty"`
	UsedUSD     *float64 `json:"used_usd,omitempty"`
	UsedPercent *float64 `json:"used_percentage,omitempty"`
}

// AgentBackend is one agent CLI a frontend may spawn: the profile name a panel
// is created under, and the command behind it. It carries no policy — limits,
// restart, attention and isolation stay on the server's own profile and are
// applied there, so a frontend never has to be trusted with them.
type AgentBackend struct {
	Name     string   `json:"name"`
	Command  string   `json:"command"`
	Args     []string `json:"args,omitempty"`
	Homepage string   `json:"homepage,omitempty"` // where to get it; set on presets, empty on a user profile

	// Missing says the command was not found on the fleet's machine. The polarity
	// is the compatibility story and is not free to flip: a daemon older than this
	// field sends only the backends it found and no flag at all, so the zero value
	// has to mean "installed". An Installed bool would decode those as false and
	// paint a working fleet as having nothing — which is the exact failure the
	// field exists to prevent.
	Missing bool `json:"missing,omitempty"`
}

// RemoteConn is one live attachment in the remote overlay's list: where it came
// from, what role it declared, and when it attached. Source is what the far end
// called itself on "hello" ("local" for a cockpit on the daemon's own socket),
// so it is a label to recognise a connection by, not an identity to trust.
type RemoteConn struct {
	ID     string `json:"id"`               // stable per-connection id; what "remote.kick" names
	Source string `json:"source"`           // "local", or the remote cockpit's self-declared label
	Role   string `json:"role"`             // "cockpit" | "conductor" | "remote"
	Since  string `json:"since"`            // RFC 3339 instant the connection attached
	Self   bool   `json:"self,omitempty"`   // this is the connection that asked — the overlay marks it
	Remote bool   `json:"remote,omitempty"` // reached the daemon over the ssh bridge
}

// RemoteInfo is the reply to "remote.status" and the push that follows every
// change: whether remote is enabled, the passkey, and the live connections.
//
// Passkey is filled in ONLY for a local connection. A remote cockpit may list
// and kick, but the code that lets a new one in is the fleet owner's to read on
// the machine the fleet runs on.
type RemoteInfo struct {
	Enabled bool         `json:"enabled"`
	Passkey string       `json:"passkey,omitempty"` // the current code; local connections only
	Local   bool         `json:"local,omitempty"`   // the asking connection may rotate the passkey and disable remote
	Conns   []RemoteConn `json:"conns,omitempty"`
}

// ServerMsg is broadcast or replied from the server to a client.
type ServerMsg struct {
	Type       string      `json:"type"`                  // "welcome" | "panels" | "telemetry" | "output" | "stats" | "error" | "ephemeral" | "diff" | "gitout" | "search" | "notice" | "config" | "footer" | "usage" | "tasks" | "tail" (the pulled trailing output of one panel: ID names it, Data carries the bytes) | "ping" (an additive, ignorable server→client keepalive that resets the client's idle read deadline) | "remote" (the remote-access status and connection list) | "goodbye" (the server is dropping this connection on purpose; Error says why) | "score" (a score.* verb's reply; Score carries the payload) | "worktree" (a worktree.* verb's reply; Worktree carries the payload)
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

	// Score is a score.* verb's reply payload, set on "score": the created entry
	// id for score.submit, the ranked entry list and the context it was ranked
	// against for score.list, the status object for score.status. It rides as raw
	// JSON rather than three typed fields because the shapes are additive and
	// version together with internal/score (#39); old clients ignore the unknown
	// field entirely.
	//
	// score.list's payload gained that envelope in R3 (#42), moving from a bare
	// array to {context, entries}. That is a shape change rather than an appended
	// field, so it would be a ProtocolVersion bump under the rule above — except
	// that the rule protects PEERS, and at the time R3 landed score.list had
	// never been in a tagged release, so there was no peer to protect.
	//
	// THAT EXEMPTION IS SPENT. It was true of the window between the S0 skeleton
	// and the first tag carrying R3, and of nothing after: from that tag onward
	// there are clients in the field speaking this shape, and reshaping
	// score.list again is an ordinary breaking change under the rule above. Read
	// it as a record of one decision, not as a standing licence.
	Score json.RawMessage `json:"score,omitempty"`

	// The merged effective client config, set on "config": defaults <- YAML <-
	// plugin. The cockpit applies it over its local config on attach and reload, so
	// a plugin can rebind keys and set toggles. Commands lists the plugin commands
	// for the command picker.
	Config   json.RawMessage `json:"config,omitempty"`
	Commands []PluginCommand `json:"commands,omitempty"`

	// The agent backends the FLEET's machine can actually run, set on "config"
	// alongside it. They are detected rather than configured — a property of the
	// host, not a statement of intent — so they ride as their own field instead of
	// being folded into Config, which is the file the cockpit writes back.
	Agents []AgentBackend `json:"agents,omitempty"`

	// Remote is the remote-access status and connection list, set on "remote"
	// (the reply to remote.status and the push after every change). It is nil on
	// every other message type.
	Remote *RemoteInfo `json:"remote,omitempty"`

	// Host resource sample on "stats", measured on the server so the footer
	// reflects the machine where the panels actually run.
	CPU      float64 `json:"cpu,omitempty"`       // system-wide CPU load %
	MemUsed  uint64  `json:"mem_used,omitempty"`  // system memory in use, bytes
	MemTotal uint64  `json:"mem_total,omitempty"` // total system memory, bytes

	// Worktree is a worktree.* verb's reply payload, set on "worktree": the
	// classified list of the trees baton opened for worktree.list, and what a
	// sweep removed, dropped and skipped for worktree.sweep. It rides as raw JSON
	// for the same reason Score does — one field carrying two additive shapes,
	// which old clients ignore whole rather than mis-reading.
	Worktree json.RawMessage `json:"worktree,omitempty"`
}
