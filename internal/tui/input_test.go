package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// typeInput feeds one key event to the input overlay and returns the next model.
func typeInput(m model, k tea.KeyMsg) model {
	next, _ := m.handleInput(k)
	return next.(model)
}

// TestHandleInputPlainRunes is the baseline: ordinary printable typing (including
// a wide CJK glyph) lands in the buffer verbatim.
func TestHandleInputPlainRunes(t *testing.T) {
	m := model{input: inputDispatch}
	m = typeInput(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")})
	m = typeInput(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("字")}) // a wide CJK glyph
	if m.inputBuf != "ab字" {
		t.Fatalf("plain runes should append verbatim, inputBuf=%q", m.inputBuf)
	}
}

// TestHandleInputIgnoresAltChord proves an Alt/Meta chord (e.g. Alt+f) is treated
// as a shortcut and does not leak its base rune into the field. The bug was that
// KeyRunes appended regardless of k.Alt, so any Meta chord typed over an open
// overlay dropped a stray character into the buffer.
func TestHandleInputIgnoresAltChord(t *testing.T) {
	m := model{input: inputDispatch, inputBuf: "hi"}
	m = typeInput(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	if m.inputBuf != "hi" {
		t.Fatalf("an Alt chord must not append to the field, inputBuf=%q", m.inputBuf)
	}
}

// TestHandleInputFiltersPastedControls proves a bracketed paste carrying newlines,
// a tab, a raw ESC, and a bare control byte keeps only the printable text: none of
// the control bytes reach the buffer (and so never render to the real terminal).
func TestHandleInputFiltersPastedControls(t *testing.T) {
	m := model{input: inputDispatch}
	esc, bel := string(rune(0x1b)), string(rune(0x07))
	paste := "line1\nline2\tend" + esc + "[31m" + bel + "!"
	m = typeInput(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	if want := "line1line2end[31m!"; m.inputBuf != want {
		t.Fatalf("paste should keep only printable runes, inputBuf=%q want %q", m.inputBuf, want)
	}
	if strings.ContainsAny(m.inputBuf, "\n\t"+esc+bel) {
		t.Fatalf("control bytes leaked into the buffer: %q", m.inputBuf)
	}
}

// TestPrintableRunes unit-tests the filter directly: printable runes (incl. CJK)
// survive, tab and every control / non-printable rune is dropped. The runes are the
// decoded values a KeyMsg carries (a C1 control arrives as U+009B, not a raw byte),
// so the control cases are built from explicit rune values.
func TestPrintableRunes(t *testing.T) {
	ctl := func(r rune) string { return "a" + string(r) + "b" }
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"münchen café", "münchen café"},
		{ctl('\t'), "ab"},
		{ctl('\n'), "ab"},
		{ctl('\r'), "ab"},
		{ctl(0x00), "ab"},                     // NUL
		{ctl(0x7f), "ab"},                     // DEL
		{ctl(0x9b), "ab"},                     // C1 CSI introducer
		{string(rune(0x1b)) + "[0mx", "[0mx"}, // ESC dropped, the now-inert rest kept
	}
	for _, c := range cases {
		if got := printableRunes([]rune(c.in)); got != c.want {
			t.Errorf("printableRunes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
