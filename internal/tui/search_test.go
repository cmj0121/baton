package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	vt "github.com/charmbracelet/x/vt"
)

// TestStripANSI drops escapes and keeps the visible text.
func TestStripANSI(t *testing.T) {
	cases := map[string]string{
		"plain":                 "plain",
		"\x1b[31mred\x1b[0m":    "red",
		"a\x1b[1mb\x1b[22mc":    "abc",
		"\x1b]0;title\x07after": "after", // an OSC sequence is consumed whole
	}
	for in, want := range cases {
		if got := stripANSI(in); got != want {
			t.Errorf("stripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestClipCells trims a plain line to a cell budget without adding escapes.
func TestClipCells(t *testing.T) {
	if got := clipCells("abcdef", 3); got != "abc" {
		t.Errorf("clipCells = %q, want abc", got)
	}
	if got := clipCells("abc", 9); got != "abc" {
		t.Errorf("clipCells over-budget = %q, want abc", got)
	}
	if got := clipCells("abc", 0); got != "" {
		t.Errorf("clipCells zero width = %q, want empty", got)
	}
}

// TestHighlightLine wraps each occurrence: reverse video for the current hit,
// underline otherwise.
func TestHighlightLine(t *testing.T) {
	reO, _ := compileSearch("o")
	cur := highlightLine("hello world", reO, 20, true)
	if !strings.Contains(cur, "\x1b[7mo\x1b[27m") {
		t.Fatalf("current hit should be reverse-video, got %q", cur)
	}
	if strings.Count(cur, "\x1b[7m") != 2 {
		t.Fatalf("both occurrences of 'o' should highlight, got %q", cur)
	}
	reL, _ := compileSearch("L")
	other := highlightLine("hello", reL, 20, false)
	if !strings.Contains(other, "\x1b[4ml\x1b[24m") {
		t.Fatalf("non-current hit should underline (case-insensitive), got %q", other)
	}
}

// TestHighlightLineRegexp wraps the span each regex match covers, not just a
// literal substring, and skips zero-width matches so a pattern like "a*" emits
// no stray styling.
func TestHighlightLineRegexp(t *testing.T) {
	reD, _ := compileSearch(`\d+`)
	got := highlightLine("err code 42 ok", reD, 20, true)
	if !strings.Contains(got, "\x1b[7m42\x1b[27m") {
		t.Fatalf(`\d+ should highlight "42", got %q`, got)
	}
	reStar, _ := compileSearch("x*")
	if zero := highlightLine("abc", reStar, 20, false); zero != "abc" {
		t.Fatalf("a zero-width match should leave the line untouched, got %q", zero)
	}
}

// TestCompileSearchFallsBackToLiteral proves a pattern that does not compile as a
// regexp is matched literally rather than erroring, and reports the fallback.
func TestCompileSearchFallsBackToLiteral(t *testing.T) {
	re, literal := compileSearch("foo[") // invalid regexp
	if re == nil {
		t.Fatal("compileSearch must never return nil")
	}
	if !literal {
		t.Fatal("an invalid pattern should report literal=true")
	}
	if !re.MatchString("a foo[ b") {
		t.Fatalf("an invalid pattern should match its literal text, re=%q", re.String())
	}
	if re.MatchString("foo only") {
		t.Fatal("the literal fallback should not match a partial of the metacharacter term")
	}
	if _, literal := compileSearch(`\d+`); literal {
		t.Fatal("a valid regexp should report literal=false")
	}
}

// TestSearchRegexp proves runSearch matches a real regular expression against the
// scrollback, not just a substring.
func TestSearchRegexp(t *testing.T) {
	m := searchModel(t, 30) // line0..line29
	m = m.runSearch("line1[5-9]")
	if !m.searchActive() {
		t.Fatal("a matching regexp should be active")
	}
	if len(m.searchHits) != 5 { // line15..line19
		t.Fatalf("line1[5-9] should match 5 lines, got %d", len(m.searchHits))
	}
}

// TestSearchBadRegexpStatus proves an invalid pattern still searches (literally)
// but the footer says it fell back rather than silently downgrading.
func TestSearchBadRegexpStatus(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	_, _ = emu.Write([]byte("a[b literal\r\n")) // a line that contains the metacharacter text
	m := model{emu: emu, mode: modeZoom, zoomID: "z", width: 20, height: 8}

	m = m.runSearch("a[b") // invalid regexp (unclosed class) → literal fallback, which matches the line
	if !m.searchActive() {
		t.Fatal("the literal fallback should still find the line")
	}
	if !strings.Contains(m.status, "bad regexp") {
		t.Fatalf("the footer should report the literal fallback, got %q", m.status)
	}
}

// searchModel builds a zoomed model over an emulator filled with n lines.
func searchModel(t *testing.T, n int) model {
	t.Helper()
	emu := vt.NewSafeEmulator(20, 4)
	fillLines(emu, n)
	return model{emu: emu, mode: modeZoom, zoomID: "z", width: 20, height: 8}
}

// TestSearchFindsAndNavigates proves a search lands on the newest hit, holds in
// scroll mode, and n/N walk older/newer matches.
func TestSearchFindsAndNavigates(t *testing.T) {
	m := searchModel(t, 30) // line0..line29

	m = m.runSearch("line1") // matches line1, line10..line19 → 11 hits
	if !m.searchActive() {
		t.Fatal("a matching search should be active")
	}
	if len(m.searchHits) != 11 {
		t.Fatalf("expected 11 hits for line1, got %d", len(m.searchHits))
	}
	if !m.scrolling || m.scrollOff <= 0 {
		t.Fatalf("search should hold in scroll mode scrolled back, scrolling=%v off=%d", m.scrolling, m.scrollOff)
	}
	if m.searchAt != len(m.searchHits)-1 {
		t.Fatalf("search should start on the newest hit, at=%d", m.searchAt)
	}

	// n steps to an older hit (a larger scroll offset).
	before := m.scrollOff
	next, _ := m.handleScrollKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = next.(model)
	if m.searchAt != len(m.searchHits)-2 || m.scrollOff <= before {
		t.Fatalf("n should walk to an older hit, at=%d off=%d (was %d)", m.searchAt, m.scrollOff, before)
	}

	// N steps back toward the newest hit.
	next, _ = m.handleScrollKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	m = next.(model)
	if m.searchAt != len(m.searchHits)-1 {
		t.Fatalf("N should walk back to the newer hit, at=%d", m.searchAt)
	}
}

// TestSearchHighlightsCurrent proves the rendered scroll window reverse-videos the
// current match.
func TestSearchHighlightsCurrent(t *testing.T) {
	m := searchModel(t, 30)
	m = m.runSearch("line2")
	window := strings.Join(m.searchWindow(m.emu, m.width, m.zoomRows(), m.scrollOff), "\n")
	if !strings.Contains(window, "\x1b[7m") {
		t.Fatalf("the current hit should be highlighted in the window, got:\n%q", window)
	}
}

// TestSearchNoMatch leaves the search inactive and reports it.
func TestSearchNoMatch(t *testing.T) {
	m := searchModel(t, 10)
	m = m.runSearch("zzz-nope")
	if m.searchActive() {
		t.Fatal("a non-matching search should not be active")
	}
	if !strings.Contains(m.status, "no match") {
		t.Fatalf("expected a no-match status, got %q", m.status)
	}
}

// TestSearchClearsOnExitScroll proves leaving scroll mode drops the search.
func TestSearchClearsOnExitScroll(t *testing.T) {
	m := searchModel(t, 30)
	m = m.runSearch("line1")
	if !m.searchActive() {
		t.Fatal("precondition: search active")
	}
	m = m.exitScroll()
	if m.searchActive() || m.searchQuery != "" {
		t.Fatalf("exiting scroll should clear the search, query=%q hits=%d", m.searchQuery, len(m.searchHits))
	}
}

// TestUpdateRoutesSearchPrompt proves the lifted input routing in Update: while a
// search prompt is open in a zoom, a real key event reaches handleInput (the text
// field) rather than handleZoomKey (the program). When no overlay is open, keys
// still reach the scroll handler.
func TestUpdateRoutesSearchPrompt(t *testing.T) {
	m := searchModel(t, 5)
	m.binds = append([]binding(nil), bindings...)
	m.prefixKey = "ctrl+t"
	m.input = inputSearch // a find prompt is open in the zoom

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = next.(model)
	if m.inputBuf != "x" {
		t.Fatalf("a key with the prompt open should type into the field, buf=%q", m.inputBuf)
	}

	// With no overlay open but scrolling, a key reaches the scroll handler.
	m.input = inputNone
	m.scrolling = true
	m.scrollOff = 0
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if next.(model).scrollOff != 1 {
		t.Fatalf("with no overlay, a key should reach the scroll handler, off=%d", next.(model).scrollOff)
	}
}

// TestSearchOpensFromZoom proves C-t f arms the find prompt in a zoom.
func TestSearchOpensFromZoom(t *testing.T) {
	m := searchModel(t, 5)
	m.binds = append([]binding(nil), bindings...)
	m.prefixKey = "ctrl+t"

	next, _ := m.handleZoomKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = next.(model)
	next, _ = m.handleZoomKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = next.(model)
	if m.input != inputSearch {
		t.Fatalf("C-t f should open the search prompt, input=%v", m.input)
	}
}
