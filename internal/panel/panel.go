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

// The Monitor lifecycle states, from spawn to exit.
const (
	Spawning State = iota
	Running
	Idle
	Attention
	Exited
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

// ParseState maps a wire state string to a State, defaulting to Running.
func ParseState(s string) State {
	switch s {
	case "spawning":
		return Spawning
	case "idle":
		return Idle
	case "attention":
		return Attention
	case "exited":
		return Exited
	default:
		return Running
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

	// Pid is the OS pid of the panel's process-group leader, reported by the
	// server, or 0 once the process has exited. It roots the panel's OS descendant
	// subtree in the process-tree overlay (and `baton ctl tree`).
	Pid int
}

// IsAgent reports whether the panel runs an agent CLI rather than a shell.
func (p Panel) IsAgent() bool { return p.Kind == Agent }

// FromProto decodes a wire panel into the domain model.
func FromProto(p proto.Panel) Panel {
	return Panel{
		ID:        p.ID,
		Kind:      ParseKind(p.Kind),
		Title:     p.Title,
		State:     ParseState(p.State),
		Group:     p.Group,
		Task:      p.Task,
		Activity:  p.Activity,
		Spark:     p.Spark,
		Pinned:    p.Pinned,
		Favourite: p.Favourite,
		Conductor: p.Conductor,
		Pid:       p.Pid,
	}
}

// ToProto encodes the panel for the wire.
func (p Panel) ToProto() proto.Panel {
	title := p.Title
	if p.DisplayTitle != "" {
		title = p.DisplayTitle // a panel.title hook's override wins on the frontends
	}
	return proto.Panel{
		ID:        p.ID,
		Kind:      p.Kind.String(),
		Title:     title,
		State:     p.State.String(),
		Group:     p.Group,
		Task:      p.Task,
		Activity:  p.Activity,
		Spark:     p.Spark,
		Pinned:    p.Pinned,
		Favourite: p.Favourite,
		Conductor: p.Conductor,
		Pid:       p.Pid,
	}
}
