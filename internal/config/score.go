package config

import (
	"github.com/cmj0121/baton/internal/paths"
)

// ScoreConfig configures the fleet-scope memory (issue #37): where the three
// score files live, whether the subsystem runs at all, how many times an
// observation must recur before it earns a tier, how many entries one brief
// carries, and what each ranking dimension is worth.
//
// Everything but Dir and Enabled reloads on SIGHUP — they are numbers the live
// store compares rather than state to swap — and everything but Dir and Enabled
// is clamped by the store rather than here; see score.Policy.clamp for the
// defaults and the floors.
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
	// The top tier is not on this knob. Recurrence alone stops one rung below
	// it, because it takes a signal that came from the user rather than from an
	// agent's claim (#38's invariant I6); UserSignalsAt is how many of those it
	// takes.
	//
	// Hyphenated, like every other key in this config: it is the only YAML name
	// the file had ever spelled with an underscore. It is in no doc, no example
	// and no release, so the two spellings never overlapped and no shim is owed.
	// The JSON side of score.status stays promote_at, which is the proto
	// convention.
	PromoteAt int `yaml:"promote-at,omitempty"`

	// UserSignalsAt is how many times the USER must reinforce an entry before it
	// may climb to tier 3 ("important"). Unset, and anything below two, lands on
	// the store's default of two; see score.defaultUserSignalsAt for why that is
	// the number and why it is the smallest one that can be right.
	//
	// What counts is the user SAYING THE THING AGAIN: typing a duplicate line
	// into score.md, submitting a repeat from their own shell, or dispatching a
	// brief that matches an entry. Correcting a line's wording is not one of them
	// — an edit is one statement re-spelled rather than a second statement — so a
	// run of typo fixes cannot walk an entry to the top tier.
	//
	// It is a SEPARATE knob from PromoteAt rather than a reuse of it because the
	// two count different things: PromoteAt counts occurrences from any source,
	// and this counts only the user's. An entry still has to climb the ordinary
	// ladder to get there — the user signal lifts the ceiling, it does not skip
	// a rung.
	UserSignalsAt int `yaml:"user-signals-at,omitempty"`

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

	// BadNumbers names the score keys whose value is present and is not a number
	// — `working-set: lots`, `rank: {recency: fast}` — and exists for the reason
	// StalePromoteAt does: so the daemon can say which key to fix.
	//
	// It matters more than the stale key does. A mistyped number fails the strict
	// parse, which takes the WHOLE file down: Load returns an error, the daemon
	// keeps its running score policy on a reload and boots on the package
	// defaults, and the one line it logs names neither the section nor the key.
	// The operator's next move is to reload again and watch nothing change. See
	// config.badNumbers, which finds them without the parse that failed.
	//
	// Set by Load from the file's own bytes, never by a key of its own: it is a
	// fact about a parse, so it must not ride into the config a Save rewrites or
	// the one the daemon broadcasts.
	BadNumbers []string `yaml:"-" json:"-"`

	// Rank is what each ranking dimension is worth when it matches. Unset weights
	// land on the store's default; see RankConfig and score.clampWeight.
	Rank RankConfig `yaml:"rank,omitempty"`

	// WorkingSet is how many entries one brief carries — the highest-ranked few,
	// not the first few. Unset, and anything below one, lands on the store's
	// default of seven; see score.clampWorkingSet for why "fewer than one" is
	// read as unset rather than as switching the memory off, which is what
	// score.enabled is for.
	//
	// It caps the BRIEF, never the store: `baton ctl score list` reports every
	// entry the store holds whatever this says, and marks which of them the
	// working set took (#42).
	WorkingSet int `yaml:"working-set,omitempty"`

	// Enabled turns the subsystem off entirely when false: no injection into
	// briefs, submissions refused with a plain reason, files left untouched.
	// It is a pointer for the reason every Settings toggle is: unset means
	// "use the default" (on), which an explicit false must stay distinguishable
	// from across a rewrite of the file.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// RankConfig weights the ranking's four dimensions (#42). An entry's rank is
//
//	tier x recency x cwd x profile x group
//
// so these are MULTIPLIERS, and the rule an operator has to remember is a
// single one: 1.0 means the dimension does not matter. A weight below one would
// penalise a match rather than reward it, which is not a semantics anyone
// wants, so the store raises anything below one to the floor rather than
// honouring it; unset (zero) means the default of 2.0, not "off". See
// score.clampWeight, which is where both rules live.
//
// The tier itself is deliberately NOT here. It is earned by recurrence (#37)
// and never granted by config, so a fleet cannot be told to ignore what it has
// learned is important.
type RankConfig struct {
	// Recency is what the most recently touched entry is worth. It is the one
	// dimension that is not a match: every entry's factor slides linearly between
	// 1.0 at the oldest position in the event log and this weight at the newest.
	//
	// TOUCHED rather than reinforced, which matters if you are tuning this to
	// promote what the fleet is actually working on. An entry's position moves on
	// its submission, on every reinforcement, and on an operator editing its line
	// — and an edit counts no reinforcement, so a line you reworded ranks as
	// fresh without having earned anything. See score.Store.lastAt.
	//
	// A POSITION, not a time. Nothing in the ranking reads a clock (invariant
	// I5), so a laptop that slept for a week or an NTP correction cannot reorder
	// what the fleet is being told.
	Recency float64 `yaml:"recency,omitempty"`

	// Cwd, Profile and Group are each worth their weight when the entry was
	// submitted from a panel whose working directory, agent profile, or fleet
	// group matches the one being dispatched to, and 1.0 otherwise. They are
	// independent, so an entry matching all three is worth their product.
	//
	// An entry with no recorded value for a dimension never matches, and neither
	// does a dispatch with none — "unknown" is not a value that agrees with
	// itself. Entries the operator submitted from their own cockpit record none
	// of the three, so they rank on tier and recency alone.
	Cwd     float64 `yaml:"cwd,omitempty"`
	Profile float64 `yaml:"profile,omitempty"`
	Group   float64 `yaml:"group,omitempty"`
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
