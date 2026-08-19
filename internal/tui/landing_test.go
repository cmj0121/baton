package tui

import (
	"strings"
	"testing"
)

// The shipped key map, driven the way a hand drives it. keypending_test.go
// proves the machinery on a made-up map; this file proves the map itself — that
// the landings really are landings, that the keys they freed are free, and that
// the families behind them still reach their actions.

// A landing does nothing on its own, in the map as shipped.
func TestShippedLandingsAreInert(t *testing.T) {
	for _, land := range []string{"n", "v", "g", "x"} {
		m := press(model{fleet: sampleFleet(), mode: modeDashboard, cursor: 1}, land)
		if len(m.pending) != 1 || m.pending[0] != land {
			t.Errorf("%q should open a run and do nothing else, pending = %v", land, m.pending)
		}
		if m.input != inputNone || m.status != "" {
			t.Errorf("%q acted on its own: input %v, status %q", land, m.input, m.status)
		}
	}
}

// The work-item family: g g marks, g c creates, g u dissolves.
func TestWorkItemFamily(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard, cursor: 1}

	m = press(m, "g", "g")
	if len(m.marked) != 1 {
		t.Fatalf("g g should mark the selection, marked = %v", m.marked)
	}
	m = press(m, "g", "c")
	if m.input != inputGroupName {
		t.Fatalf("g c should open the group-name overlay, got %v", m.input)
	}
}

// The view family draws and never touches the fleet, so each of its keys is
// safe to assert by its own toggle.
func TestViewFamily(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}

	if m = press(m, "v", "k"); !m.keycast {
		t.Error("v k should turn the keycast readout on")
	}
	if m = press(m, "v", "p"); !m.preview {
		t.Error("v p should turn the detail pane on")
	}
	before := m.usageMode
	if m = press(m, "v", "u"); m.usageMode == before {
		t.Error("v u should cycle the usage footer")
	}
}

// Purging the fleet's dead is a double tap, and the first tap alone must not do
// it — that is the whole point of spelling it x x.
func TestPurgeNeedsBothTaps(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}

	m = press(m, "x")
	if m.status != "" {
		t.Errorf("one x should be silent, got %q", m.status)
	}
	m = press(m, "x")
	if m.status == "" {
		t.Error("x x should have acted on the exited panels")
	}
}

// The letters the landings freed are no longer bound to anything, which is what
// let the escapes under the leader stop shadowing them.
func TestFreedKeysAreUnbound(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	for _, k := range []string{"c", ".", "C", "H", "G", "a", "u", "U", "K", "V", "z"} {
		if b, ok := m.lookupCmd(k); ok {
			t.Errorf("%q should be free, still bound to %q", k, b.name)
		}
	}
}

// C-t a and C-t c were unreachable from a zoom while `a` and `c` were commands,
// because an escape shadows a command under the leader. Nothing starts with
// either letter now.
func TestEscapesNoLongerShadowCommands(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	for _, b := range m.keymap() {
		if !isEscape(b.act) {
			continue
		}
		esc := b.seq()
		if len(esc) != 1 {
			t.Errorf("escape %q is %q — escapes are reached on one key after the leader", b.name, b.key)
			continue
		}
		for _, c := range m.keymap() {
			if isEscape(c.act) {
				continue
			}
			if c.seq()[0] == esc[0] {
				t.Errorf("escape %q (%s) shadows command %q (%s) under the leader", b.name, b.key, c.name, c.key)
			}
		}
	}
}

// Restart ends the fleet, and bare S signals a whole work item in the split, so
// the two must not be the same keystroke in different views.
func TestRestartIsPrefixOnly(t *testing.T) {
	m := model{mode: modeDashboard, fleet: sampleFleet()}

	if m := press(m, keyRestart); m.pendingRestart {
		t.Error("bare S must not arm a force-restart")
	}
	if m := press(m, "ctrl+t", keyRestart); !m.pendingRestart {
		t.Error("C-t S should arm the force-restart confirmation")
	}
}

// The key map and its help surfaces have to print a sequence as the run you
// press, not as one impossible key.
func TestSequencesRenderAsSeparateCaps(t *testing.T) {
	cases := []struct {
		prefix, key string
		want        []string
	}{
		{"", keyNewPanel, []string{"p"}},
		{"", keyMark, []string{"g", "g"}},
		{"", keyGroup, []string{"g", "c"}},
		{"", keyNewForm, []string{"n", "c"}},
		{"C-t", keyDashboard, []string{"C-t", "d"}},
		{"", keyExpand, []string{"space"}},
	}
	for _, c := range cases {
		got := strings.Fields(stripANSI(keycaps(c.prefix, c.key, false)))
		if len(got) != len(c.want) {
			t.Errorf("keycaps(%q, %q) rendered %v, want %v", c.prefix, c.key, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("keycaps(%q, %q) rendered %v, want %v", c.prefix, c.key, got, c.want)
				break
			}
		}
	}
}
