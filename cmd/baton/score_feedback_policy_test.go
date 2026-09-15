package main

import (
	"reflect"
	"testing"

	"github.com/cmj0121/baton/internal/config"
)

// TestFeedbackPolicyCarriesAProfileThatSaidNo is the one way this differs from
// logPolicy, and the difference is the whole reason it is a second function.
// logPolicy carries only the profiles that said yes, because `log` has no
// fleet-wide key to disagree with, so there absent and false are the same
// instruction. Here they are not: a profile absent from the table inherits
// `score.feedback`, so dropping its false would hand it back the very answer it
// was written to refuse.
func TestFeedbackPolicyCarriesAProfileThatSaidNo(t *testing.T) {
	no, yes := false, true
	cfg := config.Config{}
	cfg.Panel.Agents = map[string]config.AgentProfile{
		"chatty": {Command: "claude", ScoreFeedback: &yes},
		"quiet":  {Command: "claude", ScoreFeedback: &no},
		"silent": {Command: "claude"},
	}

	on, agents := feedbackPolicy(cfg)
	if !on {
		t.Error("fleet-wide feedback = false for a config that never named the key; want the default on")
	}
	want := map[string]bool{"chatty": true, "quiet": false}
	if !reflect.DeepEqual(agents, want) {
		t.Errorf("overrides = %v; want %v -- a profile that said no belongs in the table, one that said nothing does not", agents, want)
	}
}

// TestFeedbackPolicyLeavesTheSilentProfilesOut is the half that keeps the pair
// hot-reloadable. A profile that never mentioned the key must be absent rather
// than stored with whatever the fleet-wide value happened to be at load, or
// flipping `score.feedback` on a reload would move nothing: every profile would
// be carrying a frozen copy of the old answer.
func TestFeedbackPolicyLeavesTheSilentProfilesOut(t *testing.T) {
	off := false
	cfg := config.Config{Score: config.ScoreConfig{Feedback: &off}}
	cfg.Panel.Agents = map[string]config.AgentProfile{"silent": {Command: "claude"}}

	on, agents := feedbackPolicy(cfg)
	if on {
		t.Error("fleet-wide feedback = true; want score.feedback: false to have reached the daemon")
	}
	if agents != nil {
		t.Errorf("overrides = %v; want nil -- no profile named the key, so none of them overrides anything", agents)
	}
}
