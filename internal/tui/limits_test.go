package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/cmj0121/baton/internal/cgroup"
	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/limits"
	"github.com/cmj0121/baton/internal/proto"
)

// newLimitsModel is the shared cockpit sitting on the panel-config screen with
// the cursor on the first resource-limit row.
func newLimitsModel(t *testing.T) model {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // editing a limit persists to $HOME/.baton/config
	m := baseModel()
	// The tab as well as the row: the page's cursor belongs to the open tab, and a
	// fixture that set one without the other would be a state the keys cannot
	// reach — which is the shape of the bug the tabs first shipped with.
	m.mode, m.panelTab, m.cursor = modePanelConfig, 1, firstLimitRow
	return m
}

// TestPanelConfigWalksToLimitRows drives the keys a user actually presses: open
// the panel config, walk down past the spawn defaults, and confirm every limit
// row is reachable and opens its own overlay.
func TestPanelConfigWalksToLimitRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := baseModel()

	m = press(m, "ctrl+t", "P")
	if m.mode != modePanelConfig {
		t.Fatalf("C-t P should open panel config, mode=%v", m.mode)
	}
	// ↓ walks the OPEN tab and stops at its end: the page partitions one row index
	// across its tabs, and the defaults tab ends where the limits tab begins.
	for i := 0; i < firstLimitRow+2; i++ {
		m = press(m, "down")
	}
	if m.cursor != firstLimitRow-1 {
		t.Fatalf("down should stop at the last defaults row, cursor=%d", m.cursor)
	}

	// → opens the limits tab and lands on its first row.
	m = press(m, "right")
	if m.cursor != firstLimitRow {
		t.Fatalf("right should open the limits tab on its first row, cursor=%d", m.cursor)
	}
	for i := 0; i < len(limitFields)+2; i++ {
		m = press(m, "down")
	}
	if m.cursor != numPanelConfigRows-1 {
		t.Fatalf("down should reach the last limit row and clamp there, cursor=%d", m.cursor)
	}

	for i, f := range limitFields {
		m.cursor, m.input = firstLimitRow+i, inputNone
		m = press(m, "e")
		if m.input != inputLimit || m.limitRow != firstLimitRow+i {
			t.Fatalf("e on %q should open its overlay, input=%v limitRow=%d", f.label, m.input, m.limitRow)
		}
		m = press(m, "esc")
	}
}

// TestPanelConfigEditsCPULimit is the end-to-end path for one row: type a value,
// press enter, and find it on disk where the daemon will read it.
func TestPanelConfigEditsCPULimit(t *testing.T) {
	m := newLimitsModel(t)

	m = press(m, "e")
	if m.input != inputLimit || m.limitRow != panelRowCPUs {
		t.Fatalf("e should open the cpus overlay, input=%v limitRow=%d", m.input, m.limitRow)
	}
	for _, r := range "1.5" {
		m = press(m, string(r))
	}
	m = press(m, "enter")
	if m.input != inputNone || m.limits.CPUs != "1.5" {
		t.Fatalf("enter should save 1.5 cores, input=%v cpus=%q", m.input, m.limits.CPUs)
	}
	if got := loadPrefs().limits.CPUs; got != "1.5" {
		t.Fatalf("the cpu limit was not persisted, got %q", got)
	}
}

// TestCommitLimitRules covers the value rules on one row: a readable quantity is
// saved, blank clears the cap, and an unreadable one is rejected with the overlay
// kept open on the attempt so the typo can be corrected rather than retyped.
func TestCommitLimitRules(t *testing.T) {
	base := newLimitsModel(t)
	base.limitRow = panelRowMemory
	base.limits = limits.Limits{Memory: "4Gi"}

	if got := base.commitLimit("8Gi").limits.Memory; got != "8Gi" {
		t.Fatalf("a readable size should save, got %q", got)
	}
	if got := base.commitLimit("").limits.Memory; got != "" {
		t.Fatalf("blank should clear the cap, got %q", got)
	}
	if got := base.commitLimit(limits.Unlimited).limits.Memory; got != limits.Unlimited {
		t.Fatalf("unlimited should be accepted as an explicit no-cap, got %q", got)
	}

	m := base.commitLimit("4 gigs")
	if m.limits.Memory != "4Gi" {
		t.Fatalf("a rejected entry should leave the value alone, got %q", m.limits.Memory)
	}
	if m.input != inputLimit || m.inputBuf != "4 gigs" {
		t.Fatalf("a rejected entry should reopen the overlay on the attempt, input=%v buf=%q", m.input, m.inputBuf)
	}
	if !strings.Contains(m.status, "memory") {
		t.Fatalf("the rejection should name the field, status=%q", m.status)
	}
}

// TestCommitLimitPreservesOtherRows guards the table wiring: editing one row must
// not disturb the four beside it.
func TestCommitLimitPreservesOtherRows(t *testing.T) {
	m := newLimitsModel(t)
	m.limits = limits.Limits{CPUs: "2", Memory: "4Gi", MemoryHigh: "3Gi", Pids: "512", NOFile: "4096"}
	m.limitRow = panelRowPids

	m = m.commitLimit("1024")
	want := limits.Limits{CPUs: "2", Memory: "4Gi", MemoryHigh: "3Gi", Pids: "1024", NOFile: "4096"}
	if m.limits != want {
		t.Fatalf("editing pids changed its neighbours: %+v", m.limits)
	}
}

// TestPanelConfigViewRendersLimits checks the section renders with every row and
// that an unset limit reads as "no cap" rather than as an empty column.
func TestPanelConfigViewRendersLimits(t *testing.T) {
	m := newLimitsModel(t)
	m.limits = limits.Limits{CPUs: "2"}

	out := m.panelConfigView()
	for _, f := range limitFields {
		if !strings.Contains(out, f.label) {
			t.Errorf("the view should show the %q row:\n%s", f.label, out)
		}
	}
	if !strings.Contains(out, "no cap") {
		t.Errorf("an unset limit should read as no cap:\n%s", out)
	}
	// The spawn defaults are a tab of their own now, and this one does not show
	// them — which is the point of the split, so it is asserted rather than left
	// to be noticed.
	if strings.Contains(out, "default shell") {
		t.Errorf("the limits tab should hold only limits:\n%s", out)
	}
	m.panelTab = 0
	if out := m.panelConfigView(); !strings.Contains(out, "default shell") || !strings.Contains(out, "replay buffer") {
		t.Errorf("the defaults tab should show the spawn defaults:\n%s", out)
	}
}

// TestPanelConfigViewScrollsToEveryRow is the regression guard for the section
// headers: they make the body longer than the row count, so the scroll anchor
// has to be a body-line index, not the cursor. Rendered on a screen short enough
// that the window holds only a few lines, taking the cursor for the anchor
// scrolls the wrong part of the list into view — so check that the caret is
// visible and sits on the row the cursor actually selects.
func TestPanelConfigViewScrollsToEveryRow(t *testing.T) {
	m := newLimitsModel(t)
	m.height = 16 // leaves a three-line window, so a wrong anchor clips the selection

	labels := []string{"default shell", "default agent", "replay buffer"}
	for _, f := range limitFields {
		labels = append(labels, f.label)
	}

	for row := 0; row < numPanelConfigRows; row++ {
		m.cursor = row
		m.panelTab = 0
		if row >= firstLimitRow {
			m.panelTab = 1 // the row lives on the limits tab, which is what draws it
		}
		out := ansi.Strip(m.panelConfigView())
		caret := ""
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "▸") {
				caret = l
				break
			}
		}
		if caret == "" {
			t.Fatalf("row %d (%s): the selected row scrolled out of view:\n%s", row, labels[row], out)
		}
		if !strings.Contains(caret, labels[row]) {
			t.Fatalf("row %d: the caret sits on %q, want the %q row", row, strings.TrimSpace(caret), labels[row])
		}
	}
}

// TestLimitLabel pins how the two "no cap" spellings read: an absent field and an
// explicit unlimited mean the same thing to the fleet, so they show the same way.
func TestLimitLabel(t *testing.T) {
	m := model{}
	for _, in := range []string{"", "   ", limits.Unlimited, "UNLIMITED"} {
		if got := m.limitLabel(in); got != "no cap" {
			t.Errorf("limitLabel(%q) = %q, want %q", in, got, "no cap")
		}
	}
	if got := m.limitLabel("4Gi"); got != "4Gi" {
		t.Errorf(`limitLabel("4Gi") = %q`, got)
	}
}

// TestSpawnSendsTheProfileName checks the cockpit names the profile on the wire
// rather than resolving its limits: the server looks the caps up in its own
// config, so a client can never carry a policy it could have widened.
func TestSpawnSendsTheProfileName(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.agents = map[string]config.AgentProfile{
		"heavy": {Command: "claude", Args: []string{"--big"}, Limits: limits.Limits{Memory: "16Gi"}},
	}
	m.defaultAgent = "heavy"

	m = m.spawnAgent("/work")
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.create" })
	if got.Profile != "heavy" {
		t.Fatalf("the spawn should name its profile, got %+v", got)
	}
	if got.Path != "claude" {
		t.Fatalf("the resolved command should still travel, got %q", got.Path)
	}

	m = m.spawnConductor()
	got = waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.create" && c.Conductor })
	if got.Profile != "heavy" {
		t.Fatalf("the conductor spawn should name its profile too, got %+v", got)
	}
}

// TestEnforceLabelTellsTheTruth is the no-silent-downgrade rule at the one place
// a user edits a cap: the panel must say plainly when the daemon's host holds
// nothing to it, and must not claim enforcement before it has been told.
func TestEnforceLabelTellsTheTruth(t *testing.T) {
	m := newLimitsModel(t)

	if got := m.enforceLabel(); !strings.Contains(got, "unknown") {
		t.Errorf("before attaching, enforcement is unknown, got %q", got)
	}
	m.enforce, m.enforceWhy = string(cgroup.ModeNone), "cgroup v2 is Linux-only"
	got := m.enforceLabel()
	if !strings.Contains(got, "NOT enforced") || !strings.Contains(got, "Linux-only") {
		t.Errorf("an unenforcing host should be named outright, got %q", got)
	}
	m.enforce, m.enforceWhy = string(cgroup.ModeCgroup), ""
	if got := m.enforceLabel(); got != "enforced by cgroup" {
		t.Errorf("an enforcing host should say so, got %q", got)
	}

	// The label reaches the screen, not just the helper.
	if !strings.Contains(m.panelConfigView(), "enforced by cgroup") {
		t.Error("the panel config should show how the caps are enforced")
	}
}

// TestWelcomeCarriesTheEnforcementMode checks the cockpit learns the mode from
// the daemon, since only the daemon's host knows whether the caps bite.
func TestWelcomeCarriesTheEnforcementMode(t *testing.T) {
	m := baseModel()
	m.applyEvent(proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion,
		Enforce: string(cgroup.ModeNone), EnforceWhy: "cgroup v2 is Linux-only"})
	if m.enforce != string(cgroup.ModeNone) || m.enforceWhy != "cgroup v2 is Linux-only" {
		t.Fatalf("the welcome should carry the mode and the reason, got %q/%q", m.enforce, m.enforceWhy)
	}
}

// TestPanelConfigEditsOnlyWhatIsOnScreen: e acts on a row of the tab being
// looked at, or it refuses — never on a row of the tab you came from.
//
// This is the bug the tabs walked into, and it is worth being precise about how
// it happened. The page's cursor is a page-wide row index that the tabs
// partition, and switching tabs moved it to the arriving tab's first row — but
// only when that tab HAD rows. A fleet with no agent profiles has an empty
// feedback tab, so arriving there from the limits tab left the cursor on `cpus`,
// and e opened the CPU limit's editor: a row on another tab, not on screen, that
// nobody had selected.
//
// Two things keep it shut. The cursor parks at the tab's first row whether or
// not the tab has any, and the clamp respects the tab rather than the page —
// clamping to the page length walked the cursor straight back onto `nofile`.
func TestPanelConfigEditsOnlyWhatIsOnScreen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("an empty tab refuses", func(t *testing.T) {
		// No tab the page ships with is empty any more — the feedback tab leads with
		// the fleet's own switch — so the case is driven through a tab emptied on
		// purpose. It is the state the guard exists for, and one a future tab (or a
		// profile list that can vanish under a reload) can still arrive in.
		m := baseModel()
		m = press(m, "ctrl+t", "P")
		m.panelTab = len(panelCfgTabs) // past the last tab: a range holding nothing
		m.cursor = firstLimitRow       // ... with the cursor left on another tab's row

		m = press(m, "e")
		if m.input != inputNone {
			t.Errorf("e with the cursor off the tab opened editor %v", m.input)
		}
		if !strings.Contains(m.status, "nothing to edit") {
			t.Errorf("e with the cursor off the tab should say so, got %q", m.status)
		}
	})

	t.Run("switching tabs takes the cursor with it", func(t *testing.T) {
		m := baseModel()
		m = press(m, "ctrl+t", "P")
		for range panelCfgTabs {
			m = press(m, "right")
			first, _ := m.panelTabRange()
			if m.cursor != first {
				t.Errorf("tab %d: the cursor should park at %d, got %d", m.panelTab, first, m.cursor)
			}
			m.clampCursor() // a snapshot, a resize — anything that re-clamps
			if m.cursor != first {
				t.Errorf("tab %d: the clamp pulled the cursor to %d", m.panelTab, m.cursor)
			}
		}
	})

	t.Run("every tab edits its own rows", func(t *testing.T) {
		m := baseModel()
		m.agents = map[string]config.AgentProfile{"claude": {Command: "claude"}}
		m = press(m, "ctrl+t", "P")
		for tab := range panelCfgTabs {
			first, end := m.panelTabRange()
			for row := first; row < end; row++ {
				m.cursor, m.input, m.status = row, inputNone, ""
				next := press(m, "e")
				switch tab {
				case 1: // the limits tab opens the limit editor for ITS row
					if next.input != inputLimit || next.limitRow != row {
						t.Errorf("tab %d row %d: e opened %v for row %d", tab, row, next.input, next.limitRow)
					}
				case 2: // the feedback tab cycles the profile on that row
					if !strings.Contains(next.status, "score feedback") {
						t.Errorf("tab %d row %d: e said %q", tab, row, next.status)
					}
				default: // the defaults tab: a text overlay or the agent picker
					if next.input == inputNone && next.mode != modeAgentPick {
						t.Errorf("tab %d row %d: e did nothing (%q)", tab, row, next.status)
					}
				}
			}
			m = press(m, "right")
		}
	})
}
