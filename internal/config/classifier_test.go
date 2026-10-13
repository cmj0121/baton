package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestClassifierResolveAbsentIsOff pins the switch: no block in the file is
// (nil, false, nil), not a client config on defaults.
func TestClassifierResolveAbsentIsOff(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("panel:\n  workdir: ~\n"), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c, record, err := cfg.Panel.Classifier.Resolve()
	if c != nil || record || err != nil {
		t.Fatalf("absent block: got (%+v, %v, %v), want (nil, false, nil)", c, record, err)
	}
}

// TestClassifierResolveDefaults covers an empty block: every key filled from
// the built-ins, the socket expanded, recording off.
func TestClassifierResolveDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var cfg Config
	if err := yaml.Unmarshal([]byte("panel:\n  classifier: {}\n"), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c, record, err := cfg.Panel.Classifier.Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("an empty block should switch the classifier on")
	}
	if record {
		t.Error("record should default to off")
	}
	if want := filepath.Join(home, ".mold", "mold.sock"); c.Socket != want {
		t.Errorf("socket = %q, want the default expanded to %q", c.Socket, want)
	}
	if c.Scope != DefaultClassifierScope || c.MinConfidence != DefaultClassifierMinConfidence || c.Timeout != DefaultClassifierTimeout {
		t.Errorf("defaults not filled: %+v", c)
	}
}

// TestClassifierResolveKeys covers each key as written, including the record
// flag surfacing and a "~" socket going through the same expansion as every
// other hand-written path.
func TestClassifierResolveKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := `
panel:
  classifier:
    socket: ~/run/m.sock
    scope: other-scope
    min-confidence: 0.8
    timeout: 500ms
    record: true
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c, record, err := cfg.Panel.Classifier.Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !record {
		t.Error("record: true should surface")
	}
	if want := filepath.Join(home, "run", "m.sock"); c.Socket != want {
		t.Errorf("socket = %q, want %q", c.Socket, want)
	}
	if c.Scope != "other-scope" || c.MinConfidence != 0.8 || c.Timeout != 500*time.Millisecond {
		t.Errorf("keys not taken as written: %+v", c)
	}
}

// TestClassifierResolveBadKeys covers the contract that differs from the
// attention ladder: a bad value is reported by key and keeps its DEFAULT, and
// the block stays on. Both keys are reported, so one typo does not hide the
// other.
func TestClassifierResolveBadKeys(t *testing.T) {
	cases := []struct {
		name       string
		cfg        ClassifierConfig
		wantErrHas string
	}{
		{"min-confidence above one", ClassifierConfig{MinConfidence: "1.5"}, "panel.classifier.min-confidence"},
		{"min-confidence negative", ClassifierConfig{MinConfidence: "-0.1"}, "panel.classifier.min-confidence"},
		{"min-confidence not a number", ClassifierConfig{MinConfidence: "high"}, "panel.classifier.min-confidence"},
		{"timeout not a duration", ClassifierConfig{Timeout: "soon"}, "panel.classifier.timeout"},
		{"timeout zero", ClassifierConfig{Timeout: "0"}, "panel.classifier.timeout"},
		{"timeout negative", ClassifierConfig{Timeout: "-2s"}, "panel.classifier.timeout"},
	}
	for _, tc := range cases {
		c, _, err := tc.cfg.Resolve()
		if err == nil || !strings.Contains(err.Error(), tc.wantErrHas) {
			t.Errorf("%s: error = %v, want one naming %q", tc.name, err, tc.wantErrHas)
		}
		if c == nil {
			t.Errorf("%s: the block should stay on", tc.name)
			continue
		}
		if c.MinConfidence != DefaultClassifierMinConfidence || c.Timeout != DefaultClassifierTimeout {
			t.Errorf("%s: a bad key should keep its default, got conf=%v timeout=%v", tc.name, c.MinConfidence, c.Timeout)
		}
	}

	_, _, err := (&ClassifierConfig{MinConfidence: "x", Timeout: "y"}).Resolve()
	if err == nil || !strings.Contains(err.Error(), "min-confidence") || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("both bad keys should be reported, got %v", err)
	}
}
