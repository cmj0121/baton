package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// typeInto feeds a string to the form one keystroke at a time, the way a person
// would, so the rune path is exercised rather than the field being assigned.
func typeInto(m tea.Model, s string) tea.Model {
	for _, r := range s {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func formKey(m tea.Model, k tea.KeyType) tea.Model {
	m, _ = m.Update(tea.KeyMsg{Type: k})
	return m
}

func TestRemoteFormCollectsAnAddressAndAPasskey(t *testing.T) {
	m := NewRemoteForm("", "")
	m = typeInto(m, "cmj@laptop.lan")
	m = formKey(m, tea.KeyEnter) // address accepted, cursor moves to the passkey
	m = typeInto(m, "K7m2QxP9")
	m = formKey(m, tea.KeyEnter) // submit

	got, ok := RemoteResult(m)
	if !ok {
		t.Fatal("the form should report a submitted target")
	}
	if got.Address != "cmj@laptop.lan" || got.Passkey != "K7m2QxP9" {
		t.Fatalf("RemoteResult = %+v", got)
	}
}

func TestRemoteFormEscCancels(t *testing.T) {
	m := NewRemoteForm("", "")
	m = typeInto(m, "laptop.lan")
	m = formKey(m, tea.KeyEsc)
	if _, ok := RemoteResult(m); ok {
		t.Fatal("esc should cancel rather than submit")
	}
}

func TestRemoteFormNeedsBothFields(t *testing.T) {
	m := NewRemoteForm("", "")
	m = formKey(m, tea.KeyEnter) // empty address
	if _, ok := RemoteResult(m); ok {
		t.Fatal("an empty address must not submit")
	}
	if !strings.Contains(m.(remoteFormModel).problem, "address") {
		t.Fatalf("the form should say what is missing, got %q", m.(remoteFormModel).problem)
	}

	m = typeInto(m, "laptop.lan")
	m = formKey(m, tea.KeyEnter) // to the passkey
	m = formKey(m, tea.KeyEnter) // empty passkey
	if _, ok := RemoteResult(m); ok {
		t.Fatal("an empty passkey must not submit")
	}
	if !strings.Contains(m.(remoteFormModel).problem, "passkey") {
		t.Fatalf("the form should name the passkey, got %q", m.(remoteFormModel).problem)
	}
}

func TestRemoteFormRetryKeepsTheAddressAndLandsOnThePasskey(t *testing.T) {
	m := NewRemoteForm("cmj@laptop.lan", "wrong passkey")
	f := m.(remoteFormModel)
	if f.focus != 1 {
		t.Fatalf("a retry should open on the passkey, focus=%d", f.focus)
	}
	if !strings.Contains(f.View(), "wrong passkey") {
		t.Fatal("the failure that sent the person back should be shown")
	}

	// Typing clears the message rather than leaving a stale complaint on screen.
	m = typeInto(m, "x")
	if got := m.(remoteFormModel).problem; got != "" {
		t.Fatalf("typing should clear the message, got %q", got)
	}
}

func TestRemoteFormEditingKeys(t *testing.T) {
	m := NewRemoteForm("", "")
	m = typeInto(m, "labtop")
	m = formKey(m, tea.KeyBackspace)
	m = typeInto(m, "op.lan")
	if got := m.(remoteFormModel).address; got != "labtoop.lan" {
		t.Fatalf("address = %q", got)
	}

	// ctrl+u clears the focused field only.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := m.(remoteFormModel).address; got != "" {
		t.Fatalf("ctrl+u should clear the field, got %q", got)
	}

	// tab moves between the fields either way.
	m = formKey(m, tea.KeyTab)
	if got := m.(remoteFormModel).focus; got != 1 {
		t.Fatalf("tab moved to %d", got)
	}
	m = formKey(m, tea.KeyShiftTab)
	if got := m.(remoteFormModel).focus; got != 0 {
		t.Fatalf("shift-tab moved to %d", got)
	}
}

func TestRemoteFormBackspaceOnAnEmptyFieldIsSafe(t *testing.T) {
	m := NewRemoteForm("", "")
	m = formKey(m, tea.KeyBackspace)
	if got := m.(remoteFormModel).address; got != "" {
		t.Fatalf("address = %q", got)
	}
	if got := dropLastRune(""); got != "" {
		t.Fatalf("dropLastRune(\"\") = %q", got)
	}
	// A multi-byte character is deleted whole, not left as a broken fragment.
	if got := dropLastRune("主機"); got != "主" {
		t.Fatalf("dropLastRune(\"主機\") = %q", got)
	}
}

func TestRemoteFormViewShowsTheFieldsAndTheDefaults(t *testing.T) {
	m := NewRemoteForm("", "")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	out := m.View()
	for _, want := range []string{"ADDRESS", "PASSKEY", "port defaults to 22", "ssh", "attach"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the form should show %q:\n%s", want, out)
		}
	}
	if m.Init() != nil {
		t.Fatal("the form has nothing to do on Init")
	}
	// A message it does not care about leaves it alone.
	if _, cmd := m.Update(struct{}{}); cmd != nil {
		t.Fatal("an unrelated message should be ignored")
	}
}

func TestRemoteResultRejectsAnUnrelatedModel(t *testing.T) {
	if _, ok := RemoteResult(baseModel()); ok {
		t.Fatal("RemoteResult should only read its own model")
	}
}
