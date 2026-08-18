// Package attn is baton's attention-threshold domain: how long a panel has to
// stay quiet before the fleet says its turn is over, and longer still before it
// says something has probably gone wrong.
//
// It is deliberately a neutral package rather than part of the config file
// format or of the Monitor, for the same reason internal/limits is: the YAML
// layer reads thresholds into it and the Monitor reads them out, and neither
// should have to depend on the other to speak about the same numbers.
//
// The right values are a property of the AGENT, not of baton. A shell script
// that prints a line a second is wedged after thirty; a model planning a
// refactor is legitimately silent for ten minutes, and one running a full test
// suite for thirty. baton cannot tell those apart from the byte stream, and
// guessing wrong is expensive in both directions — too low and every thinking
// agent cries wolf until the queue is ignored, too high and a genuinely wedged
// agent sits unnoticed for the afternoon. That asymmetry is why a Policy layers:
// the fleet-wide value is the floor and an agent profile restates only the one
// line it changes.
package attn

import "time"

// The built-in thresholds, applied to any rung the config leaves unset.
const (
	// DefaultDoneAfter is six times the Monitor's idle threshold. Idle is
	// calibrated to "output stopped", which an agent hits routinely between tool
	// calls, so done has to be long enough that a between-tool-calls pause never
	// reads as a finished turn — and short enough that a finished turn reaches the
	// human while they still remember dispatching it.
	DefaultDoneAfter = time.Minute

	// DefaultStuckAfter is the point past which silence stops being patience.
	DefaultStuckAfter = 10 * time.Minute
)

// Never is the explicit "this rung does not apply" value a threshold may carry.
// It is spelled as a distinct constant because the zero value already means
// something else — "inherit the layer above" — and one number cannot say both.
// A config that writes `stuck-after: 0` means never, which is the right setting
// for a fleet of shells; a config that omits the key means "whatever the layer
// above decided", which is not the same statement.
const Never time.Duration = -1

// Policy is one panel's quiet ladder. The zero value configures nothing, so a
// fleet with no attention block behaves on the built-in defaults.
type Policy struct {
	// DoneOnQuiet is whether a quiet agent climbs from idle to done at all. nil
	// is unset (it inherits, and ultimately defaults to on); an explicit false
	// removes the middle rung, so a quiet agent stays idle exactly as it did
	// before this ladder existed.
	//
	// It defaults to ON because the fleet case is the one the ladder exists for:
	// at the size where a human is an exception handler rather than an operator,
	// "quiet agent" and "quiet shell" are genuinely different events, and done is
	// what makes the queue clearable at all. Someone watching a single agent is
	// already looking at it, and for them done is a second badge for a state they
	// can see — which is what the knob is for.
	DoneOnQuiet *bool

	// DoneAfter is how long an agent must be quiet before its turn reads as over,
	// and StuckAfter how long before the silence reads as a problem. Both are on
	// the QUIET clock (time since the last byte of output), never on how long a
	// state has held, which is what lets a panel that already settled to done keep
	// climbing to stuck rather than resting there forever.
	//
	// Zero is unset and inherits; Never switches the rung off.
	DoneAfter  time.Duration
	StuckAfter time.Duration
}

// IsZero reports whether the policy configures nothing at all, so a caller can
// skip the whole layer rather than reason about three unset fields.
func (p Policy) IsZero() bool { return p == Policy{} }

// Merge layers over on top of p — the per-profile policy over the fleet-wide one
// — so a profile restates only what it changes. A field left unset inherits.
// Never counts as set, since switching a rung off is a decision, not a silence.
func (p Policy) Merge(over Policy) Policy {
	if over.DoneOnQuiet != nil {
		p.DoneOnQuiet = over.DoneOnQuiet
	}
	if over.DoneAfter != 0 {
		p.DoneAfter = over.DoneAfter
	}
	if over.StuckAfter != 0 {
		p.StuckAfter = over.StuckAfter
	}
	return p
}

// DoneQuiet reports whether a quiet agent may climb to done, defaulting to yes.
func (p Policy) DoneQuiet() bool { return p.DoneOnQuiet == nil || *p.DoneOnQuiet }

// Done is the resolved wait before a quiet agent's turn reads as over, with the
// built-in default filled in. It returns 0 for "never", so every caller can ask
// the one question that matters — is this rung armed — as `d > 0`.
func (p Policy) Done() time.Duration { return resolve(p.DoneAfter, DefaultDoneAfter) }

// Stuck is the resolved wait before silence reads as a problem, on the same
// terms as Done: the default filled in, and 0 meaning the rung is off.
func (p Policy) Stuck() time.Duration { return resolve(p.StuckAfter, DefaultStuckAfter) }

// Ordered reports whether the resolved ladder actually climbs — stuck strictly
// after done. A ladder whose rungs are out of order is a config error rather
// than a preference: silently reordering it would make the two states mean
// something the user did not write, so the caller disables the higher rung and
// says so. A rung that is off cannot be out of order.
func (p Policy) Ordered() bool {
	stuck := p.Stuck()
	return stuck == 0 || stuck > p.Done()
}

// resolve turns a configured threshold into the wait a caller should apply:
// unset takes the built-in default, Never (and anything else negative, which
// only Never can be) reads as no wait at all.
func resolve(set, def time.Duration) time.Duration {
	switch {
	case set == 0:
		return def
	case set < 0:
		return 0
	default:
		return set
	}
}
