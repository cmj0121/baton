package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDeleteLastWord(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"claude", ""},
		{"one two", "one "},
		{"trailing   ", ""},
		{"/Users/xrspace/junk", "/Users/xrspace/"}, // a path loses its last segment
		{"/Users/xrspace/", "/Users/"},             // a trailing slash is dropped first
		{"~/work/baton", "~/work/"},
	}
	for _, c := range cases {
		if got := deleteLastWord(c.in); got != c.want {
			t.Errorf("deleteLastWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCompletePath(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "alpha"))
	mustWrite(t, filepath.Join(dir, "alphabet.txt"))
	mustWrite(t, filepath.Join(dir, "beta.txt"))

	// A single match completes in full; a directory gains a trailing slash.
	if got, _ := completePath(filepath.Join(dir, "be")); got != filepath.Join(dir, "beta.txt") {
		t.Errorf("single match should complete fully, got %q", got)
	}
	if got, hint := completePath(filepath.Join(dir, "alph")); got != filepath.Join(dir, "alpha") {
		t.Errorf("several matches should complete to the common prefix, got %q (hint %q)", got, hint)
	} else if !strings.Contains(hint, "alpha/") || !strings.Contains(hint, "alphabet.txt") {
		t.Errorf("the hint should list both candidates, got %q", hint)
	}

	// No match leaves the text unchanged and says so.
	if got, hint := completePath(filepath.Join(dir, "zzz")); got != filepath.Join(dir, "zzz") || hint != "no match" {
		t.Errorf("no match should be a no-op, got %q hint %q", got, hint)
	}
	// A non-existent parent directory is reported, not panicked on.
	if _, hint := completePath(filepath.Join(dir, "nope", "x")); hint != "no such directory" {
		t.Errorf("a bad directory should be reported, hint %q", hint)
	}
}

// TestCompletePathTilde guards the home-relative inputs that used to panic:
// completing "~" or "~/" derived base from the home-EXPANDED path, which is not a
// suffix of the typed text, so `in[:len(in)-len(base)]` went negative and tore
// down the cockpit. Every tilde form must now return safely with the "~/" prefix
// preserved.
func TestCompletePathTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to complete against")
	}
	for _, in := range []string{"~", "~/"} {
		got, _ := completePath(in) // must not panic
		if !strings.HasPrefix(got, "~/") {
			t.Errorf("completePath(%q) = %q, want a ~/-prefixed result", in, got)
		}
	}
}

// TestInputTabAndCtrlB drives the overlay keys: tab completes a path input and
// Ctrl-B deletes a word, while a non-path overlay ignores tab.
func TestInputTabAndCtrlB(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "beta.txt"))

	m := baseModel()
	m.input = inputAgentDir
	m.inputBuf = filepath.Join(dir, "be")
	next, _ := m.handleInput(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.inputBuf != filepath.Join(dir, "beta.txt") {
		t.Fatalf("tab should complete the workdir path, got %q", m.inputBuf)
	}
	if m.inputHint == "" {
		t.Fatal("tab should leave a hint under the field")
	}

	next, _ = m.handleInput(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(model)
	if !strings.HasSuffix(m.inputBuf, string(os.PathSeparator)) {
		t.Fatalf("Ctrl-B should drop the last path segment, got %q", m.inputBuf)
	}
	if m.inputHint != "" {
		t.Fatal("an edit should clear the completion hint")
	}

	// A name overlay is not a path: tab is inert.
	m.input, m.inputBuf = inputGroupName, "wo"
	next, _ = m.handleInput(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.inputBuf != "wo" {
		t.Fatalf("tab should not complete a non-path overlay, got %q", m.inputBuf)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
