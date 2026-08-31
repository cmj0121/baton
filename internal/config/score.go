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
