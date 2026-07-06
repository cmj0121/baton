package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// TestSanitizeTextStripsOSC52 is the core security case: an OSC52 clipboard-write
// escape — the kind an agent can smuggle into a commit subject or a file's
// contents — must not survive into text the popups render to the real terminal.
// Before the fix these bytes flowed through clipVisible verbatim and would hijack
// the operator's clipboard.
func TestSanitizeTextStripsOSC52(t *testing.T) {
	evil := "feat: land it\x1b]52;c;ZXZpbA==\x07 done"
	got := sanitizeText(evil)
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("sanitizeText left an ESC in %q", got)
	}
	if strings.Contains(got, "52;c;") {
		t.Fatalf("sanitizeText left the OSC52 payload in %q", got)
	}
	if want := "feat: land it done"; got != want {
		t.Fatalf("sanitizeText(%q) = %q, want %q", evil, got, want)
	}
}

// TestSanitizeTextStripsLoneControls covers bare control bytes that never form an
// ESC sequence — a raw BEL, CR, backspace, DEL, and the single-byte C1 CSI/OSC
// introducers some terminals still honour.
func TestSanitizeTextStripsLoneControls(t *testing.T) {
	for _, s := range []string{"a\x07b", "a\rb", "a\x08b", "a\x7fb", "a\x9bb", "a\x9db"} {
		got := sanitizeText(s)
		if got != "ab" {
			t.Errorf("sanitizeText(%q) = %q, want \"ab\"", s, got)
		}
	}
}

// TestSanitizeTextKeepsPrintable makes sure ordinary content — including tabs and
// the box-drawing glyphs `git log --graph` draws — is left untouched.
func TestSanitizeTextKeepsPrintable(t *testing.T) {
	for _, s := range []string{"plain text", "col1\tcol2", "* │ ├─ commit", "münchen café"} {
		if got := sanitizeText(s); got != s {
			t.Errorf("sanitizeText(%q) = %q, want it unchanged", s, got)
		}
	}
}

// TestGitOutPopupSanitizes proves the fix at the ingestion point: opening the
// git-output popup with escape-laden git output stores only clean lines, so no
// downstream render can emit the escape.
func TestGitOutPopupSanitizes(t *testing.T) {
	m := model{}
	m = m.openGitOutPopup("git log", "abc1234 feat\x1b]52;c;cwn=\x07\ndef5678 fix", false)
	for _, l := range m.gitOutLines {
		if strings.ContainsRune(l, 0x1b) {
			t.Fatalf("gitOutLines retained an escape: %q", l)
		}
	}
}

// TestDiffPopupSanitizes proves the diff popup scrubs both the path and the diff
// bodies (a file's own bytes) before it holds them for rendering.
func TestDiffPopupSanitizes(t *testing.T) {
	m := model{height: 40, width: 120}
	files := []proto.DiffFile{{
		Path:     "eng\x1b]52;c;x\x07.go",
		Work:     "?",
		Unstaged: "new file: x\n+payload\x1b]52;c;ZXZpbA==\x07\n",
	}}
	m = m.openDiffPopup("diff", files)
	f := m.diffFiles[0]
	for _, field := range []string{f.Path, f.Staged, f.Unstaged} {
		if strings.ContainsRune(field, 0x1b) {
			t.Fatalf("diff field retained an escape: %q", field)
		}
	}
}
