package tui

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
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
		{"grid", func(m *model) { m.fleet = panel.Mock()[:3] }},
		{"tree", func(m *model) { m.fleet = panel.Mock() }},
		{"tree-scroll", func(m *model) { m.fleet = panel.Mock(); m.height = 20; m.cursor = 6 }},
		{"empty", func(m *model) { m.fleet = nil }},
		{"keymap", func(m *model) { m.mode = modeKeyMap; m.fleet = panel.Mock()[:3] }},
		{"keymap-edit-prefix", func(m *model) { m.mode = modeKeyMap; m.editing = true; m.editIdx = editPrefix }},
		{"keymap-edit-binding", func(m *model) { m.mode = modeKeyMap; m.editing = true; m.editIdx = 0; m.cursor = 1 }},
		{"keymap-setting-off", func(m *model) { m.mode = modeKeyMap; m.confirmClose = false; m.cursor = len(bindings) + 1 }},
		{"panel-config", func(m *model) { m.mode = modePanelConfig; m.shellPath = "/bin/zsh" }},
		{"panel-config-default", func(m *model) { m.mode = modePanelConfig }},
		{"input-shell", func(m *model) { m.input = inputShellPath; m.inputBuf = "/bin/zsh" }},
		{"input-new-panel", func(m *model) { m.input = inputNewPanelCmd; m.inputBuf = "/bin/sh" }},
		{"prefix-armed", func(m *model) { m.fleet = panel.Mock()[:3]; m.prefix = true }},
		{"error", func(m *model) { m.fleet = panel.Mock()[:3]; m.status = "error: boom" }},
		{"narrow", func(m *model) { m.fleet = panel.Mock(); m.width = 40 }},
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
	// Walk the cursor over the whole fleet so every previewLines branch renders.
	m := baseModel()
	m.fleet = panel.Mock()
	m.height = 40
	for i := range m.fleet {
		m.cursor = i
		_ = m.View()
	}
}

func TestPreviewLinesEveryShape(t *testing.T) {
	shapes := []panel.Panel{
		{State: panel.Attention},
		{State: panel.Exited},
		{Kind: panel.Shell, State: panel.Running},
		{Kind: panel.Shell, State: panel.Idle},
		{Kind: panel.Agent, State: panel.Idle},
		{Kind: panel.Agent, State: panel.Running},
	}
	for _, p := range shapes {
		if len(previewLines(p)) == 0 {
			t.Fatalf("no preview lines for %+v", p)
		}
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
	for _, s := range []panel.State{panel.Attention, panel.Running, panel.Idle, panel.Spawning, panel.Exited} {
		if sparkFor(s) == "" {
			t.Fatalf("empty spark for %v", s)
		}
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
	if !m.quitting {
		t.Fatal("conn-closed should quit")
	}

	if _, cmd := m.Update(nil); cmd != nil {
		t.Fatal("unknown message should be a no-op")
	}
}

func TestRunActionsWithoutClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := func() model {
		return model{mode: modeDashboard, fleet: panel.Mock(), prefixKey: "ctrl+t",
			binds: append([]binding(nil), bindings...), confirmClose: true}
	}

	if m := press(base(), "ctrl+t", "d"); m.mode != modeDashboard {
		t.Fatal("prefix+d should show the dashboard")
	}
	// toggle into and back out of the key map
	m := press(base(), "ctrl+t", "k")
	if m.mode != modeKeyMap {
		t.Fatal("prefix+k should open the key map")
	}
	if m = press(m, "ctrl+t", "k"); m.mode != modeKeyMap {
		// after toggling back it returns to the dashboard
		if m.mode != modeDashboard {
			t.Fatalf("prefix+k should toggle back, mode=%v", m.mode)
		}
	}
	if m := press(base(), "ctrl+t", "S"); !m.restart {
		t.Fatal("prefix+S should request a restart")
	}
	if m := press(base(), "ctrl+t", "q"); !m.quitting {
		t.Fatal("prefix+q should quit")
	}
	if m := press(base(), "ctrl+t", "Z"); !strings.Contains(m.status, "no binding") {
		t.Fatalf("unknown binding status = %q", m.status)
	}
	press(base(), "ctrl+t", "ctrl+t") // prefix-prefix no-op
	if m := press(base(), "ctrl+c"); !m.quitting {
		t.Fatal("ctrl+c should quit")
	}

	// dashboard cursor movement and focus.
	nav := press(base(), "down", "up", "left", "right", "j", "k", "h", "l", "tab", "shift+tab", "enter")
	if !strings.Contains(nav.status, "focus") {
		t.Fatalf("enter on a card should focus, status=%q", nav.status)
	}

	// esc leaves the key map.
	if m := press(press(base(), "ctrl+t", "k"), "esc"); m.mode != modeDashboard {
		t.Fatal("esc should return to the dashboard")
	}

	// editing a binding from a nil-binds model exercises copy-on-write.
	cow := model{mode: modeKeyMap, fleet: panel.Mock(), prefixKey: "ctrl+t", cursor: 1}
	cow = press(cow, "e", "z")
	if cow.binds == nil || cow.binds[0].key != "z" {
		t.Fatal("ensureBinds copy-on-write failed")
	}
}

func TestPanelConfigEditsShell(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := model{fleet: panel.Mock(), prefixKey: "ctrl+t",
		binds: append([]binding(nil), bindings...), confirmClose: true}

	// prefix+P opens the panel config tab.
	m = press(m, "ctrl+t", "P")
	if m.mode != modePanelConfig {
		t.Fatalf("prefix+P should open panel config, mode=%v", m.mode)
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
	m := model{fleet: panel.Mock(), prefixKey: "ctrl+t",
		binds: append([]binding(nil), bindings...), shellPath: "/bin/zsh"}

	// prefix+n opens the new-panel popup prefilled with the default shell.
	m = press(m, "ctrl+t", "n")
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

	tm := New(c)  // New + loadPrefs
	_ = tm.Init() // Init
	_ = tick()    // tick constructor
	m := tm.(model)

	// Pump the welcome + panels snapshot through Update.
	for i := 0; i < 2; i++ {
		msg := waitEvent(c.Events)() // waitEvent
		next, _ := m.Update(msg)     // Update eventMsg + applyEvent
		m = next.(model)
	}
	if len(m.fleet) == 0 {
		t.Fatal("expected the server's seeded fleet")
	}

	// Spawn a panel: runAction actNewPanel sends over the live socket.
	m = press(m, "ctrl+t", "p")
	if !strings.Contains(m.status, "spawning") {
		t.Fatalf("spawn status = %q", m.status)
	}

	// Close a panel with the gate off: closeSelected sends panel.close.
	m.confirmClose = false
	m.cursor = 0
	before := len(m.fleet)
	m = press(m, "ctrl+t", "w")
	if len(m.fleet) != before-1 {
		t.Fatalf("close should drop a panel, %d -> %d", before, len(m.fleet))
	}
}
