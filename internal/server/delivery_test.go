package server

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/queue"
	"github.com/cmj0121/baton/internal/task"
)

// deliveryServer is one free agent and nothing else: enough for the scheduler to
// drain a queued task onto it and for the monitor to deliver the result. The
// profile is wired through specs because that is where panelContext reads it.
func deliveryServer() (*Server, *[]string) {
	s, _, written := gateServer(panel.Panel{
		ID: "a1", Kind: panel.Agent, State: panel.Idle, Group: "auth", Cwd: "/work/auth",
	})
	s.specs["a1"] = spawnSpec{Profile: "claude"}
	return s, written
}

// TestEnqueueDefersTheFilterToDelivery is #44 decision 1: the whole task.pre pass
// moved off enqueue. The command queues the task without consulting a hook, and
// the chain runs once, later, against the panel the scheduler picked — which is
// the first moment there is a cwd, profile and group to hand it.
func TestEnqueueDefersTheFilterToDelivery(t *testing.T) {
	s, written := deliveryServer()
	var seen []TaskBrief
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { seen = append(seen, b); return b, true }

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "task.enqueue", Group: "auth", Prompt: "later"})
	noError(t, cc)
	if len(seen) != 0 {
		t.Fatalf("task.enqueue ran the task.pre chain: %+v", seen)
	}

	s.monitorTick() // the scheduler drains the backlog onto the idle agent

	if len(seen) != 1 {
		t.Fatalf("the chain ran %d times at delivery, want exactly one pass", len(seen))
	}
	want := TaskBrief{Prompt: "later", Panel: "a1", Group: "auth", Cwd: "/work/auth", Profile: "claude"}
	if seen[0] != want {
		t.Fatalf("delivery brief = %+v, want %+v", seen[0], want)
	}
	if len(*written) != 1 || (*written)[0] != "a1:later\n" {
		t.Fatalf("delivered %v, want the prompt on a1", *written)
	}
}

// TestTheDeliveryFilterRunsOffTheLock is the constraint the whole shape of #44
// was chosen for. task.pre goes through the Lua worker behind a 2s fail-open
// timeout, so a hook that hangs while s.mu is held stalls every connection on
// the socket for two seconds per queued task. scheduleLocked therefore assigns
// under the lock and hands the deliveries back for the caller to bind after it
// has let go.
//
// TryLock is the assertion because nothing else can tell the two apart from
// inside the hook: a mutex the monitor still holds cannot be taken again.
func TestTheDeliveryFilterRunsOffTheLock(t *testing.T) {
	s, _ := deliveryServer()
	var ran, held bool
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) {
		ran = true
		if s.mu.TryLock() {
			s.mu.Unlock()
		} else {
			held = true
		}
		return b, true
	}
	if _, err := s.enqueueTask("later", "auth", nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	s.monitorTick()

	if !ran {
		t.Fatal("the chain never ran, so the lock was never tested")
	}
	if held {
		t.Fatal("the task.pre chain ran under s.mu")
	}
}

// TestAVetoAtDeliveryEndsTheTaskInTheBacklog is #44 decision 2. The connection
// that enqueued the task may have closed hours before the scheduler drained it,
// so the refusal cannot be an error to a caller: it is a terminal task carrying
// the reason, where its owner looks.
//
// The panel has to come back with it. The task is already marked dispatched on
// an agent by the time a hook can say no, and an agent left holding a task
// nobody will deliver is one the scheduler would never offer work to again — so
// the group's concurrency cap is set to one here, and the second task drains
// only if the first really did let go of its slot.
func TestAVetoAtDeliveryEndsTheTaskInTheBacklog(t *testing.T) {
	s, written := deliveryServer()
	s.queueConcurrency = 1
	s.onFilterTask = func(TaskBrief) (TaskBrief, bool) { return TaskBrief{}, false }

	id, err := s.enqueueTask("later", "auth", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	s.monitorTick()

	tk := s.tasks[id]
	if tk == nil || tk.Status != task.Failed || tk.Result != vetoReason {
		t.Fatalf("task = %+v, want it failed in the backlog with the veto's reason", tk)
	}
	if len(*written) != 0 {
		t.Fatalf("a vetoed delivery wrote %v", *written)
	}
	if s.panels[0].Task != "" {
		t.Fatalf("the card still shows %q, want the vetoed brief cleared", s.panels[0].Task)
	}
	if got, ok := s.panelTask["a1"]; ok {
		t.Fatalf("the panel is still mapped to task %q", got)
	}

	// The slot is the other half: with the cap at one, a leaked in-flight task
	// would keep the group full for ever and the next task would never drain.
	s.onFilterTask = nil
	if _, err := s.enqueueTask("next", "auth", nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	s.monitorTick()
	if len(*written) != 1 || (*written)[0] != "a1:next\n" {
		t.Fatalf("delivered %v, want the next task on the released agent", *written)
	}
}

// TestARewriteIsDeliveredButNeverRecorded is the invariant that makes a rewriting
// hook idempotent across restarts. The chain runs at delivery now, and
// restoreTasksLocked re-queues an in-flight task on every restart — so a task that
// carried its own rewrite would be handed to the hook again and rewritten again,
// once more per restart, at the same version and with no operator action. The
// prompt an author wrote is therefore the task's identity for good; the rewrite
// reaches the agent and nothing else.
//
// It is also the answer to "what did I actually ask for", which an overwritten
// prompt does not have.
func TestARewriteIsDeliveredButNeverRecorded(t *testing.T) {
	s, written := deliveryServer()
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { b.Prompt = "[R] " + b.Prompt; return b, true }

	id, err := s.enqueueTask("later", "auth", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	s.monitorTick()

	if len(*written) != 1 || (*written)[0] != "a1:[R] later\n" {
		t.Fatalf("delivered %v, want the rewrite on the wire", *written)
	}
	if got := s.tasks[id].Prompt; got != "later" {
		t.Fatalf("task prompt = %q, want the operator's own words kept", got)
	}
	if got := s.panels[0].Task; got != "later" {
		t.Fatalf("card = %q, want the operator's own words kept", got)
	}
}

// TestARewriteIsNotCompoundedByARestart drives the loop SRE reproduced on live
// daemons, through the machinery that actually produced it: a direct dispatch is
// recorded and persisted, a SECOND daemon loads that backlog file and re-queues
// the task restoreTasksLocked finds in flight, and the redelivery must send
// [R] ORIG rather than [R] [R] ORIG.
//
// The round trip is the point. A rewrite can only compound if it reaches the
// queue file, so a test that hand-edited the in-memory task would prove nothing
// about the link that failed — JSON on disk, written by one process and read by
// another.
func TestARewriteIsNotCompoundedByARestart(t *testing.T) {
	dir := t.TempDir()
	rewrite := func(b TaskBrief) (TaskBrief, bool) { b.Prompt = "[R] " + b.Prompt; return b, true }

	// The daemon that takes the dispatch in. Its panel is idle, so the brief goes
	// out at once, rewritten; what it records is the operator's own text.
	first, _, sentFirst := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Idle})
	first.qstore = queue.New(dir, time.Now)
	first.onFilterTask = rewrite

	cc := conn("")
	first.onCommand(cc, proto.Command{Action: "panel.dispatch", ID: "a1", Prompt: "ORIG"})
	if len(*sentFirst) != 1 || (*sentFirst)[0] != "a1:[R] ORIG\n" {
		t.Fatalf("first daemon sent %v, want the rewrite delivered", *sentFirst)
	}
	tk := taskFor(first, "a1")
	if tk == nil || tk.Prompt != "ORIG" {
		t.Fatalf("recorded task = %+v, want the operator's own text", tk)
	}
	if err := first.qstore.Save(*tk); err != nil { // what the task saver mirrors to disk
		t.Fatalf("save: %v", err)
	}

	// The daemon that comes up after the restart, on the same backlog directory.
	second, _, sentSecond := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Idle})
	second.qstore = queue.New(dir, time.Now)
	second.onFilterTask = rewrite
	second.mu.Lock()
	second.restoreTasksLocked()
	second.mu.Unlock()

	if got := second.tasks[tk.ID]; got == nil || got.Prompt != "ORIG" || got.Status != task.Queued {
		t.Fatalf("restored task = %+v, want the operator's text back in the backlog", got)
	}

	second.monitorTick() // the scheduler redrives it onto the idle agent

	if len(*sentSecond) != 1 || (*sentSecond)[0] != "a1:[R] ORIG\n" {
		t.Fatalf("after the restart the agent got %v, want the rewrite applied exactly once", *sentSecond)
	}
}

// TestASupersededQueuedDeliveryIsDropped is the window binding opened. Assignment
// and write used to be microseconds apart; a hook now sits between them for up to
// two seconds, and a panel.dispatch that lands in that gap would be delivered,
// recorded, and then overwritten by the queued brief already in flight — leaving
// the agent holding the stale one and the card agreeing with it. The hook does the
// dispatching here, which is the one place a test can be sure of the interleaving.
func TestASupersededQueuedDeliveryIsDropped(t *testing.T) {
	s, written := deliveryServer()
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) {
		s.onFilterTask = nil // the interloper must not recurse through the chain
		if err := s.dispatchPanel("a1", "urgent", ""); err != nil {
			t.Errorf("dispatch: %v", err)
		}
		return b, true
	}

	if _, err := s.enqueueTask("later", "auth", nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	s.monitorTick()

	if len(*written) != 1 || (*written)[0] != "a1:urgent\n" {
		t.Fatalf("wrote %v, want only the dispatch that superseded the queued one", *written)
	}
	if got := s.panels[0].Task; got != "urgent" {
		t.Fatalf("card = %q, want the brief the panel actually received", got)
	}
}

// TestASupersededVetoLeavesTheNewTaskAlone is the same window on the other branch.
// A veto walks the task back, and the task it walks back must be the one the hook
// was shown — not whatever has since taken the panel over.
func TestASupersededVetoLeavesTheNewTaskAlone(t *testing.T) {
	s, written := deliveryServer()
	s.onFilterTask = func(TaskBrief) (TaskBrief, bool) {
		s.onFilterTask = nil
		if err := s.dispatchPanel("a1", "urgent", ""); err != nil {
			t.Errorf("dispatch: %v", err)
		}
		return TaskBrief{}, false
	}

	if _, err := s.enqueueTask("later", "auth", nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	s.monitorTick()

	tk := taskFor(s, "a1")
	if tk == nil || tk.Status.Terminal() {
		t.Fatalf("task = %+v, want the superseding dispatch still live on the panel", tk)
	}
	if got := s.panels[0].Task; got != "urgent" {
		t.Fatalf("card = %q, want the superseding brief left standing", got)
	}
	if len(*written) != 1 || (*written)[0] != "a1:urgent\n" {
		t.Fatalf("wrote %v, want the superseding dispatch delivered", *written)
	}
}

// deliveryFleet is n idle agents in one group with n tasks queued for them — a
// burst arriving on a single tick, which is the shape the budget is about.
func deliveryFleet(t *testing.T, n int) (*Server, *fakeClock, *[]string) {
	t.Helper()
	panels := make([]panel.Panel, 0, n)
	for i := range n {
		panels = append(panels, panel.Panel{
			ID: fmt.Sprintf("a%d", i), Kind: panel.Agent, State: panel.Idle, Group: "auth",
		})
	}
	s, clk, written := gateServer(panels...)
	for i := range n {
		if _, err := s.enqueueTask(fmt.Sprintf("task %d", i), "auth", nil); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	return s, clk, written
}

// TestAFastTickDeliversTheWholeBurst is why the budget is a duration. A count
// cannot tell a hook that answered in microseconds from one that timed out, so it
// charges the healthy fleet for the pathological one — forty tasks onto forty
// idle agents, with no plugin loaded at all, took twenty times longer under a
// count of four than with no bound. Elapsed time costs a fast tick nothing.
func TestAFastTickDeliversTheWholeBurst(t *testing.T) {
	const burst = 40
	s, _, written := deliveryFleet(t, burst)

	s.monitorTick()

	if len(*written) != burst {
		t.Fatalf("one tick delivered %d of %d, want the whole burst", len(*written), burst)
	}
	if len(s.deferred) != 0 {
		t.Fatalf("deferred %d, want nothing held back from a tick that cost nothing", len(s.deferred))
	}
}

// TestTheDeliveryBudgetBoundsASlowTick is the ceiling itself. The deliver loop
// holds up everything else a tick does — telemetry, idle settling, the reap,
// provisioning — and an unbound delivery can sit on a hook for up to the
// fail-open timeout, so a backlog arriving at once would freeze the fleet view
// for as long as the whole backlog takes. Past the budget the rest wait for the
// next tick, and the ticks in between report.
func TestTheDeliveryBudgetBoundsASlowTick(t *testing.T) {
	s, clk, written := deliveryFleet(t, 4)
	// The budget is measured on the monitor's own clock, so the hook advances it
	// rather than sleeping: the ceiling engages at an exact instant instead of
	// wherever a real millisecond and the scheduler happen to land.
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) {
		clk.add(2 * deliveryBudget) // one delivery alone spends the tick's budget
		return b, true
	}

	s.monitorTick()
	if len(*written) != 1 {
		t.Fatalf("the slow tick delivered %d, want it to stop at the budget", len(*written))
	}
	if len(s.deferred) != 3 {
		t.Fatalf("deferred %d, want the rest held for later ticks", len(s.deferred))
	}

	// They are not lost, only spread: each later tick takes one more.
	for range 3 {
		s.monitorTick()
	}
	if len(*written) != 4 || len(s.deferred) != 0 {
		t.Fatalf("after four ticks %d delivered and %d still deferred, want all four through",
			len(*written), len(s.deferred))
	}
}

// TestAPluginQueuedTaskIsDeliveredBare is the guarantee docs/PLUGIN.md makes and
// this issue nearly took away. baton.enqueue is a plugin-originated dispatch, and
// those bypass task.pre so that a hook which enqueues cannot re-enter itself.
// Moving the chain to delivery moved it somewhere the origin was no longer
// visible: the task looked like any other queued one, so the hook that created it
// would be handed it again, and each of those runs could enqueue again.
//
// Not a synchronous recursion — the chain runs later, on the monitor goroutine,
// so the Lua worker never deadlocks. That only makes the loop slow.
func TestAPluginQueuedTaskIsDeliveredBare(t *testing.T) {
	s, written := deliveryServer()
	var seen []TaskBrief
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { seen = append(seen, b); return b, true }

	if _, err := s.Enqueue("later", "auth"); err != nil { // baton.enqueue
		t.Fatalf("enqueue: %v", err)
	}
	s.monitorTick()

	if len(seen) != 0 {
		t.Fatalf("the chain ran over a plugin-originated task: %+v", seen)
	}
	if len(*written) != 1 || (*written)[0] != "a1:later\n" {
		t.Fatalf("delivered %v, want the bare prompt on a1", *written)
	}

	// The same fleet, the same hook, a socket-borne task: the chain runs. Without
	// this the check above would pass just as well on a filter that was never
	// wired. It needs its own server because a1 is still holding the first task.
	other, _ := deliveryServer()
	other.onFilterTask = s.onFilterTask
	if _, err := other.enqueueTask("next", "auth", nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	other.monitorTick()
	if len(seen) != 1 {
		t.Fatalf("the chain ran %d times for a socket task, want once", len(seen))
	}
}

// TestAPluginOriginOutlivesTheDaemon is why the stamp lives on task.Task rather
// than in the server: a queued task can outlive the daemon that took it in, and
// the one that delivers it has only the backlog file to go on. A task written by
// a build without the field reads as socket-borne — accepted rather than
// covered, for the reason task.Task's own doc gives: a baton.enqueue task that
// predates the field is filtered once, at its first delivery after the upgrade.
func TestAPluginOriginOutlivesTheDaemon(t *testing.T) {
	s, _ := deliveryServer()
	id, err := s.Enqueue("later", "auth")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	blob, err := json.Marshal(*s.tasks[id])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back task.Task
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.Plugin {
		t.Fatalf("task round-tripped as %+v, want its plugin origin kept", back)
	}

	var older task.Task
	if err := json.Unmarshal([]byte(`{"id":"t9","prompt":"x","status":"queued","attempts":1}`), &older); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if older.Plugin {
		t.Fatalf("a task file with no origin field read as %+v, want it socket-borne", older)
	}
}
