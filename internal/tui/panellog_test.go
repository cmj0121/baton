package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// loggingFleet is a small fleet with one panel already being logged, so the badge
// and the footer cap have something to render and something to leave alone.
func loggingFleet() []panel.Panel {
	return []panel.Panel{
		{ID: "1", Kind: panel.Agent, Title: "claude", State: panel.Running, Logging: true, LogFile: "/var/log/baton/2026-08-18-claude-1.log"},
		{ID: "2", Kind: panel.Shell, Title: "shell", State: panel.Idle},
	}
}

// TestLogToggleSendsCommand checks that the logging key asks the DAEMON rather
// than deciding locally: the daemon holds the file, so the cockpit sends and
// waits for the snapshot to tell it what happened.
func TestLogToggleSendsCommand(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = loggingFleet()
	m.cursor = 0

	if _, cmd := m.toggleLog(); cmd != nil {
		t.Fatalf("toggleLog should send and return no tea.Cmd, got %v", cmd)
	}
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.log" })
	if got.ID != "1" {
		t.Errorf("panel.log should target the selected panel, got %+v", got)
	}
}

// TestLogViewSendsCommandOnlyWhenLogging checks the read-back key's precondition
// and that it stashes the transient panel's title for the "ephemeral" reply, the
// way the git menu does.
func TestLogViewSendsCommandOnlyWhenLogging(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = loggingFleet()

	m.cursor = 1 // the unlogged shell
	mm, _ := m.viewLog()
	m2 := mm.(model)
	if !strings.Contains(m2.status, "not being logged") {
		t.Errorf("status = %q; want it to say the panel is not being logged", m2.status)
	}
	if !strings.Contains(m2.status, "C-t") {
		t.Errorf("status = %q; want it to name the chord that starts logging", m2.status)
	}

	m.cursor = 0 // the logged agent
	mm, _ = m.viewLog()
	m3 := mm.(model)
	if m3.pendingEphemeralTitle == "" {
		t.Errorf("the transient panel's title should be stashed for the ephemeral reply")
	}
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.logview" })
	if got.ID != "1" {
		t.Errorf("panel.logview should target the selected panel, got %+v", got)
	}
}

// TestLogTargetFollowsTheView checks that both keys act on what is focused: the
// dashboard selection, the zoomed panel, the focused member of a split — and on
// nothing at all when a work item is selected, since logging is per panel.
func TestLogTargetFollowsTheView(t *testing.T) {
	m := baseModel()
	m.fleet = loggingFleet()

	m.cursor = 1
	if p, ok := m.logTarget(); !ok || p.ID != "2" {
		t.Errorf("dashboard target = %+v,%v; want panel 2", p, ok)
	}

	m.mode = modeZoom
	m.zoomID = "1"
	if p, ok := m.logTarget(); !ok || p.ID != "1" {
		t.Errorf("zoom target = %+v,%v; want panel 1", p, ok)
	}

	// A selected group is deliberately not a target.
	m = baseModel()
	m.fleet = groupedFleet()
	m.cursor = 0
	if it, _ := m.selectedItem(); it.kind != itemGroup {
		t.Skipf("setup: the first dashboard row is not a group (%v)", it.kind)
	}
	if _, ok := m.logTarget(); ok {
		t.Errorf("a work item must not resolve to one member")
	}
}

// TestLogKeysNeedASelection checks the empty-fleet path: both keys say what to do
// rather than sending a command with no id.
func TestLogKeysNeedASelection(t *testing.T) {
	m := baseModel()
	for name, run := range map[string]func() (any, any){
		"toggle": func() (any, any) { mm, cmd := m.toggleLog(); return mm, cmd },
		"view":   func() (any, any) { mm, cmd := m.viewLog(); return mm, cmd },
	} {
		mm, _ := run()
		if got := mm.(model).status; !strings.Contains(got, "select a panel") {
			t.Errorf("%s with no selection: status = %q; want it to ask for a selection", name, got)
		}
	}
}

// TestLogBadgeAndFooter is the visibility contract: a panel being written to disk
// is marked on its card and named in the footer while it is focused, and an
// ordinary panel gains neither.
func TestLogBadgeAndFooter(t *testing.T) {
	m := baseModel()
	m.fleet = loggingFleet()

	if got := m.logBadge(m.fleet[0]); got == "" {
		t.Errorf("a logging panel should be badged on its card")
	}
	if got := m.logBadge(m.fleet[1]); got != "" {
		t.Errorf("an unlogged panel should carry no badge, got %q", got)
	}

	m.cursor = 0
	cap0 := m.logCap()
	if cap0 == "" || !strings.Contains(cap0, "LOG") {
		t.Errorf("the footer should name the log while the panel is focused, got %q", cap0)
	}
	if !strings.Contains(cap0, "claude-1.log") {
		t.Errorf("the footer cap should name the FILE, got %q", cap0)
	}
	m.cursor = 1
	if got := m.logCap(); got != "" {
		t.Errorf("an unlogged focus should show no cap, got %q", got)
	}
}

// TestLogKeysArePrefixReached documents the binding shape: both are escapes, so
// they never collide with the dashboard's bare l (move) or the split's bare L
// (cycle the layout).
func TestLogKeysArePrefixReached(t *testing.T) {
	m := baseModel()
	for _, act := range []action{actLogToggle, actLogView} {
		if !isEscape(act) {
			t.Errorf("action %v should be prefix-reached", act)
		}
		key := m.bindingKey(act)
		if _, ok := m.lookupEscape(key); !ok {
			t.Errorf("no escape resolves %q", key)
		}
		if b, ok := m.lookupCmd(key); ok && (b.act == actLogToggle || b.act == actLogView) {
			t.Errorf("%q must not fire as a bare command-mode key", key)
		}
	}
	if m.bindingKey(actLogToggle) == m.bindingKey(actLogView) {
		t.Errorf("the two logging keys must differ")
	}
}
