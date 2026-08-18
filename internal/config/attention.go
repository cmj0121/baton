package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/attn"
)

// AttentionConfig is the on-disk form of the quiet ladder — how long a panel's
// silence has to run before the fleet calls its turn done, and longer still
// before it calls the panel stuck — as Go duration strings, so the file reads
// the way a human writes it.
//
// It is inlined into both the fleet-wide panel block and each agent profile,
// which is what lets a profile that legitimately thinks for longer say
// `stuck-after: 30m` and nothing else:
//
//	panel:
//	  stuck-after: 10m
//	  agents:
//	    claude:
//	      stuck-after: 30m
type AttentionConfig struct {
	// DoneOnQuiet is whether a quiet agent climbs from idle to done. Unset
	// inherits (and ultimately defaults to on); false keeps a quiet agent idle,
	// which is the right setting when you are watching one agent rather than
	// handling exceptions across fifty.
	DoneOnQuiet *bool `yaml:"done-on-quiet,omitempty"`

	// DoneAfter is the wait before a quiet agent's turn reads as over, e.g.
	// "60s". Empty inherits; "0" switches the rung off.
	DoneAfter string `yaml:"done-after,omitempty"`

	// StuckAfter is the wait before the silence reads as a problem, e.g. "10m".
	// Empty inherits; "0" switches the rung off, which is what a fleet of shells
	// wants.
	StuckAfter string `yaml:"stuck-after,omitempty"`
}

// Policy is the parsed ladder, and an error naming anything the file got wrong.
// A bad value never silently becomes a working threshold: the field is left
// unset, so it inherits rather than quietly promoting panels into a state the
// user did not ask for — the same discipline RestartConfig.Policy applies, and
// for the same reason. Both fields are reported, so one typo does not hide the
// other.
//
// It reads the fleet-wide block; a profile's block goes through ProfilePolicy,
// which differs only in the key it names when something does not parse.
func (a AttentionConfig) Policy() (attn.Policy, error) {
	return a.policy("panel")
}

// ProfilePolicy is Policy for an agent profile's block, naming the profile in
// any error it reports. The two exist separately because the whole value of a
// parse error is that the user can find the line: "panel.stuck-after" and
// "panel.agents.claude.stuck-after" are different lines, and a message that
// names the wrong one sends them to a value that is perfectly fine.
func (a AttentionConfig) ProfilePolicy(name string) (attn.Policy, error) {
	return a.policy("panel.agents." + name)
}

// policy parses the block under the given config scope, which is only ever used
// to spell the keys in an error.
func (a AttentionConfig) policy(scope string) (attn.Policy, error) {
	p := attn.Policy{DoneOnQuiet: a.DoneOnQuiet}
	var err error
	if d, derr := parseThreshold(scope, "done-after", a.DoneAfter); derr != nil {
		err = errors.Join(err, derr)
	} else {
		p.DoneAfter = d
	}
	if d, derr := parseThreshold(scope, "stuck-after", a.StuckAfter); derr != nil {
		err = errors.Join(err, derr)
	} else {
		p.StuckAfter = d
	}
	return p, err
}

// parseThreshold reads one rung of the ladder. Empty is UNSET — it inherits the
// layer above, and ultimately the built-in default. An explicit zero is a
// different statement: it switches the rung off, and comes back as attn.Never so
// nothing downstream can mistake "the user turned this off" for "the user said
// nothing". A negative wait is refused rather than clamped, because a threshold
// that has already passed is not a threshold.
func parseThreshold(scope, key, s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s.%s: %w", scope, key, err)
	}
	switch {
	case d < 0:
		return 0, fmt.Errorf("%s.%s %q is negative", scope, key, s)
	case d == 0:
		return attn.Never, nil
	default:
		return d, nil
	}
}
