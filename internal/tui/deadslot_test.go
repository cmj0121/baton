package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/panel"
)

// deadSlotPanel is a panel the daemon rebuilt from its persisted fleet: exited,
// and with no terminal record behind it. It is what every panel in the fleet
// looks like after a daemon restart.
var deadSlotPanel = panel.Panel{ID: "63", Title: "claude · catnip", State: panel.Exited,
	Activity: "restored · press r to re-run"}

// resultPanel is the other thing Exited means: a panel that died under THIS
// daemon, whose ring still holds the last screen it drew.
var resultPanel = panel.Panel{ID: "67", Title: "shell #67", State: panel.Exited, Replay: true}

// dashModel is a cockpit sitting on the dashboard over the given fleet, with the
// cursor on the first row.
func dashModel(t *testing.T, fleet ...panel.Panel) model {
	t.Helper()
	c, _ := recordingServer(t)
	return model{client: c, width: 80, height: 24, mode: modeDashboard, fleet: fleet,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
}

// TestEnterOnADeadSlotIsRefused is the defect: enter opened a zoom onto a panel
// with nothing behind it. The emulator was created, the footer drew "◼ EXITED"
// and the status said "result · … (exited)", but attach had no ring to send, so
// the body was black and esc was the only way out.
//
// The mutation that kills this: drop the guard from zoomInto. m.mode goes to
// modeZoom for a slot whose Replay is false, and the cockpit is in the black
// screen this refusal exists to prevent.
func TestEnterOnADeadSlotIsRefused(t *testing.T) {
	m := dashModel(t, deadSlotPanel)

	out, _ := m.activate()
	got := out.(model)

	if got.mode != modeDashboard {
		t.Fatalf("the cockpit left the dashboard for a slot with nothing behind it (mode %v)", got.mode)
	}
	if got.emu != nil {
		t.Error("an emulator was opened for a panel that can never feed one")
	}
	if !strings.Contains(got.status, "r") || got.status == "" {
		t.Errorf("the refusal should name the remedy, got %q", got.status)
	}
}

// TestEnterOnAnExitedPanelWithOutputStillZooms is the half a guard keyed on the
// STATE rather than on the replay would have destroyed. A panel that died here
// keeps its ring, and reading its last screen is the entire reason the fleet
// holds dead slots at all — see maxExitedPanels on the server.
func TestEnterOnAnExitedPanelWithOutputStillZooms(t *testing.T) {
	m := dashModel(t, resultPanel)

	out, _ := m.activate()
	got := out.(model)

	if got.mode != modeZoom {
		t.Fatalf("an exited panel with output should still open as a result view (mode %v)", got.mode)
	}
	if !got.zoomExited {
		t.Error("the zoom should know it is a result view, not a live panel")
	}
}

// TestDeadSlotRefusalIsTranslated: the line an operator meets instead of the
// black screen is one of the cockpit's own, not an English constant.
func TestDeadSlotRefusalIsTranslated(t *testing.T) {
	en := model{}.deadSlotRefusal(deadSlotPanel)
	zh := model{lang: i18n.ZhTW}.deadSlotRefusal(deadSlotPanel)
	if en == "" || zh == "" {
		t.Fatalf("a dead slot must be refused in both languages, got %q and %q", en, zh)
	}
	if en == zh {
		t.Errorf("the refusal is untranslated: %q", en)
	}
	if got := (model{}.deadSlotRefusal(resultPanel)); got != "" {
		t.Errorf("a panel with output is not refused, got %q", got)
	}
	for _, live := range []panel.State{panel.Running, panel.Idle, panel.Done, panel.Stuck, panel.Attention} {
		p := panel.Panel{ID: "1", State: live}
		if got := (model{}.deadSlotRefusal(p)); got != "" {
			t.Errorf("a %v panel carries no ring yet either, and must not be refused, got %q", live, got)
		}
	}
}

// TestGroupSplitRefusesBeforeItTearsItselfDown: the split drops every tile's
// stream and closes its emulators on the way into a single zoom, so it has to
// ask first. Refusing after that would leave the operator in a split that has to
// be rebuilt, for a keystroke that did nothing.
func TestGroupSplitRefusesBeforeItTearsItselfDown(t *testing.T) {
	m := baseModel()
	m.fleet = []panel.Panel{
		{ID: "63", Kind: panel.Agent, Title: "claude · catnip", State: panel.Exited, Group: "catnip"},
		{ID: "64", Kind: panel.Agent, Title: "grok · catnip", State: panel.Running, Group: "catnip"},
	}
	m = m.zoomGroup(m.dashItems()[0])
	if m.mode != modeGroupZoom {
		t.Fatalf("the fixture should open a split, got mode=%v", m.mode)
	}
	for i := 0; i < 8; i++ {
		if p, ok := m.focusedMember(); ok && p.ID == "63" {
			break
		}
		m.groupFocus++
	}
	p, ok := m.focusedMember()
	if !ok || p.ID != "63" {
		t.Fatalf("could not put the focus on the dead slot, got %+v (ok %v)", p, ok)
	}

	out, _ := m.zoomFocusedMember()
	got := out.(model)

	if got.mode != modeGroupZoom {
		t.Fatalf("the split should still be on screen (mode %v)", got.mode)
	}
	if got.status == "" {
		t.Error("the refusal said nothing")
	}
}

// TestInboxRefusesWithoutClosingTheQueue: same rule as the gone-panel branch it
// sits beside — the overlay must not come off the screen to say that nothing
// happened.
func TestInboxRefusesWithoutClosingTheQueue(t *testing.T) {
	dead := wire("63", "exited", time.Minute)
	dead.ExitCode = 1 // a clean exit is not news, and never reaches the queue
	m := openedInbox(t, dead)
	if len(m.inboxRows) != 1 {
		t.Fatalf("the fixture should queue one row, got %d", len(m.inboxRows))
	}

	out, _ := m.zoomInboxRow()
	got := out.(model)

	if got.mode != modeInbox {
		t.Fatalf("the queue closed to tell the operator that nothing happened (mode %v)", got.mode)
	}
	if got.status == "" {
		t.Error("the refusal said nothing")
	}
}
