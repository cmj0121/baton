package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/queue"
	"github.com/cmj0121/baton/internal/task"
)

// This file covers the second half of #82: one implementation of "spent once",
// and a parked dispatch's reinforcement surviving a restart.
//
// A dispatch to a BUSY panel used to keep the operator's permission only on the
// in-memory delivery, which dies with the process — so a brief the operator
// aimed at a busy panel lost its reinforcement to a reboot while the identical
// brief sent to the backlog kept it. The permission now lives on the task on
// both roads and is spent, on both, by takeUserSignalLocked at the moment the
// delivery is assigned.
//
// WHAT THESE TESTS ARE FOR is the direction the obvious fix gets wrong. Setting
// task.Task.UserSignal in holdDispatchLocked while the parked delivery still
// carried its own copy would leave TWO records of one permission: with no
// restart the settle counts the in-memory one and leaves the stamp behind for a
// later delivery to spend again. So a test that merely dispatches to a busy
// panel and asserts one reinforcement passes before this change and after it,
// and passes under that defect too. Every test here drives a SECOND delivery of
// the same brief — a restart, or a re-drive — because that is the only place the
// second count could land.
//
// The acceptance criterion is one operator act, at most one reinforcement,
// across: no restart, a restart while parked, a restart mid-delivery, a veto,
// and a re-drive after a delivery that failed.

// parkedBriefServer gives s a backlog on disk and starts its saver: what the
// parked road now needs to be asked about, since the whole subject is what
// survives the process.
//
// It hands back the store because every test here waits on the file before it
// restarts, and waiting on the STORE is the wait that is not a race (see
// waitBacklog). It hands back the STOP because a daemon that is supposed to be
// dead must actually be dead: leaving the first server's saver draining while
// the second one restores lets a nudge queued before the restart rewrite the
// file underneath it, which is a race with the whole point of the test and, at
// the end of the run, a save into a directory t.TempDir has already removed.
func parkedBriefServer(t *testing.T, s *Server, qdir string) (*queue.Store, func()) {
	t.Helper()
	s.qstore = queue.New(qdir, time.Now)
	return s.qstore, runSaver(t, s)
}

// waitForBacklogDelivered blocks until the backlog file shows a parked task
// DISPATCHED — the disk's record that a daemon assigned the delivery it was
// holding.
//
// waitForBacklogInFlight is the wrong wait on this road, and wrong in the way
// that makes a test pass for the wrong reason. It waits for a task assigned to a
// panel, and a parked dispatch's task carries its panel from the moment it is
// PARKED: holdDispatchLocked creates it with Panel set, minutes before anything
// is delivered. That wait returns on the file the park wrote, so a restart after
// it would be the restart-while-parked case wearing the other one's name.
//
// It waits on the STATUS rather than on the spent stamp, and that is the
// difference between a test that fails and a test that says why. One save
// carries the status and the spend together — monitorTick takes both under one
// hold of s.mu at the settle, and the saver snapshots under that same lock — so
// the two are equivalent as waits. They are not equivalent as failures: a
// daemon that left the stamp unspent would hang a stamp-wait until its deadline
// and report a timeout, where waiting on the status lets the restart run and the
// count assertion say "2, want 1", which is the defect.
func waitForBacklogDelivered(t *testing.T, qs *queue.Store) {
	t.Helper()
	waitBacklog(t, qs, "recorded the parked delivery", func(tasks []task.Task) bool {
		for _, tk := range tasks {
			if tk.Status == task.Dispatched {
				return true
			}
		}
		return false
	})
}

// TestAParkedBriefSpendsItsPermissionWhenItIsDelivered is the mechanism the two
// restart tests below stand on, asserted where it is visible rather than only
// through its consequences.
//
// The count is not the interesting half — TestABriefParkedAndThenDeliveredCountsExactlyOnce
// already pins that, and pins it just as well on the shape that double counts.
// What this asks is what is LEFT once the delivery has landed: the permission
// must be gone, in memory and on disk, because a stamp that outlives the
// delivery it paid for is a second reinforcement waiting for any later re-drive
// to find.
//
// The disk half is the one that matters. A daemon that spent the stamp only in
// memory would pass every assertion that never reboots.
func TestAParkedBriefSpendsItsPermissionWhenItIsDelivered(t *testing.T) {
	st, _ := scoreStore(t)
	s, clk, delivered := busyScoreServer(st)
	qs, stopSaver := parkedBriefServer(t, s, filepath.Join(t.TempDir(), "backlog"))
	e := seedEntry(t, st, "keep the build green")

	dispatchTo(t, s, conn(""), "Keep the build green.")

	// Parked, and the permission is on the task rather than on the delivery —
	// which is what makes it the fleet's record instead of this process's.
	s.mu.Lock()
	for _, tk := range s.tasks {
		if !tk.UserSignal {
			t.Errorf("the parked task = %+v, want the operator's stamp held for the delivery", tk)
		}
	}
	for pid, d := range s.pendingDispatch {
		if d.signal {
			t.Errorf("the delivery parked for %s carries the permission as well; one act, one record", pid)
		}
	}
	s.mu.Unlock()

	settle(s, clk)
	if !strings.Contains(string(*delivered), "Keep the build green.") {
		t.Fatalf("settling should deliver the parked brief, got %q", string(*delivered))
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 1 {
		t.Fatalf("user signals = %d, want the delivery counted once", got.UserSignals)
	}

	s.mu.Lock()
	for _, tk := range s.tasks {
		if tk.UserSignal {
			t.Errorf("the delivered task = %+v, want its permission spent", tk)
		}
	}
	s.mu.Unlock()

	waitForBacklogDelivered(t, qs)
	stopSaver() // the first daemon is dead from here; only its files reach the next one
	tasks, bad, err := qs.LoadAll()
	if err != nil || len(bad) != 0 {
		t.Fatalf("LoadAll: %v, bad=%v", err, bad)
	}
	if len(tasks) != 1 || tasks[0].UserSignal {
		t.Fatalf("backlog files = %+v, want the spend to have reached disk", tasks)
	}
}

// TestAParkedBriefSurvivesADaemonRestart is #82's second half itself, and the
// one that fails outright on the code it replaces.
//
// The operator dispatches at a panel that is still spawning. The daemon parks
// the brief and then dies before the panel ever settles — which is the ordinary
// case, not a contrived one: a panel can be busy for minutes and a daemon is
// restarted by an upgrade, a SIGHUP gone wrong, or a laptop lid. The brief
// itself already survived that, because the park records a task; the
// reinforcement did not, because it rode a map.
//
// Two daemons over the SAME backlog directory and the same score store, with
// nothing carried across but the files.
func TestAParkedBriefSurvivesADaemonRestart(t *testing.T) {
	for _, tc := range []struct {
		name        string
		self        string
		wantSignals int
	}{
		{"from the cockpit", "", 1},
		{"from an agent panel", "p9", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := scoreStore(t)
			qdir := filepath.Join(t.TempDir(), "backlog")
			e := seedEntry(t, st, "keep the build green")

			// The daemon that takes the dispatch in. Its panel never settles, so it
			// delivers nothing at all.
			first, _, firstBytes := busyScoreServer(st)
			qs, stopSaver := parkedBriefServer(t, first, qdir)
			dispatchTo(t, first, conn(tc.self), "Keep the build green.")
			waitForBacklog(t, qs, 1)
			stopSaver() // the first daemon is dead from here; only its files reach the next one
			if len(*firstBytes) != 0 {
				t.Fatalf("the first daemon delivered %q; the restart is what must do the delivering", string(*firstBytes))
			}

			// The daemon that boots onto the surviving backlog. Its p1 is idle, so
			// the restored task drains onto it on the first tick.
			second, _, secondBytes := scoreServer(st)
			second.qstore = queue.New(qdir, time.Now)
			second.mu.Lock()
			second.restoreTasksLocked()
			second.mu.Unlock()
			second.monitorTick()

			if len(*secondBytes) == 0 {
				t.Fatal("the restored brief never reached a panel, so the count below is vacuous")
			}
			if got := entryNow(t, st, e.Id); got.UserSignals != tc.wantSignals {
				t.Fatalf("user signals after the restart = %d, want %d", got.UserSignals, tc.wantSignals)
			}
		})
	}
}

// TestAParkedBriefIsNotCountedAgainAfterARestart is the other direction, and it
// is the trap #82 names: the fix that makes the test above pass by simply
// stamping the task in holdDispatchLocked fails HERE.
//
// Under that fix the parked delivery keeps its own copy of the permission, so
// the settle counts one from memory and leaves the stamp unspent on disk. The
// task is then in flight on a panel a restart brings back exited,
// restoreTasksLocked re-queues it, and the second daemon finds a stamp nobody
// spent and counts the same operator act a second time. That is exactly the
// replay #50 closed on the backlog road, re-entered from the other end.
//
// The tier is asserted as well as the counts, because the tier is what the
// double count actually costs: one operator act moving an entry a rank it did
// not earn.
func TestAParkedBriefIsNotCountedAgainAfterARestart(t *testing.T) {
	st, _ := scoreStore(t)
	qdir := filepath.Join(t.TempDir(), "backlog")
	e := seedEntry(t, st, "keep the build green")

	first, clk, firstBytes := busyScoreServer(st)
	qs, stopSaver := parkedBriefServer(t, first, qdir)
	dispatchTo(t, first, conn(""), "Keep the build green.")
	settle(first, clk) // delivered here, and counted here
	waitForBacklogDelivered(t, qs)
	stopSaver() // the first daemon is dead from here; only its files reach the next one
	if len(*firstBytes) == 0 {
		t.Fatal("the first daemon delivered nothing, so this is the other test")
	}
	before := entryNow(t, st, e.Id)
	if before.UserSignals != 1 {
		t.Fatalf("user signals before the restart = %d, want the one delivery counted once", before.UserSignals)
	}

	second, _, secondBytes := scoreServer(st)
	second.qstore = queue.New(qdir, time.Now)
	second.mu.Lock()
	second.restoreTasksLocked()
	second.mu.Unlock()
	second.monitorTick()

	if len(*secondBytes) == 0 {
		t.Fatal("the restored task never reached a panel, so the count below is vacuous")
	}
	after := entryNow(t, st, e.Id)
	if after.UserSignals != before.UserSignals || after.Reinforcements != before.Reinforcements {
		t.Fatalf("after the restart = %+v, want the counts unchanged from %+v", after, before)
	}
	if after.Tier != before.Tier {
		t.Fatalf("tier moved %d -> %d on a replayed delivery", before.Tier, after.Tier)
	}
}

// TestAParkedBriefSupersededAtDeliveryCannotBeReDrivenIntoACount is the
// re-drive after a delivery that FAILED, and it is the case where the count and
// the spend come apart.
//
// The tick pops the parked brief, spends the permission, and starts binding it.
// A dispatch that lands inside that window goes straight to the now-ready panel
// and bumps the task's Attempts, so claimDelivery refuses the brief still in
// flight and nothing is written and nothing counted. The permission is gone all
// the same — takeUserSignalLocked is lossy in that one direction by design, and
// R4 would rather miss a fold than record one for a brief no agent saw.
//
// What must NOT happen is the resurrection: the task survives the superseding
// dispatch, and a restart re-queues and re-delivers it. If the spend had not
// reached the task, that re-delivery would mint the reinforcement the failed
// one never earned — a count for a brief that was refused.
//
// The superseding dispatch repeats the SAME words, and that is what makes the
// assertion below deterministic rather than merely usually right. The task is
// saved twice on the way through — once as the settle records the delivery, once
// as the supersede bumps its Attempts — and which of the two the restart reads
// is a race with the saver. One prompt for both shapes means the re-drive folds
// into this entry whichever it reads, so a stamp left unspent lands HERE and
// cannot hide on a second entry the test never looks at.
func TestAParkedBriefSupersededAtDeliveryCannotBeReDrivenIntoACount(t *testing.T) {
	st, _ := scoreStore(t)
	qdir := filepath.Join(t.TempDir(), "backlog")
	e := seedEntry(t, st, "keep the build green")

	first, clk, _ := busyScoreServer(st)
	qs, stopSaver := parkedBriefServer(t, first, qdir)
	dispatchTo(t, first, conn(""), "Keep the build green.")

	// The hook stands in for a slow bind: an agent's dispatch lands on the
	// now-settled panel while the operator's parked brief is still being
	// filtered. It fires once — the re-entrant call runs the chain again.
	var landed bool
	first.onFilterTask = func(b TaskBrief) (TaskBrief, bool) {
		if !landed {
			landed = true
			if _, err := first.dispatchScored("p1", "Keep the build green.", "", task.AuthorAgent); err != nil {
				t.Errorf("the dispatch that supersedes: %v", err)
			}
		}
		return b, true
	}
	settle(first, clk)

	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want a refused write to have counted nothing", got)
	}
	first.mu.Lock()
	for _, tk := range first.tasks {
		if tk.UserSignal {
			t.Errorf("task = %+v, want the permission spent at the assignment the write then lost", tk)
		}
	}
	first.mu.Unlock()
	waitForBacklogDelivered(t, qs)
	stopSaver() // the first daemon is dead from here; only its files reach the next one

	// The surviving task is re-driven by a restart. Its delivery was refused, so
	// nothing it carries has been told to an agent by the operator — and a stamp
	// left unspent would count for it anyway.
	second, _, secondBytes := scoreServer(st)
	second.qstore = queue.New(qdir, time.Now)
	second.mu.Lock()
	second.restoreTasksLocked()
	second.mu.Unlock()
	second.monitorTick()

	if len(*secondBytes) == 0 {
		t.Fatal("the restored task never reached a panel, so the count below is vacuous")
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want a re-drive of a refused delivery to have counted nothing", got)
	}
}

// TestAParkedBriefRefusedAtDeliverySpendsItsPermission is the veto direction of
// the same rule. TestAParkedBriefRefusedAtDeliveryIsWalkedBack already pins that
// the refusal counts nothing and ends the task in the backlog; what it cannot
// see is what the refused task CARRIES afterwards.
//
// A task.pre veto ends the task terminal, so its file is removed and no restart
// re-drives it — which is why an unspent stamp here has no symptom today and
// would be a live replay the moment a walk-back is ever changed to re-queue
// rather than fail. The permission is spent at the assignment, before the bind
// that refuses it, so there is nothing left on the record to replay.
func TestAParkedBriefRefusedAtDeliverySpendsItsPermission(t *testing.T) {
	st, _ := scoreStore(t)
	s, clk, delivered := busyScoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	s.onFilterTask = func(TaskBrief) (TaskBrief, bool) { return TaskBrief{}, false }
	dispatchTo(t, s, conn(""), "Keep the build green.")
	settle(s, clk)

	if len(*delivered) != 0 {
		t.Fatalf("a vetoed delivery wrote %q", string(*delivered))
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want a refused brief to have counted nothing", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tk := range s.tasks {
		if tk.Status != task.Failed || tk.UserSignal {
			t.Fatalf("task = %+v, want it failed with its permission already spent", tk)
		}
	}
}

// TestASpawnedWorkersBriefCountsExactlyOnce is the OTHER road that parks, and it
// parks for the same reason: a panel provisioned a moment ago has not settled,
// so applyScheduledSpawns holds the delivery for the monitor exactly as
// holdDispatchLocked does.
//
// It used to spend the operator's permission AT THE PARK, which bought nothing a
// reboot could keep — a delivery in a map dies with the process either way — and
// cost the reinforcement outright when the fresh worker died before settling.
// The stamp now stays on the task, so this road and the direct dispatch's are
// one idea with one spend point. Nothing else in the suite drives a
// spawn-on-demand task the operator queued, so putting the spend back here fails
// nothing without this.
//
// Both halves of the park are asserted, because the park is where they can come
// apart: the task must hold the permission while the worker starts, the parked
// delivery must NOT hold a second copy of it, and the settle must then count
// exactly one and leave nothing behind.
func TestASpawnedWorkersBriefCountsExactlyOnce(t *testing.T) {
	st, _ := scoreStore(t)
	s, clk, delivered := busyScoreServer(st) // p1 is busy, so nothing standing is free
	e := seedEntry(t, st, "keep the build green")

	id, err := s.enqueueTask(conn(""), "Keep the build green.", "", &task.SpawnSpec{Command: "cat"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	_, spawns := schedule(s)
	if len(spawns) != 1 {
		t.Fatalf("a spawn task with no free agent should request one panel, got %+v", spawns)
	}
	if !s.applyScheduledSpawns(spawns) {
		t.Fatal("provisioning a panel should report a fleet change")
	}

	s.mu.Lock()
	pid := s.panels[len(s.panels)-1].ID
	if tk := s.tasks[id]; tk == nil || !tk.UserSignal {
		t.Errorf("the task = %+v, want the operator's stamp held while the worker starts", tk)
	}
	held, ok := s.pendingDispatch[pid]
	if !ok {
		s.mu.Unlock()
		t.Fatalf("no delivery was parked for the provisioned panel %s", pid)
	}
	if held.signal {
		t.Error("the parked delivery carries the permission as well; one act, one record")
	}
	s.mu.Unlock()
	t.Cleanup(func() { _ = s.closePanel(pid) }) // reap the real process

	settle(s, clk)

	if !strings.Contains(string(*delivered), "Keep the build green.") {
		t.Fatalf("settling the provisioned worker should deliver its brief, got %q", string(*delivered))
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 1 || got.Reinforcements != 1 {
		t.Fatalf("entry = %+v, want the operator's brief counted exactly once", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tk := s.tasks[id]; tk == nil || tk.UserSignal {
		t.Fatalf("the delivered task = %+v, want its permission spent", tk)
	}
}
