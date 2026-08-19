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

// The manager's own keys. They are the overlay set every other overlay uses —
// x removes the row under the cursor, X clears the lot — rather than a private
// alphabet: this popup used to spend d on cancel (d is diff elsewhere), D on
// drain (D is diff elsewhere too) and K/J on reordering (K was the keycast
// toggle, and reordering is shift+arrows on the dashboard and in the split).
const (
	keyQueueCancel = "x" // cancel the highlighted queued task
	keyQueueDrain  = "X" // drain every queued task, after a y/n
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
	m.status = "task queue · jk move · S-↑↓ reorder · " + keyQueueCancel + " cancel · " + keyQueueDrain + " drain · esc closes"
	return m
}

// handleQueueKey drives the manager: j/k and the arrows move the cursor, g/G jump
// to the ends, x cancels the queued task under it, X drains the whole backlog
// after a y/n, shift+arrows reorder it, e is reserved for an editor pass, and
// q/esc close. A cancel of an in-flight task is refused by the server and
// surfaced on the status line — the popup stays open on the unchanged list. Any
// other key is ignored so a stray press never mutates the queue.
func (m model) handleQueueKey(key string) (tea.Model, tea.Cmd) {
	// Draining is waiting on a y/n. It throws away work nobody can get back, so
	// it asks first, like closing a panel does.
	if m.pendingDrain {
		m.pendingDrain = false
		if key == "y" || key == "enter" {
			return m.drainQueue(), nil
		}
		m.status = "drain cancelled"
		return m, nil
	}

	switch key {
	case "esc", "q":
		m.mode = m.queueFrom
		m.status = "queue closed"
		return m, nil
	case "up", "k":
		if len(m.tasks) > 0 {
			m.queueCursor = wrapIndex(m.queueCursor, -1, len(m.tasks))
		}
		return m, nil
	case "down", "j":
		if len(m.tasks) > 0 {
			m.queueCursor = wrapIndex(m.queueCursor, 1, len(m.tasks))
		}
		return m, nil
	case "g", "home":
		m.queueCursor = 0
		return m, nil
	case "G", "end":
		if len(m.tasks) > 0 {
			m.queueCursor = len(m.tasks) - 1
		}
		return m, nil
	case "shift+up", "shift+left":
		return m.reprioritizeQueued(true), nil
	case "shift+down", "shift+right":
		return m.reprioritizeQueued(false), nil
	case keyQueueCancel:
		return m.cancelQueued(), nil
	case keyQueueDrain:
		if len(m.tasks) == 0 {
			m.status = "queue: already empty"
			return m, nil
		}
		m.pendingDrain = true
		m.status = "drain the whole backlog? this cancels every queued task · (y/n)"
		return m, nil
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

// reprioritizeQueued bumps the task under the cursor to the head (up) or tail
// (down) of the backlog. Only a waiting task can be reordered; the server refuses
// an in-flight or finished one and the refusal rides the status line. The reply is
// a fresh, reordered snapshot.
func (m model) reprioritizeQueued(up bool) model {
	t, ok := m.taskUnderCursor()
	if !ok {
		m.status = "queue: nothing to reorder"
		return m
	}
	if up {
		m.sendf(proto.Command{Action: "task.promote", ID: t.ID})
		m.status = "promoting " + t.ID + " to the head"
	} else {
		m.sendf(proto.Command{Action: "task.demote", ID: t.ID})
		m.status = "demoting " + t.ID + " to the tail"
	}
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
		return m.popupBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
	}

	rows = append(rows, mutedStyle.Render(fmt.Sprintf("%d task(s) · newest first", len(m.tasks))), "")
	for i, t := range m.tasks {
		st := badge.Foreground(queueStatusColor(t.Status)).Render(t.Status)
		grp := ""
		if t.Group != "" {
			grp = t.Group
		}
		id := t.ID
		if t.Spawn { // provisions its own agent when none is free
			id += "⚡"
		}
		row := caret(m.queueCursor == i) + st + idCol.Render(id) + grpCol.Render(grp) + inkStyle.Render(truncate(t.Prompt, 40))
		if t.Result != "" { // a finished task's terminal note — why it failed
			row += mutedStyle.Render("  — " + truncate(t.Result, 24))
		}
		rows = append(rows, row)
	}

	rows = append(rows, "",
		mutedStyle.Render("in-flight tasks finish on their panel"),
		"", legend("jk", "move", "S-↑↓", "reorder", keyQueueCancel, "cancel", keyQueueDrain, "drain all", "esc", "close"))
	return m.popupBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
}
