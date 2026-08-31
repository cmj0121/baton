package config

import (
	"github.com/cmj0121/baton/internal/paths"
)

// ScoreConfig configures the fleet-scope memory (issue #37): where the three
// score files live and whether the subsystem runs at all.
type ScoreConfig struct {
	// Dir is the directory holding score.md, score.json, and score-events.jsonl.
	// Empty means $HOME/.baton. Resolved through paths.Expand, so "~" works.
	Dir string `yaml:"dir,omitempty"`

	// PromoteAt is how many times an entry must be said — its first submission
	// plus every repeat folded into it since — before recurrence raises it from
	// tier 1 ("noted") to tier 2 ("note and take care"). Unset, and anything
	// below two, is passed to the store as it stands and lands on its default of
	// three; see score.defaultPromoteAt for why that is the number, and why the
	// number lives there rather than here. Two is the floor because a tier is earned: at
	// one an entry would arrive already promoted.
	//
	// Tier 3 is not on this knob. It needs a signal that came from the user
	// rather than from an agent's claim, so no threshold can reach it (#38's
	// invariant I6).
	//
	// Hyphenated, like every other key in this config: it is the only YAML name
	// the file had ever spelled with an underscore. It is in no doc, no example
	// and no release, so the two spellings never overlapped and no shim is owed.
	// The JSON side of score.status stays promote_at, which is the proto
	// convention.
	PromoteAt int `yaml:"promote-at,omitempty"`

	// StalePromoteAt says the file spelled the threshold the OLD way, and exists
	// for one reason: so the daemon can say the key is being ignored. The YAML
	// decoder is not strict, so a config still saying `promote_at:` parses
	// cleanly, PromoteAt stays zero, and the store runs on a threshold nobody
	// chose — the same silence as a boot threshold that never reached the store,
	// arriving through a different door. Nothing shipped under the underscore, so
	// no shim is owed and the value is deliberately not read; a retune the
	// operator did not ask for is worth less than their learning the key is
	// wrong.
	//
	// It is set by Load from the file's own bytes, never by a key of its own: it
	// is a fact about a parse, so it must not ride into the config a Save rewrites
	// or the one the daemon broadcasts.
	StalePromoteAt bool `yaml:"-" json:"-"`

	// Enabled turns the subsystem off entirely when false: no injection into
	// briefs, submissions refused with a plain reason, files left untouched.
	// It is a pointer for the reason every Settings toggle is: unset means
	// "use the default" (on), which an explicit false must stay distinguishable
	// from across a rewrite of the file.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled reports whether the score subsystem runs. Unset defaults to on —
// the memory is the feature, and a fresh config should not need a line to get
// it — so only an explicit `enabled: false` switches it off.
func (c ScoreConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// Directory is the resolved directory the score files live in. A configured
// value goes through paths.Expand so "~" and relative paths land where the
// user meant; empty falls back to $HOME/.baton — expanded through the same
// helper, so the daemon and the cockpit resolve home identically (see
// paths.Expand for why that matters).
func (c ScoreConfig) Directory() string {
	if dir := paths.Expand(c.Dir); dir != "" {
		return dir
	}
	return paths.Expand("~/.baton")
}
