package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// This file is R6's gate (#45, and #38 §4's rule applied to a second question):
// the refine verbs are the daemon's FIRST conductor-only surface, so they are
// the first place the answer to "is this the conductor" has to be taken from
// the server's own record rather than from what the connection said it was.

// refineServer is a socketless Server with a conductor panel, an ordinary agent
// panel, and st wired in — the smallest fleet in which the gate has both a right
// answer and a wrong one to choose between.
func refineServer(t *testing.T, st *score.Store) *Server {
	t.Helper()
	s, _, _ := gateServer(
		panel.Panel{ID: "c1", Kind: panel.Agent, State: panel.Running, Conductor: true},
		panel.Panel{ID: "w1", Kind: panel.Agent, State: panel.Idle},
	)
	WithScore(ScoreState{Store: st, Enabled: st != nil})(s)
	return s
}

// seed submits one entry as the operator, failing the test if the store refuses.
func seed(t *testing.T, st *score.Store, text string) score.Entry {
	t.Helper()
	e, _, err := st.Submit(text, score.Provenance{Source: score.SourceUser})
	if err != nil {
		t.Fatalf("submit %q: %v", text, err)
	}
	return e
}

// oneEntry is the preamble nearly every test in this file shares: a store
// holding one entry, behind refineServer's fleet. They are about the gate, the
// cap, the log or the reply, and not one of them is about how the entry got in.
func oneEntry(t *testing.T, text string) (*score.Store, score.Entry, *Server) {
	t.Helper()
	st, _ := scoreStore(t)
	return st, seed(t, st, text), refineServer(t, st)
}

// refineReply drives one refine command through onCommand — the same entry every
// wire command lands on, so guardConductor is in the path too — and returns the
// error text, or "" when the command was allowed.
func refineReply(t *testing.T, s *Server, cc *clientConn, cmd proto.Command) string {
	t.Helper()
	s.onCommand(cc, cmd)
	msg := reply(t, cc)
	if msg.Type == "error" {
		return msg.Error
	}
	if msg.Type != "score" {
		t.Fatalf("refine answered with %+v, want a score reply or an error", msg)
	}
	return ""
}

// TestRefineIsIdentifiedByTheConnectionNeverByTheClaim is the gate, and the
// third row is the one the whole design turns on: R4's monotone hello lets any
// connection declare `role: conductor` from empty, which was harmless only while
// declaring it merely ADDED refusals through guardConductor's deny list. Reserve
// something FOR the role and that reasoning inverts. So the role is not consulted
// at all — the server compares the panel the connection declared as its own
// against the panel it marked Conductor itself.
func TestRefineIsIdentifiedByTheConnectionNeverByTheClaim(t *testing.T) {
	st, e, s := oneEntry(t, "the agent asks before it deletes")

	for _, tc := range []struct {
		name    string
		self    string
		role    string
		refused bool
	}{
		{"the conductor panel's own connection", "c1", "", false},
		{"the conductor panel, having also declared the role", "c1", roleConductor, false},
		// The attack the gate exists for: an ordinary agent panel declaring the
		// role it was never given, over a hello R4 deliberately allows.
		{"a worker panel that declared the conductor role", "w1", roleConductor, true},
		{"a worker panel", "w1", "", true},
		// A cockpit is not a panel. The operator's surface on the score is their
		// own editor (#38 §3), not a verb.
		{"the operator's cockpit", "", "", true},
		{"the cockpit claiming the role without a panel", "", roleConductor, true},
		{"a self the fleet has no panel for", "ghost", roleConductor, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc := conn(tc.self)
			cc.role = tc.role
			got := refinePaced(t, s, cc, proto.Command{Action: "score.lower", ID: e.Id})
			switch {
			case tc.refused && !strings.Contains(got, "only the conductor"):
				t.Fatalf("refine answered %q, want the conductor-only refusal", got)
			case !tc.refused && got != "" && !strings.Contains(got, "bottom rung"):
				// Allowed through the gate: what comes back is then the STORE's own
				// answer about this entry, which sits on the bottom rung already.
				t.Fatalf("refine answered %q, want the store's answer rather than a refusal", got)
			}
		})
	}
	// Nothing above changed the entry, refused or not: a lower at the bottom is
	// the store refusing, and the gate refuses before the store is reached.
	if got := st.Render(score.Context{})[0]; got.Tier != 1 || got.Text != "the agent asks before it deletes" {
		t.Fatalf("entry after the whole table = %+v, want it untouched", got)
	}
}

// TestGuardConductorDoesNotFenceTheRefineVerbs is the other half of the same
// point. guardConductor is a DENY list — it restricts a conductor and passes
// everything else — so it can neither grant these verbs nor is it allowed to
// take them away from the one connection they belong to.
func TestGuardConductorDoesNotFenceTheRefineVerbs(t *testing.T) {
	s := refineServer(t, nil)
	cc := conn("c1")
	cc.role = roleConductor
	for _, action := range []string{"score.merge", "score.reword", "score.lower"} {
		if reason := s.guardConductor(cc, proto.Command{Action: action, ID: "e1"}); reason != "" {
			t.Errorf("guardConductor refused the conductor's own %s: %q", action, reason)
		}
	}
	// And it passes them for a worker too, which is precisely why it cannot be
	// the gate: everything the deny list says about a non-conductor is "allowed".
	worker := conn("w1")
	if reason := s.guardConductor(worker, proto.Command{Action: "score.merge", ID: "e1"}); reason != "" {
		t.Fatalf("guardConductor is not the gate, yet it refused a worker: %q", reason)
	}
}

// TestRefineIsRateCapped is minConductorSpawnGap's reasoning applied to the
// verb that edits a file the operator owns. Measured on a live daemon: sixty-one
// merges in nine tenths of a second collapsed a sixty-two entry store to one
// entry, with no undo short of reading the event log by hand.
//
// EVERY CALL HERE USES A FRESH CONNECTION, and that is the point of the test
// rather than a detail of it. The first version of this cap kept its stamp on
// the clientConn — beside the spawn cap, which had the same hole — and was
// driven from one long-lived conn, so it passed while being completely inert on
// the path the conductor uses: `baton mcp` dials per tool call and closes, and
// `baton ctl` is a process per command. Measured then: sixty-one merges in one
// and a half seconds, all admitted. A persistent-connection test cannot see that
// class of bug, so do not reintroduce one — the gapStamp is keyed by PANEL, the
// identity the gate above it uses, and a test must cross connections to see it.
func TestRefineIsRateCapped(t *testing.T) {
	st, _ := scoreStore(t)
	var ids []string
	for _, text := range []string{"one observation", "another observation", "a third observation"} {
		ids = append(ids, seed(t, st, text).Id)
	}
	s := refineServer(t, st)

	if got := refineReply(t, s, conn("c1"), proto.Command{Action: "score.reword", ID: ids[0], Prompt: "a corrected observation"}); got != "" {
		t.Fatalf("the first correction was refused: %q", got)
	}
	// A different verb, a different entry, a NEW connection: still refused,
	// because the cap is on the conductor panel's rate and not on any one socket,
	// entry or op.
	if got := refineReply(t, s, conn("c1"), proto.Command{Action: "score.merge", ID: ids[1], From: ids[2]}); !strings.Contains(got, "too fast") {
		t.Fatalf("an immediate second correction answered %q, want the rate refusal", got)
	}
	// It really did not happen — a rate refusal must not be a slow success.
	if got := st.Len(); got != 3 {
		t.Fatalf("store holds %d entries, want 3: the throttled merge must not have run", got)
	}

	// A burst of fresh connections, which is what a looping MCP conductor is: one
	// gets through per gap and no more.
	for i := range 20 {
		if got := refineReply(t, s, conn("c1"), proto.Command{Action: "score.merge", ID: ids[1], From: ids[2]}); !strings.Contains(got, "too fast") {
			t.Fatalf("fresh connection %d was admitted (%q); the cap must not be per-connection", i, got)
		}
	}
	if got := st.Len(); got != 3 {
		t.Fatalf("store holds %d entries after twenty fresh connections, want 3", got)
	}

	// The clock is the only thing holding it: pretend a quarter second passed.
	rewind(s, &s.refine, 2*minRefineGap)
	if got := refineReply(t, s, conn("c1"), proto.Command{Action: "score.merge", ID: ids[1], From: ids[2]}); got != "" {
		t.Fatalf("a correction past the gap was refused: %q", got)
	}
	if got := st.Len(); got != 2 {
		t.Fatalf("store holds %d entries, want 2 after the merge landed", got)
	}

	// A REFUSED correction keeps its stamp: a loop asking for something the store
	// will not do is still a loop holding the store's mutex.
	rewind(s, &s.refine, 2*minRefineGap)
	if got := refineReply(t, s, conn("c1"), proto.Command{Action: "score.lower", ID: ids[0]}); !strings.Contains(got, "bottom rung") {
		t.Fatalf("expected the store's own refusal, got %q", got)
	}
	if got := refineReply(t, s, conn("c1"), proto.Command{Action: "score.lower", ID: ids[0]}); !strings.Contains(got, "too fast") {
		t.Fatalf("a correction straight after a refused one answered %q, want the rate refusal", got)
	}

	// That a rate refusal does not push the next ALLOWED one away is asserted in
	// TestThrottleStampsOnlyWhenItAdmits and deliberately not here: this test can
	// only cross the gap by rewinding the stamp, and a rewind makes that
	// assertion pass whether a refusal stamped or not. It stood here as a comment
	// over code that ran no refusals at all, which is the shape of claim this
	// claim this file is about — the gapStamp takes its clock as a parameter so
	// the rule can be asserted rather than described.
}

// refinePaced is refineReply with the rate cap stepped over, for the tests whose
// subject is something else. It exists so that a test about the gate or about a
// store refusal cannot pass or fail for reasons belonging to the gapStamp — and
// so that removing the gapStamp does not quietly make those tests meaningless.
// The tests that ARE about the cap call refineReply directly and pace nothing.
func refinePaced(t *testing.T, s *Server, cc *clientConn, cmd proto.Command) string {
	t.Helper()
	rewind(s, &s.refine, 2*minRefineGap)
	return refineReply(t, s, cc, cmd)
}

// TestMergingAwayTheMemoryRaisesAnAlarm is the accident the rate cap cannot see.
// Paced at minRefineGap a conductor still merges four times a second, so a
// sixty-entry store is gone in fifteen seconds with every individual step
// looking reasonable — and recovery is hand-reading score-events.jsonl, which
// invariant I8 forbids.
//
// Nothing is refused: a large tidy-up is exactly what this verb is for, and
// blocking one to catch a loop is the wrong trade. The operator is told instead.
func TestMergingAwayTheMemoryRaisesAnAlarm(t *testing.T) {
	st, ids, s := alarmFleet(t, score.Policy{}, 12)

	logged := captureLog(t)
	var alarms int
	for _, id := range ids[1:] {
		rewind(s, &s.refine, 2*minRefineGap)
		if got := refineReply(t, s, conn("c1"), proto.Command{Action: "score.merge", ID: ids[0], From: id}); got != "" {
			t.Fatalf("merge of %s was refused: %q", id, got)
		}
		if n := strings.Count(logged(), "more than half the fleet's memory"); n > alarms {
			alarms = n
			// The alarm has to arrive while there is still something to save, which
			// is what a proportion buys over a count: at half of twelve, not at the
			// last merge.
			if got := st.Len(); got < 3 {
				t.Fatalf("the alarm arrived with %d entries left, too late to act on", got)
			}
		}
	}
	if alarms != 1 {
		t.Fatalf("the collapse raised %d alarms, want exactly 1 in one window", alarms)
	}
	// Every merge still happened: an alarm is a signal, not a fence.
	if got := st.Len(); got != 1 {
		t.Fatalf("store holds %d entries, want 1: nothing may have been refused", got)
	}
}

// TestTheAlarmsFiguresAreWhatTheyClaim pins the alarm's tuned numbers in the
// LOOSENING direction — the direction each one's comment actually argues about,
// and the one this file shipped without.
//
// Read this before touching the constants. Every figure was argued at length
// beside its declaration and asserted only from the tightening side, so raising
// the window to an hour and dropping the share to a tenth both passed the whole
// suite while contradicting the paragraphs that justify them. A test that proves
// the alarm FIRES leaves every loosening free; what has to be asserted is the
// property the prose claims, which is as much about when the alarm stays SILENT
// as about when it sounds.
//
// It is arithmetic over mergeAlarm and needs no store, no server and no log,
// because the alarm is a type with a clock-injectable method — the shape the
// gapStamp already had, and the reason its own rule was pinnable. The figures
// below are LITERALS on purpose: written in terms of the constants they would
// move with them and could never catch a retuning.
func TestTheAlarmsFiguresAreWhatTheyClaim(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		entries int           // what the store holds when the run starts
		merges  int           // merges in the first window, one entry each
		floor   int           // what the caller read off the store's policy
		rewind  time.Duration // how far the window's start is pushed back, then…
		again   int           // …this many more merges
		want    int           // alarms the whole run must raise
	}{
		// A quarter of the store is not half of it, and the comment says a tenth
		// would fire on any ordinary tidy-up.
		{name: "a quarter drop is silent", entries: 20, merges: 5, floor: 8, want: 0},
		// And half of it is, on the merge that reaches half rather than later.
		{name: "half is not", entries: 20, merges: 10, floor: 8, want: 1},
		// Once per window, however far past half the collapse goes.
		{name: "a collapse alarms once, not once a merge", entries: 20, merges: 19, floor: 8, want: 1},
		// A store AT the floor alarms, which is what stops the floor being raised;
		// the row below is what stops it being lowered.
		{name: "a store at the floor alarms", entries: 8, merges: 4, floor: 8, want: 1},
		// Below it, everything the fleet remembers is already in every brief, and
		// halving it is ordinary.
		{name: "a store below the floor is silent", entries: 7, merges: 6, floor: 8, want: 0},
		// A second window can alarm again, so a collapse outlasting the window is
		// not reported once and forgotten. Rewinding by TWO MINUTES is what makes
		// this an assertion about a one-minute window rather than about any window:
		// the five merges that follow are half of what the NEW window opens on
		// rather than half of the original twenty.
		{name: "a later window alarms again", entries: 20, merges: 10, floor: 8,
			rewind: 2 * time.Minute, again: 5, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a mergeAlarm
			left, fired := alarmRun(&a, tc.entries, tc.merges, tc.floor, t0)
			if tc.rewind > 0 {
				a.at = a.at.Add(-tc.rewind)
				var more int
				left, more = alarmRun(&a, left, tc.again, tc.floor, t0)
				fired += more
			}
			if fired != tc.want {
				t.Fatalf("a store of %d merged down to %d above a floor of %d raised %d alarms, want %d",
					tc.entries, left, tc.floor, fired, tc.want)
			}
		})
	}
}

// alarmRun drives merges through one alarm, one entry lost per merge, and counts
// the warnings. It answers what the store is left holding, so a second run can
// carry on from it.
func alarmRun(a *mergeAlarm, entries, merges, floor int, now time.Time) (left, fired int) {
	for range merges {
		if _, alarm := a.note(entries, entries-1, floor, now); alarm {
			fired++
		}
		entries--
	}
	return entries, fired
}

// TestTheAlarmFloorIsTheWorkingSet is the floor's derivation asserted as
// BEHAVIOUR, and it is the assertion a mirrored constant could not carry.
//
// The floor is "the smallest store that does not fit entirely into one brief",
// which is the working set plus one. That used to be the literal 8, hand-copied
// from internal/score's default, with a cross-package test to keep the copy
// honest — and the copy is only right for a fleet that never tunes
// score.working-set. Set it to twelve, as an operator may and as #46 will let
// them do without a restart, and every store of eight to twelve alarmed on a
// halving while fitting whole into every brief: precisely the alarm people learn
// to ignore that the floor exists to prevent.
//
// So the floor is read off the store's own Policy at the call site, and this
// drives real merges through the server at four working-set/store-size
// combinations — two either side of the default's floor, two either side of a
// tuned one. Both directions are here: the rows that must alarm stop the floor
// drifting up, the rows that must stay silent stop it drifting down, and the
// tuned pair is what fails if anyone puts a constant back.
func TestTheAlarmFloorIsTheWorkingSet(t *testing.T) {
	for _, tc := range []struct {
		name    string
		working int // 0 leaves score's own default in force
		entries int
		alarm   bool
	}{
		{"default working set, a store of eight does not fit in one brief", 0, 8, true},
		{"default working set, a store of seven does", 0, 7, false},
		{"tuned to twelve, a store of twelve now fits in one brief", 12, 12, false},
		{"tuned to twelve, a store of thirteen does not", 12, 13, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, ids, s := alarmFleet(t, score.Policy{WorkingSet: tc.working}, tc.entries)
			logged := captureLog(t)
			// Exactly half, rounded up: the smallest collapse the share alarms on,
			// so what the row is about is the floor and never the percentage.
			mergeAway(t, s, ids, 1, 1+(tc.entries+1)/2)
			if got := alarmed(logged()); got != tc.alarm {
				t.Fatalf("a %d-entry store with a working set of %d (%d entries left) alarmed=%v, want %v:\n%s",
					tc.entries, st.Policy().WorkingSet, st.Len(), got, tc.alarm, logged())
			}
		})
	}
}

// alarmFleet is a store of n entries, tuned by p, behind a conductor-gated
// server. The policy is a parameter because the alarm's floor is now read off it
// rather than mirrored from a constant.
func alarmFleet(t *testing.T, p score.Policy, n int) (*score.Store, []string, *Server) {
	t.Helper()
	st, _ := scoreStoreTuned(t, p)
	var ids []string
	for i := range n {
		ids = append(ids, seed(t, st, fmt.Sprintf("observation number %d", i)).Id)
	}
	return st, ids, refineServer(t, st)
}

// mergeAway merges ids[from:to] into ids[0], paced past the rate cap so that the
// subject of these tests is the alarm and never the gapStamp.
func mergeAway(t *testing.T, s *Server, ids []string, from, to int) {
	t.Helper()
	for _, id := range ids[from:to] {
		if got := refinePaced(t, s, conn("c1"), proto.Command{Action: "score.merge", ID: ids[0], From: id}); got != "" {
			t.Fatalf("merge of %s was refused: %q", id, got)
		}
	}
}

// alarms counts the slow-collapse warnings in captured log output, and alarmed
// is the yes/no. One helper for both so the two readings cannot drift.
func alarms(logged string) int {
	return strings.Count(logged, "more than half the fleet's memory")
}

func alarmed(logged string) bool { return alarms(logged) > 0 }

// TestAnExitedConductorLosesTheWriteSurface: the gate asks whether the panel is
// the conductor AND whether it is still there. A conductor whose process exited
// keeps its row until the operator purges it — that is what lets a respawn keep
// the id — so a flag check alone would leave the memory writable by a connection
// whose agent is gone.
func TestAnExitedConductorLosesTheWriteSurface(t *testing.T) {
	_, e, s := oneEntry(t, "the agent asks before it deletes")

	// Alive: the store answers, which is what makes the assertion below about the
	// state rather than about some other refusal.
	if got := refinePaced(t, s, conn("c1"), proto.Command{Action: "score.lower", ID: e.Id}); !strings.Contains(got, "bottom rung") {
		t.Fatalf("a live conductor was refused: %q", got)
	}

	s.mu.Lock()
	s.panels[s.indexLocked("c1")].State = panel.Exited
	s.mu.Unlock()

	if got := refinePaced(t, s, conn("c1"), proto.Command{Action: "score.lower", ID: e.Id}); !strings.Contains(got, "only the conductor") {
		t.Fatalf("an exited conductor answered %q, want the conductor-only refusal", got)
	}
	// A respawn puts the same id back in a live state, and the surface returns
	// with it — the id is the identity, the state is the liveness.
	s.mu.Lock()
	s.panels[s.indexLocked("c1")].State = panel.Running
	s.mu.Unlock()
	if got := refinePaced(t, s, conn("c1"), proto.Command{Action: "score.lower", ID: e.Id}); !strings.Contains(got, "bottom rung") {
		t.Fatalf("a respawned conductor was refused: %q", got)
	}
}

// TestRefineCarriesTheStoresOwnRefusals: past the gate, the answer is the
// store's. The server adds no second opinion about what a correction may be.
func TestRefineCarriesTheStoresOwnRefusals(t *testing.T) {
	_, e, s := oneEntry(t, "run the linter first")

	for _, tc := range []struct {
		cmd  proto.Command
		want string
	}{
		{proto.Command{Action: "score.merge", ID: e.Id, From: "abc123"}, `no entry "abc123"`},
		{proto.Command{Action: "score.merge", ID: e.Id, From: e.Id}, "into itself"},
		{proto.Command{Action: "score.reword", ID: e.Id}, "needs the new wording"},
		{proto.Command{Action: "score.reword", ID: "abc123", Prompt: "x"}, `no entry "abc123"`},
		{proto.Command{Action: "score.lower", ID: e.Id}, "bottom rung"},
	} {
		if got := refinePaced(t, s, conn("c1"), tc.cmd); !strings.Contains(got, tc.want) {
			t.Errorf("%s answered %q, want it to carry %q", tc.cmd.Action, got, tc.want)
		}
	}
}

// captureLog redirects the package's global zerolog for the rest of the test and
// hands back a reader of what was written.
//
// IT SWAPS A PACKAGE GLOBAL, so it is safe only while this package's tests run
// one at a time. Adding t.Parallel() to any test here — not merely to one that
// captures — would let two tests share log.Logger and race this buffer, and the
// symptom would be a flaky assertion about a line that went to the other test's
// buffer rather than anything that looks like a data race. Nothing else in this package needed
// it: the log is normally the operator's surface and the tests assert on the
// wire. Refusals have no wire audience beyond the caller being refused, so the
// log line IS the behaviour here, and prose alone would not have caught its
// absence.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	saved := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = saved })
	return buf.String
}

// TestEveryRefineOutcomeIsLogged is the symmetry: a refused correction must be
// at least as visible as an accepted one.
//
// It was not. Fifteen impersonation attempts from a worker panel left no daemon
// log line at all while every success logged at Info, so the daemon was quietest
// about the one event an operator would want to see — the same shape as the
// silent plugin veto R5 fixed. Every exit from scoreRefine now logs, refusals at
// Warn.
func TestEveryRefineOutcomeIsLogged(t *testing.T) {
	st, _ := scoreStore(t)
	e := seed(t, st, "the agent asks before it deletes")

	for _, tc := range []struct {
		name  string
		store *score.Store
		conn  func() *clientConn
		cmd   proto.Command
		level string
		want  string
	}{
		{
			name: "an impostor", store: st,
			conn:  func() *clientConn { cc := conn("w1"); cc.role = roleConductor; return cc },
			cmd:   proto.Command{Action: "score.lower", ID: e.Id},
			level: "warn", want: "not the conductor panel",
		},
		{
			name: "a store that is not there", store: nil,
			conn:  func() *clientConn { return conn("c1") },
			cmd:   proto.Command{Action: "score.lower", ID: e.Id},
			level: "warn", want: "the store is not available",
		},
		{
			name: "a refusal from the store", store: st,
			conn:  func() *clientConn { return conn("c1") },
			cmd:   proto.Command{Action: "score.merge", ID: e.Id, From: "abc123"},
			level: "warn", want: "correction was refused",
		},
		{
			name: "a correction that landed", store: st,
			conn:  func() *clientConn { return conn("c1") },
			cmd:   proto.Command{Action: "score.reword", ID: e.Id, Prompt: "the agent asks first"},
			level: "info", want: "corrected the fleet's memory",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureLog(t)
			s := refineServer(t, tc.store)
			s.onCommand(tc.conn(), tc.cmd)
			got := logged()
			if !strings.Contains(got, tc.want) {
				t.Fatalf("log = %q, want a line saying %q", got, tc.want)
			}
			if !strings.Contains(got, `"level":"`+tc.level+`"`) {
				t.Fatalf("log = %q, want it at %s", got, tc.level)
			}
		})
	}
}

// TestARateRefusalIsLogged keeps the gapStamp out of the same blind spot: a
// conductor stuck in a loop is the case the cap exists for, and an operator
// wondering why their memory stopped changing needs the daemon to say so.
func TestARateRefusalIsLogged(t *testing.T) {
	_, e, s := oneEntry(t, "the agent asks before it deletes")
	cc := conn("c1")
	s.onCommand(cc, proto.Command{Action: "score.reword", ID: e.Id, Prompt: "the agent asks first"})
	_ = reply(t, cc)

	logged := captureLog(t)
	s.onCommand(cc, proto.Command{Action: "score.reword", ID: e.Id, Prompt: "the agent asks before removing"})
	if got := logged(); !strings.Contains(got, "correcting too fast") || !strings.Contains(got, `"level":"warn"`) {
		t.Fatalf("log = %q, want a warning that the conductor is correcting too fast", got)
	}
}

// TestRefineReportsItsAliasEvictions is the counter that could not be seen. An
// eviction means a prior wording will no longer fold, so the entry it would have
// joined gains a twin instead — the store choosing to remember less, which
// invariant I8 says the operator is entitled to watch it do. ScoreCounters
// reports evictions only on a reconcile Delta, and a conductor's correction
// produces none, so the two paths that generate the most had been the only two
// that could report none.
func TestRefineReportsItsAliasEvictions(t *testing.T) {
	_, e, s := oneEntry(t, "wording 0")
	// Fill the alias list to its cap, so the next reword has to push one out.
	for i := 1; i <= maxScoreAliasesForTest; i++ {
		if got := refinePaced(t, s, conn("c1"), proto.Command{
			Action: "score.reword", ID: e.Id, Prompt: fmt.Sprintf("wording %d", i)}); got != "" {
			t.Fatalf("reword %d was refused: %q", i, got)
		}
	}

	logged := captureLog(t)
	if got := refinePaced(t, s, conn("c1"), proto.Command{
		Action: "score.reword", ID: e.Id, Prompt: "one wording too many"}); got != "" {
		t.Fatalf("the last reword was refused: %q", got)
	}
	if got := logged(); !strings.Contains(got, `"alias_evictions":1`) {
		t.Fatalf("log = %q, want exactly one eviction reported on the action that caused it. "+
			"If internal/score's maxAliases has GROWN, this test filled the list short of the cap and "+
			"evicted nothing: raise maxScoreAliasesForTest to match", got)
	}
}

// maxScoreAliasesForTest mirrors internal/score's unexported maxAliases, which
// this package cannot see. It has to be KEPT IN STEP by hand, and the assertion
// above is what says so when it drifts — a larger cap in the store means this
// test stops forcing an eviction at all, which is why that failure names the
// constant rather than leaving the reader to work it out from a missing log
// field. A smaller one is harmless: it evicts more than once and the count
// assertion catches that too.
const maxScoreAliasesForTest = 8

// TestRefineOnADisabledStore gets the same plain reason every other score verb
// gives, which is invariant I8: off, unavailable and broken are three states,
// and a conductor must not have to read the daemon log to tell them apart.
func TestRefineOnADisabledStore(t *testing.T) {
	s := refineServer(t, nil)
	got := refineReply(t, s, conn("c1"), proto.Command{Action: "score.lower", ID: "abc123"})
	if !strings.Contains(got, "disabled") {
		t.Fatalf("refine on a disabled store answered %q, want the subsystem's own reason", got)
	}
}

// TestRefineReplyNamesTheEntry: the payload is the entry and the operation, so a
// client that pipelined several corrections can tell the answers apart.
func TestRefineReplyNamesTheEntry(t *testing.T) {
	_, e, s := oneEntry(t, "run the linter frist")

	cc := conn("c1")
	s.onCommand(cc, proto.Command{Action: "score.reword", ID: e.Id, Prompt: "run the linter first"})
	msg := reply(t, cc)
	var got struct{ Id, Op string }
	if msg.Type != "score" || json.Unmarshal(msg.Score, &got) != nil {
		t.Fatalf("reword answered %+v, want a score payload", msg)
	}
	if got.Id != e.Id || got.Op != "reword" {
		t.Fatalf("reply = %+v, want %s reworded", got, e.Id)
	}
}
