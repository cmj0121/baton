package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// key builds a tea.KeyMsg for a single rune or a named special key.
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// press feeds a sequence of keys through handleKey and returns the final model.
func press(m model, keys ...string) model {
	for _, k := range keys {
		next, _ := m.handleKey(key(k))
		m = next.(model)
	}
	return m
}

func TestCloseRequiresConfirmation(t *testing.T) {
	m := model{fleet: sampleFleet(), confirmClose: true}
	before := len(m.fleet)

	// prefix + w arms the confirmation but does not close yet.
	m = press(m, "w")
	if !m.pendingClose {
		t.Fatal("expected a pending close confirmation")
	}
	if len(m.fleet) != before {
		t.Fatalf("panel closed before confirmation: %d -> %d", before, len(m.fleet))
	}

	// 'y' confirms and drops exactly one panel.
	m = press(m, "y")
	if m.pendingClose {
		t.Fatal("confirmation should be cleared after answering")
	}
	if len(m.fleet) != before-1 {
		t.Fatalf("expected one panel closed, got %d -> %d", before, len(m.fleet))
	}
}

// TestCloseGroupAlwaysConfirms proves w on a group card asks for confirmation
// even when confirm-on-close is off — a group close retires every member at once,
// so it is never a one-keystroke action — and the prompt names the panel count.
func TestCloseGroupAlwaysConfirms(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.confirmClose = false // off: a lone panel would close instantly
	m.cursor = 0           // the 3-member api group
	before := len(m.fleet)

	m = press(m, "w")
	if !m.pendingClose {
		t.Fatal("a group close must confirm even with confirm-on-close off")
	}
	if !strings.Contains(m.status, "group") || !strings.Contains(m.status, "3 panel") {
		t.Fatalf("the prompt should name the group and its panel count, got %q", m.status)
	}
	if len(m.fleet) != before {
		t.Fatalf("nothing should close before the answer: %d -> %d", before, len(m.fleet))
	}

	m = press(m, "y")
	if m.pendingClose {
		t.Fatal("confirmation should clear after answering")
	}
	for _, p := range m.fleet {
		if p.Group == "api" {
			t.Fatalf("every api member should be gone, found %s", p.ID)
		}
	}
}

// TestCloseLonePanelRespectsToggle proves a single panel still closes in one
// keystroke when confirm-on-close is off, so the group rule does not leak onto it.
func TestCloseLonePanelRespectsToggle(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	m.confirmClose = false
	m.cursor = 1 // the lone shell (id 2)
	before := len(m.fleet)

	m = press(m, "w")
	if m.pendingClose {
		t.Fatal("a lone panel with confirm-on-close off should not arm a prompt")
	}
	if len(m.fleet) != before-1 {
		t.Fatalf("the lone panel should close immediately: %d -> %d", before, len(m.fleet))
	}
}

func TestCloseCancelsOnAnyOtherKey(t *testing.T) {
	m := model{fleet: sampleFleet(), confirmClose: true}
	before := len(m.fleet)

	m = press(m, "w", "n")
	if m.pendingClose {
		t.Fatal("expected the confirmation to be cancelled")
	}
	if len(m.fleet) != before {
		t.Fatalf("panel closed on cancel: %d -> %d", before, len(m.fleet))
	}
	if !strings.Contains(m.status, "cancel") {
		t.Fatalf("status should report the cancel, got %q", m.status)
	}
}

func TestConfirmToggleSkipsPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // toggling persists to $HOME/.baton/config
	m := model{mode: modeKeyMap, fleet: sampleFleet(), confirmClose: true}
	// Move the cursor onto the settings toggle (after the prefix + bindings) and
	// flip it off.
	m.cursor = len(bindings) + 1
	m = press(m, "enter")
	if m.confirmClose {
		t.Fatal("expected confirm-on-close to be toggled off")
	}

	// With the gate off, prefix + w closes immediately, no pending state.
	m.mode = modeDashboard
	m.cursor = 0
	before := len(m.fleet)
	m = press(m, "w")
	if m.pendingClose {
		t.Fatal("close should not prompt when the gate is off")
	}
	if len(m.fleet) != before-1 {
		t.Fatalf("expected immediate close, got %d -> %d", before, len(m.fleet))
	}
}

func TestTabCyclesSelection(t *testing.T) {
	m := model{mode: modeDashboard, fleet: sampleFleet(), width: 110}
	m = press(m, "tab", "tab")
	if m.cursor != 2 {
		t.Fatalf("expected cursor at 2 after two tabs, got %d", m.cursor)
	}
}

// TestTabWrapsOnDashboard checks tab/shift+tab wrap at the ends on the dashboard,
// matching the group split's focus ring.
func TestTabWrapsOnDashboard(t *testing.T) {
	m := model{mode: modeDashboard, fleet: sampleFleet(), width: 110}
	last := m.itemCount() - 1

	// shift+tab from the first item wraps to the last.
	m = press(m, "shift+tab")
	if m.cursor != last {
		t.Fatalf("shift+tab from the top should wrap to %d, got %d", last, m.cursor)
	}
	// tab from the last wraps back to the first.
	m = press(m, "tab")
	if m.cursor != 0 {
		t.Fatalf("tab from the last should wrap to 0, got %d", m.cursor)
	}
}

func TestRebindKeyByTyping(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // rebinding persists to $HOME/.baton/config
	m := model{mode: modeKeyMap, fleet: sampleFleet(), binds: append([]binding(nil), bindings...)}
	m.cursor = 1 // row 0 is the prefix; row 1 is the spawn binding (default "p")

	// e arms the capture; it does not change anything yet.
	m = press(m, "e")
	if !m.editing || m.editIdx != 0 {
		t.Fatalf("expected to be capturing binding 0, got editing=%v idx=%d", m.editing, m.editIdx)
	}

	// Typing x rebinds spawn to x.
	m = press(m, "x")
	if m.editing {
		t.Fatal("capture should end after a key is typed")
	}
	if got := m.binds[0].key; got != "x" {
		t.Fatalf("expected spawn rebound to x, got %q", got)
	}

	// The prefix now resolves the new chord and forgets the old one.
	if b, ok := m.lookupCmd("x"); !ok || b.act != actNewPanel {
		t.Fatalf("prefix+x should trigger spawn, got %+v ok=%v", b, ok)
	}
	if _, ok := m.lookupCmd("p"); ok {
		t.Fatal("old key p should no longer be bound")
	}
}

func TestRebindPersistsToConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Rebind spawn from p to x, which writes the config file as a side effect.
	m := model{mode: modeKeyMap, fleet: sampleFleet(), binds: append([]binding(nil), bindings...)}
	m.cursor = 1 // the spawn binding (row 0 is the prefix)
	press(m, "e", "x")

	// A fresh load (as New would do) sees the override applied to spawn and the
	// other bindings left at their defaults.
	reloaded := loadPrefs().binds
	for _, b := range reloaded {
		switch b.name {
		case "new-panel":
			if b.key != "x" {
				t.Fatalf("persisted spawn key = %q, want x", b.key)
			}
		case "close":
			if b.key != keyClose {
				t.Fatalf("close key drifted to %q", b.key)
			}
		}
	}

	// Only the changed key is persisted, so a default the user never touched
	// flows through on the next release instead of being masked.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Keys["new-panel"] != "x" {
		t.Fatalf("changed key should persist, got %q", cfg.Keys["new-panel"])
	}
	if _, ok := cfg.Keys["close"]; ok {
		t.Fatal("an unchanged key should not be written to the config")
	}
}

func TestConfirmTogglePersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Default is on; toggle it off on the settings row.
	m := model{mode: modeKeyMap, fleet: sampleFleet(), confirmClose: true, binds: append([]binding(nil), bindings...)}
	m.cursor = len(bindings) + 1 // prefix row + bindings, then the settings toggle
	press(m, "enter")

	if loadPrefs().confirmClose {
		t.Fatal("confirm-on-close should persist as off")
	}
}

func TestRebindCancelsOnEsc(t *testing.T) {
	m := model{mode: modeKeyMap, fleet: sampleFleet(), binds: append([]binding(nil), bindings...)}
	m.cursor = 1 // the spawn binding (row 0 is the prefix)
	m = press(m, "e", "esc")
	if m.editing {
		t.Fatal("esc should cancel the capture")
	}
	if m.binds[0].key != "p" {
		t.Fatalf("binding should be unchanged after cancel, got %q", m.binds[0].key)
	}
}

func TestChangePrefixKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Edit the prefix row (row 0) and type a new leader key.
	m := model{mode: modeKeyMap, fleet: sampleFleet(), prefixKey: keyPrefix, binds: append([]binding(nil), bindings...)}
	m.cursor = 0
	m = press(m, "e", "ctrl+a")
	if m.editing {
		t.Fatal("capture should end after a key is typed")
	}
	if m.prefixKey != "ctrl+a" {
		t.Fatalf("prefix not changed, got %q", m.prefixKey)
	}

	// The new prefix arms a binding; the old one no longer does.
	armed := press(m, "ctrl+a")
	if !armed.prefix {
		t.Fatal("the new prefix ctrl+a should arm")
	}
	if old := press(m, "ctrl+t"); old.prefix {
		t.Fatal("the old prefix ctrl+t should no longer arm")
	}

	// And it persists for the next session.
	if got := loadPrefs().prefix; got != "ctrl+a" {
		t.Fatalf("prefix not persisted, got %q", got)
	}
}

func TestRestartBindingFlagsRestart(t *testing.T) {
	m := model{mode: modeDashboard, fleet: sampleFleet()}
	if m.RestartRequested() {
		t.Fatal("a fresh model should not request a restart")
	}

	// S arms a confirmation — it does not restart or quit yet.
	m = press(m, "S")
	if !m.pendingRestart {
		t.Fatal("S should arm the restart confirmation")
	}
	if m.restart || m.quitting {
		t.Fatal("a restart must not fire before the user confirms")
	}

	// y confirms: the cockpit flags a restart and quits so the runner relaunches.
	m = press(m, "y")
	if !m.restart || !m.RestartRequested() || !m.quitting {
		t.Fatal("y should confirm the force-restart and quit")
	}
}

// TestRestartConfirmationCancels checks any non-yes key aborts the restart.
func TestRestartConfirmationCancels(t *testing.T) {
	m := model{mode: modeDashboard, fleet: sampleFleet()}
	m = press(m, "S")
	if !m.pendingRestart {
		t.Fatal("S should arm the restart confirmation")
	}
	m = press(m, "n")
	if m.pendingRestart || m.restart || m.quitting {
		t.Fatal("n should cancel the restart cleanly")
	}
	if m.status != "restart cancelled" {
		t.Fatalf("expected a cancellation status, got %q", m.status)
	}
}

func TestMergeFleetMapsServerSnapshot(t *testing.T) {
	if got := mergeFleet(nil); len(got) != 0 {
		t.Fatalf("an empty snapshot should yield an empty fleet, got %d", len(got))
	}

	snap := []proto.Panel{
		{ID: "7", Kind: "agent", Title: "claude", State: "attention", Group: "auth", Activity: "needs you"},
		{ID: "8", Kind: "shell", Title: "sh", State: "idle"},
	}
	got := mergeFleet(snap)
	if len(got) != 2 {
		t.Fatalf("expected 2 panels, got %d", len(got))
	}
	if got[0].Kind != panel.Agent || got[0].State != panel.Attention || got[0].Group != "auth" || got[0].Activity != "needs you" {
		t.Fatalf("agent panel mapped wrong: %+v", got[0])
	}
	if got[1].Kind != panel.Shell || got[1].State != panel.Idle {
		t.Fatalf("shell panel mapped wrong: %+v", got[1])
	}
}

func TestScrollWindowKeepsCursorVisible(t *testing.T) {
	cases := []struct{ cursor, count, visible, wantStart, wantEnd int }{
		{0, 8, 4, 0, 4}, // top: window pinned to start
		{4, 8, 4, 2, 6}, // middle: cursor centred
		{7, 8, 4, 4, 8}, // bottom: window pinned to end
		{3, 3, 5, 0, 3}, // fits entirely: no scroll
	}
	for _, c := range cases {
		start, end := scrollWindow(c.cursor, c.count, c.visible)
		if start != c.wantStart || end != c.wantEnd {
			t.Errorf("scrollWindow(%d,%d,%d) = (%d,%d), want (%d,%d)",
				c.cursor, c.count, c.visible, start, end, c.wantStart, c.wantEnd)
		}
		if c.visible < c.count && (c.cursor < start || c.cursor >= end) {
			t.Errorf("cursor %d fell outside window [%d,%d)", c.cursor, start, end)
		}
	}
}

func TestTreeViewKicksInForLargeFleet(t *testing.T) {
	full := model{fleet: sampleFleet()}
	if !full.treeView() {
		t.Fatalf("fleet of %d should use the tree view", len(full.fleet))
	}

	small := model{fleet: sampleFleet()[:treeThreshold]}
	if small.treeView() {
		t.Fatalf("fleet of %d should use the card grid", len(small.fleet))
	}
}

func TestKeyMapShowsPurposeSections(t *testing.T) {
	m := model{mode: modeKeyMap, width: 120, height: 44, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	v := m.View()
	for _, sec := range []string{"Panels", "View", "Work items", "Session"} {
		if !strings.Contains(v, sec) {
			t.Fatalf("key map should show the %q purpose section", sec)
		}
	}
}

func TestKeyMapTabJumpsSections(t *testing.T) {
	m := model{mode: modeKeyMap, binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	anchors := m.keyMapAnchors()
	if anchors[0] != 0 {
		t.Fatalf("first anchor should be the prefix row, got %d", anchors[0])
	}
	if last := anchors[len(anchors)-1]; last != len(m.keymap())+1 {
		t.Fatalf("last anchor should be the settings row, got %d", last)
	}

	// tab steps forward through the section anchors; shift+tab back; both wrap.
	m.cursor = 0
	m = press(m, "tab")
	if m.cursor != anchors[1] {
		t.Fatalf("tab from the prefix should land on the first section, got %d want %d", m.cursor, anchors[1])
	}
	m = press(m, "tab")
	if m.cursor != anchors[2] {
		t.Fatalf("tab should advance to the next section, got %d want %d", m.cursor, anchors[2])
	}
	m = press(m, "shift+tab")
	if m.cursor != anchors[1] {
		t.Fatalf("shift+tab should go back a section, got %d want %d", m.cursor, anchors[1])
	}
	m.cursor = anchors[len(anchors)-1]
	m = press(m, "tab")
	if m.cursor != anchors[0] {
		t.Fatalf("tab should wrap from the last section to the prefix, got %d", m.cursor)
	}
}
