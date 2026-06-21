package tui

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/signals"
)

// baseModel is a ready-to-render model with no live client.
func baseModel() model {
	return model{
		width: 120, height: 40, now: time.Unix(0, 0), endpoint: "local",
		prefixKey: "ctrl+t", confirmClose: true,
		binds: append([]binding(nil), bindings...),
	}
}

func TestViewRendersEveryMode(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*model)
	}{
		{"grid", func(m *model) { m.fleet = sampleFleet()[:3] }},
		{"tree", func(m *model) { m.fleet = sampleFleet() }},
		{"tree-scroll", func(m *model) { m.fleet = sampleFleet(); m.height = 20; m.cursor = 6 }},
		{"empty", func(m *model) { m.fleet = nil }},
		{"keymap", func(m *model) { m.mode = modeKeyMap; m.fleet = sampleFleet()[:3] }},
		{"help-dashboard", func(m *model) { m.mode = modeHelp; m.helpFrom = modeDashboard; m.fleet = sampleFleet()[:3] }},
		{"help-group", func(m *model) { m.mode = modeHelp; m.helpFrom = modeGroupZoom; m.groupName = "api" }},
		{"help-zoom", func(m *model) { m.mode = modeHelp; m.helpFrom = modeZoom }},
		{"keymap-edit-prefix", func(m *model) { m.mode = modeKeyMap; m.editing = true; m.editIdx = editPrefix }},
		{"keymap-edit-binding", func(m *model) { m.mode = modeKeyMap; m.editing = true; m.editIdx = 0; m.cursor = 1 }},
		{"keymap-setting-off", func(m *model) { m.mode = modeKeyMap; m.confirmClose = false; m.cursor = len(bindings) + 1 }},
		{"panel-config", func(m *model) { m.mode = modePanelConfig; m.shellPath = "/bin/zsh" }},
		{"panel-config-default", func(m *model) { m.mode = modePanelConfig }},
		{"signal-picker", func(m *model) {
			m.mode = modeSignal
			m.signalTargets = []string{"1"}
			m.signalScope = "api (3 panels)"
		}},
		{"signal-picker-other", func(m *model) {
			m.mode = modeSignal
			m.signalTargets = []string{"1"}
			m.signalScope = "shell #1"
			m.signalCursor = len(signals.Choices) // cursor on the other… row
		}},
		{"signal-other-input", func(m *model) {
			m.mode = modeSignal
			m.input = inputSignalName
			m.inputBuf = "WINCH"
			m.signalScope = "shell #1"
		}},
		{"input-shell", func(m *model) { m.input = inputShellPath; m.inputBuf = "/bin/zsh" }},
		{"input-new-panel", func(m *model) { m.input = inputNewPanelCmd; m.inputBuf = "/bin/sh" }},
		{"zoom", func(m *model) { m.mode = modeZoom; m.zoomTitle = "shell #1" }},
		{"prefix-armed", func(m *model) { m.fleet = sampleFleet()[:3]; m.prefix = true }},
		{"error", func(m *model) { m.fleet = sampleFleet()[:3]; m.status = "error: boom" }},
		{"narrow", func(m *model) { m.fleet = sampleFleet(); m.width = 40 }},
		{"quitting-detach", func(m *model) { m.quitting = true }},
		{"quitting-restart", func(m *model) { m.quitting = true; m.restart = true }},
		{"zero-size", func(m *model) { m.width = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseModel()
			tc.mut(&m)
			if m.View() == "" && m.width != 0 {
				t.Fatal("non-zero-size view should render something")
			}
		})
	}
}

func TestPreviewAcrossCursor(t *testing.T) {
	// Walk the cursor over the whole fleet so every preview-pane branch renders.
	m := baseModel()
	m.fleet = sampleFleet()
	m.height = 40
	for i := range m.fleet {
		m.cursor = i
		_ = m.View()
	}
}

func TestSmallHelpers(t *testing.T) {
	if keyLabel("ctrl+t") != "C-t" || keyLabel("alt+x") != "M-x" || keyLabel("p") != "p" {
		t.Fatal("keyLabel mismatch")
	}
	if onOff(true) != "ON " || onOff(false) != "OFF" {
		t.Fatal("onOff mismatch")
	}
	if truncate("hello", 0) != "hello" || truncate("hello", 1) != "…" || truncate("hello", 3) != "he…" {
		t.Fatalf("truncate mismatch: %q", truncate("hello", 3))
	}
	if spaced("AB") != "A B" {
		t.Fatalf("spaced mismatch: %q", spaced("AB"))
	}
}

func TestApplyEventBranches(t *testing.T) {
	m := &model{endpoint: "local"}
	m.applyEvent(proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion})
	if !strings.Contains(m.status, "attached · local") {
		t.Fatalf("welcome status = %q", m.status)
	}
	m.applyEvent(proto.ServerMsg{Type: "welcome", Version: "baton/999"})
	if !strings.HasPrefix(m.status, "error") {
		t.Fatalf("version mismatch should error, got %q", m.status)
	}
	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: []proto.Panel{{ID: "1", Kind: "shell", State: "idle"}}})
	if len(m.fleet) != 1 || m.fleet[0].State != panel.Idle {
		t.Fatalf("panels not applied: %+v", m.fleet)
	}
	m.applyEvent(proto.ServerMsg{Type: "error", Error: "boom"})
	if m.status != "error: boom" {
		t.Fatalf("error status = %q", m.status)
	}
}

// TestApplyTelemetryMergesInPlace checks that a telemetry refresh updates live
// fields of existing panels by id, but never adds or removes panels — so a stale
// telemetry tick cannot resurrect a panel a structural snapshot already dropped.
func TestApplyTelemetryMergesInPlace(t *testing.T) {
	m := baseModel()
	m.fleet = []panel.Panel{{ID: "1", Kind: panel.Agent, State: panel.Running, Activity: "running · 1s"}}

	// A telemetry tick refreshes panel 1 and also carries a panel 2 the fleet no
	// longer holds (e.g. built just before a close landed on the panels channel).
	m.applyTelemetry(proto.ServerMsg{Type: "telemetry", Panels: []proto.Panel{
		{ID: "1", State: "attention", Activity: "needs you · 12s", Spark: "▂▃▅▇"},
		{ID: "2", State: "running", Activity: "running · 3s"},
	}})

	if len(m.fleet) != 1 {
		t.Fatalf("telemetry must not change the panel set, got %d panels", len(m.fleet))
	}
	if m.fleet[0].State != panel.Attention || m.fleet[0].Activity != "needs you · 12s" || m.fleet[0].Spark != "▂▃▅▇" {
		t.Fatalf("telemetry should refresh live fields in place, got %+v", m.fleet[0])
	}
}

func TestUpdateBranches(t *testing.T) {
	m := baseModel()

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = m2.(model)
	if m.width != 80 || m.height != 24 {
		t.Fatal("window size not applied")
	}

	m2, _ = m.Update(tickMsg(time.Unix(5, 0)))
	m = m2.(model)
	if m.now.Unix() != 5 {
		t.Fatal("tick not applied")
	}

	m2, _ = m.Update(key("ctrl+t"))
	m = m2.(model)
	if !m.prefix {
		t.Fatal("key event not routed to handleKey")
	}

	m2, _ = m.Update(connClosedMsg{})
	m = m2.(model)
	if m.quitting {
		t.Fatal("conn-closed should not quit — it alerts and stays up")
	}
	if !m.backendDown {
		t.Fatal("conn-closed should flag the backend as down")
	}

	if _, cmd := m.Update(nil); cmd != nil {
		t.Fatal("unknown message should be a no-op")
	}
}

func TestRunActionsWithoutClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := func() model {
		return model{mode: modeDashboard, fleet: sampleFleet(), prefixKey: "ctrl+t",
			binds: append([]binding(nil), bindings...), confirmClose: true}
	}

	if m := press(base(), "ctrl+t", "d"); m.mode != modeDashboard {
		t.Fatal("C-t d should show the dashboard")
	}
	// ? opens the key map (single key), esc leaves it.
	m := press(base(), "?")
	if m.mode != modeHelp {
		t.Fatal("? should open the key list")
	}
	if m := press(base(), "S", "y"); !m.restart {
		t.Fatal("S then y should request a restart")
	}
	if m := press(base(), "q"); !m.quitting {
		t.Fatal("q should quit")
	}
	if m := press(base(), "ctrl+t", "Z"); !strings.Contains(m.status, "no escape") {
		t.Fatalf("an unknown escape status = %q", m.status)
	}
	press(base(), "ctrl+t", "ctrl+t") // prefix then a non-escape: no-op
	// Ctrl-C and Ctrl-E are captured on the dashboard: they never quit, just
	// point the user at the detach binding.
	for _, k := range []string{"ctrl+c", "ctrl+e"} {
		m := press(base(), k)
		if m.quitting {
			t.Fatalf("%s must not quit the dashboard", k)
		}
		if !strings.Contains(m.status, "disabled") {
			t.Fatalf("%s should hint that exit is disabled, got %q", k, m.status)
		}
	}

	// dashboard cursor movement, then enter zooms the selected panel.
	nav := press(base(), "down", "up", "left", "right", "j", "k", "h", "l", "tab", "shift+tab", "enter")
	if !strings.Contains(nav.status, "zoomed") {
		t.Fatalf("enter on a card should zoom, status=%q", nav.status)
	}

	// esc leaves the key map.
	if m := press(press(base(), "?"), "esc"); m.mode != modeDashboard {
		t.Fatal("esc should return to the dashboard")
	}

	// editing a binding from a nil-binds model exercises copy-on-write.
	cow := model{mode: modeKeyMap, fleet: sampleFleet(), prefixKey: "ctrl+t", cursor: 1}
	cow = press(cow, "e", "z")
	if cow.binds == nil || cow.binds[0].key != "z" {
		t.Fatal("ensureBinds copy-on-write failed")
	}
}

func TestPanelConfigEditsShell(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := model{fleet: sampleFleet(), prefixKey: "ctrl+t",
		binds: append([]binding(nil), bindings...), confirmClose: true}

	// C-t P opens the panel config tab; bare P no longer does.
	if got := press(m, "P"); got.mode == modePanelConfig {
		t.Fatal("bare P should no longer open panel config")
	}
	m = press(m, "ctrl+t", "P")
	if m.mode != modePanelConfig {
		t.Fatalf("C-t P should open panel config, mode=%v", m.mode)
	}

	// e opens the shell text-input overlay; type a path with a correction.
	m = press(m, "e")
	if m.input != inputShellPath {
		t.Fatal("e should open the shell input overlay")
	}
	for _, r := range "/bin/zsX" { // typo the last char...
		m = press(m, string(r))
	}
	m = press(m, "backspace") // ...delete it...
	m = press(m, "h")         // ...and fix it.
	m = press(m, "enter")

	if m.input != inputNone {
		t.Fatal("enter should close the overlay")
	}
	if m.shellPath != "/bin/zsh" {
		t.Fatalf("shellPath = %q, want /bin/zsh", m.shellPath)
	}
	if got := loadPrefs().shellPath; got != "/bin/zsh" {
		t.Fatalf("shell not persisted, got %q", got)
	}

	// esc cancels an edit without changing the value.
	m = press(m, "e")
	m = press(m, "x", "esc")
	if m.input != inputNone || m.shellPath != "/bin/zsh" {
		t.Fatalf("esc should cancel, input=%v shell=%q", m.input, m.shellPath)
	}
}

func TestNewPanelFormPrefills(t *testing.T) {
	m := model{fleet: sampleFleet(), prefixKey: "ctrl+t",
		binds: append([]binding(nil), bindings...), shellPath: "/bin/zsh"}

	// prefix+n opens the new-panel popup prefilled with the default shell.
	m = press(m, "c")
	if m.input != inputNewPanelCmd {
		t.Fatalf("prefix+n should open the new-panel input, got %v", m.input)
	}
	if m.inputBuf != "/bin/zsh" {
		t.Fatalf("popup should prefill the default shell, got %q", m.inputBuf)
	}

	// Edit /bin/zsh → /bin/bash and submit (no client: spawnPanel just sets
	// status).
	m = press(m, "backspace", "backspace", "backspace") // drop "zsh"
	m = press(m, "b", "a", "s", "h")
	m = press(m, "enter")
	if m.input != inputNone {
		t.Fatal("enter should close the popup")
	}
	if !strings.Contains(m.status, "spawning") || !strings.Contains(m.status, "/bin/bash") {
		t.Fatalf("spawn status = %q", m.status)
	}
}

// TestModelWithLiveServer drives New, Init, waitEvent, Update(eventMsg) and the
// client-backed actions against a real server over a socket.
func TestModelWithLiveServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	tm := New(c, "test") // New + loadPrefs
	_ = tm.Init()        // Init
	_ = tick()           // tick constructor
	m := tm.(model)

	// Pump the welcome + (empty) panels snapshot through Update.
	for i := 0; i < 2; i++ {
		msg := waitEvent(c.Events)() // waitEvent
		next, _ := m.Update(msg)     // Update eventMsg + applyEvent
		m = next.(model)
	}
	if len(m.fleet) != 0 {
		t.Fatalf("a fresh server should have no panels, got %d", len(m.fleet))
	}

	// Spawn a panel: runAction actNewPanel sends over the live socket; the
	// server broadcasts the updated snapshot.
	m = press(m, "p")
	if !strings.Contains(m.status, "spawning") {
		t.Fatalf("spawn status = %q", m.status)
	}
	next, _ := m.Update(waitEvent(c.Events)())
	m = next.(model)
	if len(m.fleet) != 1 {
		t.Fatalf("expected one real panel after spawn, got %d", len(m.fleet))
	}

	// Close it with the gate off: closeSelected sends panel.close.
	m.confirmClose = false
	m.cursor = 0
	before := len(m.fleet)
	m = press(m, "w")
	if len(m.fleet) != before-1 {
		t.Fatalf("close should drop a panel, %d -> %d", before, len(m.fleet))
	}
}

// TestPanelConfigEditsReplayBuffer drives the new replay-buffer row: navigate to
// it, edit it, and confirm the value persists to the config.
func TestPanelConfigEditsReplayBuffer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := model{prefixKey: "ctrl+t", binds: append([]binding(nil), bindings...)}

	m = press(m, "ctrl+t", "P")
	if m.mode != modePanelConfig {
		t.Fatalf("C-t P should open panel config, mode=%v", m.mode)
	}
	m = press(m, "down")
	if m.cursor != panelRowReplayKB {
		t.Fatalf("down should land on the replay row, cursor=%d", m.cursor)
	}
	m = press(m, "e")
	if m.input != inputReplayKB {
		t.Fatal("e should open the replay-buffer overlay")
	}
	for _, r := range "1024" {
		m = press(m, string(r))
	}
	m = press(m, "enter")
	if m.input != inputNone || m.replayKB != 1024 {
		t.Fatalf("enter should save 1024, input=%v replayKB=%d", m.input, m.replayKB)
	}
	if got := loadPrefs().replayKB; got != 1024 {
		t.Fatalf("replay-kb not persisted, got %d", got)
	}
}

// TestCommitReplayKB covers the value rules: blank resets to the default, a whole
// number sets it, and a non-number is rejected with the overlay kept open.
func TestCommitReplayKB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := model{replayKB: 256, prefixKey: "ctrl+t", binds: append([]binding(nil), bindings...)}

	if got := base.commitReplayKB("").replayKB; got != 0 {
		t.Fatalf("blank should reset to 0, got %d", got)
	}
	if got := base.commitReplayKB("512").replayKB; got != 512 {
		t.Fatalf("512 should set, got %d", got)
	}
	if m := base.commitReplayKB("nope"); m.replayKB != 256 || m.input != inputReplayKB {
		t.Fatalf("an invalid entry should keep the value and reopen the overlay, replayKB=%d input=%v", m.replayKB, m.input)
	}
	if m := base.commitReplayKB("-5"); m.replayKB != 256 || m.input != inputReplayKB {
		t.Fatalf("a negative entry should be rejected, replayKB=%d input=%v", m.replayKB, m.input)
	}
}

// TestKeyMapScrollsOnSmallScreen checks the key map windows its body on a short
// terminal: the selected row stays visible, off-window rows are clipped, a
// position counter appears, the legend stays pinned, and the box fits the height.
func TestKeyMapScrollsOnSmallScreen(t *testing.T) {
	total := len(bindings) + 1 + numSettings
	m := model{mode: modeKeyMap, width: 90, height: 16,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t",
		cursor: total - 1} // the last settings row (the bell toggle)

	view := m.keyMapView()

	if h := lipgloss.Height(view); h > m.height-1 {
		t.Fatalf("key map box height %d should fit within %d", h, m.height-1)
	}
	if !strings.Contains(view, settingLabel(settingBell)) {
		t.Fatal("the selected row should stay in view when scrolled")
	}
	if strings.Contains(view, "prefix · leader key") {
		t.Fatal("the prefix row should be scrolled off the top")
	}
	if !strings.Contains(view, fmt.Sprintf("%d/%d", total, total)) {
		t.Fatal("a clipped key map should show a position counter")
	}
	if !strings.Contains(view, "back") {
		t.Fatal("the legend should stay pinned below the scrolling body")
	}

	// A tall screen shows everything with no counter.
	full := model{mode: modeKeyMap, width: 90, height: 80,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}.keyMapView()
	if !strings.Contains(full, "prefix · leader key") || !strings.Contains(full, settingLabel(settingBell)) {
		t.Fatal("a tall screen should render the whole key map")
	}
	if strings.Contains(full, fmt.Sprintf("/%d", total)) {
		t.Fatal("an unclipped key map should not show a position counter")
	}
}

// TestHelpScrollsOnSmallScreen checks the read-only help list scrolls by the
// arrows on a short screen: it fits the height, shows a scroll hint, reveals the
// bottom when scrolled down, and clamps at both ends.
func TestHelpScrollsOnSmallScreen(t *testing.T) {
	m := model{mode: modeHelp, helpFrom: modeDashboard, width: 90, height: 14,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	top := m.helpView()
	if h := lipgloss.Height(top); h > m.height-1 {
		t.Fatalf("help box height %d should fit within %d", h, m.height-1)
	}
	if !strings.Contains(top, "↑↓ scroll") {
		t.Fatal("a clipped help list should show the scroll hint")
	}
	if !strings.Contains(top, "move") {
		t.Fatal("the top of the help list should be visible at offset 0")
	}

	// Scroll to the bottom: a late row appears and a top row scrolls off.
	for i := 0; i < 50; i++ {
		m.scrollHelp(1)
	}
	bottom := m.helpView()
	if !strings.Contains(bottom, "detach (server keeps running)") {
		t.Fatal("scrolling down should reveal the bottom of the help list")
	}
	if strings.Contains(bottom, "clear the selection") {
		t.Fatal("the top help rows should scroll off the window")
	}

	// Clamp: scrolling up at the top stays at 0.
	m.helpScroll = 0
	m.scrollHelp(-1)
	if m.helpScroll != 0 {
		t.Fatalf("scrolling up at the top should stay at 0, got %d", m.helpScroll)
	}
}

// TestHelpKeyColumnFits guards against an overcrowded key-hint cluster: the
// keycaps in a help row must fit the 20-cell key column so they never overflow
// into the description. The scroll row was the offender — four separate caps
// (S-↑ S-↓ C-PgUp C-PgDn) blew past the column — so the combined caps are
// checked directly here.
func TestHelpKeyColumnFits(t *testing.T) {
	const keyColWidth = 20
	kc := func(s string) string { return keycapStyle.Render(s) }
	clusters := []string{
		kc("S-←→"),                    // reorder
		kc("C-t") + " " + kc("["),     // scroll mode (leader)
		kc("hjkl") + " " + kc("↑↓←→"), // dashboard move
	}
	for _, c := range clusters {
		if w := lipgloss.Width(c); w > keyColWidth {
			t.Errorf("key cluster overflows the %d-cell column (%d): %q", keyColWidth, w, c)
		}
	}
}
