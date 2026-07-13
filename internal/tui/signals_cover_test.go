package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/signals"
)

// signalModel opens the picker aimed at one target, wired to a recording client.
func signalModel(t *testing.T) (model, <-chan proto.Command) {
	t.Helper()
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m = m.openSignalPicker(modeDashboard, []string{"a1"}, "api (1 panel)")
	if m.mode != modeSignal {
		t.Fatalf("openSignalPicker should switch to modeSignal, got %v", m.mode)
	}
	return m, cmds
}

// TestSignalRowsCountsChoices: the picker shows every shortcut plus the other row.
func TestSignalRowsCountsChoices(t *testing.T) {
	if got := signalRows(); got != len(signals.Choices)+1 {
		t.Fatalf("signalRows = %d, want %d", got, len(signals.Choices)+1)
	}
}

// TestSignalKeyNavWraps: ↑/↓ move the cursor and wrap around the whole list
// (including the trailing other… row).
func TestSignalKeyNavWraps(t *testing.T) {
	m, _ := signalModel(t)

	nm, _ := m.handleSignalKey("down")
	m = nm.(model)
	if m.signalCursor != 1 {
		t.Fatalf("down should move to row 1, got %d", m.signalCursor)
	}
	nm, _ = m.handleSignalKey("up")
	m = nm.(model)
	nm, _ = m.handleSignalKey("up") // wrap off the top to the last (other…) row
	m = nm.(model)
	if m.signalCursor != signalRows()-1 {
		t.Fatalf("up off the top should wrap to the last row, got %d", m.signalCursor)
	}
}

// TestSignalKeyEnterSends: enter on a signal row sends that signal. Name kept
// short for the recording server's unix-socket path limit.
func TestSignalKeyEnterSends(t *testing.T) {
	m, cmds := signalModel(t)
	nm, _ := m.handleSignalKey("down") // highlight the second choice (SIGTERM)
	m = nm.(model)
	nm, _ = m.handleSignalKey("enter")
	m = nm.(model)
	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.signal" })
	if got.Signal != signals.Choices[1].Name {
		t.Fatalf("enter should send the highlighted signal %q, got %q", signals.Choices[1].Name, got.Signal)
	}
	if m.mode != modeDashboard {
		t.Fatalf("sending should close the picker, got %v", m.mode)
	}
}

// TestSignalKeyEnterOther: enter on the trailing row opens free-form entry
// instead of sending. Name kept short for the unix-socket path limit.
func TestSignalKeyEnterOther(t *testing.T) {
	m, _ := signalModel(t)
	m.signalCursor = len(signals.Choices) // the other… row
	nm, _ := m.handleSignalKey("enter")
	m = nm.(model)
	if m.input != inputSignalName {
		t.Fatalf("enter on other… should open the free-form field, got input=%v", m.input)
	}
}

// TestSignalKeyStrayIgnored: an unbound key keeps the picker open.
func TestSignalKeyStrayIgnored(t *testing.T) {
	m, _ := signalModel(t)
	nm, _ := m.handleSignalKey("z")
	m = nm.(model)
	if m.mode != modeSignal {
		t.Fatalf("a stray key should keep the picker open, got %v", m.mode)
	}
}
