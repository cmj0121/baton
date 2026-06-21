package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/signals"
)

// otherSignalKey opens the free-form entry for a signal outside the shortcut set.
const otherSignalKey = "o"

// signalRows is how many rows the picker shows: every shortcut plus the "other…"
// entry, which the cursor lands on last.
func signalRows() int { return len(signals.Choices) + 1 }

// openSignalPicker opens the send-signal overlay aimed at ids, labelled scope,
// remembering from so esc (or a send) returns there. A no-op with a status when
// there is nothing live to target.
func (m model) openSignalPicker(from mode, ids []string, scope string) model {
	if len(ids) == 0 {
		m.status = "no live panel to signal"
		return m
	}
	m.signalFrom = from
	m.signalTargets = ids
	m.signalScope = scope
	m.signalCursor = 0
	m.mode = modeSignal
	m.status = "send signal to " + scope + " · pick one · esc cancels"
	return m
}

// handleSignalKey drives the picker: a signal hotkey (or enter on the cursor row)
// sends that signal and closes the overlay, ↑↓ move the cursor, o (or the last
// row) opens free-form entry, and esc cancels. Any other key is ignored so a
// stray press never fires a signal or drops out. There is no confirmation — the
// commit is the keypress.
func (m model) handleSignalKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = m.signalFrom
		m.status = "signal cancelled"
		return m, nil
	// Cursor nav is arrows only — j/k are free for nothing here, but a letter like
	// k is a signal hotkey (SIGKILL), so the hotkeys must own the whole alphabet.
	case "up":
		m.signalCursor = wrapIndex(m.signalCursor, -1, signalRows())
		return m, nil
	case "down":
		m.signalCursor = wrapIndex(m.signalCursor, 1, signalRows())
		return m, nil
	case "enter":
		if m.signalCursor >= len(signals.Choices) {
			return m.openOtherSignal(), nil
		}
		return m.sendSignal(signals.Choices[m.signalCursor].Name), nil
	case otherSignalKey:
		return m.openOtherSignal(), nil
	}
	for _, s := range signals.Choices {
		if key == s.Key {
			return m.sendSignal(s.Name), nil
		}
	}
	return m, nil // stay in the picker on any other key
}

// sendSignal fires the named signal at the remembered targets and closes the
// picker, returning to the view it was opened from.
func (m model) sendSignal(name string) model {
	m.sendf(proto.Command{Action: "panel.signal", IDs: m.signalTargets, Signal: name})
	m.mode = m.signalFrom
	m.status = fmt.Sprintf("sent %s to %s", name, m.signalScope)
	return m
}

// openOtherSignal opens the free-form field for a signal name or number outside
// the shortcut set. The picker's targets are remembered, so the commit knows who
// to signal; esc on the field falls back to the picker.
func (m model) openOtherSignal() model {
	m.input = inputSignalName
	m.inputBuf = ""
	m.status = "signal name or number (e.g. WINCH, TSTP, 28) · enter sends"
	return m
}

// commitOtherSignal validates a hand-typed signal and sends it; an unknown token
// keeps the field open on the attempt rather than firing something unintended.
func (m model) commitOtherSignal(token string) (tea.Model, tea.Cmd) {
	if !signals.Valid(token) {
		m.input = inputSignalName // reopen on the bad entry
		m.status = fmt.Sprintf("unknown signal %q · try a name or number", token)
		return m, nil
	}
	return m.sendSignal(token), nil
}

// signalPickerView renders the picker as a centred popup: one keycap-led row per
// signal with a cursor caret, an "other…" entry, and a caption naming the target
// and clarifying that these reach the panel's process, not baton itself.
func (m model) signalPickerView() string {
	kc := func(s string) string { return keycapStyle.Render(s) }
	nameStyle := lipgloss.NewStyle().Foreground(colCyan).Bold(true).Width(9)
	keyCol := lipgloss.NewStyle().Width(6)
	caret := func(on bool) string {
		if on {
			return lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ ")
		}
		return "  "
	}

	rows := []string{
		sectionStyle.Render(spaced("SEND SIGNAL")),
		"",
		mutedStyle.Render("to " + m.signalScope),
		"",
	}
	for i, s := range signals.Choices {
		rows = append(rows, caret(m.signalCursor == i)+keyCol.Render(kc(s.Key))+nameStyle.Render(s.Name)+mutedStyle.Render(s.Desc))
	}
	otherSel := m.signalCursor >= len(signals.Choices)
	rows = append(rows, caret(otherSel)+keyCol.Render(kc(otherSignalKey))+nameStyle.Render("other…")+mutedStyle.Render("any name or number"))

	legendKey := lipgloss.NewStyle().Foreground(colCyan).Bold(true)
	legend := legendKey.Render("↑↓") + mutedStyle.Render(" move") + "   " +
		legendKey.Render("enter") + mutedStyle.Render(" send") + "   " +
		legendKey.Render("esc") + mutedStyle.Render(" cancel")
	rows = append(rows, "",
		mutedStyle.Render("delivered to the panel's process group · "+keyLabel(m.effPrefix())+" R reloads baton"),
		"", legend)
	return configBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
}
