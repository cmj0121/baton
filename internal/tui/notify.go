package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/panel"
)

// The attention notifications: everything the cockpit does to TELL a human a
// panel wants them, as opposed to the queue (inbox.go) they work through once
// they are already looking.
//
// The surfaces are deliberately independent, because each reaches a different
// moment. The status pop is what you catch if you happen to be watching the
// footer. The bell is what reaches you when you are not, but are still at this
// desk. The badge is what is still there when you come back. None of them
// suppresses another: dropping one because a louder one fired would silently
// disable the escalation exactly when it was needed.
//
// The rising edge they all hang off is computed once, in refreshAttention — a
// panel is announced when it ENTERS the state, not every tick it sits in it.

// refreshAttention fires a footer notification on the rising edge of a panel
// entering the attention state — when the Monitor decides it needs you. It tracks
// the set of panels currently flagged (attnSeen) so the pop fires once per entry,
// not every tick a panel sits waiting; a panel that resolves and later needs you
// again notifies afresh. The persistent count lives in the footer badge; this is
// the one-shot nudge that names the panel the moment it calls for you. An error
// status is left in place — it is not noise to bury under a notification.
func (m *model) refreshAttention() {
	cur := make(map[string]bool)
	var fresh []string
	// Inline singleton skip (not m.visibleFleet()): this fires on every snapshot and
	// telemetry tick, so it avoids allocating a filtered slice per event.
	for _, p := range m.fleet {
		if p.Conductor || p.GlobalShell || p.State != panel.Attention {
			continue
		}
		cur[p.ID] = true
		if !m.attnSeen[p.ID] {
			fresh = append(fresh, p.Title)
		}
	}
	m.attnSeen = cur
	if len(fresh) == 0 {
		return
	}
	if m.bellEnabled {
		m.bellPending = true // audible nudge on the rising edge, even when an error status hides the text
	}
	if strings.HasPrefix(m.status, "error") {
		return
	}
	if len(fresh) == 1 {
		m.status = "◆ " + fresh[0] + " needs you"
	} else {
		m.status = fmt.Sprintf("◆ %d panels need your attention", len(fresh))
	}
}

// bell rings the terminal once by writing the BEL control byte to the tty. It is
// emitted as a command so it rides bubbletea's own output cycle; BEL prints no
// glyph and moves no cursor, so it never disturbs the alt-screen the cockpit
// draws. Sent to stderr to stay off the renderer's stdout stream.
func bell() tea.Msg {
	_, _ = os.Stderr.WriteString("\a")
	return nil
}

// takeBell returns the bell command once when a panel has just entered attention,
// clearing the pending flag so the nudge sounds a single time per rising edge.
func (m *model) takeBell() tea.Cmd {
	if !m.bellPending {
		return nil
	}
	m.bellPending = false
	return bell
}

// attentionBadge is the footer notification that some panel needs you: a red cap
// carried by every view's status bar, so a panel asking for input is visible
// whether you are on the dashboard, in a zoom, or in a group split. It names the
// panel when exactly one waits, and counts them when several do. Empty when the
// fleet is calm.
func (m model) attentionBadge() string {
	var names []string
	// Range m.fleet with an inline singleton skip rather than m.visibleFleet(): this
	// runs in every view's footer on every frame, so it must not allocate a slice.
	for _, p := range m.fleet {
		if !p.Conductor && !p.GlobalShell && p.State == panel.Attention {
			names = append(names, p.Title)
		}
	}
	if len(names) == 0 {
		return ""
	}
	label := fmt.Sprintf("◆ %d need you", len(names))
	if len(names) == 1 {
		label = "◆ " + truncate(names[0], 16) + " needs you"
	}
	return seg(label, colDark, states[panel.Attention].color)
}
