package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	vt "github.com/charmbracelet/x/vt"
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
//
// The panel count is not enough to see this. Close asks first, so a paste that
// DID reach the binding would arm the confirmation and leave the fleet exactly
// as long as it was — which is why the armed prompt is what is asserted.
func TestPasteMatchesNoBinding(t *testing.T) {
	m := baseModel()
	m.fleet = sampleFleet()
	before := len(m.fleet)

	next, _ := m.Update(tea.PasteMsg{Content: "w"})
	got := next.(model)
	if got.pendingClose {
		t.Error("a pasted \"w\" reached the close binding and armed its prompt")
	}
	if len(got.fleet) != before {
		t.Errorf("a pasted \"w\" closed a panel: %d -> %d", before, len(got.fleet))
	}
}

// TestPasteReachesEveryTextSink walks the places pasted text has to land. Each
// is a place an ordinary keystroke reaches through a different branch of Update,
// and the paste now takes its own road there — so each needs saying out loud, or
// a paste into one of them becomes a paste into nothing.
func TestPasteReachesEveryTextSink(t *testing.T) {
	t.Run("zoomed panel", func(t *testing.T) {
		emu := vt.NewSafeEmulator(20, 5)
		got := make(chan string, 1)
		go func() {
			buf := make([]byte, 64)
			n, _ := emu.Read(buf)
			got <- string(buf[:n])
		}()

		m := baseModel()
		m.mode, m.zoomID, m.emu = modeZoom, "1", emu
		if _, _ = m.Update(tea.PasteMsg{Content: "make test"}); <-got != "make test" {
			t.Error("a paste should reach the zoomed program")
		}
	})

	t.Run("inbox composer", func(t *testing.T) {
		m := openedInbox(t, wire("1", "attention", time.Minute))
		m.inboxComposing, m.inboxReply = true, "ye"
		next, _ := m.Update(tea.PasteMsg{Content: "s please"})
		if got := next.(model).inboxReply; got != "yes please" {
			t.Errorf("inboxReply = %q", got)
		}
	})

	t.Run("workdir picker filter", func(t *testing.T) {
		m := baseModel()
		m.mode, m.dirPickTyping = modeDirPick, true
		m.dirPickDir = t.TempDir()
		next, _ := m.Update(tea.PasteMsg{Content: "src"})
		if got := next.(model).dirPickFilter; got != "src" {
			t.Errorf("dirPickFilter = %q", got)
		}
	})

	t.Run("scrollback takes none", func(t *testing.T) {
		m := baseModel()
		m.mode, m.emu, m.scrolling = modeZoom, vt.NewSafeEmulator(20, 5), true
		next, _ := m.Update(tea.PasteMsg{Content: "q"})
		if !next.(model).scrolling {
			t.Error("a paste should not drop scroll mode")
		}
	})
}

// TestPasteDropsAnArmedLeader: text is never the continuation of a chord. If a
// paste left the leader armed, the next real keystroke would be read as the
// second half of a command nobody typed.
func TestPasteDropsAnArmedLeader(t *testing.T) {
	m := baseModel()
	m.mode, m.emu, m.zoomArmed = modeZoom, vt.NewSafeEmulator(20, 5), true
	go func() {
		buf := make([]byte, 64)
		_, _ = m.emu.Read(buf)
	}()

	next, _ := m.Update(tea.PasteMsg{Content: "x"})
	if next.(model).zoomArmed {
		t.Error("a paste should clear the armed leader rather than let it swallow the next key")
	}
}
