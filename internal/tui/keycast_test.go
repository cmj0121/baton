package tui

import (
	"strings"
	"testing"
	"time"
)

// keycastModel is a command-mode cockpit with the readout on, at a width wide
// enough that the footer never has to drop the segment.
func keycastModel() model {
	return model{
		mode:      modeDashboard,
		width:     160,
		height:    40,
		keycast:   true,
		prefixKey: keyPrefix,
		binds:     append([]binding(nil), bindings...),
		now:       time.Unix(1_700_000_000, 0),
	}
}

func TestKeycastNamesTheAction(t *testing.T) {
	m := keycastModel().noteKey("p")
	if m.keycastKey != "p" || m.keycastAct != "new panel" {
		t.Fatalf("expected p/new panel, got %q/%q", m.keycastKey, m.keycastAct)
	}
	if m.keycastAt.IsZero() {
		t.Fatal("the press should be stamped so it can age out")
	}
}

func TestKeycastNamesNavigationKeys(t *testing.T) {
	// hjkl are not rebindable commands, so they get their own names rather than
	// showing up as a bare letter the viewer has to decode.
	for key, want := range map[string]string{"l": "right", "h": "left", "enter": "open"} {
		if got := keycastModel().noteKey(key); got.keycastAct != want {
			t.Fatalf("key %q: expected %q, got %q", key, want, got.keycastAct)
		}
	}
}

func TestKeycastShowsTheLeaderChord(t *testing.T) {
	// The leader alone waits for its second half...
	m := keycastModel().noteKey(keyPrefix)
	if m.keycastKey != "C-t" || m.keycastAct != "…" {
		t.Fatalf("expected the pending leader, got %q/%q", m.keycastKey, m.keycastAct)
	}

	// ...and once armed, the next key completes the chord and names it.
	m.prefix = true
	m = m.noteKey("d")
	if m.keycastKey != "C-t d" || m.keycastAct != "dashboard" {
		t.Fatalf("expected C-t d/dashboard, got %q/%q", m.keycastKey, m.keycastAct)
	}
}

func TestKeycastIgnoresKeysThatBelongToTheProgram(t *testing.T) {
	// A zoom, a split tile and a text field all feed the program being driven —
	// echoing those keys to the footer would make it a keylogger.
	cases := []struct {
		name string
		m    model
	}{
		{"zoom", func() model { m := keycastModel(); m.mode = modeZoom; return m }()},
		{"group zoom", func() model { m := keycastModel(); m.mode = modeGroupZoom; return m }()},
		{"text field", func() model { m := keycastModel(); m.input = inputRename; return m }()},
	}
	for _, c := range cases {
		if got := c.m.noteKey("s"); got.keycastKey != "" {
			t.Fatalf("%s: expected the key to be dropped, got %q", c.name, got.keycastKey)
		}
	}
}

func TestKeycastKeepsTheLeaderInsideAZoom(t *testing.T) {
	// The leader is baton's key wherever it is pressed, so it — and whatever
	// completes it — still shows while the program owns everything else.
	m := keycastModel()
	m.mode = modeZoom
	if got := m.noteKey(keyPrefix); got.keycastKey != "C-t" {
		t.Fatalf("expected the leader to survive a zoom, got %q", got.keycastKey)
	}

	m.zoomArmed = true
	if got := m.noteKey("d"); got.keycastKey != "C-t d" {
		t.Fatalf("expected the chord to complete in a zoom, got %q", got.keycastKey)
	}
}

func TestKeycastOffRecordsNothing(t *testing.T) {
	m := keycastModel()
	m.keycast = false
	if got := m.noteKey("G"); got.keycastKey != "" {
		t.Fatalf("expected nothing recorded while off, got %q", got.keycastKey)
	}
	if seg := m.keycastSeg(); seg != "" {
		t.Fatalf("expected an empty segment while off, got %q", seg)
	}
}

func TestKeycastAgesOut(t *testing.T) {
	m := keycastModel().noteKey("G")

	m.now = m.keycastAt.Add(keycastFor - time.Millisecond)
	if m = m.ageKeycast(); m.keycastKey == "" {
		t.Fatal("the key should still be on the bar just before it expires")
	}

	m.now = m.keycastAt.Add(keycastFor + time.Millisecond)
	if m = m.ageKeycast(); m.keycastKey != "" || m.keycastAct != "" {
		t.Fatalf("expected the readout cleared, got %q/%q", m.keycastKey, m.keycastAct)
	}
}

func TestKeycastSegmentCarriesKeyAndAction(t *testing.T) {
	seg := keycastModel().noteKey("p").keycastSeg()
	if !strings.Contains(seg, "p") || !strings.Contains(seg, "new panel") {
		t.Fatalf("expected the key and its action in the segment, got %q", seg)
	}
}

func TestKeycastRidesInTheFooter(t *testing.T) {
	m := keycastModel().noteKey("p")
	if bar := m.statusBar(seg("DASHBOARD", colInk, colBlue), m.helpHint()); !strings.Contains(bar, "new panel") {
		t.Fatalf("expected the readout in the status bar, got %q", bar)
	}

	// A bar with no room drops the readout rather than the help hint.
	m.width = 40
	bar := m.statusBar(seg("DASHBOARD", colInk, colBlue), m.helpHint())
	if strings.Contains(bar, "group") {
		t.Fatalf("expected the readout dropped on a narrow bar, got %q", bar)
	}
}

func TestKeycastToggleIsPersisted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	m := keycastModel()
	m.keycast = false
	next, _ := m.runAction(actKeycastToggle)
	m = next.(model)
	if !m.keycast {
		t.Fatal("expected the toggle to turn the readout on")
	}

	// Toggling off must not leave the last key stranded on the bar.
	m = m.noteKey("G")
	next, _ = m.runAction(actKeycastToggle)
	m = next.(model)
	if m.keycast || m.keycastKey != "" {
		t.Fatalf("expected off and cleared, got on=%v key=%q", m.keycast, m.keycastKey)
	}
}

func TestActionLabelReadsAsWords(t *testing.T) {
	if got := actionLabel(binding{name: "global-shell"}); got != "global shell" {
		t.Fatalf("expected %q, got %q", "global shell", got)
	}
}
