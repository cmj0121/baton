package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
)

// The plugin command picker (C-t c): a centred list of the commands a Lua plugin
// registered with baton.command, pushed to the cockpit on the config snapshot. Enter
// runs the highlighted command via command.run; the daemon dispatches it on the Lua
// worker and any fleet change broadcasts back the normal way.

// openCommandPicker opens the picker, remembering from so esc returns there. A no-op
// with a hint when no plugin has registered a command.
func (m model) openCommandPicker(from mode) model {
	if len(m.pluginCommands) == 0 {
		m.status = "no plugin commands · add baton.command in " + pluginHint()
		return m
	}
	m.commandFrom = from
	m.commandCursor = 0
	m.mode = modeCommand
	m.status = "run a plugin command · enter runs · esc cancels"
	return m
}

// handleCommandKey drives the picker: ↑↓ (or j/k) move, enter runs the highlighted
// command, esc cancels. Any other key is ignored so a stray press never fires.
func (m model) handleCommandKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = m.commandFrom
		m.status = "command cancelled"
		return m, nil
	case "up", "k":
		m.commandCursor = wrapIndex(m.commandCursor, -1, len(m.pluginCommands))
		return m, nil
	case "down", "j":
		m.commandCursor = wrapIndex(m.commandCursor, 1, len(m.pluginCommands))
		return m, nil
	case "enter":
		if m.commandCursor < 0 || m.commandCursor >= len(m.pluginCommands) {
			return m, nil
		}
		return m.runPluginCommand(m.pluginCommands[m.commandCursor].Name), nil
	}
	return m, nil
}

// runPluginCommand asks the daemon to run the named command and closes the picker,
// returning to the view it was opened from.
func (m model) runPluginCommand(name string) model {
	m.sendf(proto.Command{Action: "command.run", Name: name})
	m.mode = m.commandFrom
	m.status = "ran " + name
	return m
}

// commandPickerView renders the picker as a centred popup: one row per command with
// its description and a cursor caret.
func (m model) commandPickerView() string {
	nameStyle := lipgloss.NewStyle().Foreground(colCyan).Bold(true).Width(20)
	caret := func(on bool) string {
		if on {
			return lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ ")
		}
		return "  "
	}

	rows := []string{
		sectionStyle.Render(spaced("PLUGIN COMMANDS")),
		"",
	}
	for i, c := range m.pluginCommands {
		rows = append(rows, caret(m.commandCursor == i)+nameStyle.Render(c.Name)+mutedStyle.Render(c.Desc))
	}

	rows = append(rows, "", mutedStyle.Render("registered by your Lua plugin · "+keyLabel(m.effPrefix())+" R reloads it"), "",
		legend("↑↓", "move", "enter", "run", "esc", "cancel"))
	return configBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// pluginHint is the human path to the plugin file, for the empty-picker nudge.
func pluginHint() string { return "$HOME/.baton/plug-in.lua" }
