package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/classifier"
	"github.com/cmj0121/baton/internal/paths"
)

// The built-in classifier settings, applied key by key wherever the block
// leaves one unset or gets one wrong.
const (
	DefaultClassifierSocket        = "~/.mold/mold.sock"
	DefaultClassifierScope         = "baton-attention"
	DefaultClassifierMinConfidence = 0.5
	DefaultClassifierTimeout       = 2 * time.Second
)

// ClassifierConfig is the on-disk form of the optional MOLD classifier that
// reads the same tail window as the attention regex and may raise a quiet panel
// the regex missed. The block's PRESENCE is the switch: a fleet without it runs
// exactly as before, and an empty one runs on every default below.
//
// It is a nested block rather than inlined into AttentionConfig because a
// classifier is fleet-wide and nothing else: one bundle answers one question
// for every profile, and a nested block cannot be restated per profile by
// accident:
//
//	panel:
//	  classifier:
//	    socket: ~/.mold/mold.sock
//	    scope: baton-attention
//	    min-confidence: 0.5
//	    timeout: 2s
//	    record: false
type ClassifierConfig struct {
	// Socket is the unix socket `mold serve` listens on. Empty is the default
	// path under the user's home; a leading "~" is expanded.
	Socket string `yaml:"socket,omitempty"`

	// Scope is the mold scope that holds the one `attention` key. Empty is the
	// default; an answer from any other scope is not an answer.
	Scope string `yaml:"scope,omitempty"`

	// MinConfidence is the score an `attention` answer must reach to count,
	// 0–1 as a string so the file reads the way a human writes it. Empty is the
	// default.
	MinConfidence string `yaml:"min-confidence,omitempty"`

	// Timeout is the longest one ask may take, e.g. "2s". Empty is the default.
	Timeout string `yaml:"timeout,omitempty"`

	// Record is whether the fleet writes the attention row files a bundle is
	// trained from. Unset is off: writing scrollback to disk is a decision of
	// its own, separate from reading answers off a socket.
	Record *bool `yaml:"record,omitempty"`
}

// Resolve is the parsed block: the client config with every default filled, the
// record flag, and an error naming anything the file got wrong. A nil receiver —
// no block in the file — is the classifier switched off: (nil, false, nil).
//
// A bad `min-confidence` or `timeout` keeps its default and the block stays ON,
// which is the opposite of what AttentionConfig.Policy does with a bad rung and
// deliberately so: a threshold left unset there inherits a sane layer above,
// while a classifier the user wrote a block for and then lost to one typo would
// silently fall back to the regex alone. Both keys are reported, so one typo
// does not hide the other.
func (c *ClassifierConfig) Resolve() (*classifier.Config, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	cfg := &classifier.Config{
		Socket:        DefaultClassifierSocket,
		Scope:         DefaultClassifierScope,
		MinConfidence: DefaultClassifierMinConfidence,
		Timeout:       DefaultClassifierTimeout,
	}
	if s := strings.TrimSpace(c.Socket); s != "" {
		cfg.Socket = s
	}
	cfg.Socket = paths.Expand(cfg.Socket)
	if s := strings.TrimSpace(c.Scope); s != "" {
		cfg.Scope = s
	}
	var err error
	if f, ferr := parseConfidence(c.MinConfidence); ferr != nil {
		err = errors.Join(err, ferr)
	} else if f >= 0 {
		cfg.MinConfidence = f
	}
	if d, derr := parseTimeout(c.Timeout); derr != nil {
		err = errors.Join(err, derr)
	} else if d > 0 {
		cfg.Timeout = d
	}
	record := c.Record != nil && *c.Record
	return cfg, record, err
}

// parseConfidence reads `min-confidence`. Empty is UNSET and comes back as -1
// so the caller keeps the default; anything outside 0–1 is refused rather than
// clamped, because a gate at 1.5 never opens and one at -1 is always open, and
// neither is what the user meant.
func parseConfidence(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("panel.classifier.min-confidence: %w", err)
	}
	if f < 0 || f > 1 {
		return 0, fmt.Errorf("panel.classifier.min-confidence %q is outside 0–1", s)
	}
	return f, nil
}

// parseTimeout reads `timeout`. Empty is UNSET and comes back as zero so the
// caller keeps the default; zero or negative is refused, because an ask that
// may take no time at all is an ask that never answers.
func parseTimeout(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("panel.classifier.timeout: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("panel.classifier.timeout %q is not above zero", s)
	}
	return d, nil
}
