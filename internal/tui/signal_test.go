package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestSignalFromDashboard opens the picker on the selected panel and sends the
// chosen signal to it.
func TestSignalFromDashboard(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = sampleFleet()
	m.cursor = 0
	want := m.dashItems()[0]

	// Bare s opens the picker aimed at the selection.
	m = press(m, "s")
	if m.mode != modeSignal {
		t.Fatalf("s should open the signal picker, got mode=%v", m.mode)
	}
	if len(m.signalTargets) != 1 || m.signalTargets[0] != want.ids()[0] {
		t.Fatalf("picker should target the selected panel %v, got %v", want.ids(), m.signalTargets)
	}

	// c sends SIGINT and closes the picker.
	m = press(m, "c")
	if m.mode != modeDashboard {
		t.Fatalf("sending a signal should return to the dashboard, got mode=%v", m.mode)
	}
	sig := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.signal" })
	if sig.Signal != "SIGINT" || len(sig.IDs) != 1 || sig.IDs[0] != want.ids()[0] {
		t.Fatalf("expected SIGINT to %v, got %+v", want.ids(), sig)
	}
}

// TestSignalFromGroup checks the split's two scopes: bare s signals the focused
// member, S signals every member.
func TestSignalFromGroup(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = groupedFleet() // api = panels 1,3,6, all live
	m = m.zoomGroup(m.dashItems()[0])
	if m.mode != modeGroupZoom {
		t.Fatalf("expected the split, got mode=%v", m.mode)
	}
	focus, _ := m.focusedMember()

	// s → the focused member only.
	next, _ := m.handleGroupZoomKey(key("s"))
	ms := next.(model)
	if len(ms.signalTargets) != 1 || ms.signalTargets[0] != focus.ID {
		t.Fatalf("s should target the focused member %s, got %v", focus.ID, ms.signalTargets)
	}
	if ms.signalFrom != modeGroupZoom {
		t.Fatalf("the picker should remember the split as its origin, got %v", ms.signalFrom)
	}

	// S → every member of the group.
	next, _ = m.handleGroupZoomKey(key("S"))
	mall := next.(model)
	if len(mall.signalTargets) != 3 {
		t.Fatalf("S should target every member, got %v", mall.signalTargets)
	}

	mall = press(mall, "k") // SIGKILL the whole group
	sig := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.signal" })
	if sig.Signal != "SIGKILL" || len(sig.IDs) != 3 {
		t.Fatalf("expected SIGKILL to all 3 members, got %+v", sig)
	}
}

// TestSignalFromZoom checks C-t s opens the picker for the zoomed panel.
func TestSignalFromZoom(t *testing.T) {
	m := baseModel()
	m.mode = modeZoom
	m.zoomID = "7"
	m.zoomTitle = "claude · api"

	next, _ := m.handleZoomKey(key(m.effPrefix())) // arm the prefix
	m = next.(model)
	next, _ = m.handleZoomKey(key("s"))
	m = next.(model)
	if m.mode != modeSignal {
		t.Fatalf("C-t s should open the picker from a zoom, got mode=%v", m.mode)
	}
	if len(m.signalTargets) != 1 || m.signalTargets[0] != "7" {
		t.Fatalf("the picker should target the zoomed panel, got %v", m.signalTargets)
	}
}

// TestSignalSkipsExited checks an exited selection has nothing to signal — the
// picker never opens.
func TestSignalSkipsExited(t *testing.T) {
	m := baseModel()
	m.fleet = []panel.Panel{{ID: "1", Title: "dead", State: panel.Exited}}
	m.cursor = 0
	m = press(m, "s")
	if m.mode == modeSignal {
		t.Fatal("an exited panel should not open the signal picker")
	}
	if m.status != "no live panel to signal" {
		t.Fatalf("expected a no-live-panel status, got %q", m.status)
	}
}

// TestSignalOtherEntry checks the other… row prompts for a free-form signal and
// sends a valid one while rejecting nonsense.
func TestSignalOtherEntry(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = sampleFleet()
	m.cursor = 0
	m = press(m, "s")

	// o opens the free-form field.
	m = press(m, "o")
	if m.input != inputSignalName {
		t.Fatalf("o should open the other-signal field, got input=%v", m.input)
	}

	// A bogus token is rejected and keeps the field open.
	m.inputBuf = "nope"
	next, _ := m.commitInput()
	m = next.(model)
	if m.input != inputSignalName {
		t.Fatal("an unknown signal should keep the field open")
	}

	// A valid name is sent.
	m.inputBuf = "WINCH"
	next, _ = m.commitInput()
	m = next.(model)
	sig := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.signal" })
	if sig.Signal != "WINCH" {
		t.Fatalf("expected the typed signal WINCH, got %+v", sig)
	}
}

// TestSignalPickerCancels checks esc closes the picker without sending anything.
func TestSignalPickerCancels(t *testing.T) {
	m := baseModel()
	m.fleet = sampleFleet()
	m = press(m, "s")
	if m.mode != modeSignal {
		t.Fatalf("s should open the picker, got mode=%v", m.mode)
	}
	m = press(m, "esc")
	if m.mode != modeDashboard {
		t.Fatalf("esc should cancel back to the dashboard, got mode=%v", m.mode)
	}
}
