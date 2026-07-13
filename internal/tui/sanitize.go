package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/cmj0121/baton/internal/proto"
)

// sanitizeText neutralises terminal control sequences in UNTRUSTED text before the
// cockpit renders it to the real terminal. The diff and git-output popups show
// bytes that never passed through a panel's terminal emulator — raw `git log`
// output, a file's diff, a path — so any escape they carry is not legitimate
// styling but attacker-controlled: an agent can put an OSC52 clipboard-write or a
// cursor/screen escape into a commit subject, a branch name, or a file's contents,
// and it would otherwise flow straight through clipVisible/lipgloss to the
// operator's terminal. Everything the cockpit adds (colour, width) is applied by
// the renderer around this text, so stripping the embedded escapes here changes
// nothing legitimate.
//
// It drops each ESC-introduced sequence whole, plus any lone C0 control byte
// (except tab), DEL, and C1 controls — including the single-byte CSI/OSC
// introducers (0x9b/0x9d) some terminals still honour. Tabs are kept; callers
// split on newlines before this, so a newline never reaches it.
func sanitizeText(s string) string {
	if !needsSanitize(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // ESC: skip the whole escape sequence it introduces
			i += escLen(s[i:])
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// A stray, invalid byte (e.g. a raw 0x9b CSI introducer): drop it rather
			// than pass an undecodable control byte to the terminal.
			i++
			continue
		}
		if isControl(r) {
			i += size
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// needsSanitize reports whether s holds any byte the scrubber must act on: an ESC,
// any C0 control other than tab, DEL, or any high byte (0x7f+). Treating every
// high byte as "needs work" pulls valid multi-byte UTF-8 into the slow path too,
// but that path preserves it intact — so the result stays correct, only ASCII-only
// text takes the cheap return. A lone C1 introducer (0x9b/0x9d) is a high byte, so
// unlike a rune-level scan this never misses one.
func needsSanitize(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\t' {
			continue
		}
		if c < 0x20 || c >= 0x7f {
			return true
		}
	}
	return false
}

// isControl reports whether r is a control character the popups must not emit: a
// C0 control other than tab, DEL, or a C1 control. Ordinary printable runes
// (including box-drawing glyphs git's --graph uses) are kept.
func isControl(r rune) bool {
	switch {
	case r == '\t':
		return false
	case r < 0x20:
		return true
	case r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	default:
		return false
	}
}

// printableRunes drops the control and non-printable runes from a key event's
// runes, keeping only what a single-line text field may safely show. A bracketed
// paste arrives as one KeyRunes event whose runes can carry newlines, tabs, a raw
// ESC, or other control bytes; appended verbatim they render as stray glyphs in
// the field (and could smuggle an escape to the real terminal). Tab is a control
// here too — a field is one line, so it has no place in the buffer.
func printableRunes(rs []rune) string {
	var b strings.Builder
	b.Grow(len(rs))
	for _, r := range rs {
		if r == '\t' || isControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sanitizeLines applies sanitizeText to every line of a block of untrusted text.
func sanitizeLines(lines []string) []string {
	for i, l := range lines {
		lines[i] = sanitizeText(l)
	}
	return lines
}

// sanitizeDiffFiles scrubs the untrusted fields of a "diff" reply before the popup
// renders them: the path (a file name the agent chose) and both diff bodies (which
// carry a file's own bytes, including an untracked file rendered as added lines).
// The status letters are single-char git output, left as-is.
func sanitizeDiffFiles(files []proto.DiffFile) []proto.DiffFile {
	for i := range files {
		files[i].Path = sanitizeText(files[i].Path)
		files[i].Staged = sanitizeText(files[i].Staged)
		files[i].Unstaged = sanitizeText(files[i].Unstaged)
	}
	return files
}
