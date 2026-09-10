// Package task defines baton's unit of work: a Task is a prompt assigned to a
// panel, tracked through a lifecycle from queued to a terminal done/failed. It is
// the record the per-panel brief (a bare string) is promoted to, and the model the
// task queue stands on — so the type is self-contained here, serialisable for the
// wire and (later) the on-disk store, with no dependency on the server.
package task

import (
	"encoding/json"
	"time"
)

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

// Author is the conclusion about WHO put a task in the backlog. It is one value
// rather than a pair of bools because the states are mutually exclusive and two
// adjacent bools at a call site can be swapped without breaking a build — and
// here the swap would silently let a plugin's own enqueue reach the tier #37
// reserves for the operator.
//
// It is decided from the CONNECTION and from nothing else (#38 §4). No enqueue
// command carries an author field, so there is nothing for a client to assert.
//
// It lives in this package rather than in the server because it is a fact about
// a TASK, and a task outlives the daemon that took it in: whatever reads a
// backlog file back has to be able to spell it. It is a string for the same
// reason Status is one — the store's own doc promises a task file is inspectable
// and editable from outside baton, which an integer enum on disk is not, besides
// renumbering every stored task the day a value is inserted in the middle.
type Author string

const (
	// AuthorUnknown is the zero value, and it is a state rather than a default:
	// nobody was concluded. A task file written before the author was recorded
	// says nothing about who queued it that is not already in the plugin key, so
	// reading its absence as any of the three below would be a guess wearing the
	// authority of a record. No live road mints it; see Task.Author.
	AuthorUnknown Author = ""
	// AuthorAgent is a connection that declared a self on hello: an agent inside a
	// panel driving the backlog. The commonest, and the safe one — a stamp that
	// goes wrong in this direction only loses a reinforcement.
	AuthorAgent Author = "agent"
	// AuthorUser is a connection that declared no self: the TUI, or `baton ctl`
	// from the operator's own shell.
	AuthorUser Author = "user"
	// AuthorPlugin is baton.enqueue, called from inside the Lua worker. There is no
	// connection at all, and it is emphatically not the user.
	AuthorPlugin Author = "plugin"
)

// SpawnSpec is a queued task's optional request to provision its own agent: when
// the scheduler finds no free agent, it spawns one running Command with Args in
// Dir, dispatches the task there, and — if CloseOnDone — closes that panel once the
// task settles, so the backlog can drain onto fresh, ephemeral workers instead of
// waiting on the standing fleet. A nil Spawn means the task only rides an existing
// idle agent.
type SpawnSpec struct {
	Command     string   `json:"command"`                 // agent CLI to run (already resolved from a profile by the caller)
	Profile     string   `json:"profile,omitempty"`       // the profile that command came from, so the provisioned agent resolves that profile's resource limits
	Args        []string `json:"args,omitempty"`          // arguments to the command
	Dir         string   `json:"dir,omitempty"`           // working directory ("" = the server default)
	CloseOnDone bool     `json:"close_on_done,omitempty"` // close the spawned panel when the task finishes
}

// Task is one unit of work: the prompt assigned to a panel, its lifecycle status,
// and the bookkeeping the queue and retries need. Its identity is the unit of
// work, not the panel — the same Task id survives a reassign or respawn, with
// Attempts counting each delivery.
//
// Author records WHO queued the task, and it is here rather than in the server
// because it has to survive a restart: a plugin's task.pre filter runs when the
// task is delivered, so the daemon that delivers it may not be the one that took
// it in. A plugin-originated task is delivered bare, bypassing that filter, which
// is what stops a hook that calls baton.enqueue from re-entering itself once per
// delivery.
//
// It is the ONLY stored answer to who queued a task, deliberately. What it
// replaces answered a third of the question with a bool that said only whether a
// plugin queued it, and keeping that bool beside this field would be two
// spellings of one fact with nothing holding them together. Plugin-originated is
// asked as Author == AuthorPlugin at the two sites that care.
//
// It is also the only DURABLE answer, which is the defect it closes. UserSignal
// below is spent at the assignment that delivers the task, so it correctly reads
// false on every task in flight — and "not a plugin, no signal" was therefore
// indistinguishable from "the operator's, already delivered". Nothing could ask
// who queued a task. The author is a separate fact for that reason, and never a
// second permission.
//
// A backlog file written before this field carries the old plugin key and no
// author, and UnmarshalJSON promotes it to AuthorPlugin — a migration on read,
// so no file has to be rewritten. A file with NEITHER restores as AuthorUnknown,
// which is the truth: the old shape recorded a plugin's authorship and nothing
// else, and the operator's stamp it did carry was a permission already spent.
// Reading that absence as AuthorAgent because agent is the zero value would be a
// guess wearing the authority of a record.
//
// One consequence is accepted rather than covered, and it is the one this field
// inherited: a baton.enqueue task predating BOTH keys restores as AuthorUnknown,
// so it is filtered once, at its first delivery after the upgrade — a hook that
// enqueues would see its own earlier task exactly once. That is a one-shot,
// bounded by the backlog that survived the restart, and it only ever runs the
// chain over work the chain was going to see anyway. Downgrading costs exactly
// the same and no more: the plugin key is no longer written, so a task queued by
// this build and read by one predating Author is filtered once on those terms.
//
// UserSignal is NOT the other half of Author, and the name is the difference.
// Author is a durable fact about where the task came from, true for as long as
// the task exists. UserSignal is a PERMISSION, worth exactly one reinforcement,
// and the scheduler spends it on the assignment that takes it — so on a task in
// flight it reads false, and that is the field working, not the field lying. See
// Server.takeUserSignalLocked, which is the only thing that spends it, and which
// says which way the spend is lossy.
//
// It is here rather than in the server for #50, and the reason is a gap in time:
// a brief the operator ENQUEUED reinforces what it repeats exactly as a
// dispatched one does, but the connection that enqueued it is long gone by the
// time the scheduler drains the task onto a panel, and a queued task routinely
// outlives the daemon that took it in. Persisting it is what carries the
// server's reading of that connection across both gaps.
//
// IT IS THE SERVER'S CONCLUSION, NOT A CLAIM. Nothing on the wire can set it: the
// enqueue command carries no such field, and the value is decided at enqueue from
// Server.connProvenance — the one discrimination #38 §4 allows — under the lock
// that creates the task. That a user connection's provenance has exactly ONE
// value ({Source: SourceUser}, with no panel, cwd, profile or group, because a
// cockpit has no panel row) is what lets a single bool carry the whole of it
// without loss. An agent's enqueue is stamped false and stays false.
//
// It is a claim to anyone who can write the backlog files directly — which is the
// same exposure #38's Trust and exposure section already accepts for score.md
// itself, and no worse: an agent that can edit this file can edit that one.
//
// Absent on a task from an older build for the same reason Plugin is, and the
// absence means the same thing it means for a connection that never declared
// itself the user: no signal. A backlog that survived the upgrade counts nothing,
// which is the safe direction — invariant I6 is about entries climbing on
// something that was not the user, so the failure to fold is the tolerable half.
//
// The JSON key stays "user": the field is renamed, the file format is not, so a
// backlog written by an older build restores with its stamp intact.
type Task struct {
	ID         string     `json:"id"`
	Prompt     string     `json:"prompt"`
	Status     Status     `json:"status"`
	Panel      string     `json:"panel,omitempty"`    // the panel currently executing it, if any
	Group      string     `json:"group,omitempty"`    // the work item it belongs to, if any
	Result     string     `json:"result,omitempty"`   // a terminal note (e.g. a failure reason)
	Priority   int        `json:"priority,omitempty"` // scheduler order among queued tasks: higher drains first (default 0, ties break oldest-first)
	Attempts   int        `json:"attempts"`           // how many times its prompt has been delivered
	Spawn      *SpawnSpec `json:"spawn,omitempty"`    // provision a fresh agent for this task when none is free (nil = existing agents only)
	Author     Author     `json:"author,omitempty"`   // WHO queued it, durable and never mutated; absent means AuthorUnknown, see below
	UserSignal bool       `json:"user,omitempty"`     // ONE-SHOT permission to count the operator's reinforcement, spent at assignment; see below
	Created    time.Time  `json:"created"`
	Updated    time.Time  `json:"updated"`
}

// UnmarshalJSON restores a task, promoting the plugin key a build before Author
// wrote to AuthorPlugin when the file names no author of its own. That is the
// whole of the migration: a backlog on disk is read forward and never rewritten,
// and a file carrying both is read by its author, which is the newer fact.
//
// A file with neither is left at AuthorUnknown rather than filled in. The
// information was never recorded — the only other origin the old shape kept was
// a one-shot permission, spent at the first delivery — so there is nothing to
// migrate from, and a guess here would put a claim where a gap belongs.
func (t *Task) UnmarshalJSON(data []byte) error {
	type stored Task // stripped of this method, so decoding it cannot re-enter here
	var v struct {
		stored
		Plugin bool `json:"plugin"` // the pre-Author spelling of AuthorPlugin
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*t = Task(v.stored)
	if t.Author == AuthorUnknown && v.Plugin {
		t.Author = AuthorPlugin
	}
	return nil
}
