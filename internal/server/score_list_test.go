package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// This file covers #42's operator surface: score.list stopped capping at the
// working set, and every entry it returns carries the arithmetic that ranked it.

// listReply is the decoded score.list payload: the ranked entries and the
// context they were ranked against.
type listReply struct {
	Context score.Context  `json:"context"`
	Entries []score.Ranked `json:"entries"`
}

// listed runs score.list for panel (empty for the contextless read) and decodes
// the reply.
func listed(t *testing.T, s *Server, panel string) listReply {
	t.Helper()
	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "score.list", ID: panel})
	msg := reply(t, cc)
	var out listReply
	if msg.Type != "score" || json.Unmarshal(msg.Score, &out) != nil {
		t.Fatalf("score.list must answer a score object, got %+v", msg)
	}
	return out
}

// TestScoreListIsUncapped is the gap #41's ops review measured and #42 asks R3
// to close: a live daemon holding eleven entries answered `entries:11
// rendered:7`, and the tier of entries eight through eleven appeared in no
// surface at all — not score.list, not score.status, not score.md, which has no
// tier column. Only the raw event log had it.
func TestScoreListIsUncapped(t *testing.T) {
	st, _ := scoreStore(t)
	const n = 11
	for i := range n {
		if _, _, err := st.Submit(entryText(i), score.Provenance{Source: "user"}); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}
	s, _ := scoreServer(st)

	got := listed(t, s, "").Entries
	if len(got) != n {
		t.Fatalf("score.list returned %d entries, want all %d the store holds", len(got), n)
	}
	for _, r := range got {
		if r.Tier < 1 {
			t.Fatalf("entry %s was listed without a tier: %+v", r.Id, r)
		}
	}
	// The working set is still a handful — the budget caps the BRIEF, not the
	// list — and status names both numbers so the gap explains itself.
	active := 0
	for _, r := range got {
		if r.Active {
			active++
		}
	}
	if st := status(t, s); st.Entries != n || st.Rendered != active || st.WorkingSet != active {
		t.Fatalf("status = %+v, want %d entries with %d of them injected", st, n, active)
	}
	if active >= n {
		t.Fatalf("%d of %d entries marked active — the list is not showing anything the brief withholds", active, n)
	}
}

// TestScoreListExplainsItsRanking is the obligation multiplicative scoring
// carries (#42, decision 5): a rank reported alone answers "why is this entry in
// my brief" with a number. The breakdown must be there, and multiplying it out
// must give back exactly the number beside it — a breakdown that does not
// reconcile is worse than none, because it invites an operator to trust it.
func TestScoreListExplainsItsRanking(t *testing.T) {
	st, _ := scoreStore(t)
	for i := range 5 {
		e, _, err := st.Submit(entryText(i), score.Provenance{
			Source: "agent", SourcePanel: "p1", SourceCwd: "/work/auth",
			SourceProfile: "claude", SourceGroup: "auth",
		})
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		// Some of them said twice, so tier and recency both vary across the list.
		if i%2 == 0 {
			if err := st.Reinforce(e.Id, "agent"); err != nil {
				t.Fatalf("reinforce: %v", err)
			}
		}
	}
	s, _ := scoreServer(st)

	list := listed(t, s, "")
	if list.Context != (score.Context{}) {
		t.Fatalf("a list asked from a cockpit echoed %+v, want the empty context", list.Context)
	}
	got := list.Entries
	if len(got) != 5 {
		t.Fatalf("score.list returned %d entries, want 5", len(got))
	}
	for _, r := range got {
		f := r.Factors
		if product := f.Tier * f.Recency * f.Cwd * f.Profile * f.Group; product != r.Rank {
			t.Fatalf("entry %s: factors %+v multiply to %v but the rank reads %v", r.Id, f, product, r.Rank)
		}
		if f.Tier != float64(r.Tier) {
			t.Fatalf("entry %s: the tier factor is %v but the tier is %d", r.Id, f.Tier, r.Tier)
		}
		// score.list is asked from a cockpit, which is not a panel, so there is no
		// directory, profile or group to match against and every context factor
		// reads one. Saying so here is what keeps the claim in scoreList's doc
		// checked rather than asserted.
		if f.Cwd != 1 || f.Profile != 1 || f.Group != 1 {
			t.Fatalf("entry %s: a contextless list matched a context dimension: %+v", r.Id, f)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Rank < got[i].Rank {
			t.Fatalf("score.list is out of rank order at %d: %v then %v", i, got[i-1].Rank, got[i].Rank)
		}
	}
}

// TestScoreListRawShapeCarriesTheBreakdown reads the wire bytes rather than a
// decoded struct, because the operator's reader is as often `jq` as it is Go:
// the field names are the surface, and a rename would pass every test above.
func TestScoreListRawShapeCarriesTheBreakdown(t *testing.T) {
	st, _ := scoreStore(t)
	if _, _, err := st.Submit("one real entry", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _ := scoreServer(st)

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "score.list"})
	msg := reply(t, cc)
	var raw struct {
		Context map[string]any   `json:"context"`
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(msg.Score, &raw); err != nil || len(raw.Entries) != 1 {
		t.Fatalf("score.list must answer one entry under a context: %s (%v)", msg.Score, err)
	}
	// The echo is an OBJECT even when it is empty, so `.context` is always there
	// to read; an absent key would be indistinguishable from an older daemon.
	if raw.Context == nil || len(raw.Context) != 0 {
		t.Fatalf("a contextless list echoed %v, want an empty object: %s", raw.Context, msg.Score)
	}
	for _, key := range []string{"id", "text", "tier", "rank", "factors", "active", "standing"} {
		if _, ok := raw.Entries[0][key]; !ok {
			t.Fatalf("score.list entry is missing %q: %s", key, msg.Score)
		}
	}
	factors, ok := raw.Entries[0]["factors"].(map[string]any)
	if !ok {
		t.Fatalf("factors is not an object: %s", msg.Score)
	}
	for _, key := range []string{"tier", "recency", "cwd", "profile", "group"} {
		if _, ok := factors[key]; !ok {
			t.Fatalf("the factor breakdown is missing %q: %s", key, msg.Score)
		}
	}
}

// TestScoreListRanksForANamedPanel is the question an operator actually has —
// not "what does the store think" but "why is this in the brief THAT panel
// gets". Every real direct dispatch fills cwd from the panel row and usually
// profile, so a contextless listing marks an `active` set no real dispatch will
// produce; naming the panel is what closes that gap, and the echoed context is
// what proves which of the two the reply is.
func TestScoreListRanksForANamedPanel(t *testing.T) {
	st, _ := scoreStore(t)
	// scoreServer's one panel is p1 in /work/auth, profile claude, group auth.
	here, _, err := st.Submit("a thing learned in this very directory", score.Provenance{
		Source: "agent", SourcePanel: "p1", SourceCwd: "/work/auth",
		SourceProfile: "claude", SourceGroup: "auth",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// Submitted LATER, so recency puts it ahead of the matching entry until the
	// context weights are allowed to speak.
	elsewhere, _, err := st.Submit("a thing learned somewhere else", score.Provenance{
		Source: "agent", SourcePanel: "p9", SourceCwd: "/work/api",
		SourceProfile: "codex", SourceGroup: "api",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _ := scoreServer(st)

	flat := listed(t, s, "")
	if flat.Context != (score.Context{}) {
		t.Fatalf("the contextless list echoed %+v, want the empty context", flat.Context)
	}
	if flat.Entries[0].Id != elsewhere.Id {
		t.Fatalf("contextless, the newer entry should lead; got %s", flat.Entries[0].Id)
	}

	for_p1 := listed(t, s, "p1")
	want := score.Context{Panel: "p1", Cwd: "/work/auth", Profile: "claude", Group: "auth"}
	if for_p1.Context != want {
		t.Fatalf("score.list for p1 echoed %+v, want %+v", for_p1.Context, want)
	}
	if for_p1.Entries[0].Id != here.Id {
		t.Fatalf("ranked for p1, its own directory's entry should lead; got %s", for_p1.Entries[0].Id)
	}
	f := for_p1.Entries[0].Factors
	if f.Cwd != 2 || f.Profile != 2 || f.Group != 2 {
		t.Fatalf("ranked for p1, the matching entry's context factors are %+v, want all 2.0", f)
	}
}

// TestScoreListRefusesAnUnknownPanel keeps the echo honest at its one weak
// point: answering a question about a panel the fleet does not have with the
// contextless listing would be exactly the ambiguity the echo exists to remove,
// and the operator would have no way to see it happened.
func TestScoreListRefusesAnUnknownPanel(t *testing.T) {
	st, _ := scoreStore(t)
	if _, _, err := st.Submit("one real entry", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _ := scoreServer(st)

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "score.list", ID: "nosuchpanel"})
	if msg := reply(t, cc); msg.Type != "error" || msg.Error != "no panel nosuchpanel" {
		t.Fatalf("score.list for an unknown panel = %+v, want a plain refusal", msg)
	}
}

// TestSubmitAndDispatchReadOneContext is what makes the cwd weight able to
// match at all: an entry's recorded provenance and the context a dispatch ranks
// against must come from ONE reader. Two readers is not untidiness — an entry
// whose directory was recorded one way can never equal a dispatch's read another
// way, so the weight would silently apply to some entries and not others.
//
// The failure this pins was measured: provenance took the panels row's own Cwd
// field, which is learned lazily, so an entry submitted from a panel spawned
// with --dir while it was still starting recorded no source_cwd — and provenance
// is written once, so that entry could never match a cwd again.
func TestSubmitAndDispatchReadOneContext(t *testing.T) {
	st, _ := scoreStore(t)
	s, _ := scoreServer(st)

	cc := conn("p1")
	s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "learned right here"})
	if msg := reply(t, cc); msg.Type != "score" {
		t.Fatalf("submit answered %+v", msg)
	}

	entries := listed(t, s, "").Entries
	if len(entries) != 1 {
		t.Fatalf("score.list = %+v, want the one submission", entries)
	}
	prov := entries[0].Provenance

	ctx, ok := s.panelContext("p1")
	if !ok {
		t.Fatal("the fixture's panel went missing")
	}
	if prov.SourceCwd != ctx.Cwd || prov.SourceProfile != ctx.Profile || prov.SourceGroup != ctx.Group {
		t.Fatalf("provenance %+v does not match the dispatch context %+v; the cwd weight can never fire", prov, ctx)
	}
	if ctx.Cwd == "" {
		t.Fatal("the fixture's panel has no directory, so this proves nothing")
	}

	// And the whole point of them agreeing: ranked for that panel, every context
	// dimension matches.
	for_p1 := listed(t, s, "p1")
	if f := for_p1.Entries[0].Factors; f.Cwd != 2 || f.Profile != 2 || f.Group != 2 {
		t.Fatalf("an entry submitted from p1 scored %+v against p1's own context, want a full match", f)
	}
}

// TestScoreStatusNamesTheCapThatBit is the third cap getting a name. Three
// things can make `rendered` fall short of `working_set` — an entry too long to
// inject, the injected block running out of runes, and the budget itself — and
// from the counts alone they look identical, which is exactly the argument that
// gave `oversized` its field. block_full is the one that is invisible from both
// the entry and the policy.
func TestScoreStatusNamesTheCapThatBit(t *testing.T) {
	st, dir := scoreStore(t)
	// Enough maximal entries to overrun the block, through the FILE since a
	// submission this long is refused, and a budget wide enough that only the
	// rune backstop can be what stops it.
	var md strings.Builder
	for i := range 40 {
		fmt.Fprintf(&md, "- %s%03d\n", strings.Repeat("z", 297), i)
	}
	editScoreMD(t, dir, md.String())
	st.SetPolicy(score.Policy{WorkingSet: 1_000_000})
	s, _ := scoreServer(st)

	got := status(t, s)
	switch {
	case got.Entries != 40:
		t.Fatalf("status = %+v, want all forty entries counted", got)
	case got.Rendered >= 40:
		t.Fatalf("status = %+v, want the block to have stopped short", got)
	case got.Oversized != 0:
		t.Fatalf("status = %+v, want no entry withheld for its own weight", got)
	case !got.BlockFull:
		t.Fatalf("status = %+v, want the block cap named as what stopped it", got)
	}

	// And the ordinary case says nothing: a budget that simply ran out is not
	// the block filling, and a field that is always true explains nothing.
	st.SetPolicy(score.Policy{WorkingSet: 3})
	if got := status(t, s); got.Rendered != 3 || got.BlockFull {
		t.Fatalf("status = %+v, want three injected and no block-full claim", got)
	}
}

// TestScoreStatusOmitsWeightsWithoutAStore keeps the reply from inventing a
// policy. Zero is not a weight the clamp can ever produce — every weight in
// force is at least one — so reporting `rank: {0,0,0,0}` for a store that is not
// running would be the one number on this surface that cannot be true.
func TestScoreStatusOmitsWeightsWithoutAStore(t *testing.T) {
	s, _ := scoreServer(nil)

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "score.status"})
	var raw map[string]any
	if err := json.Unmarshal(reply(t, cc).Score, &raw); err != nil {
		t.Fatalf("status must be an object: %v", err)
	}
	for _, key := range []string{"rank", "working_set", "promote_at", "user_signals_at"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("status reported %q with no store running: %v", key, raw)
		}
	}
	// What IS reported is the pair that carries the truth on this branch.
	if raw["available"] != false || raw["reason"] == nil {
		t.Fatalf("status = %v, want an unavailable store with a reason", raw)
	}

	// With a store, they come back — so the omission above is the branch and not
	// a field that was dropped.
	st, _ := scoreStore(t)
	live, _ := scoreServer(st)
	if got := status(t, live); got.Rank != (score.Rank{Recency: 2, Cwd: 2, Profile: 2, Group: 2}) {
		t.Fatalf("status = %+v, want the weights in force", got)
	}
}

// TestScoreStatusReportsTheTuningInForce is invariant I8 on the knobs
// themselves. A weight of 1.0 is indistinguishable in a score.list breakdown
// from a dimension that did not match — the factor reads 1.0 either way — so
// without the policy beside the list an operator cannot tell "switched off" from
// "no match", and cannot confirm a SIGHUP took their retune at all.
func TestScoreStatusReportsTheTuningInForce(t *testing.T) {
	st, _ := scoreStore(t)
	s, _ := scoreServer(st)

	if got := status(t, s); got.WorkingSet != 7 || got.UserSignalsAt != 2 ||
		got.Rank != (score.Rank{Recency: 2, Cwd: 2, Profile: 2, Group: 2}) {
		t.Fatalf("status = %+v, want the package defaults", got)
	}

	// A reload, exactly as the daemon's SIGHUP path performs it, and the reply
	// has to follow — clamped, which is the number actually in force rather than
	// the one the file asked for.
	st.SetPolicy(score.Policy{
		PromoteAt: 4, UserSignalsAt: 5, WorkingSet: 2,
		Rank: score.Rank{Recency: 3, Cwd: 0.5, Profile: 1, Group: 0},
	})
	want := score.Rank{Recency: 3, Cwd: 1, Profile: 1, Group: 2}
	got := status(t, s)
	if got.PromoteAt != 4 || got.UserSignalsAt != 5 || got.WorkingSet != 2 || got.Rank != want {
		t.Fatalf("status = %+v, want promote_at 4, user_signals_at 5, working_set 2 and rank %+v", got, want)
	}
}

// entryText is a distinct, injectable entry wording per index.
func entryText(i int) string {
	return "the fleet keeps doing the thing number " + string(rune('a'+i))
}
