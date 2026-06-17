package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
	m := model{fleet: panel.Mock(), confirmClose: true}
	before := len(m.fleet)

	// prefix + w arms the confirmation but does not close yet.
	m = press(m, "ctrl+t", "w")
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

func TestCloseCancelsOnAnyOtherKey(t *testing.T) {
	m := model{fleet: panel.Mock(), confirmClose: true}
	before := len(m.fleet)

	m = press(m, "ctrl+t", "w", "n")
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
	m := model{mode: modeKeyMap, fleet: panel.Mock(), confirmClose: true}
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
	m = press(m, "ctrl+t", "w")
	if m.pendingClose {
		t.Fatal("close should not prompt when the gate is off")
	}
	if len(m.fleet) != before-1 {
		t.Fatalf("expected immediate close, got %d -> %d", before, len(m.fleet))
	}
}

func TestTabCyclesSelection(t *testing.T) {
	m := model{mode: modeDashboard, fleet: panel.Mock(), width: 110}
	m = press(m, "tab", "tab")
	if m.cursor != 2 {
		t.Fatalf("expected cursor at 2 after two tabs, got %d", m.cursor)
	}
}

func TestRebindKeyByTyping(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // rebinding persists to $HOME/.baton/config
	m := model{mode: modeKeyMap, fleet: panel.Mock(), binds: append([]binding(nil), bindings...)}
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
	if b, ok := m.lookup("x"); !ok || b.act != actNewPanel {
		t.Fatalf("prefix+x should trigger spawn, got %+v ok=%v", b, ok)
	}
	if _, ok := m.lookup("p"); ok {
		t.Fatal("old key p should no longer be bound")
	}
}

func TestRebindPersistsToConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Rebind spawn from p to x, which writes the config file as a side effect.
	m := model{mode: modeKeyMap, fleet: panel.Mock(), binds: append([]binding(nil), bindings...)}
	m.cursor = 1 // the spawn binding (row 0 is the prefix)
	press(m, "e", "x")

	// A fresh load (as New would do) sees the override applied to spawn and the
	// other bindings left at their defaults.
	_, reloaded, _ := loadConfig()
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
}

func TestConfirmTogglePersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Default is on; toggle it off on the settings row.
	m := model{mode: modeKeyMap, fleet: panel.Mock(), confirmClose: true, binds: append([]binding(nil), bindings...)}
	m.cursor = len(bindings) + 1 // prefix row + bindings, then the settings toggle
	press(m, "enter")

	if _, _, confirmClose := loadConfig(); confirmClose {
		t.Fatal("confirm-on-close should persist as off")
	}
}

func TestRebindCancelsOnEsc(t *testing.T) {
	m := model{mode: modeKeyMap, fleet: panel.Mock(), binds: append([]binding(nil), bindings...)}
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
	m := model{mode: modeKeyMap, fleet: panel.Mock(), prefixKey: keyPrefix, binds: append([]binding(nil), bindings...)}
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
	if prefix, _, _ := loadConfig(); prefix != "ctrl+a" {
		t.Fatalf("prefix not persisted, got %q", prefix)
	}
}

func TestRestartBindingFlagsRestart(t *testing.T) {
	m := model{mode: modeDashboard, fleet: panel.Mock()}
	if m.RestartRequested() {
		t.Fatal("a fresh model should not request a restart")
	}

	// prefix + S (shift+s) asks for a force-restart and quits.
	m = press(m, "ctrl+t", "S")
	if !m.restart || !m.RestartRequested() {
		t.Fatal("prefix+S should flag a restart")
	}
	if !m.quitting {
		t.Fatal("a restart should quit the cockpit so the runner can relaunch")
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
	full := model{fleet: panel.Mock()}
	if !full.treeView() {
		t.Fatalf("fleet of %d should use the tree view", len(full.fleet))
	}

	small := model{fleet: panel.Mock()[:treeThreshold]}
	if small.treeView() {
		t.Fatalf("fleet of %d should use the card grid", len(small.fleet))
	}
}
