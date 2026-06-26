package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
)

// The task-queue manager popup (modeQueue, opened with Q / C-t Q). It is the
// frontend's window onto the server-owned backlog: the same list the ctl and MCP
// surfaces print, made navigable so a task can be cancelled or the whole backlog
// drained without leaving the cockpit. Every mutation is a server action — the
// popup owns no state of its own beyond the cursor — so the reply (a fresh "tasks"
// snapshot) is what redraws the list.

// queueManageKeys: cancel the task under the cursor, drain the whole backlog.
const (
	keyQueueCancel = "d" // cancel the highlighted queued task
	keyQueueDrain  = "D" // drain every queued task (shift+d)
	keyQueueEdit   = "e" // edit the highlighted task (planned follow-up)
)

// openQueue opens the manager over the current view, remembering from so esc
// returns there. It asks the server for the current backlog; the "tasks" reply
// fills the list (and refreshes it after every later mutation). The popup opens
// immediately on whatever snapshot is already in hand, so it never blocks on the
// round-trip.
func (m model) openQueue(from mode) model {
	m.queueFrom = from
	m.queueCursor = 0
	m.mode = modeQueue
	m.sendf(proto.Command{Action: "task.list"})
	m.status = "task queue · ↑↓ move · d cancel · D drain · esc closes"
	return m
}

// handleQueueKey drives the manager: ↑↓ move the cursor, d cancels the queued task
// under it, D drains the whole backlog, e is reserved for an editor pass, and esc
// closes. A cancel of an in-flight task is refused by the server and surfaced on
// the status line — the popup stays open on the unchanged list. Any other key is
// ignored so a stray press never mutates the queue.
func (m model) handleQueueKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = m.queueFrom
		m.status = "queue closed"
		return m, nil
	case "up":
		if len(m.tasks) > 0 {
			m.queueCursor = wrapIndex(m.queueCursor, -1, len(m.tasks))
		}
		return m, nil
	case "down":
		if len(m.tasks) > 0 {
			m.queueCursor = wrapIndex(m.queueCursor, 1, len(m.tasks))
		}
		return m, nil
	case keyQueueCancel:
		return m.cancelQueued(), nil
	case keyQueueDrain:
		return m.drainQueue(), nil
	case keyQueueEdit:
		// Editing a brief means handing it to $EDITOR, which in baton runs as a
		// server-owned PTY panel (like a git commit), not a frontend shell-out — a
		// planned follow-up. Until then the popup manages, not edits.
		m.status = "edit: not yet — re-dispatch or cancel and re-enqueue for now"
		return m, nil
	}
	return m, nil // stay in the popup on any other key
}

// cancelQueued cancels the task under the cursor. Only a queued, unassigned task
// can be cancelled; the server refuses one already in flight on a panel, and that
// refusal rides the status line. The reply is a fresh backlog snapshot.
func (m model) cancelQueued() model {
	t, ok := m.taskUnderCursor()
	if !ok {
		m.status = "queue: nothing to cancel"
		return m
	}
	m.sendf(proto.Command{Action: "task.cancel", ID: t.ID})
	m.status = "cancelling " + t.ID
	return m
}

// drainQueue clears every unassigned queued task. In-flight tasks are left to
// finish — draining the backlog is not stopping the fleet. The reply refreshes the
// list to whatever survived.
func (m model) drainQueue() model {
	if len(m.tasks) == 0 {
		m.status = "queue: already empty"
		return m
	}
	m.sendf(proto.Command{Action: "task.drain"})
	m.status = "draining the queued backlog"
	return m
}

// taskUnderCursor returns the highlighted task, if the list is non-empty and the
// cursor is in range (it can trail the list for a tick after a drain).
func (m model) taskUnderCursor() (proto.Task, bool) {
	if m.queueCursor < 0 || m.queueCursor >= len(m.tasks) {
		return proto.Task{}, false
	}
	return m.tasks[m.queueCursor], true
}

// queueStatusColor maps a task status to its badge colour, mirroring the panel
// state palette: queued is muted, dispatched cyan, running green, done blue, and a
// failure red.
func queueStatusColor(status string) lipgloss.Color {
	switch status {
	case "running":
		return colGreen
	case "dispatched":
		return colCyan
	case "done":
		return colBrandHi
	case "failed":
		return colRed
	default: // queued
		return colMuted
	}
}

// queueView renders the manager as a centred popup: a header, one row per task
// (cursor caret · status badge · id · group · the brief), and a legend. An empty
// backlog says so rather than showing a bare frame.
func (m model) queueView() string {
	caret := func(on bool) string {
		if on {
			return lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ ")
		}
		return "  "
	}
	badge := lipgloss.NewStyle().Width(11)
	idCol := lipgloss.NewStyle().Foreground(colCyan).Width(6)
	grpCol := lipgloss.NewStyle().Foreground(colMuted).Width(10)

	rows := []string{
		sectionStyle.Render(spaced("TASK QUEUE")),
		"",
	}
	if len(m.tasks) == 0 {
		rows = append(rows,
			mutedStyle.Render("the backlog is empty · dispatch or enqueue to fill it"),
			"",
			legend("esc", "close"))
		return configBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
	}

	rows = append(rows, mutedStyle.Render(fmt.Sprintf("%d task(s) · newest first", len(m.tasks))), "")
	for i, t := range m.tasks {
		st := badge.Foreground(queueStatusColor(t.Status)).Render(t.Status)
		grp := ""
		if t.Group != "" {
			grp = t.Group
		}
		row := caret(m.queueCursor == i) + st + idCol.Render(t.ID) + grpCol.Render(grp) + inkStyle.Render(truncate(t.Prompt, 40))
		rows = append(rows, row)
	}

	rows = append(rows, "",
		mutedStyle.Render("d cancels a queued task · in-flight tasks finish on their panel"),
		"", legend("↑↓", "move", "d", "cancel", "D", "drain all", "esc", "close"))
	return configBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
}
