package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// This file covers #59: score.Store.WriteFailing is a LATCH, and a latch is
// announced by its two transitions rather than by every attempt that meets it.
// See noteScoreWrites.

// deadMount takes the write bits off the store's directory, so the log's first
// durable append cannot even create its file — which is where a full disk, a
// read-only mount or a directory pulled out from under a running daemon leaves
// it. The undo is returned so the same test can watch the mount come back.
//
// The probe is the directory's, not the log's, and asking it BEFORE the command
// under test runs is what keeps the skip honest: whether a chmod binds is a
// question about the user rather than about the store.
func deadMount(t *testing.T, dir string) (restore func()) {
	t.Helper()
	return unwritable(t, dir, func() error {
		probe := filepath.Join(dir, "probe")
		f, err := os.Create(probe)
		if err != nil {
			return err
		}
		_ = f.Close()
		return os.Remove(probe)
	})
}

// TestScoreWritesAreSaidOnceEnteringFailureAndOnceOnRecovery is #59's whole
// claim, and both halves of it were broken in opposite directions.
//
// The store latches a failed durable append and clears it on the next one that
// lands, which is exactly the state that can produce a transition. Nothing
// turned it into one: the only voice it had was an unpaced warn on the submit
// door, so a mount that died produced a line for every submission that met it
// and not one line when it came back. An operator watching the log therefore
// could not tell a store that is still broken from one that has been fixed,
// which is the question the whole latch exists to answer.
//
// The count is asserted rather than the presence, because presence is what the
// old shape also satisfied — it is the SECOND line that was the defect, and a
// test that only greps for the first cannot fail on it.
func TestScoreWritesAreSaidOnceEnteringFailureAndOnceOnRecovery(t *testing.T) {
	st, dir := scoreStore(t)
	s, _, _ := scoreServer(st)
	restore := deadMount(t, dir)

	logged := captureLog(t)
	// Several submissions, each one crossing the cap, and a read after each: the
	// old line rode the submit door, so anything less than a handful of attempts
	// would pass against the very shape this replaces.
	for i := range 3 {
		rewindAll(s, &s.submits, 2*minSubmitGap)
		cc := conn("")
		s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "the fleet keeps the build green"})
		if msg := reply(t, cc); msg.Type != "error" {
			t.Fatalf("submission %d into a dead mount answered %+v, want the store's refusal", i, msg)
		}
		s.scoreView(score.Context{})
	}
	if got, n := logged(), strings.Count(logged(), "score writes are not landing"); n != 1 {
		t.Fatalf("a dead mount produced %d failure lines, want exactly 1 on the transition into it:\n%s", n, got)
	}
	if got := logged(); !strings.Contains(got, dir) {
		t.Errorf("the failure line does not name the directory that failed:\n%s", got)
	}

	// The mount comes back. One line, on the write that proves it — the store
	// clears the latch on the next append that lands, so the recovery is only
	// observable from a write that succeeded.
	restore()
	logged = captureLog(t)
	for i := range 3 {
		rewindAll(s, &s.submits, 2*minSubmitGap)
		cc := conn("")
		s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "the agent asks before it deletes"})
		if msg := reply(t, cc); msg.Type != "score" {
			t.Fatalf("submission %d after the mount came back answered %+v", i, msg)
		}
		s.scoreView(score.Context{})
	}
	if got, n := logged(), strings.Count(logged(), "score writes recovered"); n != 1 {
		t.Fatalf("the recovery produced %d lines, want exactly 1:\n%s", n, got)
	}
}

// TestAHealthyStoreSaysNothingAboutItsWrites is the silence the latch has to
// keep. noteScoreWrites runs on every read path in the daemon, so a store whose
// writes are landing must produce no line at all — a recovery announced by a
// store that was never broken would arrive on the first dispatch of every
// daemon that ever booted.
func TestAHealthyStoreSaysNothingAboutItsWrites(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)

	logged := captureLog(t)
	for range 3 {
		s.scoreView(score.Context{})
	}
	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "the fleet keeps the build green"})
	if msg := reply(t, cc); msg.Type != "score" {
		t.Fatalf("submit to a healthy store answered %+v", msg)
	}
	if got := logged(); strings.Contains(got, "score writes") {
		t.Errorf("a healthy store said something about its writes:\n%s", got)
	}
}

// TestADisabledStoreSaysNothingAboutItsWrites covers the nil store, which every
// read path also reaches: score.Store.WriteFailing is false on it by
// construction, and the accessor is nil-safe precisely so this side needs no
// guard of its own. Without the assertion a future guard could be dropped here
// and only a daemon booted with score off would find out.
func TestADisabledStoreSaysNothingAboutItsWrites(t *testing.T) {
	s, _, _ := scoreServer(nil)

	logged := captureLog(t)
	s.noteScoreWrites()
	if got := logged(); strings.Contains(got, "score writes") {
		t.Errorf("a disabled store said something about its writes:\n%s", got)
	}
}
