package tui

import (
	"strings"
	"testing"
)

// zoomPress drives a run of keys at a zoom: the leader, then the binding's own
// tokens, exactly as a hand would.
func zoomPress(m model, keys ...string) model {
	for _, k := range keys {
		for _, t := range strings.Fields(normSeq(k)) {
			next, _ := m.handleZoomKey(key(t))
			m = next.(model)
		}
	}
	return m
}

// A zoom reaches a binding by the leader plus that binding's own sequence — the
// key you learned on the dashboard, with C-t in front of it. Landings included.
func TestZoomReachesALanding(t *testing.T) {
	m, _ := zoomedAgent(t)

	m = zoomPress(m, "ctrl+t", "v")
	if len(m.pending) == 0 {
		t.Fatal("C-t v should open the view landing inside a zoom")
	}
	if m.keycast {
		t.Fatal("a landing must not fire anything on its own")
	}

	m = zoomPress(m, "k")
	if !m.keycast {
		t.Error("C-t v k should have toggled the keycast readout")
	}
	if len(m.pending) != 0 {
		t.Errorf("a completed run should clear, pending = %v", m.pending)
	}
}

// A landing left open inside a zoom must not swallow the program's keys forever;
// it lapses like any other.
func TestZoomLandingExpires(t *testing.T) {
	m, _ := zoomedAgent(t)

	m = zoomPress(m, "ctrl+t", "v")
	if len(m.pending) == 0 {
		t.Fatal("C-t v should open a run")
	}
	m = expire(m)
	if len(m.pending) != 0 || m.zoomArmed {
		t.Errorf("the run should have lapsed, pending = %v armed = %v", m.pending, m.zoomArmed)
	}
}

// The escapes are prefix-reached in every view, and a zoom is a view. These
// three were documented as working here and did nothing at all, because the
// zoom's switch enumerated the escapes by hand and had drifted from isEscape.
func TestZoomReachesEveryEscape(t *testing.T) {
	cases := []struct {
		name string
		act  action
		want mode
	}{
		{"proc tree", actProcTree, modeProcTree},
		{"panel config", actPanelConfig, modePanelConfig},
		{"key map", actEditMap, modeKeyMap},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := zoomedAgent(t)
			m = zoomPress(m, "ctrl+t", m.bindingKey(c.act))
			if m.mode != c.want {
				t.Fatalf("C-t %s should open %v from a zoom, got %v", m.bindingKey(c.act), c.want, m.mode)
			}
		})
	}
}

// Panel config opened from a zoom returns to that zoom, not to the dashboard:
// looking up the default shell should not cost you the panel you were reading.
func TestZoomPanelConfigComesBack(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = zoomPress(m, "ctrl+t", m.bindingKey(actPanelConfig))
	if m.mode != modePanelConfig {
		t.Fatalf("C-t P should open panel config, got %v", m.mode)
	}
	m = press(m, "esc")
	if m.mode != modeZoom {
		t.Errorf("esc should return to the zoom that opened it, got %v", m.mode)
	}
}

// A command whose target is a dashboard row has nothing to act on in a zoom, and
// says so rather than acting on a cursor nobody can see.
func TestZoomRefusesASelectionVerb(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = zoomPress(m, "ctrl+t", keyMark)
	if m.status == "" || !strings.Contains(m.status, "not available") {
		t.Errorf("a selection verb should be refused with a way forward, got %q", m.status)
	}
}

// The leader alone still sends a literal prefix to the program, which is the one
// thing a zoom must never lose.
func TestZoomLiteralPrefixSurvives(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = zoomPress(m, "ctrl+t", "ctrl+t")
	if m.zoomArmed || len(m.pending) != 0 {
		t.Errorf("C-t C-t should be consumed as a literal, armed = %v pending = %v", m.zoomArmed, m.pending)
	}
}

// A leader left hanging inside a zoom lapses. It used to schedule the expiry and
// then ignore it, which left zoomArmed true for good — and a stuck leader in a
// zoom routes the program's next keystroke to baton, so a stray q typed at vim
// would detach the cockpit.
func TestZoomHangingLeaderExpires(t *testing.T) {
	m, _ := zoomedAgent(t)

	m = zoomPress(m, "ctrl+t")
	if !m.zoomArmed {
		t.Fatal("ctrl+t should arm the leader in a zoom")
	}
	m = expire(m)
	if m.zoomArmed {
		t.Fatal("a hanging zoom leader should lapse")
	}

	// And the next key belongs to the program again, not to baton.
	m = zoomPress(m, m.bindingKey(actDetach))
	if m.quitting {
		t.Error("after the leader lapsed, a bare detach key must reach the program")
	}
}

// The same, in the split, where the leader also used to hang forever.
func TestSplitHangingLeaderExpires(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m = m.zoomGroup(m.dashItems()[0])

	next, _ := m.handleGroupZoomKey(key("ctrl+t"))
	m = next.(model)
	if !m.groupArmed {
		t.Fatal("ctrl+t should arm the leader in the split")
	}
	if m = expire(m); m.groupArmed {
		t.Error("a hanging split leader should lapse")
	}
}
