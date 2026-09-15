package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/score"
)

// TestTheHintIsTheWholeSectionForAnEmptyStore is the cold start, and it is the
// reason the hint exists at all. score.renderBlock returns the empty string for a
// store with no entries, so before this a default install delivered briefs with
// no score section whatever: the agent was never told the memory existed, never
// submitted, and the store it would have filled stayed empty for as long as the
// fleet ran. The hint is the only part of the section rendered for an empty store,
// and that asymmetry is deliberate.
func TestTheHintIsTheWholeSectionForAnEmptyStore(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)

	b := s.dispatchBrief("p1", "fix the login flow")
	if b.Score != scoreHintLine {
		t.Fatalf("score section = %q, want exactly the hint for a store with nothing in it", b.Score)
	}
}

// TestTheHintFollowsTheBlock pins the order and the join. The working set is what
// the fleet KNOWS and the hint is what the agent may add, so the hint reads as a
// closing instruction under the box rather than as an entry inside it — and the
// block already ends in its own newline, so anything between them here would be a
// blank line in every brief the fleet delivers.
func TestTheHintFollowsTheBlock(t *testing.T) {
	st, _ := scoreStore(t)
	if _, _, err := st.Submit("prefer table-driven tests", score.Provenance{Source: score.SourceUser}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _, _ := scoreServer(st)

	b := s.dispatchBrief("p1", "fix the login flow")
	block, ok := strings.CutSuffix(b.Score, scoreHintLine)
	if !ok {
		t.Fatalf("score section = %q, want it to end with the hint", b.Score)
	}
	if !strings.Contains(block, "prefer table-driven tests") {
		t.Fatalf("block = %q, want the working set still rendered above the hint", block)
	}
	if !strings.HasSuffix(block, "───────────\n") {
		t.Fatalf("block = %q, want the hint to start on the line after the block's own border", block)
	}
}

// TestAProfileMayRefuseTheHintAndStillBeShownTheMemory is the per-profile switch
// doing the one thing it is for. Reading the memory and feeding it are different
// permissions: a one-shot runner that has nothing to notice should still be told
// what the fleet already knows, so switching its feedback off must take the
// sentence and leave the block.
func TestAProfileMayRefuseTheHintAndStillBeShownTheMemory(t *testing.T) {
	st, _ := scoreStore(t)
	if _, _, err := st.Submit("prefer table-driven tests", score.Provenance{Source: score.SourceUser}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _, _ := scoreServer(st)
	WithScoreFeedback(true, map[string]bool{"claude": false})(s)

	b := s.dispatchBrief("p1", "fix the login flow")
	if strings.Contains(b.Score, scoreHintLine) {
		t.Fatalf("score section = %q, want no hint for a profile that refused it", b.Score)
	}
	if !strings.Contains(b.Score, "prefer table-driven tests") {
		t.Fatalf("score section = %q, want the working set still delivered to it", b.Score)
	}
}

// TestTheProfileOverridesTheFleetBothWays is the layering AgentProfile documents:
// set on the profile wins, unset inherits. The second direction is the one worth
// a test of its own — a table that carried only the true entries would pass the
// first case and silently drop the second, which is exactly how AgentLog is
// built and exactly why this one is not built that way.
func TestTheProfileOverridesTheFleetBothWays(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fleet  bool
		agents map[string]bool
		want   bool
	}{
		{"fleet on, profile silent", true, nil, true},
		{"fleet off, profile silent", false, nil, false},
		{"fleet on, profile off", true, map[string]bool{"claude": false}, false},
		{"fleet off, profile on", false, map[string]bool{"claude": true}, true},
		{"fleet off, another profile on", false, map[string]bool{"codex": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := scoreStore(t)
			s, _, _ := scoreServer(st)
			WithScoreFeedback(tc.fleet, tc.agents)(s)

			got := strings.Contains(s.dispatchBrief("p1", "fix the login flow").Score, scoreHintLine)
			if got != tc.want {
				t.Fatalf("hint delivered = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestAStoreThatIsOffHintsAtNothing is the case where the hint would be actively
// worse than silence. With the memory switched off, score.submit answers with a
// refusal, so a brief that told the agent to run it would be spending the agent's
// turn to teach it nothing the daemon could not have said by saying nothing.
func TestAStoreThatIsOffHintsAtNothing(t *testing.T) {
	s, _, _ := scoreServer(nil)
	WithScoreFeedback(true, nil)(s)

	if b := s.dispatchBrief("p1", "fix the login flow"); b.Score != "" {
		t.Fatalf("score section = %q, want nothing at all when the memory is off", b.Score)
	}
}

// TestTheHintReloads is the promise ScoreConfig makes about every key but dir and
// enabled. The hint is resolved per delivery from the profile the panel records,
// so a reload has nothing in flight to migrate — and a brief built before the
// reload keeps what it was built with, which is why this asserts on the delivery
// after it rather than on the one before.
func TestTheHintReloads(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	WithScoreFeedback(true, nil)(s)
	if !strings.Contains(s.dispatchBrief("p1", "before").Score, scoreHintLine) {
		t.Fatal("the hint was absent before the reload, so the reload is not what this tested")
	}

	s.Reload(Settings{QueueMax: -1, ScoreFeedback: false})

	if got := s.dispatchBrief("p1", "after").Score; strings.Contains(got, scoreHintLine) {
		t.Fatalf("score section = %q, want the reload to have taken the hint away", got)
	}
}

// TestTheHintNamesTheDoorEveryPanelHas is a claim about content, and it is load
// bearing rather than cosmetic. score_submit is served from a .mcp.json written
// into the conductor's workspace and nowhere else, so a hint naming the MCP tool
// would name a door an ordinary agent panel does not have; the CLI works from
// every panel, because panelEnv injects the socket and the panel id into all of
// them.
func TestTheHintNamesTheDoorEveryPanelHas(t *testing.T) {
	if !strings.Contains(scoreHintLine, "baton ctl score submit") {
		t.Errorf("hint = %q, want it to name the command every panel can actually run", scoreHintLine)
	}
	if strings.Contains(scoreHintLine, "score_submit") {
		t.Errorf("hint = %q, want it not to name the MCP tool, which only the conductor is given", scoreHintLine)
	}
	if strings.Contains(scoreHintLine, "\n") {
		t.Errorf("hint = %q, want one line — it is prepended to every delivered brief", scoreHintLine)
	}
}

// TestStatusReportsTheHintInForce is invariant I8 over the write half: a knob
// whose effect cannot be observed is one the operator cannot trust. Both keys
// reload, so what the file says is not always what the daemon is doing, and
// `score status` is the only place an operator can settle the difference without
// reading the log.
func TestStatusReportsTheHintInForce(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	WithScoreFeedback(false, map[string]bool{"claude": true})(s)

	var got struct {
		Feedback  bool            `json:"feedback"`
		Profiles  map[string]bool `json:"feedback_profiles"`
		Available bool            `json:"available"`
	}
	if err := json.Unmarshal(s.scoreStatus(), &got); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if got.Feedback {
		t.Error("status feedback = true; want the fleet-wide answer actually in force")
	}
	if !reflect.DeepEqual(got.Profiles, map[string]bool{"claude": true}) {
		t.Errorf("status feedback_profiles = %v; want the overrides the daemon is holding", got.Profiles)
	}
}

// TestStatusSaysFalseRatherThanNothing pins the one field in the payload that
// must not elide itself. Every other tuning number omits its zero because a zero
// is a value the clamp can never produce; here false is not an impossible value
// but the interesting one, and a reply that dropped it would go quiet at exactly
// the moment the operator asked whether the hint was off.
func TestStatusSaysFalseRatherThanNothing(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	WithScoreFeedback(false, nil)(s)

	if raw := string(s.scoreStatus()); !strings.Contains(raw, `"feedback":false`) {
		t.Errorf("status = %s, want it to say feedback is off rather than omit the field", raw)
	}
}

// TestStatusHandsOutACopyOfTheOverrides: the map status reports is read by every
// delivery and swapped whole by a reload, so the reply must never carry the live
// one. A caller that could edit it would be editing the running policy, which is
// the same defect as the race and reachable without one.
func TestStatusHandsOutACopyOfTheOverrides(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	live := map[string]bool{"claude": true}
	WithScoreFeedback(false, live)(s)

	_, got := s.feedbackInForce()
	got["claude"] = false
	got["codex"] = true
	if !reflect.DeepEqual(live, map[string]bool{"claude": true}) {
		t.Fatalf("the running overrides became %v; want the reply to have been handed a copy", live)
	}
}
