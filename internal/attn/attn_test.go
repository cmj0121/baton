package attn

import (
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

// TestPolicyDefaults checks the resolution rules a zero policy relies on: an
// unset rung takes its built-in default, Never reads as "no wait", and the
// done-on-quiet toggle defaults to on.
func TestPolicyDefaults(t *testing.T) {
	var p Policy
	if !p.IsZero() {
		t.Fatal("the zero policy should configure nothing")
	}
	if !p.DoneQuiet() {
		t.Error("done-on-quiet should default to on")
	}
	if got := p.Done(); got != DefaultDoneAfter {
		t.Errorf("Done() = %v, want %v", got, DefaultDoneAfter)
	}
	if got := p.Stuck(); got != DefaultStuckAfter {
		t.Errorf("Stuck() = %v, want %v", got, DefaultStuckAfter)
	}

	off := Policy{DoneOnQuiet: boolPtr(false), DoneAfter: Never, StuckAfter: Never}
	if off.DoneQuiet() {
		t.Error("an explicit false should remove the done rung")
	}
	if off.Done() != 0 || off.Stuck() != 0 {
		t.Errorf("Never should resolve to no wait, got %v/%v", off.Done(), off.Stuck())
	}
}

// TestPolicyMerge covers the four layering cases a per-agent profile can be in:
// neither side set, only the fleet, only the profile, and both — plus Never,
// which counts as set because switching a rung off is a decision.
func TestPolicyMerge(t *testing.T) {
	fleet := Policy{DoneOnQuiet: boolPtr(true), DoneAfter: 60 * time.Second, StuckAfter: 10 * time.Minute}

	cases := []struct {
		name                string
		base, over          Policy
		wantQuiet           bool
		wantDone, wantStuck time.Duration
	}{
		{"neither set takes the built-ins", Policy{}, Policy{}, true, DefaultDoneAfter, DefaultStuckAfter},
		{"fleet only", fleet, Policy{}, true, 60 * time.Second, 10 * time.Minute},
		{"profile only", Policy{}, Policy{StuckAfter: 30 * time.Minute}, true, DefaultDoneAfter, 30 * time.Minute},
		{"profile wins over fleet", fleet, Policy{StuckAfter: 30 * time.Minute}, true, 60 * time.Second, 30 * time.Minute},
		{"profile switches a rung off", fleet, Policy{StuckAfter: Never}, true, 60 * time.Second, 0},
		{"profile silences the done rung", fleet, Policy{DoneOnQuiet: boolPtr(false)}, false, 60 * time.Second, 10 * time.Minute},
	}
	for _, tc := range cases {
		got := tc.base.Merge(tc.over)
		if got.DoneQuiet() != tc.wantQuiet || got.Done() != tc.wantDone || got.Stuck() != tc.wantStuck {
			t.Errorf("%s: quiet=%v done=%v stuck=%v, want %v/%v/%v",
				tc.name, got.DoneQuiet(), got.Done(), got.Stuck(), tc.wantQuiet, tc.wantDone, tc.wantStuck)
		}
	}
}

// TestPolicyOrdered checks the ladder sanity rule: stuck must sit strictly above
// done, unless it is switched off entirely.
func TestPolicyOrdered(t *testing.T) {
	cases := []struct {
		name string
		p    Policy
		want bool
	}{
		{"defaults climb", Policy{}, true},
		{"stuck below done", Policy{DoneAfter: 10 * time.Minute, StuckAfter: time.Minute}, false},
		{"stuck equal to done", Policy{DoneAfter: time.Minute, StuckAfter: time.Minute}, false},
		{"stuck off is never out of order", Policy{DoneAfter: time.Hour, StuckAfter: Never}, true},
		{"done off leaves stuck free", Policy{DoneAfter: Never, StuckAfter: time.Second}, true},
	}
	for _, tc := range cases {
		if got := tc.p.Ordered(); got != tc.want {
			t.Errorf("%s: Ordered() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
