package task

import "testing"

// TestTerminal checks which statuses are end states.
func TestTerminal(t *testing.T) {
	for st, want := range map[Status]bool{
		Queued:     false,
		Dispatched: false,
		Running:    false,
		Done:       true,
		Failed:     true,
	} {
		if got := st.Terminal(); got != want {
			t.Errorf("%q.Terminal() = %v, want %v", st, got, want)
		}
	}
}

// TestCanAdvance covers the transition table: the lifecycle moves forward only,
// any non-terminal status can fail, and a terminal status is sticky.
func TestCanAdvance(t *testing.T) {
	cases := []struct {
		from, to Status
		want     bool
	}{
		{Queued, Dispatched, true},
		{Queued, Running, true},
		{Queued, Failed, true},
		{Dispatched, Running, true},
		{Dispatched, Done, false}, // must run before done
		{Running, Done, true},
		{Running, Failed, true},
		{Running, Dispatched, false}, // no going back
		{Done, Running, false},       // terminal is sticky
		{Failed, Queued, false},
		{Queued, Queued, false}, // not a forward move
	}
	for _, c := range cases {
		if got := CanAdvance(c.from, c.to); got != c.want {
			t.Errorf("CanAdvance(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}
