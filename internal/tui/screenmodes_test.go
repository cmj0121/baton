package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The terminal modes the cockpit asks for used to be program options and
// commands — WithAltScreen at three call sites, and an enable/disable command
// fired at the moments the mouse toggle changed. They are fields on the frame
// now, which means the cockpit states them again on every render rather than
// remembering to send them. These are the guards on that: a frame that forgot to
// ask would have been invisible before, because nothing rendered checked.

// TestEveryFrameAsksForTheAltScreen: the cockpit owns the whole terminal in
// every mode, so no frame may quietly render inline and scroll the user's
// scrollback away.
func TestEveryFrameAsksForTheAltScreen(t *testing.T) {
	for _, st := range frameStates() {
		t.Run(st.name, func(t *testing.T) {
			if !st.build(t).View().AltScreen {
				t.Error("this frame would render inline instead of on the alt screen")
			}
		})
	}
}

// TestRemoteFormAsksForTheAltScreen: the connection form is its own program, run
// before there is a client, and it takes the screen the same way.
func TestRemoteFormAsksForTheAltScreen(t *testing.T) {
	if !NewRemoteForm("", "").View().AltScreen {
		t.Error("the remote form should take the alt screen")
	}
}

// TestMouseModeFollowsTheToggle: mouse reporting is carried by the frame, so the
// persisted toggle is honoured on the very first render — there is no separate
// moment to remember it at, which is what the old enable command was for.
func TestMouseModeFollowsTheToggle(t *testing.T) {
	on := baseModel()
	on.mouseEnabled = true
	if got := on.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("mouse on: MouseMode = %v, want cell motion", got)
	}

	off := baseModel()
	off.mouseEnabled = false
	if got := off.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("mouse off: MouseMode = %v, want none", got)
	}
}

// TestMouseToggleReachesTheNextFrame walks the toggle the way a person does —
// through the key map's settings row — and reads the mode off the frame that
// follows. The command that used to do this is gone; if the frame did not carry
// the mode, flipping the row would change a flag nothing acts on.
func TestMouseToggleReachesTheNextFrame(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // toggling a setting persists to $HOME/.baton/config

	m := baseModel()
	m.mode, m.mouseEnabled = modeKeyMap, false
	m.cursor = len(bindings) + 1 + int(settingMouse)

	next, _ := m.activate()
	if got := next.(model).View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("after switching the mouse on, MouseMode = %v, want cell motion", got)
	}

	back, _ := next.(model).activate()
	if got := back.(model).View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("after switching it off again, MouseMode = %v, want none", got)
	}
}

// TestOnlyADeliberateMouseActCounts: a click and a wheel turn act and reset the
// idle timer; release and motion do nothing. These were one message with an
// Action field that one branch read; they are four types now, and a case put in
// the wrong group would either make a resting pointer count as work or make the
// wheel stop scrolling.
func TestOnlyADeliberateMouseActCounts(t *testing.T) {
	base := baseModel()
	base.fleet = sampleFleet()
	base.mouseEnabled = true
	base.cursor = 2

	// Release and motion are inert: neither the selection nor the idle clock moves.
	for _, msg := range []tea.Msg{
		tea.MouseReleaseMsg{Button: tea.MouseLeft},
		tea.MouseMotionMsg{},
	} {
		next, _ := base.Update(msg)
		got := next.(model)
		if got.cursor != base.cursor {
			t.Errorf("%T moved the selection to %d", msg, got.cursor)
		}
		if !got.lastInput.IsZero() {
			t.Errorf("%T counted as input for the idle timer", msg)
		}
	}

	// The wheel steps the selection and counts as input, exactly as it did when
	// the old model reported it as a press.
	next, _ := base.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if got := next.(model); got.cursor != base.cursor+1 {
		t.Errorf("the wheel should step the selection, cursor = %d", got.cursor)
	} else if got.lastInput.IsZero() {
		t.Error("the wheel should reset the idle timer")
	}

	clicked, _ := base.Update(tea.MouseClickMsg{Button: tea.MouseLeft})
	if clicked.(model).lastInput.IsZero() {
		t.Error("a click should reset the idle timer")
	}
}

// TestPasteLandsInTheField: a bracketed paste is its own message now rather than
// a run of runes carrying a flag. It has to reach the open field, and it has to
// arrive filtered — a paste is the one input that routinely carries newlines and
// escapes, and the field is drawn straight onto a real terminal.
func TestPasteLandsInTheField(t *testing.T) {
	m := baseModel()
	m.input, m.inputBuf = inputDispatch, "run "

	next, _ := m.Update(tea.PasteMsg{Content: "make\ttest\nnow"})
	if got := next.(model).inputBuf; got != "run maketestnow" {
		t.Fatalf("inputBuf = %q, want the paste appended with its control bytes dropped", got)
	}
}

// TestPasteMatchesNoBinding: pasted text must never fire a binding. "w" alone
// closes a panel; the same character inside a paste is text, and on the
// dashboard — where there is no field open — it is simply nothing.
func TestPasteMatchesNoBinding(t *testing.T) {
	m := baseModel()
	m.fleet = sampleFleet()
	before := len(m.fleet)

	next, _ := m.Update(tea.PasteMsg{Content: "w"})
	if got := len(next.(model).fleet); got != before {
		t.Fatalf("a pasted %q closed a panel: %d -> %d", "w", before, got)
	}
}
