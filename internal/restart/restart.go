// Package restart is the policy that decides whether a panel whose process died
// comes back, and how patiently it keeps trying.
//
// The vocabulary is deliberately systemd's, because people already know what
// those words mean and what they cost. Only two of its modes are offered:
//
//   - never — the panel stays exited until you re-run it yourself (the default)
//   - on-failure — an abnormal exit brings the panel back
//
// "always" is missing on purpose. For an agent panel "restart forever" is almost
// always wrong: an agent that finished its task is *supposed* to stop, and a mode
// that cannot tell that apart turns every completed run into an infinite loop.
// The case it would serve — a dropped ssh, a crashed CLI — is already on-failure's
// case, since both exit non-zero.
package restart

import (
	"strings"
	"time"
)

// Mode is when a dead panel comes back.
type Mode string

const (
	// Never leaves a dead panel dead. It is the default: baton has never restarted
	// anything on its own, and a policy that starts processes should be asked for
	// rather than inherited by everyone on upgrade.
	Never Mode = "never"

	// OnFailure brings a panel back when its process exited abnormally, and leaves
	// a clean exit alone.
	OnFailure Mode = "on-failure"
)

// The policy defaults, applied to any field the config leaves unset.
const (
	DefaultMax     = 5
	DefaultBackoff = 2 * time.Second
	DefaultHealthy = 30 * time.Second

	// MaxBackoff caps the exponential growth. Doubling is unbounded arithmetic on
	// a number that is meant to be a wait a human tolerates; without a ceiling a
	// generous base and a high Max reach hours, which reads as "gave up" while
	// still claiming to be trying.
	MaxBackoff = 5 * time.Minute
)

// Policy is one panel's restart behaviour. The zero value restarts nothing, so a
// fleet with no restart configured behaves exactly as it did before the policy
// existed.
type Policy struct {
	// Mode is when to restart. An empty Mode counts as unset, so it can be
	// layered (see Merge) and defaulted (see WithDefaults).
	Mode Mode

	// Max is how many consecutive failures to tolerate before giving up. Reaching
	// it settles the panel with the reason rather than looping quietly.
	Max int

	// Backoff is the base of the exponential wait between attempts.
	Backoff time.Duration

	// Healthy is how long a run must last to count as a good one. A run that
	// reaches it resets the failure counter, so a panel that has been up for a day
	// gets the full budget again rather than the tail of an old crash loop.
	Healthy time.Duration
}

// ParseMode maps a config value to a Mode, reporting ok=false for anything it
// does not offer — "always" included, which is refused rather than aliased so
// the config says what it means (see the package comment).
func ParseMode(s string) (Mode, bool) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case Never:
		return Never, true
	case OnFailure:
		return OnFailure, true
	default:
		return Never, false
	}
}

// IsZero reports whether the policy sets nothing at all.
func (p Policy) IsZero() bool { return p == Policy{} }

// Merge layers over on top of p — the per-profile policy over the fleet-wide one
// — so a profile restates only what it changes. A field left unset inherits.
func (p Policy) Merge(over Policy) Policy {
	if over.Mode != "" {
		p.Mode = over.Mode
	}
	if over.Max > 0 {
		p.Max = over.Max
	}
	if over.Backoff > 0 {
		p.Backoff = over.Backoff
	}
	if over.Healthy > 0 {
		p.Healthy = over.Healthy
	}
	return p
}

// WithDefaults fills every unset field with its built-in default. Mode is left
// alone: an unset Mode means "restart nothing", which is the intended default
// and not something to fill in.
func (p Policy) WithDefaults() Policy {
	if p.Mode == "" {
		p.Mode = Never
	}
	if p.Max <= 0 {
		p.Max = DefaultMax
	}
	if p.Backoff <= 0 {
		p.Backoff = DefaultBackoff
	}
	if p.Healthy <= 0 {
		p.Healthy = DefaultHealthy
	}
	return p
}

// Restarts reports whether an exit with this code should bring the panel back.
//
// Abnormal means "not a clean zero", which covers the two cases worth reviving —
// a dropped ssh (255) and a crashed CLI — and covers the signal case too, where
// the exit code is -1. Telling a signal you asked for apart from one you did not
// is the caller's job: the exit code cannot see the difference.
func (p Policy) Restarts(exitCode int) bool {
	return p.Mode == OnFailure && exitCode != 0
}

// Delay is how long to wait before the attempt that follows the given number of
// consecutive failures: the base doubled once per failure, capped at MaxBackoff.
// The first retry waits the base itself rather than nothing, so a process that
// dies instantly cannot spin.
func (p Policy) Delay(failures int) time.Duration {
	d := p.Backoff
	if d <= 0 {
		d = DefaultBackoff
	}
	for range failures {
		if d >= MaxBackoff {
			return MaxBackoff
		}
		d *= 2
	}
	return min(d, MaxBackoff)
}

// Fields renders the policy for the structured logs and the event bus, matching
// how the resource limits report themselves. A policy that restarts nothing
// renders as nil rather than a row of zeroes.
func (p Policy) Fields() map[string]any {
	if p.Mode == "" || p.Mode == Never {
		return nil
	}
	return map[string]any{
		"mode":    string(p.Mode),
		"max":     p.Max,
		"backoff": p.Backoff.String(),
		"healthy": p.Healthy.String(),
	}
}
