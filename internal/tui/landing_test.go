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

// A key the landing pass freed answers with its new home for one release. A key
// that did something yesterday and does nothing at all today reads as a broken
// cockpit, not as a rebind.
func TestFreedKeysPointAtTheirNewHome(t *testing.T) {
	cases := map[string]string{
		"C": seqLabel(keyConductor),
		"a": seqLabel(keyAdd),
		"U": seqLabel(keyUsage),
		"V": seqLabel(keyDashLayout),
		"z": seqLabel(keyLens),
	}
	for old, want := range cases {
		m := press(model{fleet: sampleFleet(), mode: modeDashboard}, old)
		if !strings.Contains(m.status, want) {
			t.Errorf("%q should point at %q, got %q", old, want, m.status)
		}
	}

	// Restart is an escape now, so its hint has to carry the leader — otherwise
	// it names a key that still does nothing.
	m := press(model{fleet: sampleFleet(), mode: modeDashboard}, keyRestart)
	if !strings.Contains(m.status, "C-t "+seqLabel(keyRestart)) {
		t.Errorf("the restart hint should name the leader, got %q", m.status)
	}
}

// The hint must never fire for a key that actually works, so a user who binds
// something onto a freed letter is not told it moved.
func TestRebindingAFreedKeySilencesItsHint(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	m = rebind(m, "preview", "z")

	m = press(m, "z")
	if !m.preview {
		t.Fatal("a rebound freed key should run its binding")
	}
	if strings.Contains(m.status, "moved") {
		t.Errorf("a key that works must not claim to have moved, got %q", m.status)
	}
}

// The editor collects a run, not a key, so a two-key binding can be typed in
// the cockpit rather than only in the config file.
func TestKeyMapBindsARun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := model{mode: modeKeyMap, fleet: sampleFleet(), binds: append([]binding(nil), bindings...), cursor: 1}

	m = press(m, "e", "n", "z")
	if !m.editing {
		t.Fatal("the capture should stay open while a run is being typed")
	}
	if !strings.Contains(m.status, "n z") {
		t.Errorf("the status should echo the run so far, got %q", m.status)
	}
	m = press(m, "enter")
	if m.editing {
		t.Fatal("enter should end the capture")
	}
	if got := m.binds[0].key; got != "n z" {
		t.Fatalf("expected the run bound, got %q", got)
	}
	if m = press(m, "n", "z"); m.input != inputNewPanelCmd && m.status == "" {
		t.Error("the run should reach its action once bound")
	}
}

// Binding a run whose start is already a binding is allowed — vim's d and dd
// relate that way — but it costs the shorter one the timeout, and that is not
// something to discover by feel a week later.
func TestKeyMapWarnsOnAnAmbiguousRebind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := model{mode: modeKeyMap, fleet: sampleFleet(), binds: append([]binding(nil), bindings...), cursor: 1}

	// new-panel is p; bind close to "p x" so p becomes the start of a longer run.
	for i, b := range m.binds {
		if b.name == "close" {
			m.cursor = i + 1
		}
	}
	m = press(m, "e", "p", "x", "enter")
	if !strings.Contains(m.status, "waits") {
		t.Errorf("an ambiguous rebind should say what it costs, got %q", m.status)
	}
}

// An unambiguous rebind says what it did and nothing more.
func TestKeyMapQuietOnAPlainRebind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := model{mode: modeKeyMap, fleet: sampleFleet(), binds: append([]binding(nil), bindings...), cursor: 1}

	m = press(m, "e", "y", "enter")
	if strings.Contains(m.status, "waits") {
		t.Errorf("a plain rebind should not warn, got %q", m.status)
	}
	if !strings.Contains(m.status, "rebound") {
		t.Errorf("a rebind should report itself, got %q", m.status)
	}
}
