// Package panel defines baton's core panel model: one live terminal that runs
// either a shell or an agent, together with the Monitor's view of its lifecycle.
//
// A Panel is the real, server-fed model: the server owns the fleet and reports it
// to every frontend, which renders it as-is. The struct is shaped so the core can
// operate on panels directly — group them into work items, signal their processes,
// retire them, and so on.
package panel

import "github.com/cmj0121/baton/internal/proto"

// Kind is what a panel runs.
type Kind int

// The panel kinds.
const (
	Shell Kind = iota // a plain host shell
	Agent             // an agent CLI (claude, copilot, …) run as the panel process
)

func (k Kind) String() string {
	if k == Agent {
		return "agent"
	}
	return "shell"
}

// State is the Monitor lifecycle state a panel is in (see docs/SPEC.md).
type State int

// The Monitor lifecycle states, from spawn to exit. New members are APPENDED,
// never inserted: the zero value has to stay Spawning, since that is what a
// freshly created panel is. The constant itself is never serialised — the wire
// carries the string String() renders — so the numbers are free to move only in
// the sense that nothing outside this package may depend on them.
const (
	Spawning State = iota
	Running
	Idle
	Attention
	Exited

	// Done is a quiet AGENT whose turn looks over: it stopped producing output
	// long enough that the work it was given reads as finished, so the panel is
	// asking to be reviewed rather than answered. It is deliberately not reachable
	// for a shell — a shell sitting at its prompt is idle, and calling that "done"
	// would put every untouched shell in the human's queue.
	Done

	// Stuck is a panel quiet far past what its agent's work should take. It says
	// nothing about why — a wedged process, a network wait, and a model thinking
	// hard are indistinguishable from the byte stream — only that the silence has
	// outlasted the threshold configured for this agent, which is the point at
	// which a human is the cheapest way to find out.
	Stuck
)

func (s State) String() string {
	switch s {
	case Spawning:
		return "spawning"
	case Running:
		return "running"
	case Idle:
		return "idle"
	case Attention:
		return "attention"
	case Exited:
		return "exited"
	case Done:
		return "done"
	case Stuck:
		return "stuck"
	default:
		return "unknown"
	}
}

// ParseKind maps a wire kind string to a Kind, defaulting to Shell.
func ParseKind(s string) Kind {
	if s == "agent" {
		return Agent
	}
	return Shell
}

// ParseState maps a wire state string to a State, defaulting to Idle.
//
// The default is Idle rather than Running because the mismatch it exists for is
// an OLDER frontend reading a NEWER daemon, and in that direction a frontend
// must UNDER-CLAIM rather than lie. Rendering an unknown state as "running"
// asserts that work is happening and paints a green, breathing card over a panel
// that may be waiting on a human; rendering it as "idle" asserts only that
// nothing is known to be happening, which is true of every state baton might add
// after this one. The cost of guessing low is a card that looks calmer than it
// is; the cost of guessing high is a queue that silently loses an entry.
func ParseState(s string) State {
	switch s {
	case "spawning":
		return Spawning
	case "running":
		return Running
	case "attention":
		return Attention
	case "exited":
		return Exited
	case "done":
		return Done
	case "stuck":
		return Stuck
	default:
		return Idle
	}
}

// Panel is one live terminal the server owns: a shell or an agent, plus the
// Monitor's lifecycle state. The Group field files the panel under a work item;
// the Activity/Spark fields are live telemetry the Monitor reports as output
// ebbs and flows — a short status line and an output-rate sparkline.
type Panel struct {
	ID    string
	Kind  Kind
	Title string
	State State
	Group string // work item this panel belongs to, "" if ungrouped

	// DisplayTitle is the title a panel.title plugin hook computed, overriding
	// Title on the frontends only. Title stays the base "<cmd> · <dir>" the hook
	// reads, so the hook never sees its own output (no feedback). Empty means no
	// override — the frontends show Title.
	DisplayTitle string

	// Task is the brief the panel was last dispatched: the objective an agent was
	// asked to work, recorded when a prompt is handed to it as a unit (not as raw
	// keystrokes). Empty until the panel is dispatched; carried to every frontend so
	// the card can show what the agent is working, and persisted so it survives a
	// restart.
	Task string

	Activity string // short status line, e.g. "running · 3m"
	Spark    string // output-rate sparkline over the recent window, e.g. "▂▃▅▇▆▃▁"

	// Sig is the Monitor's similarity signature: eight hex characters standing for
	// this panel's state plus the shape of its last output line. It joins Activity
	// and Spark as telemetry the server computes and every frontend consumes — a
	// group's summary tile folds the members whose Sig matches the majority, which
	// is a question a frontend cannot answer for itself because it holds no output
	// for a panel it never attached to. Empty for a panel the Monitor no longer
	// tracks, which every reader treats as "unknown", never as "the same".
	Sig string

	// Pinned marks the panel as promoted to a live tile in its group's split
	// view. The server owns the flag and reports it to every frontend, so a pin
	// survives a frontend restart and is shared across clients.
	Pinned bool

	// Favourite marks the panel as a dashboard favourite: favourited cards sort
	// to the front of the dashboard and show a marker. The server owns the flag
	// and reports it to every frontend, so it survives a frontend restart and is
	// shared across clients. It is entirely separate from Pinned (which only
	// curates live tiles inside a group split).
	Favourite bool

	// Conductor marks the singleton control agent: an agent panel the server
	// spawned in a server-managed ephemeral workspace and wired to drive the
	// fleet over the socket. At most one exists at a time.
	Conductor bool

	// GlobalShell marks the singleton global shell: a plain host shell the server
	// holds in $HOME, always one keystroke away. Unlike the conductor it drives
	// nothing — no scoped role, no managed workspace. At most one exists at a time.
	GlobalShell bool

	// Cwd is the directory the panel's process is in now, as opposed to the one it
	// was launched in — learned from the shell's own report or the process table
	// (see internal/cwd). Empty when it is not known, which every reader treats as
	// "fall back to where it started" rather than as a directory.
	Cwd string

	// Pid is the OS pid of the panel's process-group leader, reported by the
	// server, or 0 once the process has exited. It roots the panel's OS descendant
	// subtree in the process-tree overlay (and `baton ctl tree`).
	Pid int

	// ExitCode is the status the panel's process ended with, and is meaningful
	// ONLY once State is Exited — a zero on a live panel means "not applicable",
	// never "succeeded". There is no `failed` state: the daemon reports the code
	// and the frontend draws the conclusion, which keeps failure a rendering of a
	// fact the lifecycle already had rather than a sixth thing the Monitor has to
	// shuttle panels in and out of.
	ExitCode int

	// Reason is why the panel says it needs a human, in the AGENT's own words —
	// set only by an explicit declaration (panel.attention), never by the tail
	// heuristic or a timer, which raise a state without being able to say why.
	// Empty whenever no declaration stands.
	Reason string
}

// IsAgent reports whether the panel runs an agent CLI rather than a shell.
func (p Panel) IsAgent() bool { return p.Kind == Agent }

// FromProto decodes a wire panel into the domain model.
func FromProto(p proto.Panel) Panel {
	return Panel{
		ID:          p.ID,
		Kind:        ParseKind(p.Kind),
		Title:       p.Title,
		State:       ParseState(p.State),
		Group:       p.Group,
		Task:        p.Task,
		Activity:    p.Activity,
		Spark:       p.Spark,
		Sig:         p.Sig,
		Pinned:      p.Pinned,
		Favourite:   p.Favourite,
		Conductor:   p.Conductor,
		GlobalShell: p.GlobalShell,
		Cwd:         p.Cwd,
		Pid:         p.Pid,
		ExitCode:    p.ExitCode,
		Reason:      p.Reason,
	}
}

// ToProto encodes the panel for the wire.
func (p Panel) ToProto() proto.Panel {
	title := p.Title
	if p.DisplayTitle != "" {
		title = p.DisplayTitle // a panel.title hook's override wins on the frontends
	}
	return proto.Panel{
		ID:          p.ID,
		Kind:        p.Kind.String(),
		Title:       title,
		State:       p.State.String(),
		Group:       p.Group,
		Task:        p.Task,
		Activity:    p.Activity,
		Spark:       p.Spark,
		Sig:         p.Sig,
		Pinned:      p.Pinned,
		Favourite:   p.Favourite,
		Conductor:   p.Conductor,
		GlobalShell: p.GlobalShell,
		Cwd:         p.Cwd,
		Pid:         p.Pid,
		ExitCode:    p.ExitCode,
		Reason:      p.Reason,
	}
}
