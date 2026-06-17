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

// sparkFor is a placeholder activity sparkline keyed on a panel's state, derived
// at render time until the Monitor reports real output rates.
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
