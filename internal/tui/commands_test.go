package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// withCommands returns a base model carrying two registered plugin commands.
func withCommands() model {
	m := baseModel()
	m.pluginCommands = []proto.PluginCommand{
		{Name: "deploy", Desc: "ship it"},
		{Name: "rollback", Desc: "undo it"},
	}
	return m
}

// TestCommandPickerEmptyIsNoOp: with no registered commands the picker refuses to
// open and nudges the user toward the plugin file.
func TestCommandPickerEmptyIsNoOp(t *testing.T) {
	m := baseModel()
	m = m.openCommandPicker(modeDashboard)
	if m.mode == modeCommand {
		t.Fatal("the picker must not open without any plugin commands")
	}
	if !strings.Contains(m.status, pluginHint()) {
		t.Fatalf("empty picker should point at the plugin file, got %q", m.status)
	}
}

// TestCommandPickerOpens remembers its origin and starts at the top.
func TestCommandPickerOpens(t *testing.T) {
	m := withCommands()
	m = m.openCommandPicker(modeZoom)
	if m.mode != modeCommand {
		t.Fatalf("openCommandPicker should switch to modeCommand, got %v", m.mode)
	}
	if m.commandFrom != modeZoom {
		t.Fatalf("the picker should remember its origin, got %v", m.commandFrom)
	}
	if m.commandCursor != 0 {
		t.Fatalf("the cursor should start at the top, got %d", m.commandCursor)
	}
}

// TestCommandKeyNavWraps: ↑/↓ (and j/k) wrap around the list.
func TestCommandKeyNavWraps(t *testing.T) {
	m := withCommands()
	m = m.openCommandPicker(modeDashboard)

	nm, _ := m.handleCommandKey("down")
	m = nm.(model)
	if m.commandCursor != 1 {
		t.Fatalf("down should move to row 1, got %d", m.commandCursor)
	}
	nm, _ = m.handleCommandKey("j")
	m = nm.(model)
	if m.commandCursor != 0 {
		t.Fatalf("j past the end should wrap to row 0, got %d", m.commandCursor)
	}
	nm, _ = m.handleCommandKey("up")
	m = nm.(model)
	if m.commandCursor != 1 {
		t.Fatalf("up should wrap to the last row, got %d", m.commandCursor)
	}
	nm, _ = m.handleCommandKey("k")
	m = nm.(model)
	if m.commandCursor != 0 {
		t.Fatalf("k should move back to row 0, got %d", m.commandCursor)
	}
}

// TestCommandKeyEsc closes the picker back to its origin.
func TestCommandKeyEsc(t *testing.T) {
	m := withCommands()
	m = m.openCommandPicker(modeZoom)
	nm, _ := m.handleCommandKey("esc")
	m = nm.(model)
	if m.mode != modeZoom {
		t.Fatalf("esc should return to the origin view, got %v", m.mode)
	}
	if m.status != "command cancelled" {
		t.Fatalf("esc should report a cancel, got %q", m.status)
	}
}

// TestCommandKeyEnterRuns dispatches command.run for the highlighted row and
// closes the picker.
func TestCommandKeyEnterRuns(t *testing.T) {
	c, cmds := recordingServer(t)
	m := withCommands()
	m.client = c
	m = m.openCommandPicker(modeDashboard)

	nm, _ := m.handleCommandKey("down") // highlight "rollback"
	m = nm.(model)
	nm, _ = m.handleCommandKey("enter")
	m = nm.(model)

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "command.run" })
	if got.Name != "rollback" {
		t.Fatalf("enter should run the highlighted command, got %q", got.Name)
	}
	if m.mode != modeDashboard {
		t.Fatalf("running a command should close the picker, got %v", m.mode)
	}
	if !strings.Contains(m.status, "rollback") {
		t.Fatalf("status should name the command that ran, got %q", m.status)
	}
}

// TestCommandKeyEnterOutOfRange is a no-op when the cursor is off the list.
func TestCommandKeyEnterOutOfRange(t *testing.T) {
	m := withCommands()
	m = m.openCommandPicker(modeDashboard)
	m.commandCursor = 99 // out of range
	nm, _ := m.handleCommandKey("enter")
	m = nm.(model)
	if m.mode != modeCommand {
		t.Fatalf("an out-of-range enter should stay in the picker, got %v", m.mode)
	}
}

// TestCommandKeyIgnoresStray leaves the picker untouched on an unbound key.
func TestCommandKeyIgnoresStray(t *testing.T) {
	m := withCommands()
	m = m.openCommandPicker(modeDashboard)
	nm, _ := m.handleCommandKey("z")
	m = nm.(model)
	if m.mode != modeCommand || m.commandCursor != 0 {
		t.Fatalf("a stray key should be ignored, got mode=%v cursor=%d", m.mode, m.commandCursor)
	}
}

// TestCommandPickerView lists every command with its description.
func TestCommandPickerView(t *testing.T) {
	m := withCommands()
	m = m.openCommandPicker(modeDashboard)
	out := m.commandPickerView()
	// The section title is letter-spaced by spaced(); assert on the row content.
	for _, want := range []string{"deploy", "ship it", "rollback", "undo it"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the picker view should contain %q, got:\n%s", want, out)
		}
	}
}
