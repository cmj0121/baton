package server

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/score"
)

// TestTheEmptyingThatRaisedNoAlarm is #53, driven rather than argued: the exact
// collapse that was measured against a running daemon, replayed through the same
// server path a conductor takes.
//
//	window 1: 74 → 38 entries (36 merges, 48.6%)   12 s   0 alarms
//	          ── wait 58 s for the window to roll ──
//	window 2: 38 → 20 entries (18 merges, 47.4%)    6 s   0 alarms
//	net: 74 → 20 — 72% of the fleet's memory gone, no alarm
//
// Neither half of that is an accident of the numbers. Each window takes just
// under half of what the store held when it opened, and the baseline used to be
// re-read from a store the merges had already shrunk — so "half of what is left"
// could be taken forever and the warning never crossed its own line.
//
// BOTH DIRECTIONS ARE HERE and the silent one is load-bearing. A test that only
// proves the second batch now alarms would pass just as happily against an alarm
// that fires on every merge, which is the alarm the floor beside it exists to
// prevent; the first batch asserts that taking 48.6% still says nothing.
func TestTheEmptyingThatRaisedNoAlarm(t *testing.T) {
	_, ids, s := alarmFleet(t, score.Policy{}, 74)

	logged := captureLog(t)
	mergeAway(t, s, ids, 1, 37) // 74 → 38
	if got := alarms(logged()); got != 0 {
		t.Fatalf("taking 36 of 74 entries raised %d alarms, want 0: 48.6%% is not half, and an "+
			"alarm on it is one an operator learns to ignore\n%s", got, logged())
	}

	// The wait the measurement used: 58 seconds, just under the window, which is
	// what used to roll the window and re-seed the baseline on the 38 entries the
	// merges had left. The run has not stopped, so its baseline has not moved.
	agedAlarm(s, 58*time.Second)

	logged = captureLog(t)
	mergeAway(t, s, ids, 37, 55) // 38 → 20
	if got := alarms(logged()); got == 0 {
		t.Fatalf("74 entries merged down to 20 — 72%% of the fleet's memory — raised no alarm at all; "+
			"the baseline is still being pushed down by the merges it is watching\n%s", logged())
	}
}

// TestTheBaselineFollowsWhatTheMergesDidNot is the reason #53's own three-line
// fix is not the one that landed.
//
// A high-water mark that only ever rises is silent everywhere the test above is
// silent and alarms everywhere it alarms — and score.md's own header tells the
// operator to "edit or delete lines freely", so a store that shrinks without a
// single merge is ordinary rather than hypothetical. Under a high-water mark the
// next perfectly ordinary merge would announce that merging had taken more than
// half the fleet's memory, when the operator had taken it with their editor. An
// alarm that can say that is one people learn to ignore.
//
// So the baseline moves with everything the merges did not do. This drives that
// end to end: real merges either side of a real hand-edit of score.md, with the
// silent direction first and the sounding one after it, because an alarm that
// went quiet altogether would pass the first half on its own.
func TestTheBaselineFollowsWhatTheMergesDidNot(t *testing.T) {
	st, ids, s := alarmFleet(t, score.Policy{}, 20)

	logged := captureLog(t)
	mergeAway(t, s, ids, 1, 5) // 20 → 16, nowhere near half
	if got := alarms(logged()); got != 0 {
		t.Fatalf("four merges of a twenty-entry store raised %d alarms, want 0\n%s", got, logged())
	}

	// The operator tidies their own file: eight entries go, and not one of them
	// to a merge. A deleted line retires its entry — score.md says so in its own
	// header — so the store is at 8 by the time the next correction reconciles.
	deleteEntries(t, st.Dir(), ids[12:20])

	logged = captureLog(t)
	mergeAway(t, s, ids, 5, 6) // 8 → 7
	if got := alarms(logged()); got != 0 {
		t.Fatalf("one merge, after the OPERATOR deleted eight lines by hand, raised %d alarms: the "+
			"baseline is holding the operator's own edit against the conductor\n%s", got, logged())
	}

	// And the guard is still armed: merging on down from what is genuinely left
	// crosses the line and says so.
	logged = captureLog(t)
	mergeAway(t, s, ids, 6, 9) // 7 → 4
	if got := alarms(logged()); got == 0 {
		t.Fatalf("merging a store of twelve down to four raised nothing; the baseline followed the "+
			"operator's deletion so far down that the alarm can no longer fire\n%s", logged())
	}
}

// agedAlarm pushes every instant the merge alarm holds into the past, so a test
// can spend a minute of the alarm's time without spending one of its own. It is
// rewind's counterpart for the other cap in this file, and it moves all three
// clocks together for the same reason rewind reaches through a helper: a test
// that set one field itself would go on passing after the rule changed under it.
func agedAlarm(s *Server, by time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.merges.at = s.merges.at.Add(-by)
	s.merges.last = s.merges.last.Add(-by)
	if !s.merges.firedAt.IsZero() {
		s.merges.firedAt = s.merges.firedAt.Add(-by)
	}
}

// deleteEntries removes the lines carrying these ids from score.md, the way an
// operator with an editor does. The store retires what the file no longer
// mentions on its next read. The write itself goes through editScoreMD, which
// also pushes the mtime past the store's fingerprint gate — the edit is only
// visible to the next read because of that.
func deleteEntries(t *testing.T, dir string, ids []string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "score.md"))
	if err != nil {
		t.Fatalf("read score.md: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	keep := make([]string, 0, len(lines))
	for _, line := range lines {
		if !slices.ContainsFunc(ids, func(id string) bool { return strings.Contains(line, id) }) {
			keep = append(keep, line)
		}
	}
	if got := len(lines) - len(keep); got != len(ids) {
		t.Fatalf("the edit removed %d lines, want %d: the ids are not one to a line", got, len(ids))
	}
	editScoreMD(t, dir, strings.Join(keep, "\n"))
}

// TestTheAlarmSaysHowLongTheRunHasBeenGoing is the log line's own claim, which
// #53 turned into a falsifiable one.
//
// It used to carry a flat `within: 1m0s`, because the drop really was measured
// inside one window. It is measured from where the RUN began now, which can be
// many windows back — so the fixed minute would have been a statement about the
// collapse that was simply untrue, and untrue in the direction that makes an
// operator think a slow emptying was a sudden one.
func TestTheAlarmSaysHowLongTheRunHasBeenGoing(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var a mergeAlarm

	entries, now := 40, t0
	for range 19 { // 40 → 21, under half, over four minutes of paced merging
		if _, _, alarm := a.note(entries, entries-1, 8, now); alarm {
			t.Fatalf("a store of 40 alarmed at %d entries, before half of it was gone", entries-1)
		}
		entries--
		now = now.Add(15 * time.Second)
	}
	_, over, alarm := a.note(entries, entries-1, 8, now)
	if !alarm {
		t.Fatal("the merge that took the twentieth entry of forty raised nothing")
	}
	if want := 285 * time.Second; over != want {
		t.Fatalf("the alarm says the run has been going %v, want %v: a collapse the daemon watched "+
			"for nearly five minutes must not be reported as a one-minute one", over, want)
	}
}

// TestASilentWindowEndsTheRun pins the other half of the minute: a baseline that
// never came down would eventually hold a fleet's whole history against one
// ordinary merge. A WHOLE WINDOW WITH NO MERGE ends the run, and nothing shorter
// does — which is the same instant the emptying above turns on, read from the
// other side.
func TestASilentWindowEndsTheRun(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		pause time.Duration
		want  bool
	}{
		{"a pause inside the window carries the baseline", mergeAlarmWindow, true},
		{"a pause past it does not", mergeAlarmWindow + time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a mergeAlarm
			// 60 → 40, a third gone: not half, so silent, and the baseline is 60.
			left, now, fired := alarmRun(&a, 60, 20, 8, t0)
			if fired != 0 {
				t.Fatalf("a third of the store raised %d alarms, want 0", fired)
			}
			// Ten more take the count to 30, which is half of the ORIGINAL 60 and
			// only three quarters of the 40 the pause would re-seed on.
			_, _, fired = alarmRun(&a, left, 10, 8, now.Add(tc.pause))
			if got := fired > 0; got != tc.want {
				t.Fatalf("after a pause of %v, merging 60 entries down to 30 alarmed=%v, want %v",
					tc.pause, got, tc.want)
			}
		})
	}
}

// TestMergeRunCountsOnlyWhatMergingTook is the baseline rule as arithmetic, at
// the two boundaries the end-to-end tests above cannot reach cheaply: a store
// that GREW between two merges, and one that lost everything to the operator.
//
// from-after is the count this run's merges have removed, and that is the whole
// invariant. Whatever else moved the store — a submission, a hand-edit, a fold —
// moves the baseline with it and is never charged to the conductor.
func TestMergeRunCountsOnlyWhatMergingTook(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var a mergeAlarm

	// 20 → 15, five taken, silent.
	left, now, fired := alarmRun(&a, 20, 5, 8, t0)
	if fired != 0 {
		t.Fatalf("a quarter of the store raised %d alarms, want 0", fired)
	}
	// The fleet submits thirty entries. The baseline follows them up, so the five
	// already taken are five out of fifty rather than five out of twenty.
	from, _, alarm := a.note(left+30, left+29, 8, now.Add(time.Second))
	if alarm {
		t.Fatal("a merge alarmed on a store that had just grown by thirty entries")
	}
	if want := 50; from != want {
		t.Fatalf("the baseline is %d after the store grew from 15 to 45, want %d: the memory the "+
			"fleet regrew is memory this run has not taken", from, want)
	}
	// The operator then deletes forty lines. The baseline comes down with them —
	// they are not entries a merge removed — leaving the six the merges did take.
	from, _, _ = a.note(4, 3, 8, now.Add(2*time.Second))
	if want := 10; from != want {
		t.Fatalf("the baseline is %d after the operator deleted forty lines by hand, want %d: a "+
			"high-water mark would still be holding their edit against the conductor", from, want)
	}
}
