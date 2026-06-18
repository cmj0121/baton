// Package panel defines baton's core panel model: one live terminal that runs
// either a shell or an agent, together with the Monitor's view of its lifecycle.
//
// Panels are mock data today — they populate the dashboard before the server
// reports anything real. The struct is shaped so the core can later own panels
// and operate on them directly: group them into work items, signal their
// processes, retire them, and so on.
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
// the Activity field is the live status line the Monitor reports as output ebbs
// and flows.
type Panel struct {
	ID    string
	Kind  Kind
	Title string
	State State
	Group string // work item this panel belongs to, "" if ungrouped

	Activity string // short status line, e.g. "running · 3m"
}

// IsAgent reports whether the panel runs an agent CLI rather than a shell.
func (p Panel) IsAgent() bool { return p.Kind == Agent }

// FromProto decodes a wire panel into the domain model.
func FromProto(p proto.Panel) Panel {
	return Panel{
		ID:       p.ID,
		Kind:     ParseKind(p.Kind),
		Title:    p.Title,
		State:    ParseState(p.State),
		Group:    p.Group,
		Activity: p.Activity,
	}
}

// ToProto encodes the panel for the wire.
func (p Panel) ToProto() proto.Panel {
	return proto.Panel{
		ID:       p.ID,
		Kind:     p.Kind.String(),
		Title:    p.Title,
		State:    p.State.String(),
		Group:    p.Group,
		Activity: p.Activity,
	}
}

// Mock returns a believable fleet of shells and agents used to populate the
// dashboard before (and alongside) the first real server snapshot.
func Mock() []Panel {
	return []Panel{
		{ID: "a1", Kind: Agent, Title: "claude · refactor auth", State: Attention, Activity: "needs you · 8m"},
		{ID: "a2", Kind: Agent, Title: "claude · write tests", State: Running, Activity: "streaming · 3m"},
		{ID: "a3", Kind: Agent, Title: "copilot · api docs", State: Idle, Activity: "waiting · 21m"},
		{ID: "s1", Kind: Shell, Title: "shell · make build", State: Running, Activity: "building · 1m"},
		{ID: "s2", Kind: Shell, Title: "shell · tail logs", State: Idle, Activity: "quiet · 12m"},
		{ID: "a4", Kind: Agent, Title: "claude · review PR", State: Spawning, Activity: "spawning · 2s"},
		{ID: "s3", Kind: Shell, Title: "shell · run server", State: Running, Activity: "serving · 44m"},
		{ID: "a5", Kind: Agent, Title: "claude · migrate db", State: Exited, Activity: "exit 0 · done"},
	}
}
