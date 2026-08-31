package server

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// This file covers the server's half of #38 §4's second source: a brief the USER
// dispatched reinforces whatever entry it repeats, and a brief an agent
// dispatched does not.

// seedEntry puts one agent-sourced entry in the store and returns it, so a test
// about the DISPATCH is not also a test about how the entry got there.
func seedEntry(t *testing.T, st *score.Store, text string) score.Entry {
	t.Helper()
	e, _, err := st.Submit(text, score.Provenance{Source: score.SourceAgent, SourcePanel: "p1"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return e
}

// dispatch drives panel.dispatch exactly as a client would, which is the whole
// point: the signal is recorded inside dispatchFiltered's action, AFTER the hook
// chain has passed and the delivery has succeeded, so a test that called
// dispatchBrief directly would be testing the half of the path that no longer
// carries the behaviour.
func dispatch(t *testing.T, s *Server, cc *clientConn, prompt string) {
	t.Helper()
	s.onCommand(cc, proto.Command{Action: "panel.dispatch", ID: "p1", Prompt: prompt})
}

// entryNow re-reads the store's copy of e.
func entryNow(t *testing.T, st *score.Store, id string) score.Entry {
	t.Helper()
	for _, e := range st.Render(score.Context{}) {
		if e.Id == id {
			return e
		}
	}
	t.Fatalf("entry %s is gone", id)
	return score.Entry{}
}

// TestTheUsersBriefReinforcesWhatItRepeats is #38 §4's discrimination end to
// end. The connection is what decides, so the same brief through a cockpit and
// through an agent panel has to land differently — and it is cc, the connection
// that ASKED for the dispatch, not the panel it lands on: every dispatch here
// targets the same agent panel.
func TestTheUsersBriefReinforcesWhatItRepeats(t *testing.T) {
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
			s, _ := scoreServer(st)
			e := seedEntry(t, st, "keep the build green")

			// Not byte-identical to the entry: the match is the folding
			// normaliser's, which is what #38 §4 asks for.
			dispatch(t, s, conn(tc.self), "Keep the build green.")

			got := entryNow(t, st, e.Id)
			if got.UserSignals != tc.wantSignals {
				t.Fatalf("user signals = %d, want %d", got.UserSignals, tc.wantSignals)
			}
			if got.Reinforcements != tc.wantSignals {
				t.Fatalf("reinforcements = %d, want %d: only the user's brief counts",
					got.Reinforcements, tc.wantSignals)
			}
		})
	}
}

// TestABriefAdmitsNothing is what separates a dispatch from a submission. Every
// direct dispatch passes through here, so a brief that matched nothing must
// leave no entry behind — otherwise a week of ordinary work would fill score.md
// with one line per task.
func TestABriefAdmitsNothing(t *testing.T) {
	st, _ := scoreStore(t)
	s, _ := scoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	for range 5 {
		dispatch(t, s, conn(""), "please refactor the auth package")
	}

	if st.Len() != 1 {
		t.Fatalf("the store holds %d entries after five dispatches, want the one that was seeded", st.Len())
	}
	if got := entryNow(t, st, e.Id); got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want it untouched by briefs that matched nothing", got)
	}
}

// TestTheUsersBriefCanGrantTheTopTier is the whole point of the path, checked
// through the server rather than the store: repeating a thing in the briefs the
// user writes IS the user asking repeatedly, which is what #37 reserves the top
// tier for.
func TestTheUsersBriefCanGrantTheTopTier(t *testing.T) {
	st, _ := scoreStore(t)
	s, _ := scoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	// Three dispatches: the entry climbs the ordinary ladder on the first two and
	// the last takes it past the ceiling the user's own signals lifted.
	for range 3 {
		dispatch(t, s, conn(""), "keep the build green")
	}

	got := entryNow(t, st, e.Id)
	if got.Tier != 3 {
		t.Fatalf("entry = %+v, want the top tier after three of the user's own briefs", got)
	}
	// And an agent saying it as often changes nothing about the ceiling.
	other := seedEntry(t, st, "run gofmt before pushing")
	for range 20 {
		dispatch(t, s, conn("p1"), "run gofmt before pushing")
	}
	if got := entryNow(t, st, other.Id); got.Tier != 1 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want an agent's briefs to have counted for nothing", got)
	}
}

// TestABriefIsNotQuotedBackAtItself pins the ordering. The signal is recorded
// AFTER the working set is rendered, so the block a dispatch carries is the
// memory as it stood when the user typed — a brief cannot promote the entry it
// repeats and then be handed that promotion inside the same dispatch.
//
// The tier WORDING is what makes the difference visible, since it is the only
// part of the block a raise changes.
func TestABriefIsNotQuotedBackAtItself(t *testing.T) {
	wording := map[int]string{1: "[noted]", 2: "[note and take care]", 3: "[important]"}

	st, _ := scoreStore(t)
	s, delivered := scoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	var raises int
	for i := range 3 {
		before := entryNow(t, st, e.Id).Tier
		*delivered = (*delivered)[:0]
		dispatch(t, s, conn(""), "keep the build green")
		sent := string(*delivered)
		after := entryNow(t, st, e.Id).Tier

		if !strings.Contains(sent, wording[before]) {
			t.Fatalf("dispatch %d delivered %q, want the tier %d it had before the dispatch",
				i+1, sent, before)
		}
		if after > before {
			raises++
			if strings.Contains(sent, wording[after]) {
				t.Fatalf("dispatch %d delivered the tier %d its own signal earned:\n%s", i+1, after, sent)
			}
		}
	}
	// Without a raise the check above is vacuous — every block would carry the
	// tier it started and ended on.
	if raises == 0 {
		t.Fatal("no dispatch raised the entry, so nothing was actually ordered")
	}
}

// TestABriefIsQuietWithTheStoreOff keeps the dispatch path free of the memory's
// problems. score.enabled: false leaves the server holding a nil store, and a
// dispatch must come out exactly as it did before Score existed: the operator's
// own prompt, no score block, and the panel's context still filled in. The
// signal is skipped before the store is ever reached, so the disabled fleet
// cannot be refused a dispatch by a subsystem it switched off.
func TestABriefIsQuietWithTheStoreOff(t *testing.T) {
	s, delivered := scoreServer(nil)
	dispatch(t, s, conn(""), "keep the build green")
	if got := string(*delivered); got != "keep the build green\n" {
		t.Fatalf("delivered %q, want the operator's own prompt and no score block", got)
	}
	// The brief still carries the panel's context, which the score block is only
	// one field of.
	b := s.dispatchBrief("p1", "keep the build green")
	if b.Score != "" {
		t.Fatalf("brief = %+v, want no score block from a store that is not there", b)
	}
	if b.Panel != "p1" || b.Cwd != "/work/auth" || b.Group != "auth" || b.Profile != "claude" {
		t.Fatalf("brief = %+v, want the panel's own context filled in regardless", b)
	}
}

// TestAVetoedBriefCountsNothing is the ordering F5 turned on, and the reason the
// signal moved out of dispatchBrief: the brief is BUILT as an argument to
// dispatchFiltered, so anything counted while building it is counted before the
// hook chain has had its say. A task.pre veto means nothing reached an agent,
// and a brief that reached no agent is not the user telling the fleet anything.
func TestAVetoedBriefCountsNothing(t *testing.T) {
	st, _ := scoreStore(t)
	s, delivered := scoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	s.onFilterTask = func(TaskBrief) (TaskBrief, bool) { return TaskBrief{}, false }
	dispatch(t, s, conn(""), "keep the build green")

	if len(*delivered) != 0 {
		t.Fatalf("a vetoed dispatch delivered %q", string(*delivered))
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want a vetoed brief to have counted nothing", got)
	}

	// And with the veto lifted the very same brief does count, so the check above
	// is the hook's doing rather than the match failing.
	s.onFilterTask = nil
	dispatch(t, s, conn(""), "keep the build green")
	if got := entryNow(t, st, e.Id); got.UserSignals != 1 {
		t.Fatalf("entry = %+v, want the unvetoed brief counted", got)
	}
}

// TestABriefToAnUnknownPanelCountsNothing is the other half of the same rule:
// the delivery itself can fail, and a signal recorded before it is a
// reinforcement for a task no agent ever saw.
func TestABriefToAnUnknownPanelCountsNothing(t *testing.T) {
	st, _ := scoreStore(t)
	s, _ := scoreServer(st)
	e := seedEntry(t, st, "keep the build green")

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch", ID: "nosuchpanel", Prompt: "keep the build green"})
	if msg := reply(t, cc); msg.Type != "error" {
		t.Fatalf("dispatch to an unknown panel replied %+v, want an error", msg)
	}
	if got := entryNow(t, st, e.Id); got.UserSignals != 0 || got.Reinforcements != 0 {
		t.Fatalf("entry = %+v, want a failed delivery to have counted nothing", got)
	}
}

// TestAHelloCannotDropAFence is not about Score, and it is here because Score is
// what made it visible: R4 keys the top tier on cc.self, which sent someone
// looking at where cc.self comes from.
//
// A hello's role and self were re-assignable on a live connection, so a peer
// could greet as conductor panel 5, greet again with neither, and leave the
// conductor fence behind on a connection the daemon had already admitted. The
// rule is monotonic now: declaring one from empty is allowed, because it only
// ever adds a restriction; changing or clearing one is refused.
func TestAHelloCannotDropAFence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first       proto.Command
		second      proto.Command
		wantDropped bool
	}{
		{"a fenced connection cannot go plain",
			proto.Command{Action: "hello", Role: roleConductor, Self: "p1"},
			proto.Command{Action: "hello"}, true},
		{"a declared panel cannot become another",
			proto.Command{Action: "hello", Self: "p1"},
			proto.Command{Action: "hello", Self: "p2"}, true},
		{"a plain connection may declare what it is",
			proto.Command{Action: "hello"},
			proto.Command{Action: "hello", Role: roleConductor, Self: "p1"}, false},
		{"re-declaring the same thing is idempotent",
			proto.Command{Action: "hello", Role: roleConductor, Self: "p1"},
			proto.Command{Action: "hello", Role: roleConductor, Self: "p1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := scoreStore(t)
			s, _ := scoreServer(st)
			cc := conn("")

			s.onCommand(cc, tc.first)
			drain(cc)
			s.onCommand(cc, tc.second)

			var dropped bool
			for {
				select {
				case msg := <-cc.out:
					if msg.Type == "goodbye" {
						dropped = true
					}
					continue
				default:
				}
				break
			}
			if dropped != tc.wantDropped {
				t.Fatalf("second hello dropped=%v, want %v", dropped, tc.wantDropped)
			}
			// What the connection IS afterwards is the half that matters: a refused
			// re-hello must leave the fence standing, not merely send a goodbye.
			if tc.wantDropped && cc.role != tc.first.Role {
				t.Fatalf("role = %q after a refused re-hello, want %q", cc.role, tc.first.Role)
			}
			if tc.wantDropped && cc.self != tc.first.Self {
				t.Fatalf("self = %q after a refused re-hello, want %q", cc.self, tc.first.Self)
			}
		})
	}
}

// drain empties a connection's queued messages, so a later assertion reads only
// what the command under test produced.
func drain(cc *clientConn) {
	for {
		select {
		case <-cc.out:
		default:
			return
		}
	}
}

// TestConnProvenanceIsTheOneDiscrimination keeps score.submit and a dispatched
// brief asking the same question of a connection. They used to ask it in two
// places, and invariant I6 rests on the answer, so a second reading that drifted
// would be a door into the top tier that nobody had noticed opening.
func TestConnProvenanceIsTheOneDiscrimination(t *testing.T) {
	st, _ := scoreStore(t)
	s, _ := scoreServer(st)

	if got := s.connProvenance(conn("")); got != (score.Provenance{Source: score.SourceUser}) {
		t.Fatalf("a cockpit connection = %+v, want the user and none of the three dimensions", got)
	}
	// The agent branch carries the three ranking dimensions, read through
	// panelContext so an entry's recorded cwd and a dispatch's can be equal.
	want := score.Provenance{
		Source: score.SourceAgent, SourcePanel: "p1",
		SourceCwd: "/work/auth", SourceProfile: "claude", SourceGroup: "auth",
	}
	if got := s.connProvenance(conn("p1")); got != want {
		t.Fatalf("an agent connection = %+v, want %+v", got, want)
	}
	// A self naming no panel in the fleet is still an agent: the id came off a
	// hello, and a row that has since gone only costs the three dimensions.
	if got := s.connProvenance(conn("gone")); got != (score.Provenance{Source: score.SourceAgent, SourcePanel: "gone"}) {
		t.Fatalf("an unknown panel = %+v, want an agent with no dimensions", got)
	}
}
