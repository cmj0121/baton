package limits

import (
	"strings"
	"testing"
)

// TestParseBytes covers the size grammar: binary and decimal units, the optional
// trailing B, fractions, and the two ways a field can ask for no cap.
func TestParseBytes(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		limited bool
		bad     bool
	}{
		{in: "4Gi", want: 4 << 30, limited: true},
		{in: "512Mi", want: 512 << 20, limited: true},
		{in: "4GiB", want: 4 << 30, limited: true},
		{in: "1.5Gi", want: 1610612736, limited: true},
		{in: "2G", want: 2e9, limited: true},
		{in: "1024", want: 1024, limited: true},
		{in: " 8Mi ", want: 8 << 20, limited: true},
		{in: "4gi", want: 4 << 30, limited: true},
		{in: ""},          // absent: inherit, not a cap
		{in: "unlimited"}, // explicit: no cap
		{in: "UNLIMITED"},
		{in: "0", bad: true},
		{in: "-1Gi", bad: true},
		{in: "lots", bad: true},
		{in: "4Xi", bad: true},
	}
	for _, tt := range tests {
		got, limited, err := ParseBytes(tt.in)
		switch {
		case tt.bad && err == nil:
			t.Errorf("ParseBytes(%q) should have failed, got %d", tt.in, got)
		case !tt.bad && err != nil:
			t.Errorf("ParseBytes(%q): %v", tt.in, err)
		case !tt.bad && (got != tt.want || limited != tt.limited):
			t.Errorf("ParseBytes(%q) = %d,%v; want %d,%v", tt.in, got, limited, tt.want, tt.limited)
		}
	}
}

// TestParseCPUsAndCount checks the two simpler quantities, including that an
// uncapped field never reads back as a zero allowance.
func TestParseCPUsAndCount(t *testing.T) {
	if cores, limited, err := ParseCPUs("1.5"); err != nil || !limited || cores != 1.5 {
		t.Fatalf(`ParseCPUs("1.5") = %v,%v,%v`, cores, limited, err)
	}
	if cores, limited, err := ParseCPUs("unlimited"); err != nil || limited || cores != 0 {
		t.Fatalf(`ParseCPUs("unlimited") = %v,%v,%v`, cores, limited, err)
	}
	for _, bad := range []string{"0", "-2", "two", "200%"} {
		if _, _, err := ParseCPUs(bad); err == nil {
			t.Errorf("ParseCPUs(%q) should have failed", bad)
		}
	}
	if n, limited, err := ParseCount("512"); err != nil || !limited || n != 512 {
		t.Fatalf(`ParseCount("512") = %v,%v,%v`, n, limited, err)
	}
	for _, bad := range []string{"0", "-1", "1.5", "many"} {
		if _, _, err := ParseCount(bad); err == nil {
			t.Errorf("ParseCount(%q) should have failed", bad)
		}
	}
}

// TestLimitsValidate confirms Validate names the offending field, so the message
// can be shown to the user as-is.
func TestLimitsValidate(t *testing.T) {
	ok := Limits{CPUs: "2", Memory: "4Gi", MemoryHigh: "3Gi", Pids: "512", NOFile: "4096"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a well-formed Limits should validate: %v", err)
	}
	if err := (Limits{}).Validate(); err != nil {
		t.Fatalf("the zero Limits should validate (it caps nothing): %v", err)
	}
	err := Limits{Memory: "4 gigs"}.Validate()
	if err == nil {
		t.Fatal("an unreadable size should fail validation")
	}
	if got := err.Error(); !strings.HasPrefix(got, "memory:") {
		t.Fatalf("the error should name the field, got %q", got)
	}
}

// TestLimitsIsZero guards the shortcut callers use to skip an empty policy.
func TestLimitsIsZero(t *testing.T) {
	if !(Limits{}).IsZero() {
		t.Fatal("the zero Limits should report IsZero")
	}
	if (Limits{Pids: "512"}).IsZero() {
		t.Fatal("a set field should not report IsZero")
	}
}

// TestLimitsMerge is the layering rule: a field set on the profile wins, one left
// unset inherits the fleet-wide value, and "unlimited" is what lifts an inherited
// cap — an empty field cannot, because empty already means inherit.
func TestLimitsMerge(t *testing.T) {
	global := Limits{CPUs: "2", Memory: "4Gi", Pids: "512"}

	got := global.Merge(Limits{Memory: "16Gi", NOFile: "8192"})
	want := Limits{CPUs: "2", Memory: "16Gi", Pids: "512", NOFile: "8192"}
	if got != want {
		t.Fatalf("Merge = %+v, want %+v", got, want)
	}
	if got := global.Merge(Limits{}); got != global {
		t.Fatalf("merging an empty profile should change nothing, got %+v", got)
	}
	if got := global.Merge(Limits{Pids: Unlimited}).Pids; got != Unlimited {
		t.Fatalf("a profile should be able to lift an inherited cap, got %q", got)
	}
	if global != (Limits{CPUs: "2", Memory: "4Gi", Pids: "512"}) {
		t.Fatalf("Merge mutated its receiver: %+v", global)
	}
}

// TestLimitsFields checks the map the event bus and the logs carry: set caps
// only, and nil when nothing is capped.
func TestLimitsFields(t *testing.T) {
	got := Limits{CPUs: "2", NOFile: "4096"}.Fields()
	want := map[string]any{"cpus": "2", "nofile": "4096"}
	if len(got) != len(want) {
		t.Fatalf("Fields() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Fields()[%q] = %v, want %v", k, got[k], v)
		}
	}
	if (Limits{}).Fields() != nil {
		t.Error("a policy that caps nothing should render as nil")
	}
}
