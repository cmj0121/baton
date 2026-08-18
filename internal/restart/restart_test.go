package restart

import (
	"testing"
	"time"
)

// TestParseMode: the two modes baton offers round-trip, and everything else is
// refused — "always" loudly among them, since silently aliasing it to on-failure
// would make the config say something it does not mean.
func TestParseMode(t *testing.T) {
	cases := map[string]struct {
		want Mode
		ok   bool
	}{
		"never":       {Never, true},
		"on-failure":  {OnFailure, true},
		" ON-FAILURE": {OnFailure, true},
		"always":      {Never, false},
		"":            {Never, false},
		"nonsense":    {Never, false},
	}
	for in, c := range cases {
		got, ok := ParseMode(in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseMode(%q) = %q/%v, want %q/%v", in, got, ok, c.want, c.ok)
		}
	}
}

// TestZeroPolicyRestartsNothing: a fleet with no restart configured must behave
// exactly as it did before the policy existed.
func TestZeroPolicyRestartsNothing(t *testing.T) {
	var p Policy
	if !p.IsZero() {
		t.Fatal("the zero policy should report itself as unset")
	}
	for _, code := range []int{0, 1, -1, 255} {
		if p.Restarts(code) {
			t.Errorf("the zero policy restarted on exit %d", code)
		}
	}
	if p.Fields() != nil {
		t.Errorf("a policy that restarts nothing should render as nil, got %v", p.Fields())
	}
}

// TestRestartsOnAbnormalExitOnly: a clean exit is a finished job, not a failure.
// The signal case (-1) counts as abnormal — telling a signal you asked for apart
// from one you did not is the caller's job, not the exit code's.
func TestRestartsOnAbnormalExitOnly(t *testing.T) {
	p := Policy{Mode: OnFailure}
	cases := map[int]bool{0: false, 1: true, 3: true, 255: true, -1: true}
	for code, want := range cases {
		if got := p.Restarts(code); got != want {
			t.Errorf("on-failure Restarts(%d) = %v, want %v", code, got, want)
		}
	}
	never := Policy{Mode: Never}
	for code := range cases {
		if never.Restarts(code) {
			t.Errorf("never restarted on exit %d", code)
		}
	}
}

// TestMergeLayersProfileOverFleet: a profile restates only what it changes.
func TestMergeLayersProfileOverFleet(t *testing.T) {
	fleet := Policy{Mode: OnFailure, Max: 5, Backoff: 2 * time.Second, Healthy: 30 * time.Second}

	if got := fleet.Merge(Policy{Mode: Never}); got.Mode != Never || got.Max != 5 || got.Backoff != 2*time.Second {
		t.Errorf("a mode-only override should keep the rest: %+v", got)
	}
	if got := fleet.Merge(Policy{}); got != fleet {
		t.Errorf("an empty override should change nothing: %+v", got)
	}
	over := Policy{Mode: OnFailure, Max: 2, Backoff: time.Second, Healthy: time.Minute}
	if got := fleet.Merge(over); got != over {
		t.Errorf("a full override should win outright: %+v", got)
	}
}

// TestWithDefaults: every field but the mode gets a built-in default. The mode
// stays never, because a policy that starts processes should be asked for rather
// than inherited on upgrade.
func TestWithDefaults(t *testing.T) {
	got := Policy{}.WithDefaults()
	want := Policy{Mode: Never, Max: DefaultMax, Backoff: DefaultBackoff, Healthy: DefaultHealthy}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	set := Policy{Mode: OnFailure, Max: 1, Backoff: time.Second, Healthy: time.Second}
	if got := set.WithDefaults(); got != set {
		t.Errorf("defaults overwrote a configured policy: %+v", got)
	}
}

// TestDelayGrowsAndIsCapped: the wait doubles per consecutive failure, starts at
// the base rather than at nothing (so an instantly-dying process cannot spin),
// and stops growing at MaxBackoff.
func TestDelayGrowsAndIsCapped(t *testing.T) {
	p := Policy{Backoff: 2 * time.Second}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	for i, w := range want {
		if got := p.Delay(i); got != w {
			t.Errorf("Delay(%d) = %v, want %v", i, got, w)
		}
	}
	if got := p.Delay(30); got != MaxBackoff {
		t.Errorf("Delay(30) = %v, want the %v cap", got, MaxBackoff)
	}
	if got := (Policy{}).Delay(0); got != DefaultBackoff {
		t.Errorf("an unset backoff should fall back to %v, got %v", DefaultBackoff, got)
	}
	if got := (Policy{Backoff: 10 * time.Minute}).Delay(0); got != MaxBackoff {
		t.Errorf("a base past the cap should still be capped, got %v", got)
	}
}

// TestFields: the log/event rendering carries the whole policy when one is in
// force, so "why did that come back" is answerable from the logs alone.
func TestFields(t *testing.T) {
	f := Policy{Mode: OnFailure, Max: 3, Backoff: time.Second, Healthy: time.Minute}.Fields()
	if f["mode"] != "on-failure" || f["max"] != 3 || f["backoff"] != "1s" || f["healthy"] != "1m0s" {
		t.Fatalf("fields = %v", f)
	}
	if (Policy{Mode: Never, Max: 3}).Fields() != nil {
		t.Error("a never policy should render as nil")
	}
}
