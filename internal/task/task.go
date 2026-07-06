// Package task defines baton's unit of work: a Task is a prompt assigned to a
// panel, tracked through a lifecycle from queued to a terminal done/failed. It is
// the record the per-panel brief (a bare string) is promoted to, and the model the
// task queue stands on — so the type is self-contained here, serialisable for the
// wire and (later) the on-disk store, with no dependency on the server.
package task

import "time"

// Status is where a task sits in its lifecycle.
type Status string

// The task lifecycle states. A task is queued when held for a not-yet-ready
// panel, dispatched once its prompt is delivered, running while the agent works
// it, and then terminal — done when the agent settles, failed when the panel dies
// under it.
const (
	Queued     Status = "queued"
	Dispatched Status = "dispatched"
	Running    Status = "running"
	Done       Status = "done"
	Failed     Status = "failed"
)

// Terminal reports whether a task has reached an end state and will not advance
// further (its file, once persisted, can be removed).
func (s Status) Terminal() bool { return s == Done || s == Failed }

// CanAdvance reports whether a task may move from one status to another. A
// terminal status never advances; otherwise the lifecycle only moves forward
// (queued → dispatched → running → done), and any non-terminal status can fail.
func CanAdvance(from, to Status) bool {
	if from.Terminal() {
		return false
	}
	switch to {
	case Dispatched:
		return from == Queued
	case Running:
		return from == Queued || from == Dispatched
	case Done:
		return from == Running
	case Failed:
		return true
	}
	return false
}

// Task is one unit of work: the prompt assigned to a panel, its lifecycle status,
// and the bookkeeping the queue and retries need. Its identity is the unit of
// work, not the panel — the same Task id survives a reassign or respawn, with
// Attempts counting each delivery.
type Task struct {
	ID       string    `json:"id"`
	Prompt   string    `json:"prompt"`
	Status   Status    `json:"status"`
	Panel    string    `json:"panel,omitempty"`    // the panel currently executing it, if any
	Group    string    `json:"group,omitempty"`    // the work item it belongs to, if any
	Result   string    `json:"result,omitempty"`   // a terminal note (e.g. a failure reason)
	Priority int       `json:"priority,omitempty"` // scheduler order among queued tasks: higher drains first (default 0, ties break oldest-first)
	Attempts int       `json:"attempts"`           // how many times its prompt has been delivered
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
}
