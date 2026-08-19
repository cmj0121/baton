package tui

import (
	"testing"
	"time"
)

// rebind puts a binding on a new sequence, the way the key map editor or a
// config would, so a case can exercise a landing before the shipped key map has
// one.
func rebind(m model, name, key string) model {
	m.binds = append([]binding(nil), m.keymap()...)
	for i := range m.binds {
		if m.binds[i].name == name {
			m.binds[i].key = key
			return m
		}
	}
	panic("no binding named " + name)
}

// expire delivers the timeout for the run currently open.
func expire(m model) model {
	next, _ := m.expirePending(m.pendingGen)
	return next.(model)
}

func TestParseKeyTimeout(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", defaultKeyTimeout},
		{"800ms", 800 * time.Millisecond},
		{"1.2s", 1200 * time.Millisecond},
		{"0", 0},                    // asked for explicitly: never expire
		{"0s", 0},                   // the same thing, spelled with a unit
		{"1ms", defaultKeyTimeout},  // below the floor
		{"1h", defaultKeyTimeout},   // above the ceiling
		{"soon", defaultKeyTimeout}, // unparseable
		{"-5s", defaultKeyTimeout},  // negative
	}
	for _, c := range cases {
		if got := parseKeyTimeout(c.in); got != c.want {
			t.Errorf("parseKeyTimeout(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The everyday path, in the shape the work-item family actually takes: g opens
// a landing, g g marks, g c groups what was marked.
func TestLandingOpensThenCompletes(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard, cursor: 1}
	m = rebind(m, "mark", "g g")
	m = rebind(m, "group", "g c")

	m = press(m, "g")
	if len(m.pending) != 1 || m.pending[0] != "g" {
		t.Fatalf("g should open a landing, pending = %v", m.pending)
	}
	if m.input != inputNone || len(m.marked) != 0 {
		t.Fatalf("a landing must not fire anything on its own: input %v, marked %v", m.input, m.marked)
	}

	m = press(m, "g")
	if len(m.pending) != 0 {
		t.Fatalf("a completed run should clear, pending = %v", m.pending)
	}
	if len(m.marked) != 1 {
		t.Fatalf("g g should have marked the selection, marked = %v", m.marked)
	}

	m = press(m, "g", "c")
	if m.input != inputGroupName {
		t.Fatalf("g c should have opened the group overlay, got %v", m.input)
	}
}

// A landing owns the keyboard while it is open: the k of "v k" completes the
// run rather than moving the cursor, which is what k means on its own.
func TestLandingBeatsNavigation(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard, cursor: 2}
	m = rebind(m, "keycast", "v k")

	m = press(m, "v")
	if len(m.pending) == 0 {
		t.Fatal("v should open a landing")
	}
	before := m.cursor
	m = press(m, "k")
	if m.cursor != before {
		t.Errorf("k completed a run and must not also move the cursor: %d -> %d", before, m.cursor)
	}
	if !m.keycast {
		t.Error("v k should have toggled the keycast readout")
	}
}

// A landing nobody finishes lapses instead of waiting to swallow an unrelated
// key minutes later.
func TestLandingExpires(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	m = rebind(m, "group", "g c")

	m = press(m, "g")
	if len(m.pending) == 0 {
		t.Fatal("g should open a landing")
	}
	m = expire(m)
	if len(m.pending) != 0 {
		t.Fatalf("the run should have lapsed, pending = %v", m.pending)
	}
	if m.input != inputNone {
		t.Errorf("a lapsed landing must not fire anything, got %v", m.input)
	}
}

// The leader is a landing like any other, and lapses the same way. It used to
// wait forever, which is how it came to eat the next keystroke.
func TestLeaderExpires(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}

	m = press(m, "ctrl+t")
	if !m.prefix {
		t.Fatal("ctrl+t should arm the leader")
	}
	m = expire(m)
	if m.prefix {
		t.Error("a hanging leader should lapse")
	}
}

// A tick belonging to a run that has already been resolved must not clear the
// run that replaced it.
func TestStaleTimeoutIgnored(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	m = rebind(m, "group", "g c")

	m = press(m, "g")
	stale := m.pendingGen
	m = press(m, "c") // resolves the run
	m = press(m, "g") // and a new one opens

	next, _ := m.expirePending(stale)
	m = next.(model)
	if len(m.pending) == 0 {
		t.Error("a stale tick cleared the run that came after it")
	}
}

// The one shape that consults the timeout: a binding that is also the start of
// a longer one fires when the longer one does not arrive.
func TestAmbiguousBindingFiresOnTimeout(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	m = rebind(m, "keycast", "v")
	m = rebind(m, "preview", "v p")

	m = press(m, "v")
	if len(m.pending) == 0 {
		t.Fatal("an ambiguous key must wait rather than fire at once")
	}
	if m.keycast {
		t.Fatal("it fired immediately, so the longer sequence could never be typed")
	}
	m = expire(m)
	if !m.keycast {
		t.Error("the timeout should have fired the shorter binding")
	}
	if m.preview {
		t.Error("the longer binding fired instead")
	}
}

func TestEscCancelsPending(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	m = rebind(m, "group", "g c")

	m = press(m, "g", "esc")
	if len(m.pending) != 0 {
		t.Fatalf("esc should close an open run, pending = %v", m.pending)
	}
	if m.status != "" {
		t.Errorf("cancelling deliberately is not an error: %q", m.status)
	}
}

// A key that cannot continue the run ends it, and says so — the run is gone and
// the user needs to know why nothing happened.
func TestDeadEndReportsAndClears(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	m = rebind(m, "group", "g c")

	m = press(m, "g", "!")
	if len(m.pending) != 0 {
		t.Fatalf("a dead end should clear the run, pending = %v", m.pending)
	}
	if m.status == "" {
		t.Error("a dead end should say so")
	}
}

// An unbound key on an idle dashboard is not a mistake worth a status line.
func TestUnboundKeyIsSilent(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	m = press(m, "!")
	if m.status != "" {
		t.Errorf("a stray key should be silent, got %q", m.status)
	}
}

// The status bar has to carry the run while it waits, otherwise a landing is
// only discoverable by reading the docs.
func TestPendingCapAndHint(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	m = rebind(m, "group", "g c")
	m = rebind(m, "add", "g a")

	if m.pendingCap() != "" || m.pendingHint(cmdBinding) != "" {
		t.Fatal("an idle cockpit should advertise no run")
	}

	m = press(m, "g")
	if cap := m.pendingCap(); cap == "" {
		t.Error("an open run should show in the status bar")
	}
	hint := m.pendingHint(cmdBinding)
	for _, want := range []string{"c", "a"} {
		if !contains(hint, want) {
			t.Errorf("the hint should name %q as a continuation, got %q", want, hint)
		}
	}

	m = press(m, "c")
	if m.pendingCap() != "" {
		t.Error("a resolved run should clear the badge")
	}
}

// The leader shows in the badge in its own right, so "C-t is down" is visible
// wherever it is pressed rather than on the dashboard alone.
func TestPendingCapCarriesTheLeader(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	m = press(m, "ctrl+t")
	if m.pendingCap() == "" {
		t.Error("an armed leader should show in the status bar")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
