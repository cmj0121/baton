package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// stateInfo is how a panel state renders: a glyph LED, a label, and a semantic
// colour. The model lives in the panel package; this is the cockpit's view of it.
type stateInfo struct {
	led   string
	label string
	color lipgloss.Color
}

// states maps each lifecycle state to its presentation.
var states = map[panel.State]stateInfo{
	panel.Spawning:  {"◌", "spawning", lipgloss.Color("45")},   // cyan
	panel.Running:   {"●", "running", lipgloss.Color("42")},    // green
	panel.Idle:      {"●", "idle", lipgloss.Color("220")},      // amber
	panel.Attention: {"◆", "attention", lipgloss.Color("203")}, // red
	panel.Exited:    {"○", "exited", lipgloss.Color("244")},    // gray
}

// stateOrder is the display order for the summary strip's chips.
var stateOrder = []panel.State{panel.Attention, panel.Running, panel.Idle, panel.Spawning, panel.Exited}

// stateCounts tallies panels by lifecycle state, the shared input to the fleet
// summary strip and a group's per-state chips.
func stateCounts(panels []panel.Panel) map[panel.State]int {
	counts := make(map[panel.State]int, len(stateOrder))
	for _, p := range panels {
		counts[p.State]++
	}
	return counts
}

// kindCounts tallies panels by kind, the shared input to a kind breakdown.
func kindCounts(panels []panel.Panel) (agents, shells int) {
	for _, p := range panels {
		if p.IsAgent() {
			agents++
		} else {
			shells++
		}
	}
	return agents, shells
}

// mergeFleet maps a server snapshot into the dashboard's panel model. The server
// owns the fleet now, so this is a faithful translation — whatever it sends is
// what the cockpit shows.
func mergeFleet(panels []proto.Panel) []panel.Panel {
	out := make([]panel.Panel, len(panels))
	for i, p := range panels {
		out[i] = panel.FromProto(p)
	}
	return out
}

// activeState reports whether a state is live enough to animate — running,
// attention, or spawning — as opposed to resting (idle) or done (exited). A
// group shows its sparkline only when it rolls up to one of these.
func activeState(s panel.State) bool {
	return s == panel.Running || s == panel.Attention || s == panel.Spawning
}

// panelSpark is a panel's sparkline: the Monitor's live output-rate bars when the
// server has reported them, falling back to the state-keyed placeholder for mock
// panels or before the first telemetry tick.
func panelSpark(p panel.Panel) string {
	if p.Spark != "" {
		return p.Spark
	}
	return sparkFor(p.State)
}

// groupSpark is the sparkline a work-item card animates with: the live bars of the
// member the group rolls up to, so the card breathes with the panel that speaks
// for it. Falls back to the rolled-up state's placeholder when no such member has
// reported telemetry yet.
func groupSpark(members []panel.Panel, rollup panel.State) string {
	for _, p := range members {
		if p.State == rollup && p.Spark != "" {
			return p.Spark
		}
	}
	return sparkFor(rollup)
}

// sparkFor is a placeholder activity sparkline keyed on a panel's state, used
// until the Monitor reports real output rates (mock panels, or the first tick).
func sparkFor(s panel.State) string {
	switch s {
	case panel.Attention:
		return "▂▃▅▇▆▃▁"
	case panel.Running:
		return "▃▅▆▇▆▅▃▅"
	case panel.Idle:
		return "▂▁▁▂▁▁▁▁"
	case panel.Spawning:
		return "▁▁▂▁▁▁▁▁"
	default: // exited
		return "▁▁▁▁▁▁▁▁"
	}
}
