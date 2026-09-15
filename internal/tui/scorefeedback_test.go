package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
)

func boolPtr(b bool) *bool { return &b }

// TestFeedbackProfilesAreSorted is not tidiness. The cursor indexes rows, and
// map iteration order is not stable between two range statements in the same
// process — so an unsorted list would let the frame that DREW the page and the
// keystroke that edited it disagree about which profile row 0 is, and the toggle
// would land on a profile the user was not looking at.
func TestFeedbackProfilesAreSorted(t *testing.T) {
	m := model{agents: map[string]config.AgentProfile{
		"zed": {Command: "zed"}, "alpha": {Command: "a"}, "mid": {Command: "m"},
	}}
	want := []string{"alpha", "mid", "zed"}
	for i := 0; i < 8; i++ { // repeated, because one pass over a map can be sorted by luck
		if got := m.feedbackProfiles(); !reflect.DeepEqual(got, want) {
			t.Fatalf("feedbackProfiles() = %v; want %v", got, want)
		}
	}
}

// TestFeedbackCyclesThroughInherit pins the three states. A two-way toggle would
// let a profile out of "inherit" and never back in, so the first press on any
// profile would pin it to whatever the fleet said at that moment — and a later
// change to score.feedback would then appear to do nothing to it.
func TestFeedbackCyclesThroughInherit(t *testing.T) {
	var cur *bool
	for _, want := range []*bool{boolPtr(true), boolPtr(false), nil, boolPtr(true)} {
		cur = nextFeedback(cur)
		switch {
		case want == nil && cur != nil:
			t.Fatalf("cycle reached %v; want inherit", *cur)
		case want != nil && cur == nil:
			t.Fatalf("cycle reached inherit; want %v", *want)
		case want != nil && cur != nil && *want != *cur:
			t.Fatalf("cycle reached %v; want %v", *cur, *want)
		}
	}
}

// TestFeedbackLabelNamesWhatIsInherited: a profile that has not answered must not
// render as though it had. Flattening "inherit" into the value it currently
// resolves to would make the fleet-wide key look like it had been copied onto
// every profile.
func TestFeedbackLabelNamesWhatIsInherited(t *testing.T) {
	for _, tc := range []struct {
		name  string
		own   *bool
		fleet bool
		want  string
	}{
		{"unset under a fleet that says yes", nil, true, "inherit · on"},
		{"unset under a fleet that says no", nil, false, "inherit · off"},
		{"explicit yes against a fleet that says no", boolPtr(true), false, "on"},
		{"explicit no against a fleet that says yes", boolPtr(false), true, "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := feedbackLabel(tc.own, tc.fleet); got != tc.want {
				t.Errorf("feedbackLabel() = %q; want %q", got, tc.want)
			}
		})
	}
}

// TestCycleFeedbackPersistsWithoutAliasing covers both halves of the write. The
// value has to reach the file, or the row is a light switch wired to nothing —
// and it must not reach it by mutating the map in place, because m.agents is
// shared with the prefs the cockpit loaded and with every model value copied from
// this one.
func TestCycleFeedbackPersistsWithoutAliasing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	shared := map[string]config.AgentProfile{"claude": {Command: "claude"}}
	m := model{agents: shared, scoreFeedback: true}

	m = m.cycleFeedback(0)
	if strings.HasPrefix(m.status, "save failed") {
		t.Fatalf("status = %q, want the save to have succeeded", m.status)
	}
	if got := shared["claude"].ScoreFeedback; got != nil {
		t.Errorf("the shared profile map was mutated in place (ScoreFeedback = %v); want the model to have copied it", *got)
	}
	if got := m.agents["claude"].ScoreFeedback; got == nil || !*got {
		t.Fatalf("model profile = %v; want the first press to have set it on", got)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load the saved config: %v", err)
	}
	if got := cfg.Panel.Agents["claude"].ScoreFeedback; got == nil || !*got {
		t.Errorf("saved score-feedback = %v; want the toggle to have reached the file", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".baton", "config")); err != nil {
		t.Errorf("no config written: %v", err)
	}
}

// TestCycleFeedbackIgnoresARowThatIsNotThere: the rows are one per name in the
// user's file, so the index the cursor carries can outlive the profile it named —
// a reload that dropped a profile leaves the cursor past the end.
func TestCycleFeedbackIgnoresARowThatIsNotThere(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := model{agents: map[string]config.AgentProfile{"claude": {Command: "claude"}}}
	for _, i := range []int{-1, 1, 99} {
		if got := m.cycleFeedback(i); got.status != "" {
			t.Errorf("cycleFeedback(%d) status = %q; want the out-of-range row to do nothing", i, got.status)
		}
	}
}

// TestThePageGrowsWithTheProfiles is what makes the rows reachable at all. Every
// other row on this page is a fixed field, so itemCount was a constant; a cursor
// that still stopped at it would draw the section and never let anyone into it.
func TestThePageGrowsWithTheProfiles(t *testing.T) {
	m := model{mode: modePanelConfig}
	if got := m.itemCount(); got != numPanelConfigRows {
		t.Fatalf("itemCount with no profiles = %d; want %d", got, numPanelConfigRows)
	}
	m.agents = map[string]config.AgentProfile{"claude": {Command: "claude"}, "codex": {Command: "codex"}}
	if got := m.itemCount(); got != numPanelConfigRows+2 {
		t.Fatalf("itemCount with two profiles = %d; want %d", got, numPanelConfigRows+2)
	}
}

// TestThePageHintSaysWhatTheSwitchDoesNotDo. A row reading "off" invites exactly
// one wrong conclusion — that submission is blocked — and this is the only place
// the page can say otherwise.
func TestThePageHintSaysWhatTheSwitchDoesNotDo(t *testing.T) {
	hint := feedbackHintLine()
	if !strings.Contains(hint, "not the submitting") {
		t.Errorf("hint = %q; want it to say the switch stops the telling and not the submitting", hint)
	}
}
