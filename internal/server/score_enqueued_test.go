package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/queue"
	"github.com/cmj0121/baton/internal/task"
)

// This file covers #50: a brief the operator ENQUEUED reinforces what it repeats
// exactly as one they dispatched does. Its subject is the gap the dispatch path
// does not have — the connection that enqueued the task is gone by the time the
// scheduler drains it, and a restart may sit in between — so the tests here are
// about what survives that gap rather than about the fold itself, which
// score_brief_test.go already pins on the direct path.

// enqueueOn drives task.enqueue exactly as a client would and then runs the tick
// that drains the backlog onto the idle agent scoreServer provides. Both halves
// matter: the stamp is decided in the first and read in the second, and a test
// that called enqueueTask directly would skip the wire case that supplies the
// connection.
func enqueueOn(t *testing.T, s *Server, cc *clientConn, prompt string) {
	t.Helper()
	s.onCommand(cc, proto.Command{Action: "task.enqueue", Prompt: prompt})
	s.monitorTick()
}

// TestAnEnqueuedBriefReinforcesWhatItRepeats is #50 itself: `baton ctl queue add`
// and `baton ctl dispatch` are the same act from the operator's side, so the same
// words must land the same way. The discrimination is still the connection's and
// only the connection's — the same command from an agent panel counts nothing —
// and every case here drains onto the same panel, so it is the enqueueing
// connection deciding rather than the delivering one.
func TestAnEnqueuedBriefReinforcesWhatItRepeats(t *testing.T) {
	for _, tc := range []struct {
		name        string
		self        string
		wantSignals int
	}{
		{"from the cockpit", "", 1},
		{"from an agent panel", "p1", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := scoreStore(t)
			s, _, delivered := scoreServer(st)
			e := seedEntry(t, st, "keep the build green")

			// Not byte-identical to the entry, so the match is the folding
			// normaliser's — the same one the direct path is held to.
			enqueueOn(t, s, conn(tc.self), "Keep the build green.")

			if len(*delivered) == 0 {
				t.Fatal("the queued task never reached the panel, so the count below is vacuous")
			}
			got := entryNow(t, st, e.Id)
			if got.UserSignals != tc.wantSignals {
				t.Fatalf("user signals = %d, want %d", got.UserSignals, tc.wantSignals)
			}
			if got.Reinforcements != tc.wantSignals {
				t.Fatalf("reinforcements = %d, want %d: only the operator's brief counts",
					got.Reinforcements, tc.wantSignals)
			}
		})
	}
}

// TestAnEnqueuedBriefSurvivesADaemonRestart is the load-bearing one, and the
// reason the stamp is on task.Task rather than in a server-side map.
//
// A queued task routinely outlives the daemon that took it in: restoreTasksLocked
// re-queues the whole backlog on boot, and the connection that enqueued each task
// closed long before. A signal that is right in memory and wrong after a reboot
// passes every test that does not reboot, so this one reboots — a second Server
// over the SAME backlog directory, with nothing carried across but the files.
//
// The store is shared because score.md is on disk too: the fleet's memory is what
// the restart is being asked to reinforce, not something the daemon holds.
func TestAnEnqueuedBriefSurvivesADaemonRestart(t *testing.T) {
	for _, tc := range []struct {
		name        string
		self        string
		wantSignals int
	}{
		{"from the cockpit", "", 1},
		{"from an agent panel", "p1", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := scoreStore(t)
			qdir := filepath.Join(t.TempDir(), "backlog")
			e := seedEntry(t, st, "keep the build green")

			// The daemon that takes the task in. It never delivers anything: the
			// enqueue is all it does before it stops.
			first, _, firstBytes := scoreServer(st)
			first.qstore = queue.New(qdir, time.Now)
			stopSaver := runSaver(t, first)
			first.onCommand(conn(tc.self), proto.Command{Action: "task.enqueue", Prompt: "Keep the build green."})
			waitForBacklog(t, first.qstore, 1)
			stopSaver()
			if len(*firstBytes) != 0 {
				t.Fatalf("the first daemon delivered %q; the restart is what must do the delivering", string(*firstBytes))
			}

			// The daemon that boots onto the surviving backlog, restores it, and
			// drains it. Nothing but the files reached it.
			second, _, secondBytes := scoreServer(st)
			second.qstore = queue.New(qdir, time.Now)
			second.mu.Lock()
			second.restoreTasksLocked()
			second.mu.Unlock()
			second.monitorTick()

			if len(*secondBytes) == 0 {
				t.Fatal("the restored task never reached a panel, so the count below is vacuous")
			}
			if got := entryNow(t, st, e.Id); got.UserSignals != tc.wantSignals {
				t.Fatalf("user signals after the restart = %d, want %d", got.UserSignals, tc.wantSignals)
			}
		})
	}
}

// TestADeliveredBriefIsNotCountedAgainAfterARestart is the other half of the
// restart, and the half the test above cannot see: its first daemon deliberately
// delivers nothing, so the stamp it hands the second one has never been spent.
//
// Here it has been. The first daemon drains the backlog and counts the operator's
// reinforcement; the task is then in flight on a panel a restart brings back
// exited, so restoreTasksLocked re-queues it and the second daemon delivers the
// SAME brief again. R4's rule is that the signal counts once — a task already
// delivered is not countable again, whatever the backlog file says — so the entry
// must read after the reboot exactly what it read before it.
//
// It asserts the tier as well as the counts, because the tier is what the double
// count actually cost: one operator act moved an entry a rank it had not earned.
func TestADeliveredBriefIsNotCountedAgainAfterARestart(t *testing.T) {
	st, _ := scoreStore(t)
	qdir := filepath.Join(t.TempDir(), "backlog")
	e := seedEntry(t, st, "keep the build green")

	first, _, firstBytes := scoreServer(st)
	first.qstore = queue.New(qdir, time.Now)
	stopSaver := runSaver(t, first)
	first.onCommand(conn(""), proto.Command{Action: "task.enqueue", Prompt: "Keep the build green."})
	first.monitorTick() // delivered here, and counted here
	waitForBacklogInFlight(t, first.qstore)
	stopSaver()
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

// TestABacklogFromAnOlderBuildCountsNothing is the absence half of the round trip.
// A task queued before the field existed decodes with it false, which is the same
// thing an agent's enqueue means: no signal. The safe direction — invariant I6 is
// about entries climbing on something that was not the operator, so failing to
// fold is the tolerable miss and folding wrongly is not.
//
// The file is hand-written rather than produced by an older binary, so what it
// actually pins is that the key's absence decodes as false and reaches delivery
// as false.
func TestABacklogFromAnOlderBuildCountsNothing(t *testing.T) {
	st, _ := scoreStore(t)
	qdir := filepath.Join(t.TempDir(), "backlog")
	e := seedEntry(t, st, "keep the build green")

	if err := os.MkdirAll(qdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Every field an old build wrote, and no "user" key at all.
	old := `{"schema":1,"task":{"id":"t1","prompt":"Keep the build green.","status":"queued",` +
		`"attempts":1,"created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z"}}`
	if err := os.WriteFile(filepath.Join(qdir, "t1.json"), []byte(old), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The file is only a fixture if it really lacks the key.
	var probe struct {
		Task map[string]any `json:"task"`
	}
	if err := json.Unmarshal([]byte(old), &probe); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, ok := probe.Task["user"]; ok {
		t.Fatal("the fixture carries a user key, so it does not stand for an older build")
	}

	s, _, delivered := scoreServer(st)
	s.qstore = queue.New(qdir, time.Now)
	s.mu.Lock()
	s.restoreTasksLocked()
	s.mu.Unlock()
	s.monitorTick()

	if len(*delivered) == 0 {
		t.Fatal("the restored task never reached a panel, so the count below is vacuous")
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want a task from an older build to have counted nothing", got)
	}
}

// TestABacklogKeyIsTheNameOnDisk is the other half of the fixture above, and it
// exists because the field and the key are now spelled differently.
// task.Task.UserSignal is stored under "user", and the mismatch is deliberate:
// the Go name says what the field IS (a permission, spent once), the JSON name
// is a FILE FORMAT and renaming it would silently drop the stamp off every
// backlog written by a build on the other side of the rename.
//
// Nothing else could fail on that. The neighbouring fixture pins the key's
// ABSENCE, and every other path writes and reads the key with the same binary,
// so a round trip agrees with itself whatever the key is called. Retagging the
// field passed the entire suite before this test existed. Here the key is
// hand-written, so the assertion is against the format rather than against the
// encoder's opinion of it.
func TestABacklogKeyIsTheNameOnDisk(t *testing.T) {
	st, _ := scoreStore(t)
	qdir := filepath.Join(t.TempDir(), "backlog")
	e := seedEntry(t, st, "keep the build green")

	if err := os.MkdirAll(qdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stamped := `{"schema":1,"task":{"id":"t1","prompt":"Keep the build green.","status":"queued",` +
		`"user":true,"attempts":0,"created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z"}}`
	if err := os.WriteFile(filepath.Join(qdir, "t1.json"), []byte(stamped), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s, _, delivered := scoreServer(st)
	s.qstore = queue.New(qdir, time.Now)
	s.mu.Lock()
	s.restoreTasksLocked()
	s.mu.Unlock()
	s.monitorTick()

	if len(*delivered) == 0 {
		t.Fatal("the restored task never reached a panel, so the count below is vacuous")
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 1 {
		t.Fatalf("entry = %+v, want the stamp under the \"user\" key to have counted once", got)
	}
}

// TestAnEnqueuedBriefCountsTheOperatorsOwnWords is R4's rule carried over the gap:
// a task.pre hook may rewrite a prompt freely, and ranking its output as the
// operator's voice would let a plugin reach the one tier #37 reserves for a
// person — the self-report #38 §4 rules out, arriving through the customisation
// point.
//
// Two entries, so the check is positive on both sides: the words the operator
// typed fold, and the words the hook substituted do not.
func TestAnEnqueuedBriefCountsTheOperatorsOwnWords(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	typed := seedEntry(t, st, "keep the build green")
	rewritten := seedEntry(t, st, "ship it on friday")

	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) {
		b.Prompt = "ship it on friday"
		return b, true
	}
	enqueueOn(t, s, conn(""), "keep the build green")

	if got := entryNow(t, st, typed.Id); got.UserSignals != 1 {
		t.Fatalf("the typed entry = %+v, want the operator's own words counted", got)
	}
	if got := entryNow(t, st, rewritten.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("the rewritten entry = %+v, want a hook's words to reach no tier", got)
	}
}

// TestAVetoedQueuedTaskCountsNothing is R4's other rule over the same gap: the
// signal is what the fleet was TOLD, not what the operator asked for. A task.pre
// veto at delivery means nothing reached an agent, and vetoQueuedTask walks the
// assignment back — so a reinforcement recorded here would be one for a task no
// agent ever saw, on an entry the operator cannot account for afterwards.
func TestAVetoedQueuedTaskCountsNothing(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, delivered := scoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	s.onFilterTask = func(TaskBrief) (TaskBrief, bool) { return TaskBrief{}, false }
	enqueueOn(t, s, conn(""), "keep the build green")

	if len(*delivered) != 0 {
		t.Fatalf("a vetoed queued task delivered %q", string(*delivered))
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want a vetoed queued task to have counted nothing", got)
	}

	// With the veto lifted the very same words do count, so the check above is the
	// hook's doing rather than the enqueue path failing to signal at all.
	s.onFilterTask = nil
	enqueueOn(t, s, conn(""), "keep the build green")
	if got := entryNow(t, st, e.Id); got.UserSignals != 1 {
		t.Fatalf("entry = %+v, want the unvetoed queued task counted", got)
	}
}

// TestAPluginEnqueuedBriefCountsNothing closes the door baton.enqueue would
// otherwise open. A plugin-originated task is delivered bare — no chain, no score
// — and it is not the operator saying anything, so a hook that enqueues its own
// wording must not be able to climb an entry by repeating it.
func TestAPluginEnqueuedBriefCountsNothing(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, delivered := scoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	if _, err := s.Enqueue("keep the build green", ""); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	s.monitorTick()

	if len(*delivered) == 0 {
		t.Fatal("the plugin's task never reached a panel, so the count below is vacuous")
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want a plugin's enqueue to have counted nothing", got)
	}
}

// TestTheEnqueueStampIsTheServersConclusion pins the stamp's provenance at the
// only place it is decided. Nothing on the wire can assert it: the same enqueue
// command, byte for byte, is stamped by the CONNECTION it arrived on, and a
// plugin's enqueue — which has no connection at all — is stamped by neither.
//
// It reads the task table rather than an entry because the subject is the record
// itself, which is what a restart replays and what a frontend is shown.
//
// The author is asserted rather than a pair of bools, and the middle case is why
// that matters: an agent's enqueue is now a positive record and not the absence
// of two flags. Nothing here may read AuthorUnknown — an unknown author is a
// file written before the field, never something a running fleet mints.
func TestTheEnqueueStampIsTheServersConclusion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enqueue    func(*Server)
		wantUser   bool
		wantAuthor task.Author
	}{
		{"the cockpit's connection", func(s *Server) {
			s.onCommand(conn(""), proto.Command{Action: "task.enqueue", Prompt: "go"})
		}, true, task.AuthorUser},
		{"an agent panel's connection", func(s *Server) {
			s.onCommand(conn("p1"), proto.Command{Action: "task.enqueue", Prompt: "go"})
		}, false, task.AuthorAgent},
		{"baton.enqueue, which has no connection", func(s *Server) {
			if _, err := s.Enqueue("go", ""); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
		}, false, task.AuthorPlugin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := scoreServer(nil)
			tc.enqueue(s)

			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.tasks) != 1 {
				t.Fatalf("backlog holds %d tasks, want one", len(s.tasks))
			}
			for _, got := range s.tasks {
				if got.UserSignal != tc.wantUser || got.Author != tc.wantAuthor {
					t.Fatalf("task = %+v, want user=%v author=%q", got, tc.wantUser, tc.wantAuthor)
				}
			}
		})
	}
}

// runSaver starts the backlog saver and hands back the stop. Its writes are a
// temp file and a rename, so a test that only closes the stop can have one still
// in flight when t.TempDir's cleanup walks the directory — which failed as
// "RemoveAll cleanup: directory not empty", twice in sixty runs, on a tree with
// nothing else changed. Waiting for the loop to return is what makes the
// directory quiet before anyone removes it.
func runSaver(t *testing.T, s *Server) func() {
	t.Helper()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { defer close(done); s.taskSaverLoop(stop) }()
	var once sync.Once
	wait := func() { once.Do(func() { close(stop); <-done }) }
	t.Cleanup(wait)
	return wait
}

// TestTheEnqueueStampReachesTheBacklogFile is the disk half of the same
// conclusion: what the restart reads back has to be what the connection decided,
// and a stamp the saver drops is a signal the reboot loses.
func TestTheEnqueueStampReachesTheBacklogFile(t *testing.T) {
	qdir := filepath.Join(t.TempDir(), "backlog")
	s, _, _ := scoreServer(nil)
	s.qstore = queue.New(qdir, time.Now)
	runSaver(t, s)

	s.onCommand(conn(""), proto.Command{Action: "task.enqueue", Prompt: "go"})
	waitForBacklog(t, s.qstore, 1)

	tasks, bad, err := s.qstore.LoadAll()
	if err != nil || len(bad) != 0 {
		t.Fatalf("LoadAll: %v, bad=%v", err, bad)
	}
	if len(tasks) != 1 || !tasks[0].UserSignal {
		t.Fatalf("backlog files = %+v, want the operator's stamp persisted", tasks)
	}
}

// waitForBacklog blocks until the store can load n tasks. The saver is a
// goroutine draining a channel, so the file arrives shortly after the command
// returns rather than during it.
//
// It asks the STORE rather than counting directory entries, and that is not
// fussiness: Save is a temp-file-plus-rename, so a directory listing sees a file
// one tick before the task is readable and a test that waited on the listing
// would restart onto an empty backlog roughly at random.
func waitForBacklog(t *testing.T, qs *queue.Store, n int) {
	t.Helper()
	waitBacklog(t, qs, fmt.Sprintf("held %d tasks", n), func(tasks []task.Task) bool {
		return len(tasks) >= n
	})
}

// waitForBacklogInFlight blocks until the backlog file shows a task ASSIGNED to a
// panel — the disk's record that a daemon delivered it.
//
// waitForBacklog is the wrong wait for that, and getting it wrong is not a slower
// test but a different one. A task is saved twice on the way to a panel, once
// when it is enqueued and once when it is assigned, and the saver is a goroutine
// draining a channel: the first file lands while the second nudge is still
// queued. A test that waited only for a file to EXIST would restart, at random,
// onto the shape the task had before it was delivered — a backlog that genuinely
// holds an undelivered brief, which is the other restart test's case and counts
// once by design. Measured at two runs in twenty before this wait existed.
//
// It is the assignment rather than the stamp that is waited on, because the
// assignment is the fact: one save carries Panel, Status and the spent stamp
// together, since scheduleLocked sets all three under one hold of s.mu and the
// saver snapshots under that same lock.
func waitForBacklogInFlight(t *testing.T, qs *queue.Store) {
	t.Helper()
	waitBacklog(t, qs, "recorded a delivery", func(tasks []task.Task) bool {
		for _, tk := range tasks {
			if tk.Panel != "" {
				return true
			}
		}
		return false
	})
}

// waitBacklog polls the store until pred holds over what it loads, and fails
// naming what the caller was waiting FOR — the two waits above differ in nothing
// else, and a poll loop copied per predicate is a timeout somebody tunes in one
// place and not the other.
//
// A load that errors, or that reports a bad file, is never handed to pred: those
// are the half-written states the retry exists to ride out, and a predicate would
// have to remember not to trust them.
func waitBacklog(t *testing.T, qs *queue.Store, want string, pred func([]task.Task) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		tasks, bad, err := qs.LoadAll()
		if err == nil && len(bad) == 0 && pred(tasks) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the backlog at %s never %s (tasks=%+v, bad=%v, err=%v)", qs.Dir(), want, tasks, bad, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
