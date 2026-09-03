package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// This file covers #58 on the refine door: whose failure a refused correction
// was, and therefore which of the daemon's two lines it gets. See
// noteScoreTrouble, and score.ErrRefine for the half the store owns.

// TestACallersRefusalDoesNotClaimTheStoreIsBroken is the defect, stated as the
// table it needed.
//
// The refine door logged `the conductor's correction was refused` for every
// failure it saw, so a mistyped id and a mount that died produced the same
// words. That made the sentence an operator greps for a broken store into one
// any conductor could manufacture with a typo — the exact shape R7 (#46) had
// just finished removing from the submit door, one door over, and left here.
//
// Both directions are asserted on every row, and the second is the one that
// matters: a test that only checks the refusal IS logged passes just as happily
// against the old unconditional warn.
func TestACallersRefusalDoesNotClaimTheStoreIsBroken(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func(keep, gone score.Entry) proto.Command
	}{
		{
			name: "a correction naming no entry",
			cmd: func(score.Entry, score.Entry) proto.Command {
				return proto.Command{Action: "score.lower", ID: "nosuch"}
			},
		},
		{
			name: "a merge from an id naming no entry",
			cmd: func(keep, _ score.Entry) proto.Command {
				return proto.Command{Action: "score.merge", ID: keep.Id, From: "nosuch"}
			},
		},
		{
			name: "a reword to nothing",
			cmd: func(keep, _ score.Entry) proto.Command {
				return proto.Command{Action: "score.reword", ID: keep.Id, Prompt: "   \t "}
			},
		},
		{
			name: "a reword past the entry cap",
			cmd: func(keep, _ score.Entry) proto.Command {
				return proto.Command{Action: "score.reword", ID: keep.Id, Prompt: strings.Repeat("x", 4000)}
			},
		},
		{
			name: "a reword onto what another entry already says",
			cmd: func(keep, gone score.Entry) proto.Command {
				return proto.Command{Action: "score.reword", ID: keep.Id, Prompt: gone.Text}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := scoreStore(t)
			keep := seed(t, st, "the agent asks before it deletes")
			gone := seed(t, st, "it runs the tests before it pushes")
			s := refineServer(t, st)

			logged := captureLog(t)
			if got := refinePaced(t, s, conn("c1"), tc.cmd(keep, gone)); got == "" {
				t.Fatalf("%s was accepted, so there is no refusal to classify", tc.name)
			}
			got := logged()
			// Still said. #45's symmetry stands: a correction changes what the
			// operator has on nobody's initiative but an agent's, so one that did
			// not happen is worth a line whoever's fault it was.
			if !strings.Contains(got, "the conductor's correction was refused") {
				t.Errorf("a refused correction went unlogged:\n%s", got)
			}
			// And said as the CALLER's. Neither of the operator's broken-store lines
			// may be reachable from a conductor's typo.
			for _, claim := range []string{
				"score could not record the conductor's correction",
				"score writes are not landing",
			} {
				if strings.Contains(got, claim) {
					t.Errorf("a healthy store was reported broken by a caller's own mistake (%q):\n%s", claim, got)
				}
			}
		})
	}
}

// TestARefineTheDiskRefusedStillSpeaks is the other half, and the one a gate
// added carelessly would have silenced: classifying refusals is only safe if
// the class that IS the operator's still reaches them.
//
// The DIRECTORY is unwritable over a store that already holds an entry, so the
// events log — an existing file — still takes its append while the score.md
// rewrite, which goes through a sibling temp, cannot. That is a genuine disk
// failure with the write latch clear throughout, because appendEvents covers
// the log and says so in its own comment. Nothing but this door can say it.
func TestARefineTheDiskRefusedStillSpeaks(t *testing.T) {
	st, dir := scoreStore(t)
	e := seed(t, st, "the agent asks before it deletes")
	s := refineServer(t, st)
	deadMount(t, dir)

	logged := captureLog(t)
	if got := refinePaced(t, s, conn("c1"), proto.Command{
		Action: "score.reword", ID: e.Id, Prompt: "the agent asks first"}); got == "" {
		t.Fatal("the reword was accepted with the directory unwritable")
	}
	got := logged()
	if !strings.Contains(got, "score could not record the conductor's correction") {
		t.Errorf("a disk refusal on the refine door went unsaid:\n%s", got)
	}
	if !strings.Contains(got, dir) {
		t.Errorf("the store's line does not name the directory that failed:\n%s", got)
	}
}

// TestABrokenMountIsNotSaidOncePerCorrection is #59's claim carried onto the
// refine door, which the issue's own text only reached by implication.
//
// The EVENT LOG is what is unwritable here, so every correction fails at its
// durable append — the one funnel R7 put the latch in — and the store's trouble
// is therefore a latched state rather than three separate discoveries of it.
// One line for the mount, whatever the fleet does next.
//
// The audit line still arrives per correction, and that is not a leak of the
// same fact: it names the entry and the conductor whose correction was lost,
// which is what an operator needs to put the memory back afterwards, and it
// claims nothing about the disk.
func TestABrokenMountIsNotSaidOncePerCorrection(t *testing.T) {
	st, dir := scoreStore(t)
	entries := []score.Entry{
		seed(t, st, "the agent asks before it deletes"),
		seed(t, st, "it runs the tests before it pushes"),
		seed(t, st, "the fleet keeps the build green"),
	}
	s := refineServer(t, st)
	// The probe OPENS FOR APPEND rather than writing: it is the operation
	// appendDurable itself makes, and a probe that truncated the log to find out
	// whether it could would destroy the store it is testing on the runs where
	// the chmod does not bind.
	events := filepath.Join(dir, "score-events.jsonl")
	unwritable(t, events, func() error {
		f, err := os.OpenFile(events, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	})

	logged := captureLog(t)
	for i, e := range entries {
		if got := refinePaced(t, s, conn("c1"), proto.Command{
			Action: "score.reword", ID: e.Id, Prompt: "a wording the store will never keep"}); got == "" {
			t.Fatalf("correction %d was accepted with the event log unwritable", i)
		}
	}
	got := logged()
	if n := strings.Count(got, "score writes are not landing"); n != 1 {
		t.Errorf("three corrections on one dead mount produced %d store lines, want 1:\n%s", n, got)
	}
	if n := strings.Count(got, "the conductor's correction was refused"); n != len(entries) {
		t.Errorf("the audit line arrived %d times for %d lost corrections, want one each:\n%s",
			n, len(entries), got)
	}
}
